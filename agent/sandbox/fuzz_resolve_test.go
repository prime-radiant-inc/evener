//go:build serffuzz

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFuzzResolveFixtureStaysUnderTempRoot(t *testing.T) {
	fixture := newFuzzResolveFixture(t, "../../ambient/path")
	layout, err := ClassifyWorkspace(fixture.cwd)
	if err != nil {
		t.Fatalf("ClassifyWorkspace(%q): %v", fixture.cwd, err)
	}
	if layout.WorktreeRoot != fixture.workspace {
		t.Fatalf("ClassifyWorkspace(%q).WorktreeRoot = %q, want %q", fixture.cwd, layout.WorktreeRoot, fixture.workspace)
	}
}

type fuzzResolveFixture struct {
	root       string
	workspace  string
	cwd        string
	home       string
	denyAdd    string
	extraRead  string
	extraWrite string
}

// newFuzzResolveFixture gives Resolve a structurally valid main checkout and a
// fake home beneath one temp root. cwd is derived from fuzz bytes but never uses
// them as a host path, because ClassifyWorkspace walks parent directories.
func newFuzzResolveFixture(t TestingT, cwdHint string) fuzzResolveFixture {
	t.Helper()
	root := resolveCleanPath(t.TempDir())
	fixture := fuzzResolveFixture{
		root:       root,
		workspace:  filepath.Join(root, "workspace"),
		home:       filepath.Join(root, "home"),
		denyAdd:    filepath.Join(root, "extra", "secret"),
		extraRead:  filepath.Join(root, "extra", "read"),
		extraWrite: filepath.Join(root, "extra", "write"),
	}
	for _, dir := range []string{
		filepath.Join(fixture.workspace, ".git"),
		fixture.home,
		filepath.Dir(fixture.denyAdd),
		fixture.extraRead,
		fixture.extraWrite,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir resolver fixture %q: %v", dir, err)
		}
	}
	if len(cwdHint)%2 == 0 {
		fixture.cwd = fixture.workspace
		return fixture
	}
	digest := sha256.Sum256([]byte(cwdHint))
	fixture.cwd = filepath.Join(fixture.workspace, "fuzz", hex.EncodeToString(digest[:8]))
	return fixture
}

func (fixture fuzzResolveFixture) homeFor(homeHint string) string {
	if filepath.IsAbs(homeHint) {
		return fixture.home
	}
	return homeHint
}

// FuzzResolve drives the resolver with arbitrary policy/host/cwd hints against a
// structural temp-root checkout. The fuzzer never hands a raw path to
// ClassifyWorkspace, so seed replay cannot inspect the developer's filesystem.
// It enforces its three total-correctness invariants, no matter how adversarial
// the input:
//
//  1. It never panics.
//  2. Every error it returns is a typed *RefusalError (the fail-closed floor is
//     never a bare/undifferentiated error).
//  3. A successfully-resolved policy NEVER lists a masked (denylisted/pseudo-fs)
//     path as a readable or writable root — the containment floor can't be
//     resolved away. And any enforced (non-off) policy always masks the pseudo-fs
//     set (at minimum /proc), so serf's own /proc/<pid>/environ stays unreadable.
func FuzzResolve(f *testing.F) {
	// Seeds spanning the floor matrix: each host tier × representative modes. The
	// trailing string is a fuzzed DenylistRemove entry; cwd is a safe path hint.
	f.Add(0, true, "linux", "/home/u", true, true, "", "/work", "")                          // off, bwrap
	f.Add(1, true, "linux", "/home/u", true, false, "", "/work", ".aws")                     // read-only, bwrap; remove a credential
	f.Add(2, false, "linux", "/home/u", true, true, "", "/work", "/proc")                    // workspace-write net=off; try to remove /proc
	f.Add(3, true, "linux", "/home/u", false, false, "", "/work", "")                        // restricted, non-bwrap linux
	f.Add(3, false, "linux", "/home/u", false, false, "", "/work", "")                       // restricted net=off, non-bwrap linux
	f.Add(2, true, "windows", "C:/u", false, false, "", "C:/work", "")                       // workspace-write, windows
	f.Add(1, true, "darwin", "/Users/u", false, false, "/usr/bin/sandbox-exec", "/work", "") // read-only, seatbelt
	f.Add(3, true, "darwin", "/Users/u", false, false, "", "/work", "")                      // restricted, darwin bare
	f.Add(2, true, "linux", "relative/home", true, true, "", "/work", "")                    // bwrap, NON-absolute home → must refuse
	f.Add(2, true, "", "", false, false, "", "", "")                                         // degenerate: empty everything

	f.Fuzz(func(t *testing.T,
		modeSel int, network bool, osName, homeHint string,
		bwrapCapable, overlay bool, sandboxExec, cwdHint, denyRemove string,
	) {
		fixture := newFuzzResolveFixture(t, cwdHint)
		mode := Mode(((modeSel % len(AllModes())) + len(AllModes())) % len(AllModes()))
		policy := SandboxPolicy{
			Mode:               mode,
			Network:            &network,
			DenylistAdd:        []string{fixture.denyAdd, "~/.custom"},
			DenylistRemove:     []string{denyRemove, "/proc"}, // /proc removal must be ignored (floor)
			ExtraWritableRoots: []string{"/proc", fixture.extraWrite},
			ExtraReadRoots:     []string{"/sys/kernel", fixture.extraRead},
		}
		host := HostFacts{
			OS: osName, Home: fixture.homeFor(homeHint), BwrapCapable: bwrapCapable,
			OverlaySupported: overlay, SandboxExecPath: sandboxExec,
		}

		rp, err := Resolve(policy, host, fixture.cwd)
		if err != nil {
			var ref *RefusalError
			if !errors.As(err, &ref) {
				t.Fatalf("Resolve returned a non-RefusalError: %T %v", err, err)
			}
			return
		}
		if rp.Enforced() && !pathUnder(rp.Git.WorktreeRoot, fixture.root) {
			t.Fatalf("resolved worktree %q escapes fuzz fixture %q", rp.Git.WorktreeRoot, fixture.root)
		}

		// Invariant 3a: no granted root is at/under a masked path.
		roots := slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots)
		for _, r := range roots {
			for _, m := range rp.MaskedPaths {
				if r == m || pathUnder(r, m) {
					t.Fatalf("resolved policy grants root %q at/under masked path %q (mode=%v)", r, m, mode)
				}
			}
		}

		// Invariant 3b: an enforced policy always masks the pseudo-fs floor.
		if rp.Enforced() && !slices.Contains(rp.MaskedPaths, "/proc") {
			t.Fatalf("enforced policy (mode=%v) does not mask /proc: %v", mode, rp.MaskedPaths)
		}
		// off never carries containment.
		if !rp.Enforced() && len(rp.MaskedPaths) != 0 {
			t.Fatalf("off policy carries masked paths: %v", rp.MaskedPaths)
		}
	})
}
