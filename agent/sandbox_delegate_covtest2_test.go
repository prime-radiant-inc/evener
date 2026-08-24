package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// faultSession creates a Session whose currentEnv is a local env and whose
// subagentPrepareFault hook returns an error for the named fault point.
func faultSession(t *testing.T, faultPoint string) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	dir := t.TempDir()
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			subagentPrepareFault: func(point string) error {
				if point == faultPoint {
					return errors.New("fault: " + point)
				}
				return nil
			},
		},
	}))
	// Ensure env is a local env.
	env := execenv.NewLocalExecutionEnvironment(dir)
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	return s
}

// TestPrepareSubagentEnvironment_WorkingDirEnvFault covers the fault path at
// line 26-28 where subagentPrepareFault("working_dir_env") returns an error.
func TestPrepareSubagentEnvironment_WorkingDirEnvFault(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "working_dir_env")
	_, _, err := s.prepareSubagentEnvironment("/some/dir", nil)
	if err == nil || !strings.Contains(err.Error(), "does not support working_dir") {
		t.Fatalf("expected working_dir_env fault error, got %v", err)
	}
}

// TestPrepareSubagentEnvironment_SandboxRerootFault covers the fault path at
// line 31-35 where subagentPrepareFault("sandbox_reroot") returns an error.
func TestPrepareSubagentEnvironment_SandboxRerootFault(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "sandbox_reroot")
	_, _, err := s.prepareSubagentEnvironment(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot confine") {
		t.Fatalf("expected sandbox_reroot fault error, got %v", err)
	}
}

// TestPrepareSubagentEnvironment_SandboxEnvFault covers the fault path at
// line 41-43 where subagentPrepareFault("sandbox_env") returns an error
// when a sandbox policy is requested.
func TestPrepareSubagentEnvironment_SandboxEnvFault(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "sandbox_env")
	pol := &sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}
	_, _, err := s.prepareSubagentEnvironment("", pol)
	if err == nil || !strings.Contains(err.Error(), "does not support a per-delegate sandbox") {
		t.Fatalf("expected sandbox_env fault error, got %v", err)
	}
}

// TestPrepareSubagentEnvironment_SandboxResolveFault covers the fault path at
// line 49-51 where subagentPrepareFault("sandbox_resolve") returns an error.
func TestPrepareSubagentEnvironment_SandboxResolveFault(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "sandbox_resolve")
	pol := &sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}
	_, _, err := s.prepareSubagentEnvironment("", pol)
	if err == nil || !strings.Contains(err.Error(), "per-delegate sandbox") {
		t.Fatalf("expected sandbox_resolve fault error, got %v", err)
	}
}

// TestPrepareSubagentEnvironment_SandboxEnableFault covers the fault path at
// line 60-62 where subagentPrepareFault("sandbox_enable") returns an error.
func TestPrepareSubagentEnvironment_SandboxEnableFault(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "sandbox_enable")
	pol := &sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}
	_, _, err := s.prepareSubagentEnvironment("", pol)
	if err == nil || !strings.Contains(err.Error(), "per-delegate sandbox") {
		t.Fatalf("expected sandbox_enable fault error, got %v", err)
	}
}

// TestPrepareSubagentEnvironment_NoWorkingDirNoSandbox covers the nil path
// where neither workingDir nor a sandbox policy is provided — the function
// should return the current env with ownsFresh=false.
func TestPrepareSubagentEnvironment_NoWorkingDirNoSandbox(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "")
	env, ownsFresh, err := s.prepareSubagentEnvironment("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if ownsFresh {
		t.Fatal("expected ownsFresh=false")
	}
}

// TestPrepareSubagentEnvironment_WorkingDirSuccess covers the success path
// where workingDir is set and reroot succeeds.
func TestPrepareSubagentEnvironment_WorkingDirSuccess(t *testing.T) {
	t.Parallel()
	s := faultSession(t, "")
	dir := t.TempDir()
	env, ownsFresh, err := s.prepareSubagentEnvironment(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if !ownsFresh {
		t.Fatal("expected ownsFresh=true")
	}
}
