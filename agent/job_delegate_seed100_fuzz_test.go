//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

// FuzzJobDelegateSeed100Edges supplies deterministic seeds for delegate
// lifecycle guard and adapter branches that are deliberately rare in normal
// operation. It uses only temporary stores and in-process sessions.
func FuzzJobDelegateSeed100Edges(f *testing.F) {
	for i := byte(0); i < 16; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		switch op % 16 {
		case 0:
			for _, v := range []any{nil, 1, "{", `{}`, `{"agent_id":"child"}`} {
				_, _ = parseSpawnedAgentID(v)
			}
			_ = isUnsupportedRuntimeMessageAlias("main")
			_ = isUnsupportedRuntimeMessageAlias(runtimeMessageAliasWatched)
		case 1:
			_ = (&Session{}).assessDelegateResumability(nil, delegateResumabilityPreflight)
			_ = validateDelegateRestoreState(&jobstore.JobRecord{Type: jobstore.JobDelegate, DelegateRestore: &jobstore.DelegateRestoreDescriptor{ChildSessionID: "child", TranscriptRef: "bad"}}, "", true)
		case 2:
			for _, v := range []any{"[]", struct{ X int }{}, make(chan int), map[string]any{}} {
				_ = delegateResultSchemaMap(v)
				_ = cloneDelegateResultSchema(v)
			}
		case 3:
			jm := newTestJM(t)
			run := jd100Run(t, jm)
			close(run.done)
			_ = waitForDelegateFinalization(context.Background(), nil, jm, run, make(chan error))
		case 4:
			jm := newTestJM(t)
			run := jd100Run(t, jm)
			finalized := make(chan error, 1)
			finalized <- nil
			_ = waitForDelegateFinalization(context.Background(), nil, jm, run, finalized)
		case 5:
			jm := newTestJM(t)
			run := jd100Run(t, jm)
			close(run.done)
			_ = waitForResumedDelegateResult(context.Background(), nil, jm, "dlg_seed", "job_old", run, make(chan error), 1)
		case 6:
			jm := newTestJM(t)
			run := jd100Run(t, jm)
			finalized := make(chan error, 1)
			finalized <- nil
			_ = waitForResumedDelegateResult(context.Background(), nil, jm, "dlg_seed", "job_old", run, finalized, 1)
		case 7:
			s := newTestSession(t)
			child := newTestSession(t)
			sub := w3dlg_attachSub(child)
			run, err := s.attachDelegateJobFromWatch(s.jobManager, child.ID(), "seed attach watch", sub, nil, nil, true)
			jd100CloseRun(t, s, run, err)
		case 8:
			s := newTestSession(t)
			child := newTestSession(t)
			sub := w3dlg_attachSub(child)
			run, err := s.attachDelegateJobWithRestore(s.jobManager, child.ID(), "seed attach restore", sub, jobstore.NewJobID(), nil, false, nil, nil)
			jd100CloseRun(t, s, run, err)
		case 9:
			s := newTestSession(t)
			_ = s.finalizeDelegateWithNotification("missing", "missing", nil, true)
		case 10:
			s := newTestSession(t)
			_, _ = s.resolveDelegateRestoreProfile(schema.SessionMeta{}, nil)
		case 11:
			s := newTestSession(t)
			_, _ = s.restoreTerminalDelegateChild(nil, "child", nil)
			_, _ = s.restoreTerminalDelegateChild(&jobstore.JobRecord{}, "child", &delegateRestorePreflight{})
		case 12:
			_ = (&Session{}).reacquireDelegateWorktreeLock("/missing", "dlg_seed")
		case 13:
			jm := newTestJM(t)
			run := jd100Run(t, jm)
			_ = delegateTerminalResult(nil, jm, run)
		case 14:
			s := newTestSession(t)
			_ = s.isolatedDelegateWorktreeReport(&jobstore.DelegateRestoreDescriptor{Isolation: "worktree", WorkingDir: t.TempDir()})
		case 15:
			jm := newTestJM(t)
			jm.mu.Lock()
			jm.closing = true
			jm.mu.Unlock()
			if !delegateFinalizeStopsRetry(jm, errors.New("seed")) {
				t.Fatal("closing manager did not stop retry")
			}
		}
	})
}

func jd100Run(t *testing.T, jm *jobManager) *runningJob {
	t.Helper()
	run := &runningJob{rec: &jobstore.JobRecord{JobID: jobstore.NewJobID(), DelegateID: "dlg_seed", Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted}, done: make(chan struct{})}
	output, err := jm.openOutput(filepath.Join(jm.dir, "jobs", run.rec.JobID+".log"), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatal(err)
	}
	run.output = output
	t.Cleanup(func() { _ = output.Close() })
	return run
}

func jd100CloseRun(t *testing.T, s *Session, run *runningJob, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if run == nil {
		t.Fatal("attach returned nil run")
	}
	s.jobManager.abandonRunningJob(run.rec.JobID)
}
