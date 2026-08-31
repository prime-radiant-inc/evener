//go:build unix

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
}

// TestGit_CancellationSendsSIGTERM pins the graceful-cancellation contract of
// the git() helper: on context cancellation the child must receive SIGTERM —
// git's signal handlers remove its own lock files (.git/index.lock etc.) on
// termination — rather than exec's default SIGKILL, which strands those lock
// files and wedges persistent clones. A fake `git` first in PATH traps TERM
// and records its delivery.
func TestGit_CancellationSendsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	termed := filepath.Join(dir, "termed")
	script := "#!/bin/sh\n" +
		"trap 'touch " + termed + "; exit 143' TERM\n" +
		"touch " + started + "\n" +
		// Background + wait keeps the shell interruptible (the trap fires
		// during wait); the redirect keeps the orphaned sleep from holding
		// git()'s output pipe open past the shell's exit.
		"sleep 30 >/dev/null 2>&1 & wait $!\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := git(ctx, "", "fetch")
		errCh <- err
	}()
	waitForFile(t, started)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("git() returned nil after cancellation; want error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("git() did not return after cancellation")
	}
	waitForFile(t, termed)
}
