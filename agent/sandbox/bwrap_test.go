//go:build linux

// buildBwrapArgv is only ever driven on Linux (bubblewrap is the Linux backend),
// and these tests assert exact bind paths against t.TempDir(). On macOS TempDir
// lives under /var, a symlink the kernel canonicalizes to /private/var, so the
// argv builder correctly emits the canonical spelling while the byte-exact
// assertions expect the /var spelling — a spurious failure for a Linux-only code
// path. Constrain the file to linux so it neither compiles nor runs on darwin.
package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A read-only session whose worktree lives under /tmp: the /tmp tmpfs shadows the
// cwd, so without a re-bind --chdir aborts the sandbox. Read-only mode grants no
// write root to save the cwd, so it must be re-bound read-only AFTER the tmpfs.
func TestBuildBwrapArgvReadOnlyRebindsTmpCwd(t *testing.T) {
	home := t.TempDir()
	cwd := MaterializeWorkspace(t, MainCheckout) // t.TempDir()-based, under /tmp
	if !pathUnder(cwd, "/tmp") {
		t.Skipf("test needs a /tmp-based cwd; TempDir gave %q", cwd)
	}
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeReadOnly, Network: &net}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	args := buildBwrapArgv(rp, "/tmp/serf-session", cwd)

	tmpfsIdx := seqIndex(args, "--tmpfs", "/tmp")
	rebindIdx := seqIndex(args, "--ro-bind", cwd, cwd)
	if rebindIdx < 0 {
		t.Fatalf("read-only cwd under /tmp must be re-bound read-only: %v", args)
	}
	if tmpfsIdx < 0 || rebindIdx < tmpfsIdx {
		t.Errorf("cwd re-bind (idx %d) must come after the /tmp tmpfs (idx %d): %v", rebindIdx, tmpfsIdx, args)
	}
}

func TestBuildBwrapArgvBaseHardening(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "/tmp/serf-session", cwd)

	for _, want := range []string{"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session"} {
		if !slices.Contains(args, want) {
			t.Errorf("base hardening flag %q missing: %v", want, args)
		}
	}
	if !hasSeq(args, "--proc", "/proc") {
		t.Errorf("expected a fresh --proc /proc mount: %v", args)
	}
	if !hasSeq(args, "--dev", "/dev") {
		t.Errorf("expected a minimal --dev /dev: %v", args)
	}
	if !hasSeq(args, "--chdir", cwd) {
		t.Errorf("expected --chdir into the worktree %q: %v", cwd, args)
	}
	// The command separator is appended by Wrap, not by the flag builder.
	if slices.Contains(args, "--") {
		t.Errorf("the flag builder must not emit the -- command separator: %v", args)
	}
}

func TestBuildBwrapArgvProcIsFreshNotHostBind(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "", cwd)

	// /proc must come ONLY from a fresh --proc mount (pid-ns), never a --ro-bind
	// of the host /proc (which would expose host process state, e.g. serf's env).
	if hasSeq(args, "--ro-bind", "/proc", "/proc") || hasSeq(args, "--tmpfs", "/proc") {
		t.Errorf("/proc must be a fresh --proc mount, not a host bind/tmpfs: %v", args)
	}
	if !hasSeq(args, "--proc", "/proc") {
		t.Errorf("expected --proc /proc: %v", args)
	}
}

func TestBuildBwrapArgvReadAnywhereBindsRootReadOnly(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "", cwd)

	if !hasSeq(args, "--ro-bind", "/", "/") {
		t.Errorf("read-anywhere mode must bind / read-only: %v", args)
	}
	if hasSeq(args, "--tmpfs", "/") {
		t.Errorf("read-anywhere mode must not start from an empty tmpfs root: %v", args)
	}
	// The worktree is re-bound writable on top of the read-only root.
	if !hasSeq(args, "--bind", cwd, cwd) {
		t.Errorf("expected worktree %q bound writable: %v", cwd, args)
	}
}

func TestBuildBwrapArgvRestrictedTmpfsRootAndSystemRoots(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeRestricted, true)
	args := buildBwrapArgv(rp, "", cwd)

	if !hasSeq(args, "--tmpfs", "/") {
		t.Errorf("restricted mode must start from an empty tmpfs root: %v", args)
	}
	if hasSeq(args, "--ro-bind", "/", "/") {
		t.Errorf("restricted mode must NOT bind all of / : %v", args)
	}
	if !hasSeq(args, "--ro-bind", "/usr", "/usr") {
		t.Errorf("restricted spawned procs must read system roots like /usr: %v", args)
	}
	if !hasSeq(args, "--bind", cwd, cwd) {
		t.Errorf("restricted worktree %q must be writable: %v", cwd, args)
	}
}

func TestBuildBwrapArgvMasksSecrets(t *testing.T) {
	rp, cwd, home := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "", cwd)

	ssh := filepath.Join(home, ".ssh")
	if !hasSeq(args, "--tmpfs", ssh) {
		t.Errorf("secret dir %q must be masked with an empty tmpfs: %v", ssh, args)
	}
	creds := filepath.Join(home, ".git-credentials")
	if !hasSeq(args, "--ro-bind", "/dev/null", creds) {
		t.Errorf("secret file %q must be masked with a read-only /dev/null bind: %v", creds, args)
	}
}

func TestBuildBwrapArgvProtectsGitConfig(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "", cwd)

	cfg := filepath.Join(cwd, ".git", "config")
	if !hasSeq(args, "--ro-bind", cfg, cfg) {
		t.Errorf("git config %q must be re-mounted read-only inside the writable worktree: %v", cfg, args)
	}
	hooks := filepath.Join(cwd, ".git", "hooks")
	if !hasSeq(args, "--ro-bind", hooks, hooks) {
		t.Errorf("git hooks dir %q must be read-only: %v", hooks, args)
	}
}

func TestBuildBwrapArgvNetwork(t *testing.T) {
	off, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, false)
	if !slices.Contains(buildBwrapArgv(off, "", cwd), "--unshare-net") {
		t.Errorf("net=off must unshare the network namespace")
	}
	on, cwd2, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	if slices.Contains(buildBwrapArgv(on, "", cwd2), "--unshare-net") {
		t.Errorf("net=on must NOT unshare the network namespace")
	}
}

// TestBuildBwrapArgvLinkedWorktreeNoDeadPin covers verifier finding F2: a linked
// worktree's common .git lives OUTSIDE the worktree and is only read-granted, so a
// missing protected surface there (config.worktree) must NOT be pinned — pinning
// it makes bwrap try to create a mountpoint under the read-only common dir (EROFS)
// and abort the whole sandbox, which would break serf's primary workflow.
func TestBuildBwrapArgvLinkedWorktreeNoDeadPin(t *testing.T) {
	requireGitHarness(t)
	home := t.TempDir()
	cwd := MaterializeWorkspace(t, LinkedWorktree)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	args := buildBwrapArgv(rp, "/tmp/s", cwd)

	// No protected surface may be pinned via /dev/null under a read-only parent.
	// The effective roots — not the resolved ones — decide: the argv now binds the
	// common dir writable at directory level, so a pin inside it lands on a
	// writable mount; a pin outside every effective write root would still EROFS.
	commonDir := rp.Git.CommonDir
	effective := bwrapWriteRoots(rp)
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--ro-bind" && args[i+1] == "/dev/null" {
			target := args[i+2]
			if pathUnder(target, commonDir) && !isUnderAnyRoot(target, effective) {
				t.Errorf("dead pin under read-only common dir would EROFS-abort the sandbox: %q", target)
			}
		}
	}
}

func TestBuildBwrapArgvMasksDaemonSockets(t *testing.T) {
	if !pathExists("/run/docker.sock") {
		t.Skip("no /run/docker.sock on this host to mask")
	}
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	args := buildBwrapArgv(rp, "", cwd)
	if !hasSeq(args, "--ro-bind", "/dev/null", "/run/docker.sock") {
		t.Errorf("the docker daemon socket must be masked (connect() escape vector): %v", args)
	}
}

func TestBuildBwrapArgvMasksSymlinkedSecret(t *testing.T) {
	// Verifier finding F3: a symlinked credential dir must be masked at its real
	// target (as a directory), not misclassified and aborted.
	home := t.TempDir()
	realSSH := filepath.Join(home, "real-ssh")
	if err := os.MkdirAll(realSSH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSSH, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	args := buildBwrapArgv(rp, "", cwd)
	// The mask lands on the resolved real dir with a tmpfs (dir), never a
	// /dev/null file-bind over the symlink (which bwrap refuses).
	if !hasSeq(args, "--tmpfs", realSSH) {
		t.Errorf("symlinked ~/.ssh must be masked at its real target with a tmpfs: %v", args)
	}
	if hasSeq(args, "--ro-bind", "/dev/null", filepath.Join(home, ".ssh")) {
		t.Errorf("a symlinked secret dir must not be file-bound (bwrap aborts): %v", args)
	}
}

func TestBuildBwrapArgvCacheOverlay(t *testing.T) {
	// Overlay is a pure-argv concern here (no bwrap run), so this validates the
	// overlay branch even though this host's bwrap lacks overlay support.
	home := t.TempDir()
	for _, d := range []string{".cache", ".cargo"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cwd := MaterializeWorkspace(t, MainCheckout)
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rp.CacheStrategy != CacheOverlay {
		t.Fatalf("workspace-write on an overlay-capable host must use overlay, got %v", rp.CacheStrategy)
	}
	args := buildBwrapArgv(rp, "/tmp/s", cwd)
	cache := filepath.Join(home, ".cache")
	if !hasSeq(args, "--overlay-src", cache, "--tmp-overlay", cache) {
		t.Errorf("expected a read-real/write-private overlay for %q: %v", cache, args)
	}
}

func TestBuildBwrapArgvNoOverlayWhenSessionPrivate(t *testing.T) {
	// The default fixture host lacks overlay → session-private cache → no overlay
	// mounts in the argv (the env floor redirects the cache vars instead).
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	if rp.CacheStrategy != CacheSessionPrivate {
		t.Fatalf("no-overlay host must use session-private cache, got %v", rp.CacheStrategy)
	}
	if slices.Contains(buildBwrapArgv(rp, "/tmp/s", cwd), "--overlay-src") {
		t.Errorf("session-private cache must not emit overlay mounts")
	}
}

func TestBuildBwrapArgvSessionTmp(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	tmp := t.TempDir()
	args := buildBwrapArgv(rp, tmp, cwd)
	if !hasSeq(args, "--tmpfs", "/tmp") {
		t.Errorf("expected a non-shared /tmp tmpfs: %v", args)
	}
	if !hasSeq(args, "--bind", tmp, tmp) {
		t.Errorf("expected the session tmp %q bound writable: %v", tmp, args)
	}
	if seqIndex(args, "--remount-ro", "/tmp") >= 0 {
		t.Errorf("writable mode must NOT remount /tmp read-only: %v", args)
	}
}

// TestBuildBwrapArgvReadOnlySessionTmp pins the fix for the read-only-mode /tmp
// leak: session_prompts.go promises "all other writes are denied" under
// ModeReadOnly, but the /tmp tmpfs was left fully writable. /tmp must be
// remounted read-only AFTER the session tmp is bound writable inside it, so the
// scratch dir stays writable while everything else under /tmp is not.
func TestBuildBwrapArgvReadOnlySessionTmp(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeReadOnly, true)
	tmp := t.TempDir()
	args := buildBwrapArgv(rp, tmp, cwd)

	bindIdx := seqIndex(args, "--bind", tmp, tmp)
	if bindIdx < 0 {
		t.Fatalf("expected the session tmp %q bound writable: %v", tmp, args)
	}
	remountIdx := seqIndex(args, "--remount-ro", "/tmp")
	if remountIdx < 0 {
		t.Fatalf("expected /tmp remounted read-only in read-only mode: %v", args)
	}
	if remountIdx < bindIdx {
		t.Errorf("--remount-ro /tmp (at %d) must come AFTER the session tmp bind (at %d): %v", remountIdx, bindIdx, args)
	}
}

// TestBuildBwrapArgvBindsInfraReadRoots pins the Linux half of the 2026-08-06
// ruling: the session's hook/MCP-server paths reach the bwrap argv as a
// read-only bind (never a writable --bind), so a hook script in the plugin cache
// execs under restricted mode on Linux exactly as it does under Seatbelt. bwrap
// needs no special case — it binds every Spawned.ReadRoot — and this test is what
// stops that from regressing.
func TestBuildBwrapArgvBindsInfraReadRoots(t *testing.T) {
	home := t.TempDir()
	infra := t.TempDir()
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net, InfraReadRoots: []string{infra}}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if seqIndex(args, "--ro-bind", infra, infra) < 0 {
		t.Errorf("hook/MCP root %q must be bound read-only, got argv:\n%v", infra, args)
	}
	if seqIndex(args, "--bind", infra, infra) >= 0 {
		t.Errorf("hook/MCP root %q must never be bound WRITABLE, got argv:\n%v", infra, args)
	}
}
