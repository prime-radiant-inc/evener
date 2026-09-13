package dev

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// TestRunAgentShardsBadCount covers the envPositiveInt error path for
// AGENT_SHARD_COUNT in runAgentShards.
func TestRunAgentShardsBadCount(t *testing.T) {
	t.Setenv("AGENT_SHARD_COUNT", "not-a-number")
	// Capture stderr by replacing os.Stderr.
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := runAgentShards(nil)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if code != 1 {
		t.Fatalf("runAgentShards with bad count = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "must be a positive integer") {
		t.Fatalf("stderr = %q, want 'must be a positive integer'", buf.String())
	}
}

// TestRunAgentShardsBadParallel covers the envPositiveInt error path for
// AGENT_SHARD_PARALLEL in runAgentShards.
func TestRunAgentShardsBadParallel(t *testing.T) {
	t.Setenv("AGENT_SHARD_COUNT", "2")
	t.Setenv("AGENT_SHARD_PARALLEL", "0")
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := runAgentShards(nil)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if code != 1 {
		t.Fatalf("runAgentShards with bad parallel = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "must be a positive integer") {
		t.Fatalf("stderr = %q, want 'must be a positive integer'", buf.String())
	}
}

// TestRunAgentShardsBadSurveyParallel covers the envPositiveInt error path for
// AGENT_SHARD_SURVEY_PARALLEL in runAgentShards.
func TestRunAgentShardsBadSurveyParallel(t *testing.T) {
	t.Setenv("AGENT_SHARD_COUNT", "2")
	t.Setenv("AGENT_SHARD_PARALLEL", "1")
	t.Setenv("AGENT_SHARD_SURVEY_PARALLEL", "-2")
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := runAgentShards(nil)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if code != 1 {
		t.Fatalf("runAgentShards with bad survey parallel = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "must be a positive integer") {
		t.Fatalf("stderr = %q, want 'must be a positive integer'", buf.String())
	}
}

// TestSurveyArgsCarryParallelismAndSkip pins the survey's argument list: the
// parallelism the caller asks for must reach the test binary, and the skip and
// short passthroughs must stay attached.
func TestSurveyArgsCarryParallelismAndSkip(t *testing.T) {
	got := surveyArgs(2, "Flaky", true)
	want := []string{"-test.count=1", "-test.parallel", "2", "-test.run", "^(Test|Example)", "-test.v", "-test.skip", "Flaky", "-test.short"}
	if !slices.Equal(got, want) {
		t.Fatalf("surveyArgs(2, Flaky, true) = %q, want %q", got, want)
	}
	got = surveyArgs(6, "", false)
	want = []string{"-test.count=1", "-test.parallel", "6", "-test.run", "^(Test|Example)", "-test.v"}
	if !slices.Equal(got, want) {
		t.Fatalf("surveyArgs(6, \"\", false) = %q, want %q", got, want)
	}
	// A config built without runAgentShards leaves the field zero.
	got = surveyArgs(0, "", false)
	want = []string{"-test.count=1", "-test.parallel", "6", "-test.run", "^(Test|Example)", "-test.v"}
	if !slices.Equal(got, want) {
		t.Fatalf("surveyArgs(0, \"\", false) = %q, want %q", got, want)
	}
}

// TestRunAgentShardsNoAgentDir covers the path where runAgentShards reaches
// runShards but the agent dir doesn't exist (returns 2).
func TestRunAgentShardsNoAgentDir(t *testing.T) {
	t.Setenv("AGENT_SHARD_COUNT", "1")
	t.Setenv("AGENT_SHARD_PARALLEL", "1")
	// Change to a temp dir so "agent" doesn't exist.
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	oldStdout, oldStderr := os.Stdout, os.Stderr
	_, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()
	code := runAgentShards(nil)
	wOut.Close()
	wErr.Close()
	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(rErr)
	if code != 2 {
		t.Fatalf("runAgentShards with no agent dir = %d, want 2; stderr=%s", code, stderrBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "no agent dir") {
		t.Fatalf("stderr = %q, want 'no agent dir'", stderrBuf.String())
	}
}

// TestInterrupterInterruptWithPgids covers the loop in interrupt that calls
// procgroup.Terminate for each pgid. We can't easily test with real process
// groups, but we can verify the interrupt function sets the signal and
// iterates over pgids (even if Terminate fails on invalid pgids).
func TestInterrupterInterruptWithPgids(t *testing.T) {
	in := &interrupter{}
	// Add a fake pgid. The Terminate call on an invalid pgid may fail silently.
	in.add(-1) // -1 is not a valid pgid, but add will append it since no signal yet
	in.interrupt(syscall.SIGTERM)
	if in.signal != syscall.SIGTERM {
		t.Fatalf("signal = %v, want SIGTERM", in.signal)
	}
	if code := in.exitCode(); code != 143 {
		t.Fatalf("exitCode = %d, want 143", code)
	}
}

// TestRunShardsScratchError covers the scratch.Acquire error path in runShards.
func TestRunShardsScratchError(t *testing.T) {
	// Set TMPDIR to a file (not a directory) so scratch.Acquire fails.
	conflict := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", conflict)
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: fixtureModule(t),
		count:    1,
		parallel: 1,
		stdout:   &stdout,
		stderr:   &stderr,
	}
	rc := runShards(cfg)
	if rc != 2 {
		t.Fatalf("runShards with bad TMPDIR = %d, want 2; stderr=%s", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not create a scratch directory") {
		t.Fatalf("stderr = %q, want scratch error", stderr.String())
	}
}
