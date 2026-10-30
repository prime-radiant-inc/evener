package agent

import (
	"fmt"
	"time"

	"primeradiant.com/evener/agent/events"
)

func (jm *jobManager) startProgressTimer(key watchKey, cfg *watchConfig, stop <-chan struct{}) {
	if cfg == nil || cfg.progressIntervalMS <= 0 || stop == nil {
		return
	}
	interval := time.Duration(cfg.progressIntervalMS) * time.Millisecond

	go func() {
		ticker := jm.clock.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C():
				if !jm.fireProgressTick(key, cfg) {
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

// progressTickSnapshot is the read-only view fireProgressTick gathers under the
// lock before consulting decideProgressTick. target is the raw watch target,
// echoed into the notification job id for non-session targets. oneShot marks a
// one-shot timer, whose single fire is also its last.
type progressTickSnapshot struct {
	closing         bool
	stillRegistered bool
	sessionTarget   bool
	targetRunning   bool
	hasSend         bool
	oneShot         bool
	firedPendingEnd bool
	target          string
}

// progressTickDecision is what decideProgressTick returns for one tick: whether
// the timer goroutine keeps running (keepAlive), whether this tick delivers
// (fire) and how it routes — a watch send (sendDelivery) or a notification that
// counts a delivery (recordBudget), plus the notification job id. A periodic
// tick is not a condition fire, so its delivery never counts against the budget.
// endOneShot asks the caller to end a fired one-shot: set together with fire on
// its first delivery, and alone when a previous end failed to persist and this
// tick only retries the teardown.
type progressTickDecision struct {
	keepAlive    bool
	fire         bool
	sendDelivery bool
	recordBudget bool
	endOneShot   bool
	notifyJobID  string
}

// decideProgressTick is the pure decision core lifted out of fireProgressTick. It
// gates a progress tick on manager/registration/liveness and reports
// how the surviving tick routes. It locks nothing and mutates nothing; the caller
// performs the effects. A send tick and a budget-counted notification are mutually
// exclusive. A gated-out tick neither fires nor keeps the goroutine alive; a
// one-shot timer's tick fires and then ends the goroutine, and a one-shot whose
// end could not persist keeps the goroutine alive without firing until the
// retry lands, so fire alone — not keepAlive — decides whether this tick
// delivers.
func decideProgressTick(snap progressTickSnapshot) progressTickDecision {
	if snap.closing || !snap.stillRegistered {
		return progressTickDecision{}
	}
	// A one-shot that already fired and could not persist its end retries the
	// teardown on this tick and delivers nothing. The retry outranks the
	// liveness gate below: the watch is registered and owes an end, whatever
	// its target is now doing.
	if snap.firedPendingEnd {
		return progressTickDecision{keepAlive: true, endOneShot: true}
	}
	if !snap.sessionTarget && !snap.targetRunning {
		return progressTickDecision{}
	}
	dec := progressTickDecision{keepAlive: !snap.oneShot, fire: true, endOneShot: snap.oneShot}
	if snap.hasSend {
		dec.sendDelivery = true
		return dec
	}
	dec.recordBudget = true
	if !snap.sessionTarget {
		dec.notifyJobID = snap.target
	}
	return dec
}

func (jm *jobManager) fireProgressTick(key watchKey, cfg *watchConfig) bool {
	defer jm.publishWatchActivity()
	release, err := jm.beginRetirementMutation("watch")
	if err != nil {
		// Refusal must not consume the firing or end its registration. If a
		// tentative claim aborts, the existing ticker retries normally.
		return true
	}
	defer release()
	var notifications []jobNotification
	var deliveries []watchSendDelivery

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, cfg.target)}
	jm.mu.Lock()
	snap := progressTickSnapshot{
		closing:         jm.closing,
		stillRegistered: jm.watches[key] == cfg,
		sessionTarget:   isWatchSessionTarget(cfg.target),
		targetRunning:   jm.running[cfg.target] != nil,
		hasSend:         cfg.send != nil,
		oneShot:         cfg.oneShot,
		firedPendingEnd: cfg.firedPendingEnd,
		target:          cfg.target,
	}
	dec := decideProgressTick(snap)
	if !dec.fire && !dec.endOneShot {
		jm.mu.Unlock()
		return false
	}
	if dec.fire {
		// This tick is the cadence's own fire instant, whether it routes to a
		// send rail (whose delivery is counted only when the frame settles) or
		// to the notification counted below. The delivery ring cannot serve as
		// the clock's history; see lastClockFire.
		cfg.lastClockFire = jm.now()
	}
	if dec.sendDelivery {
		deliveries = append(deliveries, jm.watchSendSnapshot(cfg, cfg.target, "progress_tick", root).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, root.Provenance)))
	}
	if dec.recordBudget {
		reason := "progress_tick"
		if cfg.timer {
			reason = "repeat"
			if cfg.oneShot {
				reason = "after"
			}
		}
		n := jm.watchNotificationFromWatch(cfg, dec.notifyJobID, reason, root.Provenance)
		if cfg.timer {
			n.WatchID, n.Fires, n.IntervalSeconds, n.Terminal = cfg.watchID, 1, cfg.timerSeconds, cfg.oneShot
		}
		notifications = append(notifications, n)
		// Periodic ticks never trip the condition-fire budget.
		jm.countWatchDeliveryLocked(cfg)
	}
	jm.mu.Unlock()

	// A one-shot ends on the tick that fires it, or on a later tick retrying an
	// end that did not persist. An end that lands stops the ticker with the
	// watch; one that fails keeps it armed for the next retry.
	if dec.endOneShot {
		dec.keepAlive = !jm.endFiredOneShot(cfg)
		if dec.keepAlive {
			jm.markOneShotFiredPendingEnd(cfg)
		}
	}
	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	return dec.keepAlive
}

// endFiredOneShot retires a one-shot timer after its only fire through the
// same snapshot, persist, detach sequence clearWatch uses, recorded with end
// reason "fired" so history distinguishes it from a clear. Called with jm.mu
// released; the fire's notification is enqueued by the caller afterwards. It
// reports whether the end persisted; a failure is warned about and left for the
// caller to retry.
func (jm *jobManager) endFiredOneShot(cfg *watchConfig) bool {
	if _, err := jm.clearWatchByIDMatchingWithReason(cfg.watchID, func(c *watchConfig) bool { return c == cfg }, true, "fired"); err != nil {
		if jm.emit != nil {
			jm.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("job_watch: one-shot %s fired but its teardown did not persist: %v", cfg.watchID, err),
			}, nil)
		}
		return false
	}
	return true
}

// markOneShotFiredPendingEnd records that a one-shot has spent its single fire
// and still owes a durable end, so the next tick retries the teardown without
// delivering again.
func (jm *jobManager) markOneShotFiredPendingEnd(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg.firedPendingEnd = true
}
