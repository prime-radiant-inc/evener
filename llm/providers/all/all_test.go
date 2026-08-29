package all

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestEveryProtocolAndSchemeIsRegistered(t *testing.T) {
	for _, id := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses, registry.ProtocolAnthropic, registry.ProtocolGoogle} {
		if _, ok := llm.ProtocolFor(id); !ok {
			t.Errorf("protocol %s not registered", id)
		}
	}
	for _, scheme := range []string{registry.AuthBearer, registry.AuthOptionalBearer, registry.AuthHeader, registry.AuthNone, registry.AuthGCPADC, registry.AuthOAuthOpenAICodex} {
		if _, ok := llm.AuthenticatorFor(scheme); !ok {
			t.Errorf("scheme %s not registered", scheme)
		}
	}
}
