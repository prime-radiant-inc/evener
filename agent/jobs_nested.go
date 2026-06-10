package agent

import "primeradiant.com/serf/agent/internal/jobstore"

func (jm *jobManager) forwardLocked(e jobstore.Event) {
	if jm.forward == nil || jm.parentJobID == "" {
		return
	}
	jm.forward(e)
}

func (jm *jobManager) forwardEvent(e jobstore.Event) {
	e.VisibleToSession = jm.sessionID
	_ = jm.store.Append(e)
}
