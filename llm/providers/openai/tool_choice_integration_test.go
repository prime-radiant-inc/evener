package openai

import (
	"context"
	"os"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// TestAdapter_Integration_NamedToolChoice_ResponsesAPI forces a specific tool via
// named tool_choice against the live Responses API. With no ChatGPT account
// configured, Complete issues a direct POST /v1/responses (no Chat-Completions
// fallback), so this exercises the Responses-API tool_choice wire shape end to end.
//
// The Responses API requires the forced function name at the top level
// ({"type":"function","name":"X"}); the Chat Completions shape
// ({"type":"function","function":{"name":"X"}}) is rejected with a 400. This test
// is the live regression guard for PRI-2007.
func TestAdapter_Integration_NamedToolChoice_ResponsesAPI(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	objArg := func() map[string]any {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []any{"city"},
		}
	}
	weather := llm.ToolDefinition{Name: "get_weather", Description: "Get the current weather for a city.", Parameters: objArg()}
	other := llm.ToolDefinition{Name: "get_time", Description: "Get the current time in a city.", Parameters: objArg()}

	resp, err := a.Complete(ctx, llm.Request{
		Model:      "gpt-5.4-mini",
		Messages:   []llm.Message{llm.User("Hello.")},
		Tools:      []llm.ToolDefinition{weather, other},
		ToolChoice: &llm.ToolChoice{Mode: "named", Name: "get_weather"},
	})
	if err != nil {
		t.Fatalf("named tool_choice on Responses API failed: %v", err)
	}

	calls := resp.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("expected a forced tool call to get_weather; got none (text=%q)", resp.Text())
	}
	found := false
	for _, c := range calls {
		t.Logf("tool call: %s", c.Name)
		if c.Name == "get_weather" {
			found = true
		}
	}
	if !found {
		t.Fatalf("forced tool_choice=get_weather not honored; calls=%v", calls)
	}
}
