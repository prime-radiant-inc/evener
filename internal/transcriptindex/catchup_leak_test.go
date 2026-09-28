package transcriptindex

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// firstInPlaceUpdatePair finds the first turn in fx's lines with at least two
// entries: firstIdx is its first entry (the one that creates the turn's
// record), secondIdx is its next entry (the one that updates that record in
// place and logs an updatedTurn record). It requires no unreadable/blank
// lines up to secondIdx, so the byte lengths computed from the encoded lines
// line up exactly with entry boundaries.
func firstInPlaceUpdatePair(t testing.TB, fx fixture) (firstIdx, secondIdx int) {
	t.Helper()
	seen := map[string]int{}
	for i, line := range fx.lines {
		if line.blank || line.unreadable != "" || line.turn.TurnID == "" {
			continue
		}
		if first, ok := seen[line.turn.TurnID]; ok {
			return first, i
		}
		seen[line.turn.TurnID] = i
	}
	t.Fatal("fixture has no turn with two entries")
	return 0, 0
}

// TestCatchUpToShorterThanAKilledExtensionsReachRedoesTheLeftoverUpdate pins
// the "CatchUpTo's truncate-first extension can leak in-place updates past
// the requested length" known gap. Two handles share one sidecar, as a hub
// process and a daemon process do: one (hub) extends all the way to the
// current file size and is killed after writing its records but before
// committing meta, leaving an in-place update on disk (an existing,
// already-committed turn record overwritten) with no update-log row to
// announce it. The other (daemon), already open from before the append and
// still holding its own, unpolluted in-memory state, then calls CatchUpTo
// for a shorter length that never reaches the entry causing that update.
// That call must not answer as if it covers only the shorter length while
// the turn record it returns already reflects the update: Latest/Before must
// not return content beyond Window.Length, and ChangedSince must not omit
// the change.
func TestCatchUpToShorterThanAKilledExtensionsReachRedoesTheLeftoverUpdate(t *testing.T) {
	fx := everything()
	header, lines := fx.encode(t)
	firstIdx, secondIdx := firstInPlaceUpdatePair(t, fx)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// Commit through the turn's first entry: a real, uninterrupted build.
	for i := 0; i <= firstIdx; i++ {
		appendBytes(t, path, lines[i])
	}
	hub := openIndex(t, path, dir)
	floor := hub.meta.Length
	if hub.meta.PendingCommunicate {
		t.Skip("fixture's first pending-communicate turn cannot exercise this without also rebuilding")
	}

	// daemon opens now, at the same floor, so its in-memory state is honest
	// and untouched by what hub does next.
	daemon := openIndex(t, path, dir)
	if daemon.meta.Length != floor {
		t.Fatalf("daemon's handle opened at %d, want the floor %d", daemon.meta.Length, floor)
	}

	// reference opens now too, in its own sidecar, also at the floor: Open
	// always catches up to the transcript's current size, so it must open
	// before the rest is appended, the same as daemon, to answer for
	// shortOfSecond uninterrupted rather than for whatever the file grows to.
	reference := openIndex(t, path, filepath.Join(t.TempDir(), "reference"))
	if reference.meta.Length != floor {
		t.Fatalf("reference's handle opened at %d, want the floor %d", reference.meta.Length, floor)
	}

	// The second entry's byte offset: a CatchUpTo for anything short of it
	// must not observe its in-place update.
	shortOfSecond := int64(len(header))
	for i := 0; i <= secondIdx-1; i++ {
		shortOfSecond += int64(len(lines[i]))
	}
	if shortOfSecond <= floor {
		t.Fatalf("second entry's offset %d does not exceed the committed floor %d", shortOfSecond, floor)
	}

	// Append through the rest of the fixture, then have hub extend all the
	// way to the new file size and kill it after it writes its records but
	// before it commits meta: the update-log row for the in-place update, and
	// any table rows appended past the old committed counts, are left on
	// disk uncounted, but the in-place overwrite of the turn's existing,
	// already-committed record already landed.
	for i := firstIdx + 1; i < len(lines); i++ {
		appendBytes(t, path, lines[i])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full := info.Size()
	simulateKill(t)
	if err := hub.CatchUpTo(full); err == nil {
		t.Fatal("the killed extension returned success")
	}
	testKillAfterRecordWrites = nil

	// daemon, still holding its own pre-kill in-memory state, now asks for
	// shortOfSecond: short of hub's crashed reach, and short of the entry
	// causing the in-place update.
	if err := daemon.CatchUpTo(shortOfSecond); err != nil {
		t.Fatal(err)
	}
	if daemon.meta.Length > shortOfSecond {
		t.Fatalf("daemon's index covers %d, want at most the requested %d", daemon.meta.Length, shortOfSecond)
	}

	if err := reference.CatchUpTo(shortOfSecond); err != nil {
		t.Fatal(err)
	}

	targetTurnID := fx.lines[firstIdx].turn.TurnID
	findTurn := func(x *Index) appwire.Turn {
		t.Helper()
		changes, err := x.ChangedSince(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range changes.Turns {
			if turn.ID == targetTurnID {
				return turn
			}
		}
		t.Fatalf("target turn %q not found", targetTurnID)
		return appwire.Turn{}
	}
	got, want := findTurn(daemon), findTurn(reference)
	if got.Version != want.Version || got.Status != want.Status {
		t.Fatalf("daemon's target turn = status %v version %d, want the reference's (never touched by the killed extension's reach past shortOfSecond) status %v version %d",
			got.Status, got.Version, want.Status, want.Version)
	}
}

// TestAddContributorLogsBeforeOverwritingSoAKillLeavesTheCommittedItemUntouched
// pins the ordering fix for the roborev finding on this PR's CatchUpTo fix:
// addContributor used to overwrite a tool call item's record in place and
// only log that update afterward, so a kill between the write and the log
// left the committed slot already changed with no leftover update-log row
// to make unsafeLeftoverUpdate notice it -- any reader sharing the sidecar
// (regardless of what length it has itself caught up to, since a table read
// goes straight to the slot on disk) would see the call completed before
// its own covered length admits the completing entry exists. addContributor
// now logs first, matching stampTurn: a kill between the two
// (testKillAfterItemUpdateLog) leaves a leftover row with the item's
// committed slot still genuinely unmodified, so a second handle that never
// even asks to extend past the call entry reads it exactly as a reference
// that was never interrupted does.
func TestAddContributorLogsBeforeOverwritingSoAKillLeavesTheCommittedItemUntouched(t *testing.T) {
	header := transcript.Header{SessionID: "kill_item", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	lines := [][]byte{
		encodeEntry(t, 1, assistant(call("call_1", "shell", `{"cmd":"ls"}`))),
		encodeEntry(t, 2, results(result("call_1", "shell", "a.txt"))),
	}

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, encodeHeader(t, header), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	appendBytes(t, path, lines[0])
	hub := openIndex(t, path, dir)
	floor := hub.meta.Length
	if floor == 0 {
		t.Fatal("hub did not cover the call entry")
	}

	// daemon and reference open now, at the same floor, before the result
	// entry (and the kill) land. Neither ever asks to extend past floor:
	// the point is that a reader who never requested the completing entry
	// must not see its effect anyway, just from reading the shared slot.
	daemon := openIndex(t, path, dir)
	reference := openIndex(t, path, filepath.Join(t.TempDir(), "reference"))

	appendBytes(t, path, lines[1])
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full := info.Size()

	testKillAfterItemUpdateLog = func() error {
		return errors.New("killed between the update-log row and the item overwrite")
	}
	t.Cleanup(func() { testKillAfterItemUpdateLog = nil })
	if err := hub.CatchUpTo(full); err == nil {
		t.Fatal("the killed extension returned success")
	}
	testKillAfterItemUpdateLog = nil

	got, err := daemon.ChangedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.ChangedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	got.Incarnation, want.Incarnation = "", ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("daemon after the kill (never asked past floor %d) = %s, want the reference (never interrupted) %s", floor, dump(got), dump(want))
	}
}
