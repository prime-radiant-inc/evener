//go:build unix

package agent

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// A NonGit session can name its .git/commondir a FIFO. A blocking open would
// stall managed-worktree validation for as long as the session wants, a denial
// of service the request context cannot interrupt. The descriptor walk opens
// non-blocking and refuses a non-regular leaf (#3724 review).
func TestWorktreeList_RefusesFIFOCommondir(t *testing.T) {
	fixture := canonicalFixtureTempDir(t, "worktree-fifo-commondir-*")
	_, _, env := newSandboxedFixtureSession(t, fixture, "worktree-fifo-commondir-state-*")
	if env.Sandbox.Git.Kind != sandbox.NonGit {
		t.Fatalf("fixture sandbox kind = %v, want NonGit", env.Sandbox.Git.Kind)
	}
	writeMinimalMainGitDir(t, fixture)
	fifo := filepath.Join(fixture, ".git", "commondir")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- rejectUntrustedMainRoot(env, fixture) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("main root with a FIFO commondir was accepted")
		}
		if !errors.Is(err, errUntrustedWorktreeProject) {
			t.Fatalf("main root error = %v, want the untrusted-project refusal", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("managed-worktree validation blocked on a FIFO commondir")
	}
}
