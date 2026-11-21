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

	"primeradiant.com/evener/internal/testwait"
)

// TestRunCheckKillsWhatAHungCheckStarted: the kill at a check's deadline
// reaches every process the check started, so a hung go test binary cannot
// keep running beside the runs that follow.
func TestRunCheckKillsWhatAHungCheckStarted(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	// The deadline must land after the shell has written child.pid; under a
	// loaded full-suite run, 200ms was sometimes too short for that.
	// The sleeper must outlive testwait's ceiling: if the check's kill misses
	// it, the wait must fail rather than see the sleeper exit on its own.
	if ok, _ := runCheck(work, checkSpec{Name: "hang", Run: "sleep 300 & echo $! > child.pid; wait"}, 2*time.Second); ok {
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
	// Don't leave the sleeper behind if the wait fails.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	testwait.Until(t, "the check's child "+strconv.Itoa(pid)+" to die", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}
