package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func anthropicE2EAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	if os.Getenv("SERF_ANTHROPIC_E2E") != "1" {
		t.Skip("set SERF_ANTHROPIC_E2E=1 to run live Anthropic e2e tests")
	}
	if testing.Short() {
		t.Skip("skipping live Anthropic e2e test in short mode")
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	model := strings.TrimSpace(os.Getenv("SERF_ANTHROPIC_E2E_MODEL"))
	if model == "" {
		model = "claude-sonnet-4-5-20250929"
	}
	return a, model
}

func TestAdapter_E2E_AnthropicTransportServiceTierAndCaching(t *testing.T) {
	a, model := anthropicE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:       model,
		Messages:    []llm.Message{llm.User("Reply with exactly: serf anthropic e2e ok")},
		ServiceTier: "standard_only",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) == "" {
		t.Fatalf("empty text response")
	}
	endpoint, _ := resp.Raw["endpoint_url"].(string)
	if !strings.HasSuffix(endpoint, "/v1/messages") {
		t.Fatalf("endpoint_url = %q, want /v1/messages", endpoint)
	}
	serviceTier, _ := resp.Usage.Raw["service_tier"].(string)
	if serviceTier == "" {
		t.Logf("Anthropic response usage did not include service_tier; usage=%#v", resp.Usage.Raw)
	}
	if resp.Usage.CacheWriteTokens == nil && resp.Usage.CacheWrite1hTokens == nil && resp.Usage.CacheReadTokens == nil {
		t.Logf("Anthropic response did not report cache activity for short prompt; usage=%#v", resp.Usage.Raw)
	}
}

func TestAdapter_E2E_AnthropicCountInputTokens(t *testing.T) {
	a, model := anthropicE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.User("Count this short Serf token-counting prompt.")},
	}
	got, err := a.CountInputTokens(ctx, req)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if !got.Exact {
		t.Fatalf("Exact = false, want true")
	}
	if got.Source != llm.TokenCountSourceProvider {
		t.Fatalf("Source = %q, want %q", got.Source, llm.TokenCountSourceProvider)
	}
	if got.Tokens <= 0 {
		t.Fatalf("Tokens = %d, want positive", got.Tokens)
	}
	if got.Provider != a.Name() || got.Model != model {
		t.Fatalf("provider/model = %q/%q, want %q/%q", got.Provider, got.Model, a.Name(), model)
	}
	if got.Raw == nil || got.Raw["input_tokens"] == nil {
		t.Fatalf("raw response missing input_tokens: %#v", got.Raw)
	}
	t.Logf("anthropic count tokens: provider=%s model=%s tokens=%d", got.Provider, got.Model, got.Tokens)
}

func TestAdapter_E2E_AnthropicThinkingAndRoundTrip(t *testing.T) {
	a, model := anthropicE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	effort := "low"
	first, err := a.Complete(ctx, llm.Request{
		Model:           model,
		Messages:        []llm.Message{llm.User("Think briefly, then reply with exactly: first ok")},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	thinking := firstThinking(first.Message)
	if thinking == nil {
		t.Logf("Anthropic response did not include a thinking block for this prompt/model; content=%#v raw=%#v", first.Message.Content, first.Raw)
		return
	}
	if thinking.Signature == "" {
		t.Logf("Anthropic thinking block did not include a signature; thinking=%#v raw=%#v", thinking, first.Raw)
	}

	second, err := a.Complete(ctx, llm.Request{
		Model: model,
		Messages: []llm.Message{
			llm.User("Think briefly, then reply with exactly: first ok"),
			first.Message,
			llm.User("Now reply with exactly: thinking roundtrip ok"),
		},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("second Complete with replayed thinking: %v", err)
	}
	if strings.TrimSpace(second.Text()) == "" {
		t.Fatalf("second response empty")
	}
}

func TestAdapter_E2E_AnthropicToolRoundTrip(t *testing.T) {
	a, model := anthropicE2EAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

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
		Model:      model,
		Messages:   []llm.Message{llm.User("Call echo_state with value first.")},
		Tools:      []llm.ToolDefinition{tool},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
	})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	calls := first.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("expected tool call; text=%q content=%#v", first.Text(), first.Message.Content)
	}
	var args map[string]any
	_ = json.Unmarshal(calls[0].Arguments, &args)
	if args["value"] == nil {
		t.Logf("tool call did not include value arg; args=%s", string(calls[0].Arguments))
	}

	second, err := a.Complete(ctx, llm.Request{
		Model: model,
		Messages: []llm.Message{
			llm.User("Call echo_state with value first."),
			first.Message,
			llm.ToolResultNamed(calls[0].ID, calls[0].Name, "first", false),
			llm.User("Now reply with exactly: tool roundtrip ok"),
		},
	})
	if err != nil {
		t.Fatalf("second Complete with tool result: %v", err)
	}
	if strings.TrimSpace(second.Text()) == "" {
		t.Fatalf("second response empty")
	}
}

func firstThinking(msg llm.Message) *llm.ThinkingData {
	for _, p := range msg.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			return p.Thinking
		}
	}
	return nil
}
