//go:build browserguard && linux

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Real Node processes exercise the same lifecycle that owns Chrome, without
// needing a second hub. The child is detached, just as Driver.start's Chrome is.
// Linux subreaping is only a failure-cleanup backstop: the assertion requires
// the Node owner itself to have reaped its child and removed its profile.
func TestRetryContinuationDriverCancellation(t *testing.T) {
	// Not parallel: the red-test cleanup temporarily adopts orphaned children.
	var previous int32
	if err := unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&previous)), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, uintptr(previous), 0, 0, 0); err != nil {
			t.Error(err)
		}
	})
	var ready retryContinuationDriverReady
	// Registered before the owner's cleanup, so Wait has joined that owner
	// before this backstop attempts to reap an orphan from a broken Cancel.
	t.Cleanup(func() {
		if ready.PID == 0 {
			return
		}
		var status unix.WaitStatus
		pid, err := unix.Wait4(ready.PID, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) || pid == ready.PID {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		if err := unix.Kill(-ready.PID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			t.Error(err)
		}
		if _, err := unix.Wait4(ready.PID, &status, 0, nil); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const script = `
import {spawn} from 'node:child_process';
import {mkdtempSync} from 'node:fs';
import {join} from 'node:path';
import {createBrowserProcessCleanup} from './frontend/scripts/browserGuardProcess.mjs';
const profileDir = mkdtempSync(join(process.argv[1], 'profile-'));
const lifecycle = createBrowserProcessCleanup({profileDir});
const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {
  detached: true, stdio: 'ignore',
});
lifecycle.addChild(child, {processGroupId: child.pid});
console.log(JSON.stringify({pid: child.pid, profile: profileDir}));
`
	wait := startRetryContinuationCancellationDriver(ctx, t, &ready, script, t.TempDir())
	if ready.PID <= 0 || ready.Profile == "" {
		t.Fatalf("invalid child readiness: %+v", ready)
	}
	if _, err := os.Stat(ready.Profile); err != nil {
		t.Fatal(err)
	}
	cancel()
	err := wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Errorf("canceled driver error = %v, want lifecycle SIGTERM exit 143", err)
	}
	if err := unix.Kill(ready.PID, 0); !errors.Is(err, unix.ESRCH) {
		t.Errorf("detached child %d was not reaped by its owner: %v", ready.PID, err)
	}
	if _, err := os.Stat(ready.Profile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("profile survived owner cancellation: %s, stat error %v", ready.Profile, err)
	}
}

func TestRetryContinuationDriverFailure(t *testing.T) {
	cmd := retryContinuationDriverCommand(t.Context(), "-e", "process.exit(7)")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("driver failure = %v, want exit 7; output: %s", err, output)
	}
}

func TestRetryContinuationDriverUnresponsive(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var ready retryContinuationDriverReady
	wait := startRetryContinuationCancellationDriver(ctx, t, &ready, `
process.on('SIGTERM', () => {});
console.log('{}');
setInterval(() => {}, 1000);
`)
	cancel()
	var exit *exec.ExitError
	err := wait()
	if !errors.As(err, &exit) {
		t.Fatalf("unresponsive driver error = %v, want a forced-stop error", err)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("unresponsive driver status = %v, want SIGKILL", exit.Sys())
	}
}

type retryContinuationDriverReady struct {
	PID     int    `json:"pid"`
	Profile string `json:"profile"`
}

func startRetryContinuationCancellationDriver(ctx context.Context, t *testing.T, ready *retryContinuationDriverReady, script string, args ...string) func() error {
	t.Helper()
	cmd := retryContinuationDriverCommand(ctx, append([]string{"--input-type=module", "-e", script}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(finished)
	}()
	wait := func() error {
		select {
		case <-finished:
			return waitErr
		case <-time.After(backgroundDriverJoinReserve):
			t.Fatal("retry driver did not exit within the cancellation join reserve")
			return nil
		}
	}
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		_ = wait()
	})
	type readiness struct {
		value retryContinuationDriverReady
		err   error
	}
	decoded := make(chan readiness, 1)
	go func() {
		var result readiness
		result.err = json.NewDecoder(stdout).Decode(&result.value)
		decoded <- result
	}()
	select {
	case result := <-decoded:
		*ready = result.value
		if result.err != nil {
			_ = wait()
			t.Fatalf("driver readiness: %v; stderr: %s", result.err, stderr.String())
		}
	case <-time.After(backgroundDriverJoinReserve):
		t.Fatal("retry driver did not report readiness")
	}
	return wait
}
