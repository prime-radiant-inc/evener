package agent

import (
	"errors"
	"fmt"
	"strings"
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

// TestPersistPendingWatchSendEvictsBeforeStableVerification pins the
// eviction/runtime ordering fixed for roborev on head 68a473c: the
// cap-overflow eviction events land in the same batch as the pending event,
// so the evicted keys must leave the runtime map BEFORE the fallible
// CompleteWatchEnqueue/refold verification — not after it. A verification
// failure still returns persisted=true with the batch already durable, and
// the journal and the runtime map must agree on that path too.
//
// The fault injection fails CompleteWatchEnqueue deterministically: the
// overflow send begins a real stable enqueue, then the test steals the live
// enqueue receipt out of the controller (the stop-capture path in
// TestStableDelegateWatch_StopFencesAndDrainsBothReceiptClasses proves
// receipts stay completable once admitted) so completion reports a stale
// lease. The batch (pending + eviction) is already durable at that point,
// and the return carries persisted=true + the completion failure. The
// evicted key must already be gone from the runtime map.
func TestPersistPendingWatchSendEvictsBeforeStableVerification(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	jm := fixture.sourceJM
	cfg := fixture.onlyWatchConfig(t)

	build := func(target string) watchSendDelivery {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		return jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
	}

	// Fill the pending map to the cap through the durable path. Distinct
	// watched identities build distinct pending keys (ResolvedWatchedIdentity
	// is part of the key), so the map actually grows to the cap instead of
	// coalescing onto one entry.
	for i := range defaultWatchSendPendingCap {
		if _, _, ok, err := jm.recordWatchSend(build(fmt.Sprintf("target_%d", i))); err != nil || !ok {
			t.Fatalf("fill send %d: ok=%v err=%v", i, ok, err)
		}
	}
	if len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending entries = %d, want %d", len(cfg.pending), defaultWatchSendPendingCap)
	}
	oldestKey := cfg.pendingOrder[0]

	// Gate the overflow send's completion: once its batch has landed (seen
	// via the batch seam), steal the live enqueue receipt so
	// CompleteWatchEnqueue reports a stale lease. Every boundary hook fires
	// in order (enqueue admission, then completion), so the completion gate
	// waits for the batch to land first.
	origBoundary := jm.watchReceiptBoundary
	var boundaries int
	batchLanded := make(chan struct{})
	releaseCompletion := make(chan struct{})
	jm.watchReceiptBoundary = func() {
		boundaries++
		if origBoundary != nil {
			origBoundary()
		}
		// The completion boundary is the second hook call (admission is the
		// first). Wait until the batch is durable, then steal the live
		// enqueue receipt so CompleteWatchEnqueue reports a stale lease.
		if boundaries == 2 {
			<-batchLanded
			fixture.controller.mu.Lock()
			for token := range fixture.controller.watchEnqueues {
				delete(fixture.controller.watchEnqueues, token)
			}
			fixture.controller.mu.Unlock()
			<-releaseCompletion
		}
	}
	origBatch := jm.appendEvents
	jm.appendEvents = func(evts []jobstore.Event) error {
		err := origBatch(evts)
		// The overflow group is the only multi-event append in this test:
		// one pending event plus one cap-overflow eviction event.
		if err == nil && len(evts) == 2 {
			close(batchLanded)
		}
		return err
	}
	type overflowResult struct {
		state   jobstore.WatchSendState
		persist bool
		err     error
	}
	done := make(chan overflowResult, 1)
	go func() {
		state, _, ok, err := jm.recordWatchSend(build("target_overflow"))
		done <- overflowResult{state: state, persist: ok, err: err}
	}()
	// Wait for the batch to land, then release the gated completion.
	<-batchLanded
	close(releaseCompletion)
	res := <-done
	jm.appendEvents = origBatch
	jm.watchReceiptBoundary = origBoundary
	if res.err == nil || !res.persist {
		t.Fatalf("overflow with failed verification: ok=%v err=%v, want persisted=true + the completion failure", res.persist, res.err)
	}

	// The batch is durable (pending + eviction landed)...
	folded := loadWatchSendRecord(t, jm).Pending
	if folded[res.state.Key] == nil {
		t.Fatal("overflow send missing from durable pending fold")
	}
	if folded[oldestKey] != nil {
		t.Fatal("evicted oldest key still in durable pending fold")
	}
	// ...and the runtime map agrees: the evicted key is gone even though the
	// verification failed.
	jm.mu.Lock()
	_, evictedStillHeld := cfg.pending[oldestKey]
	_, newcomerHeld := cfg.pending[res.state.Key]
	jm.mu.Unlock()
	if evictedStillHeld {
		t.Fatal("evicted oldest key still in runtime pending map after failed verification")
	}
	if !newcomerHeld {
		t.Fatal("overflow send missing from runtime pending map after failed verification")
	}
}

// TestPersistPendingWatchSendWithoutBatchSeamCommitsDurablePrefix pins the
// fallback fixed for roborev on head 68a473c: without the AppendBatch seam
// (jm.appendEvents == nil) a co-generated pending + eviction group has no
// atomic write, so persistPendingWatchSend keeps the pre-batch sequential
// protocol — pending write then commit, per-eviction applied-prefix commit.
// When the eviction write fails after the pending event is already durable,
// the return carries persisted=true (not a ghost ok=false that never commits
// the durable prefix): the newcomer is in both the journal and the runtime
// map, and the evicted key — whose terminal event never landed — stays put.
func TestPersistPendingWatchSendWithoutBatchSeamCommitsDurablePrefix(t *testing.T) {
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

	// Drop to the singular seam (the registry fallback test
	// TestS1Cov_configureWatch_RegisterAppendFailure uses the same shape so
	// failAppendN can inject the failure), then fail the eviction terminal
	// write: the pending event lands, the eviction does not.
	jm.appendEvents = nil
	failAppendN(jm, jobstore.EventWatchSendEvicted, 1)
	before := len(loadJobStoreEvents(t, jm))

	state, _, ok, err := jm.recordWatchSend(build("target_overflow"))
	if err == nil || !ok {
		t.Fatalf("overflow with failed eviction write: ok=%v err=%v, want persisted=true + the eviction failure", ok, err)
	}

	// The pending event is durable and committed to the runtime map...
	folded := loadWatchSendRecord(t, jm).Pending
	if folded[state.Key] == nil {
		t.Fatal("overflow send missing from durable pending fold after failed eviction write")
	}
	if got := len(loadJobStoreEvents(t, jm)); got != before+1 {
		t.Fatalf("journal events after failed eviction write = %d, want %d (pending only, no eviction terminal)", got, before+1)
	}
	jm.mu.Lock()
	_, newcomerHeld := cfg.pending[state.Key]
	_, evictedHeld := cfg.pending[oldestKey]
	jm.mu.Unlock()
	if !newcomerHeld {
		t.Fatal("overflow send missing from runtime pending map after failed eviction write")
	}
	// ...and the evicted key — whose terminal event never landed — is still
	// held in both, so the journal and the runtime map agree.
	if folded[oldestKey] == nil {
		t.Fatal("evicted oldest key missing from durable pending fold although its eviction never landed")
	}
	if !evictedHeld {
		t.Fatal("evicted oldest key missing from runtime pending map although its eviction never landed")
	}
}

// TestRecordWatchSendsWakesOwnerOnDurablePrefix pins the roborev fix on head
// 3541703: recordWatchSend returns ok=true alongside the failure when a
// durable prefix already landed (persisted=true), and recordWatchSends must
// treat that as recorded — token for caller-targeted sends, kick otherwise —
// instead of dropping it as "already handled". Without the fix the journaled
// pending frame stalls until unrelated activity wakes the owner.
//
// The fault injection reuses the nil-seam prefix-commit shape: without the
// AppendBatch seam the pending event lands, the eviction write fails, and the
// return carries persisted=true + the eviction failure.
func TestRecordWatchSendsWakesOwnerOnDurablePrefix(t *testing.T) {
	t.Parallel()
	buildJM := func(t *testing.T, sendTo string) (*jobManager, *watchConfig, func(string) watchSendDelivery) {
		t.Helper()
		jm := newTestJM(t)
		installWatchBelowValidation(t, jm, watchArgs{
			Target: "caller",
			Events: []string{"assistant.message"},
			Send:   &watchSendArgs{To: sendTo},
		})
		cfg := onlyWatchConfigForTest(t, jm)
		build := func(target string) watchSendDelivery {
			jm.mu.Lock()
			defer jm.mu.Unlock()
			return jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
		}
		for i := range defaultWatchSendPendingCap {
			if _, _, ok, err := jm.recordWatchSend(build(fmt.Sprintf("target_%d", i))); err != nil || !ok {
				t.Fatalf("fill send %d: ok=%v err=%v", i, ok, err)
			}
		}
		return jm, cfg, build
	}
	failEvictionWrite := func(t *testing.T, jm *jobManager) {
		t.Helper()
		jm.appendEvents = nil
		failAppendN(jm, jobstore.EventWatchSendEvicted, 1)
	}

	// Caller-targeted send: the durable prefix must surface as a wake token.
	t.Run("caller token", func(t *testing.T) {
		t.Parallel()
		jm, cfg, build := buildJM(t, "caller")
		failEvictionWrite(t, jm)
		var queued []jobNotification
		jm.enqueue = func(n jobNotification) { queued = append(queued, n) }
		tokens, recorded := jm.recordWatchSends([]watchSendDelivery{build("target_overflow")})
		if !recorded {
			t.Fatal("recordWatchSends on a durable prefix: recorded=false, want true (the pending frame is journaled and owes the owner a wake)")
		}
		if len(tokens) != 1 {
			t.Fatalf("recordWatchSends on a durable caller prefix: %d tokens, want 1 caller wake token", len(tokens))
		}
		if tokens[0].WatchSend == nil {
			t.Fatal("caller wake token carries no watch-send token")
		}
		if got := tokens[0].WatchSend.Key; cfg.pending[got] == nil {
			t.Fatal("caller wake token keys a send missing from the runtime pending map")
		}
		if folded := loadWatchSendRecord(t, jm).Pending; folded[tokens[0].WatchSend.Key] == nil {
			t.Fatal("caller wake token keys a send missing from the durable pending fold")
		}
		// The wake token returns to the caller unqueued; the eviction failure
		// itself already queued exactly one diagnostic at the persist site —
		// the two coexist, which is the point of the fix.
		if len(queued) != 1 {
			t.Fatalf("recordWatchSends queued %d notifications itself, want 1 (the persist-site failure diagnostic)", len(queued))
		}
		if queued[0].WatchSend != nil {
			t.Fatal("persist-site queue carried the wake token; tokens must return to the caller for one wake")
		}
		if !strings.Contains(queued[0].Reason, "pending state failed") {
			t.Fatalf("queued diagnostic reason = %q, want the persist-site failure", queued[0].Reason)
		}
	})

	// Delegate-targeted send: no caller token exists, so recorded=true is what
	// still owes the owner a kick via recordWatchSendsAndKick.
	t.Run("delegate kick", func(t *testing.T) {
		t.Parallel()
		jm, cfg, build := buildJM(t, "dlg_obs")
		failEvictionWrite(t, jm)
		var kicks int
		jm.wake = func() { kicks++ }
		// newTestJM leaves jm.enqueue nil, so the batch helper's token queue
		// reports nothing queued: a recorded delegate send with no caller
		// token must wake the owner via kick.
		jm.recordWatchSendsAndKick([]watchSendDelivery{build("target_overflow")})
		if kicks != 1 {
			t.Fatalf("durable delegate prefix produced %d kicks, want 1 (recorded with no tokens must wake the owner)", kicks)
		}
		// And the frame the kick covers is really there: journaled and in
		// the runtime map.
		folded := loadWatchSendRecord(t, jm).Pending
		if len(folded) != defaultWatchSendPendingCap+1 {
			t.Fatalf("durable pending entries = %d, want %d (newcomer journaled, evicted key retained)", len(folded), defaultWatchSendPendingCap+1)
		}
		if len(cfg.pending) != defaultWatchSendPendingCap+1 {
			t.Fatalf("runtime pending entries = %d, want %d (matches the journal)", len(cfg.pending), defaultWatchSendPendingCap+1)
		}
	})
}

// TestPersistPendingWatchSendPersistsEachEvictionOnce guards the batch path
// against persisting an eviction's terminal events twice: once in the group
// write and again in a per-eviction loop. A duplicated EventWatchSendEvicted
// collapses invisibly in the durable fold (a map keyed by watch), so this
// counts the raw journal records instead — exactly one eviction event per
// overflow, on both the batch seam and the nil-seam sequential protocol.
func TestPersistPendingWatchSendPersistsEachEvictionOnce(t *testing.T) {
	t.Parallel()
	for _, seam := range []string{"batch", "sequential"} {
		t.Run(seam, func(t *testing.T) {
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
			for i := range defaultWatchSendPendingCap {
				if _, _, ok, err := jm.recordWatchSend(build(fmt.Sprintf("target_%d", i))); err != nil || !ok {
					t.Fatalf("fill send %d: ok=%v err=%v", i, ok, err)
				}
			}
			if seam == "sequential" {
				jm.appendEvents = nil
			}
			if _, _, ok, err := jm.recordWatchSend(build("target_overflow")); err != nil || !ok {
				t.Fatalf("overflow send: ok=%v err=%v", ok, err)
			}
			evicted := 0
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.Kind == jobstore.EventWatchSendEvicted {
					evicted++
				}
			}
			if evicted != 1 {
				t.Fatalf("%s seam persisted %d EventWatchSendEvicted records, want exactly 1 (no double-persist)", seam, evicted)
			}
		})
	}
}
