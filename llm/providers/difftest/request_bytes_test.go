package difftest_test

// This file extends difftest's cross-provider oracle to the REQUEST side: it
// builds a body with each real protocol builder and asserts that the bytes
// Prepare puts on the wire are the bytes encoding/json produces from the same
// final body. That is #6409's wire-byte contract, checked against the four
// builders the framework actually ships rather than against hand-written
// fixture maps.
//
// It lives in the external test package so it can import protocolhttp and the
// four protocols at once: protocolhttp is internal to llm/providers, and the
// protocols import it, so an in-package difftest test importing both would be
// the same graph. As an external test it compiles into the difftest test
// binary only.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// requestByteLeg is one protocol's real builder plus the resolved row it needs.
type requestByteLeg struct {
	name     string
	protocol llm.Protocol
	res      registry.Resolved
}

// requestByteLegs returns the four shipping protocols with a resolved row whose
// endpoints and baseline field table are the same ones the differential uses.
func requestByteLegs() []requestByteLeg {
	endpoints := map[string][2]string{
		registry.ProtocolAnthropic:       {"/messages", "/messages"},
		registry.ProtocolGoogle:          {"/models/{model}:generateContent", "/models/{model}:streamGenerateContent?alt=sse"},
		registry.ProtocolOpenAIChat:      {"/chat/completions", "/chat/completions"},
		registry.ProtocolOpenAIResponses: {"/responses", "/responses"},
	}
	leg := func(name string, protocol llm.Protocol) requestByteLeg {
		ep := endpoints[name]
		return requestByteLeg{name: name, protocol: protocol, res: registry.Resolved{
			Instance: name, Protocol: name, ModelID: "test-model", WireID: "test-model",
			Transport: registry.Transport{Auth: registry.AuthNone, BaseURL: "https://api.example.test", Endpoint: ep[0], StreamEndpoint: ep[1], ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported},
			Caps:      registry.Caps{Fields: registry.Baseline(name)},
		}}
	}
	return []requestByteLeg{
		leg(registry.ProtocolAnthropic, &anthropic.Protocol{}),
		leg(registry.ProtocolGoogle, &google.Protocol{}),
		leg(registry.ProtocolOpenAIChat, &chatcompletions.Protocol{}),
		leg(registry.ProtocolOpenAIResponses, &responses.Protocol{}),
	}
}

// requestByteHistory builds a 60-turn request with the escapes, tool results,
// and numbers a real session carries, so each builder exercises its message
// conversion rather than an empty list.
func requestByteHistory() llm.Request {
	var toolResult strings.Builder
	toolResult.WriteString("line one with <html> & \"quotes\"\n")
	for i := range 40 {
		fmt.Fprintf(&toolResult, "line %d\ttabbed 日本語 \u2028 more\n", i)
	}
	var msgs []llm.Message
	for i := range 60 {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: fmt.Sprintf("run grep %d & <run>", i)}}},
			llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("toolu_%d", i), Name: "grep", Arguments: json.RawMessage(fmt.Sprintf(`{"pattern":"func main %d"}`, i))},
			}}},
			llm.ToolResult(fmt.Sprintf("toolu_%d", i), toolResult.String(), false),
		)
	}
	effort := "medium"
	temperature := 0.3
	return llm.Request{
		Model: "test-model", Messages: msgs, MaxTokens: new(32000), ReasoningEffort: &effort, Temperature: &temperature,
		Tools: []llm.ToolDefinition{{
			Name: "grep", Description: "search <files> & dirs",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string", "description": "regex <&>"}}},
		}},
		Metadata: map[string]string{"user_id": "u-1"},
	}
}

// TestBuildBodyBytesMatchMarshal is the request-side wire contract across all
// four protocols: every real builder's body, encoded by protocolhttp's
// encoder, must equal encoding/json's bytes for the same body.
func TestBuildBodyBytesMatchMarshal(t *testing.T) {
	t.Parallel()
	req := requestByteHistory()
	for _, leg := range requestByteLegs() {
		t.Run(leg.name, func(t *testing.T) {
			t.Parallel()
			body, err := leg.protocol.BuildBody(llm.ShapeRequest(req, leg.res), leg.res)
			if err != nil {
				t.Fatalf("BuildBody: %v", err)
			}
			want, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("stdlib marshal: %v", err)
			}
			got, err := protocolhttp.MarshalBody(body)
			if err != nil {
				t.Fatalf("MarshalBody: %v", err)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("encoded bytes differ from encoding/json for %s\nwant %s\ngot  %s", leg.name, want, got)
			}
			// And the same through Prepare, which is what actually goes on the
			// wire after prune, constants, auth and the request preparer.
			call := &protocolhttp.Call{Operation: "test", Method: http.MethodPost, URL: protocolhttp.URL(leg.res, leg.res.Transport.Endpoint), Body: body, Req: req, Res: leg.res}
			p, err := protocolhttp.Prepare(context.Background(), call)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			wireWant, err := json.Marshal(call.Body)
			if err != nil {
				t.Fatalf("stdlib marshal of the wire body: %v", err)
			}
			if !bytes.Equal(p.Body, wireWant) {
				t.Fatalf("wire bytes differ from encoding/json for %s\nwant %s\ngot  %s", leg.name, wireWant, p.Body)
			}
		})
	}
}
