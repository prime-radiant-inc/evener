package agent

import (
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// delegateRunTerminal reports whether a delegate's latest run has ended with
// no run open after it: the Subagents list's terminal rule
// (projectStableActivityDelegate) and the row tally's, kept in one place so
// the row and the list cannot disagree.
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
// the session's.
func (s *Session) SubagentTally() (appwire.SubagentTally, bool) {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil {
		return appwire.SubagentTally{}, false
	}
	return s.delegateController.tally(), true
}
