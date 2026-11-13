package transcript

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/schema"
)

// Door is how an append reaches durability.
type Door int

const (
	// DoorBuffered is Append's door: fsync at most once per SyncInterval.
	DoorBuffered Door = iota
	// DoorDurable is AppendDurable's door: fsync, and roll the line back if
	// the fsync fails.
	DoorDurable
	// DoorSynced is AppendSynced's door: recorded AND durable, or an error.
	DoorSynced
)

// RecordOptions chooses an append's door and the turn its entry joins.
type RecordOptions struct {
	Door  Door
	Place Placement
}

// Record is the outcome of one append: recorded, with the entry's ordinal and
// sequence number, or not recorded. Only a recorded line has an ordinal.
type Record struct {
	// Recorded reports that the whole line is in the file. The other fields
	// are meaningful only when it is set.
	Recorded bool
	// Ordinal is the entry ordinal: the 0-based index of the entry line among
	// the file's entry lines (the header excluded). FailureCounter's older
	// fromEntryOrdinal counts from 1: it is Ordinal+1.
	Ordinal uint64
	// Seq is the entry's sequence number.
	Seq int
	// Offset and Length locate the line in the file, its newline included.
	Offset int64
	Length int64
	// Turn is the turn as written, with whatever identity the placement
	// stamped on it.
	Turn schema.Turn
}

// Record appends one turn through opts.Door and reports whether it was
// recorded. A placement with nothing to place (a completion with no running
// execution) records nothing and returns no error through every door, as a
// nil writer does; check Recorded. The error contract is the door's own (see Append, AppendDurable
// and AppendSynced): a nil writer, and a closed one through the buffered or
// durable door, record nothing and return no error; the synced door returns
// ErrWriterClosed for a closed writer, and a *RetainedUnsyncedError for a
// line that is recorded but could not be made durable — Record then reports
// Recorded with the error, and the caller adopts the record.
func (w *Writer) Record(turn schema.Turn, opts RecordOptions) (Record, error) {
	if opts.Door == DoorSynced {
		return w.recordSynced(turn, opts.Place)
	}
	records, _, err := w.appendBatch([]schema.Turn{turn}, opts.Place, opts.Door)
	return firstRecord(records), err
}

func firstRecord(records []Record) Record {
	if len(records) == 0 {
		return Record{}
	}
	return records[0]
}

// recordSynced is the synced door: see AppendSynced for its three outcomes.
func (w *Writer) recordSynced(turn schema.Turn, place Placement) (Record, error) {
	records, retained, err := w.appendBatch([]schema.Turn{turn}, place, DoorSynced)
	if err != nil {
		return Record{}, err // not recorded (includes ErrWriterClosed)
	}
	rec := firstRecord(records)
	if retained == nil {
		return rec, nil // recorded and its own fsync succeeded: durable, or no writer
	}
	// Recorded but unsynced: the record is in the file, so a barrier that
	// fsyncs the whole file settles it.
	barrierErr := w.EstablishDurability()
	if barrierErr == nil {
		return rec, nil
	}
	// The record is durable neither by its own fsync nor the barrier. It is
	// still a record: queue its diagnostic for the session to surface, and tell
	// the owner to adopt it rather than re-append.
	w.mu.Lock()
	w.queueWarningLocked(errors.Join(retained, fmt.Errorf("establish durability: %w", barrierErr)))
	w.mu.Unlock()
	return rec, &RetainedUnsyncedError{Seq: rec.Seq, Cause: retained}
}

// RecordedLength is the length of the file's recorded prefix: the header and
// every recorded entry line. It never includes a rolled-back line. Nil-safe.
func (w *Writer) RecordedLength() int64 {
	if w == nil || w.tail == nil {
		return 0
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	return w.tail.recordedLength
}

// CommitBoundary is the append point a caller captured before writing, so it
// can take the lines it wrote since back out. Opaque: only RetractToBoundary
// consumes one.
type CommitBoundary struct {
	length  int64
	ordinal uint64
	turns   turnPlacement
}

// CaptureCommitBoundary records where this file's next entry would land: the
// recorded length, the next entry ordinal and the placement state. A caller
// that holds every writer on the file off for the whole transaction (the
// session's transcript door does) can pass the boundary to RetractToBoundary
// to take the transaction's own lines back out again. Nil-safe: a writer that
// does not exist has nothing to retract.
func (w *Writer) CaptureCommitBoundary() CommitBoundary {
	if w == nil || w.tail == nil {
		return CommitBoundary{}
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	return CommitBoundary{length: w.tail.recordedLength, ordinal: w.tail.nextOrdinal, turns: w.tail.turns}
}

// RetractToBoundary takes back out of the file every line written since b was
// captured, restoring the recorded length, the next entry ordinal and the
// placement state b captured. It is the rollback of a failed durable append,
// initiated by the caller for lines that did land: the one use is a
// transaction that must not leave a half-applied marker behind (#1158) — the
// fold's compaction markers are what a resume anchors on to discard every
// entry before them, so the fold takes its whole commit back out when the
// staged copies the anchor would depend on could not all be written.
//
// removed reports whether the lines are gone, which only the truncate
// decides, exactly as in an append's own rollback; err names a truncate, seek
// or sync that failed after or while removing them. With removed set and err
// non-nil the bytes are gone but the truncate may not be durable — the same
// residual crash window an ordinary rollback leaves. The caller must hold
// every writer on the file off between capture and retract (the session's
// transcript door): a line some other writer appended in that span would be
// taken out with the transaction's own. Sequence numbers stay spent, as in any
// rollback: a reader in another process may have seen the lines.
//
// The state restore happens only when the lines are gone: a truncate that
// failed leaves the file, the counters and the placement as they were. The
// retracted lines' failure-counter observations stand (the count is monotone
// by contract), and a writer the failed append poisoned stays poisoned — the
// retract works regardless, because it only ever removes bytes.
func (w *Writer) RetractToBoundary(b CommitBoundary) (removed bool, err error) {
	if w == nil || w.tail == nil {
		return true, nil // a writer that never existed recorded nothing to take out
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed.Load() {
		return false, errors.New("transcript writer is closed: cannot retract")
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	if w.tail.recordedLength < b.length || w.tail.nextOrdinal < b.ordinal {
		// The file shrank below the boundary since it was captured: some other
		// writer's rollback is in flight. Truncating would cut into lines the
		// boundary never covered.
		return false, fmt.Errorf("transcript shrank below the captured boundary (length %d < %d, ordinal %d < %d)", w.tail.recordedLength, b.length, w.tail.nextOrdinal, b.ordinal)
	}
	removed, err = w.rollbackAppendLocked(b.length)
	if !removed {
		return removed, err
	}
	w.tail.recordedLength = b.length
	w.tail.nextOrdinal = b.ordinal
	w.tail.turns = b.turns
	w.dirty = err != nil // the truncate's own sync settled the shorter file
	return removed, err
}

// OnRecorded installs fn as the file's recorded-entry hook, replacing any
// earlier one; nil removes it. The hook belongs to the file, so it sees every
// writer's appends in this process, not only this writer's.
//
// fn is called once for each recorded entry, in ordinal order, while the
// append lock is held and after the recorded length covers the entry. That
// is what lets a consumer (phase 3's per-thread projection queue) see entries
// in file order whichever goroutine appended them. The append lock is a leaf:
// fn must not append, and must not take any lock an appender may hold. It
// never sees an entry a rollback took back out. Nil-safe.
func (w *Writer) OnRecorded(fn func(Record)) {
	if w == nil || w.tail == nil {
		return
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	w.tail.onRecorded = fn
}
