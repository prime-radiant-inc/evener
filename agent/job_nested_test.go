package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// finishRunningTestJob completes a test shell through the real job-manager
// finalization path and waits for its durable completion boundary.
func finishRunningTestJob(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	code := 0
	if err := jm.finalize(jobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize job %q: %v", jobID, err)
	}
}

func findListedJob(records []*jobstore.JobRecord, jobID string) *jobstore.JobRecord {
	for _, record := range records {
		if record != nil && record.JobID == jobID {
			return record
		}
	}
	return nil
}
