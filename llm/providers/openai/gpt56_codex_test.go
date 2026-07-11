package openai

import (
	"encoding/json"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildCodexBodyForTest runs the real Responses request builder on a
// codex-backend adapter and round-trips the result through JSON so assertions
// see exactly the wire shape.
func buildCodexBodyForTest(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	a := &Adapter{ChatGPTAccountID: "acct_test"}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return round
}

// The ChatGPT codex backend has no bare "gpt-5.6" slug (it 400s with "not
// supported when using Codex with a ChatGPT account"); the codex CLI always
// sends a full variant slug. Map bare gpt-5.6 to the default variant on the
// wire, codex backend only.
func TestGPT56_CodexBackendMapsBareSlugToSol(t *testing.T) {
	body := buildCodexBodyForTest(t, llm.Request{
		Model:    "gpt-5.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %#v, want \"gpt-5.6-sol\" on the codex backend", body["model"])
	}

	// Explicit variant slugs pass through untouched.
	body = buildCodexBodyForTest(t, llm.Request{
		Model:    "gpt-5.6-terra",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6-terra" {
		t.Errorf("model = %#v, want \"gpt-5.6-terra\"", body["model"])
	}
}

// Platform-API (api-key) requests keep the caller's slug: the public API
// serves bare gpt-5.6 directly.
func TestGPT56_PlatformKeepsBareSlug(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "gpt-5.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6" {
		t.Errorf("model = %#v, want \"gpt-5.6\" on the platform API", body["model"])
	}
}

// codexLiteRequest is a request with system instructions and a tool, the
// shape most sensitive to responses-lite restructuring.
func codexLiteRequest(model string) llm.Request {
	return llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "be terse"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run a command",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}},
		}},
	}
}

// GPT-5.6 on the codex backend takes the responses-lite request shape,
// mirroring the codex CLI (codex-rs/core/src/client.rs build_responses_request
// when use_responses_lite): instructions ride in the input as a developer
// message, tools ride in the input as an additional_tools item (no top-level
// tools), parallel_tool_calls is false, and reasoning carries
// context:"all_turns".
func TestGPT56_CodexResponsesLiteShape(t *testing.T) {
	body := buildCodexBodyForTest(t, codexLiteRequest("gpt-5.6-sol"))

	if body["instructions"] != "" {
		t.Errorf("instructions = %#v, want empty string on responses-lite", body["instructions"])
	}
	if _, ok := body["tools"]; ok {
		t.Errorf("top-level tools present, want omitted on responses-lite: %#v", body["tools"])
	}
	if body["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls = %#v, want false on responses-lite", body["parallel_tool_calls"])
	}

	input, _ := body["input"].([]any)
	if len(input) < 3 {
		t.Fatalf("input has %d items, want additional_tools + developer instructions + user message: %#v", len(input), input)
	}
	at, _ := input[0].(map[string]any)
	if at["type"] != "additional_tools" || at["role"] != "developer" {
		t.Errorf("input[0] = %#v, want additional_tools developer item first", at)
	}
	tools, _ := at["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("additional_tools carries %d tools, want 1: %#v", len(tools), at)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "shell" {
		t.Errorf("additional_tools[0] = %#v, want the function tool", tool)
	}
	dev, _ := input[1].(map[string]any)
	if dev["type"] != "message" || dev["role"] != "developer" {
		t.Fatalf("input[1] = %#v, want developer message carrying base instructions", dev)
	}
	content, _ := dev["content"].([]any)
	c0, _ := content[0].(map[string]any)
	if c0["type"] != "input_text" || c0["text"] != "be terse" {
		t.Errorf("developer message content = %#v, want input_text \"be terse\"", content)
	}

	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["context"] != "all_turns" {
		t.Errorf("reasoning = %#v, want context \"all_turns\"", reasoning)
	}
}

// The additional_tools item is always first in the input on responses-lite,
// even with no tools declared — matching the codex CLI, which prepends it
// unconditionally.
func TestGPT56_CodexResponsesLiteEmptyTools(t *testing.T) {
	req := codexLiteRequest("gpt-5.6-sol")
	req.Tools = nil
	body := buildCodexBodyForTest(t, req)
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		t.Fatal("empty input")
	}
	at, _ := input[0].(map[string]any)
	if at["type"] != "additional_tools" {
		t.Errorf("input[0] = %#v, want additional_tools even with no tools", at)
	}
	tools, ok := at["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Errorf("additional_tools tools = %#v, want empty array", at["tools"])
	}
}

// Regression guard: gpt-5.5 codex-backend requests keep the pre-lite shape.
func TestGPT55_CodexShapeUnchanged(t *testing.T) {
	body := buildCodexBodyForTest(t, codexLiteRequest("gpt-5.5"))
	if body["instructions"] != "be terse" {
		t.Errorf("instructions = %#v, want \"be terse\"", body["instructions"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("top-level tools = %#v, want the declared tool", body["tools"])
	}
	if body["parallel_tool_calls"] != true {
		t.Errorf("parallel_tool_calls = %#v, want true", body["parallel_tool_calls"])
	}
	input, _ := body["input"].([]any)
	for _, itemAny := range input {
		item, _ := itemAny.(map[string]any)
		if item["type"] == "additional_tools" {
			t.Errorf("unexpected additional_tools item on gpt-5.5: %#v", item)
		}
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if _, has := reasoning["context"]; has {
			t.Errorf("reasoning.context present on gpt-5.5: %#v", reasoning)
		}
	}
}

// Regression guard: platform-API gpt-5.6 requests keep the exact shape main
// ships — no codex-lite restructuring on the api-key path.
func TestGPT56_PlatformShapeUnchanged(t *testing.T) {
	body := buildBodyForTest(t, codexLiteRequest("gpt-5.6"))
	if body["instructions"] != "be terse" {
		t.Errorf("instructions = %#v, want \"be terse\"", body["instructions"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("top-level tools = %#v, want the declared tool", body["tools"])
	}
	if body["parallel_tool_calls"] != true {
		t.Errorf("parallel_tool_calls = %#v, want true", body["parallel_tool_calls"])
	}
	input, _ := body["input"].([]any)
	for _, itemAny := range input {
		item, _ := itemAny.(map[string]any)
		if item["type"] == "additional_tools" {
			t.Errorf("unexpected additional_tools item on platform gpt-5.6: %#v", item)
		}
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if _, has := reasoning["context"]; has {
		t.Errorf("reasoning.context present on platform gpt-5.6: %#v", reasoning)
	}
}

// Responses-lite requests carry text.verbosity, defaulting to "low" — the
// codex client's default_verbosity for every gpt-5.6 variant.
func TestGPT56_CodexResponsesLiteVerbosityDefaultLow(t *testing.T) {
	body := buildCodexBodyForTest(t, codexLiteRequest("gpt-5.6-sol"))
	text, _ := body["text"].(map[string]any)
	if text["verbosity"] != "low" {
		t.Errorf("text = %#v, want verbosity \"low\"", body["text"])
	}

	// Platform gpt-5.6 and codex gpt-5.5 stay verbosity-free.
	if _, ok := buildBodyForTest(t, codexLiteRequest("gpt-5.6"))["text"]; ok {
		t.Error("platform gpt-5.6 request grew a text param")
	}
	if _, ok := buildCodexBodyForTest(t, codexLiteRequest("gpt-5.5"))["text"]; ok {
		t.Error("codex gpt-5.5 request grew a text param")
	}
}

// Responses-lite requests are routed by the x-openai-internal-codex-
// responses-lite header; without it the codex backend hangs on gpt-5.6.
// It must appear only on codex-backend requests for lite models.
func TestGPT56_CodexResponsesLiteHeader(t *testing.T) {
	const liteHeader = "x-openai-internal-codex-responses-lite"
	newHTTPReq := func(t *testing.T) *http.Request {
		t.Helper()
		httpReq, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
		if err != nil {
			t.Fatal(err)
		}
		return httpReq
	}

	codex := &Adapter{ChatGPTAccountID: "acct_test"}
	httpReq := newHTTPReq(t)
	codex.setRequestHeaders(httpReq, llm.Request{Model: "gpt-5.6-sol"})
	if got := httpReq.Header.Get(liteHeader); got != "true" {
		t.Errorf("codex gpt-5.6-sol %s = %q, want \"true\"", liteHeader, got)
	}

	httpReq = newHTTPReq(t)
	codex.setRequestHeaders(httpReq, llm.Request{Model: "gpt-5.5"})
	if got := httpReq.Header.Get(liteHeader); got != "" {
		t.Errorf("codex gpt-5.5 %s = %q, want absent", liteHeader, got)
	}

	platform := &Adapter{}
	httpReq = newHTTPReq(t)
	platform.setRequestHeaders(httpReq, llm.Request{Model: "gpt-5.6"})
	if got := httpReq.Header.Get(liteHeader); got != "" {
		t.Errorf("platform gpt-5.6 %s = %q, want absent", liteHeader, got)
	}
}
