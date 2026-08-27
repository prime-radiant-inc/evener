package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

	got, pages := walkRootActivityJobPages(t, stateDir, rootID, 3)
	want := activityJobIDs("job", jobCount)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked job IDs differ: got %d IDs, want %d", len(got), len(want))
	}
	if pages != 2 {
		t.Fatalf("walk used %d pages, want 2", pages)
	}
}

func TestLoadSessionJobActivityTree_SizeContinuationWalksRetainedJobsOnce(t *testing.T) {
	stateDir := t.TempDir()
	const (
		rootID   = "rootsizepage"
		jobCount = 6
	)
	started := time.Unix(2_000, 0).UTC()
	description := strings.Repeat("x", 900<<10)
	events := make([]jobstore.Event, 0, jobCount)
	for i := range jobCount {
		at := started.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               at,
			JobID:            fmt.Sprintf("large_%04d", i),
			Type:             jobstore.JobShell,
			OwnerSessionID:   rootID,
			VisibleToSession: rootID,
			StartedAt:        &at,
			Description:      description,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	savePastActivityMeta(t, stateDir, rootID, "Size continuation")

	got, pages := walkRootActivityJobPages(t, stateDir, rootID, 4)
	want := activityJobIDs("large", jobCount)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked job IDs differ: got %v, want %v", got, want)
	}
	if pages < 2 {
		t.Fatalf("walk used %d page, want encoded-size continuation", pages)
	}
}

func walkRootActivityJobPages(t *testing.T, stateDir, rootID string, maxPages int) ([]string, int) {
	t.Helper()
	seen := make(map[string]bool)
	ordered := make([]string, 0)
	continuation := ""
	for pageNumber := 1; pageNumber <= maxPages; pageNumber++ {
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
			ordered = append(ordered, entry.Job.JobID)
		}

		next := page.Root.Branch.Continuation
		if next == "" {
			if page.Root.Branch.Truncated {
				t.Fatalf("terminal page %d remains truncated: %+v", pageNumber, page.Root.Branch)
			}
			return ordered, pageNumber
		}
		if next == continuation {
			t.Fatalf("page %d repeated continuation %q", pageNumber, next)
		}
		continuation = next
	}
	t.Fatalf("continuation did not terminate within %d pages", maxPages)
	return nil, 0
}

func activityJobIDs(prefix string, count int) []string {
	ids := make([]string, count)
	for i := range count {
		ids[i] = fmt.Sprintf("%s_%04d", prefix, i)
	}
	return ids
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
