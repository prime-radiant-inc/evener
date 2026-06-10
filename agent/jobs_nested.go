package agent

import (
	"errors"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func (s *Session) ownerJobManagerFor(jobID string) (*jobManager, *jobstore.JobRecord) {
	if s == nil || s.jobManager == nil || s.jobManager.store == nil {
		return nil, nil
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return nil, nil
	}
	rec := recs[jobID]
	if rec == nil || rec.ParentJobID == "" || rec.OwnerSessionID == "" || rec.OwnerSessionID == s.id {
		return nil, rec
	}
	if s.subagents == nil {
		return nil, rec
	}
	sub := s.subagents.get(rec.OwnerSessionID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		return nil, rec
	}
	sub.mu.Lock()
	closed := sub.closed
	sub.mu.Unlock()
	if closed {
		return nil, rec
	}
	owner := sub.sess.jobManager
	if owner.store == nil {
		return nil, rec
	}
	if _, err := owner.store.Load(); errors.Is(err, jobstore.ErrStoreClosed) {
		return nil, rec
	}
	return owner, rec
}

func (s *Session) nestedOrLocalJobManager(jobID string) (*jobManager, *jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, nil, err
	}
	owner, forwarded := s.ownerJobManagerFor(jobID)
	if owner == nil {
		if forwarded != nil {
			return local, forwarded, nil
		}
		rec, err := findJobRecord(local, jobID)
		return local, rec, err
	}
	rec, err := findJobRecord(owner, jobID)
	if err != nil {
		return nil, nil, err
	}
	return owner, rec, nil
}

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
