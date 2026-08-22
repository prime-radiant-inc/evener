package agent

import (
	"context"
	"testing"
	"time"
)

// liveShellBackground reads Background off the LIVE running-map record. The
// field is json:"-", so a record folded from the store always reads false;
// only the running map can answer what mode the job is executing in.
func liveShellBackground(t *testing.T, jm *jobManager, jobID string) bool {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.rec == nil {
		t.Fatalf("job %s is not in the running map", jobID)
	}
	return run.rec.Background
}

// TestRunShellPromotionMarksRecordBackground pins that a foreground command
// promoted at the block timeout is recorded as running in the background. The
// promotion returns RunningInBackground:true to the model; a record that still
// says foreground lies about execution mode to everything that reads it.
func TestRunShellPromotionMarksRecordBackground(t *testing.T) {
	t.Parallel()
	jm, se, clk := newFakeClockShellTestRig(t)
	resCh := make(chan shellResult, 1)
	go func() {
		resCh <- runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", BlockTimeoutMS: 100})
	}()
	clk.BlockUntil(1)
	clk.Advance(time.Second)

	var res shellResult
	select {
	case res = <-resCh:
	// TRIPWIRE: the fake clock has already advanced past the timeout, so
	// runShell returns promptly; this only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("runShell did not return after foreground timeout")
	}
	if res.Reason != "foreground_timeout" {
		t.Fatalf("res = %+v, want a promoted job", res)
	}
	if !liveShellBackground(t, jm, res.JobID) {
		t.Fatal("promoted job's live record has Background=false; the record contradicts RunningInBackground:true")
	}
	_, _ = jm.stop(res.JobID)
	waitForShellDone(t, jm, res.JobID)
}

// TestRunShellBackgroundModeStillMarksRecord pins the pre-existing half: an
// explicit mode:"background" launch stamps the record at creation.
func TestRunShellBackgroundModeStillMarksRecord(t *testing.T) {
	t.Parallel()
	jm, se, _ := newFakeClockShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", Background: true})
	if res.JobID == "" {
		t.Fatalf("res = %+v, want a running background job", res)
	}
	if !liveShellBackground(t, jm, res.JobID) {
		t.Fatal("explicit background job's live record has Background=false")
	}
	_, _ = jm.stop(res.JobID)
	waitForShellDone(t, jm, res.JobID)
}
