package hub

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// TestDelegateJobIDFromRawEmpty covers the empty-raw path.
func TestDelegateJobIDFromRawEmpty(t *testing.T) {
	if got := delegateJobIDFromRaw(nil); got != "" {
		t.Fatalf("delegateJobIDFromRaw(nil) = %q, want empty", got)
	}
	if got := delegateJobIDFromRaw(json.RawMessage{}); got != "" {
		t.Fatalf("delegateJobIDFromRaw(empty) = %q, want empty", got)
	}
}

// TestDelegateJobIDFromRawInvalidJSON covers the invalid-JSON path.
func TestDelegateJobIDFromRawInvalidJSON(t *testing.T) {
	if got := delegateJobIDFromRaw(json.RawMessage("not json")); got != "" {
		t.Fatalf("delegateJobIDFromRaw(invalid) = %q, want empty", got)
	}
}

// TestDelegateJobIDFromRawJobID covers the job_id field path.
func TestDelegateJobIDFromRawJobID(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job-123"}`)
	if got := delegateJobIDFromRaw(raw); got != "job-123" {
		t.Fatalf("delegateJobIDFromRaw = %q, want job-123", got)
	}
}

// TestDelegateJobIDFromRawStartedJobID covers the started_job_id fallback path.
func TestDelegateJobIDFromRawStartedJobID(t *testing.T) {
	raw := json.RawMessage(`{"started_job_id":"started-456"}`)
	if got := delegateJobIDFromRaw(raw); got != "started-456" {
		t.Fatalf("delegateJobIDFromRaw = %q, want started-456", got)
	}
}

// TestDelegateJobIDFromRawCurrentJobID covers the current_job_id fallback path.
func TestDelegateJobIDFromRawCurrentJobID(t *testing.T) {
	raw := json.RawMessage(`{"current_job_id":"current-789"}`)
	if got := delegateJobIDFromRaw(raw); got != "current-789" {
		t.Fatalf("delegateJobIDFromRaw = %q, want current-789", got)
	}
}

// TestDelegateJobIDFromRawLatestJobID covers the latest_job_id fallback path.
func TestDelegateJobIDFromRawLatestJobID(t *testing.T) {
	raw := json.RawMessage(`{"latest_job_id":"latest-012"}`)
	if got := delegateJobIDFromRaw(raw); got != "latest-012" {
		t.Fatalf("delegateJobIDFromRaw = %q, want latest-012", got)
	}
}

// TestDelegateJobIDFromRawPriority covers the priority order: job_id first,
// then started_job_id, then current_job_id, then latest_job_id.
func TestDelegateJobIDFromRawPriority(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"first","started_job_id":"second","current_job_id":"third","latest_job_id":"fourth"}`)
	if got := delegateJobIDFromRaw(raw); got != "first" {
		t.Fatalf("delegateJobIDFromRaw = %q, want first (priority)", got)
	}
}

// TestDelegateJobIDFromRawWhitespaceCovers covers the TrimSpace path.
func TestDelegateJobIDFromRawWhitespace(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"  spaced  "}`)
	if got := delegateJobIDFromRaw(raw); got != "spaced" {
		t.Fatalf("delegateJobIDFromRaw = %q, want spaced", got)
	}
}

// TestDelegateJobIDFromRawAllEmpty covers the path where all fields are empty.
func TestDelegateJobIDFromRawAllEmpty(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"","started_job_id":"","current_job_id":"","latest_job_id":""}`)
	if got := delegateJobIDFromRaw(raw); got != "" {
		t.Fatalf("delegateJobIDFromRaw = %q, want empty", got)
	}
}

// TestIsTerminalHistoricalJobStatusTerminal covers the terminal statuses.
func TestIsTerminalHistoricalJobStatusTerminal(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", "stopped", "exhausted"} {
		if !isTerminalHistoricalJobStatus(status) {
			t.Fatalf("isTerminalHistoricalJobStatus(%q) = false, want true", status)
		}
	}
}

// TestIsTerminalHistoricalJobStatusNonTerminal covers the non-terminal path.
func TestIsTerminalHistoricalJobStatusNonTerminal(t *testing.T) {
	for _, status := range []string{"running", "idle", "", "unknown"} {
		if isTerminalHistoricalJobStatus(status) {
			t.Fatalf("isTerminalHistoricalJobStatus(%q) = true, want false", status)
		}
	}
}

// TestCloneHubBoolNonNil covers the non-nil path.
func TestCloneHubBoolNonNil(t *testing.T) {
	v := true
	clone := cloneHubBool(&v)
	if clone == nil || *clone != true {
		t.Fatalf("cloneHubBool(&true) = %v, want &true", clone)
	}
	*clone = false
	if v != true {
		t.Fatalf("original was mutated: v = %v, want true", v)
	}
}

// TestCloneHubBoolNil covers the nil path.
func TestCloneHubBoolNil(t *testing.T) {
	if clone := cloneHubBool(nil); clone != nil {
		t.Fatalf("cloneHubBool(nil) = %v, want nil", clone)
	}
}

// TestCloneHubInt64NonNil covers the non-nil path.
func TestCloneHubInt64NonNil(t *testing.T) {
	v := int64(42)
	clone := cloneHubInt64(&v)
	if clone == nil || *clone != 42 {
		t.Fatalf("cloneHubInt64(&42) = %v, want &42", clone)
	}
	*clone = 99
	if v != 42 {
		t.Fatalf("original was mutated: v = %d, want 42", v)
	}
}

// TestCloneHubInt64Nil covers the nil path.
func TestCloneHubInt64Nil(t *testing.T) {
	if clone := cloneHubInt64(nil); clone != nil {
		t.Fatalf("cloneHubInt64(nil) = %v, want nil", clone)
	}
}

// TestOutputImageDescriptorKeySHA covers the SHA path.
func TestOutputImageDescriptorKeySHA(t *testing.T) {
	img := appwire.OutputImage{SHA: "abc123"}
	if got := outputImageDescriptorKey(img); got != "sha:abc123" {
		t.Fatalf("key = %q, want sha:abc123", got)
	}
}

// TestOutputImageDescriptorKeyURL covers the URL path.
func TestOutputImageDescriptorKeyURL(t *testing.T) {
	img := appwire.OutputImage{URL: "https://example.com/img.png"}
	if got := outputImageDescriptorKey(img); got != "https://example.com/img.png" {
		t.Fatalf("key = %q, want URL", got)
	}
}

// TestOutputImageDescriptorKeyPath covers the Path path.
func TestOutputImageDescriptorKeyPath(t *testing.T) {
	img := appwire.OutputImage{Path: "/tmp/img.png"}
	if got := outputImageDescriptorKey(img); got != "path:/tmp/img.png" {
		t.Fatalf("key = %q, want path:/tmp/img.png", got)
	}
}

// TestOutputImageDescriptorKeyEmpty covers the empty path.
func TestOutputImageDescriptorKeyEmpty(t *testing.T) {
	img := appwire.OutputImage{}
	if got := outputImageDescriptorKey(img); got != "" {
		t.Fatalf("key = %q, want empty", got)
	}
}

// TestOutputImageDescriptorKeyPriority covers that SHA takes priority over URL.
func TestOutputImageDescriptorKeyPriority(t *testing.T) {
	img := appwire.OutputImage{SHA: "sha", URL: "url", Path: "path"}
	if got := outputImageDescriptorKey(img); got != "sha:sha" {
		t.Fatalf("key = %q, want sha:sha (SHA has priority)", got)
	}
}

// TestAppwireDelegateFromAgentStatus covers the full mapping function.
func TestAppwireDelegateFromAgentStatus(t *testing.T) {
	delegate := agent.DelegateStatusInfo{
		DelegateID:     "dlg-1",
		OwnerSessionID: "sess-1",
		Status:         "running",
		Terminal:       false,
		Resumable:      true,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.DelegateID != "dlg-1" {
		t.Fatalf("DelegateID = %q, want dlg-1", info.DelegateID)
	}
	if info.OwnerSessionID != "sess-1" {
		t.Fatalf("OwnerSessionID = %q, want sess-1", info.OwnerSessionID)
	}
	if info.Status != "running" {
		t.Fatalf("Status = %q, want running", info.Status)
	}
	if info.Terminal != false {
		t.Fatal("Terminal = true, want false")
	}
	if info.Resumable != true {
		t.Fatal("Resumable = false, want true")
	}
}

// TestAppwireDelegateFromAgentStatusWithUsage covers the Usage non-nil path.
func TestAppwireDelegateFromAgentStatusWithUsage(t *testing.T) {
	usage := appwire.EvenerUsage{InputTokens: 100}
	delegate := agent.DelegateStatusInfo{
		DelegateID: "dlg-2",
		Usage:      &usage,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.Usage == nil || info.Usage.InputTokens != 100 {
		t.Fatalf("Usage = %+v, want InputTokens=100", info.Usage)
	}
}

// TestAppwireDelegateFromAgentStatusWithWorktree covers the Worktree non-nil path.
func TestAppwireDelegateFromAgentStatusWithWorktree(t *testing.T) {
	worktree := appwire.JobActivityWorktree{Branch: "test"}
	delegate := agent.DelegateStatusInfo{
		DelegateID: "dlg-3",
		Worktree:   &worktree,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.Worktree == nil || info.Worktree.Branch != "test" {
		t.Fatalf("Worktree = %+v, want Branch=test", info.Worktree)
	}
}

// TestReconcileDelegateThreadItemsEmpty covers the empty-jobs path.
func TestReconcileDelegateThreadItemsEmptyJobs(t *testing.T) {
	thread := appwire.Thread{Turns: []appwire.Turn{{ID: "turn-1"}}}
	got := reconcileDelegateThreadItems(thread, nil)
	if len(got.Turns) != 1 || got.Turns[0].ID != "turn-1" {
		t.Fatalf("reconcileDelegateThreadItems with empty jobs should return thread unchanged")
	}
}

// TestReconcileDelegateThreadItemsEmptyTurns covers the empty-turns path.
func TestReconcileDelegateThreadItemsEmptyTurns(t *testing.T) {
	thread := appwire.Thread{Turns: nil}
	jobs := map[string]agent.HistoricalJobRecord{"job-1": {}}
	got := reconcileDelegateThreadItems(thread, jobs)
	if len(got.Turns) != 0 {
		t.Fatalf("reconcileDelegateThreadItems with empty turns should return no turns, got %d", len(got.Turns))
	}
}
