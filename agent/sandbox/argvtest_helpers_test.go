package sandbox

// Shared test helpers used by BOTH the linux-only bwrap-argv tests and the
// cross-platform backend/reroot tests. They live in an untagged file so the
// darwin build (which runs backend_test.go/reroot_test.go) still sees them even
// though bwrap_test.go is constrained to linux.

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

// devShmMainCheckout is a main-checkout workspace under /dev/shm, the tmpfs a
// gate or a user may keep a workspace on. Hosts without a writable /dev/shm
// skip.
func devShmMainCheckout(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/dev/shm", "sbx-devshm-cwd-")
	if err != nil {
		t.Skipf("this host offers no writable /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root = resolveCleanPath(root)
	if !pathUnder(root, "/dev/shm") {
		// /dev/shm is a symlink here (to /run/shm, say): --dev does not
		// shadow the resolved path, so there is no /dev/shm workspace to test.
		t.Skipf("/dev/shm resolves to %q on this host; no /dev/shm-based workspace exists", root)
	}
	contractGitRunnerFor(t)(t, root, "init", "-q")
	return root
}

// tmpMainCheckout is a main-checkout workspace under /tmp itself, whatever
// TMPDIR says. The read-only re-bind under test exists only for a cwd the
// sandbox's fresh /tmp tmpfs would otherwise shadow, and with the gate's
// TMPDIR on /dev/shm, t.TempDir() no longer lands under /tmp.
func tmpMainCheckout(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sbx-tmp-cwd-")
	if err != nil {
		t.Skipf("this host offers no writable /tmp for the workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root = resolveCleanPath(root)
	if !pathUnder(root, "/tmp") {
		// /tmp is a symlink here (to /var/tmp, say): the sandbox's /tmp
		// tmpfs does not shadow the resolved path, so there is no /tmp-based
		// workspace this host can offer.
		t.Skipf("/tmp resolves to %q on this host; no /tmp-based workspace exists", root)
	}
	contractGitRunnerFor(t)(t, root, "init", "-q")
	return root
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
