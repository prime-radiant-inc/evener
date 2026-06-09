package execenv

import (
	"bytes"
	"context"
	"errors"
	"sync"
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
	if got != "stdout\nstderr\n" {
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
