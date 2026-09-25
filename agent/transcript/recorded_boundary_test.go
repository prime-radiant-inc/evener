package transcript

import (
	"path/filepath"
	"testing"
)

// AtRecordedBoundary runs its func under the file's append lock, with the
// recorded length every writer on the file shares: no append can record
// between what the func sees and what it does.
func TestAtRecordedBoundaryRunsUnderTheAppendLock(t *testing.T) {
	path := newSharedFileTranscript(t)
	w := openSharedFileWriter(t, path)
	defer w.Close() //nolint:errcheck // fixture
	rec, err := w.Record(steeringTurn("one"), RecordOptions{Place: PlaceAsync})
	if err != nil || !rec.Recorded {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	var seen int64
	held := false
	found, err := AtRecordedBoundary(path, func(recordedLength int64) {
		seen = recordedLength
		if w.tail.mu.TryLock() {
			w.tail.mu.Unlock()
		} else {
			held = true
		}
	})
	if err != nil || !found {
		t.Fatalf("AtRecordedBoundary = %v, %v; want the open writer's tail", found, err)
	}
	if seen != rec.Offset+rec.Length || seen != w.RecordedLength() {
		t.Fatalf("recorded length at the boundary = %d, want %d", seen, rec.Offset+rec.Length)
	}
	if !held {
		t.Fatal("the func ran without the append lock held")
	}
}

// A file no writer in this process has open has no in-process boundary:
// nothing in this process can append to it.
func TestAtRecordedBoundaryWithNoOpenWriter(t *testing.T) {
	path := newSharedFileTranscript(t)
	called := false
	found, err := AtRecordedBoundary(path, func(int64) { called = true })
	if err != nil || found || called {
		t.Fatalf("AtRecordedBoundary on a closed file = %v, %v (called %v); want not found", found, err, called)
	}
	if _, err := AtRecordedBoundary(filepath.Join(t.TempDir(), "missing.jsonl"), func(int64) {}); err == nil {
		t.Fatal("a missing transcript reported no error")
	}
}
