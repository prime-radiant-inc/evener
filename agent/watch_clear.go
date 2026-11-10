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

// countWatchDeliveryLocked increments the model-facing delivery count for cfg
// and appends this delivery's instant to its bounded timeline ring, stamped
// from the job manager's clock. Counting and stamping happen together here so
// the count and the ring can never diverge. The caller must hold jm.mu.
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

// hasArmedWatch reports whether any watch on this manager could still deliver
// a wake. The count is receiver-agnostic on purpose: the caller asks "could
// anything here deliver a wake to a session it holds," so overcounting is safe
// and undercounting is the only error.
func (jm *jobManager) hasArmedWatch() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.hasArmedWatchLocked()
}

// hasArmedWatchLocked reports whether any registered watch is still armed. A
// spent one-shot no longer fires, so only the not-yet-fired ones count. Caller
// holds jm.mu.
func (jm *jobManager) hasArmedWatchLocked() bool {
	for _, cfg := range jm.watches {
		if !watchOneShotHasFired(cfg) {
			return true
		}
	}
	return false
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
