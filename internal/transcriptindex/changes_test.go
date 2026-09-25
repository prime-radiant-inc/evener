package transcriptindex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestScanOverACorruptEntryYieldsEntryErrorWithItsOrdinal(t *testing.T) {
	fx := everything()
	path, lines := writeHeaderOnly(t, fx)
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, joinLines(lines[:3]))
	catchUp(t, x)

	line := encodeEntry(t, 1, user("bad"))
	line = bytes.TrimRight(line, "\n")
	line = append(line[:len(line)-1], []byte(`,"unknown_field":true}`+"\n")...)
	appendBytes(t, path, line)

	err := x.CatchUp()
	var entryErr *EntryError
	if !errors.As(err, &entryErr) {
		t.Fatalf("CatchUp over a corrupt entry: err = %v, want *EntryError", err)
	}
	if entryErr.Ordinal != 3 {
		t.Fatalf("EntryError.Ordinal = %d, want 3", entryErr.Ordinal)
	}
	if entryErr.Unwrap() == nil {
		t.Fatal("EntryError.Unwrap() = nil")
	}
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
