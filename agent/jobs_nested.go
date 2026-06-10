package agent

import (
	"errors"
	"fmt"

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
		if errors.Is(err, jobstore.ErrStoreClosed) && forwarded != nil {
			return local, forwarded, nil
		}
		return nil, nil, err
	}
	return owner, rec, nil
}

func (s *Session) stopNestedOrLocal(jobID string) (*jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, err
	}
	owner, forwarded := s.ownerJobManagerFor(jobID)
	if owner != nil {
		// Spec 5.8's not_controllable path is reserved for future cross-process
		// owners; a live in-process owner routes directly to its job manager.
		return owner.stop(jobID)
	}
	if forwarded != nil && forwarded.ParentJobID != "" && forwarded.OwnerSessionID != s.id {
		return nil, fmt.Errorf("job %q is not controllable: nested job owner runtime is not live", jobID)
	}
	return local.stop(jobID)
}

func (s *Session) stopChildren(delegateJobID string) ([]*jobstore.JobRecord, error) {
	local, err := sessionJobManager(s)
	if err != nil {
		return nil, err
	}
	recs, err := local.store.Load()
	if err != nil {
		return nil, err
	}

	var stopped []*jobstore.JobRecord
	var stopErr error
	for jobID, rec := range recs {
		if rec.ParentJobID != delegateJobID || rec.Status.IsTerminal() {
			continue
		}
		stoppedRec, err := s.stopNestedOrLocal(jobID)
		if err != nil {
			stopErr = errors.Join(stopErr, err)
			continue
		}
		if stoppedRec != nil {
			stopped = append(stopped, stoppedRec)
		}
	}
	return stopped, stopErr
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
