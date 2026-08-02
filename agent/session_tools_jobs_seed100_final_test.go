//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"primeradiant.com/serf/agent/internal/jobstore"
	"testing"
)

func seed100ToolsFinal(t *testing.T) {
	t.Helper()
	s := newSession(t)
	jm := s.jobManager

	origLoad := loadDelegatesForJobList
	loadDelegatesForJobList = func(*jobManager) (map[string]*jobstore.DelegateRecord, error) {
		return nil, errors.New("delegate load fault")
	}
	_, _ = jobListTool(s, map[string]any{}, 1024)
	loadDelegatesForJobList = origLoad
	t.Cleanup(func() { loadDelegatesForJobList = origLoad })

	// A live delegate child plus an injected cascade failure reaches job_stop's
	// joined subtree error without disturbing the real stop implementation.
	child := newSession(t)
	delegateJob := "job_final_delegate"
	started := frozenTestTime
	_ = jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: delegateJob, Type: jobstore.JobDelegate, OwnerSessionID: s.ID(), VisibleToSession: s.ID(), TranscriptRef: encodeRef("", child.ID()), StartedAt: &started})
	child.jobManager.setParentJobID(delegateJob)
	s.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child}
	origStop := stopDelegateSubtreeForJobStop
	stopDelegateSubtreeForJobStop = func(*Session, *Session) ([]*jobstore.JobRecord, error) { return nil, errors.New("cascade fault") }
	_, _ = jobStopTool(context.Background(), s, map[string]any{"job_id": delegateJob}, 1024)
	stopDelegateSubtreeForJobStop = origStop
	t.Cleanup(func() { stopDelegateSubtreeForJobStop = origStop })

	// A canceled wait keeps a still-running record and projects stop_pending.
	runID := "job_final_pending"
	jm.running[runID] = &runningJob{rec: &jobstore.JobRecord{JobID: runID, Type: jobstore.JobShell, Status: jobstore.StatusRunning}, done: make(chan struct{}), signal: func() {}}
	origStopLocal := stopNestedOrLocalForJobStop
	stopNestedOrLocalForJobStop = func(*Session, string) (*jobstore.JobRecord, error) {
		return &jobstore.JobRecord{JobID: runID, Type: jobstore.JobShell, Status: jobstore.StatusRunning}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = jobStopTool(ctx, s, map[string]any{"job_id": runID, "max_wait_ms": 1000}, 1024)
	stopNestedOrLocalForJobStop = origStopLocal
	t.Cleanup(func() { stopNestedOrLocalForJobStop = origStopLocal })
	delete(jm.running, runID)

}
