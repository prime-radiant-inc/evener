//go:build serffuzz

package sandbox

import (
	"errors"
	"slices"
	"testing"
)

// FuzzResolve drives the resolver with arbitrary policy/host/cwd inputs and
// enforces its three total-correctness invariants, no matter how adversarial the
// input:
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
	// trailing string is a fuzzed DenylistRemove entry.
	f.Add(0, true, "linux", "/home/u", true, true, "", "/work", "")                          // off, bwrap
	f.Add(1, true, "linux", "/home/u", true, false, "", "/work", ".aws")                     // read-only, bwrap; remove a credential
	f.Add(2, false, "linux", "/home/u", true, true, "", "/work", "/proc")                    // workspace-write net=off; try to remove /proc
	f.Add(3, true, "linux", "/home/u", false, false, "", "/work", "")                        // restricted, non-bwrap linux
	f.Add(3, false, "linux", "/home/u", false, false, "", "/work", "")                       // restricted net=off, non-bwrap linux
	f.Add(2, true, "windows", "C:/u", false, false, "", "C:/work", "")                       // workspace-write, windows
	f.Add(1, true, "darwin", "/Users/u", false, false, "/usr/bin/sandbox-exec", "/work", "") // read-only, seatbelt
	f.Add(3, true, "darwin", "/Users/u", false, false, "", "/work", "")                      // restricted, darwin bare
	f.Add(2, true, "relative/home", "linux", true, true, "", "/work", "")                    // bwrap, NON-absolute home → must refuse
	f.Add(2, true, "", "", false, false, "", "", "")                                         // degenerate: empty everything

	f.Fuzz(func(t *testing.T,
		modeSel int, network bool, home, os string,
		bwrapCapable, overlay bool, sandboxExec, cwd, denyRemove string,
	) {
		mode := Mode(((modeSel % len(AllModes())) + len(AllModes())) % len(AllModes()))
		policy := SandboxPolicy{
			Mode:               mode,
			Network:            &network,
			DenylistAdd:        []string{"/extra/secret", "~/.custom"},
			DenylistRemove:     []string{denyRemove, "/proc"},  // /proc removal must be ignored (floor)
			ExtraWritableRoots: []string{"/proc", "/work/sub"}, // absolute (relative entries are refused); /proc must be filtered back out
			ExtraReadRoots:     []string{"/sys/kernel"},        // under a masked pseudo-fs; must be filtered
		}
		host := HostFacts{
			OS: os, Home: home, BwrapCapable: bwrapCapable,
			OverlaySupported: overlay, SandboxExecPath: sandboxExec,
		}

		rp, err := Resolve(policy, host, cwd)
		if err != nil {
			var ref *RefusalError
			if !errors.As(err, &ref) {
				t.Fatalf("Resolve returned a non-RefusalError: %T %v", err, err)
			}
			return
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
