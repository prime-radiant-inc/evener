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
	if WireFieldEnabled(caps, "max_completion_tokens", registry.ProtocolOpenAIChat) {
		t.Fatal("aliased max-token field reported enabled")
	}
	if !WireFieldEnabled(caps, "max_tokens", registry.ProtocolOpenAIChat) {
		t.Fatal("unmatched wire path reported disabled")
	}
}

// TestWireFieldEnabledMaxTokensSpellingIsAChatFact pins that the cap names a
// path only on the protocol that spells the output limit that way. A chat
// listing can advertise max_completion_tokens for a row resolving to anthropic,
// where the finalizer would otherwise read "max_tokens enabled" and restore a
// field the user disabled.
func TestWireFieldEnabledMaxTokensSpellingIsAChatFact(t *testing.T) {
	caps := registry.Caps{
		Fields:         map[string]bool{registry.FieldMaxTokens: false},
		MaxTokensField: new("max_completion_tokens"),
	}
	if WireFieldEnabled(caps, "max_tokens", registry.ProtocolAnthropic) {
		t.Fatal("fields.max_tokens=false must disable max_tokens on the anthropic protocol")
	}
}
