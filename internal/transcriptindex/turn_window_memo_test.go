package transcriptindex

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// turnWindowFixture writes four single-entry turns and returns the transcript
// path with the turn IDs in order.
func turnWindowFixture(t *testing.T) (string, []string) {
	t.Helper()
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	lines := make([]fixtureLine, 0, 4)
	ids := make([]string, 0, 4)
	for i := range 4 {
		id := "t" + strconv.Itoa(i)
		ids = append(ids, id)
		lines = append(lines, entryLine(opens(id, schema.TurnSpanExecution, user("q"))))
	}
	fx := fixture{name: "turn-window", header: header, lines: lines}
	fxHeader, encoded := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(fxHeader, joinLines(encoded)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, ids
}

// TestTurnWindowOrderMemoMatchesAFullRebuild pins the memoized turn order
// against the order a freshly opened handle builds from the same records, and
// requires a repeated read not to rebuild it.
func TestTurnWindowOrderMemoMatchesAFullRebuild(t *testing.T) {
	path, ids := turnWindowFixture(t)
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	defer x.Close() //nolint:errcheck // read-only test handle

	selector := appwire.ThreadTurnWindow{Turn: ids[1]}
	first, err := x.ResolveTurnWindow(selector)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if walked := x.turnOrderWalks; walked != 1 {
		t.Fatalf("walks after the first resolve = %d, want 1", walked)
	}
	second, err := x.ResolveTurnWindow(selector)
	if err != nil {
		t.Fatalf("repeated resolve: %v", err)
	}
	if walked := x.turnOrderWalks; walked != 1 {
		t.Fatalf("walks after a repeated resolve = %d, want 1", walked)
	}

	// A fresh handle builds the order from the records, so it is the full
	// recomputation the memo must agree with.
	fresh := openIndex(t, path, dir)
	defer fresh.Close() //nolint:errcheck // read-only test handle
	want, err := fresh.ResolveTurnWindow(selector)
	if err != nil {
		t.Fatalf("fresh resolve: %v", err)
	}
	if len(first.TurnIDs) != len(want.TurnIDs) {
		t.Fatalf("memoized turn IDs = %v, rebuilt = %v", first.TurnIDs, want.TurnIDs)
	}
	for i := range first.TurnIDs {
		if first.TurnIDs[i] != want.TurnIDs[i] {
			t.Fatalf("memoized turn IDs = %v, rebuilt = %v", first.TurnIDs, want.TurnIDs)
		}
	}
	if first.Frontier != want.Frontier {
		t.Fatalf("memoized frontier = %+v, rebuilt = %+v", first.Frontier, want.Frontier)
	}
	for i := range second.TurnIDs {
		if second.TurnIDs[i] != want.TurnIDs[i] {
			t.Fatalf("repeated turn IDs = %v, rebuilt = %v", second.TurnIDs, want.TurnIDs)
		}
	}
}

// TestTurnWindowOrderMemoSeesAnAppend requires a record change to invalidate the
// memo: the order is a function of the records, and an appended turn belongs in
// it.
func TestTurnWindowOrderMemoSeesAnAppend(t *testing.T) {
	path, ids := turnWindowFixture(t)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	selector := appwire.ThreadTurnWindow{Turn: ids[0]}
	if _, err := x.ResolveTurnWindow(selector); err != nil {
		t.Fatalf("resolve before append: %v", err)
	}
	before := x.turnOrderWalks

	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	fx := fixture{name: "turn-window-append", header: header, lines: []fixtureLine{
		entryLine(opens("t9", schema.TurnSpanExecution, user("q"))),
	}}
	_, encoded := fx.encode(t)
	appendBytes(t, path, joinLines(encoded))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUpTo(info.Size()); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	resolved, err := x.ResolveTurnWindow(selector)
	if err != nil {
		t.Fatalf("resolve after append: %v", err)
	}
	if x.turnOrderWalks == before {
		t.Fatalf("walks stayed %d across an append: the memo outlived the records", before)
	}
	if resolved.Frontier.NewestTurnID != "t9" {
		t.Fatalf("newest turn = %q, want t9", resolved.Frontier.NewestTurnID)
	}
}

// TestTurnWindowOrderMemoSurvivesAcrossObservedReads pins the memo where
// production keeps it: ReadObserved wraps the handle in a fresh view per
// request, so a memo stored on the view would be discarded before the next
// request could reuse it.
func TestTurnWindowOrderMemoSurvivesAcrossObservedReads(t *testing.T) {
	path, ids := turnWindowFixture(t)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	selector := appwire.ThreadTurnWindow{Turn: ids[2]}
	read := func() []string {
		t.Helper()
		var got []string
		if err := x.ReadObserved(func() time.Time { return fixtureClock }, func(s *Snapshot, _ time.Time) error {
			resolved, err := s.ResolveTurnWindow(selector)
			got = resolved.TurnIDs
			return err
		}); err != nil {
			t.Fatalf("ReadObserved: %v", err)
		}
		return got
	}
	if got := read(); len(got) == 0 {
		t.Fatal("first observed read resolved no turns")
	}
	if walked := x.turnOrderWalks; walked != 1 {
		t.Fatalf("walks after the first observed read = %d, want 1", walked)
	}
	read()
	if walked := x.turnOrderWalks; walked != 1 {
		t.Fatalf("walks after a repeated observed read = %d, want 1: the memo did not survive the per-request view", walked)
	}
}
