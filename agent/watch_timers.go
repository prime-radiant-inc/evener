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
//
// The output fields answer one question: did the watched job produce anything
// since the model last heard from this watch? targetOutputBytes is the job's
// lifetime stream length (jobstore.OutputStore.Len), the same offset space the
// output matcher scans in, and lastTickOutputBytes is that length at the last
// DELIVERED tick. timer and sessionTarget both mark ticks whose cadence is time
// rather than output: a timer's repeat is its own contract, and a session has no
// output stream to grow. targetTransitioning marks a target that is stopping,
// finalizing, or already terminal — a state change the model must not miss.
type progressTickSnapshot struct {
	closing             bool
	stillRegistered     bool
	sessionTarget       bool
	targetRunning       bool
	targetTransitioning bool
	hasSend             bool
	oneShot             bool
	firedPendingEnd     bool
	timer               bool
	tickFired           bool
	targetOutputBytes   int64
	lastTickOutputBytes int64
	target              string
}

// progressTickDecision is what decideProgressTick returns for one tick: whether
// the timer goroutine keeps running (keepAlive), whether this tick delivers
// (fire) and how it routes — a watch send (sendDelivery) or a notification that
// counts a delivery (recordBudget), plus the notification job id. A periodic
// tick is not a condition fire, so its delivery never counts against the budget.
// endOneShot asks the caller to end a fired one-shot: set together with fire on
// its first delivery, and alone when a previous end failed to persist and this
// tick only retries the teardown.
//
// coalesce is the tick that is recorded but not delivered: the watch fired on
// its own cadence and stays armed, but carried nothing the model has not already
// been told, so it produces no notification and no delivery. It is mutually
// exclusive with fire.
type progressTickDecision struct {
	keepAlive    bool
	fire         bool
	coalesce     bool
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
//
// A progress-interval tick that would carry no new output coalesces instead of
// delivering. "No new output" is a job-targeted tick whose target has produced
// no byte since the last delivered tick, so re-waking the model would tell it
// nothing it did not already know. Four things are never coalesced, and each
// protects an invariant rather than a preference: the first tick (!tickFired),
// because the model must see the output's content at least once; a target in
// transition, because a terminal state change is not a quiet interval; and
// timer and session-target ticks, whose cadence is time rather than output. A
// coalesced tick still keeps the goroutine alive and still counts as a fire, so
// the watch is never inert.
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
	// Output coalescing: only a job-targeted tick that has already delivered has
	// a baseline to compare against, and only a target that is not changing state
	// is quiet in the sense that matters. The cadence stays armed either way.
	if !snap.timer && !snap.sessionTarget && !snap.targetTransitioning && snap.tickFired &&
		snap.targetOutputBytes <= snap.lastTickOutputBytes {
		return progressTickDecision{keepAlive: true, coalesce: true}
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

// runOutputBytesLocked reports a running job's lifetime output length — the same
// offset space the output matcher scans in — or 0 for a nil run. It is the
// cursor a progress tick compares against, not a new one: jobstore.OutputStore
// already owns the count, and liveJobRecords reports the same value as a running
// job's OutputBytes. The store's mutex is independent of jm.mu and takes no
// callback into the manager, so reading it under jm.mu cannot invert a lock
// order. The caller must hold jm.mu.
func runOutputBytesLocked(run *runningJob) int64 {
	if run == nil || run.output == nil {
		return 0
	}
	return run.output.Len()
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
	run := jm.running[cfg.target]
	snap := progressTickSnapshot{
		closing:           jm.closing,
		stillRegistered:   jm.watches[key] == cfg,
		sessionTarget:     isWatchSessionTarget(cfg.target),
		targetRunning:     run != nil,
		hasSend:           cfg.send != nil,
		oneShot:           cfg.oneShot,
		firedPendingEnd:   cfg.firedPendingEnd,
		timer:             cfg.timer,
		tickFired:         cfg.tickFired,
		targetOutputBytes: runOutputBytesLocked(run),
		target:            cfg.target,
	}
	snap.lastTickOutputBytes = cfg.lastTickOutputBytes
	snap.targetTransitioning = run != nil && (run.terminal != nil || run.finalize != nil || run.stopStatus != "")
	dec := decideProgressTick(snap)
	if dec.coalesce {
		// A coalesced tick is a fire the model does not hear: the watch ran on its
		// cadence and stays armed, so the cadence's own history advances. It
		// delivers nothing, so it counts no delivery and leaves the baseline
		// alone — the next tick still compares against the last output the model
		// was actually told about. Returning keepAlive is what keeps the ticker
		// armed; a coalesced tick must never retire the goroutine.
		cfg.lastClockFire = jm.now()
		jm.mu.Unlock()
		return dec.keepAlive
	}
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
		cfg.tickFired = true
		cfg.lastTickOutputBytes = snap.targetOutputBytes
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
		// A periodic tick is a clock, not a condition fire.
		jm.countWatchDeliveryLocked(cfg)
	}
	emitting := len(notifications) > 0
	if emitting {
		jm.noticesInFlight++
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
	if emitting {
		jm.noteNoticeEmission(-1)
	}
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
