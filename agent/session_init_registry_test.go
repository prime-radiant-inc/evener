package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
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
	t.Run("allowance>0 retains delegate and job_watch", func(t *testing.T) {
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

	t.Run("allowance==0 strips delegate and job_watch", func(t *testing.T) {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		cfg := SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
		}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 0 // leaf child — strip must still run

		child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer child.Close()

		for _, toolName := range []string{"delegate", "job_watch"} {
			if child.reg.Get(toolName) != nil {
				t.Errorf("child with delegationAllowance=0: registry still contains %q (strip should have run)", toolName)
			}
		}
	})
}
