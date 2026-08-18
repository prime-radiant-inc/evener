//go:build serffuzz

package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func seed100ToolsFinal(t *testing.T) {
	t.Helper()
	s := newSession(t)
	jm := s.jobManager

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
