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
	// The backgrounded child opens the release FIFO before it announces
	// itself, so by the time the test hears "started" the grandchild is
	// attached to the release and blocked reading it, holding the output pipe.
	const orphansAPipeHolder = `#!/bin/sh
dir=$(dirname "$0")
{ exec 3<"$dir/release"; echo started > "$dir/started"; cat <&3; } &
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
			// The test holds both FIFOs open read-write, which never blocks, so
			// no setup failure can hang it: the fake's opens succeed at once,
			// the grandchild reads the release until the test's end closes,
			// and cleanup closes both even when the fake never ran.
			startedEnd, err := os.OpenFile(started, os.O_RDWR, 0)
			if err != nil {
				t.Fatalf("open %s: %v", started, err)
			}
			t.Cleanup(func() { _ = startedEnd.Close() })
			releaseEnd, err := os.OpenFile(release, os.O_RDWR, 0)
			if err != nil {
				t.Fatalf("open %s: %v", release, err)
			}
			releaseGrandchild := func() { _ = releaseEnd.Close() }
			t.Cleanup(releaseGrandchild)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				// The first byte arrives once the grandchild holds the release,
				// so the cancel below cannot race its creation. A read that
				// fails because cleanup closed the FIFO cancels harmlessly.
				_, _ = startedEnd.Read(make([]byte, 1))
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
