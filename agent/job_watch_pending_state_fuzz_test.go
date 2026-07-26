package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// FuzzWatchPendingStateProgram drives the pending-send state machine through
// coalescing, rollback, bounded settlement, detached matching, and durable
// append failures. The input selects harmless identity variants; every seed
// executes the same state transitions so corpus replay retains branch coverage.
func FuzzWatchPendingStateProgram(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Fuzz(func(t *testing.T, variant byte) {
		jm := newTestJM(t)
		jm.enqueue = func(jobNotification) {}
		now := time.Unix(1700000000+int64(variant), 0).UTC()
		jm.now = func() time.Time { return now }

		target := fmt.Sprintf("job_pending_%d", variant%3)
		key := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedWatchedIdentity: target, ResolvedSendTo: "dlg_sink", WatchGeneration: "gen"}
		state := jobstore.WatchSendState{Key: key, DeliveryID: "delivery", UpdateSeq: 2, Frame: "frame"}
		wk := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_sink"}
		cfg := &watchConfig{target: target, send: &watchSendArgs{To: "dlg_sink"}, generation: "gen", pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}, pendingOrder: []jobstore.WatchSendKey{key}}
		jm.watches[wk] = cfg
		_, _ = jm.deliverPendingWatchSend(t.Context(), nil, state, false, nil)
		fresh := func(sendTo string) (*watchConfig, jobstore.WatchSendState, watchKey) {
			k := key
			k.ResolvedSendTo = sendTo
			s := state
			s.Key = k
			c := &watchConfig{target: target, send: &watchSendArgs{To: sendTo}, generation: "gen", pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{k: &s}, pendingOrder: []jobstore.WatchSendKey{k}}
			w := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: sendTo}
			jm.watches[w] = c
			return c, s, w
		}
		staleCfg, staleState, _ := fresh("caller_nil")
		_, _ = jm.deliverPendingWatchSend(t.Context(), staleCfg, staleState, false, nil)
		busyCfg, busyState, _ := fresh("caller_busy")
		_, _ = jm.deliverPendingWatchSend(t.Context(), busyCfg, busyState, false, func(context.Context, sendMessageArgs) sendMessageResult {
			return sendMessageResult{Err: errors.New("busy")}
		})
		hardCfg, hardState, _ := fresh("caller_hard")
		_, _ = jm.deliverPendingWatchSend(t.Context(), hardCfg, hardState, false, func(context.Context, sendMessageArgs) sendMessageResult {
			return sendMessageResult{WatchSendDeliveryClassSet: true, WatchSendDeliveryClass: watchSendHardFailure, Err: errors.New("hard")}
		})
		defaultCfg, defaultState, _ := fresh("caller_default")
		_, _ = jm.deliverPendingWatchSend(t.Context(), defaultCfg, defaultState, false, func(context.Context, sendMessageArgs) sendMessageResult {
			return sendMessageResult{WatchSendDeliveryClassSet: true, WatchSendDeliveryClass: watchSendDeliveryClass(99)}
		})
		invalidateCfg, invalidateState, invalidateKey := fresh("caller_invalidate")
		_, _ = jm.deliverPendingWatchSend(t.Context(), invalidateCfg, invalidateState, false, func(context.Context, sendMessageArgs) sendMessageResult {
			delete(jm.watches, invalidateKey)
			return sendMessageResult{}
		})
		ensureCfg, ensureState, _ := fresh("caller_ensure")
		originalAppendEarly := jm.appendEvent
		jm.appendEvent = func(jobstore.Event) error { return errors.New("ensure pending") }
		_, _ = jm.deliverPendingWatchSend(t.Context(), ensureCfg, ensureState, true, func(context.Context, sendMessageArgs) sendMessageResult { return sendMessageResult{} })
		jm.appendEvent = originalAppendEarly
		deliveredCfg, deliveredState, _ := fresh("caller_delivered_error")
		jm.appendEvent = func(jobstore.Event) error { return errors.New("settle delivered") }
		_, _ = jm.deliverPendingWatchSend(t.Context(), deliveredCfg, deliveredState, false, func(context.Context, sendMessageArgs) sendMessageResult { return sendMessageResult{} })
		jm.appendEvent = originalAppendEarly
		_, _, _ = jm.staleDelegateWatchSend(jobstore.WatchSendState{Key: jobstore.WatchSendKey{ResolvedSendTo: "caller"}})
		seedWatchSendDelegateTarget(t, jm, "dlg_stale")
		delegates, err := jm.store.LoadDelegates()
		if err != nil {
			t.Fatal(err)
		}
		generation := delegates["dlg_stale"].Generation
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventDelegateStopGateClosed, TS: now, DelegateID: "dlg_stale", Delegate: &jobstore.DelegateEvent{Generation: generation, StopJobID: "job_stale"}}); err != nil {
			t.Fatal(err)
		}
		_, _, _ = jm.staleDelegateWatchSend(jobstore.WatchSendState{Key: jobstore.WatchSendKey{ResolvedSendTo: "dlg_stale"}})
		_, _, _ = jm.staleDelegateWatchSend(jobstore.WatchSendState{Key: jobstore.WatchSendKey{ResolvedSendTo: "dlg_stale"}, DelegateGeneration: generation})
		_, _ = jm.delegateStoppedAfterWatchSendPending("dlg_stale", state)
		legacyKey := key
		legacyKey.ResolvedSendTo = "dlg_stale"
		legacy := state
		legacy.Key = legacyKey
		legacy.DeliveryID = "legacy"
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: now, WatchSend: &legacy}); err != nil {
			t.Fatal(err)
		}
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "noise"}); err != nil {
			t.Fatal(err)
		}
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventDelegateStopGateClosed, TS: now, DelegateID: "other", Delegate: &jobstore.DelegateEvent{StopJobID: "other"}}); err != nil {
			t.Fatal(err)
		}
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventDelegateStopGateClosed, TS: now, DelegateID: "dlg_stale", Delegate: &jobstore.DelegateEvent{Generation: generation, StopJobID: "job_stale"}}); err != nil {
			t.Fatal(err)
		}
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_stale_restart", DelegateID: "dlg_stale"}); err != nil {
			t.Fatal(err)
		}
		_, _, _ = jm.staleDelegateWatchSend(legacy)
		legacyCfg := &watchConfig{target: target, send: &watchSendArgs{To: "dlg_stale"}, generation: "gen", pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{legacy.Key: &legacy}, pendingOrder: []jobstore.WatchSendKey{legacy.Key}}
		legacyWatchKey := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_stale"}
		jm.watches[legacyWatchKey] = legacyCfg
		_, _ = jm.deliverPendingWatchSend(t.Context(), legacyCfg, legacy, false, func(context.Context, sendMessageArgs) sendMessageResult { return sendMessageResult{} })
		activeLegacyKey := legacyKey
		activeLegacyKey.ResolvedSendTo = "dlg_active"
		seedWatchSendDelegateTarget(t, jm, "dlg_active")
		activeLegacy := legacy
		activeLegacy.Key = activeLegacyKey
		activeLegacy.DeliveryID = "active-legacy"
		if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: now, WatchSend: &activeLegacy}); err != nil {
			t.Fatal(err)
		}
		_, _, _ = jm.staleDelegateWatchSend(activeLegacy)
		_, _ = jm.delegateStoppedAfterWatchSendPending("dlg_stale", legacy)
		staleDeliveryCfg, staleDeliveryState, _ := fresh("dlg_stale")
		_, _ = jm.deliverPendingWatchSend(t.Context(), staleDeliveryCfg, staleDeliveryState, false, func(context.Context, sendMessageArgs) sendMessageResult { return sendMessageResult{} })

		// Event-sequence helpers see absent/current/settled generations and both
		// early-break paths without needing a running delegate.
		pending := state
		events := []jobstore.Event{
			{Seq: 1, Kind: jobstore.EventWatchSendPending, WatchSend: &pending},
			{Seq: 2, Kind: jobstore.EventWatchSendDropped, WatchSend: &pending},
			{Seq: 3, Kind: jobstore.EventWatchSendPending, WatchSend: &pending},
			{Seq: 4, Kind: jobstore.EventDelegateStopGateClosed, DelegateID: "other"},
		}
		_, _ = jm.delegateStoppedAfterWatchSendPending("dlg_stale", jobstore.WatchSendState{Key: key, DeliveryID: "absent", UpdateSeq: 99})
		stoppedEvents := []jobstore.Event{{Seq: 1, Kind: jobstore.EventWatchSendPending, WatchSend: &pending}, {Seq: 2, Kind: jobstore.EventJobStarted}, {Seq: 3, Kind: jobstore.EventDelegateStopGateClosed, DelegateID: "other"}, {Seq: 4, Kind: jobstore.EventDelegateStopGateClosed, DelegateID: "dlg_stale"}}
		_ = watchSendPendingCreationSeq(stoppedEvents, state)
		_, _ = jm.delegateStoppedAfterWatchSendPending("dlg_stale", state)
		_ = watchSendPendingCreationSeq(nil, state)
		_ = watchSendPendingCreationSeq(events, state)
		future := append(append([]jobstore.Event{}, events...), jobstore.Event{Seq: 5, Kind: jobstore.EventWatchSendPending, WatchSend: &pending})
		_ = watchSendPendingCreationSeq(future, state)
		_ = watchSendPendingCreationSeq([]jobstore.Event{{Seq: 2, Kind: jobstore.EventJobStarted}, {Seq: 1, Kind: jobstore.EventWatchSendPending, WatchSend: &pending}}, state)
		_ = watchSendEventMatchesState(nil, state)
		wrongDelivery := state
		wrongDelivery.DeliveryID = "wrong"
		_ = watchSendEventMatchesState(&wrongDelivery, state)
		wrongUpdate := state
		wrongUpdate.UpdateSeq++
		_ = watchSendEventMatchesState(&wrongUpdate, state)
		_ = classifyWatchSendDelivery(sendMessageResult{})
		_ = classifyWatchSendDelivery(sendMessageResult{Err: errors.New("busy")})

		// Current-state rejection variants and the terminal-flush exception.
		_ = jm.isCurrentPendingWatchSendLocked(nil, state)
		cfg.rejectingDelivery = true
		_ = jm.isCurrentPendingWatchSendLocked(cfg, state)
		cfg.rejectingDelivery = false
		jm.closing = true
		_ = jm.isCurrentPendingWatchSendLocked(cfg, state)
		jm.closing = false
		bad := state
		bad.UpdateSeq++
		_ = jm.isCurrentPendingWatchSendLocked(cfg, bad)
		jm.terminalFlush = map[*watchConfig]bool{cfg: true}
		_ = jm.isCurrentPendingWatchSendLocked(cfg, state)
		delete(jm.terminalFlush, cfg)
		_ = (*watchConfig)(nil).sendTo()
		_ = cfg.sendTo()
		jm.rememberUnpersistedTerminalPendingWatchSend(nil, state)
		emptyCfg := &watchConfig{}
		jm.rememberUnpersistedTerminalPendingWatchSend(emptyCfg, state)

		// Delivery identity guards, including detached delivery authorization.
		d := watchSendDelivery{cfg: cfg, key: wk, generation: cfg.generation}
		_ = jm.isCurrentWatchSendDeliveryLocked(watchSendDelivery{})
		_ = jm.isCurrentWatchSendDeliveryLocked(watchSendDelivery{cfg: cfg, generation: "wrong"})
		d.allowAfterTerminalExpiry = true
		_ = jm.isCurrentWatchSendDeliveryLocked(d)
		jm.rememberDetachedPendingLocked(cfg)
		_ = jm.isCurrentWatchSendDeliveryLocked(d)

		// A stale update is ignored; a newer update coalesces and can be rolled
		// back to the exact previous runtime state.
		d.allowAfterTerminalExpiry = false
		_ = jm.recordWatchSendPending(state, watchSendDelivery{})
		cfg.settledUpdateSeq = map[jobstore.WatchSendKey]uint64{key: state.UpdateSeq}
		_, _, _ = jm.persistPendingWatchSend(state, watchSendDelivery{cfg: cfg, key: wk, generation: cfg.generation})
		delete(cfg.settledUpdateSeq, key)
		persistCfg, persistState, persistKey := fresh("caller_persist_error")
		jm.appendEvent = func(jobstore.Event) error { return errors.New("persist pending") }
		_, _, _ = jm.persistPendingWatchSend(persistState, watchSendDelivery{cfg: persistCfg, key: persistKey, generation: persistCfg.generation})
		jm.appendEvent = originalAppendEarly
		older := state
		older.UpdateSeq = 1
		_ = jm.recordWatchSendPending(older, d)
		newer := state
		newer.UpdateSeq = 3
		cfg.watchID = "watch_fuse"
		jm.deliveredWatchSendIDs = map[string]struct{}{"prior-delivery": {}}
		newer.Provenance = &provenance.Causal{Chain: []provenance.Entry{{Kind: "watch", WatchID: cfg.watchID, DeliveryID: "prior-delivery"}}}
		record := jm.recordWatchSendPending(newer, d)
		jm.rollbackWatchSendPendingRecord(record)
		jm.rollbackWatchSendPendingRecord(watchSendPendingRecord{})
		emptyCurrent := record
		delete(cfg.pending, key)
		jm.rollbackWatchSendPendingRecord(emptyCurrent)
		cfg.pending[key] = &state
		jm.removeRuntimePendingWatchSend(jobstore.WatchSendState{Key: jobstore.WatchSendKey{WatchTarget: "not-installed"}})
		delete(jm.terminalFlush, cfg)
		delete(jm.watches, wk)
		_ = jm.dropWatchSend(state, cfg, "stale")
		jm.watches[wk] = cfg

		// Overflow walks across an ordered hole before selecting real pending
		// states for eviction.
		overflowCfg := &watchConfig{target: target, send: &watchSendArgs{To: "dlg_sink"}, generation: "overflow", pending: make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)}
		overflowKey := watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_sink"}
		jm.watches[overflowKey] = overflowCfg
		overflowCfg.pendingOrder = append(overflowCfg.pendingOrder, jobstore.WatchSendKey{WatchTarget: "nil-hole"})
		for i := range defaultWatchSendPendingCap {
			k := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedWatchedIdentity: fmt.Sprintf("watched-%d", i), ResolvedSendTo: "dlg_sink"}
			s := jobstore.WatchSendState{Key: k, DeliveryID: fmt.Sprintf("d-%d", i), UpdateSeq: 1, TriggerIdentity: fmt.Sprintf("trigger-%d", i)}
			overflowCfg.pending[k] = &s
			overflowCfg.pendingOrder = append(overflowCfg.pendingOrder, k)
		}
		extraKey := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedWatchedIdentity: "extra", ResolvedSendTo: "dlg_sink"}
		extraState := jobstore.WatchSendState{Key: extraKey, DeliveryID: "extra", UpdateSeq: 1}
		_, _, _ = jm.persistPendingWatchSend(extraState, watchSendDelivery{cfg: overflowCfg, key: overflowKey, generation: "overflow"})
		// Refill the cap and fail the first eviction tombstone after allowing the
		// new pending event to commit, exercising partial persistence cleanup.
		for len(overflowCfg.pending) < defaultWatchSendPendingCap {
			i := len(overflowCfg.pending) + defaultWatchSendPendingCap
			k := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedWatchedIdentity: fmt.Sprintf("refill-%d", i), ResolvedSendTo: "dlg_sink"}
			s := jobstore.WatchSendState{Key: k, DeliveryID: fmt.Sprintf("refill-%d", i), UpdateSeq: 1}
			overflowCfg.pending[k] = &s
			overflowCfg.pendingOrder = append(overflowCfg.pendingOrder, k)
		}
		failCount := 0
		jm.appendEvent = func(e jobstore.Event) error {
			failCount++
			if failCount == 2 {
				return errors.New("eviction append")
			}
			return originalAppendEarly(e)
		}
		failKey := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedWatchedIdentity: "eviction-fail", ResolvedSendTo: "dlg_sink"}
		_, _, _ = jm.persistPendingWatchSend(jobstore.WatchSendState{Key: failKey, DeliveryID: "eviction-fail", UpdateSeq: 1}, watchSendDelivery{cfg: overflowCfg, key: overflowKey, generation: "overflow"})
		jm.appendEvent = originalAppendEarly
		jm.watches[wk] = cfg
		cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
		cfg.pendingOrder = []jobstore.WatchSendKey{key}
		jm.removeRuntimePendingWatchSend(state)

		// Removal covers nil/no-map, missing/stale, ordered deletion, and the
		// bounded settled-key history eviction path.
		removePendingWatchSendLocked(nil, key, 1)
		removePendingWatchSendLocked(&watchConfig{}, key, 1)
		removePendingWatchSendLocked(cfg, key, 1)
		deletePendingWatchSendKeyLocked(nil, key)
		deletePendingWatchSendKeyLocked(&watchConfig{}, key)
		deletePendingWatchSendKeyLocked(cfg, jobstore.WatchSendKey{WatchTarget: "absent"})
		for i := 0; i <= defaultWatchSendPendingCap; i++ {
			settleWatchSendLocked(cfg, jobstore.WatchSendKey{WatchTarget: fmt.Sprintf("settled-%d", i)}, uint64(i+1))
		}
		settleWatchSendLocked(cfg, key, 0)

		// Detached/snapshot match predicates cover receiver, alias, empty send,
		// hidden watch-id, nil entries, and pending-order holes.
		_ = terminalSnapshots([]watchConfigTerminalSnapshot{{terminal: watchSendTerminalSnapshot{cfg: cfg}}})
		markWatchConfigSnapshotsRejectingLocked([]watchConfigTerminalSnapshot{{}, {cfg: cfg}})
		rollbackWatchConfigSnapshotsRejectingLocked(jm, []watchConfigTerminalSnapshot{{key: wk, cfg: cfg}})
		markWatchConfigsRejectingLocked([]*watchConfig{nil, cfg})
		rollbackWatchConfigsRejectingLocked(jm, []*watchConfig{nil, cfg})
		cfg.rejectingDelivery = true
		jm.terminalFlush[cfg] = true
		jm.rollbackWatchConfigsRejecting([]*watchConfig{cfg})
		cfg.rejectingDelivery = false
		cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
		cfg.pendingOrder = []jobstore.WatchSendKey{{WatchTarget: "hole"}, key}
		_ = watchSendTerminalSnapshotsLocked(nil, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotsLocked(&watchConfig{}, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotMatchingKeyLocked(cfg, watchKey{VisibleSessionID: jm.sessionID, Target: "other"}, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotMatchingKeyLocked(cfg, watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: runtimeMessageAliasWatched}, jobstore.EventWatchSendDropped, "x", now)
		aliasHole := jobstore.WatchSendKey{VisibleSessionID: jm.sessionID, WatchTarget: target, ResolvedSendTo: "dlg_sink"}
		cfg.pendingOrder = append([]jobstore.WatchSendKey{aliasHole}, cfg.pendingOrder...)
		_ = watchSendTerminalSnapshotMatchingKeyLocked(cfg, watchKey{VisibleSessionID: jm.sessionID, Target: target, SendTo: "dlg_sink"}, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotMatchingKeyLocked(nil, wk, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendTerminalSnapshotMatchingKeyLocked(&watchConfig{}, wk, jobstore.EventWatchSendDropped, "x", now)
		_ = watchSendKeyMatchesWatchKey(key, watchKey{VisibleSessionID: "other", Target: target})
		_ = watchConfigMatchesWatchKey(nil, wk)
		_ = watchConfigMatchesWatchKey(cfg, watchKey{Target: "other"})
		_ = watchConfigMatchesWatchKey(cfg, watchKey{Target: target})
		_ = watchConfigMatchesWatchKey(cfg, watchKey{Target: target, SendTo: runtimeMessageAliasWatched})
		_ = watchConfigSendToMatchesWatchKey(cfg, watchKey{Target: target, SendTo: "other", ReceiverSessionID: "wrong"})
		_ = watchConfigSendToMatchesWatchKey(cfg, watchKey{Target: target})
		_ = watchConfigReceiverMatchesWatchKey(nil, wk)

		jm.terminalFlush[nil] = true
		_, _ = jm.detachedWatchSendTerminalSnapshotsLocked(watchKey{Target: "no-match"}, jobstore.EventWatchSendDropped, "x", now)
		emptyMatch := &watchConfig{target: target, send: &watchSendArgs{To: "dlg_sink"}}
		jm.terminalFlush[emptyMatch] = true
		_, _ = jm.detachedWatchSendTerminalSnapshotsLocked(watchKey{Target: target, SendTo: "dlg_sink"}, jobstore.EventWatchSendDropped, "x", now)
		jm.terminalFlush[cfg] = true
		cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
		cfg.pendingOrder = []jobstore.WatchSendKey{key}
		_, _ = jm.detachedWatchSendTerminalSnapshotsLocked(wk, jobstore.EventWatchSendDropped, "x", now)
		_, _, _ = jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked("", jobstore.EventWatchSendDropped, "x", now, nil)
		_, _, _ = jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked("missing", jobstore.EventWatchSendDropped, "x", now, nil)
		cfg.watchID = "watch"
		hiddenCfg := &watchConfig{watchID: "hidden"}
		jm.terminalFlush[hiddenCfg] = true
		_, _, _ = jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked("hidden", jobstore.EventWatchSendDropped, "x", now, func(*watchConfig) bool { return false })

		// Durable append failures return the successfully applied prefix and
		// removal tolerates terminal events without watch-send payloads.
		originalAppend := jm.appendEvent
		calls := 0
		jm.appendEvent = func(e jobstore.Event) error {
			calls++
			if calls == 2 {
				return errors.New("injected append failure")
			}
			return originalAppend(e)
		}
		snap := watchSendTerminalSnapshot{cfg: cfg, events: []jobstore.Event{{Kind: jobstore.EventWatchSendDropped, WatchSend: &state}, {Kind: jobstore.EventWatchSendDropped, WatchSend: &state}}}
		applied, _ := jm.appendWatchSendTerminalSnapshots([]watchSendTerminalSnapshot{snap})
		jm.appendEvent = originalAppend
		jm.removeWatchSendTerminalSnapshots([]watchSendTerminalSnapshot{{cfg: cfg, events: []jobstore.Event{{}, {WatchSend: &state}}}})
		jm.removeWatchSendTerminalSnapshots(applied)

		jm.appendEvent = func(jobstore.Event) error { return errors.New("append") }
		_ = jm.appendWatchSendEvents([]jobstore.Event{{Kind: jobstore.EventWatchSendPending}})
		jm.appendEvent = originalAppend
		_ = jm.appendWatchSendPendingState(nil, state)

		// Drop failure is exercised while the state is current; restoring the
		// append seam leaves the real store usable for cleanup.
		cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
		cfg.pendingOrder = []jobstore.WatchSendKey{key}
		jm.watches[wk] = cfg
		jm.appendEvent = func(jobstore.Event) error { return errors.New("drop append") }
		_ = jm.dropWatchSend(state, cfg, "failed")
		jm.appendEvent = originalAppend

		jm.terminalFlush = map[*watchConfig]bool{cfg: true}
		cfg.pending = nil
		jm.forgetDetachedWatchSendConfigsIfEmpty([]*watchConfig{nil, cfg})
		jm.rememberDetachedPendingLocked(nil)
		closeCfg := &watchConfig{}
		jm.closeWatchConfigSnapshots([]watchConfigTerminalSnapshot{{}, {cfg: closeCfg}})

		// A closed real store provides the external filesystem fault boundary for
		// the direct event-load error path. newTestJM cleanup tolerates re-close.
		if err := jm.store.Close(); err != nil {
			t.Fatal(err)
		}
		_, _ = jm.delegateStoppedAfterWatchSendPending("dlg_stale", state)
		closedCfg, closedState, _ := fresh("dlg_closed")
		_, _ = jm.deliverPendingWatchSend(t.Context(), closedCfg, closedState, false, func(context.Context, sendMessageArgs) sendMessageResult { return sendMessageResult{} })
	})
}
