package execenv

import (
	"testing"
	"time"
)

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
