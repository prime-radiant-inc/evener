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

func TestLoadSessionJobActivityTree_SizeContinuationSkipsUnrepresentableEntry(t *testing.T) {
	assertUnrepresentableActivityEntryWalk(t, strings.Repeat("x", activityMaxEncodedBytes+(256<<10)))
}

func TestLoadSessionJobActivityTree_SizeContinuationAccountsForResponseEnvelope(t *testing.T) {
	assertUnrepresentableActivityEntryWalk(t, activityBoundaryDescription(t))
}

func activityBoundaryDescription(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	const rootID = "rootoversizedpage"
	started := time.Unix(3_000, 0).UTC()
	s1cov_writeJobLog(t, stateDir, rootID, jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "oversized_job",
		Type:             jobstore.JobShell,
		OwnerSessionID:   rootID,
		VisibleToSession: rootID,
		StartedAt:        &started,
		Description:      "x",
	})
	savePastActivityMeta(t, stateDir, rootID, "Oversized continuation")
	probe, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("load boundary probe: %v", err)
	}
	if len(probe.Root.Entries) != 1 || probe.Root.Entries[0].Job == nil {
		t.Fatalf("boundary probe entries = %+v, want one shell job", probe.Root.Entries)
	}
	entry := probe.Root.Entries[0]
	entry.Job.Description = ""
	base, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal boundary entry base: %v", err)
	}
	description := strings.Repeat("x", activityMaxEncodedBytes-len(base)-1)
	entry.Job.Description = description
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal boundary entry: %v", err)
	}
	if len(encodedEntry) != activityMaxEncodedBytes-1 {
		t.Fatalf("boundary entry encoded to %d bytes, want %d", len(encodedEntry), activityMaxEncodedBytes-1)
	}
	probe.Root.Entries[0] = entry
	encodedTree, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal boundary tree: %v", err)
	}
	if len(encodedTree) <= activityMaxEncodedBytes {
		t.Fatalf("boundary tree encoded to %d bytes, want more than %d", len(encodedTree), activityMaxEncodedBytes)
	}
	return description
}

func assertUnrepresentableActivityEntryWalk(t *testing.T, description string) {
	t.Helper()
	stateDir := t.TempDir()
	const rootID = "rootoversizedpage"
	started := time.Unix(3_000, 0).UTC()
	normalStarted := started.Add(time.Second)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               started,
			JobID:            "oversized_job",
			Type:             jobstore.JobShell,
			OwnerSessionID:   rootID,
			VisibleToSession: rootID,
			StartedAt:        &started,
			Description:      description,
		},
		jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               normalStarted,
			JobID:            "normal_job",
			Type:             jobstore.JobShell,
			OwnerSessionID:   rootID,
			VisibleToSession: rootID,
			StartedAt:        &normalStarted,
			Description:      "reachable after oversized job",
		},
	)
	savePastActivityMeta(t, stateDir, rootID, "Oversized continuation")

	first, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("load omission page: %v", err)
	}
	assertActivityPageBound(t, 1, first)
	if len(first.Root.Entries) != 0 {
		t.Fatalf("omission page entries = %+v, want no representable entries", first.Root.Entries)
	}
	if first.Root.Branch.Error == "" {
		t.Fatal("omission page branch error is empty")
	}
	if first.Root.Branch.Continuation == "" || !first.Root.Branch.Truncated {
		t.Fatalf("omission page branch = %+v, want a continuation", first.Root.Branch)
	}

	second, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{
		Continuation: first.Root.Branch.Continuation,
	})
	if err != nil {
		t.Fatalf("load page after omission: %v", err)
	}
	assertActivityPageBound(t, 2, second)
	if len(second.Root.Entries) != 1 || second.Root.Entries[0].Job == nil {
		t.Fatalf("page after omission entries = %+v, want one shell job", second.Root.Entries)
	}
	if second.Root.Entries[0].Job.JobID != "normal_job" {
		t.Fatalf("page after omission job = %q, want normal_job", second.Root.Entries[0].Job.JobID)
	}
	if second.Root.Branch.Truncated || second.Root.Branch.Continuation != "" {
		t.Fatalf("page after omission branch = %+v, want terminal completion", second.Root.Branch)
	}
}

func TestLoadSessionJobActivityTree_DepthContinuationStartsAtOmittedBranch(t *testing.T) {
	stateDir := t.TempDir()
	const delegateCount = activityMaxNewDepth*2 + 6
	sessionIDs := make([]string, delegateCount+1)
	sessionIDs[0] = "rootdepthpage"
	for i := 1; i <= delegateCount; i++ {
		sessionIDs[i] = fmt.Sprintf("depth%03d", i)
	}
	for i := range delegateCount {
		writePastStableDelegates(t, stateDir, sessionIDs[i], pastStableDescriptor(sessionIDs[i], sessionIDs[i+1], fmt.Sprintf("depth task %d", i)))
	}
	for i, sessionID := range sessionIDs {
		s1cov_writeJobLog(t, stateDir, sessionID)
		savePastActivityMeta(t, stateDir, sessionID, fmt.Sprintf("Depth %d", i))
	}

	seen := make(map[string]bool, delegateCount)
	continuation := ""
	targetSessionID := sessionIDs[0]
	nextDelegate := 0
	for pageNumber := 1; pageNumber <= 4; pageNumber++ {
		page, err := LoadSessionJobActivityTree(stateDir, sessionIDs[0], appwire.JobsListParams{Continuation: continuation})
		if err != nil {
			t.Fatalf("load page %d: %v", pageNumber, err)
		}
		assertActivityPageBound(t, pageNumber, page)
		target := findActivitySession(t, &page.Root, targetSessionID)
		next := ""
		for current := target; len(current.Entries) > 0; {
			if len(current.Entries) != 1 || current.Entries[0].Delegate == nil {
				t.Fatalf("page %d session %q entries = %+v, want one chain delegate", pageNumber, current.SessionID, current.Entries)
			}
			delegate := current.Entries[0].Delegate
			wantID := "dlg_" + sessionIDs[nextDelegate+1]
			if delegate.DelegateID != wantID {
				t.Fatalf("page %d delegate %d = %q, want %q", pageNumber, nextDelegate, delegate.DelegateID, wantID)
			}
			if seen[delegate.DelegateID] {
				t.Fatalf("page %d replayed retained delegate %q below continuation target", pageNumber, delegate.DelegateID)
			}
			seen[delegate.DelegateID] = true
			nextDelegate++
			if delegate.Branch.Continuation != "" {
				if delegate.Child != nil {
					t.Fatalf("page %d truncated delegate %q unexpectedly included its omitted child", pageNumber, delegate.DelegateID)
				}
				next = delegate.Branch.Continuation
				targetSessionID = delegate.ChildSessionID
				break
			}
			if delegate.Child == nil {
				t.Fatalf("page %d delegate %q omitted child without continuation: branch=%+v diagnostics=%v", pageNumber, delegate.DelegateID, delegate.Branch, delegate.Diagnostics)
			}
			current = delegate.Child
		}

		if next == "" {
			if nextDelegate != delegateCount {
				t.Fatalf("depth walk ended after %d delegates, want %d", nextDelegate, delegateCount)
			}
			if len(seen) != delegateCount {
				t.Fatalf("depth walk retained %d unique delegates, want %d", len(seen), delegateCount)
			}
			return
		}
		if next == continuation {
			t.Fatalf("page %d repeated continuation %q", pageNumber, next)
		}
		continuation = next
	}
	t.Fatalf("depth continuation did not terminate within four pages")
}

func findActivitySession(t *testing.T, root *appwire.JobActivitySession, sessionID string) *appwire.JobActivitySession {
	t.Helper()
	for current := root; current != nil; {
		if current.SessionID == sessionID {
			return current
		}
		if len(current.Entries) != 1 || current.Entries[0].Delegate == nil {
			t.Fatalf("continuation path ended before target session %q", sessionID)
		}
		current = current.Entries[0].Delegate.Child
	}
	t.Fatalf("continuation omitted target session %q", sessionID)
	return nil
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
