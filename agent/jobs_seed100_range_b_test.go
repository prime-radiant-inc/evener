//go:build serffuzz

package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func seed100JobsRangeB(t *testing.T) {
	t.Helper()

	// Exercise the live head reader after the output store has closed.
	headJM := newTestJM(t)
	out, err := headJM.openOutput(filepath.Join(headJM.dir, "jobs", "closed-head.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	headJM.running["closed-head"] = &runningJob{
		rec:    &jobstore.JobRecord{JobID: "closed-head", Type: jobstore.JobShell},
		output: out,
	}
	if _, _, _, err := headJM.readOutputHead("closed-head", 1); err == nil {
		t.Fatal("readOutputHead on closed live output succeeded")
	}

	if got, want := headJM.outputPathForJob(nil, "fallback"), filepath.Join(headJM.dir, "jobs", "fallback.log"); got != want {
		t.Fatalf("fallback output path = %q, want %q", got, want)
	}

	// Kept-sync finalization must return a durable finish append failure.
	finishJM := newTestJM(t)
	finishOut, err := finishJM.openOutput(filepath.Join(finishJM.dir, "jobs", "finish-fault.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	finishRun := &runningJob{
		rec:    &jobstore.JobRecord{JobID: "finish-fault", Type: jobstore.JobShell, Status: jobstore.StatusRunning},
		output: finishOut,
		done:   make(chan struct{}),
	}
	finishJM.running[finishRun.rec.JobID] = finishRun
	wantErr := errors.New("finish append fault")
	finishJM.appendEvent = func(jobstore.Event) error { return wantErr }
	if err := finishJM.finalizeKeptSync(finishRun, jobstore.StatusCompleted, "exit_zero", nil); !errors.Is(err, wantErr) {
		t.Fatalf("finalizeKeptSync error = %v, want %v", err, wantErr)
	}
	_ = finishOut.Close()
}
