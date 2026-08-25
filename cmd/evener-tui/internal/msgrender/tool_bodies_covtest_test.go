package msgrender

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// --- SubagentRunBody: empty status falls back to "running" ---

func TestCovSubagentRunBodyEmptyStatusFallback(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{}, 80)
	if !strings.Contains(body, "running") {
		t.Fatalf("empty status should fall back to 'running': %q", body)
	}
}

// --- SubagentRunBody: delegate with no delegate ID ---

func TestCovSubagentRunBodyDelegateNoID(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Task:   "work",
		Status: "running",
	}, 80)
	if !strings.Contains(body, "Delegate") {
		t.Fatalf("should show 'Delegate' label: %q", body)
	}
	if !strings.Contains(body, "work") {
		t.Fatalf("should show task: %q", body)
	}
}

// --- SubagentRunBody: delegate with delegate ID ---

func TestCovSubagentRunBodyDelegateWithID(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		DelegateID: "dlg-abc",
		Status:     "running",
	}, 80)
	if !strings.Contains(body, "dlg-abc") {
		t.Fatalf("should show delegate ID: %q", body)
	}
}

// --- SubagentRunBody: with job ID ---

func TestCovSubagentRunBodyWithJobID(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType: "shell",
		Command: "echo hi",
		Status:  "done",
		JobID:   "job_02wMz5TxvEMoJEDTDGOTil_000000000123",
	}, 80)
	if !strings.Contains(body, "job job_02wMz5Txv…000000000123") {
		t.Fatalf("should show the compact job ID, got %q", body)
	}
}

// --- SubagentRunBody: with parent delegate ID ---

func TestCovSubagentRunBodyParentDelegateID(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Status:           "running",
		ParentDelegateID: "dlg-parent",
	}, 80)
	if !strings.Contains(body, "parent dlg-parent") {
		t.Fatalf("should show parent delegate ID: %q", body)
	}
}

// --- SubagentRunBody: transcript ref hidden at narrow width ---

func TestCovSubagentRunBodyTranscriptRefNarrow(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Status:        "running",
		TranscriptRef: "local:abc",
	}, 30)
	if strings.Contains(body, "transcript") {
		t.Fatalf("transcript ref should be hidden at narrow width: %q", body)
	}
}

// --- SubagentRunBody: transcript ref shown at wide width ---

func TestCovSubagentRunBodyTranscriptRefWide(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Status:        "running",
		TranscriptRef: "local:abc",
	}, 80)
	if !strings.Contains(body, "transcript local:abc") {
		t.Fatalf("transcript ref should be shown at wide width: %q", body)
	}
}

// --- SubagentRunBody: with DurationMS ---

func TestCovSubagentRunBodyDurationMS(t *testing.T) {
	dur := int64(3000)
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType:    "shell",
		Command:    "echo hi",
		Status:     "done",
		DurationMS: &dur,
	}, 80)
	if !strings.Contains(body, "3.0s") {
		t.Fatalf("should show duration: %q", body)
	}
}

// --- SubagentRunBody: with usage ---

func TestCovSubagentRunBodyUsage(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType: "shell",
		Command: "echo hi",
		Status:  "done",
		Usage:   &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50},
	}, 80)
	if !strings.Contains(body, "100 in / 50 out") {
		t.Fatalf("should show usage: %q", body)
	}
}

// --- SubagentRunBody: with QuietForMS ---

func TestCovSubagentRunBodyQuietForMS(t *testing.T) {
	quiet := int64(5000)
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType:    "shell",
		Command:    "echo hi",
		Status:     "running",
		QuietForMS: &quiet,
	}, 80)
	if !strings.Contains(body, "quiet 5.0s") {
		t.Fatalf("should show exact quiet duration: %q", body)
	}
}

// --- SubagentRunBody: with worktree ahead and dirty ---

func TestCovSubagentRunBodyWorktreeAheadDirty(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType: "shell",
		Command: "echo hi",
		Status:  "running",
		Worktree: &appwire.JobActivityWorktree{
			Branch: "feature-x",
			Ahead:  3,
			Dirty:  true,
		},
	}, 80)
	if !strings.Contains(body, "feature-x") {
		t.Fatalf("should show worktree branch: %q", body)
	}
	if !strings.Contains(body, "ahead 3") {
		t.Fatalf("should show ahead count: %q", body)
	}
	if !strings.Contains(body, "dirty") {
		t.Fatalf("should show dirty: %q", body)
	}
}

// --- SubagentRunBody: with warnings ---

func TestCovSubagentRunBodyWarnings(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Status:   "running",
		Warnings: []string{"deprecated API", "  ", ""},
	}, 80)
	if !strings.Contains(body, "warning: deprecated API") {
		t.Fatalf("should show warning: %q", body)
	}
}

// --- SubagentRunBody: terminal status with outcome ---

func TestCovSubagentRunBodyTerminalOutcome(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		Terminal: true,
		Outcome:  "completed",
	}, 80)
	if !strings.Contains(body, "completed") {
		t.Fatalf("should show terminal outcome: %q", body)
	}
}
