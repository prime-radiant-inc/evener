package responses

import (
	"encoding/json"
	"errors"
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

// TestBuildBody_WebSearchNilCapsIsFailOpen pins the mechanism behind issue
// #738's endpoint gate: this layer's own gate, caps.WebSearch == nil ||
// *caps.WebSearch, treats an unset capability as permissive. That is the
// right default for a model this adapter simply has no catalog opinion
// about - trust the caller's own req.WebSearch - but it means the
// registry's endpoint gate (llm/registry/resolve.go, gateWebSearch) can
// never represent "denied because this endpoint is not the vendor's
// first-party API" as a bare nil: any caller that independently sets
// req.WebSearch = true without first consulting registry.Caps -
// cmd/llmcall's --web-search flag builds its request this way, never
// touching the registry's decision at all - would still get the hosted
// tool sent to an endpoint that rejects it, reproducing #738's crash. The
// registry closes this by stripping to an explicit false rather than nil
// (llm/registry's TestResolve_WebSearchEndpointGate and
// TestResolveInstanceCarriesWebSearchDisabledWarning pin that half); this
// test pins the reason that fix is necessary, not optional: nil is let
// through right here, unconditionally, by design.
func TestBuildBody_WebSearchNilCapsIsFailOpen(t *testing.T) {
	req := userReq("hi")
	req.WebSearch = true
	res := resolved(nil) // Caps.WebSearch left nil, the state a gated instance must never carry
	if res.Caps.WebSearch != nil {
		t.Fatal("test setup: resolved(nil) must leave WebSearch nil")
	}
	tools, _ := build(t, req, res)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["type"] != "web_search" {
		t.Fatalf("nil Caps.WebSearch is fail-open at this layer: a caller setting req.WebSearch still gets the tool: %v", tools)
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

func TestBuildBody_ProviderOptionMaxOutputTokensRespectsCapsAndLowerWireValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire int
		want int
	}{
		{name: "capped by MaxOutputTokens", wire: 1000, want: 50},
		{name: "keeps lower positive wire value", wire: 25, want: 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := userReq("hi")
			req.MaxTokens = new(100)
			req.ProviderOptions = map[string]any{registry.ProtocolOpenAIResponses: map[string]any{"max_output_tokens": tc.wire}}
			body := build(t, req, resolved(func(c *registry.Caps) {
				openaiCaps(c)
				c.MaxOutputTokens = new(50)
			}))
			if got := body["max_output_tokens"]; got != tc.want {
				t.Fatalf("max_output_tokens = %v, want %d", got, tc.want)
			}
		})
	}
}

// TestUnsupportedToolChoiceCarriesTheInstance pins the spec §7.5 rule that
// every error stamp is res.Instance, not a provider literal.
func TestUnsupportedToolChoiceCarriesTheInstance(t *testing.T) {
	res := resolved(openaiCaps)
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "sometimes"}
	_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.Provider() != res.Instance {
		t.Fatalf("err = %v provider = %v, want %q", err, ok, res.Instance)
	}
}

// An explicit off means the user turned thinking off. Where the model's
// ladder lists an off level the request says so on the wire; where it does
// not, the reasoning object is dropped entirely — including the summary a
// mandatory-thinking row would otherwise still carry, which would leave
// thinking on against the user's stated intent.
func TestReasoningObject_ExplicitOff(t *testing.T) {
	none := llm.ReasoningEffortNone
	req := userReq("hi")
	req.ReasoningEffort = &none
	withOff := resolved(func(c *registry.Caps) {
		openaiCaps(c)
		c.EffortValues = []string{"none", "low", "high"}
	})
	offBody := build(t, req, withOff)
	if got := offBody["reasoning"]; jsonOf(t, got) != jsonOf(t, map[string]any{"effort": "none"}) {
		t.Fatalf("reasoning = %s, want the explicit off on the wire", jsonOf(t, got))
	}
	// The encrypted-reasoning include rides the off object too, deliberately.
	// It is inert when the model honors the off — there is no reasoning item
	// to return — and it is the only thing that keeps replay working on a
	// gateway that reasons anyway, which is not knowable in advance. A row
	// that takes no include at all still drops it through Fields["include"].
	if got := jsonOf(t, offBody["include"]); got != jsonOf(t, []string{encryptedReasoning}) {
		t.Fatalf("include = %s, want the encrypted-reasoning include kept on an off request", got)
	}
	noOffLevel := resolved(func(c *registry.Caps) {
		openaiCaps(c)
		c.ThinkingAlwaysOn = new(true)
		c.EffortValues = []string{"low", "high"}
	})
	if got, ok := build(t, req, noOffLevel)["reasoning"]; ok {
		t.Fatalf("reasoning = %s, want no reasoning object for a model with no off level", jsonOf(t, got))
	}
}
