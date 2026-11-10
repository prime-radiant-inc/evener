package agent

import (
	"context"
	"errors"
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
	// ended is closed when the wait ends, however it ends.
	ended chan struct{}
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

// waitingForQuota reports whether a model call is waiting out a usage limit.
func (s *Session) waitingForQuota() bool {
	return s.quotaWaitEnded() != nil
}

// quotaWaitEnded is closed when the model call now waiting out a usage limit
// stops waiting; nil while none waits.
func (s *Session) quotaWaitEnded() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaWait == nil {
		return nil
	}
	return s.quotaWait.ended
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
	wait := &quotaWait{failure: quotaWaitFailure(callErr, model, resetAt), wake: make(chan struct{}, 1), ended: make(chan struct{})}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return false, nil
	}
	s.quotaWait = wait
	if s.quotaWakes != wakesBefore || s.pendingModel.model != "" {
		wait.wake <- struct{}{}
	}
	s.mu.Unlock()
	end := sync.OnceFunc(func() {
		s.mu.Lock()
		s.quotaWait = nil
		s.mu.Unlock()
		close(wait.ended)
	})
	defer end()

	s.noteParentJobActivity(jobPhaseModelRetrying)
	s.emitQuotaWait(callErr, model, resetAt.Sub(now))
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C():
	case <-wait.wake:
	}
	end()
	s.applyQueuedModelSwitches(true)
	s.injectDrainedSteering()
	return true, nil
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
// wall-budgeted rate limit, it counts no attempts against a budget.
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

// boundHungTurn cancels a turn that runs past timeout, the way a hung turn
// is cut loose, except while sess's model call waits out a usage limit: that
// turn is waiting by design and resumes by itself, so the bound starts over
// once the wait ends instead of cutting the turn loose and failing its owner.
// stop disarms it.
func boundHungTurn(sess *Session, timeout time.Duration, cancel context.CancelFunc) (stop func()) {
	stopped := make(chan struct{})
	var timer *time.Timer
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	timer = time.AfterFunc(timeout, func() {
		ended := sess.quotaWaitEnded()
		if ended == nil {
			cancel()
			return
		}
		go func() {
			select {
			case <-ended:
				mu.Lock()
				timer.Reset(timeout)
				mu.Unlock()
			case <-stopped:
			}
		}()
	})
	return sync.OnceFunc(func() {
		close(stopped)
		mu.Lock()
		timer.Stop()
		mu.Unlock()
	})
}
