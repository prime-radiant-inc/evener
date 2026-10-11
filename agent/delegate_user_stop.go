package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
)

// ErrUnknownDelegate is StopDelegateRun's answer for an id this session's
// delegate tree does not hold.
var ErrUnknownDelegate = errors.New("unknown delegate")

// delegateUserStopMessage is what a coordinator reads in place of a stopped
// run's report, so it knows the user ended the run rather than the run
// failing.
const delegateUserStopMessage = "Stopped by the user."

var (
	errSubagentNotRunning = errors.New("not running")
	errSubagentSettling   = errors.New("completing its current run")
)

// StopDelegateRun ends delegateID's current run at the user's request (S6).
// Only that delegate's run ends: subagents it started keep running and report
// to it, and it takes their results the next time it runs. Nothing durable is
// written here; the run's own settlement records it as cancelled, and its
// coordinator reads delegateUserStopMessage. It is safe to repeat: a second
// call finds nothing running and answers DelegateStopNotRunning.
func (s *Session) StopDelegateRun(delegateID string) (appwire.DelegateStopOutcome, error) {
	controller := s.delegateController
	if controller == nil || s.isSubagentSession() {
		return "", errors.New("this session has no delegate tree to stop")
	}
	childSessionID, ok := controller.childSessionIDFor(delegateID)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownDelegate, delegateID)
	}
	sub := s.subagentForChild(childSessionID)
	if sub == nil {
		return appwire.DelegateStopNotRunning, nil
	}
	switch _, err := sub.requestRunCancel(true); {
	case errors.Is(err, errSubagentNotRunning), errors.Is(err, errSubagentSettling):
		return appwire.DelegateStopNotRunning, nil
	case err != nil:
		return "", err
	}
	return appwire.DelegateStopStopping, nil
}

// childSessionIDFor names the session delegateID runs as.
func (c *delegateTreeController) childSessionIDFor(delegateID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return "", false
	}
	return aggregate.Descriptor.ChildSessionID, true
}

// subagentForChild finds the subagent whose session is childSessionID
// anywhere below s. Each session tracks only the subagents it started, so a
// nested delegate is found through its parent's session.
func (s *Session) subagentForChild(childSessionID string) *subagent {
	if sub := s.subagents.get(childSessionID); sub != nil {
		return sub
	}
	for _, child := range s.subagents.sessions() {
		if sub := child.subagentForChild(childSessionID); sub != nil {
			return sub
		}
	}
	return nil
}

// requestRunCancel cancels the subagent's current run and marks the
// cancellation as requested, so settlement maps it to a cancelled outcome.
// asUserStop marks it as S6's direct user stop (StopDelegateRun), so a
// cancelled run with nothing to report tells its coordinator "Stopped by the
// user."; cancelAgent's own cancel (unwired in production today) passes
// false, so it is never misattributed as a direct user stop.
// It refuses a subagent that is not running, one whose run has passed its
// last pre-settlement check, and one already stopping, so a repeated stop
// answers notRunning. On success it returns the admitted run's done channel,
// captured under the same lock as the check: a caller must wait on this
// channel rather than reading sub.done separately, which could hand back a
// different run's channel if the run settles and resumes in between.
func (a *subagent) requestRunCancel(asUserStop bool) (chan struct{}, error) {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil, errSubagentNotRunning
	}
	if a.settlementClaimed || a.cancelRequested {
		a.mu.Unlock()
		return nil, errSubagentSettling
	}
	a.cancelRequested = true
	a.userStopRequested = asUserStop
	cancel := a.cancel
	done := a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return done, nil
}
