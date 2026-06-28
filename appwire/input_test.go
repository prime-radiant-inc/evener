package appwire

import "testing"

// TestExpectedTurnIDUsed verifies that EffectiveTurnID returns ExpectedTurnID
// rather than any other field such as Ref. Both fields are populated with
// distinct values so the assertion distinguishes which field is actually read.
func TestExpectedTurnIDUsed(t *testing.T) {
	steer := TurnSteerParams{ExpectedTurnID: "turn_codex", Ref: "turn_ref"}
	if got := steer.EffectiveTurnID(); got != "turn_codex" {
		t.Fatalf("steer EffectiveTurnID=%q, want %q", got, "turn_codex")
	}

	interrupt := TurnInterruptParams{ExpectedTurnID: "turn_codex", Ref: "turn_ref"}
	if got := interrupt.EffectiveTurnID(); got != "turn_codex" {
		t.Fatalf("interrupt EffectiveTurnID=%q, want %q", got, "turn_codex")
	}
}
