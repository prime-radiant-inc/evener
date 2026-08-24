//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func TestLinuxEnforcedSandboxTrustedResourcesReachEnvironmentPrompt(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := sandbox.RealProber{}.Probe()
	if !facts.BwrapCapable {
		t.Skip("real enforced Linux sandbox is unavailable")
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: new(true)}, facts, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	if err := env.EnableSandbox(&rp); err != nil {
		t.Skipf("real enforced Linux sandbox is unavailable: %v", err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	local, ok := sess.env.(*execenv.LocalExecutionEnvironment)
	if !ok || local.Sandbox == nil || !local.Sandbox.Enforced() || local.Wrapper == nil {
		t.Fatalf("session must retain an enforced sandbox: %#v", sess.env)
	}

	// The model-facing shell must not be able to inspect the cgroup files that
	// the trusted launch snapshot reads. This is the security boundary under test.
	result, err := local.ExecCommand(context.Background(), `if [ -e /sys/fs/cgroup/cpu.max ] || [ -e /sys/fs/cgroup/memory.max ] || [ -e /sys/fs/cgroup/cpu/cpu.cfs_quota_us ] || [ -e /sys/fs/cgroup/memory/memory.limit_in_bytes ]; then printf visible; fi`, 5000, worktree, nil)
	if err != nil {
		t.Fatalf("model-facing /sys probe: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		t.Fatalf("model-facing shell unexpectedly reached masked cgroup files: %q", result.Stdout)
	}

	resources := sess.envInfo.Resources
	if resources == nil || resources.CPUs <= 0 || resources.MemoryMB <= 0 {
		t.Skip("host does not expose finite CPU and memory cgroup caps")
	}
	prompt, warning := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	for _, want := range []string{
		fmt.Sprintf("CPUs: %v", resources.CPUs),
		fmt.Sprintf("Memory: %d MB", resources.MemoryMB),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("assembled environment prompt missing %q:\n%s", want, prompt)
		}
	}
}
