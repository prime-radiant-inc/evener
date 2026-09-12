package requestutil

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestWireFieldEnabledFollowsMaxTokensField(t *testing.T) {
	caps := registry.Caps{
		Fields:         map[string]bool{registry.FieldMaxTokens: false, "unrelated": false},
		MaxTokensField: new("max_completion_tokens"),
	}
	if WireFieldEnabled(caps, "max_completion_tokens") {
		t.Fatal("aliased max-token field reported enabled")
	}
	if !WireFieldEnabled(caps, "max_tokens") {
		t.Fatal("unmatched wire path reported disabled")
	}
}
