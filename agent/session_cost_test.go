package agent

import (
	"testing"
)

// TestSessionCostFor_ResolvesTheRowsCost pins where a session's dollar figures
// come from (spec §7.5): the cost on the row its own registry resolves the
// instance/model reference to, not a bundled pricing table keyed on the model
// id alone.
func TestSessionCostFor_ResolvesTheRowsCost(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withClient(registryClient(t, nil)))

	cost := sess.CostFor("anthropic/claude-opus-4-5")
	if cost == nil {
		t.Fatal("CostFor(anthropic/claude-opus-4-5) = nil, want the curated row's cost")
	}
	if cost.Input <= 0 || cost.Output <= 0 {
		t.Fatalf("CostFor = %+v, want positive input and output rates", cost)
	}

	res, err := sess.Client().Resolve("anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cost != res.Caps.Cost {
		t.Fatalf("CostFor = %+v, want the resolved row's own cost %+v", cost, res.Caps.Cost)
	}
}

// TestSessionCostFor_UnpricedReferenceIsNil pins the honest answer for a
// reference the session's registry cannot price — one it refuses outright, and
// one it synthesizes a row for from provider-level caps (spec §7.3), which
// carries no cost. Either way the caller renders nothing, never a fabricated
// zero.
func TestSessionCostFor_UnpricedReferenceIsNil(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withClient(registryClient(t, nil)))

	for _, ref := range []string{
		"no-such-instance/whatever", // unknown instance: an error
		"",                          // empty reference: an error
		"anthropic/totally-unknown", // synthesized row, no cost
		"/claude-opus-4-5",          // a pre-registry meta with no instance
	} {
		if cost := sess.CostFor(ref); cost != nil {
			t.Errorf("CostFor(%q) = %+v, want nil", ref, cost)
		}
	}
}
