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

// childReadyTripwire bounds the readiness poll below. Reaching a fixture's
// first write costs a process creation: 440 samples taken on a host at load
// average ~130 all landed under 65ms, but the two creations the sibling git
// fixture needed have been measured at up to 10s on that same class of host —
// which is exactly where the bare 10s ceiling this replaced would have fired,
// on a fixture that was never in trouble. The bound is a hang guard, never the
// mechanism: the poll's real exits are the file appearing and the child's own
// reap.
const childReadyTripwire = 90 * time.Second

// childWait reaps a started child exactly once and publishes the result, so a
// readiness poll can watch the child's own exit while the test keeps ownership
// of the reap signal Stop needs.
type childWait struct {
	done chan struct{}
	err  error // valid once done is closed
}

func reapChild(cmd *exec.Cmd) *childWait {
	w := &childWait{done: make(chan struct{})}
	go func() {
		w.err = cmd.Wait()
		close(w.done)
	}()
	return w
}

// waitForFile polls for a fixture file the child writes once it is ready,
// so tests synchronize on real child state instead of sleeps. It watches the
// child's own reap alongside the file: a fixture that dies before writing —
// a bad script, a failed exec, a sandbox refusal — names that exit instead of
// spending the whole tripwire and then blaming a file that was never coming.
func waitForFile(t *testing.T, path string, child *childWait) string {
	t.Helper()
	deadline := time.Now().Add(childReadyTripwire)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		select {
		case <-child.done:
			t.Fatalf("child exited before writing %s: %v", path, child.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("child never wrote %s within %v", path, childReadyTripwire)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	child := reapChild(cmd)
	grandchild, err := strconv.Atoi(waitForFile(t, pidFile, child))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	Stop(cmd.Process.Pid, child.done, 5*time.Second)
	<-child.done
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
	child := reapChild(cmd)
	waitForFile(t, readyFile, child)
	start := time.Now()
	Stop(cmd.Process.Pid, child.done, 200*time.Millisecond)
	// TRIPWIRE: the KILL is already sent by the time Stop returns, so the reap
	// is one signal delivery away; this only fires if it never lands at all.
	select {
	case <-child.done:
	case <-time.After(10 * time.Second):
		t.Fatal("TERM-ignoring child was never killed")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("Stop returned in %v, before the TERM grace elapsed", elapsed)
	}
	err := child.err
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
	child := reapChild(cmd)
	waitForFile(t, readyFile, child)
	// The grace is a tripwire, not a mechanism: a child that honors TERM
	// must release Stop long before it, or interrupted runs would always
	// stall for the full escalation window.
	start := time.Now()
	Stop(cmd.Process.Pid, child.done, 30*time.Second)
	<-child.done
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Errorf("Stop took %v against a cooperative child; it waited out the grace instead of the reap", elapsed)
	}
	if code := cmd.ProcessState.ExitCode(); code != 143 {
		t.Errorf("cooperative child exited %d, want 143", code)
	}
}

func TestStopNeverSignalsAnAlreadyReapedChild(t *testing.T) {
	// After a child is reaped its pid can be recycled by an unrelated
	// process group; a Stop that signals first and checks later aims TERM
	// at whoever owns the number now. The decoy here is a live group WE
	// own, standing in for that stranger: with the reaped channel already
	// closed, Stop must send it nothing. (The shell runner was immune by
	// construction — single-threaded blank-after-wait; the Go guard's
	// remaining check-vs-reap window is microseconds and needs an
	// immediate pid wraparound.)
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	marker := filepath.Join(dir, "signalled")
	decoy := exec.Command("sh", "-c", `trap ": >`+marker+`; exit 143" TERM; echo ready >`+readyFile+`; while :; do sleep 1; done`) //nolint:noctx // stopped explicitly at the end of the test
	if err := Start(decoy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	decoyChild := reapChild(decoy)
	waitForFile(t, readyFile, decoyChild)
	alreadyReaped := make(chan struct{})
	close(alreadyReaped)
	Stop(decoy.Process.Pid, alreadyReaped, 50*time.Millisecond)
	// A signal in flight would hit the trap well within this window; the
	// sleep bounds how long a wrongly-sent TERM has to land, not a
	// mechanism the pass depends on.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("Stop signalled a child it was told is already reaped (marker stat err %v)", err)
	}
	if !pidAlive(decoy.Process.Pid) {
		t.Error("decoy died during a Stop that should have sent nothing")
	}
	Stop(decoy.Process.Pid, decoyChild.done, 5*time.Second)
	<-decoyChild.done
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
