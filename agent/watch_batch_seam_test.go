package agent

import (
	"errors"
	"fmt"
	"slices"
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
// failure must leave neither event durable nor the persistence runtime map
// changed. The recordWatchSend wrapper additionally retains an unpersisted
// produced frame for retry, which the real delivery pump must recover.
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
		d := jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
		jm.mu.Unlock()
		// Isolate source persistence from diagnostic-triggered receiver custody.
		d.deliveryID = "batch-source-" + target
		if !jm.claimWatchSendDelivery(d.deliveryID) {
			t.Fatal("fixture delivery claim already held")
		}
		t.Cleanup(func() { jm.releaseWatchSendDeliveryClaim(d.deliveryID) })
		return d
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
	pendingBefore := make(map[jobstore.WatchSendKey]jobstore.WatchSendState, runtimeBefore)
	for key, pending := range cfg.pending {
		pendingBefore[key] = *pending
	}
	orderBefore := slices.Clone(cfg.pendingOrder)
	d := build("target_lost")
	if _, ok, err := jm.persistPendingWatchSend(jm.watchSendState(d, "dlg_obs"), d); !errors.Is(err, want) || ok {
		t.Fatalf("failed batch: ok=%v err=%v, want ok=false + the batch failure", ok, err)
	}
	if got := len(loadJobStoreEvents(t, jm)); got != before {
		t.Fatalf("failed batch left %d journal events, want %d (all-or-nothing)", got, before)
	}
	if len(cfg.pending) != runtimeBefore {
		t.Fatalf("failed batch changed runtime pending %d -> %d, want unchanged", runtimeBefore, len(cfg.pending))
	}
	for key, pending := range pendingBefore {
		if got := cfg.pending[key]; got == nil || *got != pending {
			t.Fatalf("failed batch changed pending key %+v: got %+v, want %+v", key, got, pending)
		}
	}
	if !slices.Equal(cfg.pendingOrder, orderBefore) || jm.terminalFlush[cfg] {
		t.Fatalf("failed batch changed pending order or terminal projection: order=%v terminal=%v", cfg.pendingOrder, jm.terminalFlush[cfg])
	}
	// The source wrapper has an additional recovery contract: preserve an
	// unpersisted produced frame, without claiming a successful batch commit.
	retryDelivery := build("target_retry")
	if _, _, ok, err := jm.recordWatchSend(retryDelivery); !errors.Is(err, want) || ok {
		t.Fatalf("wrapper failed batch: ok=%v err=%v", ok, err)
	}
	if got := len(loadJobStoreEvents(t, jm)); got != before {
		t.Fatalf("wrapper failure changed durable journal %d -> %d", before, got)
	}
	if len(cfg.pending) != runtimeBefore+1 {
		t.Fatalf("wrapper retry pending entries = %d, want %d", len(cfg.pending), runtimeBefore+1)
	}
	for key, pending := range pendingBefore {
		if got := cfg.pending[key]; got == nil || *got != pending {
			t.Fatalf("wrapper failure changed existing pending key %+v: got %+v, want %+v", key, got, pending)
		}
	}
	var retained *jobstore.WatchSendState
	for _, pending := range cfg.pending {
		if pending.DeliveryID == "batch-source-target_retry" {
			retained = pending
		}
	}
	if retained == nil || retained.Key.ResolvedWatchedIdentity != "target_retry" || !jm.terminalFlush[cfg] {
		t.Fatalf("wrapper lost its exact unpersisted retry: state=%+v detached=%v", retained, jm.terminalFlush[cfg])
	}
	if len(loadWatchSendRecord(t, jm).Pending) != defaultWatchSendPendingCap {
		t.Fatal("unpersisted wrapper retry appeared durable")
	}
	// Recover through the real pump. Only the failed frame's transport claim
	// is released, so successful source entries remain untouched.
	jm.appendEvents = origBatch
	jm.releaseWatchSendDeliveryClaim("batch-source-target_retry")
	jm.dispatchProducedWatchSends()
	var retryEvents []jobstore.EventKind
	for _, event := range loadJobStoreEvents(t, jm)[before:] {
		if event.WatchSend == nil || event.WatchSend.DeliveryID != "batch-source-target_retry" {
			t.Fatalf("recovery changed another delivery: %+v", event)
		}
		retryEvents = append(retryEvents, event.Kind)
	}
	if len(retryEvents) != 2 || retryEvents[0] != jobstore.EventWatchSendPending || retryEvents[1] != jobstore.EventWatchSendDropped {
		t.Fatalf("wrapper recovery events = %v, want pending then dropped for retired loose receiver", retryEvents)
	}
	if len(cfg.pending) != runtimeBefore || len(loadWatchSendRecord(t, jm).Pending) != defaultWatchSendPendingCap {
		t.Fatal("wrapper recovery did not restore the source-only pending inventory")
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
		d := jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
		jm.mu.Unlock()
		// Hold transport only, stable enqueue and its verification remain real.
		d.deliveryID = "stable-source-" + target
		if !jm.claimWatchSendDelivery(d.deliveryID) {
			t.Fatal("fixture delivery claim already held")
		}
		t.Cleanup(func() { jm.releaseWatchSendDeliveryClaim(d.deliveryID) })
		return d
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

	// After the overflow batch lands, eviction releases the oldest receipt.
	// Gate that boundary and steal the newcomer's live enqueue receipt so
	// its later CompleteWatchEnqueue verification reports a stale lease.
	origBoundary := jm.watchReceiptBoundary
	var boundaries int
	batchLanded := make(chan struct{})
	releaseCompletion := make(chan struct{})
	jm.watchReceiptBoundary = func() {
		boundaries++
		if origBoundary != nil {
			origBoundary()
		}
		// Enqueue admission is first, eviction receipt release is second.
		// Steal the live enqueue receipt after durability but before the
		// newcomer's CompleteWatchEnqueue verification.
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
		d := jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
		jm.mu.Unlock()
		// This fixture checks source writes, not authorization of dummy targets.
		// Diagnostic delivery pumps must not consume the prefix under test.
		d.deliveryID = "prefix-" + target
		if !jm.claimWatchSendDelivery(d.deliveryID) {
			t.Fatal("fixture delivery claim already held")
		}
		t.Cleanup(func() { jm.releaseWatchSendDeliveryClaim(d.deliveryID) })
		return d
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

// TestRecordWatchSendsRetainsDurablePrefix pins the roborev fix on head
// 3541703: recordWatchSend returns ok=true alongside the failure when a
// durable prefix already landed (persisted=true), and recordWatchSends must
// treat that as recorded for both caller and delegate sends,
// instead of dropping it as "already handled". Without the fix the journaled
// pending frame stalls until unrelated activity wakes the owner.
//
// The fault injection reuses the nil-seam prefix-commit shape: without the
// AppendBatch seam the pending event lands, the eviction write fails, and the
// return carries persisted=true + the eviction failure.
func TestRecordWatchSendsRetainsDurablePrefix(t *testing.T) {
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
			d := jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
			jm.mu.Unlock()
			// Hold transport claims while testing source writes. Diagnostics pump
			// canonical delivery, which must not consume this fixture's prefix.
			d.deliveryID = "prefix-" + target
			if !jm.claimWatchSendDelivery(d.deliveryID) {
				t.Fatal("fixture delivery claim already held")
			}
			t.Cleanup(func() { jm.releaseWatchSendDeliveryClaim(d.deliveryID) })
			return d
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

	// Bare-manager fixtures prove source work, not receiver delivery.
	t.Run("caller source", func(t *testing.T) {
		t.Parallel()
		jm, cfg, build := buildJM(t, "caller")
		failEvictionWrite(t, jm)
		var queued []jobNotification
		jm.enqueue = func(n jobNotification) { queued = append(queued, n) }
		recorded := jm.recordWatchSends([]watchSendDelivery{build("target_overflow")})
		if !recorded {
			t.Fatal("recordWatchSends on a durable prefix: recorded=false, want true (the pending frame is journaled and owes the owner a wake)")
		}
		found := false
		for key := range cfg.pending {
			if key.ResolvedWatchedIdentity == "target_overflow" {
				found = true
				if loadWatchSendRecord(t, jm).Pending[key] == nil {
					t.Fatal("durable prefix missing from source fold")
				}
			}
		}
		if !found {
			t.Fatal("durable prefix missing from pending source map")
		}
		if len(queued) != 1 {
			t.Fatalf("diagnostics = %d, want 1", len(queued))
		}
		if !strings.Contains(queued[0].Reason, "pending state failed") {
			t.Fatalf("queued diagnostic reason = %q, want the persist-site failure", queued[0].Reason)
		}
	})

	// A durable delegate prefix likewise remains source work. Do not pump an
	// invalid fixture-only target through canonical authorization and then
	// mistake its legitimate diagnostic drop for lost persistence.
	t.Run("delegate source", func(t *testing.T) {
		t.Parallel()
		jm, cfg, build := buildJM(t, "dlg_obs")
		failEvictionWrite(t, jm)
		if recorded := jm.recordWatchSends([]watchSendDelivery{build("target_overflow")}); !recorded {
			t.Fatal("durable delegate prefix was not reported as source work")
		}
		// And the source frame is really there: journaled and in
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
