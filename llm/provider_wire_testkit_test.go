package llm_test

import (
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// Shared fixture for the core wire tests (outcomes, client timeouts, the wall
// ceiling, redirects, and wire-capture provenance): one table of protocol legs,
// each an injected registry instance, and the client builder that points a
// leg's protocol at a test server.

// wireProvider is one protocol leg of the core wire tests: the registry
// instance that points a protocol at the test server, plus the response
// bodies that protocol speaks.
type wireProvider struct {
	name         string
	protocol     string
	base         string
	completeBody string
	streamBody   string
	streamPrefix string
}

// wireProviders covers every protocol the registry can dispatch: the two
// OpenAI wire shapes under distinct instance names (the openai id speaks
// Responses; a renamed instance speaks Chat Completions), plus Anthropic and
// Google.
func wireProviders() []wireProvider {
	return []wireProvider{
		{
			name:         "openai",
			protocol:     registry.ProtocolOpenAIResponses,
			base:         "openai",
			completeBody: `{"id":"resp-1","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			streamBody: strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"hello"}`,
				``,
				`event: response.completed`,
				`data: {"type":"response.completed","response":{"id":"resp-1","model":"test-model","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"partial"}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "anthropic",
			protocol:     registry.ProtocolAnthropic,
			base:         "anthropic",
			completeBody: `{"id":"msg_1","model":"test-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			streamBody: strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"test-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				``,
				`event: content_block_start`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				``,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				``,
				`event: content_block_stop`,
				`data: {"type":"content_block_stop","index":0}`,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				``,
				`event: message_stop`,
				`data: {"type":"message_stop"}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "google",
			protocol:     registry.ProtocolGoogle,
			base:         "google",
			completeBody: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"test-model"}`,
			streamBody: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "work",
			protocol:     registry.ProtocolOpenAIChat,
			base:         "openai",
			completeBody: `{"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			streamBody: strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`,
				``,
				``,
			}, "\n"),
		},
	}
}

// instance is the registry record that points this leg at baseURL, with the
// default headers the redirect tests assert on.
func (p wireProvider) instance(baseURL string, headers map[string]string) registry.Provider {
	return registry.Provider{
		Base:      p.base,
		Protocol:  p.protocol,
		APIKey:    "test-key",
		Headers:   headers,
		Transport: registry.Transport{BaseURL: baseURL},
	}
}

// wireClient builds a hermetic registry client serving this leg at baseURL and
// points the leg's protocol at httpClient for the test's duration. The
// protocols are process singletons, so no test in this file may run in
// parallel.
func (p wireProvider) wireClient(t *testing.T, baseURL string, httpClient *http.Client, headers map[string]string) *llm.Client {
	t.Helper()
	useWireHTTPClient(t, p.protocol, httpClient)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{p.name: p.instance(baseURL, headers)}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

// useWireHTTPClient swaps a protocol's HTTP client for the test's duration,
// the seam the redirect and timeout cases need to observe the transport.
func useWireHTTPClient(t *testing.T, protocol string, c *http.Client) {
	t.Helper()
	var restore func()
	switch protocol {
	case registry.ProtocolOpenAIResponses:
		prev := responses.DefaultProtocol.Client
		responses.DefaultProtocol.Client = c
		restore = func() { responses.DefaultProtocol.Client = prev }
	case registry.ProtocolOpenAIChat:
		prev := chatcompletions.DefaultProtocol.Client
		chatcompletions.DefaultProtocol.Client = c
		restore = func() { chatcompletions.DefaultProtocol.Client = prev }
	case registry.ProtocolAnthropic:
		prev := anthropic.DefaultProtocol.Client
		anthropic.DefaultProtocol.Client = c
		restore = func() { anthropic.DefaultProtocol.Client = prev }
	case registry.ProtocolGoogle:
		prev := google.DefaultProtocol.Client
		google.DefaultProtocol.Client = c
		restore = func() { google.DefaultProtocol.Client = prev }
	default:
		t.Fatalf("no protocol client seam for %q", protocol)
	}
	t.Cleanup(restore)
}

// providerRequest is the minimal user request the wire tests dispatch.
func providerRequest(provider, model string) llm.Request {
	return llm.Request{
		Provider: provider,
		Model:    model,
		Messages: []llm.Message{llm.User("hi")},
	}
}
