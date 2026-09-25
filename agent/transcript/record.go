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
// recorded. The error contract is the door's own (see Append, AppendDurable
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
