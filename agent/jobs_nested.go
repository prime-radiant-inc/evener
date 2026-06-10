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
	if err := jm.store.Append(e); err != nil {
		return
	}
	if e.Kind != jobstore.EventJobNotificationPending || jm.enqueue == nil {
		return
	}
	recs, err := jm.store.Load()
	if err != nil {
		return
	}
	rec := recs[e.JobID]
	if rec == nil || !jobstore.ShouldDeliver(rec) {
		return
	}
	jm.enqueue(jobNotificationFromRecord(rec))
}
