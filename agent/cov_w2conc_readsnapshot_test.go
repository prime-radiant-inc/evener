package agent

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestW2Conc_ReadJobOutputSnapshotUnknownJobReturns pins the first retry site's
// non-store-closed return arm: a job that resolves to nothing yields a
// not-found error the fallback declines to redirect, so the snapshot read
// returns that error.
func TestW2Conc_ReadJobOutputSnapshotUnknownJobReturns(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	_, err := s.readJobOutputSnapshot(s.jobManager, s, "job_missing", 1024, false, nil)
	if err == nil {
		t.Fatal("readJobOutputSnapshot on an unknown job returned nil error, want not-found")
	}
}

// TestW2Conc_ReadJobOutputSnapshotWindowReadFailsReturns pins the second retry
// site's non-store-closed return arm: the record is found, but reading its
// output window fails (the terminal job's output file was removed), and the
// fallback declines to redirect so the error propagates.
func TestW2Conc_ReadJobOutputSnapshotWindowReadFailsReturns(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "some retained output\n")
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)

	// The record still exists (findJobRecord succeeds) but its output file is
	// gone, so readJobWindow fails with a non-store-closed error.
	if err := os.Remove(rec.OutputPath); err != nil {
		t.Fatalf("remove output file: %v", err)
	}

	_, err := s.readJobOutputSnapshot(s.jobManager, s, rec.JobID, 1024, false, nil)
	if err == nil {
		t.Fatal("readJobOutputSnapshot with a missing output file returned nil error, want a read failure")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want an output-read failure (record is present), not a not-found", err)
	}
}
