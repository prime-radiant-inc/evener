package sandbox_test

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestSeatbeltContractParity re-runs M1's exported contract suite through a
// resolver-equivalent that also generates the SBPL for every resolving darwin
// cell, holding the seatbelt backend to the same contract as bwrap. Beyond
// AssertResolve's own invariants it asserts, for every enforced seatbelt cell,
// that the generator never panics; no GRANTED root is itself a masked path; every
// masked path (except the intentional /dev/fd exemption) is emitted as an
// authoritative MASKED_ read+write deny; and every protected git surface is
// emitted as a PROTECTED_ write deny. Because the denials are trailing and
// unconditional (no per-root gating), a masked path is denied regardless of which
// allow — including the static platform-defaults — might otherwise re-grant it.
// (No-path-interpolation is proven separately by FuzzSeatbeltPolicyNoInterpolation.)
func TestSeatbeltContractParity(t *testing.T) {
	t.Parallel()
	sandboxtest.AssertResolve(t, func(p sandbox.SandboxPolicy, h sandbox.HostFacts, cwd string) (sandbox.ResolvedPolicy, error) {
		rp, err := sandbox.Resolve(p, h, cwd)
		if err != nil || rp.Backend != sandbox.BackendSeatbelt {
			return rp, err
		}
		_, params := sandbox.SeatbeltPolicy(rp, "/evener-session-tmp", nil)

		grantedRoots := map[string]bool{}
		maskedDenied := map[string]bool{}
		protectedDenied := map[string]bool{}
		for _, pr := range params {
			switch {
			case strings.HasPrefix(pr.Key, "MASKED_"):
				maskedDenied[pr.Path] = true
			case strings.HasPrefix(pr.Key, "PROTECTED_"):
				protectedDenied[pr.Path] = true
			case strings.HasPrefix(pr.Key, "WRITABLE_ROOT_"), strings.HasPrefix(pr.Key, "READABLE_ROOT_"):
				grantedRoots[pr.Path] = true
			}
		}
		// No granted root is itself a masked path.
		for _, m := range rp.MaskedPaths {
			if grantedRoots[m] {
				t.Errorf("case %v/net=%v: masked path %q is a granted root", rp.Mode, rp.Network, m)
			}
		}
		// Every masked path is authoritatively denied (except the /dev/fd exemption).
		for _, m := range rp.MaskedPaths {
			if m == "/dev/fd" {
				continue
			}
			if !maskedDenied[m] {
				t.Errorf("case %v/net=%v: masked path %q is not authoritatively denied", rp.Mode, rp.Network, m)
			}
		}
		// Every protected git surface is authoritatively write-denied.
		for _, pp := range rp.Git.ProtectedPaths {
			if !protectedDenied[pp] {
				t.Errorf("case %v/net=%v: protected surface %q is not write-denied", rp.Mode, rp.Network, pp)
			}
		}
		return rp, err
	})
}
