package google

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// liveInteractionsDefaultModel is the model the live suite drives unless
// EVENER_LIVE_GOOGLE_MODEL names another one.
const liveInteractionsDefaultModel = "gemini-2.5-flash"

type liveInteractionsTarget struct {
	name     string
	instance string
}

// liveInteractionsTargets is the surface matrix both live suites cover: the
// Gemini Developer API and Vertex AI's global location.
func liveInteractionsTargets() []liveInteractionsTarget {
	return []liveInteractionsTarget{
		{name: "google", instance: "google"},
		{name: "google-vertex-global", instance: "google-vertex"},
	}
}

// skipUnlessLiveInteractions is the first statement of every live test: the
// default offline suite must issue no requests even when a key is exported.
func skipUnlessLiveInteractions(t *testing.T, instance string) {
	t.Helper()
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live Google Interactions suite")
	}
	switch instance {
	case "google":
		if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
			t.Skip("set GEMINI_API_KEY or GOOGLE_API_KEY to run the live google surface")
		}
	case "google-vertex":
		if os.Getenv("GOOGLE_VERTEX_PROJECT") == "" {
			t.Skip("set GOOGLE_VERTEX_PROJECT to run the live google-vertex surface")
		}
		if loc := os.Getenv("GOOGLE_VERTEX_LOCATION"); loc != "global" {
			t.Skipf("live google-vertex coverage requires GOOGLE_VERTEX_LOCATION=global, got %q", loc)
		}
	default:
		t.Fatalf("unknown live interactions instance %q", instance)
	}
}

func liveInteractionsModel() string {
	if model := strings.TrimSpace(os.Getenv("EVENER_LIVE_GOOGLE_MODEL")); model != "" {
		return model
	}
	return liveInteractionsDefaultModel
}

// resolveLiveInteractions resolves the surface through the real registry, so
// the developer's environment and user layer name endpoints and credentials
// exactly as a real run does.
func resolveLiveInteractions(t *testing.T, instance string) registry.Resolved {
	t.Helper()
	r, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	res, err := r.Resolve(instance + "/" + liveInteractionsModel())
	if err != nil {
		t.Fatalf("resolve %s: %v", instance, err)
	}
	return res
}

func liveInteractionsPingTool() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "ping",
		Description: "Return the supplied value unchanged.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []any{"value"},
		},
	}
}

func liveInteractionsToolCall(message llm.Message, name string) *llm.ToolCallData {
	for _, part := range message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.Name == name {
			return part.ToolCall
		}
	}
	return nil
}

func liveInteractionsSummary(message llm.Message) string {
	kinds := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		kinds = append(kinds, string(part.Kind))
	}
	return strings.Join(kinds, ",")
}

func liveInteractionsStreamText(stream llm.Stream) (string, error) {
	var text strings.Builder
	var streamErr error
	for event := range stream.Events() {
		switch event.Type {
		case llm.StreamEventTextDelta:
			text.WriteString(event.Delta)
		case llm.StreamEventError:
			streamErr = event.Err
		}
	}
	if err := stream.Close(); err != nil && streamErr == nil {
		streamErr = err
	}
	return text.String(), streamErr
}

func TestLiveInteractionsCompleteAndStreamStatelessFunctionRound(t *testing.T) {
	for _, target := range liveInteractionsTargets() {
		t.Run(target.name, func(t *testing.T) {
			skipUnlessLiveInteractions(t, target.instance)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			res := resolveLiveInteractions(t, target.instance)
			p := &Protocol{Client: http.DefaultClient}
			tools := []llm.ToolDefinition{liveInteractionsPingTool()}

			turn1 := llm.Request{
				Model:    res.WireID,
				Tools:    tools,
				Messages: []llm.Message{llm.User(`Call the ping tool with value "7". Do not reply with text.`)},
			}
			resp1, err := p.Complete(ctx, turn1, res)
			if err != nil {
				t.Fatalf("Complete turn 1: %v", err)
			}
			call := liveInteractionsToolCall(resp1.Message, "ping")
			if call == nil {
				t.Fatalf("turn 1 did not call ping (content kinds: %s)", liveInteractionsSummary(resp1.Message))
			}

			turn2 := llm.Request{
				Model: res.WireID,
				Tools: tools,
				Messages: []llm.Message{
					turn1.Messages[0],
					resp1.Message,
					llm.ToolResultNamed(call.ID, call.Name, "7", false),
					llm.User("Reply with the single word done."),
				},
			}
			stream, err := p.Stream(ctx, turn2, res)
			if err != nil {
				t.Fatalf("Stream turn 2: %v", err)
			}
			text, err := liveInteractionsStreamText(stream)
			if err != nil {
				t.Fatalf("Stream turn 2 events: %v", err)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatal("Stream turn 2 produced no text")
			}
		})
	}
}

func TestLiveInteractionsSettlesLegacySignedToolCallHistory(t *testing.T) {
	for _, target := range liveInteractionsTargets() {
		t.Run(target.name, func(t *testing.T) {
			skipUnlessLiveInteractions(t, target.instance)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			res := resolveLiveInteractions(t, target.instance)
			p := &Protocol{Client: http.DefaultClient}
			req := llm.Request{
				Model: res.WireID,
				Messages: []llm.Message{
					llm.User("Check the file, then answer."),
					{Role: llm.RoleAssistant, Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:               "call_read",
							Name:             "read_file",
							Arguments:        json.RawMessage(`{"path":"a.txt"}`),
							ThoughtSignature: "legacy_sig",
						}},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:        "call_old_search",
							Name:      "web_search",
							Arguments: json.RawMessage(`{"query":"legacy"}`),
						}},
					}},
					llm.ToolResultNamed("call_old_search", "web_search", "legacy search result", false),
					llm.ToolResultNamed("call_read", "read_file", "file body", false),
					llm.User("Reply with the single word done."),
				},
			}
			resp, err := p.Complete(ctx, req, res)
			if err != nil {
				t.Fatalf("Complete over legacy-signed history: %v", err)
			}
			if strings.TrimSpace(resp.Text()) == "" {
				t.Fatal("Complete over legacy-signed history produced no text")
			}
		})
	}
}

func TestLiveInteractionsStatefulCombinedSearchAndFunctionRound(t *testing.T) {
	for _, target := range liveInteractionsTargets() {
		t.Run(target.name, func(t *testing.T) {
			skipUnlessLiveInteractions(t, target.instance)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			res := resolveLiveInteractions(t, target.instance)
			// The web_search = true test config layer: combined google_search
			// and function tools are opt-in through these caps.
			enabled := true
			res.Caps.WebSearch = &enabled
			res.Caps.CombinedSearchAndFunctions = &enabled
			store := true
			p := &Protocol{Client: http.DefaultClient}
			tools := []llm.ToolDefinition{liveInteractionsPingTool()}

			turn1 := llm.Request{
				Model:     res.WireID,
				Store:     &store,
				WebSearch: true,
				Tools:     tools,
				Messages: []llm.Message{
					llm.User(`Search the web for today's UTC date, then call ping with that date as "value". Use both tools.`),
				},
			}
			resp1, err := p.Complete(ctx, turn1, res)
			if err != nil {
				t.Fatalf("Complete combined turn 1: %v", err)
			}
			var sawSearch bool
			for _, part := range resp1.Message.Content {
				if part.Kind == llm.ContentWebSearch {
					sawSearch = true
				}
			}
			if !sawSearch {
				t.Fatalf("turn 1 emitted no google_search result (content kinds: %s)", liveInteractionsSummary(resp1.Message))
			}
			call := liveInteractionsToolCall(resp1.Message, "ping")
			if call == nil {
				t.Fatalf("turn 1 emitted no ping function_call (content kinds: %s)", liveInteractionsSummary(resp1.Message))
			}
			if strings.TrimSpace(resp1.ID) == "" {
				t.Fatal("turn 1 returned no interaction id to continue from")
			}

			turn2 := llm.Request{
				Model:              res.WireID,
				Store:              &store,
				WebSearch:          true,
				Tools:              tools,
				PreviousResponseID: resp1.ID,
				Messages: []llm.Message{
					llm.ToolResultNamed(call.ID, call.Name, "2026-11-21", false),
					llm.User("Reply with the single word done."),
				},
			}
			resp2, err := p.Complete(ctx, turn2, res)
			if err != nil {
				t.Fatalf("Complete combined turn 2: %v", err)
			}
			if strings.TrimSpace(resp2.Text()) == "" {
				t.Fatal("combined turn 2 produced no text")
			}
		})
	}
}
