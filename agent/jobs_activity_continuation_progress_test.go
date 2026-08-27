package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestLoadSessionJobActivityTree_WorkContinuationWalksRetainedJobsOnce(t *testing.T) {
	stateDir := t.TempDir()
	const (
		rootID   = "rootworkpage"
		jobCount = activityMaxWorkUnits + 1
	)
	started := time.Unix(1_000, 0).UTC()
	events := make([]jobstore.Event, 0, jobCount)
	for i := range jobCount {
		at := started.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               at,
			JobID:            fmt.Sprintf("job_%04d", i),
			Type:             jobstore.JobShell,
			OwnerSessionID:   rootID,
			VisibleToSession: rootID,
			StartedAt:        &at,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Work continuation")

	seen := make(map[string]bool, jobCount)
	continuation := ""
	for pageNumber := 1; ; pageNumber++ {
		page, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("load page %d: %v", pageNumber, err)
		}
		assertActivityPageBound(t, pageNumber, page)
		if len(page.Root.Entries) == 0 {
			t.Fatalf("page %d made no progress", pageNumber)
		}
		for _, entry := range page.Root.Entries {
			if entry.Job == nil {
				t.Fatalf("page %d entry = %+v, want shell job", pageNumber, entry)
			}
			if seen[entry.Job.JobID] {
				t.Fatalf("page %d replayed retained job %q", pageNumber, entry.Job.JobID)
			}
			seen[entry.Job.JobID] = true
		}

		next := page.Root.Branch.Continuation
		if next == "" {
			if page.Root.Branch.Truncated {
				t.Fatalf("terminal page %d remains truncated: %+v", pageNumber, page.Root.Branch)
			}
			break
		}
		if next == continuation {
			t.Fatalf("page %d repeated continuation %q", pageNumber, next)
		}
		continuation = next
		if pageNumber > 2 {
			t.Fatalf("work continuation did not terminate after two pages")
		}
	}

	if len(seen) != jobCount {
		t.Fatalf("walk retained %d jobs, want %d", len(seen), jobCount)
	}
	for i := range jobCount {
		jobID := fmt.Sprintf("job_%04d", i)
		if !seen[jobID] {
			t.Errorf("walk omitted retained job %q", jobID)
		}
	}
}

func assertActivityPageBound(t *testing.T, pageNumber int, page appwire.JobActivityTree) {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page %d: %v", pageNumber, err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page %d encoded to %d bytes, limit %d", pageNumber, len(raw), activityMaxEncodedBytes)
	}
}
