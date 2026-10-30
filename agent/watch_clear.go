package agent

import (
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func (jm *jobManager) clearWatch(key watchKey) (watchResult, error) {
	defer jm.publishWatchActivity()
	var targets []watchConfigTerminalSnapshot
	jm.mu.Lock()
	detachedCfgs, detached := jm.detachedWatchSendTerminalSnapshotsLocked(key, jobstore.EventWatchSendDropped, "watch cleared", jm.now())
	if key.SendTo != "" {
		cfg := jm.watches[key]
		targets = append(targets, watchConfigTerminalSnapshot{
			key:       key,
			cfg:       cfg,
			terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
			endReason: "cleared",
		})
	} else {
		for existingKey, cfg := range jm.watches {
			if watchKeyMatchesClearRequest(existingKey, key) {
				targets = append(targets, watchConfigTerminalSnapshot{
					key:       existingKey,
					cfg:       cfg,
					terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
					endReason: "cleared",
				})
			}
		}
	}
	teardown := watchTeardown{targets: targets, detachedCfgs: detachedCfgs}
	teardown.markRejectingLocked()
	jm.mu.Unlock()
	dropped := append(terminalSnapshots(targets), detached...)
	if err := jm.appendWatchTeardownBatch(dropped, targets); err != nil {
		teardown.rollback(jm)
		return watchResult{}, err
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)

	return watchResult{
		Source:   sourcePublicForClearedWatch(key, targets),
		Target:   key.Target,
		Watching: false,
	}, nil
}

func (jm *jobManager) clearWatchByID(watchID string) (watchResult, error) {
	return jm.clearWatchByIDMatching(watchID, func(cfg *watchConfig) bool {
		return watchConfigVisibleToSession(cfg, jm.sessionID)
	}, true)
}

func (jm *jobManager) clearWatchByIDMatching(watchID string, allow func(*watchConfig) bool, allowDurable bool) (watchResult, error) {
	return jm.clearWatchByIDMatchingWithReason(watchID, allow, allowDurable, "cleared")
}

// clearWatchByIDMatchingWithReason is the teardown clearWatchByIDMatching runs,
// with the end reason recorded in the durable clear event and the history ring
// as a parameter: a one-shot timer retires through the same sequence but is
// recorded as "fired" rather than "cleared".
func (jm *jobManager) clearWatchByIDMatchingWithReason(watchID string, allow func(*watchConfig) bool, allowDurable bool, endReason string) (watchResult, error) {
	defer jm.publishWatchActivity()
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if ok && !allow(cfg) {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	detachedCfgs, detached, hiddenDetached := jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID, jobstore.EventWatchSendDropped, "watch cleared", jm.now(), allow)
	markWatchConfigsRejectingLocked(detachedCfgs)
	if !ok {
		jm.mu.Unlock()
		if hiddenDetached || !allowDurable {
			if len(detachedCfgs) == 0 {
				return watchResult{WatchID: watchID, Watching: false}, nil
			}
		}
		var clearEvent *jobstore.Event
		var clearReceiver string
		if allowDurable {
			var err error
			clearEvent, clearReceiver, err = jm.durableWatchClearEvent(watchID, endReason)
			if err != nil {
				jm.rollbackWatchConfigsRejecting(detachedCfgs)
				return watchResult{}, err
			}
		}
		if len(detachedCfgs) == 0 && clearEvent == nil {
			return watchResult{WatchID: watchID, Watching: false}, nil
		}
		dropped := detached
		events := watchSendTerminalEvents(dropped)
		if clearEvent != nil {
			events = append(events, *clearEvent)
		}
		if err := jm.appendWatchRegistryEvents(events); err != nil {
			jm.rollbackWatchConfigsRejecting(detachedCfgs)
			return watchResult{}, err
		}
		if clearEvent != nil {
			jm.noteWatchActivity(clearReceiver)
		}
		jm.removeWatchSendTerminalSnapshots(dropped)
		jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	targets := []watchConfigTerminalSnapshot{{
		key:       key,
		cfg:       cfg,
		terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
		endReason: endReason,
	}}
	teardown := watchTeardown{targets: targets, detachedCfgs: detachedCfgs}
	teardown.markRejectingLocked()
	jm.mu.Unlock()

	dropped := append(terminalSnapshots(targets), detached...)
	if err := jm.appendWatchTeardownBatch(dropped, targets); err != nil {
		teardown.rollback(jm)
		return watchResult{}, err
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)

	return watchResult{
		WatchID:  watchID,
		Source:   cfg.sourcePublic,
		Target:   key.Target,
		Watching: false,
	}, nil
}

// watchConfigByIDWithFlushLocked resolves a watch config by ID, including
// detached terminal-flush configs. Callers must hold jm.mu.
func (jm *jobManager) watchConfigByIDWithFlushLocked(watchID string) (*watchConfig, bool) {
	if _, cfg, ok := jm.watchConfigByIDLocked(watchID); ok {
		return cfg, true
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID {
			return cfg, true
		}
	}
	return nil, false
}

// watchReceiverIdentity looks a watch up by ID ignoring session visibility —
// unlike hasWatchID, which returns the visibility verdict. Returns the watch's
// receiver identity so a session holding a receiver-keyed watch it cannot see
// can decide, from tree topology, whether it may route that watch's clear to
// the receiver.
func (jm *jobManager) watchReceiverIdentity(watchID string) (receiverSessionID, receiverDelegateID string, found bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg, found := jm.watchConfigByIDWithFlushLocked(watchID)
	if !found || cfg == nil {
		return "", "", false
	}
	return cfg.receiverSessionID, cfg.receiverDelegateID, true
}

func (jm *jobManager) hasWatchID(watchID string) (bool, error) {
	jm.mu.Lock()
	if _, cfg, ok := jm.watchConfigByIDLocked(watchID); ok {
		jm.mu.Unlock()
		return watchConfigVisibleToSession(cfg, jm.sessionID), nil
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID {
			jm.mu.Unlock()
			return watchConfigVisibleToSession(cfg, jm.sessionID), nil
		}
	}
	jm.mu.Unlock()

	watches, err := jm.store.LoadWatches()
	if err != nil {
		return false, err
	}
	w := watches[watchID]
	return w != nil && w.Active, nil
}

func (jm *jobManager) clearReceiverWatchByID(watchID, receiverSessionID, receiverDelegateID string) (watchResult, error) {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return watchResult{}, errors.New("source_not_watchable: parent watch observer session is unknown")
	}

	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok {
		for terminalCfg := range jm.terminalFlush {
			if terminalCfg != nil && terminalCfg.watchID == watchID {
				cfg = terminalCfg
				ok = true
				break
			}
		}
	}
	if !ok {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	if cfg.receiverSessionID != receiverSessionID || cfg.receiverDelegateID != receiverDelegateID {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	jm.mu.Unlock()
	return jm.clearWatchByIDMatching(watchID, func(cfg *watchConfig) bool {
		return watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID)
	}, false)
}

func sourcePublicForClearedWatch(key watchKey, targets []watchConfigTerminalSnapshot) string {
	for _, target := range targets {
		if watchKeyMatchesClearRequest(target.key, key) && target.cfg != nil && target.cfg.sourcePublic != "" {
			return target.cfg.sourcePublic
		}
	}
	return ""
}

func watchKeyMatchesClearRequest(candidate, request watchKey) bool {
	if candidate.Slot != request.Slot {
		return false
	}
	if candidate.VisibleSessionID != request.VisibleSessionID || candidate.Target != request.Target {
		return false
	}
	if request.SendTo != "" && candidate.SendTo != request.SendTo {
		return false
	}
	if request.ReceiverSessionID != "" && candidate.ReceiverSessionID != request.ReceiverSessionID {
		return false
	}
	if request.ReceiverDelegateID != "" && candidate.ReceiverDelegateID != request.ReceiverDelegateID {
		return false
	}
	return true
}

func (jm *jobManager) durableWatchClearEvent(watchID, endReason string) (*jobstore.Event, string, error) {
	watches, err := jm.store.LoadWatches()
	if err != nil {
		return nil, "", err
	}
	w := watches[watchID]
	if w == nil || !w.Active || w.Generation == "" {
		return nil, "", nil
	}
	return &jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      jm.now(),
		WatchID: watchID,
		Watch: &jobstore.WatchEvent{
			Generation: w.Generation,
			EndReason:  endReason,
		},
	}, w.ReceiverSessionID, nil
}

// tripConditionFireBudgetLocked latches the condition-fire circuit breaker for
// cfg and reports whether THIS call tripped it. The test is "at or past the
// budget", latched by budgetTripped to exactly one true per cfg lifetime.
// Equality would be wrong for the send rail: a send watch counts its fire when
// it SNAPSHOTS a frame and counts its delivery when that frame SETTLES, so two
// fires snapshotted before either settles walk conditionFires from 49 to 51 and
// every later call would compare a counter that already stepped over the
// budget.
//
// Both ends of the send rail consult it: the observation side, where the match
// is counted, so a watch whose frames never settle still trips; and the settle
// side, which keeps its delivery accounting. The latch is what makes the second
// of those a no-op rather than a second teardown.
//
// The caller must hold jm.mu. A true result means the caller should schedule
// autoClearWatchOverBudget(cfg) AFTER releasing jm.mu (the auto-clear does
// durable I/O and re-takes jm.mu; it must never run from inside an
// observation's critical section, spec §3).
func tripConditionFireBudgetLocked(cfg *watchConfig) (crossedBudget bool) {
	if cfg == nil || cfg.conditionFires < watchDeliveryBudget || cfg.budgetTripped {
		return false
	}
	cfg.budgetTripped = true
	return true
}

// noteConditionFireLocked offers one condition match to cfg's breaker. It is the
// ONLY place conditionFires moves: every match site — the event rail, the live
// output rail, and both attach-scan rails — goes through it, so no path can
// count a fire without consulting the breaker.
//
// accepted=false means the breaker has already latched and the caller must build
// NOTHING from this match. The window it closes is the teardown's own durable
// append: the config is not detached until that append returns, so it is still
// in jm.watches and still matching. rejectingDelivery stops the send rail at the
// delivery gate, but a no-send watch's notification has no such gate, so the
// refusal has to happen where the match is counted. The match that crosses the
// budget is itself accepted — the budget is a count of allowed fires, and that
// one is the last of them.
//
// crossedBudget=true means the caller must schedule autoClearWatchOverBudget(cfg)
// after releasing jm.mu, on tripConditionFireBudgetLocked's terms. The caller
// must hold jm.mu.
func noteConditionFireLocked(cfg *watchConfig) (accepted, crossedBudget bool) {
	if cfg == nil || cfg.budgetTripped {
		return false, false
	}
	cfg.conditionFires++
	return true, tripConditionFireBudgetLocked(cfg)
}

// countWatchDeliveryLocked increments the model-facing delivery count for cfg
// and appends this delivery's instant to its bounded timeline ring, stamped
// from the job manager's clock. Counting and stamping happen together here so
// the count and the ring can never diverge. It says nothing about the breaker:
// the budget bounds CONDITION fires, so a periodic progress tick counts a
// delivery here and never trips anything, and a condition fire's crossing is
// reported by noteConditionFireLocked at the match. The caller must hold jm.mu.
func (jm *jobManager) countWatchDeliveryLocked(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	cfg.deliveries++
	jm.noteWatchActivity(cfg.receiverSessionID)
	now := jm.now()
	if len(cfg.deliveryTimes) == watchDeliveryTimeCap {
		copy(cfg.deliveryTimes, cfg.deliveryTimes[1:])
		cfg.deliveryTimes[watchDeliveryTimeCap-1] = now
		return
	}
	cfg.deliveryTimes = append(cfg.deliveryTimes, now)
}

// recordWatchDeliveryLocked counts a delivery at the send rail's settle end and
// reports whether it crossed the condition-fire budget. The send rail counts its
// fire when it snapshots a frame and its delivery when that frame settles, so
// this is the second of the two ends that can report the crossing; the latch
// keeps the pair to one teardown. The caller must hold jm.mu.
func (jm *jobManager) recordWatchDeliveryLocked(cfg *watchConfig) (crossedBudget bool) {
	jm.countWatchDeliveryLocked(cfg)
	return tripConditionFireBudgetLocked(cfg)
}

// watchKeyForConfigLocked finds the live map key holding cfg. ok=false means cfg
// is no longer in jm.watches (already cleared, replaced, or detached). The caller
// must hold jm.mu.
func watchKeyForConfigLocked(jm *jobManager, cfg *watchConfig) (watchKey, bool) {
	for key, c := range jm.watches {
		if c == cfg {
			return key, true
		}
	}
	return watchKey{}, false
}

// autoClearWatchOverBudgetNotification tears down exactly the one watch config
// that tripped the delivery budget and returns its ONE final cleared notification
// without enqueuing or waking. It is the circuit breaker's teardown: jm-state
// mutation only — NO delivery from observation (spec §3). It works on the single
// (key, cfg) pair the breaker latched, so a watch sharing a target with others
// does not over-clear its neighbors.
//
// The sequence is mark, persist, detach. It takes no terminal snapshot and drops
// nothing, which is what separates it from a model-requested clear: the budget
// bounds how many times a watch may FIRE, and a frame it already produced —
// including the one whose match crossed the budget, which every rail records
// before calling this — is persisted as pending and owed to its receiver. All
// that is persisted here is the watch's own cleared event; the detached config
// keeps its frames and goes onto the terminal-flush rail, exactly where a watch
// auto-removed by its target's exit parks them. rejectingDelivery is the freeze
// that holds across the durable append, so no fire past the crossing can add a
// frame between the mark and the detach; detachOverBudgetWatch lifts it.
//
// The reverse lookup under jm.mu doubles as the no-double-fire latch: once the
// cfg is detached, a later in-flight settle that increments past the budget
// finds no live key and returns without re-notifying.
func (jm *jobManager) autoClearWatchOverBudgetNotification(cfg *watchConfig) (jobNotification, bool) {
	defer jm.publishWatchActivity()
	jm.mu.Lock()
	key, ok := watchKeyForConfigLocked(jm, cfg)
	if !ok {
		jm.mu.Unlock()
		return jobNotification{}, false
	}
	targets := []watchConfigTerminalSnapshot{{key: key, cfg: cfg, endReason: "budget_exhausted"}}
	markWatchConfigSnapshotsRejectingLocked(targets)
	jm.mu.Unlock()

	if err := jm.appendWatchTeardownBatch(nil, targets); err != nil {
		jm.rollbackWatchBudgetTeardown(targets, cfg)
		return jobNotification{}, false
	}
	jm.detachOverBudgetWatch(targets[0])

	return jm.watchNotificationFromWatch(cfg, "", watchBudgetClearedMessage(cfg.target), nil), true
}

// detachOverBudgetWatch removes the over-budget config from the live set and, in
// the SAME critical section, releases it onto the terminal-flush rail, where
// drains, restore, and clear-by-id can still see and settle the frames it
// already produced. The rejecting mark comes off HERE and not earlier: it is
// what froze the config while the teardown persisted, so no fire past the
// crossing could slip a frame in, and a config still marked rejecting delivers
// nothing. Detached, it can gain no new frames — a delivery snapshotted before
// the teardown no longer resolves to a live watch. A config with nothing pending
// is left off the rail: terminalFlush holds flushing configs, not ended ones.
//
// The release is gated on this call being the one that detached the config, and
// shares its critical section for the same reason. A model-requested clear that
// won the key owns that config: it has already dropped those frames under its
// own rejecting mark, and unmarking it here would hand a config that is in
// neither jm.watches nor terminalFlush back to a fire still in flight.
func (jm *jobManager) detachOverBudgetWatch(target watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if !jm.detachWatchConfigSnapshotLocked(target) {
		return
	}
	cfg := target.cfg
	cfg.rejectingDelivery = false
	if len(cfg.pending) == 0 {
		return
	}
	jm.rememberDetachedPendingLocked(cfg)
}

// rollbackWatchBudgetTeardown undoes a budget teardown that did not persist:
// the rejecting marks come off and the once-only budget latch is re-armed. The
// rollback leaves the watch live and still over budget, so the latch has to come
// off with the rejecting marks — held set, no later condition fire would report
// a crossing and nothing would ever retry the auto-clear. Both flip in ONE jm.mu
// critical section: a condition fire that landed between two acquisitions would
// find delivery re-enabled with the latch still set and skip its own teardown.
// Only the failed-teardown path calls this; a teardown that lands detaches the
// config and the latch goes with it.
func (jm *jobManager) rollbackWatchBudgetTeardown(targets []watchConfigTerminalSnapshot, cfg *watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchBudgetTeardownLocked(jm, targets, cfg)
}

// rollbackWatchBudgetTeardownLocked is rollbackWatchBudgetTeardown's body. The
// caller must hold jm.mu.
func rollbackWatchBudgetTeardownLocked(jm *jobManager, targets []watchConfigTerminalSnapshot, cfg *watchConfig) {
	rollbackWatchConfigSnapshotsRejectingLocked(jm, targets)
	if cfg != nil {
		cfg.budgetTripped = false
	}
}

// autoClearWatchOverBudget is the standalone wrapper for attach scans and watch
// sends, where there is no same-event notification batch to extend.
func (jm *jobManager) autoClearWatchOverBudget(cfg *watchConfig) {
	notification, ok := jm.autoClearWatchOverBudgetNotification(cfg)
	if !ok {
		return
	}
	jm.enqueueWatchNotifications([]jobNotification{notification})
	jm.kick()
}

// autoClearOverBudgetWatches runs autoClearWatchOverBudget for each cfg that
// crossed the budget during a single observation pass. Call it AFTER releasing
// the observation's jm.mu critical section, alongside enqueueWatchNotifications.
func (jm *jobManager) autoClearOverBudgetWatches(cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		jm.autoClearWatchOverBudget(cfg)
	}
}

func (jm *jobManager) hasWatchClearState(key watchKey) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if key.SendTo != "" {
		if jm.watches[key] != nil {
			return true
		}
	} else {
		for existingKey := range jm.watches {
			if watchKeyMatchesClearRequest(existingKey, key) {
				return true
			}
		}
	}
	for cfg := range jm.terminalFlush {
		if watchConfigHasPendingMatchingKey(cfg, key) {
			return true
		}
	}
	return false
}

func watchConfigHasPendingMatchingKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil || len(cfg.pending) == 0 {
		return false
	}
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	for pendingKey := range cfg.pending {
		if watchSendKeyMatchesWatchKey(pendingKey, key) {
			return true
		}
	}
	return false
}

// liveTimerCountLocked counts installed timer watches. Caller holds jm.mu.
func (jm *jobManager) liveTimerCountLocked() int {
	n := 0
	for _, cfg := range jm.watches {
		if cfg.timer {
			n++
		}
	}
	return n
}

func (jm *jobManager) watchConfigByIDLocked(watchID string) (watchKey, *watchConfig, bool) {
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID {
			return key, cfg, true
		}
	}
	return watchKey{}, nil, false
}

func (jm *jobManager) pruneWatchedTargetWatchesLocked(jobID, reason string, now time.Time) []watchConfigTerminalSnapshot {
	var targets []watchConfigTerminalSnapshot
	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		targets = append(targets, watchConfigTerminalSnapshot{
			key:      key,
			cfg:      cfg,
			terminal: watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, reason, now),
		})
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	return targets
}

func (cfg *watchConfig) initProgressStop() chan struct{} {
	if cfg.progressIntervalMS > 0 {
		cfg.progressStop = make(chan struct{})
	}
	return cfg.progressStop
}

func closeWatchConfig(cfg *watchConfig) {
	if cfg == nil || cfg.progressStop == nil {
		return
	}
	close(cfg.progressStop)
	cfg.progressStop = nil
}
