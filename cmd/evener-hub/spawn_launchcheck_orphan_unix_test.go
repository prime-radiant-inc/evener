//go:build unix

package hub

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestEvenerLaunchCheckDeadlineDoesNotWaitForAnOrphanedPipeHolder pins that the
// launch-check budget bounds the call even when the checked binary leaves a
// child behind holding its output: a wrapper script around evener is enough.
// Killing the direct child does not close a pipe the grandchild inherited, so
// without a WaitDelay the hub's "timeout" lasts as long as the grandchild does.
//
// The grandchild blocks on a FIFO only this test releases, and the test
// releases it only after the call returns. A call that waits for the
// grandchild therefore cannot return at all; the watchdog below is a tripwire
// that unblocks it and fails the test rather than hanging the suite.
func TestEvenerLaunchCheckDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	const orphansAPipeHolder = `#!/bin/sh
dir=$(dirname "$0")
cat "$dir/release" &
echo started > "$dir/started"
wait
`
	for name, call := range map[string]func(ctx context.Context, evenerBinary string) error{
		"validate": func(ctx context.Context, evenerBinary string) error {
			return validateEvenerLaunchContract(ctx, evenerBinary, "openrouter/free", nil)
		},
		"models": func(ctx context.Context, evenerBinary string) error {
			_, err := listEvenerLaunchModelContract(ctx, evenerBinary, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			evenerBinary := filepath.Join(dir, "fake-evener")
			writeFakeEvener(t, evenerBinary, orphansAPipeHolder)
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			for _, fifo := range []string{started, release} {
				if err := syscall.Mkfifo(fifo, 0o600); err != nil {
					t.Fatalf("mkfifo %s: %v", fifo, err)
				}
			}
			// Opening a FIFO for writing blocks until its reader opens it, so
			// this returns only once the grandchild is waiting on it.
			releaseGrandchild := func() {
				if f, err := os.OpenFile(release, os.O_WRONLY, 0); err == nil {
					_ = f.Close()
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				// Reading "started" returns once the fake has backgrounded the
				// grandchild, so the cancel below cannot race its creation.
				_, _ = os.ReadFile(started)
				cancel()
			}()
			done := make(chan error, 1)
			go func() { done <- call(ctx, evenerBinary) }()

			select {
			case err := <-done:
				releaseGrandchild()
				assertHubLaunchError(t, err)
			case <-time.After(10 * time.Second):
				releaseGrandchild()
				<-done
				t.Fatal("launch-check did not return after its context ended while an orphaned grandchild held its output pipe")
			}
		})
	}
}
