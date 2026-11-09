package agent

import (
	"time"

	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/goal"
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
//
// Only a quota failure that names its reset time begins a wait. One that
// does not keeps the permanent-failure pause the root attention rail already
// takes (finishRootDelegateAttentionTurn), with no timer invented for it.
type quotaWait struct {
	// resetAt is when the provider said the allowance returns.
	resetAt time.Time
	// timer ends the wait at resetAt.
	timer clock.Timer
	// err is the quota failure that began the wait, which a job-tree drain
	// returns instead of running turns that would meet it again.
	err error
	// failure is the recorded turn failure that began the wait. The session
	// rests on it for as long as it waits: a notification appended to the
	// transcript meanwhile is held work, not a turn that ended the failure.
	failure schema.Turn
}

// beginQuotaWait starts waiting out a quota failure that names its reset
// time, or restarts the wait with the latest one. It runs after the failed
// turn recorded its failure. A reset time already past is no reason to wait:
// the provider says the allowance is back.
func (s *Session) beginQuotaWait(err error) {
	resetAt, ok := llm.UsageLimitResetAt(err)
	if !ok || llm.Kind(err) != llm.KindQuotaExceeded {
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
	wait := &quotaWait{resetAt: resetAt, err: err, failure: failure}
	wait.timer = s.sclock().AfterFunc(resetAt.Sub(now), func() { s.quotaResetReached(wait) })
	s.quotaWait = wait
}

// quotaResetReached ends the wait its timer belongs to, unless something
// already ended or replaced it.
func (s *Session) quotaResetReached(wait *quotaWait) {
	s.mu.Lock()
	current := s.quotaWait == wait && !s.closingOrClosedLocked()
	if current {
		s.quotaWait = nil
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

func (s *Session) stopQuotaWaitLocked() {
	if s.quotaWait != nil {
		s.quotaWait.timer.Stop()
	}
	s.quotaWait = nil
}

// waitingForQuota reports whether a quota wait holds autonomous turns.
func (s *Session) waitingForQuota() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaWait != nil
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
	kick := s.kickFunc
	idle := !s.goalInTurn && len(s.askPending) == 0
	var prompt string
	if kick != nil && idle {
		if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Status == goal.StatusActive {
			prompt = goal.Render(snap.Objective)
		}
	}
	s.mu.Unlock()
	if prompt != "" {
		kick(prompt)
	}
}
