package hub

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestPickOpenAIVoiceProvider(t *testing.T) {
	instances := []registry.Instance{
		{Name: "anthropic", Protocol: registry.ProtocolAnthropic},
		{Name: "gateway", Protocol: registry.ProtocolOpenAIResponses},
		{Name: "groq", Protocol: registry.ProtocolOpenAIChat},
	}
	for _, test := range []struct {
		name        string
		named       string
		defaultName string
		want        string
	}{
		{name: "named-wins", named: "groq", defaultName: "gateway", want: "groq"},
		{name: "default-is-openai", defaultName: "gateway", want: "gateway"},
		{name: "default-not-openai-falls-to-first-openai", defaultName: "anthropic", want: "gateway"},
		{name: "no-default-falls-to-first-openai", want: "gateway"},
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
}
