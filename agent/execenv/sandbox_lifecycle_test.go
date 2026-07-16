package execenv

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/sandbox"
)

// TestEnableSandboxProvisionsSeatbeltBackend: EnableSandbox provisions the kernel
// wrapper for whichever backend resolved — bubblewrap on Linux, sandbox-exec
// (Seatbelt) on macOS — so a darwin-resolved Seatbelt policy attaches a real
// (non-nil) wrapper rather than failing closed. The darwin-resolved policy is
// built purely (no Mac needed) so the wiring is exercised on the Linux test host;
// the wrapper's actual sandbox-exec enforcement is validated live on paradise-park.
func TestEnableSandboxProvisionsSeatbeltBackend(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	darwin := sandbox.HostFacts{OS: "darwin", Home: home, SandboxExecPath: "/usr/bin/sandbox-exec"}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, darwin, worktree)
	if err != nil {
		t.Fatalf("Resolve(darwin): %v", err)
	}
	if rp.Backend != sandbox.BackendSeatbelt {
		t.Fatalf("expected a seatbelt backend, got %v", rp.Backend)
	}
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	if err := env.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox on a seatbelt backend must provision, not refuse: %v", err)
	}
	if env.Sandbox == nil || env.Wrapper == nil {
		t.Errorf("a provisioned seatbelt env must have both Sandbox and Wrapper set, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}
}

// TestWithWorkingDirectoryReRootDivergentFailureNilsBoth: when the Sandbox re-root
// succeeds but the Wrapper re-root fails (or vice versa), the child must NOT be
// left half-confined — BOTH Sandbox and Wrapper are nil'd and the sticky error is
// recorded. Uses a valid Resolve-produced Sandbox (re-roots fine) alongside a
// wrapper built from an inputs-less literal (cannot re-root), so the two re-roots
// diverge instead of failing identically.
func TestWithWorkingDirectoryReRootDivergentFailureNilsBoth(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	env := NewLocalExecutionEnvironment(laneA)
	env.Sandbox = resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite) // re-roots fine
	w, err := sandbox.NewWrapper(
		sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap},
		"/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env.Wrapper = w // inputs-less literal → cannot re-root

	child := env.WithWorkingDirectory(laneB)
	if child.SandboxReRootError() == nil {
		t.Fatal("a divergent re-root must record a sticky SandboxReRootError()")
	}
	if child.Sandbox != nil {
		t.Errorf("a half-failed re-root must nil the Sandbox too (no half-confined child), got %+v", child.Sandbox)
	}
	if child.Wrapper != nil {
		t.Errorf("a failed wrapper re-root must leave Wrapper nil, got %+v", child.Wrapper)
	}
}

// TestEnableSandboxErrorLeavesUnsandboxed: when wrapper construction fails,
// EnableSandbox must leave the env unsandboxed (nil Sandbox/Wrapper) rather than
// keeping a prior policy, and must dispose the tmp a prior call owned so a second
// call never leaks the first's dir.
func TestEnableSandboxErrorLeavesUnsandboxed(t *testing.T) {
	laneA, _, home := twoLanes(t)
	env := NewLocalExecutionEnvironment(laneA)

	// A prior, valid sandbox state that the failing call must tear down.
	priorRP := resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(priorRP); err != nil {
		t.Fatalf("prior EnableSandbox: %v", err)
	}
	priorTmp := env.Wrapper.SessionTmp()
	if _, err := os.Stat(priorTmp); err != nil {
		t.Fatalf("prior session tmp must exist: %v", err)
	}

	// A host that is bwrap-capable but has NO resolved bwrap path → Backend is bwrap
	// yet HostBwrapPath() is "", so NewWrapper fails on the non-absolute path.
	net := true
	badFacts := sandbox.HostFacts{OS: "linux", Home: home, BwrapCapable: true, BwrapPath: ""}
	badRP, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net}, badFacts, laneA)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if err := env.EnableSandbox(&badRP); err == nil {
		t.Fatal("EnableSandbox with an unbuildable wrapper must return an error")
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Errorf("a failed EnableSandbox must leave the env unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}
	if _, err := os.Stat(priorTmp); !os.IsNotExist(err) {
		t.Errorf("a failed EnableSandbox must dispose the prior owned tmp, stat err = %v", err)
	}
}

// TestPolicyReplaceRebuildsSandboxFS: replacing the sandbox policy (EnableSandbox
// or UseControlPolicy) must invalidate the lazily-built, fd-anchored file-tool
// layer so the next file tool rebuilds it against the NEW policy — a stale sbfs
// would keep enforcing the OLD roots.
func TestPolicyReplaceRebuildsSandboxFS(t *testing.T) {
	t.Run("EnableSandbox", func(t *testing.T) {
		laneA, laneB, home := twoLanes(t)
		env := NewLocalExecutionEnvironment(laneA)
		env.Sandbox = resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)
		first := env.sandbox()
		if first == nil {
			t.Fatal("an enforced policy must build a sandboxFS")
		}
		rp2 := resolvedAt(t, home, laneB, sandbox.ModeWorkspaceWrite)
		if err := env.EnableSandbox(rp2); err != nil {
			t.Fatalf("EnableSandbox: %v", err)
		}
		t.Cleanup(env.Cleanup)
		second := env.sandbox()
		if second == nil || second == first {
			t.Errorf("EnableSandbox must rebuild the fd layer, got rebuilt=%v", second != nil && second != first)
		}
		if second != nil && second.policy != rp2 {
			t.Error("rebuilt sandboxFS must reflect the new policy")
		}
	})

	t.Run("UseControlPolicy", func(t *testing.T) {
		base := t.TempDir()
		home := t.TempDir()
		main := filepath.Join(base, "repo")
		runGit(t, base, "init", "-q", "repo")
		runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
		main = evalSym(t, main)

		env := NewLocalExecutionEnvironment(main)
		env.Sandbox = resolvedAt(t, home, main, sandbox.ModeWorkspaceWrite)
		first := env.sandbox()
		if first == nil {
			t.Fatal("an enforced policy must build a sandboxFS")
		}
		if err := env.UseControlPolicy(main); err != nil {
			t.Fatalf("UseControlPolicy: %v", err)
		}
		second := env.sandbox()
		if second == nil || second == first {
			t.Errorf("UseControlPolicy must rebuild the fd layer, got rebuilt=%v", second != nil && second != first)
		}
		if second != nil && second.policy != env.Sandbox {
			t.Error("rebuilt sandboxFS must reflect the control policy")
		}
	})
}

// TestCleanupDisposesOwnedTmpAfterChildGrace: a tracked child's TMPDIR/caches point
// into the owned session tmp (via ApplyEnvFloor), so Cleanup must dispose that tmp
// only AFTER the SIGTERM + grace + SIGKILL sequence — not before, which would delete
// the dir out from under a gracefully shutting-down child. The child records, on
// SIGTERM, whether the watched dir still exists; the sentinel (written outside the
// tmp, so it survives disposal) proves the tmp was alive when the child was signaled.
func TestCleanupDisposesOwnedTmpAfterChildGrace(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	dir := t.TempDir()
	tmp, err := sandbox.NewSessionScratch("", dir)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	env := NewLocalExecutionEnvironment(dir)
	env.ownedSessionTmp = tmp
	sentinel := filepath.Join(dir, "tmp-was-present")
	ready := filepath.Join(dir, "trap-installed")

	// The child touches READY only AFTER the TERM trap is installed, so the test can
	// wait for readiness before signaling — otherwise Cleanup could SIGTERM the child
	// before its trap exists (the default action would terminate it, masking the
	// ordering under test).
	script := `trap 'if [ -d "$WATCH" ]; then : > "$SENTINEL"; fi; exit 0' TERM
: > "$READY"
sleep 300 & wait`
	h, err := env.StreamCommand(context.Background(), script, dir,
		map[string]string{"WATCH": tmp.Dir, "SENTINEL": sentinel, "READY": ready}, io.Discard)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never signaled trap readiness")
		}
		time.Sleep(2 * time.Millisecond)
	}

	env.Cleanup()
	_, _ = h.Wait()

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("Cleanup disposed the owned tmp before signaling the child (TMPDIR pulled out from under graceful shutdown): sentinel missing, stat err = %v", err)
	}
	if _, err := os.Stat(tmp.Dir); !os.IsNotExist(err) {
		t.Errorf("Cleanup must still dispose the owned tmp after the grace, stat err = %v", err)
	}
}
