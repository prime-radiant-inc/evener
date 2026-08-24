package execenv

import (
	"testing"
	"time"
)

// TestCovSetGitExecTimeoutForTesting covers SetGitExecTimeoutForTesting
// (gitpath.go lines 32-36).
func TestCovSetGitExecTimeoutForTesting(t *testing.T) {
	orig := gitExecTimeout
	defer func() { gitExecTimeout = orig }()

	restore := SetGitExecTimeoutForTesting(5 * time.Second)
	if gitExecTimeout != 5*time.Second {
		t.Fatalf("gitExecTimeout = %v, want 5s", gitExecTimeout)
	}

	restore()
	if gitExecTimeout != orig {
		t.Fatalf("after restore: gitExecTimeout = %v, want %v", gitExecTimeout, orig)
	}
}

// TestCovSessionScratchDir_NoProvision covers SessionScratchDir when neither
// wrapper nor unsandboxed scratch is provisioned (local.go lines 203-213).
func TestCovSessionScratchDir_NoProvision(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	got := env.SessionScratchDir()
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
