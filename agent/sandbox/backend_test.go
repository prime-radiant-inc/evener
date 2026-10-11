package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestNewWrapperRejectsNonEnforcingBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendNone}, "/usr/bin/bwrap", "/tmp/s"); err == nil {
		t.Error("expected NewWrapper to refuse the non-enforcing BackendNone")
	}
}

func TestNewWrapperAcceptsSeatbeltBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendSeatbelt}, "/usr/bin/sandbox-exec", "/tmp/s"); err != nil {
		t.Errorf("NewWrapper must accept the seatbelt backend: %v", err)
	}
}

func TestNewWrapperRejectsRelativeBinaryPath(t *testing.T) {
	for _, b := range []Backend{BackendBwrap, BackendSeatbelt} {
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "bwrap", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "./bin/sandbox-exec", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
	}
}

func TestWrapPrependsBwrapAndSeparatesCommand(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	argv := []string{"/bin/bash", "-c", "echo hi"}
	got := w.Wrap(argv, cwd)

	if got[0] != "/usr/bin/bwrap" {
		t.Errorf("wrapped argv must start with the bwrap binary, got %q", got[0])
	}
	// --argv0 preserves the command's own argv[0].
	if !hasSeq(got, "--argv0", "/bin/bash") {
		t.Errorf("expected --argv0 /bin/bash: %v", got)
	}
	// Everything after the LAST "--" is the original command, unmodified.
	sep := slices.Index(got, "--")
	if sep < 0 {
		t.Fatalf("wrapped argv missing the -- command separator: %v", got)
	}
	if !slices.Equal(got[sep+1:], argv) {
		t.Errorf("command after -- = %v, want %v", got[sep+1:], argv)
	}
	// The bwrap flags carry the confinement.
	if !hasSeq(got, "--unshare-pid") || !hasSeq(got, "--proc", "/proc") {
		t.Errorf("wrapped argv missing confinement flags: %v", got)
	}
}

func TestWrapNilIsIdentity(t *testing.T) {
	var w *Wrapper
	argv := []string{"/bin/echo", "hi"}
	if got := w.Wrap(argv, "/somewhere"); !slices.Equal(got, argv) {
		t.Errorf("nil wrapper must be identity, got %v", got)
	}
}

// TestConfineWrapsBwrapAndLeavesDir pins that Confine rewrites the command's argv
// with the backend invocation but does NOT touch cmd.Dir for bwrap — bwrap carries
// the working directory in its own argv (--chdir), so the caller's cmd.Dir is left
// as-is. (The Seatbelt cmd.Dir path is exercised on darwin by TestConfineSetsSeatbeltDir.)
func TestConfineWrapsBwrapAndLeavesDir(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	orig := slices.Clone(cmd.Args)
	w.Confine(cmd, cwd)

	if cmd.Args[0] != "/usr/bin/bwrap" || cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("Confine must prepend the bwrap binary: args[0]=%q path=%q", cmd.Args[0], cmd.Path)
	}
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], orig) {
		t.Errorf("original command must survive after --: %v", cmd.Args)
	}
	if cmd.Dir != "" {
		t.Errorf("Confine must not set cmd.Dir for bwrap (uses --chdir), got %q", cmd.Dir)
	}
}

// TestConfineNilIsIdentity pins that a nil wrapper leaves the command untouched,
// so an unsandboxed spawn is byte-identical to before.
func TestConfineNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.Confine(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}

// TestConfineTrustedInfraKeepsNetworkUnderNetOff pins kata 83pm: MCP servers are
// trusted infrastructure (launched only from config layers the model cannot
// write), so under a net=off session policy Confine still severs the network
// (--unshare-net) for an ordinary spawned process, but ConfineTrustedInfra does
// NOT — while both retain identical filesystem confinement (the base hardening
// flags and the worktree bind survive either path).
func TestConfineTrustedInfraKeepsNetworkUnderNetOff(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, false)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	ordinary := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.Confine(ordinary, cwd)
	if !slices.Contains(ordinary.Args, "--unshare-net") {
		t.Errorf("model-authored spawn must be network-severed under net=off: %v", ordinary.Args)
	}

	infra := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.ConfineTrustedInfra(infra, cwd)
	if slices.Contains(infra.Args, "--unshare-net") {
		t.Errorf("trusted-infrastructure spawn (MCP) must keep network under net=off: %v", infra.Args)
	}
	// Filesystem confinement is unchanged by the network carve-out.
	if !slices.Contains(infra.Args, "--unshare-pid") || !hasSeq(infra.Args, "--bind", cwd, cwd) {
		t.Errorf("trusted-infrastructure spawn must keep filesystem confinement: %v", infra.Args)
	}
}

// TestConfineTrustedInfraNilIsIdentity mirrors TestConfineNilIsIdentity for the
// trusted-infra entry point.
func TestConfineTrustedInfraNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.ConfineTrustedInfra(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}

// Git's own write grants include files (packed-refs and its lock), so a
// missing one inside an overlaid cache root is never created as a directory:
// git would then fail to write it.
func TestWrapperLeavesAMissingGitFileGrantInsideACacheUncreated(t *testing.T) {
	cache := t.TempDir()
	commonDir := filepath.Join(cache, "repo", ".git")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packedRefs := filepath.Join(commonDir, "packed-refs")
	rp := ResolvedPolicy{CacheRoots: []string{cache}}
	rp.Git.CommonDir = commonDir
	rp.Git.GitDir = commonDir
	rp.Spawned.WriteRoots = []string{packedRefs}
	createWriteRootsInsideCaches(rp)
	if _, err := os.Lstat(packedRefs); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing git file grant must not be created (err=%v)", err)
	}
}

// A missing write root spelled through a link that resolves inside an
// overlaid cache root is created too: the overlay covers where it resolves.
func TestWrapperCreatesAMissingWriteRootThatResolvesInsideACache(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "cachelink")
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(link, "library")
	rp := ResolvedPolicy{CacheRoots: []string{cache}}
	rp.Spawned.WriteRoots = []string{library}
	createWriteRootsInsideCaches(rp)
	if fi, err := os.Stat(library); err != nil || !fi.IsDir() {
		t.Errorf("a missing write root resolving inside a cache must be created (err=%v)", err)
	}
}

// When a missing write root inside a cache cannot be created (here the cache
// is not writable), bwrap cannot bind it over the overlay, so that cache is
// not overlaid for the spawn: writes there would otherwise land in the
// discarded layer and vanish. Without the overlay they fail where they happen.
func TestPrepareCacheRootsDropsAnOverlayWhoseWriteRootCannotBeCreated(t *testing.T) {
	cache := t.TempDir()
	library := filepath.Join(cache, "library")
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cache, 0o755) })
	if err := os.Mkdir(library, 0o755); err == nil {
		t.Skip("running with privileges that ignore directory permissions")
	}
	rp := ResolvedPolicy{CacheStrategy: CacheOverlay, CacheRoots: []string{cache}}
	rp.Spawned.WriteRoots = []string{library}
	if got := prepareCacheRoots(rp); slices.Contains(got.CacheRoots, cache) {
		t.Errorf("a cache holding a write root that could not be created must not be overlaid: %v", got.CacheRoots)
	}
}

// A granted write root spelled inside the worktree but resolving through a
// link into a cache is created too: only git's own entries are skipped.
func TestWrapperCreatesAWorktreeSpelledWriteRootThatResolvesInsideACache(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".cache")
	worktree := filepath.Join(home, "repo")
	for _, d := range []string{cache, filepath.Join(worktree, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(cache, filepath.Join(worktree, "cache")); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(worktree, "cache", "library")
	rp := ResolvedPolicy{CacheRoots: []string{cache}}
	rp.Git.WorktreeRoot = worktree
	rp.Git.GitDir = filepath.Join(worktree, ".git")
	rp.Git.CommonDir = rp.Git.GitDir
	rp.Spawned.WriteRoots = []string{library}
	createWriteRootsInsideCaches(rp)
	if fi, err := os.Stat(library); err != nil || !fi.IsDir() {
		t.Errorf("a write root reached through a worktree link into a cache must be created (err=%v)", err)
	}
}

// A write root that is a dangling link inside a cache cannot be bound, and
// creating its target through the link could land anywhere, so the cache
// holding it is not overlaid for the spawn.
func TestPrepareCacheRootsDropsAnOverlayHoldingADanglingWriteRoot(t *testing.T) {
	cache := t.TempDir()
	library := filepath.Join(cache, "library")
	if err := os.Symlink(filepath.Join(cache, "library-data"), library); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{CacheStrategy: CacheOverlay, CacheRoots: []string{cache}}
	rp.Spawned.WriteRoots = []string{library}
	if got := prepareCacheRoots(rp); slices.Contains(got.CacheRoots, cache) {
		t.Errorf("a cache holding a dangling write root must not be overlaid: %v", got.CacheRoots)
	}
}

// A dangling write root's missing target can sit in another cache root; that
// cache would take writes through the link into its discarded layer, so it is
// not overlaid either.
func TestPrepareCacheRootsDropsTheOverlayOverADanglingWriteRootsTarget(t *testing.T) {
	cache, npm := t.TempDir(), t.TempDir()
	library := filepath.Join(cache, "library")
	if err := os.Symlink(filepath.Join(npm, "library-data"), library); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{CacheStrategy: CacheOverlay, CacheRoots: []string{cache, npm}}
	rp.Spawned.WriteRoots = []string{library}
	if got := prepareCacheRoots(rp); len(got.CacheRoots) != 0 {
		t.Errorf("neither the link's cache nor its target's may stay overlaid: %v", got.CacheRoots)
	}
}

// A write root reached through a link whose own target passes a dangling
// link lands where that dangling link points, possibly in another cache root;
// that cache is not overlaid either.
func TestPrepareCacheRootsDropsTheOverlayBehindADanglingIntermediateLink(t *testing.T) {
	home := t.TempDir()
	cache, npm := filepath.Join(home, ".cache"), filepath.Join(home, ".npm")
	for _, d := range []string{cache, npm} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(npm, "data"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(cache, "library")
	if err := os.Symlink(filepath.Join(home, "alias", "pkg"), library); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{CacheStrategy: CacheOverlay, CacheRoots: []string{cache, npm}}
	rp.Spawned.WriteRoots = []string{library}
	if got := prepareCacheRoots(rp); slices.Contains(got.CacheRoots, npm) {
		t.Errorf("the cache a dangling intermediate link leads into must not stay overlaid: %v", got.CacheRoots)
	}
}
