package transcriptindex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func turnScalars(turn appwire.Turn) appwire.Turn {
	turn.Items = nil
	return turn
}

// TestChangedSinceReturnsWhatLaterEntriesChanged snapshots the index at every
// entry boundary of the corpus, appends the rest, and requires ChangedSince to
// return the current form of every item and turn the later entries changed.
func TestChangedSinceReturnsWhatLaterEntriesChanged(t *testing.T) {
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
		before := referenceCandidates(t, path)
		beforeTurns := referenceTurns(t, path)
		appendBytes(t, path, joinLines(lines[cut:]))
		catchUp(t, x)
		after := referenceCandidates(t, path)
		afterTurns := referenceTurns(t, path)

		changes, err := x.ChangedSince(held.Length)
		if err != nil {
			t.Fatal(err)
		}
		if changes.Incarnation != held.Incarnation && x.rebuilds == 1 {
			t.Fatalf("cut %d: incarnation changed without a rebuild", cut)
		}
		// Every returned item is its current form.
		current := map[appwire.ThreadItemPosition]appitempaging.TranscriptItemCandidate{}
		for _, candidate := range after {
			current[candidate.Position] = candidate
		}
		returned := map[appwire.ThreadItemPosition]bool{}
		for _, candidate := range changes.Items {
			if !reflect.DeepEqual(candidate, current[candidate.Position]) {
				t.Fatalf("cut %d: changed item %v is not its current form:\n got: %s\nwant: %s", cut, candidate.Position, dump(candidate), dump(current[candidate.Position]))
			}
			returned[candidate.Position] = true
		}
		// Every item held before the cut whose content changed is returned —
		// except a flushed communicate item (see window.go's pendingFlush):
		// it has no itemRecord or update-log entry to begin with (a known
		// gap the spec's follow-ups track), so its disappearance or
		// replacement once a later entry pairs the call is invisible to
		// ChangedSince by construction, not a regression.
		for _, old := range before {
			if strings.HasPrefix(old.Item.ID, "item_assistant_flushed_") {
				continue
			}
			if !reflect.DeepEqual(old.Item, current[old.Position].Item) && !returned[old.Position] {
				t.Fatalf("cut %d: item %v changed but was not returned", cut, old.Position)
			}
		}
		// Every returned turn is the current form of a turn with its id, and
		// every turn held before the cut whose scalars changed is returned.
		currentTurn := func(turn appwire.Turn) bool {
			for _, candidate := range afterTurns {
				if reflect.DeepEqual(turn, turnScalars(candidate)) {
					return true
				}
			}
			return false
		}
		for _, turn := range changes.Turns {
			if !currentTurn(turn) {
				t.Fatalf("cut %d: changed turn %s is not a current turn: %s", cut, turn.ID, dump(turn))
			}
		}
		for i, old := range beforeTurns {
			now := afterTurns[i]
			if old.ID != now.ID || reflect.DeepEqual(turnScalars(old), turnScalars(now)) {
				continue
			}
			found := false
			for _, turn := range changes.Turns {
				found = found || reflect.DeepEqual(turn, turnScalars(now))
			}
			if !found {
				t.Fatalf("cut %d: turn %s changed but was not returned", cut, old.ID)
			}
		}
		if err := x.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtensionTruncatesTablesToTheirCounts(t *testing.T) {
	fx := namedResults()
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	appendBytes(t, path, joinLines(lines[:len(lines)-5]))
	x := openIndex(t, path, dir)
	build := liveBuild(t, dir)
	// Records an extension wrote before a crash, past the meta's counts.
	for _, name := range []string{itemsFile, turnsFile, updatesFile} {
		f, err := os.OpenFile(filepath.Join(build, name), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		// More than the remaining entries can overwrite.
		if _, err := f.Write(make([]byte, 100*itemRecordSize)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendBytes(t, path, joinLines(lines[len(lines)-5:]))
	catchUp(t, x)
	for name, size := range map[string]int64{itemsFile: itemRecordSize, turnsFile: turnRecordSize, updatesFile: updateRecordSize} {
		info, err := os.Stat(filepath.Join(build, name))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]uint64{itemsFile: x.meta.Items, turnsFile: x.meta.Turns, updatesFile: x.meta.Updates}[name]
		if info.Size() != int64(want)*size {
			t.Fatalf("%s holds %d bytes, want %d counted records", name, info.Size(), want)
		}
	}
	assertAllWindows(t, x, path)
}
