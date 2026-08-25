package msgrender

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// TestSubagentRunBodyShellEmptyCommand covers the branch where
// run.JobType is "shell" but run.Command is empty, hitting the
// label = "Shell" fallback (line 273-274).
func TestSubagentRunBodyShellEmptyCommand(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType: "shell",
		Status:  "done",
	}, 80)
	if !strings.Contains(body, "Shell") {
		t.Fatalf("empty shell command should use 'Shell' label, got %q", body)
	}
}

// TestSubagentRunBodyRunningForMS covers the else branch where
// run.DurationMS is nil but run.RunningForMS is set (line 299).
func TestSubagentRunBodyRunningForMS(t *testing.T) {
	rms := int64(5000)
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType:      "shell",
		Command:      "echo hi",
		Status:       "running",
		RunningForMS: &rms,
	}, 80)
	if !strings.Contains(body, "running") {
		t.Fatalf("should show running duration, got %q", body)
	}
}

// TestSubagentRunBodyWorktreePathFallback covers the branch where
// run.Worktree.Branch is empty and it falls back to Worktree.Path
// (line 310-311).
func TestSubagentRunBodyWorktreePathFallback(t *testing.T) {
	body := SubagentRunBody(transcript.SubagentRunInfo{
		JobType: "shell",
		Command: "echo hi",
		Status:  "done",
		Worktree: &appwire.JobActivityWorktree{
			Path: "/tmp/worktree-branch",
		},
	}, 80)
	if !strings.Contains(body, "/tmp/worktree-branch") {
		t.Fatalf("should show worktree path when branch is empty, got %q", body)
	}
}
