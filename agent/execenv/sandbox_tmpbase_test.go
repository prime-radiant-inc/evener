package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
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
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.sandboxTmpBase = base
	if err := env.EnableSandbox(&policy); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if env.ownedSessionTmp == nil {
		t.Fatal("EnableSandbox did not retain its owned scratch directory")
	}
	rel, err := filepath.Rel(canonicalBase, env.ownedSessionTmp.Dir)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("sandbox scratch %q escaped configured base %q (rel=%q err=%v)", env.ownedSessionTmp.Dir, base, rel, err)
	}
}

// TestSessionScratchBaseReportsTheBaseAllocationChose: a requested base inside
// the session's workspace is refused and the scratch lands in the user cache
// dir, so the base the env reports is the one it allocated in — the one a hub
// must search to find the session's directory. Reporting the requested one
// would send the hub to a tree that never exists.
func TestSessionScratchBaseReportsTheBaseAllocationChose(t *testing.T) {
	// Not parallel: redirectUserCacheDirForTest points this process's user
	// cache dir — the fallback base, on every platform — at a test directory
	// for the duration of the test.
	redirectUserCacheDirForTest(t)
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
	env := NewLocalExecutionEnvironment(worktree)
	refused := filepath.Join(worktree, "scratchbase")
	if err := os.MkdirAll(refused, 0o755); err != nil {
		t.Fatal(err)
	}
	env.sandboxTmpBase = refused
	env.SetScratchIdentity("ROOTY", "ROOTY")
	if err := env.EnableSandbox(&policy); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	// Disposal removes exactly the env's own scratch directory. The cleanup
	// must never remove the scratch's PARENT: a contended named scratch falls
	// back to a disposable directory minted directly under the cache base, and
	// a parent removal would delete that whole base.
	t.Cleanup(env.DisposeSandboxScratch)
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("no scratch provisioned")
	}
	base := env.SessionScratchBase()
	if base == "" {
		t.Fatal("no scratch base reported")
	}
	if rel, err := filepath.Rel(base, scratch); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("reported base %q does not contain the scratch %q (rel=%q err=%v)", base, scratch, rel, err)
	}
	if canonical, err := filepath.EvalSymlinks(refused); err == nil && base == canonical {
		t.Fatalf("the scratch allocated in the refused in-workspace base %q", base)
	}
}
