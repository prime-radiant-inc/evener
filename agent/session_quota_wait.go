package agent

import (
	"context"
	"errors"
	"time"

	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// quotaWait is a session waiting out its provider's usage limit. After a
// quota failure no request can succeed until the allowance returns, so the
// autonomous wakes that would start turns against it — job completions,
// delegate reports, watch frames, goal continuations — are refused at the
// entry gate and keep their work pending. The wait ends on its own at the
// provider's reset time, on a model switch, or when a turn succeeds (a user
// message still tries the provider, since the user may know the quota came
// back). Ending it by the clock or a switch asks for the wakes it refused.
// A delegate's own run waits too (holdDelegateRunForQuota): its generation
// stays open, and resumes when the wait ends.
//
// Only a quota failure that names its reset time begins a wait. One that
// does not keeps the permanent-failure pause the root attention rail already
// takes (finishRootDelegateAttentionTurn), with no timer invented for it.
//
// Only a long-lived session waits. A one-shot run (TurnEndsProcess: `evener
// run` and every delegate under it) has no human to switch models and would
// hold its process until the reset, so its turn fails fast as any provider
// failure does.
type quotaWait struct {
	// resetAt is when the provider said the allowance returns.
	resetAt time.Time
	// timer ends the wait at resetAt.
	timer clock.Timer
	// err is the quota failure that began the wait, which a job-tree drain
	// returns instead of running turns that would meet it again.
	err error
	// ended is closed when the wait ends, however it ends.
	ended chan struct{}
	// failure is the recorded turn failure that began the wait. The session
	// rests on it for as long as it waits: a notification appended to the
	// transcript meanwhile is held work, not a turn that ended the failure.
	failure schema.Turn
}

// beginQuotaWait starts waiting out a quota failure that names its reset
// time, or restarts the wait with the latest one. It runs after the failed
// turn recorded its failure. A reset time already past is no reason to wait:
// the provider says the allowance is back, and a one-shot run never waits.
func (s *Session) beginQuotaWait(err error) {
	resetAt, ok := llm.UsageLimitResetAt(err)
	if !ok || llm.Kind(err) != llm.KindQuotaExceeded || s.cfg.TurnEndsProcess {
		return
	}
	now := s.sclock().Now()
	if !resetAt.After(now) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closingOrClosedLocked() {
		return
	}
	failure, _ := historyTurnFailure(s.history)
	s.stopQuotaWaitLocked()
	wait := &quotaWait{resetAt: resetAt, err: err, failure: failure, ended: make(chan struct{})}
	wait.timer = s.sclock().AfterFunc(resetAt.Sub(now), func() { s.quotaResetReached(wait) })
	s.quotaWait = wait
	s.lastQuotaWait = wait
}

// quotaResetReached ends the wait its timer belongs to, unless something
// already ended or replaced it.
func (s *Session) quotaResetReached(wait *quotaWait) {
	s.mu.Lock()
	current := s.quotaWait == wait && !s.closingOrClosedLocked()
	if current {
		s.stopQuotaWaitLocked()
	}
	s.mu.Unlock()
	if current {
		s.wakeAfterQuotaWait()
	}
}

// endQuotaWaitForModelSwitch ends a wait because the session now runs on
// another model, which the spent allowance may not limit, and asks for the
// wakes the wait refused.
func (s *Session) endQuotaWaitForModelSwitch() {
	if s.endQuotaWait() {
		s.wakeAfterQuotaWait()
	}
}

// endQuotaWait ends any wait without asking for a wake, reporting whether
// one was in progress. A successful model call ends it this way: the turn
// that made the call delivers what was held through its own drain.
func (s *Session) endQuotaWait() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaWait == nil {
		return false
	}
	s.stopQuotaWaitLocked()
	return true
}

// stopQuotaWaitLocked ends any wait: its timer no longer holds the session,
// and whatever waits on it is released. Closing the session stops it too.
func (s *Session) stopQuotaWaitLocked() {
	if s.quotaWait != nil {
		s.quotaWait.timer.Stop()
		close(s.quotaWait.ended)
	}
	s.quotaWait = nil
}

// quotaWaitEndedFor returns a channel closed when the quota wait that err
// began ends, or false when err began none: it is not a quota failure, or
// its reset time had already passed.
func (s *Session) quotaWaitEndedFor(err error) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := s.lastQuotaWait
	if err == nil || wait == nil || !errors.Is(err, wait.err) {
		return nil, false
	}
	return wait.ended, true
}

// waitingForQuota reports whether a quota wait holds autonomous turns.
func (s *Session) waitingForQuota() bool {
	return s.quotaWaitError() != nil
}

// quotaWaitError is the quota failure the session is waiting out, nil when
// it is not waiting.
func (s *Session) quotaWaitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaWait == nil {
		return nil
	}
	return s.quotaWait.err
}

// quotaWaitKeepsGoal reports whether err is a quota failure the session is
// now waiting out. An active goal stays active through the wait and
// continues when it ends, rather than blocking on a failure that clears by
// itself.
func (s *Session) quotaWaitKeepsGoal(err error) bool {
	return llm.Kind(err) == llm.KindQuotaExceeded && s.waitingForQuota()
}

// wakeAfterQuotaWait asks for the turns the wait held back. Held
// notifications get one notification turn, whose idle settle then continues
// an active goal; with no notification held, the active goal is kicked
// directly. Root attention a quota failure paused is re-armed first, so the
// notification turn carries it.
func (s *Session) wakeAfterQuotaWait() {
	s.resumePausedRootAttention()
	if s.peekNotifications() > 0 || s.pendingRootDelegateAttention() {
		s.notify()
		return
	}
	s.mu.Lock()
	var kick func(string)
	var prompt string
	if !s.goalInTurn {
		kick, prompt = s.idleGoalKickLocked()
	}
	s.mu.Unlock()
	if prompt != "" {
		kick(prompt)
	}
}

// holdDelegateRunForQuota holds a delegate's run whose turn met a usage limit
// with a known reset, instead of letting the run finish as failed: no
// missing-report reminder, stop-hook or settlement turn runs against the
// limit, and the parent keeps the delegate as running rather than failed. It
// reports true when the wait ended (at the reset, or on a model switch) and
// the run should resume, and false when err began no wait or the run was
// stopped or cancelled meanwhile.
func (a *subagent) holdDelegateRunForQuota(ctx context.Context, err error) bool {
	ended, waiting := a.sess.quotaWaitEndedFor(err)
	if !waiting {
		return false
	}
	select {
	case <-ended:
	case <-ctx.Done():
		return false
	}
	a.mu.Lock()
	cancelRequested := a.cancelRequested
	a.mu.Unlock()
	return !cancelRequested && ctx.Err() == nil
}
