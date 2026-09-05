package execenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
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
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if err := env.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox on a seatbelt backend must provision, not refuse: %v", err)
	}
	if env.Sandbox == nil || env.Wrapper == nil {
		t.Errorf("a provisioned seatbelt env must have both Sandbox and Wrapper set, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}
}

// TestEnableSandboxFileToolsReachOwnScratch is the kata g8q6 regression: the
// model's own file tools (write_file, read_file, …) must be able to use the SAME
// per-session scratch directory a spawned shell command reaches via
// $TMPDIR/$EVENER_SCRATCH_DIR, in every enforced mode. Before this fix,
// EnableSandbox provisioned the scratch dir for the kernel-wrapped
// spawned-process layer only (env_floor.go, bwrap.go, seatbelt.go all thread it
// through separately) — the in-process file-tool layer's ResolvedPolicy.FileTool
// never carried it, so a model that wrote there (rather than guessing a literal,
// always-denied "/tmp/...") was denied too.
func TestEnableSandboxFileToolsReachOwnScratch(t *testing.T) {
	for _, mode := range []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite, sandbox.ModeRestricted} {
		t.Run(mode.String(), func(t *testing.T) {
			home := t.TempDir()
			worktree := filepath.Join(home, "project")
			if err := os.MkdirAll(worktree, 0o755); err != nil {
				t.Fatal(err)
			}
			host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
			if err != nil {
				t.Fatalf("Resolve(%v): %v", mode, err)
			}
			env := NewLocalExecutionEnvironment(worktree)
			t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
			if err := env.EnableSandbox(&rp); err != nil {
				t.Fatalf("EnableSandbox(%v): %v", mode, err)
			}

			target := filepath.Join(env.Wrapper.SessionTmp(), "scratch.txt")
			if _, err := env.WriteFile(target, "hello scratch\n"); err != nil {
				t.Fatalf("%v: write_file into the session's own scratch dir should succeed: %v", mode, err)
			}
			got, err := env.ReadFile(target, nil, nil)
			if err != nil || !strings.Contains(got, "hello scratch") {
				t.Fatalf("%v: read_file of the just-written scratch file failed: got %q err %v", mode, got, err)
			}
		})
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
// keeping a prior policy, while retaining the tmp a prior call owned for manual
// cleanup.
func TestEnableSandboxErrorLeavesUnsandboxed(t *testing.T) {
	laneA, _, home := twoLanes(t)
	env := NewLocalExecutionEnvironment(laneA)

	// A prior, valid sandbox state that the failing call must tear down.
	priorRP := resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(priorRP); err != nil {
		t.Fatalf("prior EnableSandbox: %v", err)
	}
	priorTmp := env.Wrapper.SessionTmp()
	t.Cleanup(func() { _ = os.RemoveAll(priorTmp) })
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
	if _, err := os.Stat(priorTmp); err != nil {
		t.Errorf("a failed EnableSandbox must retain the prior owned tmp, stat err = %v", err)
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
		t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
		second := env.sandbox()
		if second == nil || second == first {
			t.Errorf("EnableSandbox must rebuild the fd layer, got rebuilt=%v", second != nil && second != first)
		}
		// Not a pointer-identity check against rp2: sandbox() folds the concrete
		// scratch dir into its OWN copy of the policy (WithSessionScratch), so the
		// rebuilt sandboxFS legitimately carries a policy value distinct from rp2 —
		// what must hold is that it reflects lane B (the new policy), not lane A
		// (the stale one).
		if second != nil && second.policy.Git.WorktreeRoot != rp2.Git.WorktreeRoot {
			t.Errorf("rebuilt sandboxFS must reflect the new policy: worktree = %q, want %q", second.policy.Git.WorktreeRoot, rp2.Git.WorktreeRoot)
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

// TestCleanupRetainsOwnedTmpAfterChildGrace: a tracked child's TMPDIR/caches point
// into the owned session tmp (via ApplyEnvFloor), so Cleanup must retain that tmp
// until AFTER the SIGTERM + grace + SIGKILL sequence — not remove it while the
// child is shutting down. The child records, on SIGTERM, whether the watched dir
// still exists; the sentinel proves the tmp was alive when the child was signaled.
func TestCleanupRetainsOwnedTmpAfterChildGrace(t *testing.T) {
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
	if _, err := os.Stat(tmp.Dir); err != nil {
		t.Errorf("Cleanup must retain the owned tmp after the grace, stat err = %v", err)
	}
	if err := env.ownedSessionTmp.Cleanup(); err != nil {
		t.Fatalf("manual scratch cleanup: %v", err)
	}
	if _, err := os.Stat(tmp.Dir); !os.IsNotExist(err) {
		t.Errorf("manual scratch cleanup must remove the owned tmp, stat err = %v", err)
	}
}

// TestEnableSandboxWriteBlockedOffConfinesFileTools is the enforcement half of
// the degraded read-only delegate: on a host with no sandbox backend, a
// write-blocked OFF policy still builds the in-process file-tool layer, so a
// write the delegate attempts through write_file/edit_file is REFUSED with a
// typed denial and never reaches the disk. Reads stay open and the delegate's own
// session scratch stays writable — the whole point of degrading rather than
// refusing the spawn. No kernel wrapper is built (there is no backend), which is
// the residual gap the delegate and its parent are told about.
func TestEnableSandboxWriteBlockedOffConfinesFileTools(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	env := writeBlockedOffEnv(t, home, worktree)
	if env.Wrapper != nil {
		t.Fatalf("a host with no backend must build no kernel wrapper, got %+v", env.Wrapper)
	}
	if env.sandbox() == nil {
		t.Fatal("a write-blocked policy must build the file-tool enforcement layer")
	}

	// A write into the workspace is refused and lands nothing.
	target := filepath.Join(worktree, "deliverable.md")
	if err := os.WriteFile(target, []byte("the parent's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(worktree, "new.txt")
	_, werr := env.WriteFile(newFile, "clobber")
	mustDenied(t, werr, "write_file under a degraded read-only delegate")
	if _, err := os.Stat(newFile); err == nil {
		t.Error("a denied write_file must not create the file")
	}
	_, eerr := env.EditFile(target, "the parent's work", "destroyed", false)
	mustDenied(t, eerr, "edit_file under a degraded read-only delegate")
	if got, _ := os.ReadFile(target); string(got) != "the parent's work\n" {
		t.Errorf("a denied edit_file mutated the file: %q", got)
	}

	// Reads stay open: the delegate keeps the capability it was spawned for.
	got, err := env.ReadFile(target, nil, nil)
	if err != nil || !strings.Contains(got, "the parent's work") {
		t.Fatalf("a degraded read-only delegate must still read: got %q err %v", got, err)
	}

	// The session scratch is the one writable place, and the file tools reach the
	// same directory a shell command gets as $TMPDIR/$EVENER_SCRATCH_DIR.
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("a write-blocked env must provision a session scratch dir to write into")
	}
	note := filepath.Join(scratch, "findings.md")
	if _, err := env.WriteFile(note, "what I found\n"); err != nil {
		t.Fatalf("write_file into the delegate's own scratch dir must succeed: %v", err)
	}
	if back, err := env.ReadFile(note, nil, nil); err != nil || !strings.Contains(back, "what I found") {
		t.Fatalf("read_file of the just-written scratch file failed: got %q err %v", back, err)
	}

	// One scratch dir, both layers: the shell's $TMPDIR/$EVENER_SCRATCH_DIR must be
	// the SAME directory the file tools may write, or the delegate's own prompt
	// would name a path half its tools cannot use.
	shell := envToMap(env.commandEnvironment(nil))
	if shell["EVENER_SCRATCH_DIR"] != scratch || shell["TMPDIR"] != scratch {
		t.Errorf("shell scratch vars = %q/%q, want the file tools' scratch %q",
			shell["EVENER_SCRATCH_DIR"], shell["TMPDIR"], scratch)
	}
}

// TestWriteBlockedOffMasksCredentialPaths: the degraded box's reads are "anywhere
// MINUS the denylist", the same set every confining mode masks. Without the
// denylist a read-only delegate — the one shape that runs with no shell at all,
// so the file tools are its whole surface — would read ~/.ssh and evener's own
// environment out of /proc.
func TestWriteBlockedOffMasksCredentialPaths(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := writeBlockedOffEnv(t, home, worktree)

	_, err := env.ReadFile(secret, nil, nil)
	mustDenied(t, err, "read_file of a credential path under a degraded read-only delegate")
	if _, err := env.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getpid()), nil, nil); err == nil {
		t.Error("read_file of evener's own /proc environ must be denied (it holds the provider API key)")
	}
	// A non-masked path outside the worktree still reads: the mode is anywhere
	// MINUS the denylist, not worktree-only.
	sibling := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(sibling, []byte("ordinary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(sibling, nil, nil); err != nil || !strings.Contains(got, "ordinary") {
		t.Fatalf("an ordinary out-of-worktree read must still work: got %q err %v", got, err)
	}
}

// TestReadsThroughSymlinkedWorkspaceRoot: a workspace reached through a symlinked
// ancestor (a bind-mounted link, a symlinked checkout, macOS /tmp) must stay
// readable. The file-tool layer anchors an in-worktree read at the worktree's own
// fd, which tolerates that ancestor while still refusing a symlink component
// INSIDE the worktree. Both shapes with no write root are covered: the degraded
// box, and the enforced read-only box that has always had the same gap.
//
// The root is deliberately NOT canonicalized here — resolving it in the test is
// what hid this in the first place.
func TestReadsThroughSymlinkedWorkspaceRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, home, worktree string) *LocalExecutionEnvironment
	}{
		{name: "degraded", build: writeBlockedOffEnv},
		{name: "enforced-read-only", build: func(t *testing.T, home, worktree string) *LocalExecutionEnvironment {
			t.Helper()
			env := NewLocalExecutionEnvironment(worktree)
			env.Sandbox = resolvePolicy(t, sandbox.ModeReadOnly, home, worktree)
			t.Cleanup(env.Cleanup)
			return env
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := realTempDir(t)
			target := filepath.Join(home, "real")
			if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			// The session's working directory IS the symlink.
			worktree := filepath.Join(home, "link")
			if err := os.Symlink(target, worktree); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "README.md"), []byte("hello\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "sub", "deep.txt"), []byte("nested\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			env := tc.build(t, home, worktree)

			got, err := env.ReadFile(filepath.Join(worktree, "README.md"), nil, nil)
			if err != nil || !strings.Contains(got, "hello") {
				t.Fatalf("read through a symlinked workspace root: got %q err %v", got, err)
			}
			if got, err := env.ReadFile(filepath.Join(worktree, "sub", "deep.txt"), nil, nil); err != nil || !strings.Contains(got, "nested") {
				t.Fatalf("nested read through a symlinked workspace root: got %q err %v", got, err)
			}
			entries, err := env.ListDirectory(worktree, 1)
			if err != nil || len(entries) == 0 {
				t.Fatalf("list_dir through a symlinked workspace root: %d entries, err %v", len(entries), err)
			}
			matches, err := env.Glob(context.Background(), "*.md", worktree)
			if err != nil || len(matches) == 0 {
				t.Fatalf("glob through a symlinked workspace root: %v err %v", matches, err)
			}

			// The anchor tolerates the ancestor only. A write is still denied, and a
			// symlink INSIDE the worktree is still refused (the same refusal every
			// enforced mode makes).
			if _, err := env.WriteFile(filepath.Join(worktree, "new.txt"), "nope"); err == nil {
				t.Error("the read anchor must not make the workspace writable")
			}
			if err := os.Symlink(filepath.Join(target, "sub"), filepath.Join(target, "inner")); err != nil {
				t.Fatal(err)
			}
			if _, err := env.ReadFile(filepath.Join(worktree, "inner", "deep.txt"), nil, nil); err == nil {
				t.Error("a symlink component inside the worktree must still be refused")
			}
		})
	}
}

// writeBlockedOffEnv builds the degraded read-only delegate's environment through
// the real EnableSandbox path: a resolved write-blocked off policy on a host with
// no sandbox backend.
func writeBlockedOffEnv(t *testing.T, home, worktree string) *LocalExecutionEnvironment {
	t.Helper()
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeOff, WriteBlocked: true},
		sandbox.HostFacts{OS: "linux", Home: home}, worktree)
	if err != nil {
		t.Fatalf("Resolve(write-blocked off): %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if err := env.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox(write-blocked off): %v", err)
	}
	return env
}

// An unsandboxed environment mints its session scratch lazily, on the first
// command environment it builds, and a sandboxed one owns the scratch
// EnableSandbox provisioned; only a session's Cleanup ever releases either. So
// a launch that provisioned an environment and then failed before any session
// adopted it has to drop both, lease and directory together, and has to be
// able to say so more than once — several failure paths can run on the way
// out.
func TestDisposeUnadoptedScratchDropsBothVariantsAndRepeats(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeUnadoptedScratch() })

	// Building the environment for a command is what mints the unsandboxed one.
	_ = env.commandEnvironment(nil)
	unsandboxed := env.SessionScratchDir()
	if unsandboxed == "" {
		t.Fatal("an unsandboxed env minted no session scratch, so there is nothing to dispose")
	}
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	owned := env.SessionScratchDir()
	if owned == "" || owned == unsandboxed {
		t.Fatalf("owned scratch = %q, want one of its own beside the unsandboxed %q", owned, unsandboxed)
	}

	env.DisposeUnadoptedScratch()
	env.DisposeUnadoptedScratch()

	for name, dir := range map[string]string{"unsandboxed": unsandboxed, "owned": owned} {
		// The lease lives inside the directory, so it goes with it.
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s scratch %s survived disposal: stat err = %v", name, dir, err)
		}
	}
	if got := env.SessionScratchDir(); got != "" {
		t.Errorf("SessionScratchDir = %q after disposal, want none", got)
	}
}

// A session swapping environments moves the scratch between two environment
// objects while a child sharing one of them renders its scratch path (the
// session prompt's SessionScratchDir). The sandboxed kind's field has to be
// guarded like the unsandboxed one, or the swap and the render race.
func TestAdoptSessionScratchDoesNotRaceAReaderOfTheSharedEnvironment(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeUnadoptedScratch() })
	// Write-blocked off: an owned scratch with no kernel wrapper, so the reader
	// goes through the owned field rather than the wrapper's copy of the path.
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	clone := env.WithWorkingDirectory(t.TempDir())
	t.Cleanup(clone.DisposeUnadoptedScratch)

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = env.SessionScratchDir()
			}
		}
	}()
	for range 200 {
		clone.AdoptSessionScratch(env)
		env.AdoptSessionScratch(clone)
	}
	close(stop)
	<-readerDone
}

// readConfinedEnvAt builds a file-tool-confined env with no kernel wrapper
// rooted at worktree — the one shape whose effective scratch path comes from
// the fields AdoptSessionScratch moves — whose reads are confined to the
// worktree and its own session scratch, so a probe's read half can fail as
// well as its write half.
func readConfinedEnvAt(t *testing.T, worktree string) *LocalExecutionEnvironment {
	t.Helper()
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(func() { env.Cleanup(); env.DisposeUnadoptedScratch() })
	env.Sandbox = &sandbox.ResolvedPolicy{
		Mode:         sandbox.ModeOff,
		WriteBlocked: true,
		FileTool:     sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}},
	}
	tmp, err := env.newSessionScratch()
	if err != nil {
		t.Fatalf("newSessionScratch: %v", err)
	}
	env.setOwnedSessionTmp(tmp)
	return env
}

// assertFileToolsShareTheShellScratch has a shell command write into the env's
// $EVENER_SCRATCH_DIR and checks the file tools read that file back and can
// write beside it: the two must name the same directory.
func assertFileToolsShareTheShellScratch(t *testing.T, env *LocalExecutionEnvironment) {
	t.Helper()
	if _, err := env.ExecCommand(context.Background(), `printf shell > "$EVENER_SCRATCH_DIR/probe"`, 5000, "", nil); err != nil {
		t.Fatalf("shell write into the scratch: %v", err)
	}
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the env reports no scratch after a shell command")
	}
	if got, err := env.ReadFile(filepath.Join(scratch, "probe"), nil, nil); err != nil || !strings.Contains(got, "shell") {
		t.Errorf("read_file of the shell's probe in %s: got %q err %v; the file tools do not reach the scratch the shell writes to", scratch, got, err)
	}
	if _, err := env.WriteFile(filepath.Join(scratch, "tool.txt"), "tool\n"); err != nil {
		t.Errorf("write_file into %s: %v; the file tools do not reach the scratch the shell writes to", scratch, err)
	}
}

// The file-tool enforcement layer folds the session scratch into its grants
// when it is built. A session's environment swap moves the scratch between
// environment objects after that: the clone the session lands on gains one it
// had none of, and the environment it left mints a fresh one for whoever
// still shares it. A layer built before the move keeps granting the old path
// (or none), so the file tools and the shell would name different scratch
// directories; the layer has to follow the effective path like the
// unsandboxed scratch layer already does.
func TestFileToolLayerFollowsTheScratchAcrossAdoption(t *testing.T) {
	t.Run("environment that adopts", func(t *testing.T) {
		from := readConfinedEnvAt(t, t.TempDir())
		// A confined env that owns no scratch (its own was disposed) builds its
		// layer with no scratch at all.
		to := readConfinedEnvAt(t, t.TempDir())
		to.DisposeSandboxScratch()
		if _, err := to.ReadFile(filepath.Join(to.RootDir, "missing"), nil, nil); err == nil {
			t.Fatal("reading a missing file succeeded")
		}

		to.AdoptSessionScratch(from)

		assertFileToolsShareTheShellScratch(t, to)
	})
	t.Run("environment that was left", func(t *testing.T) {
		from := readConfinedEnvAt(t, t.TempDir())
		// A file tool builds the layer around the scratch the env owns now.
		if _, err := from.WriteFile(filepath.Join(from.SessionScratchDir(), "before.txt"), "before\n"); err != nil {
			t.Fatalf("write_file into the owned scratch: %v", err)
		}
		to := from.WithWorkingDirectory(t.TempDir())
		t.Cleanup(to.DisposeUnadoptedScratch)

		to.AdoptSessionScratch(from)

		// The env that was left mints a fresh scratch for its next command.
		assertFileToolsShareTheShellScratch(t, from)
	})
}

// A swap moving the scratch while a file tool on the shared environment is
// mid-operation must stay race-free: the layer the operation holds is never
// closed under it, and every cache field is read and written under the lock.
func TestAdoptSessionScratchDoesNotRaceAFileToolOnTheSharedEnvironment(t *testing.T) {
	from := readConfinedEnvAt(t, t.TempDir())
	to := from.WithWorkingDirectory(t.TempDir())
	t.Cleanup(to.DisposeUnadoptedScratch)

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
				// The scratch may be mid-move: a refusal is the expected outcome
				// then, and only the absence of a race is under test.
				_, _ = from.WriteFile(filepath.Join(from.SessionScratchDir(), "racing.txt"), "x\n")
			}
		}
	}()
	for range 200 {
		to.AdoptSessionScratch(from)
		from.AdoptSessionScratch(to)
	}
	close(stop)
	<-writerDone

	assertFileToolsShareTheShellScratch(t, from)
}

// retiredLayerFootprint reports how many replaced layers env still holds and
// how many root descriptors they keep open between them.
func retiredLayerFootprint(e *LocalExecutionEnvironment) (layers, fds int) {
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	for _, layer := range e.retiredFS {
		layer.mu.Lock()
		fds += len(layer.rootFds)
		layer.mu.Unlock()
	}
	return len(e.retiredFS), fds
}

// A session that enters and exits worktrees repeatedly moves its scratch on
// every swap, and a file tool in between rebuilds the layer each time. A
// replaced layer stays open only while an operation still holds it; once every
// operation on it has completed it is reclaimed at the next rebuild, so the
// retired set is bounded by the operations in flight, not by the number of
// swaps — and an operation that is in flight across a rebuild still completes
// against the root it started with.
func TestRetiredFileToolLayersAreReclaimedOnceDrained(t *testing.T) {
	from := readConfinedEnvAt(t, t.TempDir())
	to := readConfinedEnvAt(t, t.TempDir())
	to.DisposeSandboxScratch()
	first := from.SessionScratchDir()
	if _, err := from.WriteFile(filepath.Join(first, "held.txt"), "held\n"); err != nil {
		t.Fatalf("write_file into the owned scratch: %v", err)
	}
	// An operation in flight across every rebuild below: it holds the layer
	// built around the scratch from owns now.
	held := from.sandbox()
	if held == nil {
		t.Fatal("a confined env built no file-tool layer")
	}

	// Every move changes both environments' effective scratch path, and the
	// file tool that follows on each rebuilds that environment's layer.
	for range 200 {
		to.AdoptSessionScratch(from)
		_, _ = from.ReadFile(filepath.Join(from.RootDir, "missing"), nil, nil)
		_, _ = to.ReadFile(filepath.Join(to.RootDir, "missing"), nil, nil)
		from.AdoptSessionScratch(to)
		_, _ = from.ReadFile(filepath.Join(from.RootDir, "missing"), nil, nil)
		_, _ = to.ReadFile(filepath.Join(to.RootDir, "missing"), nil, nil)
	}

	heldFds := func() int {
		held.mu.Lock()
		defer held.mu.Unlock()
		return len(held.rootFds)
	}()
	if layers, fds := retiredLayerFootprint(from); layers > 1 || fds > heldFds {
		t.Errorf("from retains %d replaced layers holding %d root fds after 200 moves, want at most the one layer (%d fds) an operation still holds", layers, fds, heldFds)
	}
	if layers, fds := retiredLayerFootprint(to); layers != 0 || fds != 0 {
		t.Errorf("to retains %d replaced layers holding %d root fds after 200 moves, want none: nothing holds them", layers, fds)
	}
	if b, err := held.readFile("read_file", filepath.Join(first, "held.txt")); err != nil || string(b) != "held\n" {
		t.Errorf("the held layer no longer completes against its old root: got %q err %v", b, err)
	}

	// Released, the held layer is reclaimed at the next rebuild.
	held.release()
	to.AdoptSessionScratch(from)
	_, _ = from.ReadFile(filepath.Join(from.RootDir, "missing"), nil, nil)
	if layers, fds := retiredLayerFootprint(from); layers != 0 || fds != 0 {
		t.Errorf("from retains %d replaced layers holding %d root fds after the held operation completed, want none", layers, fds)
	}
}

// openRootFdsOf counts the root descriptors env's file-tool layers, current
// and retired, still hold open.
func openRootFdsOf(e *LocalExecutionEnvironment) int {
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	fds := 0
	for _, layer := range append([]*sandboxFS{e.sbfs, e.scratchFS}, e.retiredFS...) {
		if layer == nil {
			continue
		}
		layer.mu.Lock()
		fds += len(layer.rootFds)
		layer.mu.Unlock()
	}
	return fds
}

// A worktree exit discards the clone the session entered on, and nothing
// touches that clone again. The layers its file tools built around the
// scratch it was holding have to go with the scratch when ownership moves
// back, through the same drain (closed once no operation holds them), or
// every enter/exit cycle leaks the clone's root descriptors.
func TestAdoptSessionScratchRetiresTheSourcesFileToolLayers(t *testing.T) {
	if runtimeGOOS != "linux" && runtimeGOOS != "darwin" {
		t.Skip("the unsandboxed scratch layer is linux/darwin only")
	}
	shapes := map[string]func(t *testing.T, worktree string) *LocalExecutionEnvironment{
		"unsandboxed": func(t *testing.T, worktree string) *LocalExecutionEnvironment {
			env := NewLocalExecutionEnvironment(worktree)
			t.Cleanup(func() { env.Cleanup(); env.DisposeUnadoptedScratch() })
			if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
				t.Fatalf("ExecCommand: %v", err)
			}
			return env
		},
		"confined without a wrapper": readConfinedEnvAt,
	}
	for name, build := range shapes {
		t.Run(name, func(t *testing.T) {
			owner := build(t, t.TempDir())
			lane := t.TempDir()
			var discarded []*LocalExecutionEnvironment
			for range 200 {
				clone := owner.WithWorkingDirectory(lane)
				clone.AdoptSessionScratch(owner)
				if _, err := clone.WriteFile(filepath.Join(clone.SessionScratchDir(), "cycle.txt"), "x\n"); err != nil {
					t.Fatalf("write_file on the entered clone: %v", err)
				}
				owner.AdoptSessionScratch(clone)
				discarded = append(discarded, clone)
			}
			open := 0
			for _, clone := range discarded {
				open += openRootFdsOf(clone)
			}
			if open != 0 {
				t.Errorf("200 discarded clones still hold %d root fds open, want none", open)
			}
			if _, err := owner.WriteFile(filepath.Join(owner.SessionScratchDir(), "after.txt"), "x\n"); err != nil {
				t.Errorf("write_file on the owner after the cycles: %v", err)
			}
		})
	}
}
