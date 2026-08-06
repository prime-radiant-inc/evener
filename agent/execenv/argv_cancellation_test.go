//go:build !windows

package execenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestExecArgvCancellationTerminatesEntireProcessGroupNotJustLeader is the
// coverage a roborev review asked for after flagging that Argv previously
// built its *exec.Cmd via exec.CommandContext. CommandContext installs its
// own ctx-triggered kill (a single-process Process.Kill, no grace period),
// which races execPreparedCommand's own process-group SIGTERM->SIGKILL
// escalation for the very same ctx.Done() event: if CommandContext's
// internal kill closes the leader's Wait() first, execPreparedCommand's
// select picks the `done` branch instead of `ctx.Done()` and never calls
// Terminate()/Kill() on the group at all — leaving a leader-spawned child
// (a git hook, in production) running with nothing left to reap it. RunGit's
// direct-exec git calls (agent/execenv/rungit.go) go through this exact
// Argv/execPreparedCommand path, so this is the right level to pin the fix.
//
// The leader here is a shell script that backgrounds a heartbeat child (a
// loop appending a byte to heartbeatPath every 20ms via a plain `&` job, so
// it inherits the leader's process group rather than being setsid'd away)
// and then blocks. The test cancels the context once the heartbeat is
// confirmed running, then asserts the heartbeat stops growing — the child
// died alongside the leader because the whole group was signaled, not just
// the leader.
func TestExecArgvCancellationTerminatesEntireProcessGroupNotJustLeader(t *testing.T) {
	if _, err := execLookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	heartbeat := filepath.Join(dir, "heartbeat")
	script := fmt.Sprintf(`(while true; do printf x >> %q; sleep 0.02; done) & sleep 5`, heartbeat)

	env := NewLocalExecutionEnvironment(dir)
	zeroGrace := 50 * time.Millisecond
	env.terminationGrace = &zeroGrace

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = env.ExecArgv(ctx, "sh", []string{"-c", script}, 10_000, dir, nil)
	}()

	// Wait for the heartbeat child to actually start writing before canceling,
	// so a slow-starting child can't produce a false pass.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if info, statErr := os.Stat(heartbeat); statErr == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecArgv did not return after cancellation")
	}

	settledInfo, statErr := os.Stat(heartbeat)
	if statErr != nil {
		t.Fatalf("stat heartbeat: %v", statErr)
	}
	settled := settledInfo.Size()

	// A dead process gets no further chance to write; a live one gets every
	// chance to prove it's still alive.
	time.Sleep(300 * time.Millisecond)
	afterInfo, statErr := os.Stat(heartbeat)
	if statErr != nil {
		t.Fatalf("stat heartbeat after grace: %v", statErr)
	}
	if afterInfo.Size() > settled {
		t.Fatalf("heartbeat child kept writing after cancellation (size %d -> %d): the process group was not fully terminated", settled, afterInfo.Size())
	}
}
