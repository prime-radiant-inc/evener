package responses

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// An empty ladder vouches for no level, so the Responses reasoning object must
// not spell one. This is the same bug class as the lunaroute 400 on the
// chat-completions side. ReasoningControls must list "effort" so the row is
// effort-capable and the vouch gate actually runs.
func TestReasoningObject_EmptyLadderWritesNoEffort(t *testing.T) {
	high := "high"
	caps := registry.Caps{Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if got := reasoningObject(llm.Request{ReasoningEffort: &high}, caps); got != nil {
		t.Fatalf("empty-ladder row got reasoning = %v, want nil", got)
	}
}

// A vouched level is clamped into the row's ladder and written by name.
func TestReasoningObject_VouchedEffortClamps(t *testing.T) {
	xhigh := "xhigh"
	got := reasoningObject(llm.Request{ReasoningEffort: &xhigh}, registry.Caps{
		Reasoning: new(true), ReasoningControls: []string{"effort"}, EffortValues: []string{"low", "high"},
	})
	if got == nil || got["effort"] != "high" {
		t.Fatalf("reasoning = %v, want effort high", got)
	}
}
