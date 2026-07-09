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

// bwrapFacts is a bwrap-capable host anchored at a fake home so masked paths land
// under a directory the test controls. It reports OS "linux" so Resolve (which
// keys on HostFacts.OS, not runtime.GOOS) yields a bwrap-backed policy on any
// build host — that is what lets backend/reroot tests exercise the bwrap path
// while running on darwin.
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
