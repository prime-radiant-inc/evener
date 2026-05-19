package appwire

import "testing"

func TestTurnStartParamsPreferCodexInput(t *testing.T) {
	params := TurnStartParams{
		Input: []InputItem{{Type: "text", Text: "codex"}},
	}
	got := params.EffectiveInput()
	if len(got) != 1 || got[0].Text != "codex" {
		t.Fatalf("EffectiveInput=%+v, want codex input", got)
	}
}

func TestExpectedTurnIDFallback(t *testing.T) {
	steer := TurnSteerParams{ExpectedTurnID: "turn_codex"}
	if got := steer.EffectiveTurnID(); got != "turn_codex" {
		t.Fatalf("steer EffectiveTurnID=%q", got)
	}

	interrupt := TurnInterruptParams{ExpectedTurnID: "turn_codex"}
	if got := interrupt.EffectiveTurnID(); got != "turn_codex" {
		t.Fatalf("interrupt EffectiveTurnID=%q", got)
	}
}
