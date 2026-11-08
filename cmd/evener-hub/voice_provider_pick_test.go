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
		defaultName string
		want        string
	}{
		{name: "openai-default", defaultName: "openai", want: "openai"},
		// A Codex login speaks the OpenAI protocol but not OpenAI's Realtime endpoint.
		{name: "skips-codex-default", defaultName: "openai-codex", want: "openai"},
		{name: "openai-compatible-default-falls-through", defaultName: "groq", want: "openai"},
		{name: "non-openai-default-falls-through", defaultName: "anthropic", want: "openai"},
		{name: "no-default-falls-to-openai", want: "openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := pickOpenAIVoiceProvider(test.defaultName, instances)
			if err != nil || got != test.want {
				t.Fatalf("pick = %q, %v, want %q", got, err, test.want)
			}
		})
	}
	if _, err := pickOpenAIVoiceProvider("", []registry.Instance{{Name: "anthropic", Protocol: registry.ProtocolAnthropic}}); err == nil {
		t.Fatal("no OpenAI-transport instance must refuse voice rather than name a non-Realtime provider")
	}
	if _, err := pickOpenAIVoiceProvider("groq", []registry.Instance{
		{Name: "groq", ProviderID: "groq", Base: "groq", Protocol: registry.ProtocolOpenAIChat},
	}); err == nil {
		t.Fatal("an OpenAI-compatible instance must not be chosen as the Realtime provider")
	}
	// An instance whose base is named "openai" but whose endpoint is a gateway
	// (Lunaroute, Groq's OpenAI shim) has no Realtime endpoint; first-party
	// OpenAI wins even when the gateway is the hub's default.
	gateway := registry.Instance{
		Name: "lunaroute", ProviderID: "lunaroute", Base: "openai", Protocol: registry.ProtocolOpenAIResponses,
		BaseURL: "https://gw.lunaroute.com/v1",
	}
	firstParty := registry.Instance{
		Name: "openai", ProviderID: "openai", Base: "openai", Protocol: registry.ProtocolOpenAIResponses,
		BaseURL: "https://api.openai.com/v1",
	}
	if got, err := pickOpenAIVoiceProvider("lunaroute", []registry.Instance{gateway, firstParty}); err != nil || got != "openai" {
		t.Fatalf("gateway default pick = %q, %v, want openai", got, err)
	}
	if _, err := pickOpenAIVoiceProvider("lunaroute", []registry.Instance{gateway}); err == nil {
		t.Fatal("an OpenAI-protocol gateway alone must not be chosen as the Realtime provider")
	}
	// An explicitly configured OpenAI instance with no credential cannot reach
	// the Realtime endpoint, so it must not make voice look available.
	credentialless := registry.Instance{
		Name: "openai", ProviderID: "openai", Base: "openai", Protocol: registry.ProtocolOpenAIResponses,
		BaseURL: "https://api.openai.com/v1", Auth: registry.AuthBearer, CredentialSource: "none",
	}
	if _, err := pickOpenAIVoiceProvider("openai", []registry.Instance{credentialless}); err == nil {
		t.Fatal("a credentialless OpenAI instance must not be chosen as the Realtime provider")
	}
	credentialed := credentialless
	credentialed.Name, credentialed.ProviderID, credentialed.CredentialSource = "house", "openai", "api_key"
	if got, err := pickOpenAIVoiceProvider("openai", []registry.Instance{credentialless, credentialed}); err != nil || got != "house" {
		t.Fatalf("credentialless default pick = %q, %v, want the credentialed instance", got, err)
	}
}
