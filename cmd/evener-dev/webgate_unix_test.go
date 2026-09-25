//go:build linux || darwin

package dev

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// awaitGone waits (tripwire-bounded) until pid no longer exists. A zombie still
// answers kill -0, so this waits for the reap by whoever inherited it.
func awaitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(tripwire)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still running", what, pid)
		}
		time.Sleep(10 * time.Millisecond) // TRIPWIRE-bounded wait for the kernel's reap
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(tripwire)
	for {
		data, err := os.ReadFile(path)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && convErr == nil {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", path)
		}
		time.Sleep(10 * time.Millisecond) // TRIPWIRE-bounded wait for the child's own pid file
	}
}

// startGroupedTree starts a real process tree through the launcher and makes
// sure none of it outlives the test, whatever the test concluded: the cleanup
// KILLs the leader, its group, and every pid the tree records in pidFile.
func startGroupedTree(t *testing.T, script, pidFile string, grace time.Duration) guardProcess {
	t.Helper()
	logFile, err := os.Create(filepath.Join(t.TempDir(), "guard.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close() //nolint:errcheck // the started process holds its own descriptor
	proc, err := execGuardLauncher{drainGrace: grace}.Start(guardSpec{argv: []string{"sh", "-c", script, "sh", pidFile}, dir: ".", group: true}, logFile)
	if err != nil {
		t.Fatal(err)
	}
	leader := proc.(*execGuard).cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = syscall.Kill(leader, syscall.SIGKILL)
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return proc
}

// waitBounded is Wait under the tripwire, so a guard that never exits fails
// the test instead of hanging it.
func waitBounded(t *testing.T, proc guardProcess) int {
	t.Helper()
	status := make(chan int, 1)
	go func() { status <- proc.Wait() }()
	select {
	case s := <-status:
		return s
	case <-time.After(tripwire):
		t.Fatal("the guard never exited")
		return 0
	}
}

// A grouped check's TERM reaches the whole tree at once. The leader here
// survives TERM and keeps waiting for its child, as a wrapper can: a TERM to
// the leader alone would then never reach the child, and the check would never
// finish. (A handler, not an ignored TERM, which the child would inherit.)
// The grandchild must get the TERM itself, to shut down cleanly.
func TestGroupedGuardTerminateReachesGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	proc := startGroupedTree(t, `trap : TERM; sh -c 'trap "touch \"\$1.term\"; exit 0" TERM; echo $$ > "$1"; while :; do sleep 0.05; done' sh "$1" & child=$!; while kill -0 $child 2>/dev/null; do wait $child; done`, pidFile, 200*time.Millisecond)
	grandchild := readPid(t, pidFile)
	proc.Terminate()
	waitBounded(t, proc)
	awaitGone(t, grandchild, "the grandchild of a terminated check")
	if _, err := os.Stat(pidFile + ".term"); err != nil {
		t.Fatalf("the grandchild never got the TERM: %v", err)
	}
}

// A grouped check's leftovers are stopped when its leader exits, so a check
// that exits and leaves a child behind does not outlive the gate.
func TestGroupedGuardLeavesNoStragglerAfterExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "straggler.pid")
	proc := startGroupedTree(t, `sh -c 'trap "" TERM; echo $$ > "$1"; while :; do sleep 0.05; done' sh "$1" & while [ ! -s "$1" ]; do sleep 0.01; done; exit 0`, pidFile, 200*time.Millisecond)
	if status := waitBounded(t, proc); status != 0 {
		t.Fatalf("status = %d", status)
	}
	awaitGone(t, readPid(t, pidFile), "a check's straggler after the check exited")
}

// npm's shape on an interrupt: the leader dies on TERM at once while its child
// is still winding down. The drain waits for the child rather than killing it
// the moment the leader is reaped.
func TestGroupedGuardDrainWaitsForAWindingDownChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	proc := startGroupedTree(t, `sh -c 'trap "sleep 0.3; touch \"\$1.term\"; exit 0" TERM; echo $$ > "$1"; while :; do sleep 0.05; done' sh "$1" & wait`, pidFile, 2*time.Second)
	child := readPid(t, pidFile)
	proc.Terminate()
	waitBounded(t, proc)
	awaitGone(t, child, "the winding-down child")
	if _, err := os.Stat(pidFile + ".term"); err != nil {
		t.Fatalf("the child was killed before it finished winding down: %v", err)
	}
}

// A leftover that would stop on TERM gets one when its leader exits, rather
// than only the KILL after the grace.
func TestGroupedGuardDrainTermsALeftoverFirst(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "leftover.pid")
	proc := startGroupedTree(t, `sh -c 'trap "touch \"\$1.term\"; exit 0" TERM; echo $$ > "$1"; while :; do sleep 0.05; done' sh "$1" & while [ ! -s "$1" ]; do sleep 0.01; done; exit 0`, pidFile, 2*time.Second)
	if status := waitBounded(t, proc); status != 0 {
		t.Fatalf("status = %d", status)
	}
	awaitGone(t, readPid(t, pidFile), "the leftover")
	if _, err := os.Stat(pidFile + ".term"); err != nil {
		t.Fatalf("the leftover never got a TERM: %v", err)
	}
}
