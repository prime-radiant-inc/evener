package sandbox

// Shared test helpers used by BOTH the linux-only bwrap-argv tests and the
// cross-platform backend/reroot tests. They live in an untagged file so the
// darwin build (which runs backend_test.go/reroot_test.go) still sees them even
// though bwrap_test.go is constrained to linux.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// seqIndex returns the start index of the first contiguous occurrence of seq in
// args, or -1 if absent.
func seqIndex(args []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return i
		}
	}
	return -1
}

// bwrapFacts is a bwrap-capable host anchored at a fake home so masked paths land
// under a directory the test controls. It reports OS "linux" so Resolve (which
// keys on HostFacts.OS, not runtime.GOOS) yields a bwrap-backed policy on any
// build host — that is what lets backend/reroot tests exercise the bwrap path
// while running on darwin.
func bwrapFacts(home string) HostFacts {
	return HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false}
}

// secretHomeDir is a fake home whose secrets the bwrap argv must mask. It is
// t.TempDir() unless that lands under /dev, as it does when the gate puts
// TMPDIR on /dev/shm: bwrap's minimal --dev already hides everything under
// /dev, so buildBwrapArgv rightly emits no mask there and a /dev home would
// leave the masking under test unexercised. /var/tmp is the real-filesystem
// scratch the sandbox fixtures already use for paths that must not live
// under /tmp, so the home moves there instead.
func secretHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if home != "/dev" && !strings.HasPrefix(home, "/dev/") {
		return home
	}
	home, err := os.MkdirTemp("/var/tmp", "sbx-secret-home-")
	if err != nil {
		t.Fatalf("mkdir fake home outside /dev: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return resolveCleanPath(home)
}

// tmpMainCheckout is a main-checkout workspace under /tmp itself, whatever
// TMPDIR says. The read-only re-bind under test exists only for a cwd the
// sandbox's fresh /tmp tmpfs would otherwise shadow, and with the gate's
// TMPDIR on /dev/shm, t.TempDir() no longer lands under /tmp.
func tmpMainCheckout(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sbx-tmp-cwd-")
	if err != nil {
		t.Fatalf("mkdir /tmp workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root = resolveCleanPath(root)
	if !pathUnder(root, "/tmp") {
		t.Fatalf("/tmp workspace resolved to %q, outside /tmp", root)
	}
	contractGitRunnerFor(t)(t, root, "init", "-q")
	return root
}

// resolveFixture materializes a main-checkout git repo, plants ~/.ssh and
// ~/.git-credentials in a fake home so the mask flags have real targets to stat,
// and resolves the requested mode against a bwrap host.
func resolveFixture(t *testing.T, mode Mode, netOn bool) (ResolvedPolicy, string, string) {
	t.Helper()
	home := secretHomeDir(t)
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
