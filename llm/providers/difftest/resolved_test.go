package difftest

import "primeradiant.com/evener/llm/registry"

// resolvedFor is the differential's minimal Resolved record: the protocol's
// default endpoints against the leg's httptest server, no auth, the
// baseline Fields table, and no caps beyond that, so every leg decodes
// the same logical response through the same shaping.
func resolvedFor(protocol, baseURL string) registry.Resolved {
	endpoints := map[string][2]string{
		registry.ProtocolAnthropic:       {"/messages", "/messages"},
		registry.ProtocolGoogle:          {"/models/{model}:generateContent", "/models/{model}:streamGenerateContent?alt=sse"},
		registry.ProtocolOpenAIChat:      {"/chat/completions", "/chat/completions"},
		registry.ProtocolOpenAIResponses: {"/responses", "/responses"},
	}[protocol]
	return registry.Resolved{
		Instance: protocol, Protocol: protocol, ModelID: "test-model", WireID: "test-model",
		Transport: registry.Transport{Auth: registry.AuthNone, BaseURL: baseURL, Endpoint: endpoints[0], StreamEndpoint: endpoints[1], ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported},
		Caps:      registry.Caps{Fields: registry.Baseline(protocol)},
	}
}
