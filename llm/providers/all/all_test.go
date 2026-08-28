package all

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
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

// TestRegisteredProtocolsAreThePackageSingletons proves each package
// registered its exported DefaultProtocol, which is the handle a caller or
// test sets Client and Hasher on.
func TestRegisteredProtocolsAreThePackageSingletons(t *testing.T) {
	for _, want := range []llm.Protocol{
		chatcompletions.DefaultProtocol,
		responses.DefaultProtocol,
		anthropic.DefaultProtocol,
		google.DefaultProtocol,
	} {
		got, ok := llm.ProtocolFor(want.ID())
		if !ok {
			t.Errorf("protocol %s not registered", want.ID())
			continue
		}
		if got != want {
			t.Errorf("protocol %s registered %p, want the package DefaultProtocol %p", want.ID(), got, want)
		}
	}
}
