package execenv

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

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
	h, err := env.StreamCommand(context.Background(), "printf 'a\nb\n'", "", nil, &lockedWriter{&mu, &buf})
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
	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if got != "a\nb\n" {
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
	select {
	case <-done: // process group killed; Wait returned
	case <-time.After(5 * time.Second):
		t.Fatal("Signal did not stop the process within 5s")
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
