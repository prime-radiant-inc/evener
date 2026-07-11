//go:build serffuzz

package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzWatchRestoreClearHistoryProgram exercises the durable watch-send restore
// rail and the manager's clear/list/history projections. It uses only test-owned
// job stores and direct job-manager calls: no provider, process, network, or
// repository operation is reachable from this target.
func FuzzWatchRestoreClearHistoryProgram(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 2, 3, 4})
	f.Add([]byte{7, 11, 13, 17, 19})
	f.Add([]byte{255, 0, 255, 0, 255, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wrcReader{data: data}
		wrcExerciseRestore(t, r)
		wrcExerciseClearAndDurability(t, r)
		wrcExerciseClearHelpers(t, r)
		wrcExerciseHistoryAndProjections(t, r)
	})
}

type wrcReader struct {
	data []byte
	pos  int
}

func (r *wrcReader) next() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func (r *wrcReader) bool() bool { return r.next()&1 != 0 }

func (r *wrcReader) pick(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[int(r.next())%len(values)]
}

func wrcNewJM(t *testing.T) *jobManager {
	t.Helper()
	jm := newTestJM(t)
	t.Cleanup(func() {
		jm.closeGrace = 0
		_ = jm.close()
	})
	return jm
}

func wrcConfig(id, source, target, sendTo, receiverSessionID, receiverDelegateID string) *watchConfig {
	cfg := &watchConfig{
		id:                 id,
		watchID:            id,
		configHash:         "hash-" + id,
		sourcePublic:       source,
		receiverSessionID:  receiverSessionID,
		receiverDelegateID: receiverDelegateID,
		target:             target,
		outputMatch:        "ready",
		progressIntervalMS: minWatchProgressIntervalMS,
		events:             []string{"assistant.tool"},
		eventKinds:         map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:        &watchEventFilter{ToolName: "read_file", Status: "ok"},
		generation:         "gen-" + id,
		pending:            make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
		settledUpdateSeq:   make(map[jobstore.WatchSendKey]uint64),
		createdAt:          frozenTestTime,
	}
	if sendTo != "" {
		cfg.send = &watchSendArgs{To: sendTo, Message: "observe " + id, IncludeExcerpt: true}
	}
	return cfg
}

func wrcAddPending(cfg *watchConfig, key jobstore.WatchSendKey, seq uint64) {
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	cfg.pending[key] = &jobstore.WatchSendState{
		Key:           key,
		DeliveryID:    "delivery-" + cfg.watchID + fmt.Sprintf("-%d", seq),
		UpdateSeq:     seq,
		Message:       "observe " + cfg.watchID,
		TriggerReason: "output_match: ready",
		CreatedAt:     frozenTestTime,
		UpdatedAt:     frozenTestTime,
	}
	cfg.pendingOrder = append(cfg.pendingOrder, key)
	if seq > cfg.nextUpdateSeq {
		cfg.nextUpdateSeq = seq
	}
}

func wrcInstall(jm *jobManager, key watchKey, cfg *watchConfig) {
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.mu.Unlock()
}

func wrcExerciseRestore(t *testing.T, r *wrcReader) {
	t.Helper()
	jmA := wrcNewJM(t)
	jmB := wrcNewJM(t)
	base := frozenTestTime.Add(time.Duration(r.next()) * time.Second)

	states := []jobstore.WatchSendState{
		{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        "S1",
				WatchID:                 "watch-alpha",
				WatchTarget:             "job_alpha",
				ResolvedWatchedIdentity: "job_alpha",
				ResolvedSendTo:          "dlg_alpha",
				WatchGeneration:         "gen-alpha",
			},
			DeliveryID: "delivery-alpha-1",
			UpdateSeq:  2,
			Message:    "alpha",
			CreatedAt:  base.Add(2 * time.Second),
			UpdatedAt:  base.Add(4 * time.Second),
		},
		{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        "S1",
				WatchID:                 "watch-alpha",
				WatchTarget:             "job_alpha",
				ResolvedWatchedIdentity: "job_alpha-child",
				ResolvedSendTo:          "dlg_alpha",
				WatchGeneration:         "gen-alpha",
			},
			DeliveryID: "delivery-alpha-2",
			UpdateSeq:  7,
			Message:    "alpha child",
			CreatedAt:  base.Add(time.Second),
			UpdatedAt:  base.Add(3 * time.Second),
		},
		{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        "S1",
				WatchID:                 "watch-parent",
				WatchTarget:             runtimeMessageAliasCaller,
				ResolvedWatchedIdentity: runtimeMessageAliasCaller,
				ResolvedSendTo:          "dlg_observer",
				WatchGeneration:         "gen-parent",
			},
			DeliveryID:         "delivery-parent",
			UpdateSeq:          5,
			Message:            "parent",
			ReceiverSessionID:  "child-session",
			ReceiverDelegateID: "dlg_observer",
			CreatedAt:          base,
			UpdatedAt:          base.Add(2 * time.Second),
		},
		{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        "S2",
				WatchID:                 "watch-beta",
				WatchTarget:             "job_beta",
				ResolvedWatchedIdentity: "job_beta",
				ResolvedSendTo:          "dlg_beta",
				WatchGeneration:         "gen-beta",
			},
			DeliveryID: "delivery-beta",
			UpdateSeq:  1,
			Message:    "beta",
			CreatedAt:  base.Add(3 * time.Second),
			UpdatedAt:  base.Add(5 * time.Second),
		},
	}
	if r.bool() {
		states[0], states[3] = states[3], states[0]
	}
	for _, jm := range []*jobManager{jmA, jmB} {
		for i := range states {
			state := states[i]
			if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: base, WatchSend: &state}); err != nil {
				t.Fatalf("append restore state: %v", err)
			}
		}
		if err := jm.restoreWatchSendPending(); err != nil {
			t.Fatalf("restore pending: %v", err)
		}
	}

	gotA := wrcRestoredStateSummary(t, jmA)
	gotB := wrcRestoredStateSummary(t, jmB)
	if !equalStrings(gotA, gotB) {
		t.Fatalf("restore reconstruction diverged\nA=%v\nB=%v", gotA, gotB)
	}
	if len(gotA) != len(states) {
		t.Fatalf("restored pending = %d, want %d (%v)", len(gotA), len(states), gotA)
	}

	jmA.mu.Lock()
	var parent *watchConfig
	for cfg := range jmA.terminalFlush {
		if cfg.watchID == "watch-parent" {
			parent = cfg
		}
		for i := 1; i < len(cfg.pendingOrder); i++ {
			left := cfg.pending[cfg.pendingOrder[i-1]]
			right := cfg.pending[cfg.pendingOrder[i]]
			if left == nil || right == nil || watchSendStateLess(right, left) {
				jmA.mu.Unlock()
				t.Fatalf("restore pending order is not stable for %q: %+v", cfg.watchID, cfg.pendingOrder)
			}
		}
	}
	jmA.mu.Unlock()
	if parent == nil || parent.sourcePublic != "parent" || parent.nextUpdateSeq != 5 {
		t.Fatalf("parent restore config = %+v, want parent source and next seq 5", parent)
	}

	wrcExerciseWatchSendOrdering(t, base)
}

func wrcRestoredStateSummary(t *testing.T, jm *jobManager) []string {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	var out []string
	for cfg := range jm.terminalFlush {
		if cfg == nil || len(cfg.pending) == 0 {
			t.Fatalf("restore retained an empty terminal-flush config: %+v", cfg)
		}
		for key, state := range cfg.pending {
			if state == nil {
				t.Fatalf("restore retained nil state for %+v", key)
			}
			out = append(out, fmt.Sprintf("%s|%s|%s|%d", cfg.watchID, key.ResolvedWatchedIdentity, state.DeliveryID, state.UpdateSeq))
		}
	}
	sort.Strings(out)
	return out
}

func wrcExerciseWatchSendOrdering(t *testing.T, base time.Time) {
	t.Helper()
	key := func(visible, target, identity, sendTo, id, generation string) jobstore.WatchSendKey {
		return jobstore.WatchSendKey{
			VisibleSessionID: visible, WatchTarget: target, ResolvedWatchedIdentity: identity,
			ResolvedSendTo: sendTo, WatchID: id, WatchGeneration: generation,
		}
	}
	baseState := &jobstore.WatchSendState{Key: key("a", "a", "a", "a", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1}
	variants := []*jobstore.WatchSendState{
		{Key: key("a", "a", "a", "a", "a", "a"), CreatedAt: base.Add(time.Second), UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "a", "a", "a", "a", "a"), CreatedAt: base, UpdatedAt: base.Add(time.Second), UpdateSeq: 1},
		{Key: key("a", "a", "a", "a", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 2},
		{Key: key("b", "a", "a", "a", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "b", "a", "a", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "a", "b", "a", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "a", "a", "b", "a", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "a", "a", "a", "b", "a"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
		{Key: key("a", "a", "a", "a", "a", "b"), CreatedAt: base, UpdatedAt: base, UpdateSeq: 1},
	}
	for _, variant := range variants {
		if !watchSendStateLess(baseState, variant) {
			t.Fatalf("watch send ordering lost a strict comparison: base=%+v variant=%+v", baseState, variant)
		}
		if watchSendStateLess(variant, baseState) {
			t.Fatalf("watch send ordering is not antisymmetric: base=%+v variant=%+v", baseState, variant)
		}
	}
	if watchSendStateLess(baseState, baseState) || watchSendKeyLess(baseState.Key, baseState.Key) {
		t.Fatal("watch send ordering is not reflexive")
	}
}

func wrcExerciseClearAndDurability(t *testing.T, r *wrcReader) {
	t.Helper()
	jm := wrcNewJM(t)
	target := r.pick([]string{"job_shared", "job_shared_alt"})
	keyA := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_a"}
	keyB := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_b"}
	keyOther := watchKey{VisibleSessionID: jm.sessionID, Target: "job_other", SendTo: "dlg_a"}
	cfgA := wrcConfig("watch-a", "self", target, "dlg_a", "", "")
	cfgB := wrcConfig("watch-b", "self", target, "dlg_b", "", "")
	cfgOther := wrcConfig("watch-other", "job_other", "job_other", "dlg_a", "", "")
	wrcInstall(jm, keyA, cfgA)
	wrcInstall(jm, keyB, cfgB)
	wrcInstall(jm, keyOther, cfgOther)
	if !jm.hasWatchClearState(keyA) {
		t.Fatal("installed watch is not clearable")
	}

	res, err := jm.clearWatch(keyA)
	if err != nil {
		t.Fatalf("clear exact watch: %v", err)
	}
	if res.Watching || res.Source != "self" || res.Target != target {
		t.Fatalf("exact clear result = %+v", res)
	}
	if jm.hasWatchClearState(keyA) || !jm.hasWatchClearState(keyB) {
		t.Fatalf("exact clear affected the wrong watch: A=%t B=%t", jm.hasWatchClearState(keyA), jm.hasWatchClearState(keyB))
	}

	res, err = jm.clearWatch(watchKey{VisibleSessionID: jm.sessionID, Target: target})
	if err != nil {
		t.Fatalf("clear all matching watches: %v", err)
	}
	if res.Watching || res.Source != "self" || jm.hasWatchClearState(keyB) || !jm.hasWatchClearState(keyOther) {
		t.Fatalf("broad clear result/state = %+v, other=%t", res, jm.hasWatchClearState(keyOther))
	}

	// A failed teardown must leave the watch callable and accepting delivery so a
	// later retry has the same semantic target.
	failing := wrcConfig("watch-failing", "self", "job_failing", "dlg_f", "", "")
	failingKey := watchKey{VisibleSessionID: jm.sessionID, Target: "job_failing", SendTo: "dlg_f"}
	wrcInstall(jm, failingKey, failing)
	failErr := errors.New("injected teardown failure")
	originalAppendEvents := jm.appendEvents
	jm.appendEvents = func([]jobstore.Event) error { return failErr }
	if _, err := jm.clearWatchByIDMatching(failing.watchID, func(*watchConfig) bool { return true }, false); !errors.Is(err, failErr) {
		t.Fatalf("failed teardown error = %v, want %v", err, failErr)
	}
	jm.appendEvents = originalAppendEvents
	jm.mu.Lock()
	_, _, stillLive := jm.watchConfigByIDLocked(failing.watchID)
	rejecting := failing.rejectingDelivery
	jm.mu.Unlock()
	if !stillLive || rejecting {
		t.Fatalf("failed teardown lost retryable state: live=%t rejecting=%t", stillLive, rejecting)
	}
	if exists, err := jm.hasWatchID(failing.watchID); err != nil || !exists {
		t.Fatalf("live hasWatchID = %t, %v", exists, err)
	}

	// clearWatch has the same retry promise when callers use a key rather than
	// a public watch id.
	failingByKey := wrcConfig("watch-failing-key", "self", "job_failing_key", "dlg_fk", "", "")
	failingByKeyKey := watchKey{VisibleSessionID: jm.sessionID, Target: "job_failing_key", SendTo: "dlg_fk"}
	wrcInstall(jm, failingByKeyKey, failingByKey)
	jm.appendEvents = func([]jobstore.Event) error { return failErr }
	if _, err := jm.clearWatch(failingByKeyKey); !errors.Is(err, failErr) {
		t.Fatalf("failed key teardown error = %v, want %v", err, failErr)
	}
	jm.appendEvents = originalAppendEvents
	if !jm.hasWatchClearState(failingByKeyKey) || failingByKey.rejectingDelivery {
		t.Fatalf("failed key teardown lost retryable state: clearable=%t rejecting=%t", jm.hasWatchClearState(failingByKeyKey), failingByKey.rejectingDelivery)
	}

	// The durable fall-back handles a runtime-lost watch without making the
	// clear operation falsely succeed while the persisted watch remains active.
	durable, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"communicate"}})
	if err != nil {
		t.Fatalf("configure durable watch: %v", err)
	}
	jm.mu.Lock()
	for key, cfg := range jm.watches {
		if cfg.watchID == durable.WatchID {
			delete(jm.watches, key)
		}
	}
	jm.mu.Unlock()
	if exists, err := jm.hasWatchID(durable.WatchID); err != nil || !exists {
		t.Fatalf("durable hasWatchID = %t, %v", exists, err)
	}
	clearEvent, err := jm.durableWatchClearEvent(durable.WatchID, "cleared")
	if err != nil || clearEvent == nil || clearEvent.WatchID != durable.WatchID {
		t.Fatalf("durable clear event = %+v, %v", clearEvent, err)
	}
	res, err = jm.clearWatchByID(durable.WatchID)
	if err != nil || res.Watching || res.WatchID != durable.WatchID {
		t.Fatalf("durable clear = %+v, %v", res, err)
	}
	if exists, err := jm.hasWatchID(durable.WatchID); err != nil || exists {
		t.Fatalf("durable watch remains active: %t, %v", exists, err)
	}
	if event, err := jm.durableWatchClearEvent(durable.WatchID, "cleared"); err != nil || event != nil {
		t.Fatalf("cleared durable watch made another clear event: %+v, %v", event, err)
	}

	// A failed durable append must leave the folded registry active for the
	// subsequent retry instead of dropping runtime state that is already gone.
	durableFailure, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}})
	if err != nil {
		t.Fatalf("configure durable failure watch: %v", err)
	}
	jm.mu.Lock()
	for key, cfg := range jm.watches {
		if cfg.watchID == durableFailure.WatchID {
			delete(jm.watches, key)
		}
	}
	jm.mu.Unlock()
	jm.appendEvents = func([]jobstore.Event) error { return failErr }
	if _, err := jm.clearWatchByID(durableFailure.WatchID); !errors.Is(err, failErr) {
		t.Fatalf("durable append failure = %v, want %v", err, failErr)
	}
	jm.appendEvents = originalAppendEvents
	if exists, err := jm.hasWatchID(durableFailure.WatchID); err != nil || !exists {
		t.Fatalf("durable append failure lost active registry: %t, %v", exists, err)
	}
	if _, err := jm.clearWatchByID(durableFailure.WatchID); err != nil {
		t.Fatalf("durable retry: %v", err)
	}

	if res, err := jm.clearWatchByID("not-a-watch"); err != nil || res.Watching {
		t.Fatalf("unknown durable clear = %+v, %v", res, err)
	}
	if res, err := jm.clearWatchByIDMatching("not-a-watch", nil, false); err != nil || res.Watching {
		t.Fatalf("unknown non-durable clear = %+v, %v", res, err)
	}
	if res, err := jm.clearWatchByIDMatching(failing.watchID, func(*watchConfig) bool { return false }, false); err != nil || res.Watching {
		t.Fatalf("disallowed clear = %+v, %v", res, err)
	}
	if res, err := jm.clearReceiverWatchByID("not-a-watch", "receiver", "dlg_receiver"); err != nil || res.Watching {
		t.Fatalf("missing receiver clear = %+v, %v", res, err)
	}
	if got := sourcePublicForClearedWatch(watchKey{VisibleSessionID: jm.sessionID, Target: "missing"}, nil); got != "" {
		t.Fatalf("missing cleared source = %q", got)
	}

	// Store-closed reads are an explicit error contract rather than an implicit
	// empty watch registry. This manager owns only a test temp directory.
	errorJM := wrcNewJM(t)
	if err := errorJM.store.Close(); err != nil {
		t.Fatalf("close error store: %v", err)
	}
	if err := errorJM.restoreWatchSendPending(); err == nil {
		t.Fatal("restore from closed store succeeded")
	}
	if _, err := errorJM.durableWatchClearEvent("watch", "cleared"); err == nil {
		t.Fatal("durable clear event from closed store succeeded")
	}
	if _, err := errorJM.hasWatchID("watch"); err == nil {
		t.Fatal("hasWatchID from closed store succeeded")
	}
}

func wrcExerciseClearHelpers(t *testing.T, r *wrcReader) {
	t.Helper()
	jm := wrcNewJM(t)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "job_helpers", SendTo: "dlg_helpers"}
	cfg := wrcConfig("watch-helpers", "self", key.Target, key.SendTo, "", "")
	wrcInstall(jm, key, cfg)

	jm.mu.Lock()
	foundKey, found := watchKeyForConfigLocked(jm, cfg)
	if !found || foundKey != key {
		jm.mu.Unlock()
		t.Fatalf("watchKeyForConfigLocked = %+v, %t; want %+v", foundKey, found, key)
	}
	if _, found := watchKeyForConfigLocked(jm, wrcConfig("missing", "", "", "", "", "")); found {
		jm.mu.Unlock()
		t.Fatal("missing config was found in live watches")
	}
	if cfg.deliveries != 0 || jm.recordWatchDeliveryLocked(cfg) {
		jm.mu.Unlock()
		t.Fatal("first watch delivery incorrectly crossed its budget")
	}
	if jm.recordWatchDeliveryLocked(nil) {
		jm.mu.Unlock()
		t.Fatal("nil watch config crossed the delivery budget")
	}
	cfg.deliveries = watchDeliveryBudget - 1
	if !jm.recordWatchDeliveryLocked(cfg) || jm.recordWatchDeliveryLocked(cfg) {
		jm.mu.Unlock()
		t.Fatal("watch delivery budget crossing is not latched to one increment")
	}
	pruned := jm.pruneWatchedTargetWatchesLocked(key.Target, "finished", frozenTestTime)
	jm.mu.Unlock()
	if len(pruned) != 1 || !cfg.rejectingDelivery {
		t.Fatalf("prune result = %+v rejecting=%t", pruned, cfg.rejectingDelivery)
	}
	jm.rollbackWatchConfigSnapshotsRejecting(pruned)
	if cfg.rejectingDelivery {
		t.Fatal("rollback did not restore a live pruned config")
	}

	// The terminal-flush rail remains clearable even after a live config has
	// detached. Its pending key must respect target/send and receiver scoping.
	detached := wrcConfig("watch-detached", "self", key.Target, key.SendTo, "receiver", "dlg_receiver")
	pendingKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchID:                 detached.watchID,
		WatchTarget:             key.Target,
		ResolvedWatchedIdentity: key.Target,
		ResolvedSendTo:          key.SendTo,
		WatchGeneration:         detached.generation,
	}
	wrcAddPending(detached, pendingKey, 3)
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[detached] = true
	jm.mu.Unlock()
	if !jm.hasWatchClearState(watchKey{VisibleSessionID: jm.sessionID, Target: key.Target, SendTo: key.SendTo, ReceiverSessionID: "receiver", ReceiverDelegateID: "dlg_receiver"}) {
		t.Fatal("matching detached pending watch is not clearable")
	}
	if watchConfigHasPendingMatchingKey(detached, watchKey{VisibleSessionID: jm.sessionID, Target: key.Target, SendTo: "other", ReceiverSessionID: "receiver", ReceiverDelegateID: "dlg_receiver"}) {
		t.Fatal("detached pending matched an unrelated send target")
	}
	if watchConfigHasPendingMatchingKey(nil, key) || watchConfigHasPendingMatchingKey(detached, watchKey{VisibleSessionID: jm.sessionID, Target: key.Target, SendTo: key.SendTo, ReceiverSessionID: "other", ReceiverDelegateID: "dlg_receiver"}) {
		t.Fatal("detached pending ignored its nil or receiver boundary")
	}
	if !watchConfigHasPendingMatchingKey(detached, watchKey{VisibleSessionID: jm.sessionID, Target: key.Target, SendTo: key.SendTo, ReceiverSessionID: "receiver", ReceiverDelegateID: "dlg_receiver"}) {
		t.Fatal("detached pending did not match its exact key")
	}

	if _, err := jm.clearReceiverWatchByID(detached.watchID, "", "dlg_receiver"); err == nil {
		t.Fatal("empty receiver clear succeeded")
	}
	if result, err := jm.clearReceiverWatchByID(detached.watchID, "other", "dlg_receiver"); err != nil || result.Watching {
		t.Fatalf("non-owner receiver clear = %+v, %v", result, err)
	}
	if result, err := jm.clearReceiverWatchByID(detached.watchID, "receiver", "dlg_receiver"); err != nil || result.Watching || result.WatchID != detached.watchID {
		t.Fatalf("owner receiver clear = %+v, %v", result, err)
	}

	// The budget cleaner must detach exactly one live config, issue its final
	// message once, and become a no-op after that config is gone.
	budgetKey := watchKey{VisibleSessionID: jm.sessionID, Target: "job_budget", SendTo: "dlg_budget"}
	budgetCfg := wrcConfig("watch-budget", "self", budgetKey.Target, budgetKey.SendTo, "", "")
	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
	wrcInstall(jm, budgetKey, budgetCfg)
	jm.autoClearWatchOverBudget(budgetCfg)
	jm.autoClearWatchOverBudget(budgetCfg)
	if jm.hasWatchClearState(budgetKey) || len(notifications) != 1 || !strings.Contains(notifications[0].Reason, "watch cleared") {
		t.Fatalf("budget auto-clear state=%t notifications=%+v", jm.hasWatchClearState(budgetKey), notifications)
	}
	jm.autoClearOverBudgetWatches([]*watchConfig{nil, budgetCfg})
	failingBudgetKey := watchKey{VisibleSessionID: jm.sessionID, Target: "job_budget_failed", SendTo: "dlg_budget_failed"}
	failingBudget := wrcConfig("watch-budget-failed", "self", failingBudgetKey.Target, failingBudgetKey.SendTo, "", "")
	wrcInstall(jm, failingBudgetKey, failingBudget)
	originalAppendEvents := jm.appendEvents
	jm.appendEvents = func([]jobstore.Event) error { return errors.New("budget append failed") }
	jm.autoClearWatchOverBudget(failingBudget)
	jm.appendEvents = originalAppendEvents
	if !jm.hasWatchClearState(failingBudgetKey) || failingBudget.rejectingDelivery {
		t.Fatalf("failed budget clear lost retryable config: clearable=%t rejecting=%t", jm.hasWatchClearState(failingBudgetKey), failingBudget.rejectingDelivery)
	}

	// Projection helpers must preserve value ownership and receiver privacy.
	progressCfg := wrcConfig("watch-progress", "self", "caller", "dlg_progress", "", "")
	if stop := progressCfg.initProgressStop(); stop == nil {
		t.Fatal("progress watch did not allocate a stop channel")
	}
	closeWatchConfig(progressCfg)
	closeWatchConfig(progressCfg)
	plainCfg := wrcConfig("watch-plain", "self", "caller", "", "", "")
	plainCfg.progressIntervalMS = 0
	if stop := plainCfg.initProgressStop(); stop != nil {
		t.Fatal("non-progress watch allocated a stop channel")
	}
	plainResult := watchResultFromConfig(plainCfg, r.bool())
	if plainResult.Send != nil || !plainResult.Watching {
		t.Fatalf("plain watch result = %+v", plainResult)
	}
	progressResult := watchResultFromConfig(progressCfg, false)
	if progressResult.Send == nil || progressResult.Send == progressCfg.send {
		t.Fatalf("watch result did not clone send args: %+v", progressResult)
	}
	progressResult.Send.Message = "mutated"
	if progressCfg.send.Message == "mutated" {
		t.Fatal("watch result leaked mutable send args")
	}
	internalCfg := wrcConfig("watch-internal", "parent", "caller", "dlg_internal", "child", "dlg_internal")
	if result := watchResultFromConfig(internalCfg, false); result.Send != nil {
		t.Fatalf("receiver-internal send leaked into public result: %+v", result)
	}
	if watchConfigSnapshot(nil) != nil {
		t.Fatal("nil watch config produced a snapshot")
	}
	snapshot := watchConfigSnapshot(progressCfg)
	if snapshot == nil || snapshot.SendTo != "dlg_progress" || snapshot.EventFilter == nil || snapshot.EventFilter.ToolName != "read_file" {
		t.Fatalf("watch snapshot = %+v", snapshot)
	}

	registered := watchRegisteredEvent(jm, progressCfg)
	cleared := watchClearedEvent(jm, progressCfg, "cleared")
	if registered.Kind != jobstore.EventWatchRegistered || registered.Watch == nil || cleared.Kind != jobstore.EventWatchCleared || cleared.Watch == nil {
		t.Fatalf("watch registry events are malformed: registered=%+v cleared=%+v", registered, cleared)
	}
	if err := jm.appendWatchRegisteredEvent(progressCfg); err != nil {
		t.Fatalf("append registered event: %v", err)
	}
	if err := jm.appendWatchRegistryEvents(nil); err != nil {
		t.Fatalf("append empty registry events: %v", err)
	}
	originalAppendEvents = jm.appendEvents
	jm.appendEvents = nil
	if err := jm.appendWatchRegistryEvents([]jobstore.Event{registered, cleared}); err != nil {
		t.Fatalf("append registry fallback: %v", err)
	}
	jm.appendEvents = originalAppendEvents
	originalAppend := jm.appendEvent
	jm.appendEvents = nil
	jm.appendEvent = func(jobstore.Event) error { return errors.New("registry append failed") }
	if err := jm.appendWatchRegistryEvents([]jobstore.Event{registered}); err == nil {
		t.Fatal("registry fallback append failure succeeded")
	}
	jm.appendEvent = originalAppend
	jm.appendEvents = originalAppendEvents
	terminal := watchSendTerminalSnapshot{cfg: progressCfg, events: []jobstore.Event{cleared}}
	targets := []watchConfigTerminalSnapshot{
		{key: budgetKey, cfg: budgetCfg, terminal: terminal, endReason: "cleared"},
		{key: watchKey{Target: "skip-nil"}, terminal: terminal},
	}
	if err := jm.appendWatchReplacementBatch(progressCfg, targets, []watchSendTerminalSnapshot{terminal}); err != nil {
		t.Fatalf("append replacement batch: %v", err)
	}
	if err := jm.appendWatchTeardownBatch([]watchSendTerminalSnapshot{terminal}, targets); err != nil {
		t.Fatalf("append teardown batch: %v", err)
	}
	if events := watchSendTerminalEvents([]watchSendTerminalSnapshot{terminal}); len(events) != 1 || events[0].Kind != jobstore.EventWatchCleared {
		t.Fatalf("terminal event flattening = %+v", events)
	}

	if !watchKeyMatchesClearRequest(key, key) || watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: "other", Target: key.Target}) || watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: key.VisibleSessionID, Target: "other"}) {
		t.Fatal("watch clear-key matching lost its identity constraints")
	}
	if !watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: key.VisibleSessionID, Target: key.Target}) || watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: key.VisibleSessionID, Target: key.Target, SendTo: "other"}) {
		t.Fatal("watch clear-key matching lost its wildcard send semantics")
	}
	if watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: key.VisibleSessionID, Target: key.Target, ReceiverSessionID: "other"}) || watchKeyMatchesClearRequest(key, watchKey{VisibleSessionID: key.VisibleSessionID, Target: key.Target, ReceiverDelegateID: "other"}) {
		t.Fatal("watch clear-key matching ignored receiver identity")
	}
}

func wrcExerciseHistoryAndProjections(t *testing.T, r *wrcReader) {
	t.Helper()
	jm := wrcNewJM(t)
	jm.sessionID = "S1"

	// Live and detached rows deliberately overlap on an id so the projection's
	// de-duplication and source/id sort tie-breaker are observable.
	plainA := wrcConfig("live-a", "same", "job_a", "", "", "")
	plainB := wrcConfig("live-b", "same", "job_b", "", "", "")
	owned := wrcConfig("owned", "parent", runtimeMessageAliasCaller, "dlg_owned", "S1", "dlg_owned")
	ownedTie := wrcConfig("owned-tie", "parent", runtimeMessageAliasCaller, "dlg_owned", "S1", "dlg_owned")
	ownedEarlier := wrcConfig("owned-earlier", "aaa", runtimeMessageAliasCaller, "dlg_owned", "S1", "dlg_owned")
	hidden := wrcConfig("hidden", "hidden", "job_hidden", "", "OTHER", "dlg_hidden")
	detached := wrcConfig("detached", "zzz", "job_detached", "", "", "")
	duplicate := wrcConfig("live-a", "same", "job_a", "", "", "")
	hiddenDetached := wrcConfig("hidden-detached", "hidden", "job_hidden_detached", "", "OTHER", "dlg_hidden")
	blankDetached := wrcConfig("", "blank", "job_blank", "", "", "")
	receiverDetached := wrcConfig("receiver-detached", "parent", runtimeMessageAliasCaller, "dlg_owned", "S1", "dlg_owned")
	jm.mu.Lock()
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "job_a"}] = plainA
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "job_b"}] = plainB
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: runtimeMessageAliasCaller, ReceiverSessionID: "S1", ReceiverDelegateID: "dlg_owned"}] = owned
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "job_owned_tie", ReceiverSessionID: "S1", ReceiverDelegateID: "dlg_owned"}] = ownedTie
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "job_owned_earlier", ReceiverSessionID: "S1", ReceiverDelegateID: "dlg_owned"}] = ownedEarlier
	jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "job_hidden", ReceiverSessionID: "OTHER", ReceiverDelegateID: "dlg_hidden"}] = hidden
	jm.terminalFlush = map[*watchConfig]bool{nil: true, detached: true, duplicate: true, hiddenDetached: true, blankDetached: true, receiverDetached: true}
	for i := 0; i < watchHistoryCap+2; i++ {
		cfg := wrcConfig(fmt.Sprintf("history-%02d", i), r.pick([]string{"self", "parent"}), fmt.Sprintf("job_history_%02d", i), "dlg_history", "", "")
		if i%3 == 0 {
			cfg.receiverSessionID = "S1"
			cfg.receiverDelegateID = "dlg_owned"
		}
		if i%5 == 0 {
			cfg.receiverSessionID = "OTHER"
			cfg.receiverDelegateID = "dlg_hidden"
		}
		key := watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: "dlg_history", ReceiverSessionID: cfg.receiverSessionID, ReceiverDelegateID: cfg.receiverDelegateID}
		cfg.deliveries = i
		jm.recordWatchEndedLocked(key, cfg, r.pick([]string{"cleared", "finished", "budget_exhausted"}))
	}
	jm.mu.Unlock()

	list := jm.watchListToolResult()
	if list.Count != len(list.Watches) {
		t.Fatalf("watch list count=%d rows=%d", list.Count, len(list.Watches))
	}
	wrcAssertInspectSorted(t, list.Watches)
	if !wrcHasWatch(list.Watches, "live-a") || !wrcHasWatch(list.Watches, "live-b") || !wrcHasWatch(list.Watches, "owned") || !wrcHasWatch(list.Watches, "detached") || wrcHasWatch(list.Watches, "hidden") {
		t.Fatalf("session-visible watch list leaked or omitted rows: %+v", list.Watches)
	}
	if len(list.RecentWatches) == 0 || len(list.RecentWatches) > watchHistoryCap {
		t.Fatalf("recent history length=%d", len(list.RecentWatches))
	}
	for _, entry := range list.RecentWatches {
		if strings.HasPrefix(entry.WatchID, "history-00") || strings.HasPrefix(entry.WatchID, "history-05") || strings.HasPrefix(entry.WatchID, "history-10") || strings.HasPrefix(entry.WatchID, "history-15") {
			t.Fatalf("hidden receiver history leaked into session list: %+v", entry)
		}
	}
	recent := jm.recentWatchSummaries()
	if len(recent) != len(list.RecentWatches) || len(recent) == 0 || recent[0].ID != list.RecentWatches[0].WatchID {
		t.Fatalf("recent projection diverged: recent=%+v list=%+v", recent, list.RecentWatches)
	}

	receiverList := jm.watchListToolResultForReceiver("S1", "dlg_owned")
	if receiverList.Count != len(receiverList.Watches) || !wrcHasWatch(receiverList.Watches, "owned") || !wrcHasWatch(receiverList.Watches, "receiver-detached") || wrcHasWatch(receiverList.Watches, "live-a") {
		t.Fatalf("receiver watch list = %+v", receiverList)
	}
	wrcAssertInspectSorted(t, receiverList.Watches)
	if empty := jm.watchListToolResultForReceiver("", "dlg_owned"); empty.Count != 0 || len(empty.Watches) != 0 {
		t.Fatalf("empty receiver list = %+v", empty)
	}

	if inspect := jm.inspectWatchByID("live-a"); !inspect.Watching || inspect.WatchID != "live-a" {
		t.Fatalf("live inspect = %+v", inspect)
	}
	if inspect := jm.inspectWatchByID("detached"); inspect.Watching || inspect.WatchID != "detached" {
		t.Fatalf("detached inspect = %+v", inspect)
	}
	if inspect := jm.inspectWatchByID("history-17"); inspect.Watching || inspect.WatchID != "history-17" || inspect.EndReason == "" {
		t.Fatalf("history inspect = %+v", inspect)
	}
	if inspect := jm.inspectWatchByID("history-15"); inspect.Watching || inspect.WatchID != "history-15" {
		t.Fatalf("hidden history inspect = %+v", inspect)
	}
	if inspect := jm.inspectWatchByID("not-a-watch"); inspect.Watching || inspect.WatchID != "not-a-watch" {
		t.Fatalf("missing inspect = %+v", inspect)
	}
	if inspect, ok := jm.inspectReceiverWatchByID("owned", "S1", "dlg_owned"); !ok || !inspect.Watching {
		t.Fatalf("receiver inspect = %+v, %t", inspect, ok)
	}
	if inspect, ok := jm.inspectReceiverWatchByID("receiver-detached", "S1", "dlg_owned"); !ok || inspect.Watching {
		t.Fatalf("receiver detached inspect = %+v, %t", inspect, ok)
	}
	if inspect, ok := jm.inspectReceiverWatchByID("history-03", "S1", "dlg_owned"); !ok || inspect.Watching || inspect.EndReason == "" {
		t.Fatalf("receiver history inspect = %+v, %t", inspect, ok)
	}
	if inspect, ok := jm.inspectReceiverWatchByID("owned", "OTHER", "dlg_owned"); ok || inspect.WatchID != "" {
		t.Fatalf("wrong receiver inspect = %+v, %t", inspect, ok)
	}
	if inspect, ok := jm.inspectReceiverWatchByID("owned", "", "dlg_owned"); ok || inspect.WatchID != "" {
		t.Fatalf("empty receiver inspect = %+v, %t", inspect, ok)
	}

	live := jm.liveWatchSummaries()
	if !wrcHasLiveWatch(live, "live-a") || !wrcHasLiveWatch(live, "owned") || wrcHasLiveWatch(live, "hidden") {
		t.Fatalf("live summaries visibility = %+v", live)
	}
	wrcAssertLiveSorted(t, live)
	receiverLive := jm.liveWatchSummariesForReceiver("S1", "dlg_owned")
	if len(receiverLive) != 3 || !wrcHasLiveWatch(receiverLive, "owned") || !wrcHasLiveWatch(receiverLive, "owned-tie") || !wrcHasLiveWatch(receiverLive, "owned-earlier") {
		t.Fatalf("receiver live summaries = %+v", receiverLive)
	}
	wrcAssertLiveSorted(t, receiverLive)
	if got := jm.liveWatchSummariesForReceiver("S1", ""); got != nil {
		t.Fatalf("receiver live summaries without delegate = %+v", got)
	}

	condition := watchConditionSummary(&watchConfig{
		outputMatch:        "ready",
		progressIntervalMS: minWatchProgressIntervalMS,
		events:             []string{"assistant.tool"},
		triggerEvery:       2,
		eventFilter:        &watchEventFilter{ToolName: "read_file", Status: "ok"},
	})
	if !strings.Contains(condition, "output_match") || !strings.Contains(condition, "every 2") || !strings.Contains(condition, "tool_name=read_file") {
		t.Fatalf("condition summary = %q", condition)
	}
	if got := watchConditionSummary(&watchConfig{wildcardEvents: true}); got != "events: [*]" {
		t.Fatalf("wildcard condition summary = %q", got)
	}
	if got := watchConditionSummary(&watchConfig{}); got != "" {
		t.Fatalf("empty condition summary = %q", got)
	}
	if got := watchEventFilterSummary(&watchEventFilter{}); got != "" {
		t.Fatalf("empty filter summary = %q", got)
	}
	if !watchConfigMatchesReceiver(owned, "S1", "dlg_owned") || watchConfigMatchesReceiver(nil, "S1", "dlg_owned") || !watchConfigVisibleToSession(plainA, "S1") || watchConfigVisibleToSession(hidden, "S1") || watchConfigVisibleToSession(nil, "S1") {
		t.Fatal("watch receiver visibility predicates diverged")
	}
	if !watchHistoryMatchesReceiver(watchHistoryEntry{receiverSessionID: "S1", receiverDelegateID: "dlg_owned"}, "S1", "dlg_owned") || !watchHistoryVisibleToSession(watchHistoryEntry{}, "S1") || watchHistoryVisibleToSession(watchHistoryEntry{receiverSessionID: "OTHER"}, "S1") {
		t.Fatal("watch history visibility predicates diverged")
	}
}

func wrcHasWatch(watches []jobWatchInspectToolResult, id string) bool {
	for _, watch := range watches {
		if watch.WatchID == id {
			return true
		}
	}
	return false
}

func wrcHasLiveWatch(watches []watchListEntry, id string) bool {
	for _, watch := range watches {
		if watch.ID == id {
			return true
		}
	}
	return false
}

func wrcAssertInspectSorted(t *testing.T, watches []jobWatchInspectToolResult) {
	t.Helper()
	for i := 1; i < len(watches); i++ {
		left, right := watches[i-1], watches[i]
		if right.Source < left.Source || right.Source == left.Source && right.WatchID < left.WatchID {
			t.Fatalf("inspect rows are not source/id sorted: %+v", watches)
		}
	}
}

func wrcAssertLiveSorted(t *testing.T, watches []watchListEntry) {
	t.Helper()
	for i := 1; i < len(watches); i++ {
		left, right := watches[i-1], watches[i]
		if right.Source < left.Source || right.Source == left.Source && right.ID < left.ID {
			t.Fatalf("live rows are not source/id sorted: %+v", watches)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
