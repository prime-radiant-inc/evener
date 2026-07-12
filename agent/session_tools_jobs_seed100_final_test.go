//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
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

	seed100WaitSelectArms(t, jm)

	origRead := readJobOutputForScan
	readJobOutputForScan = func(_ *jobManager, _ string, bytes int) (string, int64, bool, error) {
		return strings.Repeat("x", bytes), int64(bytes + 1), false, nil
	}
	_, _, _ = readJobOutputFrom(jm, "growing", 0, 1)
	readJobOutputForScan = origRead
	t.Cleanup(func() { readJobOutputForScan = origRead })
}

func seed100WaitSelectArms(t *testing.T, jm *jobManager) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	jm.clock = clk

	closed := make(chan struct{})
	close(closed)
	doneOutput, err := jm.openOutput(filepath.Join(jm.dir, "jobs", "done-final.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	jm.running["done"] = &runningJob{rec: &jobstore.JobRecord{JobID: "done"}, done: closed, output: doneOutput}
	_ = waitForJobDone(context.Background(), jm, "done", time.Second)
	waitForJobDoneOrOutput(context.Background(), jm, "done", time.Second)
	waitForJobGrepMatch(context.Background(), jm, "done", regexp.MustCompile("never"), time.Second)
	delete(jm.running, "done")

	timerOutput, err := jm.openOutput(filepath.Join(jm.dir, "jobs", "wait-timer.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	jm.running["wait-timer"] = &runningJob{rec: &jobstore.JobRecord{JobID: "wait-timer"}, done: make(chan struct{}), output: timerOutput}
	waited := make(chan struct{})
	go func() { _ = waitForJobDone(context.Background(), jm, "wait-timer", time.Second); close(waited) }()
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	<-waited
	delete(jm.running, "wait-timer")

	for _, grep := range []bool{false, true} {
		id := "timer"
		if grep {
			id = "grep-timer"
		}
		output, err := jm.openOutput(filepath.Join(jm.dir, "jobs", id+".log"), 64)
		if err != nil {
			t.Fatal(err)
		}
		jm.running[id] = &runningJob{rec: &jobstore.JobRecord{JobID: id}, done: make(chan struct{}), output: output}
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			if grep {
				waitForJobGrepMatch(context.Background(), jm, id, regexp.MustCompile("never"), time.Second)
			} else {
				waitForJobDoneOrOutput(context.Background(), jm, id, time.Second)
			}
		}()
		clk.BlockUntil(2)
		clk.Advance(time.Second)
		<-finished
		delete(jm.running, id)
	}
}
