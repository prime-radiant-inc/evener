//go:build linux || darwin

package procgroup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForFile polls for a fixture file the child writes once it is ready,
// so tests synchronize on real child state instead of sleeps.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child never wrote %s", path)
	return ""
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestStartPlacesChildInOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30") //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		Kill(cmd.Process.Pid)
		_ = cmd.Wait()
	}()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Errorf("child pgid = %d, want its own pid %d", pgid, cmd.Process.Pid)
	}
	if own, _ := syscall.Getpgid(os.Getpid()); pgid == own {
		t.Error("child shares the test's process group; a group signal would hit the test itself")
	}
}

func TestStopTerminatesWholeGroup(t *testing.T) {
	// The child forks a grandchild and publishes its pid; Stop must take
	// out both, or an interrupted lint run leaks linter descendants.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! >"+pidFile+"; wait") //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	grandchild, err := strconv.Atoi(waitForFile(t, pidFile))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	Stop(cmd.Process.Pid, reaped, 5*time.Second)
	<-reaped
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(grandchild) {
		Kill(cmd.Process.Pid)
		t.Fatalf("grandchild %d survived Stop", grandchild)
	}
}

func TestStopEscalatesToKillWhenTermIgnored(t *testing.T) {
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready >`+readyFile+`; sleep 30`) //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, readyFile)
	reaped := make(chan struct{})
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		close(reaped)
	}()
	start := time.Now()
	Stop(cmd.Process.Pid, reaped, 200*time.Millisecond)
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("TERM-ignoring child was never killed")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("Stop returned in %v, before the TERM grace elapsed", elapsed)
	}
	err := <-waitErr
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("TERM-ignoring child died with %v (state %v), want SIGKILL", err, cmd.ProcessState)
	}
}

func TestStopReturnsWithoutKillWhenChildHonorsTerm(t *testing.T) {
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	cmd := exec.Command("sh", "-c", `trap "exit 143" TERM; echo ready >`+readyFile+`; while :; do sleep 1; done`) //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, readyFile)
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	// The grace is a tripwire, not a mechanism: a child that honors TERM
	// must release Stop long before it, or interrupted runs would always
	// stall for the full escalation window.
	start := time.Now()
	Stop(cmd.Process.Pid, reaped, 30*time.Second)
	<-reaped
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Errorf("Stop took %v against a cooperative child; it waited out the grace instead of the reap", elapsed)
	}
	if code := cmd.ProcessState.ExitCode(); code != 143 {
		t.Errorf("cooperative child exited %d, want 143", code)
	}
}

func TestExitCodeMapsSignalDeathsLikeAShell(t *testing.T) {
	cmd := exec.Command("sleep", "30") //nolint:noctx // the test kills it directly
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	Kill(cmd.Process.Pid)
	_ = cmd.Wait()
	if got := ExitCode(cmd.ProcessState); got != 128+int(syscall.SIGKILL) {
		t.Errorf("ExitCode after SIGKILL = %d, want %d", got, 128+int(syscall.SIGKILL))
	}

	ok := exec.Command("true") //nolint:noctx // exits on its own
	if err := Start(ok); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = ok.Wait()
	if got := ExitCode(ok.ProcessState); got != 0 {
		t.Errorf("ExitCode after clean exit = %d, want 0", got)
	}
}
