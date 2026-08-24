package dev

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestRunShardsBuildFailurePrintsBuildLog covers the build-failure path in
// runShards where the build fails but no signal was received (line 239-241).
func TestRunShardsBuildFailurePrintsBuildLog(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOWORK", "off")
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}
	// Make the build fail by setting the agent dir to a module with a
	// compile error. We create a fake "agent" dir with a broken go file.
	brokenDir := t.TempDir()
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "go.mod"), []byte("module broken\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "broken_test.go"), []byte("package broken\n\nfunc TestBroken(t *testing.T) {\n\tx := undefinedVar\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.agentDir = brokenDir

	rc := runShards(cfg)
	if rc != 1 {
		t.Fatalf("build failure rc = %d, want 1\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	out := stdout.String()
	if !strings.Contains(out, "agent-shards: build failed") {
		t.Fatalf("build failure stdout missing 'build failed':\n%s", out)
	}
}

// TestCachedSurveyPathWithGOCACHE covers the path where cacheDir is empty and
// the function falls through to `go env GOCACHE`. This covers lines 412-413.
func TestCachedSurveyPathWithGOCACHE(t *testing.T) {
	// Ensure GOCACHE resolves — the test should get a non-empty path.
	cfg := shardsConfig{cacheDir: ""}
	// Set a GOCACHE that actually works
	gocache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Skipf("go env GOCACHE failed: %v", err)
	}
	if len(strings.TrimSpace(string(gocache))) == 0 {
		t.Skip("GOCACHE is empty")
	}
	got := cfg.cachedSurveyPath("TestA\nTestB\n")
	if got == "" {
		t.Fatalf("cachedSurveyPath with default GOCACHE should not be empty")
	}
	if !strings.Contains(got, "evener-agent-shards") {
		t.Fatalf("cachedSurveyPath should contain evener-agent-shards: %q", got)
	}
}

// TestRunToLogCreateError covers the os.Create error path in runToLog.
func TestRunToLogCreateError(t *testing.T) {
	cfg := shardsConfig{}
	in := &interrupter{}
	// Use a path under a file (not a directory) so Create fails.
	conflict := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badLogPath := filepath.Join(conflict, "sub", "log")
	err := cfg.runToLog(in, badLogPath, ".", "true")
	if err == nil {
		t.Fatalf("runToLog with unwritable log path should error")
	}
}

// TestCaptureChildStartError covers the procgroup.Start error path in
// captureChild.
func TestCaptureChildStartError(t *testing.T) {
	cfg := shardsConfig{}
	in := &interrupter{}
	// Use a command that doesn't exist so Start fails.
	_, err := cfg.captureChild(in, ".", "/nonexistent/binary/that/does/not/exist")
	if err == nil {
		t.Fatalf("captureChild with nonexistent binary should error")
	}
}

// TestInterrupterAddTerminatesAfterSignal covers the add-after-signal path
// where procgroup.Terminate is called on the given pgid. We can't easily
// verify the terminate call, but we can verify the pgids list stays empty.
func TestInterrupterAddTerminatesAfterSignalNoPanic(t *testing.T) {
	in := &interrupter{}
	in.interrupt(syscall.SIGTERM)
	// add a pgid that doesn't exist — Terminate should handle it gracefully
	in.add(-1)
	if len(in.pgids) != 0 {
		t.Fatalf("pgids should be empty after add-with-signal, got %v", in.pgids)
	}
	if code := in.exitCode(); code != 143 {
		t.Fatalf("exitCode = %d, want 143", code)
	}
}

// TestRunShardsNoTestsFound covers the path where the survey finds no tests
// to shard (line 299-301).
func TestRunShardsNoTestsFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOWORK", "off")
	// Create a module with no tests.
	emptyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyDir, "go.mod"), []byte("module empty\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: emptyDir,
		count:    2,
		parallel: 1,
		noSurvey: true,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}
	rc := runShards(cfg)
	if rc != 1 {
		t.Fatalf("no tests rc = %d, want 1\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	if !strings.Contains(stderr.String(), "found no tests to shard") {
		t.Fatalf("stderr missing 'found no tests to shard':\n%s", stderr.String())
	}
}

// TestSignalHandlerNonSyscallSignal covers the path where a non-syscall
// signal is received by the signal handler goroutine (line 202-203). We send
// a custom signal type through the channel.
func TestSignalHandlerNonSyscallSignal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOWORK", "off")

	signals := make(chan os.Signal, 2)
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		noSurvey: true,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
		signals:  signals,
	}
	// Send a non-syscall signal before starting the run, so the handler
	// processes it. The run will still proceed but the signal handler will
	// convert it to SIGTERM.
	done := make(chan int, 1)
	go func() {
		done <- runShards(cfg)
	}()
	// Send a non-syscall signal type
	signals <- fakeSignal{}
	// Wait for the run to complete (the signal will cause interruption)
	rc := <-done
	if rc != 143 && rc != 0 {
		t.Fatalf("rc = %d, want 143 (signal processed) or 0 (run completed first)", rc)
	}
	if rc == 143 && !strings.Contains(stderr.String(), "interrupted by SIGTERM") {
		t.Fatalf("rc=143 but stderr missing 'interrupted by SIGTERM': %q", stderr.String())
	}
}

type fakeSignal struct{}

func (fakeSignal) String() string { return "FAKE" }
func (fakeSignal) Signal()        {}
