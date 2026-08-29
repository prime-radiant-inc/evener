package responses

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func resolved(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses)}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "oai-prod", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.5", WireID: "gpt-5.5", Transport: registry.Transport{Endpoint: "/responses"}, Caps: caps}
}

func openaiCaps(c *registry.Caps) {
	c.Reasoning = new(true)
	c.ReasoningControls = []string{"effort"}
	c.StrictTools = new(true)
	c.ReasoningSummary = new("auto")
	c.WebSearch = new(true)
	for _, f := range []string{"store", "prompt_cache_key", "include", "truncation", "safety_identifier", "service_tier", "previous_response_id", "conversation"} {
		c.Fields[f] = true
	}
}

func codexLiteCaps(c *registry.Caps) {
	openaiCaps(c)
	c.ResponsesLite = new(true)
	c.ThinkingAlwaysOn = new(true)
	c.ImageDetail = new("omit")
	c.ReasoningSummary = new("detailed")
}

func userReq(text string) llm.Request {
	return llm.Request{Model: "gpt-5.5", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "be terse"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: text}}},
	}}
}

func build(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildBody_GroqBaselineSendsOnlyTheSpecFields(t *testing.T) {
	high := "high"
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
	req.ReasoningEffort = &high
	req.MaxTokens = new(100)
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	req.StopSequences = []string{"x"}
	res := resolved(func(c *registry.Caps) { c.Reasoning = new(true); c.ReasoningControls = []string{"effort"} })
	res.Instance, res.ModelID, res.WireID = "groq", "openai/gpt-oss-120b", "openai/gpt-oss-120b"
	body := build(t, req, res)
	for _, k := range []string{"model", "instructions", "input", "tools", "max_output_tokens", "reasoning", "text", "include"} {
		if _, has := body[k]; !has {
			t.Fatalf("missing %s: %s", k, jsonOf(t, body))
		}
	}
	for _, k := range []string{"stop", "store", "parallel_tool_calls", "metadata", "previous_response_id", "truncation"} {
		if _, has := body[k]; has {
			t.Fatalf("%s must not be built: %s", k, jsonOf(t, body))
		}
	}
	fn := body["tools"].([]map[string]any)[0]
	if _, has := fn["strict"]; has || fn["parameters"].(map[string]any)["additionalProperties"] != nil {
		t.Fatalf("no strict at baseline: %s", jsonOf(t, body))
	}
	if r := body["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != nil {
		t.Fatalf("reasoning: %s", jsonOf(t, body))
	}
	if body["model"] != "openai/gpt-oss-120b" || body["instructions"] != "be terse" {
		t.Fatalf("model/instructions: %s", jsonOf(t, body))
	}
	pruned := registry.Prune(body, res.Caps)
	if len(pruned) != 1 || pruned[0] != "include" {
		t.Fatalf("the baseline prunes include and nothing else that was built: %v", pruned)
	}
}

func TestBuildBody_OpenAIRow(t *testing.T) {
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
	req.WebSearch = true
	req.Store = nil
	req.Include = []string{"web_search_call.results"}
	req.Metadata = map[string]string{"k": "v"}
	req.PreviousResponseID = "resp_prev"
	body := build(t, req, resolved(openaiCaps))
	tools := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["strict"] != true || tools[0]["parameters"].(map[string]any)["additionalProperties"] != false || tools[1]["type"] != "web_search" {
		t.Fatalf("tools: %s", jsonOf(t, body))
	}
	if body["store"] != false || body["previous_response_id"] != "resp_prev" || body["metadata"] == nil {
		t.Fatalf("control fields: %s", jsonOf(t, body))
	}
	if _, has := body["reasoning"]; has {
		t.Fatalf("no effort and no always-on means no reasoning object: %s", jsonOf(t, body))
	}
	if inc := body["include"].([]string); len(inc) != 1 || inc[0] != "web_search_call.results" {
		t.Fatalf("include without a reasoning object carries only the caller's entries: %v", inc)
	}
	store := true
	req.Store = &store
	high := "high"
	req.ReasoningEffort = &high
	body = build(t, req, resolved(openaiCaps))
	if body["store"] != true {
		t.Fatal("an explicit Store overrides the privacy default")
	}
	if r := body["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != "auto" {
		t.Fatalf("reasoning: %s", jsonOf(t, body))
	}
	if inc := body["include"].([]string); len(inc) != 2 || inc[1] != "reasoning.encrypted_content" {
		t.Fatalf("include: %v", inc)
	}
	noWeb := resolved(openaiCaps)
	noWeb.Caps.WebSearch = new(false)
	if tools := build(t, req, noWeb)["tools"].([]map[string]any); len(tools) != 1 {
		t.Fatal("WebSearch=false drops the web_search tool")
	}
}

func TestBuildBody_CodexLite(t *testing.T) {
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	res := resolved(codexLiteCaps)
	res.Instance, res.WireID = "openai-codex", "gpt-5.6-sol"
	body := build(t, req, res)
	if body["model"] != "gpt-5.6-sol" || body["instructions"] != "" {
		t.Fatalf("lite: %s", jsonOf(t, body))
	}
	if _, has := body["tools"]; has {
		t.Fatalf("lite moves tools into input: %s", jsonOf(t, body))
	}
	input := body["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "additional_tools" || first["role"] != "developer" || len(first["tools"].([]any)) != 1 {
		t.Fatalf("additional_tools item: %s", jsonOf(t, first))
	}
	if second := input[1].(map[string]any); second["type"] != "message" || second["role"] != "developer" {
		t.Fatalf("developer instructions item: %s", jsonOf(t, second))
	}
	if r := body["reasoning"].(map[string]any); r["summary"] != "detailed" || r["effort"] != nil {
		t.Fatalf("always-on without an effort sends the summary alone: %s", jsonOf(t, body))
	}
	for _, k := range []string{"parallel_tool_calls", "text"} {
		if _, has := body[k]; has {
			t.Fatalf("%s is a body constant, not a builder field: %s", k, jsonOf(t, body))
		}
	}
	empty := userReq("hi")
	if first := build(t, empty, res)["input"].([]any)[0].(map[string]any); len(first["tools"].([]any)) != 0 {
		t.Fatal("additional_tools is always present, even empty")
	}
}

func TestBuildBody_ImageDetailAndStructuredOutput(t *testing.T) {
	img := llm.Request{Model: "gpt-5.5", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "see"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://img.example/a.png"}},
	}}}}
	detailOf := func(body map[string]any) any {
		parts := body["input"].([]any)[0].(map[string]any)["content"].([]any)
		return parts[1].(map[string]any)["detail"]
	}
	if d := detailOf(build(t, img, resolved(nil))); d != "high" {
		t.Fatalf("baseline detail = %v", d)
	}
	if d := detailOf(build(t, img, resolved(func(c *registry.Caps) { c.ImageDetail = new("original") }))); d != "original" {
		t.Fatalf("original detail = %v", d)
	}
	if d := detailOf(build(t, img, resolved(func(c *registry.Caps) { c.ImageDetail = new("omit") }))); d != nil {
		t.Fatalf("omit must drop detail, got %v", d)
	}
	req := userReq("hi")
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	format := build(t, req, resolved(func(c *registry.Caps) { c.StructuredOutput = new(false) }))["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("StructuredOutput=false downgrades: %v", format)
	}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	if tc := build(t, req, resolved(func(c *registry.Caps) { c.ToolChoiceForcing = new(false) }))["tool_choice"]; tc != "auto" {
		t.Fatalf("forcing off: %v", tc)
	}
	none := "none"
	req.ReasoningEffort = &none
	if _, has := build(t, req, resolved(openaiCaps))["reasoning"]; has {
		t.Fatal("none sends no reasoning object")
	}
	inPath := resolved(nil)
	inPath.Transport.Endpoint = "/openai/deployments/{model}/responses"
	if b := build(t, req, inPath); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}
}
