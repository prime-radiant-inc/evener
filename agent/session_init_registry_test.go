package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestChildRegistryKeepsDelegateWithAllowance verifies seam 3 (spec §1): the
// registry strip at child init is gated on delegationAllowance, not depth.
//
// Positive case: a child with delegationAllowance > 0 retains delegate and
// job_watch in its registry (today fails: strip runs on depth > 0).
//
// Negative case: a child with delegationAllowance == 0 still has delegate and
// job_watch stripped (preserves today's leaf behaviour).
func TestChildRegistryKeepsDelegateWithAllowance(t *testing.T) {
	t.Parallel()
	t.Run("allowance>0 retains delegate and job_watch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		cfg := SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
		}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 1
		cfg.testOnly.metaFS = afero.NewMemMapFs()

		child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer child.Close()

		for _, toolName := range []string{"delegate", "job_watch"} {
			if child.reg.Get(toolName) == nil {
				t.Errorf("child with delegationAllowance=1: registry is missing %q (strip should not have run)", toolName)
			}
		}
	})

	t.Run("allowance==0 strips delegate but keeps job_watch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		cfg := SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
		}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 0 // leaf child — delegation strip must still run
		cfg.testOnly.metaFS = afero.NewMemMapFs()

		child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer child.Close()

		if child.reg.Get("delegate") != nil {
			t.Error("child with delegationAllowance=0: registry still contains delegate (strip should have run)")
		}
		if child.reg.Get("job_watch") == nil {
			t.Error("child with delegationAllowance=0: registry is missing job_watch — a session that can run jobs must be able to watch its own jobs")
		}
	})
}

// TestNewSessionCanonicalizesRelativeStateDir pins the path contract of a
// relative --state-dir: attachment paths recorded into transcripts name an
// absolute location, and the model's file tools resolve paths against their
// own working directory, so the session must anchor a relative state dir to
// the process working directory once, at construction — writes and announced
// paths then resolve identically no matter who reads them back. No
// t.Parallel: chdir is process-global.
func TestNewSessionCanonicalizesRelativeStateDir(t *testing.T) {
	work := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: "state"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	want := filepath.Join(work, "state")
	if sess.stateDir != want {
		t.Fatalf("session stateDir=%q, want %q (a relative StateDir must resolve against the process working directory at construction)", sess.stateDir, want)
	}
}

// TestNewSessionResolvesSymlinkedStateDirToPhysicalPath: a state dir reached
// through a symlink keeps that component in an Abs-only resolution, and the
// sandbox's file tools refuse symlinked ancestors on some hosts (macOS's
// /tmp, /var), which would turn an announced attachment path into a
// deterministic refusal. The anchored state dir must be the physical path.
func TestNewSessionResolvesSymlinkedStateDirToPhysicalPath(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	real := filepath.Join(work, "real-state")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(work, "link-state")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: link})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if sess.stateDir != real {
		t.Fatalf("session stateDir=%q, want the physical path %q (a symlinked StateDir must resolve at construction)", sess.stateDir, real)
	}
}

// TestRestoreSessionCanonicalizesRelativeStateDir pins the restore-side half
// of the same contract: `evener serve --resume` threads a --state-dir
// through RestoreSessionFromMetaWithConfig, and a restored session must carry
// the same absolute anchor the original session recorded, so state paths
// written before the restart and reads after it agree. The setup uses an
// absolute state dir, so only the restore path exercises the relative form.
// No t.Parallel: chdir is process-global.
func TestRestoreSessionCanonicalizesRelativeStateDir(t *testing.T) {
	work := t.TempDir()
	stateDir := filepath.Join(work, "state")
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(work), SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := sess.ID()
	sess.Close()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	meta, err := schema.LoadSessionMeta("state", id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(work), meta, RestoreSessionConfig{StateDir: "state"})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.stateDir != stateDir {
		t.Fatalf("restored stateDir=%q, want %q (a relative restore StateDir must resolve against the process working directory at construction)", restored.stateDir, stateDir)
	}
}

// TestLeafDelegateWatchesItsOwnJobsOnly: a session that can run jobs can watch
// them, at any depth — job_watch is not delegation, and stripping it left a
// leaf delegate unable to wait on its own background work. What stays closed
// is everything the root-only strip was actually protecting, which each watch
// SOURCE enforces itself: `parent` requires delegate(watch_parent=true), and a
// concrete job id must be owned by the watching session.
func TestLeafDelegateWatchesItsOwnJobsOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		NoProjectPrompts: true,
		StateDir:         dir,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 0 // leaf: no watch_parent grant
	cfg.testOnly.metaFS = afero.NewMemMapFs()

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer child.Close()

	shellRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if shellRes.IsError {
		t.Fatalf("leaf shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil || shellOut.JobID == "" {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = child.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, child.jobManager, shellOut.JobID)
	})

	ownRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "watch-own",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"` + shellOut.JobID + `","progress_interval_ms":120000}`),
	})
	if ownRes.IsError {
		t.Fatalf("leaf delegate must be able to watch its own job, got error: %s", ownRes.Output)
	}

	parentRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "watch-parent",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["communicate"]}`),
	})
	if !parentRes.IsError {
		t.Fatalf("leaf delegate without watch_parent must not watch its parent, got: %s", parentRes.Output)
	}
}
