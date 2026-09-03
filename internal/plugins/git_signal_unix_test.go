//go:build unix

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gitCancelTripwire bounds the waits in this file. TRIPWIRE: each one is a
// hang guard, never the synchronisation mechanism — the readiness file and
// git()'s own return are the real conditions. It sits far above the expected
// time because getting the fake git to its readiness file costs two process
// creations on a machine that may be running the rest of the suite beside it;
// that latency was measured at 10s on a loaded 18-core runner, which is what
// made the old 5s ceiling fail while doing the real work.
const gitCancelTripwire = 90 * time.Second

// waitForFakeGitStart waits until the fake git has recorded that it is running
// with its TERM trap installed. run is git()'s own result channel: a fake that
// never execs will never write the file, and polling out the tripwire would
// report "never appeared" instead of naming the error git() already has.
func waitForFakeGitStart(t *testing.T, path string, run <-chan error) {
	t.Helper()
	deadline := time.Now().Add(gitCancelTripwire)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-run:
			t.Fatalf("git() returned before the fake git started: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake git never wrote %s within %s", path, gitCancelTripwire)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
		// Park in short foreground sleeps. `sleep 30 & wait $!` reads as the
		// same thing but loses the race this test creates: cancellation fires
		// the moment the readiness file lands, which is before the shell is
		// parked, and the shell's `wait` does not notice a trapped signal that
		// was already pending when it started blocking. The trap would then
		// run when the 30s sleep ended, long after git()'s WaitDelay had
		// SIGKILLed the shell, so the TERM record never arrived at all. A
		// pending trap always runs at the next command boundary, so whichever
		// side of the race the signal lands on, the record is written within
		// one sleep interval. The count bounds the fake's own lifetime, so an
		// orphan still exits on its own.
		"n=0\n" +
		"while [ $n -lt 300 ]; do sleep 0.1; n=$((n + 1)); done\n"
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
	waitForFakeGitStart(t, started, errCh)
	cancel()
	var gitErr error
	select {
	case gitErr = <-errCh:
		if gitErr == nil {
			t.Fatal("git() returned nil after cancellation; want error")
		}
	case <-time.After(gitCancelTripwire):
		t.Fatal("git() did not return after cancellation")
	}
	// git() returns only once the child has been reaped, and the trap writes
	// its record before the shell exits, so the record is on disk already or
	// it is never coming: there is nothing left to wait for here.
	if _, err := os.Stat(termed); err != nil {
		t.Fatalf("fake git exited without recording SIGTERM (%v); git() error: %v", err, gitErr)
	}
}
