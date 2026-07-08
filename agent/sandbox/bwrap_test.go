package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// hasSeq reports whether args contains seq as a contiguous subsequence — the way
// bwrap flags come in ordered (flag, value…) groups.
func hasSeq(args []string, seq ...string) bool {
	if len(seq) == 0 {
		return true
	}
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

// bwrapFacts is a bwrap-capable host anchored at a fake home so masked paths land
// under a directory the test controls.
func bwrapFacts(home string) HostFacts {
	return HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
}

// resolveFixture materializes a main-checkout git repo, plants ~/.ssh and
// ~/.git-credentials in a fake home so the mask flags have real targets to stat,
// and resolves the requested mode against a bwrap host.
func resolveFixture(t *testing.T, mode Mode, netOn bool) (ResolvedPolicy, string, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".git-credentials"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write .git-credentials: %v", err)
	}
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := netOn
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", mode, err)
	}
	return rp, cwd, home
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
}
