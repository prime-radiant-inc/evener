package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newRecordWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterNoSync(path, sharedFileHeader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func TestRecordReportsOrdinalSeqAndRecordedLength(t *testing.T) {
	w, path := newRecordWriter(t)
	headerLen := w.RecordedLength()
	if headerLen <= 0 {
		t.Fatalf("a new transcript's recorded length is %d, want its header line", headerLen)
	}
	first, err := w.Record(steeringTurn("one"), RecordOptions{Door: DoorDurable})
	if err != nil || !first.Recorded || first.Ordinal != 0 || first.Seq != 0 || first.Offset != headerLen || first.Length <= 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := w.Record(steeringTurn("two"), RecordOptions{Door: DoorBuffered})
	if err != nil || !second.Recorded || second.Ordinal != 1 || second.Seq != 1 || second.Offset != first.Offset+first.Length {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if second.Turn.Message.Text() != "two" {
		t.Fatalf("record carries turn %+v", second.Turn)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if w.RecordedLength() != info.Size() {
		t.Fatalf("recorded length %d, file %d", w.RecordedLength(), info.Size())
	}

	// A writer resumed on the same file, while this one is open, continues
	// the ordinal and the recorded length from the shared tail.
	resumed := openSharedFileWriter(t, path)
	defer resumed.Close() //nolint:errcheck // fixture
	third, err := resumed.Record(steeringTurn("three"), RecordOptions{Door: DoorSynced})
	if err != nil || third.Ordinal != 2 || third.Seq != 2 || third.Offset != info.Size() {
		t.Fatalf("third = %+v, %v", third, err)
	}
	if w.RecordedLength() != third.Offset+third.Length || resumed.RecordedLength() != w.RecordedLength() {
		t.Fatalf("writers disagree on the recorded length: %d, %d", w.RecordedLength(), resumed.RecordedLength())
	}
}

func TestRecordOrdinalAfterResumeCountsEntryLines(t *testing.T) {
	path := newSharedFileTranscript(t) // header plus one entry, writer closed
	w := openSharedFileWriter(t, path)
	defer w.Close() //nolint:errcheck // fixture
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if w.RecordedLength() != info.Size() {
		t.Fatalf("resumed recorded length %d, file %d", w.RecordedLength(), info.Size())
	}
	rec, err := w.Record(steeringTurn("after resume"), RecordOptions{Door: DoorDurable})
	if err != nil || rec.Ordinal != 1 {
		t.Fatalf("record after resume = %+v, %v", rec, err)
	}
}

func TestRecordOnMissingOrClosedWriterIsNotRecorded(t *testing.T) {
	var missing *Writer
	if rec, err := missing.Record(steeringTurn("x"), RecordOptions{Door: DoorDurable}); err != nil || rec.Recorded {
		t.Fatalf("nil writer: %+v, %v", rec, err)
	}
	if missing.RecordedLength() != 0 {
		t.Fatal("a nil writer reports a recorded length")
	}
	w, _ := newRecordWriter(t)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, door := range []Door{DoorBuffered, DoorDurable} {
		if rec, err := w.Record(steeringTurn("x"), RecordOptions{Door: door}); err != nil || rec.Recorded {
			t.Fatalf("closed writer, door %d: %+v, %v", door, rec, err)
		}
	}
	if rec, err := w.Record(steeringTurn("x"), RecordOptions{Door: DoorSynced}); !errors.Is(err, ErrWriterClosed) || rec.Recorded {
		t.Fatalf("closed writer, synced: %+v, %v", rec, err)
	}
}
