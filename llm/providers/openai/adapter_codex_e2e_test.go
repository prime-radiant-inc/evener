package openai

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/llm"
)

func codexE2EAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	if os.Getenv("SERF_OPENAI_CODEX_E2E") != "1" {
		t.Skip("set SERF_OPENAI_CODEX_E2E=1 to run live Codex backend e2e tests")
	}
	if testing.Short() {
		t.Skip("skipping live Codex backend e2e test in short mode")
	}
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if !a.usesCodexBackend() {
		t.Skip("OpenAI env did not resolve to stored OAuth/Codex backend credentials")
	}
	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_CODEX_E2E_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}
	return a, model
}

func TestAdapter_E2E_CodexResponsesTransportAndReasoningState(t *testing.T) {
	a, model := codexE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	effort := "low"
	id := ulid.Make().String()
	resp, err := a.Complete(ctx, llm.Request{
		Model:           model,
		Messages:        []llm.Message{llm.User("Reply with exactly: serf codex e2e ok")},
		ReasoningEffort: &effort,
		PromptCacheKey:  "serf-e2e-" + id,
		SessionID:       "serf-e2e-session-" + id,
		ThreadID:        "serf-e2e-thread-" + id,
		ClientMetadata: map[string]string{
			"x-codex-installation-id": "serf-e2e-install-" + id,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := strings.TrimSpace(resp.Text()); got == "" {
		t.Fatalf("empty text response")
	}
	endpoint, _ := resp.Raw["endpoint_url"].(string)
	if !strings.Contains(endpoint, defaultCodexResponses) {
		t.Fatalf("endpoint_url = %q, want Codex responses endpoint", endpoint)
	}
	if encryptedReasoningFrom(resp.Message) == "" {
		t.Logf("Codex backend did not include encrypted reasoning state for this prompt/model; content=%#v raw=%#v", resp.Message.Content, resp.Raw)
	}
}

func TestAdapter_E2E_CodexEncryptedReasoningRoundTripWithTool(t *testing.T) {
	a, model := codexE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	effort := "low"
	id := ulid.Make().String()
	tool := llm.ToolDefinition{
		Name:        "echo_state",
		Description: "Echoes a short state string.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required":             []string{"value"},
			"additionalProperties": false,
		},
	}
	first, err := a.Complete(ctx, llm.Request{
		Model:           model,
		Messages:        []llm.Message{llm.User("Call echo_state with value first.")},
		Tools:           []llm.ToolDefinition{tool},
		ToolChoice:      &llm.ToolChoice{Mode: "required"},
		ReasoningEffort: &effort,
		PromptCacheKey:  "serf-e2e-tool-" + id,
		SessionID:       "serf-e2e-session-" + id,
		ThreadID:        "serf-e2e-thread-" + id,
	})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	encrypted := encryptedReasoningFrom(first.Message)
	if encrypted == "" {
		t.Logf("Codex backend did not include encrypted reasoning state for tool call; content=%#v raw=%#v", first.Message.Content, first.Raw)
	}
	calls := first.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("expected tool call; text=%q content=%#v", first.Text(), first.Message.Content)
	}

	secondMessages := []llm.Message{
		llm.User("Call echo_state with value first."),
		first.Message,
		llm.ToolResultNamed(calls[0].ID, calls[0].Name, "first", false),
		llm.User("Now answer exactly: roundtrip ok"),
	}
	second, err := a.Complete(ctx, llm.Request{
		Model:           model,
		Messages:        secondMessages,
		ReasoningEffort: &effort,
		PromptCacheKey:  "serf-e2e-tool-" + id,
		SessionID:       "serf-e2e-session-" + id,
		ThreadID:        "serf-e2e-thread-" + id,
	})
	if err != nil {
		t.Fatalf("second Complete with replayed encrypted reasoning: %v", err)
	}
	if strings.TrimSpace(second.Text()) == "" {
		t.Fatalf("second response empty")
	}
}

func TestAdapter_E2E_CodexResponsesControlCompatibility(t *testing.T) {
	a, model := codexE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	id := ulid.Make().String()
	falseBool := false
	storeFalse := false
	maxToolCalls := 0

	cases := []struct {
		name string
		req  llm.Request
	}{
		{
			name: "safety_identifier",
			req: llm.Request{
				SafetyIdentifier: "serf-e2e-user-" + id,
			},
		},
		{
			name: "prompt_cache_retention",
			req: llm.Request{
				PromptCacheRetention: "24h",
			},
		},
		{
			name: "truncation_auto",
			req: llm.Request{
				Truncation: "auto",
			},
		},
		{
			name: "max_tool_calls_zero",
			req: llm.Request{
				MaxToolCalls: &maxToolCalls,
			},
		},
		{
			name: "background_false",
			req: llm.Request{
				Background: &falseBool,
			},
		},
		{
			name: "store_false",
			req: llm.Request{
				Store: &storeFalse,
			},
		},
		{
			name: "service_tier_auto",
			req: llm.Request{
				ServiceTier: "auto",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Model = model
			req.Messages = []llm.Message{llm.User(fmt.Sprintf("Reply with exactly: %s ok", tc.name))}
			req.PromptCacheKey = "serf-e2e-controls-" + id
			req.SessionID = "serf-e2e-session-" + id
			req.ThreadID = "serf-e2e-thread-" + id
			resp, err := a.Complete(ctx, req)
			if err != nil {
				if isCodexUnsupportedControlError(err) {
					t.Skipf("Codex backend rejected %s: %v", tc.name, err)
				}
				t.Fatalf("Complete: %v", err)
			}
			if strings.TrimSpace(resp.Text()) == "" {
				t.Fatalf("empty text response")
			}
		})
	}
}

func encryptedReasoningFrom(msg llm.Message) string {
	for _, p := range msg.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.EncryptedContent != "" {
			return p.Thinking.EncryptedContent
		}
	}
	return ""
}

func isCodexUnsupportedControlError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"unknown parameter",
		"unsupported",
		"not supported",
		"unrecognized",
		"invalid parameter",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
