//go:build unix

package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
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

// TestEvenerLaunchCheckSuccessSurvivesAnOrphanedPipeHolder is the other side
// of the WaitDelay bound: a check that answered and exited 0 while leaving a
// child on its output pipe still answered. Once the delay closes the pipe,
// exec reports ErrWaitDelay over the complete output, and that must read as
// the response it is, not as a failed check quoting its own JSON.
func TestEvenerLaunchCheckSuccessSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	response := fmt.Sprintf(`{"protocol":%q,"launch_flags":[%q],"models":[{"provider":"openai","model":"gpt-5"}]}`, appwire.ProtocolVersion, requiredLaunchFlag)
	answersAndOrphans := "#!/bin/sh\n" +
		"dir=$(dirname \"$0\")\n" +
		"{ exec 3<\"$dir/release\"; cat <&3; } &\n" +
		"printf '%s\\n' '" + response + "'\n"
	for name, call := range map[string]func(evenerBinary string) error{
		"validate": func(evenerBinary string) error {
			return validateEvenerLaunchContract(context.Background(), evenerBinary, "openai/gpt-5", nil)
		},
		"models": func(evenerBinary string) error {
			resp, err := listEvenerLaunchModelContract(context.Background(), evenerBinary, nil)
			if err == nil && (len(resp.Data) != 1 || resp.Data[0].Model != "gpt-5") {
				return fmt.Errorf("models = %+v, want the one the check answered", resp.Data)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			evenerBinary := filepath.Join(dir, "fake-evener")
			writeFakeEvener(t, evenerBinary, answersAndOrphans)
			release := filepath.Join(dir, "release")
			if err := syscall.Mkfifo(release, 0o600); err != nil {
				t.Fatalf("mkfifo: %v", err)
			}
			// Held read-write so neither side's open blocks; closing it at the
			// end ends the orphan.
			releaseEnd, err := os.OpenFile(release, os.O_RDWR, 0)
			if err != nil {
				t.Fatalf("open %s: %v", release, err)
			}
			t.Cleanup(func() { _ = releaseEnd.Close() })
			done := make(chan error, 1)
			go func() { done <- call(evenerBinary) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("a check that answered and exited 0 was reported as: %v", err)
				}
			case <-time.After(10 * time.Second):
				// TRIPWIRE: a call that waits on the orphan's pipe never returns
				// on its own; end the orphan so the call can, then fail.
				_ = releaseEnd.Close()
				<-done
				t.Fatal("launch-check that answered did not return while an orphan held its output pipe")
			}
		})
	}
}
