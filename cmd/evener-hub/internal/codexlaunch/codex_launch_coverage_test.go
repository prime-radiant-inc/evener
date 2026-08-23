package codexlaunch

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCloseOnceReadCloser(t *testing.T) {
	closer := &closeOnceReadCloser{ReadCloser: io.NopCloser(strings.NewReader("data"))}
	if err := closer.Close(); err != nil {
		t.Fatalf("first Close should not error: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("second Close should not error: %v", err)
	}
}

func TestStopTimerAlreadyFired(t *testing.T) {
	timer := time.NewTimer(1 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	// The timer has already fired — Stop returns false, and we need to drain C
	stopTimer(timer)
}

func TestStopTimerNotFired(t *testing.T) {
	timer := time.NewTimer(1 * time.Second)
	stopTimer(timer)
}

func TestLaunchedCodexExitedNotExited(t *testing.T) {
	exited := make(chan struct{})
	launched := &LaunchedCodex{Exited: exited}
	if launchedCodexExited(launched) {
		t.Fatal("should return false when Exited channel is open")
	}
}

func TestLaunchedCodexExitedAlreadyExited(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	launched := &LaunchedCodex{Exited: exited}
	if !launchedCodexExited(launched) {
		t.Fatal("should return true when Exited channel is closed")
	}
}

func TestCodexReadyWaitErrorCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := codexReadyWaitError(ctx)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled context should produce canceled error, got %v", err)
	}
}

func TestCodexReadyWaitErrorTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Nanosecond)
	err := codexReadyWaitError(ctx)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timed-out context should produce timeout error, got %v", err)
	}
}

func TestBuildCodexLaunchArgsDefaultAppServer(t *testing.T) {
	args := buildCodexLaunchArgs("codex", nil, "ws://127.0.0.1:0")
	// Should contain "app-server" since no configured args and binary is not "codex-app-server"
	found := false
	for _, a := range args {
		if a == "app-server" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'app-server' in args for bare codex binary, got %v", args)
	}
	if !argsContainFlag(args, "--listen") {
		t.Fatal("expected --listen flag in args")
	}
}

func TestBuildCodexLaunchArgsCodexAppServerBinary(t *testing.T) {
	args := buildCodexLaunchArgs("codex-app-server", nil, "ws://127.0.0.1:0")
	// Should NOT contain "app-server" since binary is already "codex-app-server"
	for _, a := range args {
		if a == "app-server" {
			t.Fatal("should not add 'app-server' when binary already contains 'codex-app-server'")
		}
	}
}

func TestBuildCodexLaunchArgsConfiguredOverrides(t *testing.T) {
	args := buildCodexLaunchArgs("codex", []string{"--foo", "bar"}, "ws://127.0.0.1:0")
	if args[0] != "--foo" || args[1] != "bar" {
		t.Fatalf("configured args should come first, got %v", args)
	}
	if !argsContainFlag(args, "--listen") {
		t.Fatal("expected --listen flag in args")
	}
}

func TestBuildCodexLaunchArgsAlreadyHasListen(t *testing.T) {
	args := buildCodexLaunchArgs("codex", []string{"--listen", "ws://custom:1234"}, "ws://127.0.0.1:0")
	// Should not add a second --listen
	count := 0
	for _, a := range args {
		if a == "--listen" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 --listen flag, got %d in %v", count, args)
	}
}

func TestArgsContainFlag(t *testing.T) {
	if !argsContainFlag([]string{"--listen", "ws://x"}, "--listen") {
		t.Fatal("exact match should return true")
	}
	if !argsContainFlag([]string{"--listen=ws://x"}, "--listen") {
		t.Fatal("prefix match should return true")
	}
	if argsContainFlag([]string{"--foo", "bar"}, "--listen") {
		t.Fatal("missing flag should return false")
	}
	if argsContainFlag(nil, "--listen") {
		t.Fatal("nil args should return false")
	}
}

func TestCodexLaunchEnvIncludesSpawnedFlag(t *testing.T) {
	env := codexLaunchEnv(nil)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "EVENER_HUB_SPAWNED_CODEX=") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected EVENER_HUB_SPAWNED_CODEX in env")
	}
}

func TestCodexLaunchEnvOverrides(t *testing.T) {
	env := codexLaunchEnv(map[string]string{"CUSTOM_VAR": "custom_value"})
	found := false
	for _, e := range env {
		if e == "CUSTOM_VAR=custom_value" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected CUSTOM_VAR=custom_value in env")
	}
}

func TestCodexLogPrefixEmpty(t *testing.T) {
	if got := codexLogPrefix(""); got != "[codex]" {
		t.Fatalf("empty ID should produce '[codex]', got %q", got)
	}
	if got := codexLogPrefix("  "); got != "[codex]" {
		t.Fatalf("whitespace ID should produce '[codex]', got %q", got)
	}
}

func TestCodexLogPrefixWithID(t *testing.T) {
	if got := codexLogPrefix("myid"); got != "[codex:myid]" {
		t.Fatalf("expected '[codex:myid]', got %q", got)
	}
}

func TestDropCodexLineEndNoNewline(t *testing.T) {
	result := dropCodexLineEnd([]byte("no newline"))
	if !bytes.Equal(result, []byte("no newline")) {
		t.Fatalf("line without newline should be unchanged, got %q", result)
	}
}

func TestDropCodexLineEndNewlineOnly(t *testing.T) {
	result := dropCodexLineEnd([]byte("line\n"))
	if !bytes.Equal(result, []byte("line")) {
		t.Fatalf("line with \\n should have \\n stripped, got %q", result)
	}
}

func TestDropCodexLineEndCRLF(t *testing.T) {
	result := dropCodexLineEnd([]byte("line\r\n"))
	if !bytes.Equal(result, []byte("line")) {
		t.Fatalf("line with \\r\\n should have both stripped, got %q", result)
	}
}

func TestExecLaunchProcessKillNoProcess(t *testing.T) {
	cmd := exec.Command("echo", "test")
	p := &execLaunchProcess{cmd: cmd}
	// Process is nil before Start, so Kill should be a no-op
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill with nil Process should not error, got %v", err)
	}
}
