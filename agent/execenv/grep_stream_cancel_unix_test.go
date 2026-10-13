//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"testing"
	"time"
)

// /dev/ptmx is a device named as the search target: admitted, since naming
// a device is a deliberate request, and its read blocks until the slave side
// writes — an idle pty master. The capped read must not outlive its context:
// cancellation has to interrupt the blocked read, where the admitted
// descriptor with O_NONBLOCK cleared sat in read(2) with no deadline and no
// way for the byte cap to act (#4574 round 3, HIGH). Before the fix this
// test's grep goroutine never returns on its own; the watchdog records the
// failure in bounded time and the blocked goroutine is left to the runner.
func TestGrepCancelsABlockedAdmittedStream(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.Grep(ctx, "needle", "/dev/ptmx", "", false, 100, "content")
		done <- err
	}()
	// No awaitable marks the moment the read blocks, so cancellation rides a
	// tripwire window instead. Either ordering is correct once the read
	// honors the context: cancelled before the open, the search answers at
	// the open; cancelled while blocked, the deadline watcher wakes it.
	select {
	case err := <-done:
		t.Fatalf("the pty-master search returned before cancellation: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()

	// The read must answer the cancellation promptly, not when a slave
	// writes. The ceiling is a tripwire: the awaitable is the done channel,
	// and only a blocked read can miss it.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled blocked stream read = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the blocked admitted-stream read did not return within 10s of cancellation")
	}
}
