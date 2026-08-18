package transcript

import "testing"

// Fixtures shared across the multi-block tests below: two delegate jobs, each
// reporting a distinct rich headline through a communicate envelope excerpt.
const (
	jobNotificationBlockA = `<job-notification job_id="job_A" job_type="delegate" status="completed" exit_code="0">` +
		`excerpt: {"data":{"test_summary":"all green","commit_hashes":["abcdef1234567890"],"concerns":["c1"]}}` +
		`</job-notification>`
	jobNotificationHeadlineA = "all green · abcdef12 · 1 concern"

	jobNotificationBlockB = `<job-notification job_id="job_B" job_type="delegate" status="completed" exit_code="0">` +
		`excerpt: {"data":{"test_summary":"3 passed","commit_hashes":["1234567890abcdef"]}}` +
		`</job-notification>`
	jobNotificationHeadlineB = "3 passed · 12345678"
)

func requireJobNotificationTie(t *testing.T, ties []JobNotificationTie, jobID string) JobNotificationTie {
	t.Helper()
	for _, tie := range ties {
		if tie.JobID == jobID {
			return tie
		}
	}
	t.Fatalf("ties = %+v, want a tie for job %q", ties, jobID)
	return JobNotificationTie{}
}

func TestParseJobNotificationHeadlines(t *testing.T) {
	t.Run("single block unchanged", func(t *testing.T) {
		ties := ParseJobNotificationHeadlines(jobNotificationBlockA)
		if len(ties) != 1 {
			t.Fatalf("len(ties) = %d, want 1: %+v", len(ties), ties)
		}
		tie := ties[0]
		if tie.JobID != "job_A" {
			t.Fatalf("JobID = %q, want job_A", tie.JobID)
		}
		if tie.Headline != jobNotificationHeadlineA {
			t.Fatalf("Headline = %q, want %q", tie.Headline, jobNotificationHeadlineA)
		}
		if tie.IsError {
			t.Fatal("IsError = true, want false")
		}
	})

	t.Run("several job-notification blocks each parse individually (A then B)", func(t *testing.T) {
		ties := ParseJobNotificationHeadlines(jobNotificationBlockA + "\n" + jobNotificationBlockB)
		if len(ties) != 2 {
			t.Fatalf("len(ties) = %d, want 2: %+v", len(ties), ties)
		}
		tieA := requireJobNotificationTie(t, ties, "job_A")
		if tieA.Headline != jobNotificationHeadlineA || tieA.IsError {
			t.Fatalf("job_A tie = %+v, want headline %q, no error", tieA, jobNotificationHeadlineA)
		}
		tieB := requireJobNotificationTie(t, ties, "job_B")
		if tieB.Headline != jobNotificationHeadlineB || tieB.IsError {
			t.Fatalf("job_B tie = %+v, want headline %q, no error", tieB, jobNotificationHeadlineB)
		}
	})

	t.Run("several job-notification blocks each parse individually (B then A)", func(t *testing.T) {
		ties := ParseJobNotificationHeadlines(jobNotificationBlockB + "\n" + jobNotificationBlockA)
		if len(ties) != 2 {
			t.Fatalf("len(ties) = %d, want 2: %+v", len(ties), ties)
		}
		tieA := requireJobNotificationTie(t, ties, "job_A")
		if tieA.Headline != jobNotificationHeadlineA || tieA.IsError {
			t.Fatalf("job_A tie = %+v, want headline %q, no error", tieA, jobNotificationHeadlineA)
		}
		tieB := requireJobNotificationTie(t, ties, "job_B")
		if tieB.Headline != jobNotificationHeadlineB || tieB.IsError {
			t.Fatalf("job_B tie = %+v, want headline %q, no error", tieB, jobNotificationHeadlineB)
		}
	})

	t.Run("a block with isError", func(t *testing.T) {
		text := `<job-notification job_id="job_E" status="failed" exit_code="1"></job-notification>`
		ties := ParseJobNotificationHeadlines(text)
		if len(ties) != 1 {
			t.Fatalf("len(ties) = %d, want 1: %+v", len(ties), ties)
		}
		if !ties[0].IsError {
			t.Fatal("IsError = false, want true")
		}
		if ties[0].Headline != "failed" {
			t.Fatalf("Headline = %q, want failed", ties[0].Headline)
		}
	})

	t.Run("text with no blocks", func(t *testing.T) {
		ties := ParseJobNotificationHeadlines("just some steering text, no notification here")
		if ties != nil {
			t.Fatalf("ties = %+v, want nil", ties)
		}
	})
}

// TestParseJobNotificationHeadlineDelegatesToPlural pins the single-block
// entry point (still used directly in a couple of call sites and tests) to
// the same per-block parsing the multi-block entry point uses, so the two
// never drift into divergent regexes.
func TestParseJobNotificationHeadlineDelegatesToPlural(t *testing.T) {
	jobID, headline, isErr, ok := ParseJobNotificationHeadline(jobNotificationBlockA)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if jobID != "job_A" {
		t.Fatalf("jobID = %q, want job_A", jobID)
	}
	if headline != jobNotificationHeadlineA {
		t.Fatalf("headline = %q, want %q", headline, jobNotificationHeadlineA)
	}
	if isErr {
		t.Fatal("isError = true, want false")
	}

	// A steering event carrying several blocks: the singular entry point
	// keeps returning only the first block's tie (its documented contract),
	// never an aggregate across blocks.
	jobID, headline, _, ok = ParseJobNotificationHeadline(jobNotificationBlockA + "\n" + jobNotificationBlockB)
	if !ok || jobID != "job_A" || headline != jobNotificationHeadlineA {
		t.Fatalf("multi-block input: jobID=%q headline=%q ok=%v, want job_A %q true", jobID, headline, ok, jobNotificationHeadlineA)
	}
}
