package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
)

// TestCovWatchConfigHasPendingMatchingKey covers watchConfigHasPendingMatchingKey
// (job_watch.go lines 1617-1630): nil cfg, empty pending, receiver mismatch,
// and a matching pending key.
func TestCovWatchConfigHasPendingMatchingKey(t *testing.T) {
	jm := newTestJM(t)

	// nil cfg.
	if watchConfigHasPendingMatchingKey(nil, watchKey{}) {
		t.Fatal("nil cfg should return false")
	}

	// Empty pending.
	cfg := &watchConfig{target: "job_test", pending: nil}
	if watchConfigHasPendingMatchingKey(cfg, watchKey{Target: "job_test"}) {
		t.Fatal("empty pending should return false")
	}

	// Non-empty pending but receiver mismatch.
	cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{
		{}: {DeliveryID: "d1"},
	}
	cfg.receiverSessionID = "other_session"
	key := watchKey{Target: "job_test", ReceiverSessionID: "my_session"}
	if watchConfigHasPendingMatchingKey(cfg, key) {
		t.Fatal("receiver mismatch should return false")
	}

	// Matching receiver and matching pending key.
	cfg.receiverSessionID = ""
	pendingKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             "job_test",
		ResolvedWatchedIdentity: "job_test",
		ResolvedSendTo:          "caller",
	}
	cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{
		pendingKey: {DeliveryID: "d1"},
	}
	matchKey := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           "job_test",
		SendTo:           "caller",
	}
	if !watchConfigHasPendingMatchingKey(cfg, matchKey) {
		t.Fatal("matching pending key should return true")
	}

	// Non-matching pending key (different target).
	cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{
		{WatchTarget: "other_job"}: {DeliveryID: "d2"},
	}
	if watchConfigHasPendingMatchingKey(cfg, matchKey) {
		t.Fatal("non-matching pending key should return false")
	}
}

// TestCovRememberUnpersistedTerminalPendingWatchSend covers
// rememberUnpersistedTerminalPendingWatchSend (job_watch.go lines 3432-3452).
func TestCovRememberUnpersistedTerminalPendingWatchSend(t *testing.T) {
	jm := newTestJM(t)

	// nil cfg — no-op.
	jm.rememberUnpersistedTerminalPendingWatchSend(nil, jobstore.WatchSendState{})
	// Should not panic.

	// cfg with rejectingDelivery — no-op.
	cfg := &watchConfig{rejectingDelivery: true}
	jm.rememberUnpersistedTerminalPendingWatchSend(cfg, jobstore.WatchSendState{})
	if len(cfg.pending) != 0 {
		t.Fatal("rejecting delivery should not add pending")
	}

	// closing manager — no-op.
	jm.closing = true
	cfg2 := &watchConfig{target: "job_x"}
	jm.rememberUnpersistedTerminalPendingWatchSend(cfg2, jobstore.WatchSendState{Key: jobstore.WatchSendKey{WatchTarget: "job_x"}})
	if len(cfg2.pending) != 0 {
		t.Fatal("closing manager should not add pending")
	}
	jm.closing = false

	// Normal path — adds pending entry.
	key := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             "job_test",
		ResolvedWatchedIdentity: "job_test",
		ResolvedSendTo:          "caller",
	}
	state := jobstore.WatchSendState{
		Key:        key,
		DeliveryID: "delivery_1",
		UpdateSeq:  1,
		Frame:      "test frame",
	}
	cfg3 := &watchConfig{target: "job_test"}
	jm.rememberUnpersistedTerminalPendingWatchSend(cfg3, state)
	if cfg3.pending[key] == nil {
		t.Fatal("pending entry should be created")
	}
	if cfg3.pending[key].DeliveryID != "delivery_1" {
		t.Fatalf("delivery id = %q, want delivery_1", cfg3.pending[key].DeliveryID)
	}
	if len(cfg3.pendingOrder) != 1 || cfg3.pendingOrder[0] != key {
		t.Fatalf("pendingOrder = %+v, want [%+v]", cfg3.pendingOrder, key)
	}

	// Re-adding same key should not duplicate in pendingOrder.
	jm.rememberUnpersistedTerminalPendingWatchSend(cfg3, state)
	if len(cfg3.pendingOrder) != 1 {
		t.Fatalf("pendingOrder len = %d, want 1", len(cfg3.pendingOrder))
	}
}

// TestCovFinishStableWatchSettlementRetry covers
// finishStableWatchSettlementRetry (job_watch.go lines 3649-3653).
func TestCovFinishStableWatchSettlementRetry(t *testing.T) {
	jm := newTestJM(t)
	jm.stableWatchSettlementRetrying = true
	jm.finishStableWatchSettlementRetry()
	if jm.stableWatchSettlementRetrying {
		t.Fatal("stableWatchSettlementRetrying should be false after finish")
	}
}

// TestCovRecordWatchSendPending covers recordWatchSendPending
// (job_watch.go lines 3814-3818): the retained runtime-state transition.
func TestCovRecordWatchSendPending(t *testing.T) {
	jm := newTestJM(t)

	// Install a live watch so we can record a pending send against it.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("watch config not installed")
	}

	// Build a delivery and record the pending state.
	wsKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             runtimeMessageAliasCaller,
		ResolvedWatchedIdentity: runtimeMessageAliasCaller,
		ResolvedSendTo:          runtimeMessageAliasCaller,
		WatchGeneration:         cfg.generation,
	}
	state := jobstore.WatchSendState{
		Key:        wsKey,
		DeliveryID: "delivery_test",
		UpdateSeq:  1,
		Frame:      "test frame",
	}
	d := watchSendDelivery{
		cfg:        cfg,
		key:        key,
		generation: cfg.generation,
		send:       cfg.send,
	}
	record := jm.recordWatchSendPending(state, d)
	if record.cfg == nil {
		t.Fatal("record cfg should not be nil")
	}
	// The pending entry should be in cfg.pending.
	if cfg.pending[wsKey] == nil {
		t.Fatal("pending entry should be recorded")
	}
}

// TestCovRemoveRuntimePendingWatchSend covers removeRuntimePendingWatchSend
// (job_watch.go lines 3851-3860).
func TestCovRemoveRuntimePendingWatchSend(t *testing.T) {
	jm := newTestJM(t)

	// No watches — should be a no-op.
	jm.removeRuntimePendingWatchSend(jobstore.WatchSendState{Key: jobstore.WatchSendKey{}})

	// Install a watch with pending, then remove.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	onSessionEventKD(jm, events.EventCommunicate, nil)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil || len(cfg.pendingOrder) == 0 {
		t.Fatal("watch with pending not set up")
	}
	pendingKey := cfg.pendingOrder[0]
	pending := cfg.pending[pendingKey]
	jm.removeRuntimePendingWatchSend(*pending)
	if cfg.pending[pendingKey] != nil {
		t.Fatal("pending entry should be removed")
	}
}

// TestCovChildResumable covers childResumable (job_watch.go lines 4406-4409)
// and directStableDelegateForChildSession (lines 4411-4421).
func TestCovChildResumable(t *testing.T) {
	s := &Session{}
	// nil session / no controller — returns false.
	if s.childResumable("child_1") {
		t.Fatal("nil controller should return false")
	}
	// Empty child session ID.
	if s.childResumable("") {
		t.Fatal("empty child ID should return false")
	}
}

// TestCovWatchKeyMatchesClearRequest covers all branches of
// watchKeyMatchesClearRequest (job_watch.go lines 1473-1487).
func TestCovWatchKeyMatchesClearRequest(t *testing.T) {
	candidate := watchKey{VisibleSessionID: "s1", Target: "job_1", SendTo: "caller", ReceiverSessionID: "r1", ReceiverDelegateID: "d1"}

	// Exact match.
	if !watchKeyMatchesClearRequest(candidate, candidate) {
		t.Fatal("exact match should return true")
	}

	// Mismatch VisibleSessionID.
	req := candidate
	req.VisibleSessionID = "other"
	if watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("mismatch session should return false")
	}

	// Mismatch Target.
	req = candidate
	req.Target = "other_job"
	if watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("mismatch target should return false")
	}

	// Request SendTo non-empty and mismatch.
	req = candidate
	req.SendTo = "other"
	if watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("mismatch SendTo should return false")
	}

	// Request SendTo empty — match (wildcard).
	req = candidate
	req.SendTo = ""
	if !watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("empty request SendTo should match")
	}

	// ReceiverSessionID non-empty and mismatch.
	req = candidate
	req.ReceiverSessionID = "other"
	if watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("mismatch ReceiverSessionID should return false")
	}

	// ReceiverDelegateID non-empty and mismatch.
	req = candidate
	req.ReceiverDelegateID = "other"
	if watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("mismatch ReceiverDelegateID should return false")
	}

	// All empty optional fields — should match.
	req = watchKey{VisibleSessionID: "s1", Target: "job_1"}
	if !watchKeyMatchesClearRequest(candidate, req) {
		t.Fatal("empty optional fields should match")
	}
}

// TestCovRecordWatchDeliveryLocked covers recordWatchDeliveryLocked
// (job_watch.go lines 1517-1523) including the nil guard and budget crossing.
func TestCovRecordWatchDeliveryLocked(t *testing.T) {
	jm := newTestJM(t)

	// nil cfg — returns false.
	if jm.recordWatchDeliveryLocked(nil) {
		t.Fatal("nil cfg should return false")
	}

	// Normal increment — not yet at budget.
	cfg := &watchConfig{deliveries: 0}
	if jm.recordWatchDeliveryLocked(cfg) {
		t.Fatal("first delivery should not cross budget")
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", cfg.deliveries)
	}

	// Increment to just below budget — not yet crossing.
	cfg.deliveries = watchDeliveryBudget - 2
	if jm.recordWatchDeliveryLocked(cfg) {
		t.Fatal("delivery at budget-2 should not cross")
	}

	// Cross the budget — should return true exactly once.
	cfg.deliveries = watchDeliveryBudget - 1
	if !jm.recordWatchDeliveryLocked(cfg) {
		t.Fatal("delivery at budget should cross")
	}
	// Beyond budget — should not cross again.
	if jm.recordWatchDeliveryLocked(cfg) {
		t.Fatal("delivery beyond budget should not cross again")
	}
}

// TestCovWatchKeyForConfigLocked covers watchKeyForConfigLocked
// (job_watch.go lines 1528-1535).
func TestCovWatchKeyForConfigLocked(t *testing.T) {
	jm := newTestJM(t)

	// Empty watches — not found.
	_, ok := watchKeyForConfigLocked(jm, &watchConfig{})
	if ok {
		t.Fatal("empty watches should not find config")
	}

	// Install a watch and find it.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
	})
	wk := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[wk]
	if cfg == nil {
		t.Fatal("watch not installed")
	}
	foundKey, found := watchKeyForConfigLocked(jm, cfg)
	if !found {
		t.Fatal("should find installed config")
	}
	if foundKey != wk {
		t.Fatalf("found key = %+v, want %+v", foundKey, wk)
	}

	// A config not in watches — not found.
	other := &watchConfig{}
	_, found = watchKeyForConfigLocked(jm, other)
	if found {
		t.Fatal("unrelated config should not be found")
	}
}

// TestCovPruneWatchedTargetWatchesLocked covers pruneWatchedTargetWatchesLocked
// (job_watch.go lines 1641-1655).
func TestCovPruneWatchedTargetWatchesLocked(t *testing.T) {
	jm := newTestJM(t)

	// No watches — returns nil.
	now := jm.now()
	snapshots := jm.pruneWatchedTargetWatchesLocked("job_nonexistent", "pruned", now)
	if snapshots != nil {
		t.Fatalf("no watches should return nil, got %+v", snapshots)
	}

	// Install a watch targeting a concrete job.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "job_target1",
		Events: []string{"communicate"},
	})
	// Install a different watch on a different target.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "job_target2",
		Events: []string{"communicate"},
	})

	snapshots = jm.pruneWatchedTargetWatchesLocked("job_target1", "test_prune", now)
	if len(snapshots) != 1 {
		t.Fatalf("want 1 pruned snapshot, got %d", len(snapshots))
	}
	if snapshots[0].key.Target != "job_target1" {
		t.Fatalf("pruned target = %q, want job_target1", snapshots[0].key.Target)
	}
	if snapshots[0].endReason != "" {
		// endReason is not set by prune; it's set by the caller.
	}
	// Verify the pruned config is now rejecting delivery.
	if !snapshots[0].cfg.rejectingDelivery {
		t.Fatal("pruned config should be rejecting delivery")
	}

	// job_target2 should still be in watches.
	key2 := watchKey{VisibleSessionID: jm.sessionID, Target: "job_target2"}
	if jm.watches[key2] == nil {
		t.Fatal("job_target2 watch should still be in watches")
	}
}

// TestCovInspectWatchByID covers inspectWatchByID (job_watch.go lines 1960-1979).
func TestCovInspectWatchByID(t *testing.T) {
	jm := newTestJM(t)

	// Not found.
	result := jm.inspectWatchByID("watch_nonexistent")
	if result.Watching || result.Source != "" {
		t.Fatalf("not found should be zero result, got %+v", result)
	}
	if result.WatchID != "watch_nonexistent" {
		t.Fatalf("watch_id = %q, want watch_nonexistent", result.WatchID)
	}

	// Install a watch.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "job_inspect",
		Events: []string{"communicate"},
	})
	// Find its watchID.
	var watchID string
	for _, cfg := range jm.watches {
		if cfg != nil && cfg.target == "job_inspect" {
			watchID = cfg.watchID
			break
		}
	}
	if watchID == "" {
		t.Fatal("could not find installed watch ID")
	}

	result = jm.inspectWatchByID(watchID)
	if !result.Watching {
		t.Fatal("installed watch should be watching")
	}
	if result.Source == "" {
		t.Fatal("source should be non-empty for a live watch")
	}

	// Inspect a watch from history (after clearing).
	jm.mu.Lock()
	for key, cfg := range jm.watches {
		jm.recordWatchEndedLocked(key, cfg, "test_ended")
	}
	jm.mu.Unlock()

	result = jm.inspectWatchByID(watchID)
	// The watch should now be found in history.
	if result.WatchID != watchID {
		t.Fatalf("history watch_id = %q, want %q", result.WatchID, watchID)
	}
}

// TestCovLiveWatchSummaries covers liveWatchSummaries (job_watch.go lines 2040-2064).
func TestCovLiveWatchSummaries(t *testing.T) {
	jm := newTestJM(t)

	// No watches — empty result.
	entries := jm.liveWatchSummaries()
	if len(entries) != 0 {
		t.Fatalf("no watches should yield empty, got %d", len(entries))
	}

	// Install two watches.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "job_a",
		Events: []string{"communicate"},
	})
	installWatchBelowValidation(t, jm, watchArgs{
		Target: "job_b",
		Events: []string{"communicate"},
	})
	entries = jm.liveWatchSummaries()
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Entries should be sorted by Source.
	if entries[0].Source > entries[1].Source {
		t.Fatalf("entries not sorted: %q > %q", entries[0].Source, entries[1].Source)
	}

	// A receiver-scoped watch (not visible to this session) should be excluded.
	recvKey := watchKey{
		VisibleSessionID:  jm.sessionID,
		Target:            "job_c",
		ReceiverSessionID: "other_session",
	}
	recvCfg := &watchConfig{
		watchID:           "watch_recv",
		target:            "job_c",
		sourcePublic:      "job_c",
		receiverSessionID: "other_session",
	}
	jm.mu.Lock()
	jm.watches[recvKey] = recvCfg
	jm.mu.Unlock()

	entries = jm.liveWatchSummaries()
	if len(entries) != 2 {
		t.Fatalf("receiver-scoped watch should be excluded, got %d", len(entries))
	}
}

// TestCovSettleWatchSendLocked covers settleWatchSendLocked
// (job_watch.go lines 3892-3908): settling a key, settling with higher seq,
// and the settled-order cap eviction.
func TestCovSettleWatchSendLocked(t *testing.T) {
	cfg := &watchConfig{}

	// Settle key with seq 1.
	key1 := jobstore.WatchSendKey{WatchTarget: "job_1"}
	settleWatchSendLocked(cfg, key1, 1)
	if cfg.settledUpdateSeq[key1] != 1 {
		t.Fatalf("settled seq = %d, want 1", cfg.settledUpdateSeq[key1])
	}
	if len(cfg.settledOrder) != 1 {
		t.Fatalf("settledOrder len = %d, want 1", len(cfg.settledOrder))
	}

	// Settle same key with lower seq — should not update.
	settleWatchSendLocked(cfg, key1, 0)
	if cfg.settledUpdateSeq[key1] != 1 {
		t.Fatalf("settled seq should remain 1, got %d", cfg.settledUpdateSeq[key1])
	}

	// Settle same key with higher seq — should update.
	settleWatchSendLocked(cfg, key1, 5)
	if cfg.settledUpdateSeq[key1] != 5 {
		t.Fatalf("settled seq = %d, want 5", cfg.settledUpdateSeq[key1])
	}

	// Settle a new key.
	key2 := jobstore.WatchSendKey{WatchTarget: "job_2"}
	settleWatchSendLocked(cfg, key2, 1)
	if len(cfg.settledOrder) != 2 {
		t.Fatalf("settledOrder len = %d, want 2", len(cfg.settledOrder))
	}

	// Fill beyond the cap to test eviction.
	for i := 0; i < defaultWatchSendPendingCap+5; i++ {
		k := jobstore.WatchSendKey{WatchTarget: "job_overflow"}
		settleWatchSendLocked(cfg, k, uint64(i))
	}
	// The settledOrder should be capped.
	if len(cfg.settledOrder) > defaultWatchSendPendingCap {
		t.Fatalf("settledOrder len = %d, should be <= %d", len(cfg.settledOrder), defaultWatchSendPendingCap)
	}
}

// TestCovResolveWatchSendTarget covers resolveWatchSendTarget
// (job_watch.go lines 4633-4643).
func TestCovResolveWatchSendTarget(t *testing.T) {
	// Non-watched alias — returned as-is.
	got, err := resolveWatchSendTarget("caller", "job_1")
	if err != nil || got != "caller" {
		t.Fatalf("resolveWatchSendTarget(caller) = (%q, %v), want (caller, nil)", got, err)
	}

	// Watched alias with concrete job — resolves to watched job.
	got, err = resolveWatchSendTarget(runtimeMessageAliasWatched, "job_42")
	if err != nil || got != "job_42" {
		t.Fatalf("resolveWatchSendTarget(watched, job_42) = (%q, %v), want (job_42, nil)", got, err)
	}

	// Watched alias with empty jobID — error.
	got, err = resolveWatchSendTarget(runtimeMessageAliasWatched, "")
	if err == nil || !strings.Contains(err.Error(), "watched_unresolved") {
		t.Fatalf("resolveWatchSendTarget(watched, empty) = (%q, %v), want error", got, err)
	}

	// Watched alias with session target (caller) — error.
	got, err = resolveWatchSendTarget(runtimeMessageAliasWatched, runtimeMessageAliasCaller)
	if err == nil || !strings.Contains(err.Error(), "watched_unresolved") {
		t.Fatalf("resolveWatchSendTarget(watched, caller) = (%q, %v), want error", got, err)
	}
}

// TestCovHasPendingWatchSends covers hasPendingWatchSends
// (job_watch.go lines 4614-4631).
func TestCovHasPendingWatchSends(t *testing.T) {
	jm := newTestJM(t)

	// No watches, no terminal flush — false.
	if jm.hasPendingWatchSends() {
		t.Fatal("empty jm should have no pending watch sends")
	}

	// Install a watch with no pending — still false.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	if jm.hasPendingWatchSends() {
		t.Fatal("watch with no pending should return false")
	}

	// Fire the watch to create pending state.
	onSessionEventKD(jm, events.EventCommunicate, nil)
	if !jm.hasPendingWatchSends() {
		t.Fatal("watch with pending should return true")
	}
}

// TestCovWatchSendTokenNotification covers watchSendTokenNotification
// (job_watch.go lines 375-389).
func TestCovWatchSendTokenNotification(t *testing.T) {
	state := jobstore.WatchSendState{
		Key: jobstore.WatchSendKey{
			ResolvedWatchedIdentity: "job_watched",
		},
		UpdateSeq:     3,
		DeliveryID:    "delivery_42",
		TriggerReason: "output_match: ready",
		Provenance:    &provenance.Causal{ChainTruncated: true},
	}
	notif := watchSendTokenNotification("child_session_1", state)
	if notif.Kind != jobNotificationKindWatch {
		t.Fatalf("kind = %v, want watch", notif.Kind)
	}
	if notif.JobID != "job_watched" {
		t.Fatalf("jobID = %q, want job_watched", notif.JobID)
	}
	if notif.Status != jobNotificationEventWatch {
		t.Fatalf("status = %q, want %q", notif.Status, jobNotificationEventWatch)
	}
	if notif.Reason != "output_match: ready" {
		t.Fatalf("reason = %q", notif.Reason)
	}
	if notif.WatchSend == nil {
		t.Fatal("WatchSend token should be set")
	}
	if notif.WatchSend.ChildSessionID != "child_session_1" {
		t.Fatalf("childSessionID = %q, want child_session_1", notif.WatchSend.ChildSessionID)
	}
	if notif.WatchSend.UpdateSeq != 3 {
		t.Fatalf("updateSeq = %d, want 3", notif.WatchSend.UpdateSeq)
	}
	if notif.WatchSend.DeliveryID != "delivery_42" {
		t.Fatalf("deliveryID = %q, want delivery_42", notif.WatchSend.DeliveryID)
	}
}

// TestCovJobManagerForToken covers jobManagerForToken (job_watch.go lines 392-406).
func TestCovJobManagerForToken(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{jobManager: jm}

	// nil token — returns nil.
	if s.jobManagerForToken(nil) != nil {
		t.Fatal("nil token should return nil")
	}

	// Token with empty ChildSessionID — returns the session's own jm.
	tok := &watchSendToken{ChildSessionID: ""}
	if s.jobManagerForToken(tok) != jm {
		t.Fatal("empty child session should return session's own jm")
	}

	// Token with non-existent child session — returns nil.
	tok.ChildSessionID = "nonexistent"
	if s.jobManagerForToken(tok) != nil {
		t.Fatal("non-existent child should return nil")
	}

	// nil subagents — returns nil.
	s2 := &Session{}
	tok2 := &watchSendToken{ChildSessionID: "child_1"}
	if s2.jobManagerForToken(tok2) != nil {
		t.Fatal("nil subagents should return nil")
	}
}

// TestCovResolveWatchSendToken covers resolveWatchSendToken
// (job_watch.go lines 410-426).
func TestCovResolveWatchSendToken(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{jobManager: jm}

	// nil token — returns ok=false.
	_, _, _, ok := s.resolveWatchSendToken(nil)
	if ok {
		t.Fatal("nil token should return ok=false")
	}

	// Token for non-existent config — returns ok=false.
	tok := &watchSendToken{
		ChildSessionID: "",
		Key:            jobstore.WatchSendKey{WatchTarget: "job_none"},
		UpdateSeq:      1,
	}
	_, _, _, ok = s.resolveWatchSendToken(tok)
	if ok {
		t.Fatal("non-existent config should return ok=false")
	}
}

// TestCovWithSelfInfluence covers withSelfInfluence (job_watch.go lines 347-350).
func TestCovWithSelfInfluence(t *testing.T) {
	d := watchSendDelivery{}
	c := selfInfluence{self: true, gradientDepth: 3, fuseDepth: 5, truncated: true}
	d = d.withSelfInfluence(c)
	if !d.selfInfluence || d.gradientDepth != 3 || d.fuseDepth != 5 || !d.truncated {
		t.Fatalf("withSelfInfluence did not stamp correctly: %+v", d)
	}
}

// TestCovAppendWatchFrameJobRead covers appendWatchFrameJobRead
// (job_watch.go lines 4693-4701).
func TestCovAppendWatchFrameJobRead(t *testing.T) {
	// Empty frame — returned as-is.
	if got := appendWatchFrameJobRead("", "job_1"); got != "" {
		t.Fatalf("empty frame should return empty, got %q", got)
	}
	// Empty jobID — returned as-is.
	if got := appendWatchFrameJobRead("frame", ""); got != "frame" {
		t.Fatalf("empty jobID should return frame as-is, got %q", got)
	}
	// Normal case — appends read instruction.
	got := appendWatchFrameJobRead("some frame", "job_42")
	if !strings.Contains(got, "read_transcript") || !strings.Contains(got, "job:job_42") {
		t.Fatalf("frame should contain read instruction, got %q", got)
	}
	// Frame without trailing newline gets one appended.
	got = appendWatchFrameJobRead("line", "job_1")
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("frame should end with newline, got %q", got)
	}
	// Frame with trailing newline does not get an extra one.
	got = appendWatchFrameJobRead("line\n", "job_1")
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("frame should not end with double newline, got %q", got)
	}
}

// TestCovWaitForJobDone covers waitForJobDone (session_tools_jobs.go lines 1902-1917).
// It uses the clock abstraction so no real sleep is needed.
func TestCovWaitForJobDone(t *testing.T) {
	jm := newTestJM(t)

	// Job not in running — returns true immediately (already done).
	if !waitForJobDone(context.Background(), jm, "job_nonexistent", time.Second) {
		t.Fatal("non-existent job should return true (already done)")
	}

	// Create a running job and immediately finalize it — waitForJobDone should
	// return true because the done channel closes.
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	jobID := rec.JobID
	code := 0
	done := make(chan struct{})
	go func() {
		jm.finalize(jobID, jobstore.StatusCompleted, "done", &code)
		close(done)
	}()
	<-done
	if !waitForJobDone(context.Background(), jm, jobID, 5*time.Second) {
		t.Fatal("finalized job should return true")
	}
}

// TestCovWaitForJobDone_ContextCancel covers the context cancellation path.
func TestCovWaitForJobDone_ContextCancel(t *testing.T) {
	jm := newTestJM(t)
	// Create a running job that never finalizes.
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() {
		// Clean up the job so it doesn't hang test teardown.
		code := 0
		jm.finalize(rec.JobID, jobstore.StatusCompleted, "cleanup", &code)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	// With a canceled context, waitForJobDone should return false (context done).
	if waitForJobDone(ctx, jm, rec.JobID, 5*time.Second) {
		// The done channel may or may not have closed depending on timing,
		// but with a canceled context it should return false.
		// If the job finalized first, true is also acceptable — but it shouldn't.
		// The job is still running, so this should be false.
		t.Fatal("running job with canceled context should return false")
	}
}

// TestCovProjectJobRecord covers projectJobRecord (session_tools_jobs.go lines 1930-1932).
func TestCovProjectJobRecord(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{jobManager: jm, clock: jm.clock}
	rec := &jobstore.JobRecord{
		JobID:  "job_proj",
		Type:   jobstore.JobShell,
		Status: jobstore.StatusRunning,
	}
	entry := projectJobRecord(s, rec)
	if entry.JobID != "job_proj" {
		t.Fatalf("entry.JobID = %q, want job_proj", entry.JobID)
	}
	if entry.Type != string(jobstore.JobShell) {
		t.Fatalf("entry.Type = %q, want %q", entry.Type, jobstore.JobShell)
	}
}

// TestCovJobToolRegisterFuncRegister covers jobToolRegisterFunc.Register
// (session_tools_jobs.go lines 69-70).
func TestCovJobToolRegisterFuncRegister(t *testing.T) {
	called := false
	f := jobToolRegisterFunc(func(rt tool.RegisteredTool) error {
		called = true
		return nil
	})
	if err := f.Register(tool.RegisteredTool{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !called {
		t.Fatal("underlying function should be called")
	}

	// Error propagation.
	errSentinel := errors.New("registration failed")
	f2 := jobToolRegisterFunc(func(rt tool.RegisteredTool) error {
		return errSentinel
	})
	if err := f2.Register(tool.RegisteredTool{}); !errors.Is(err, errSentinel) {
		t.Fatalf("error = %v, want %v", err, errSentinel)
	}
}
