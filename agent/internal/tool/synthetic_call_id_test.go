package tool

import (
	"testing"

	"primeradiant.com/evener/identifier"
)

// SynthesizedCallID must never reissue an id. The predecessor minted
// "call_<argHash>_<n>" from a process-scoped counter, so a resumed session
// whose first unnamed call repeated the previous process's first unnamed call's
// arguments minted the same call_<hash>_1 and collided with an id already
// recorded. A UUIDv7 payload (identifier.NewSyntheticCallID) carries no process
// state to reset, so it cannot collide across restarts. A restart cannot be
// simulated in-process; the closest cheap check is that the minter never
// repeats itself over many calls and that every id is a well-formed synthetic
// call id.
func TestSynthesizedCallID_NeverRepeatsAndValidates(t *testing.T) {
	t.Parallel()
	const n = 2000
	seen := make(map[string]struct{}, n)
	for i := range n {
		id := SynthesizedCallID()
		if err := identifier.ValidateSyntheticCallID(id); err != nil {
			t.Fatalf("SynthesizedCallID() = %q, not a valid synthetic call id: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("SynthesizedCallID() repeated %q after %d calls", id, i)
		}
		seen[id] = struct{}{}
	}
}
