package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
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
