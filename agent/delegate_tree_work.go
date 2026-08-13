package agent

import (
	"context"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

type delegateWorkToken struct{ processID uint64 }

type delegateShellWork struct {
	token     delegateWorkToken
	owner     delegateLease
	jobID     string
	cancel    context.CancelFunc
	committed bool
}

func (c *delegateTreeController) BeginShellWork(owner delegateLease) (delegateWorkToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return delegateWorkToken{}, errDelegateTargetBusy
	}
	if _, _, err := c.admitLeaseLocked(owner, delegatestore.PhaseRunning); err != nil {
		return delegateWorkToken{}, err
	}
	c.nextToken++
	token := delegateWorkToken{processID: c.nextToken}
	c.work[token.processID] = &delegateShellWork{token: token, owner: owner}
	c.evidenceVersion++
	return token, nil
}

func (c *delegateTreeController) CommitShellWork(token delegateWorkToken, shellJobID string, cancel context.CancelFunc) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	work := c.work[token.processID]
	if work == nil || work.token != token || work.committed || shellJobID == "" || cancel == nil {
		return false, errDelegateStaleLease
	}
	if c.closing || c.stopCoversLocked(work.owner.delegateID) {
		work.jobID = shellJobID
		work.cancel = cancel
		work.committed = true
		if c.stop != nil {
			c.stop.work[token] = shellJobID
		}
		c.evidenceVersion++
		return true, nil
	}
	work.jobID = shellJobID
	work.cancel = cancel
	work.committed = true
	if c.stopCoversLocked(work.owner.delegateID) {
		c.stop.work[token] = shellJobID
	}
	c.evidenceVersion++
	return false, nil
}

func (c *delegateTreeController) AbortShellWork(token delegateWorkToken) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	work := c.work[token.processID]
	if work == nil || work.token != token || work.committed {
		return errDelegateStaleLease
	}
	delete(c.work, token.processID)
	if c.stop != nil {
		if _, tracked := c.stop.work[token]; tracked {
			delete(c.stop.work, token)
			c.signalStopProgressLocked()
		}
	}
	c.evidenceVersion++
	return nil
}

func (c *delegateTreeController) ReportShellFinished(token delegateWorkToken, shellJobID string) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	work := c.work[token.processID]
	if work == nil || work.token != token || !work.committed || work.jobID != shellJobID {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	delete(c.work, token.processID)
	if c.stop != nil {
		if _, tracked := c.stop.work[token]; tracked {
			delete(c.stop.work, token)
			c.signalStopProgressLocked()
		}
	}
	c.evidenceVersion++
	return delegateMutationPlans{}, nil
}
