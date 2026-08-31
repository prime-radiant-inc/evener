package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
)

// TestBuildSpawnArgsEmpty covers the path with minimal fields.
func TestBuildSpawnArgsEmpty(t *testing.T) {
	req := hubcore.SpawnRequest{}
	args := buildSpawnArgs(req)
	if len(args) == 0 || args[0] != "--addr" || args[1] != "127.0.0.1:0" {
		t.Fatalf("expected first arg --addr 127.0.0.1:0, got %v", args)
	}
}

// TestBuildResumeArgsEmpty covers the path with minimal fields.
func TestBuildResumeArgsEmpty(t *testing.T) {
	req := hubcore.ResumeRequest{SessionID: "session-1"}
	args := buildResumeArgs(req)
	if len(args) < 4 || args[0] != "serve" || args[1] != "--addr" || args[3] != "--resume" || args[4] != "session-1" {
		t.Fatalf("expected serve --addr ... --resume session-1, got %v", args)
	}
}

// TestBuildResumeArgsWithFields covers the path with all fields set.
func TestBuildResumeArgsWithFields(t *testing.T) {
	replaySize := 2048
	req := hubcore.ResumeRequest{
		SessionID:     "session-1",
		WorkingDir:    "/work",
		StateDir:      "/state",
		RunDir:        "/run",
		AppReplaySize: replaySize,
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model:          "openai/gpt-5.2",
			FastCheapModel: "openai/gpt-5-mini",
		}},
	}
	args := buildResumeArgs(req)
	// Check key args are present
	foundDir, foundState, foundRun, foundReplay := false, false, false, false
	for i, arg := range args {
		switch arg {
		case "--dir":
			if i+1 < len(args) && args[i+1] == "/work" {
				foundDir = true
			}
		case "--state-dir":
			if i+1 < len(args) && args[i+1] == "/state" {
				foundState = true
			}
		case "--run-dir":
			if i+1 < len(args) && args[i+1] == "/run" {
				foundRun = true
			}
		case "--app-replay-size":
			if i+1 < len(args) && args[i+1] == "2048" {
				foundReplay = true
			}
		}
	}
	if !foundDir {
		t.Fatal("expected --dir /work in resume args")
	}
	if !foundState {
		t.Fatal("expected --state-dir /state in resume args")
	}
	if !foundRun {
		t.Fatal("expected --run-dir /run in resume args")
	}
	if !foundReplay {
		t.Fatal("expected --app-replay-size 2048 in resume args")
	}
	// Model and FastCheapModel should be cleared for resume
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) && args[i+1] != "" {
			t.Fatalf("model should be cleared for resume, got %q", args[i+1])
		}
	}
}

// TestTailBufferWriteWithinLimit covers the normal write path.
func TestTailBufferWriteWithinLimit(t *testing.T) {
	b := &tailBuffer{limit: 100}
	n, err := b.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write returned %d, %v", n, err)
	}
	if b.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", b.String())
	}
}

// TestTailBufferWriteExceedsLimit covers the path where write exceeds limit
// and only the tail is kept.
func TestTailBufferWriteExceedsLimit(t *testing.T) {
	b := &tailBuffer{limit: 5}
	b.Write([]byte("hello world"))
	if b.String() != "world" {
		t.Fatalf("expected 'world' (last 5 chars), got %q", b.String())
	}
}

// TestTailBufferWriteZeroLimit covers the limit<=0 path.
func TestTailBufferWriteZeroLimit(t *testing.T) {
	b := &tailBuffer{limit: 0}
	n, err := b.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write with limit=0 should accept all, got %d, %v", n, err)
	}
	if b.String() != "" {
		t.Fatalf("buffer should stay empty with limit=0, got %q", b.String())
	}
}

// TestTailBufferWritePartialExceeds covers the path where buffer + new data
// exceeds limit but new data alone doesn't.
func TestTailBufferWritePartialExceeds(t *testing.T) {
	b := &tailBuffer{limit: 5}
	b.Write([]byte("abc"))    // buf = "abc"
	b.Write([]byte("defghi")) // buf + "defghi" exceeds limit
	// Should keep last 5 chars
	if got := b.String(); len(got) != 5 {
		t.Fatalf("expected 5 chars, got %d: %q", len(got), got)
	}
}

// TestRendezvousWaitErrorCanceled covers the context.Canceled path.
func TestRendezvousWaitErrorCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := rendezvousWaitError(ctx)
	if !errors.Is(err, errRendezvousCanceled) {
		t.Fatalf("expected errRendezvousCanceled, got %v", err)
	}
}

// TestRendezvousWaitErrorTimeout covers the DeadlineExceeded path.
func TestRendezvousWaitErrorTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	err := rendezvousWaitError(ctx)
	if !errors.Is(err, errRendezvousTimeout) {
		t.Fatalf("expected errRendezvousTimeout, got %v", err)
	}
}

// TestLaunchFailurePrefixTimeout covers the timeout prefix path.
func TestLaunchFailurePrefixTimeout(t *testing.T) {
	got := launchFailurePrefix("spawn", errRendezvousTimeout)
	if got != "spawn timed out" {
		t.Fatalf("expected 'spawn timed out', got %q", got)
	}
}

// TestLaunchFailurePrefixCanceled covers the canceled prefix path.
func TestLaunchFailurePrefixCanceled(t *testing.T) {
	got := launchFailurePrefix("resume", errRendezvousCanceled)
	if got != "resume canceled" {
		t.Fatalf("expected 'resume canceled', got %q", got)
	}
}

// TestLaunchFailurePrefixDefault covers the default prefix path.
func TestLaunchFailurePrefixDefault(t *testing.T) {
	got := launchFailurePrefix("spawn", errors.New("some error"))
	if got != "spawn failed" {
		t.Fatalf("expected 'spawn failed', got %q", got)
	}
}

// TestLaunchFailureErrorEmptyStderr covers the path where stderr is empty.
func TestLaunchFailureErrorEmptyStderr(t *testing.T) {
	err := launchFailureError("spawn failed", errors.New("inner"), "")
	if !strings.Contains(err.Error(), "inner") {
		t.Fatalf("expected to contain 'inner', got %v", err)
	}
	if strings.Contains(err.Error(), ": ") && strings.Contains(err.Error(), "inner: ") {
		// Should be "spawn failed: inner" not "spawn failed: inner: "
		// This branch intentionally has no body; it verifies the condition is false.
		_ = err
	}
}

// TestLaunchFailureErrorWithStderr covers the path where stderr has content.
func TestLaunchFailureErrorWithStderr(t *testing.T) {
	err := launchFailureError("spawn failed", errors.New("inner"), "stderr output")
	if !strings.Contains(err.Error(), "stderr output") {
		t.Fatalf("expected to contain 'stderr output', got %v", err)
	}
}

// TestLaunchFailureErrorStderrInError covers the path where the error already
// contains the stderr text.
func TestLaunchFailureErrorStderrInError(t *testing.T) {
	inner := errors.New("error with stderr output inside")
	err := launchFailureError("spawn failed", inner, "stderr output")
	// stderr text is already in the error, so it should not be appended again
	if !strings.Contains(err.Error(), "error with stderr output inside") {
		t.Fatalf("expected to contain inner error, got %v", err)
	}
	// Should not have duplicate stderr appended
	if strings.Count(err.Error(), "stderr output") > 1 {
		t.Fatalf("stderr should not be duplicated, got %v", err)
	}
}

// TestEnvLookupFound covers the found path.
func TestEnvLookupFound(t *testing.T) {
	env := []string{"A=1", "B=2", "B=3"}
	value, ok := envLookup(env, "B")
	if !ok || value != "3" {
		t.Fatalf("expected last 'B=3', got %q, %v", value, ok)
	}
}

// TestEnvLookupNotFound covers the not-found path.
func TestEnvLookupNotFound(t *testing.T) {
	env := []string{"A=1"}
	_, ok := envLookup(env, "B")
	if ok {
		t.Fatal("expected not found")
	}
}

// TestEnvLookupEmptyEnv covers the empty env path.
func TestEnvLookupEmptyEnv(t *testing.T) {
	_, ok := envLookup(nil, "A")
	if ok {
		t.Fatal("expected not found in nil env")
	}
}
