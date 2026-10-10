//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunCheckKillsWhatAHungCheckStarted: the kill at a check's deadline
// reaches every process the check started, so a hung go test binary cannot
// keep running beside the runs that follow.
func TestRunCheckKillsWhatAHungCheckStarted(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	// The deadline must land after the shell has written child.pid; under a
	// loaded full-suite run, 200ms was sometimes too short for that.
	if ok, _ := runCheck(work, checkSpec{Name: "hang", Run: "sleep 30 & echo $! > child.pid; wait"}, 2*time.Second); ok {
		t.Fatal("a hung check passed")
	}
	data, err := os.ReadFile(filepath.Join(work, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the check's child %d is still running after the check timed out", pid)
		}
	}
}
