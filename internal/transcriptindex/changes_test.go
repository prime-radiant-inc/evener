package transcriptindex

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// TestChangedSinceReturnsCreatedAndUpdatedRecords requires ChangedSince(L) to
// return exactly the items and turns whose record an entry at or past L
// created or updated in place, each once: items in position order, turns in
// the stable order their first entry created them.
func TestChangedSinceReturnsCreatedAndUpdatedRecords(t *testing.T) {
	fx := everything()
	header, lines := fx.encode(t)
	for cut := range len(lines) + 1 {
		path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
		if err := os.WriteFile(path, append(append([]byte(nil), header...), joinLines(lines[:cut])...), 0o600); err != nil {
			t.Fatal(err)
		}
		x := openIndex(t, path, t.TempDir())
		held, err := x.Latest(1)
		if err != nil {
			t.Fatal(err)
		}
		beforeProjection := referenceProjection(t, path)
		beforeItems, beforeAllTurns := candidatesOf(beforeProjection), allTurns(beforeProjection)
		appendBytes(t, path, joinLines(lines[cut:]))
		catchUp(t, x)
		afterProjection := referenceProjection(t, path)
		afterItems, afterAllTurns := candidatesOf(afterProjection), allTurns(afterProjection)

		changes, err := x.ChangedSince(held.Length)
		if err != nil {
			t.Fatal(err)
		}

		beforeItemAt := map[appwire.ThreadItemPosition]appitempaging.TranscriptItemCandidate{}
		for _, c := range beforeItems {
			beforeItemAt[c.Position] = c
		}
		var wantItems []appitempaging.TranscriptItemCandidate
		for _, c := range afterItems {
			old, existed := beforeItemAt[c.Position]
			if !existed || !reflect.DeepEqual(old.Item, c.Item) {
				wantItems = append(wantItems, c)
			}
		}
		if !reflect.DeepEqual(changes.Items, wantItems) {
			t.Fatalf("cut %d: ChangedSince items\n got: %s\nwant: %s", cut, dump(changes.Items), dump(wantItems))
		}

		var wantTurns []appwire.Turn
		for i, now := range afterAllTurns {
			if i >= len(beforeAllTurns) || !reflect.DeepEqual(turnScalars(beforeAllTurns[i]), turnScalars(now)) {
				wantTurns = append(wantTurns, turnScalars(now))
			}
		}
		var gotTurns []appwire.Turn
		for _, turn := range changes.Turns {
			gotTurns = append(gotTurns, turnScalars(turn))
		}
		if !reflect.DeepEqual(gotTurns, wantTurns) {
			t.Fatalf("cut %d: ChangedSince turns\n got: %s\nwant: %s", cut, dump(gotTurns), dump(wantTurns))
		}
		if err := x.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// deliberatelyUnreadable is the field corruptEntryLine adds: no entry has it,
// so the line fails strict decoding, and readFixtureFile accepts the failure
// only on a line that carries it.
const deliberatelyUnreadable = `"deliberately_unreadable":true`

// corruptEntryLine is an entry line that fails strict decoding: it carries a
// field no entry has.
func corruptEntryLine(t testing.TB, text string) []byte {
	t.Helper()
	line := bytes.TrimRight(encodeEntry(t, 1, user(text)), "\n")
	return append(line[:len(line)-1], []byte(","+deliberatelyUnreadable+"}\n")...)
}

// An entry that fails to decode is quarantined: it projects as one visible
// unreadable-entry item naming its ordinal, in a completed turn of its own,
// and the entries after it project as usual, whether the index extends over
// it or builds over it.
func TestAnUnreadableEntryIsQuarantined(t *testing.T) {
	fx := everything()
	path, lines := writeHeaderOnly(t, fx)
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, joinLines(lines[:3]))
	catchUp(t, x)
	appendBytes(t, path, corruptEntryLine(t, "bad"))
	appendBytes(t, path, encodeEntry(t, 5, user("after the bad entry")))
	catchUp(t, x)

	check := func(label string, x *Index) {
		t.Helper()
		window, err := x.Latest(appwire.TranscriptItemPageLimit)
		if err != nil {
			t.Fatal(err)
		}
		var unreadable, after *appitempaging.TranscriptItemCandidate
		for i, c := range window.Candidates {
			switch {
			case c.Item.EventKind == appwire.ThreadItemEventKindError:
				unreadable = &window.Candidates[i]
			case c.Item.Text == "after the bad entry":
				after = &window.Candidates[i]
			}
		}
		if unreadable == nil || unreadable.Item.Type != "systemMessage" || !strings.Contains(unreadable.Item.Text, "transcript entry 3") ||
			*unreadable.Item.Position != (appwire.ThreadItemPosition{Entry: 4}) || unreadable.Item.Version != 4 ||
			unreadable.Turn.Status != appwire.TurnStatusCompleted || unreadable.Item.TranscriptKey == "" {
			t.Fatalf("%s: unreadable entry item = %s, want a completed systemMessage naming entry 3 at its position", label, dump(unreadable))
		}
		if after == nil || after.Item.Version != 5 {
			t.Fatalf("%s: the entry after the unreadable one = %s, want it projected", label, dump(after))
		}
	}
	check("extended", x)
	built := openIndex(t, path, t.TempDir())
	check("built", built)
}

func TestRebuildMintsANewIncarnation(t *testing.T) {
	fx := everything()
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	before, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Rebuild(before.Length); err != nil {
		t.Fatal(err)
	}
	after, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Incarnation == before.Incarnation {
		t.Fatalf("Rebuild kept the incarnation %q", before.Incarnation)
	}
	assertAllWindows(t, x, path)
}

func TestDirForIsTheTranscriptPathWithIndexSuffix(t *testing.T) {
	if got, want := DirFor("/a/b/session.transcript.jsonl"), "/a/b/session.transcript.jsonl.index"; got != want {
		t.Fatalf("DirFor = %q, want %q", got, want)
	}
}

// An unreadable entry closes the legacy group before it: a legacy entry after
// it opens a turn of its own rather than joining a turn whose items sit
// before the unreadable entry's.
func TestAnUnreadableEntryClosesTheLegacyGroup(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "unreadable entry"), -1)
	turnOfText := map[string]string{}
	for _, c := range candidates {
		turnOfText[c.Item.Text] = c.TurnID
	}
	if before, after := turnOfText["legacy before"], turnOfText["legacy after"]; before == "" || after == "" || before == after {
		t.Fatalf("legacy entries around the unreadable one in turns %q and %q, want two turns", before, after)
	}
}

// TestLatestSinceReadsOneSnapshot requires LatestSince to answer as Latest and
// ChangedSince do on the same snapshot, and to answer no changes when the held
// snapshot names another incarnation or predates the kept update log: the
// caller then sends a full latest-window replacement.
func TestLatestSinceReadsOneSnapshot(t *testing.T) {
	previous := updateLogRecords
	updateLogRecords = 4
	t.Cleanup(func() { updateLogRecords = previous })
	path, lines := writeHeaderOnly(t, everything())
	x := openIndex(t, path, t.TempDir())
	var held []appwire.SnapshotIdentity
	for _, line := range lines {
		appendBytes(t, path, line)
		catchUp(t, x)
		held = append(held, appwire.SnapshotIdentity{Incarnation: x.meta.Incarnation, Length: x.meta.Length})
	}
	recent := held[len(held)-3]
	window, changes, err := x.LatestSince(2, recent)
	if err != nil {
		t.Fatal(err)
	}
	wantWindow, err := x.Latest(2)
	if err != nil {
		t.Fatal(err)
	}
	wantChanges, err := x.ChangedSince(recent.Length)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(window, wantWindow) || changes == nil || !reflect.DeepEqual(*changes, wantChanges) {
		t.Fatalf("LatestSince = %s, %s\nwant %s, %s", dump(window), dump(changes), dump(wantWindow), dump(wantChanges))
	}
	for name, snapshot := range map[string]appwire.SnapshotIdentity{
		"another incarnation": {Incarnation: "another", Length: recent.Length},
		"before the kept log": held[0],
	} {
		window, changes, err := x.LatestSince(2, snapshot)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(window, wantWindow) || changes != nil {
			t.Fatalf("%s: LatestSince = %s, %s, want the window and no changes", name, dump(window), dump(changes))
		}
	}
}
