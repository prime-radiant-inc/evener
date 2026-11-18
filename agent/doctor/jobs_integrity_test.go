package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// A jobs.jsonl ending in a torn, unterminated event still loads, and the
// report carries a diagnostic for the ignored tail — the job-journal
// counterpart of TestJobs_DelegateDiagnosticsTornTail, which is what makes the
// two stores' integrity reports the same shape.
func TestJobs_JobJournalTornTailDiagnostic(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	jobsPath := filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")
	writeJobsEvents(t, jobsPath, nil)
	f, err := os.OpenFile(jobsPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"job_started","seq":1,"job_id":"job_t`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Jobs(t.Context(), base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one torn-tail diagnostic", r.Diagnostics)
	}
}

// A jobs.jsonl whose only event is a finish folds to a record, but the report
// must say the lifecycle is incomplete rather than presenting the row as an
// ordinary job.
func TestJobs_JobJournalIncompleteLifecycleDiagnostic(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), []jobstore.Event{
		{Kind: jobstore.EventJobFinished, JobID: "job_orphan", Status: jobstore.StatusCompleted, TerminalGen: "gen"},
	})

	r, err := Jobs(t.Context(), base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one incomplete-lifecycle diagnostic", r.Diagnostics)
	}
}
