package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// TestEnableSandboxUsesConfiguredTmpBase keeps the deterministic lifecycle seam
// instance-local. A nil/empty base remains the production default; a test can
// constrain scratch creation to its own fixture and verify Cleanup owns it.
func TestEnableSandboxUsesConfiguredTmpBase(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "worktree")
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, sandbox.HostFacts{
		OS: "linux", Home: home, BwrapCapable: true, BwrapPath: "/fixture/bwrap",
	}, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	base := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	env.sandboxTmpBase = base
	if err := env.EnableSandbox(&policy); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	t.Cleanup(env.Cleanup)
	if env.ownedSessionTmp == nil {
		t.Fatal("EnableSandbox did not retain its owned scratch directory")
	}
	rel, err := filepath.Rel(base, env.ownedSessionTmp.Dir)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("sandbox scratch %q escaped configured base %q (rel=%q err=%v)", env.ownedSessionTmp.Dir, base, rel, err)
	}
}
