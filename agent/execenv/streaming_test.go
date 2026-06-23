package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var errStreamWrite = errors.New("stream write failed")

// Compile-time assertion that the local env implements the optional interface.
func TestLocalEnvImplementsStreamingExecutor(t *testing.T) {
	var _ StreamingExecutor = (*LocalExecutionEnvironment)(nil)
}

func TestStreamCommandCapturesOutputAndExit(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var buf bytes.Buffer
	h, err := env.StreamCommand(context.Background(), "printf 'stdout\n'; printf 'stderr\n' >&2", "", nil, &lockedWriter{&mu, &buf})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !pidTracked(env, h.Pid) {
		t.Fatalf("pid %d was not tracked before Wait", h.Pid)
	}
	code, err := h.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if pidTracked(env, h.Pid) {
		t.Fatalf("pid %d still tracked after Wait", h.Pid)
	}
	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if !strings.Contains(got, "stdout\n") || !strings.Contains(got, "stderr\n") {
		t.Errorf("streamed output = %q", got)
	}
}

func TestStreamCommandSignalStops(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	_ = env.Initialize()
	h, err := env.StreamCommand(context.Background(), "sleep 30", "", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	done := make(chan int, 1)
	go func() { c, _ := h.Wait(); done <- c }()
	h.Signal()
	h.Signal()
	h.Signal()
	select {
	case <-done: // process group killed; Wait returned
	case <-time.After(5 * time.Second):
		t.Fatal("Signal did not stop the process within 5s")
	}
}

// TestStreamCommandSignalKillsWholeProcessGroup pins that Signal terminates
// every process-group member by actual liveness (kill(pid,0) → ESRCH), not
// just by Wait returning: both the exec'd-replacement shape from the runaway
// e2e card (sh -c '...; exec sleep N') and a forked background child. A live
// run on 2026-06-12 suspected an orphaned exec'd sleep after run_timeout; the
// evidence was a pgrep -f self-match, but nothing pinned real group death.
func TestStreamCommandSignalKillsWholeProcessGroup(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var buf bytes.Buffer
	h, err := env.StreamCommand(context.Background(),
		"sh -c 'sleep 300 & echo CHILD=$!; echo READY; exec sleep 300'",
		"", nil, &lockedWriter{&mu, &buf})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		mu.Lock()
		out := buf.String()
		mu.Unlock()
		if strings.Contains(out, "READY") {
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(line, "CHILD="); ok {
					childPID, _ = strconv.Atoi(strings.TrimSpace(rest))
				}
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatalf("never parsed forked child pid; output: %q", buf.String())
	}

	// READY prints before the exec, so signaling now could kill the shell
	// pre-exec and never exercise the exec'd-replacement case. Wait until the
	// handle's pid has actually become the sleep before signaling.
	execDeadline := time.Now().Add(5 * time.Second)
	for {
		cmdline, err := processCommandName(h.Pid)
		if err == nil && strings.HasPrefix(cmdline, "sleep") {
			break
		}
		if time.Now().After(execDeadline) {
			t.Fatalf("pid %d never became the exec'd sleep; cmdline=%q err=%v", h.Pid, cmdline, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.Signal()

	waitDone := make(chan struct{})
	go func() { _, _ = h.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Signal")
	}

	gone := func(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
	deadline = time.Now().Add(4 * time.Second) // SIGTERM immediate, SIGKILL escalation at 2s
	for time.Now().Before(deadline) {
		if gone(h.Pid) && gone(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process group member survived Signal: exec'd pid %d gone=%v, forked child pid %d gone=%v",
		h.Pid, gone(h.Pid), childPID, gone(childPID))
}

func processCommandName(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		return strings.TrimRight(string(data), "\x00"), nil
	}
	out, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if psErr != nil {
		return "", fmt.Errorf("%v; ps: %w", err, psErr)
	}
	return strings.TrimSpace(string(out)), nil
}

func TestStreamCommandSignalAfterWaitNoops(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	h, err := env.StreamCommand(context.Background(), "true", "", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	code, err := h.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	called := make(chan struct{})
	go func() {
		h.Signal()
		h.Signal()
		close(called)
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("Signal after Wait hung")
	}
}

func TestStreamCommandWaitReturnsStreamErrors(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	h, err := env.StreamCommand(context.Background(), "printf 'output\n'", "", nil, failingWriter{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	code, err := h.Wait()
	if !errors.Is(err, errStreamWrite) {
		t.Fatalf("wait error = %v, want %v", err, errStreamWrite)
	}
	if code != 127 {
		t.Errorf("exit code = %d, want 127", code)
	}
	if pidTracked(env, h.Pid) {
		t.Fatalf("pid %d still tracked after Wait", h.Pid)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func pidTracked(env *LocalExecutionEnvironment, pid int) bool {
	_, ok := env.runningPIDs.Load(pid)
	return ok
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errStreamWrite
}
