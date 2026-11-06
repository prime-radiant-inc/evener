package hub

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestPickOpenAIVoiceProvider(t *testing.T) {
	instances := []registry.Instance{
		{Name: "openai-codex", ProviderID: "openai-codex", Base: "openai-codex", Protocol: registry.ProtocolOpenAIResponses},
		{Name: "openai", ProviderID: "openai", Base: "openai", Protocol: registry.ProtocolOpenAIResponses},
		{Name: "groq", ProviderID: "groq", Base: "groq", Protocol: registry.ProtocolOpenAIChat},
		{Name: "anthropic", Protocol: registry.ProtocolAnthropic},
	}
	for _, test := range []struct {
		name        string
		named       string
		defaultName string
		want        string
	}{
		{name: "named-wins", named: "groq", defaultName: "openai", want: "groq"},
		{name: "openai-default", defaultName: "openai", want: "openai"},
		// A Codex login speaks the OpenAI protocol but not OpenAI's Realtime endpoint.
		{name: "skips-codex-ranked-first", defaultName: "groq", want: "openai"},
		{name: "openai-compatible-default-falls-through", defaultName: "groq", want: "openai"},
		{name: "non-openai-default-falls-through", defaultName: "anthropic", want: "openai"},
		{name: "no-default-falls-to-openai", want: "openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := pickOpenAIVoiceProvider(test.named, test.defaultName, instances)
			if err != nil || got != test.want {
				t.Fatalf("pick = %q, %v, want %q", got, err, test.want)
			}
		})
	}
	if _, err := pickOpenAIVoiceProvider("", "", []registry.Instance{{Name: "anthropic", Protocol: registry.ProtocolAnthropic}}); err == nil {
		t.Fatal("no OpenAI-transport instance must refuse voice rather than name a non-Realtime provider")
	}
	if _, err := pickOpenAIVoiceProvider("", "groq", []registry.Instance{
		{Name: "groq", ProviderID: "groq", Base: "groq", Protocol: registry.ProtocolOpenAIChat},
	}); err == nil {
		t.Fatal("an OpenAI-compatible instance must not be chosen as the Realtime provider")
	}
}
