//go:build linux || darwin

package execenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The two tests below pin issue #5468: a background child that redirects its
// own output holds no inherited output pipe, so the pipe cannot be the
// teardown trigger, and a child that ignores SIGTERM cannot be ended by the
// group SIGTERM a stop sends first. Before the group reap at the streamed
// command's end, such a child survived both the job's stop and the job's
// natural completion — reparented to pid 1, still in the job's process group,
// holding whatever resources it had (an orphaned `go run` server held its
// listening port for hours).
//
// Not parallel: each test signals and reaps a real process group.

// redirectingHeartbeatScript backgrounds a child that appends a byte to
// heartbeat every 20ms. The child redirects its own stdout and stderr (so it
// holds no inherited output pipe) and ignores SIGTERM (so only a group SIGKILL
// can end it): if it is alive after the job ended, no teardown reached it.
func redirectingHeartbeatScript(heartbeat string) string {
	return fmt.Sprintf(
		"(trap '' TERM; while true; do printf x >> %q; sleep 0.02; done) >/dev/null 2>&1 &",
		heartbeat)
}

func awaitHeartbeat(t *testing.T, heartbeat string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if info, statErr := os.Stat(heartbeat); statErr == nil && info.Size() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("redirecting background child never started writing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertHeartbeatStopped fails the test if the heartbeat child is still
// writing: a dead process gets no further chance to write, a live one gets
// every chance to prove it is still alive.
func assertHeartbeatStopped(t *testing.T, heartbeat string) {
	t.Helper()
	settledInfo, statErr := os.Stat(heartbeat)
	if statErr != nil {
		t.Fatalf("stat heartbeat: %v", statErr)
	}
	settled := settledInfo.Size()
	time.Sleep(300 * time.Millisecond)
	afterInfo, statErr := os.Stat(heartbeat)
	if statErr != nil {
		t.Fatalf("stat heartbeat after settle: %v", statErr)
	}
	if afterInfo.Size() > settled {
		t.Fatalf("redirecting background child kept writing after the job ended (size %d -> %d): the process group was not reaped", settled, afterInfo.Size())
	}
}

func reapStreamTestEnv(t *testing.T, dir string) *LocalExecutionEnvironment {
	t.Helper()
	env := NewLocalExecutionEnvironment(dir)
	zeroGrace := 50 * time.Millisecond
	env.terminationGrace = &zeroGrace
	return env
}

// TestStreamCommandStopReapsRedirectingBackgroundChild stops a streamed job
// whose background child redirects its own output and ignores SIGTERM, then
// asserts the child does not survive the job's end. The stop signals the
// group with SIGTERM, which the leader dies from at once: before the fix the
// SIGKILL escalation was abandoned the moment the leader was reaped, the
// closed output pipe signaled nothing, and the child kept running.
func TestStreamCommandStopReapsRedirectingBackgroundChild(t *testing.T) {
	if _, err := execLookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	heartbeat := filepath.Join(dir, "heartbeat")
	script := redirectingHeartbeatScript(heartbeat) + " sleep 30"

	env := reapStreamTestEnv(t, dir)
	handle, err := env.StreamCommand(context.Background(), script, dir, nil, io.Discard)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-handle.Pid, syscall.SIGKILL) })

	awaitHeartbeat(t, heartbeat)

	handle.Signal()

	waitDone := make(chan struct{})
	go func() {
		_, _ = handle.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Signal")
	}

	assertHeartbeatStopped(t, heartbeat)
}

// TestStreamCommandWaitReapsRedirectingBackgroundChildAfterLeaderExit lets
// the job's leader exit on its own — the leader gates its exit on the child's
// first heartbeat byte, so the child is provably running when Wait returns —
// and asserts the child does not survive the job's end. Before the fix the
// closed output pipe signaled nothing on this path either, and the child kept
// running with the job completed.
func TestStreamCommandWaitReapsRedirectingBackgroundChildAfterLeaderExit(t *testing.T) {
	if _, err := execLookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	heartbeat := filepath.Join(dir, "heartbeat")
	script := redirectingHeartbeatScript(heartbeat) +
		fmt.Sprintf(" while [ ! -s %q ]; do sleep 0.01; done; exit 0", heartbeat)

	env := reapStreamTestEnv(t, dir)
	handle, err := env.StreamCommand(context.Background(), script, dir, nil, io.Discard)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-handle.Pid, syscall.SIGKILL) })

	waitDone := make(chan struct{})
	go func() {
		_, _ = handle.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the leader exited")
	}

	assertHeartbeatStopped(t, heartbeat)
}
