package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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

func TestRetractToBoundaryTakesBackTheLinesWrittenSinceIt(t *testing.T) {
	t.Parallel()
	w, path := newRecordWriter(t)
	var seen []Record
	w.OnRecorded(func(rec Record) { seen = append(seen, rec) })
	kept, err := w.Record(steeringTurn("kept"), RecordOptions{Door: DoorDurable})
	if err != nil || !kept.Recorded {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
	if len(seen) != 1 || seen[0].Ordinal != kept.Ordinal {
		t.Fatalf("recorded hook before any boundary = %+v, want the kept entry", seen)
	}
	boundary := w.CaptureCommitBoundary()
	if boundary.length != kept.Offset+kept.Length || boundary.ordinal != kept.Ordinal+1 {
		t.Fatalf("boundary = %+v, want the end of %+v", boundary, kept)
	}
	var spent int
	for _, text := range []string{"transient", "transact"} {
		rec, err := w.Record(steeringTurn(text), RecordOptions{Door: DoorDurable})
		if err != nil || !rec.Recorded {
			t.Fatalf("%s = %+v, %v", text, rec, err)
		}
		spent = rec.Seq
	}
	if len(seen) != 1 {
		t.Fatalf("recorded hook fired for %d entries under a captured boundary, want none: a held line must not reach any consumer", len(seen)-1)
	}
	if removed, retractErr := w.RetractToBoundary(boundary); !removed || retractErr != nil {
		t.Fatalf("retract = %v, %v", removed, retractErr)
	}
	if len(seen) != 1 {
		t.Fatalf("recorded hook fired for retracted entries: %d extra deliveries", len(seen)-1)
	}
	if w.RecordedLength() != boundary.length {
		t.Fatalf("recorded length after retract = %d, want the boundary %d", w.RecordedLength(), boundary.length)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != boundary.length {
		t.Fatalf("file holds %d bytes after retract, want the boundary %d: the lines written since it must be gone", info.Size(), boundary.length)
	}
	// Retracting to the same boundary again removes nothing and reports it.
	if removed, retractErr := w.RetractToBoundary(boundary); !removed || retractErr != nil {
		t.Fatalf("second retract = %v, %v", removed, retractErr)
	}
	// The next append takes the boundary's ordinal back, so an entry's ordinal
	// stays its index among the file's entry lines, and its offset is the
	// boundary; the sequence numbers the retracted lines spent stay spent.
	next, err := w.Record(steeringTurn("after retract"), RecordOptions{Door: DoorDurable})
	if err != nil || !next.Recorded {
		t.Fatalf("next = %+v, %v", next, err)
	}
	if next.Ordinal != boundary.ordinal || next.Offset != boundary.length {
		t.Fatalf("next append = ordinal %d offset %d, want ordinal %d offset %d: the boundary state must be restored", next.Ordinal, next.Offset, boundary.ordinal, boundary.length)
	}
	if next.Seq <= spent {
		t.Fatalf("next append reuses sequence number %d after %d was spent", next.Seq, spent)
	}
	if len(seen) != 2 || seen[1].Ordinal != next.Ordinal {
		t.Fatalf("recorded hook after the boundary ended = %+v, want the settled entry", seen)
	}
}

// A fresh execution's first recorded entry can be a fold's compaction
// marker, written while the execution has recorded nothing of its own — an
// attention-only wake folds before the model call records anything. That
// write is what turns running.fresh false, so a retract taken after it must
// still restore the placement the boundary captured: the execution itself is
// unchanged, and its first surviving entry must carry the span kind the
// retracted entry stamped and took with it.
func TestRetractRestoresAFreshExecutionsFirstRecord(t *testing.T) {
	t.Parallel()
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m9", false)
	boundary := w.CaptureCommitBoundary()
	marker := schema.NewTurn(schema.TurnCheckpoint, llm.Message{Role: llm.RoleUser})
	if got := recordPlaced(t, w, marker, PlaceSession); got.TurnID != "turn_m9" || got.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("the execution's first record = %+v, want it stamped into turn_m9", got)
	}
	removed, retractErr := w.RetractToBoundary(boundary)
	if !removed || retractErr != nil {
		t.Fatalf("retract = %v, %v", removed, retractErr)
	}
	first := recordPlaced(t, w, steeringTurn("first surviving entry"), PlaceSession)
	if first.TurnID != "turn_m9" || first.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("the execution's first surviving entry = %+v, want turn_m9 carrying the span kind the retracted record stamped", first)
	}
}

func TestReleaseCommitBoundaryFiresHeldNotificationsInOrder(t *testing.T) {
	t.Parallel()
	w, _ := newRecordWriter(t)
	var seen []Record
	w.OnRecorded(func(rec Record) { seen = append(seen, rec) })
	if kept, err := w.Record(steeringTurn("kept"), RecordOptions{Door: DoorDurable}); err != nil || !kept.Recorded {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
	boundary := w.CaptureCommitBoundary()
	var want []uint64
	for _, text := range []string{"held-one", "held-two", "held-three"} {
		rec, err := w.Record(steeringTurn(text), RecordOptions{Door: DoorDurable})
		if err != nil || !rec.Recorded {
			t.Fatalf("%s = %+v, %v", text, rec, err)
		}
		want = append(want, rec.Ordinal)
	}
	if len(want) == 0 || want[0] != boundary.ordinal {
		t.Fatalf("first held entry took ordinal %d, want the boundary's next ordinal %d", want[0], boundary.ordinal)
	}
	if len(seen) != 1 {
		t.Fatalf("recorded hook fired for %d held entries, want none", len(seen)-1)
	}
	w.ReleaseCommitBoundary()
	if len(seen) != len(want)+1 {
		t.Fatalf("recorded hook saw %d entries after release, want the %d held ones in order", len(seen)-1, len(want))
	}
	for i, ordinal := range want {
		if seen[i+1].Ordinal != ordinal {
			t.Fatalf("held entry %d delivered at ordinal %d, want %d in recorded order", i, seen[i+1].Ordinal, ordinal)
		}
	}
	// A release ends the hold: later appends fire immediately again.
	rec, err := w.Record(steeringTurn("after release"), RecordOptions{Door: DoorDurable})
	if err != nil || !rec.Recorded {
		t.Fatalf("after release = %+v, %v", rec, err)
	}
	if len(seen) != len(want)+2 || seen[len(seen)-1].Ordinal != rec.Ordinal {
		t.Fatalf("recorded hook after release-end = %+v, want the settled entry delivered immediately", seen)
	}
}
