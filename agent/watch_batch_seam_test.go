package agent

import (
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/events"
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

// TestPersistPendingWatchSendBatchesCapOverflowEvictions drives a real
// cap-overflow through persistPendingWatchSend (via recordWatchSend): with the
// pending map full, the next send evicts the oldest entry, and the pending
// event plus the eviction terminal event must land in ONE AppendBatch call —
// one fsync, all-or-nothing — with no per-event appendEvent calls. A batch
// failure must leave neither event durable nor the runtime map changed.
func TestPersistPendingWatchSendBatchesCapOverflowEvictions(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "caller",
		Events: []string{"assistant.message"},
		Send:   &watchSendArgs{To: "dlg_obs"},
	})
	cfg := onlyWatchConfigForTest(t, jm)

	build := func(target string) watchSendDelivery {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		return jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
	}

	// Fill the pending map to the cap through the durable path.
	for i := range defaultWatchSendPendingCap {
		if _, _, ok, err := jm.recordWatchSend(build(fmt.Sprintf("target_%d", i))); err != nil || !ok {
			t.Fatalf("fill send %d: ok=%v err=%v", i, ok, err)
		}
	}
	if len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending entries = %d, want %d", len(cfg.pending), defaultWatchSendPendingCap)
	}
	oldestKey := cfg.pendingOrder[0]

	// Count seam calls for the overflow send.
	var batches [][]jobstore.Event
	var singles int
	origSingle := jm.appendEvent
	origBatch := jm.appendEvents
	jm.appendEvent = func(e jobstore.Event) error {
		singles++
		return origSingle(e)
	}
	jm.appendEvents = func(evts []jobstore.Event) error {
		batches = append(batches, evts)
		return origBatch(evts)
	}

	state, _, ok, err := jm.recordWatchSend(build("target_overflow"))
	if err != nil || !ok {
		t.Fatalf("overflow send: ok=%v err=%v", ok, err)
	}
	if len(batches) != 1 {
		t.Fatalf("overflow persist made %d AppendBatch calls, want exactly 1 (pending + eviction share one fsync)", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Fatalf("batch carried %d events, want 2 (pending + eviction land together or roll back together)", len(batches[0]))
	}
	if batches[0][0].Kind != jobstore.EventWatchSendPending || batches[0][1].Kind != jobstore.EventWatchSendEvicted {
		t.Fatalf("batch kinds = [%v %v], want [pending evicted]", batches[0][0].Kind, batches[0][1].Kind)
	}
	if singles != 0 {
		t.Fatalf("overflow persist leaked %d appendEvent calls; the batch seam must own the group", singles)
	}

	// Runtime map: oldest evicted, newcomer present, size still at cap.
	if cfg.pending[oldestKey] != nil {
		t.Fatal("evicted oldest key still in runtime pending map")
	}
	if cfg.pending[state.Key] == nil {
		t.Fatal("overflow send missing from runtime pending map")
	}
	if len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending entries = %d, want %d", len(cfg.pending), defaultWatchSendPendingCap)
	}

	// Durable fold: newcomer pending, oldest gone.
	folded := loadWatchSendRecord(t, jm).Pending
	if folded[state.Key] == nil {
		t.Fatal("overflow send missing from durable pending fold")
	}
	if folded[oldestKey] != nil {
		t.Fatal("evicted oldest key still in durable pending fold")
	}
	if len(folded) != defaultWatchSendPendingCap {
		t.Fatalf("durable pending entries = %d, want %d", len(folded), defaultWatchSendPendingCap)
	}

	// Batch failure: all-or-nothing — neither event durable, runtime unchanged.
	want := errors.New("batch fsync failed")
	jm.appendEvents = func([]jobstore.Event) error { return want }
	before := len(loadJobStoreEvents(t, jm))
	runtimeBefore := len(cfg.pending)
	if _, _, ok, err := jm.recordWatchSend(build("target_lost")); !errors.Is(err, want) || ok {
		t.Fatalf("failed batch: ok=%v err=%v, want ok=false + the batch failure", ok, err)
	}
	if got := len(loadJobStoreEvents(t, jm)); got != before {
		t.Fatalf("failed batch left %d journal events, want %d (all-or-nothing)", got, before)
	}
	if len(cfg.pending) != runtimeBefore {
		t.Fatalf("failed batch changed runtime pending %d -> %d, want unchanged", runtimeBefore, len(cfg.pending))
	}
}
