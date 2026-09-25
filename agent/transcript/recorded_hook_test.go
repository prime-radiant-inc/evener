package transcript

import (
	"strconv"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

// Phase 3 hands each recorded entry to its thread's projection queue from this
// hook, so projection sees entries in ordinal order whichever goroutine
// appended them. Two writers on one file append concurrently; the hook must
// see every ordinal exactly once, in order, with the recorded length already
// covering each line.
func TestRecordedHookSeesEveryOrdinalInOrderUnderConcurrentAppends(t *testing.T) {
	path := newSharedFileTranscript(t) // one entry already recorded
	a := openSharedFileWriter(t, path)
	defer a.Close() //nolint:errcheck // fixture
	b := openSharedFileWriter(t, path)
	defer b.Close() //nolint:errcheck // fixture
	var seen []uint64
	var ends []int64
	var lengthsAtCall []int64
	a.OnRecorded(func(r Record) {
		// Called under the append lock: no other append interleaves, so the
		// unsynchronised appends here are race-free by construction.
		seen = append(seen, r.Ordinal)
		ends = append(ends, r.Offset+r.Length)
		lengthsAtCall = append(lengthsAtCall, a.tail.recordedLength)
	})
	const perWriter = 200
	var wg sync.WaitGroup
	for _, w := range []*Writer{a, b} {
		wg.Go(func() {
			for i := range perWriter {
				door := DoorBuffered
				if i%3 == 0 {
					door = DoorDurable
				}
				if _, err := w.Record(steeringTurn(strconv.Itoa(i)), RecordOptions{Door: door, Place: PlaceAsync}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if len(seen) != 2*perWriter {
		t.Fatalf("hook saw %d records, want %d", len(seen), 2*perWriter)
	}
	for i, ordinal := range seen {
		if ordinal != uint64(i+1) {
			t.Fatalf("hook call %d saw ordinal %d, want %d", i, ordinal, i+1)
		}
		if lengthsAtCall[i] != ends[i] {
			t.Fatalf("hook call %d: recorded length %d does not yet cover the line ending at %d", i, lengthsAtCall[i], ends[i])
		}
	}
	if got := a.RecordedLength(); got != ends[len(ends)-1] {
		t.Fatalf("recorded length %d, last hooked end %d", got, ends[len(ends)-1])
	}
}

// The hook belongs to the file, not the writer that installed it: a cold
// writer's append reaches it too.
func TestRecordedHookSeesAnotherWritersAppend(t *testing.T) {
	path := newSharedFileTranscript(t)
	session := openSharedFileWriter(t, path)
	defer session.Close() //nolint:errcheck // fixture
	var seen []string
	session.OnRecorded(func(r Record) { seen = append(seen, r.Turn.Message.Text()) })
	cold := openSharedFileWriter(t, path)
	if _, err := cold.Record(steeringTurn("cold"), RecordOptions{Door: DoorSynced, Place: PlaceDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := cold.Close(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "cold" {
		t.Fatalf("hook saw %q", seen)
	}
	session.OnRecorded(nil)
	if _, err := session.Record(steeringTurn("unhooked"), RecordOptions{Place: PlaceSession}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("a removed hook still ran: %q", seen)
	}
}

// A rolled-back append is never announced: the hook sees only the record that
// takes its ordinal afterwards.
func TestRecordedHookNeverSeesARolledBackAppend(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(faultPlan(5))), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatal(err)
	}
	var hooked []Record
	w.OnRecorded(func(r Record) { hooked = append(hooked, r) })
	if rec, err := w.Record(schema.NewTurn(schema.TurnUserInput, llm.User("rolled back")), RecordOptions{Door: DoorDurable}); err == nil || rec.Recorded {
		t.Fatalf("faulted append = %+v, %v", rec, err)
	}
	kept, err := w.Record(schema.NewTurn(schema.TurnUserInput, llm.User("kept")), RecordOptions{Door: DoorDurable})
	if err != nil || kept.Ordinal != 0 || kept.Seq != 1 {
		t.Fatalf("kept = %+v, %v; want ordinal 0 and seq 1", kept, err)
	}
	if len(hooked) != 1 || hooked[0].Ordinal != 0 || hooked[0].Turn.Message.Text() != "kept" {
		t.Fatalf("hook saw %+v", hooked)
	}
}
