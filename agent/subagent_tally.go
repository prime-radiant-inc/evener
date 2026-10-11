package agent

import (
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// delegateRunTerminal reports whether a delegate's latest run has ended with
// no run open after it. It is the one statement of that rule: every reader
// that needs the answer calls this instead of restating the check, so they
// cannot disagree.
func delegateRunTerminal(outcome *delegatestore.Outcome, currentRunOpen bool) bool {
	return outcome != nil && !currentRunOpen
}

// tally counts every delegate the controller has folded, at every depth and
// over the tree's whole history, by how its latest run stands (S3).
func (c *delegateTreeController) tally() appwire.SubagentTally {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tally appwire.SubagentTally
	for _, aggregate := range c.durable {
		switch {
		case !delegateRunTerminal(aggregate.LatestOutcome, aggregate.CurrentRunOpen):
			tally.Running++
		case activityDelegateOutcome(string(aggregate.LatestOutcome.Status)) == "failure":
			tally.Failed++
		default:
			tally.Done++
		}
	}
	return tally
}

// SubagentTally is this session's whole delegate tree counted by state, for its
// thread list row (S3). Only the tree's root owns the controller: a child
// session shares its root's, and it and a session with no delegate tree report
// false. It takes the controller's lock, as DetailedStatus does, and never
// the session's. subagentTallyForTest, when set, answers instead - see
// SubagentTallyForTest.
func (s *Session) SubagentTally() (appwire.SubagentTally, bool) {
	if s == nil {
		return appwire.SubagentTally{}, false
	}
	if s.subagentTallyForTest != nil {
		return *s.subagentTallyForTest, true
	}
	if !s.ownsDelegateController || s.delegateController == nil {
		return appwire.SubagentTally{}, false
	}
	return s.delegateController.tally(), true
}

// SubagentTallyForTest makes SubagentTally report one completed delegate,
// exposing enough of the unexported delegate controller for the daemon's
// server-side tests (which live outside package agent) to give a session a
// tally distinct from a fresh session's empty one - for example to prove
// which of two sessions' tally seams a caller is actually reading, which a
// real delegate tree would take a whole scripted run to arrange.
func (s *Session) SubagentTallyForTest() {
	tally := appwire.SubagentTally{Done: 1}
	s.subagentTallyForTest = &tally
}
