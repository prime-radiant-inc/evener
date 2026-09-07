package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestAppendWatchSendEventsBatchesMultiEventGroups pins the AppendBatch seam
// added for perf(watch-batch): a co-generated multi-event group (pending +
// cap-overflow evictions, as planned by planWatchSendPending) must go through
// jm.appendEvents in ONE call — one fsync, all-or-nothing — while single-event
// appends stay on the appendEvent seam so fault-injection harnesses stubbing
// only appendEvent keep intercepting them.
func TestAppendWatchSendEventsBatchesMultiEventGroups(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	var batches [][]jobstore.Event
	var singles int
	origSingle := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		singles++
		return origSingle(e)
	}
	jm.appendEvents = func(events []jobstore.Event) error {
		batches = append(batches, events)
		// Mirror AppendBatch semantics for the test store: append each event
		// through the real single seam so the fold stays identical.
		for _, e := range events {
			if err := origSingle(e); err != nil {
				return err
			}
		}
		return nil
	}

	// Single event: must stay on the appendEvent seam (no batch call).
	single := jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: jm.now()}
	if err := jm.appendWatchSendEvents([]jobstore.Event{single}); err != nil {
		t.Fatalf("appendWatchSendEvents single: %v", err)
	}
	if len(batches) != 0 {
		t.Fatalf("single-event append went through AppendBatch (%d batch calls); singles must stay on the appendEvent seam", len(batches))
	}
	if singles != 1 {
		t.Fatalf("single-event append hit appendEvent %d times, want 1", singles)
	}

	// Multi-event group: exactly one batch call carrying every event.
	evicted := single
	evicted.Kind = jobstore.EventWatchSendEvicted
	if err := jm.appendWatchSendEvents([]jobstore.Event{single, evicted}); err != nil {
		t.Fatalf("appendWatchSendEvents multi: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("multi-event append made %d AppendBatch calls, want exactly 1 (one fsync per co-generated group)", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Fatalf("batch carried %d events, want 2 (pending + eviction land together or roll back together)", len(batches[0]))
	}
	if singles != 1 {
		t.Fatalf("multi-event append leaked %d extra appendEvent calls; the batch seam must own the group", singles-1)
	}

	// Batch error propagates (all-or-nothing surfaces to the caller).
	want := errors.New("batch fsync failed")
	jm.appendEvents = func([]jobstore.Event) error { return want }
	if err := jm.appendWatchSendEvents([]jobstore.Event{single, evicted}); !errors.Is(err, want) {
		t.Fatalf("multi-event append error = %v, want the batch failure", err)
	}
}
