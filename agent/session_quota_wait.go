package agent

import (
	"context"
	"errors"
	"primeradiant.com/evener/agent/internal/clock"
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// quotaWait is a model call waiting out its provider's usage limit. When a
// call meets a usage limit that names its reset time, every request before
// the reset would fail the same way, so the call waits inside itself instead
// of failing the turn (waitOutUsageLimit). Every kind of turn — a user's, a
// notification's, a goal continuation, a delegate's generation, nudge,
// stop-hook, finalization or notification-drive turn — makes its model calls
// through the same round loop, so every one of them waits the same way and
// none of them sees the error. The session stays busy in its turn while it
// waits: it does not retire, a drain running the turn does not end, and
// notifications queue behind the turn as they queue behind any running turn.
//
// The wait ends at the reset, when a model switch is picked (the call
// retries on the new model at once), when the user steers or queues a
// message (the call tries the provider once more, carrying the steer: the
// user may know the quota came back), or when the turn's context ends (a
// Stop or Close, which ends the turn as an ordinary cancellation).
type quotaWait struct {
	// failure is what the session's row says while the call waits: the
	// usage limit, and when the call resumes by itself.
	failure appwire.ThreadFailure
	// wake asks the wait to retry now.
	wake chan struct{}
}

// quotaWakeCount is how many times something has asked a waiting call to
// retry now. A model call reads it before it starts; a wake asked for while
// the request was in flight then reaches the wait the request's failure
// begins.
func (s *Session) quotaWakeCount() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaWakes
}

// wakeQuotaWaitLocked asks a waiting model call to retry now: the user sent
// a message, or picked another model. The caller holds s.mu.
func (s *Session) wakeQuotaWaitLocked() {
	s.quotaWakes++
	if wait := s.quotaWait; wait != nil {
		select {
		case wait.wake <- struct{}{}:
		default:
		}
	}
}

// wakeQuotaWait is wakeQuotaWaitLocked for a caller that does not hold s.mu.
func (s *Session) wakeQuotaWait() {
	s.mu.Lock()
	s.wakeQuotaWaitLocked()
	s.mu.Unlock()
}

// wakeQuotaWaitForSteer is wakeQuotaWait for user steering just accepted:
// it also counts the steer, so a wait that begins before the steer reaches
// a request still retries for it.
func (s *Session) wakeQuotaWaitForSteer() {
	s.mu.Lock()
	s.steerArrivals++
	s.wakeQuotaWaitLocked()
	s.mu.Unlock()
}

// noteSteeringDrainLocked records that a steering drain begins: user
// steering accepted before now is in the drain or already delivered. The
// caller holds s.mu.
func (s *Session) noteSteeringDrainLocked() {
	s.steerArrivalsDrained = s.steerArrivals
}

// waitingForQuota reports whether a model call is waiting out a usage limit.
func (s *Session) waitingForQuota() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaWait != nil
}

// waitOutUsageLimit waits out callErr when it is a usage limit with a future
// reset time and the session is long-lived, and reports whether the call
// should be made again. wakesBefore is quotaWakeCount as the call began. A
// wait that ends because ctx ended returns ctx's error, which the round
// handles as it handles a cancellation during any model call.
//
// A one-shot run (TurnEndsProcess: `evener run` and every delegate under it)
// never waits: it has no human to switch models and would hold its process
// until the reset, so its turn fails fast as on any provider failure. Nor
// does a usage limit that names no reset time, or one already past: the
// turn fails as before, and the permanent-failure handling stands.
//
// Before the retry it applies a model switch picked meanwhile and delivers
// steering that arrived, as a round boundary does, so the retry runs on the
// model the user picked and carries what they said.
func (s *Session) waitOutUsageLimit(ctx context.Context, callErr error, model string, wakesBefore uint64) (retry bool, err error) {
	resetAt, ok := llm.UsageLimitResetAt(callErr)
	if !ok || llm.Kind(callErr) != llm.KindQuotaExceeded || s.cfg.TurnEndsProcess {
		return false, nil
	}
	now := s.sclock().Now()
	if !resetAt.After(now) {
		return false, nil
	}
	// The timer is armed before the wait is published, so anything that sees
	// the session waiting sees a wait whose reset will end it.
	timer := s.sclock().NewTimer(resetAt.Sub(now))
	defer timer.Stop()
	wait := &quotaWait{failure: quotaWaitFailure(callErr, model, resetAt), wake: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return false, nil
	}
	s.quotaWait = wait
	// A wake that came while the request was in flight, a switch already
	// picked, or a steer that arrived after the last steering drain began
	// (and so may not be in this request) retries at once.
	if s.quotaWakes != wakesBefore || s.pendingModel.model != "" || s.steerArrivals != s.steerArrivalsDrained {
		wait.wake <- struct{}{}
	}
	s.mu.Unlock()
	end := sync.OnceFunc(func() {
		s.mu.Lock()
		if s.quotaWait == wait {
			s.quotaWait = nil
		}
		s.mu.Unlock()
	})
	defer end()

	s.noteParentJobActivity(jobPhaseModelRetrying)
	s.emitQuotaWait(callErr, model, resetAt.Sub(now))
	drive := driveTurnFor(ctx, s)
	drive.waitBegins()
	defer drive.waitEnded()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C():
	case <-wait.wake:
	}
	// Still inside the wait, so a drive's hung-turn bound does not count the
	// time the turn queues for a slot behind other drives.
	if err := drive.retakeSlot(ctx); err != nil {
		return false, err
	}
	end()
	s.applyModelSwitchPickedDuringWait()
	s.injectDrainedSteering()
	return true, nil
}

// applyModelSwitchPickedDuringWait applies a switch picked while the call
// waited, so the retry runs on the model the user chose. The rounds this turn
// already made on the old model then become prior history: the turn's
// in-flight boundary moves to the present, so the new target's replay
// provenance rules filter them (no signed thinking or encrypted reasoning
// another model produced is sent to it), as they would at a turn boundary.
func (s *Session) applyModelSwitchPickedDuringWait() {
	s.mu.Lock()
	before := s.profile
	s.mu.Unlock()
	s.applyQueuedModelSwitches(true)
	s.mu.Lock()
	if s.profile != before {
		s.turnHistoryBaseline = len(s.history)
	}
	s.mu.Unlock()
}

// quotaWaitFailure is the row summary of a call waiting out callErr: the
// failure's headline and cause, as a failed turn's summary carries them, and
// when the call resumes by itself.
func quotaWaitFailure(callErr error, model string, resetAt time.Time) appwire.ThreadFailure {
	failure := appwire.ThreadFailure{
		Title:     appwire.Excerpt(errorDataFromError(callErr).Title, appwire.MaxFailureTitleRunes),
		ResumesAt: resetAt.UnixMilli(),
	}
	if cause := providerCauseFromError(callErr, model); cause != nil {
		failure.Cause = &appwire.DiagnosticCause{Kind: cause.Kind, Provider: cause.Provider, Model: cause.Model, Status: cause.Status}
	}
	return failure
}

// emitQuotaWait announces the wait the way the retry loop announces each of
// its backoffs: a model retry whose delay runs to the reset. Like a
// wall-budgeted rate limit, it counts no attempts against a budget, so every
// wait says attempt 1 and clients read only its delay.
func (s *Session) emitQuotaWait(callErr error, model string, delay time.Duration) {
	data := events.ModelRetryData{
		Attempt:    1,
		DelayMS:    delay.Milliseconds(),
		ErrorClass: llm.Kind(callErr).String(),
		Message:    callErr.Error(),
		Model:      model,
	}
	if le, ok := errors.AsType[llm.Error](callErr); ok {
		data.StatusCode = le.StatusCode()
	}
	s.emit(data)
}

// driveTurn is a parent's notification drive of one child's turn
// (driveSubagentNotificationTurn): the drive-budget slot it holds and the
// bound that cuts a hung turn loose. A model call of that turn waiting out a
// usage limit gives the slot back for the wait, so children on healthy
// models are not queued behind waits that can last days, and pauses the
// bound, since waiting is not hanging. Before it retries it takes a slot
// again, and when the wait ends, however it ends, the bound starts over with
// its whole timeout.
type driveTurn struct {
	// owner is the session whose drive budget the slot belongs to.
	owner *Session
	// turn is the session whose turn the drive runs: only that session's
	// model calls act on the drive, never a descendant's that inherits the
	// turn's context.
	turn *Session

	mu      sync.Mutex
	slot    *treeReservation
	timer   clock.Timer
	timeout time.Duration
	ended   bool
}

type driveTurnKey struct{}

// startDriveTurn takes over slot for turn's drive and arms its hung-turn
// bound, which calls cancel once the turn has worked past timeout.
func startDriveTurn(owner, turn *Session, slot *treeReservation, timeout time.Duration, cancel context.CancelFunc) *driveTurn {
	d := &driveTurn{owner: owner, turn: turn, slot: slot, timeout: timeout}
	d.timer = turn.sclock().AfterFunc(timeout, cancel)
	return d
}

func withDriveTurn(ctx context.Context, d *driveTurn) context.Context {
	return context.WithValue(ctx, driveTurnKey{}, d)
}

// driveTurnFor is the drive s's current turn runs under, nil when the turn
// is not a drive's.
func driveTurnFor(ctx context.Context, s *Session) *driveTurn {
	d, _ := ctx.Value(driveTurnKey{}).(*driveTurn)
	if d == nil || d.turn != s {
		return nil
	}
	return d
}

// waitBegins gives the slot back and pauses the bound.
func (d *driveTurn) waitBegins() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.slot.release()
	d.slot = nil
	d.timer.Stop()
}

// waitEnded starts the bound over, unless the drive has ended.
func (d *driveTurn) waitEnded() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.ended {
		d.timer.Reset(d.timeout)
	}
}

// retakeSlot takes a drive slot again for a waiting drive, waiting for a
// release while the budget is full. It fails only when ctx ends.
func (d *driveTurn) retakeSlot(ctx context.Context) error {
	if d == nil {
		return nil
	}
	for {
		var released <-chan struct{}
		if d.owner.driveCounter != nil {
			released = d.owner.driveCounter.nextRelease()
		}
		if slot, ok := d.owner.reserveDriveSlot(); ok {
			d.mu.Lock()
			d.slot = slot
			d.mu.Unlock()
			return nil
		}
		select {
		case <-released:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// end releases the slot and disarms the bound once the drive is over.
func (d *driveTurn) end() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ended = true
	d.slot.release()
	d.slot = nil
	d.timer.Stop()
}
