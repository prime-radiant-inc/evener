//go:build serffuzz

package agent

import "testing"

// FuzzJdValidateDelegateGrant drives validateDelegateGrant — the pure grant
// decision lifted out of startDelegate — over arbitrary requested/own
// allowances. Oracles (beyond never-panic):
//   - determinism: the same (requested, own) yields the same decision;
//   - monotone decrement: an accepted grant is strictly less than own;
//   - floor rendering: own <= 1 always renders validRange "0";
//   - range/predicate consistency: for own >= 2 the range's max (own-1) is
//     itself acceptable, and requesting exactly own is always rejected.
func FuzzJdValidateDelegateGrant(f *testing.F) {
	f.Add(1, 2)
	f.Add(2, 2)
	f.Add(0, 1)
	f.Add(-3, 0)
	f.Add(5, 3)

	f.Fuzz(func(t *testing.T, requested, own int) {
		ok, validRange := validateDelegateGrant(requested, own)
		if ok2, vr2 := validateDelegateGrant(requested, own); ok != ok2 || validRange != vr2 {
			t.Fatalf("non-deterministic: (%v,%q) vs (%v,%q)", ok, validRange, ok2, vr2)
		}

		if ok && !(requested < own) {
			t.Fatalf("accepted grant not strictly less than own: requested=%d own=%d", requested, own)
		}

		if own <= 1 && validRange != "0" {
			t.Fatalf("own<=1 must render range %q, got %q", "0", validRange)
		}

		// Requesting exactly own is always rejected (strict-less rule).
		if selfOK, _ := validateDelegateGrant(own, own); selfOK {
			t.Fatalf("requesting own=%d was accepted", own)
		}

		if own >= 2 {
			if maxOK, _ := validateDelegateGrant(own-1, own); !maxOK {
				t.Fatalf("range max own-1=%d rejected for own=%d", own-1, own)
			}
		}
	})
}
