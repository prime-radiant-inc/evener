package google

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func protoRes(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolGoogle), Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "gemini-prod", Protocol: registry.ProtocolGoogle, ModelID: "gemini-x", WireID: "gemini-x", Transport: registry.Transport{Endpoint: "/models/{model}:generateContent", StreamEndpoint: "/models/{model}:streamGenerateContent?alt=sse"}, Caps: caps}
}

func protoReq(effort string) llm.Request {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
	}}
	if effort != "" {
		req.ReasoningEffort = &effort
	}
	return req
}

func protoBuild(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProtocolPrunablePathsMatchRegistry(t *testing.T) {
	p, ok := llm.ProtocolFor(registry.ProtocolGoogle)
	if !ok {
		t.Fatal("google not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolGoogle); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}

func TestProtocolBuildBody(t *testing.T) {
	req := protoReq("high")
	req.WebSearch = true
	req.Temperature = new(0.2)
	req.ProviderOptions = map[string]any{"google": map[string]any{"safetySettings": []any{map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}}}}
	body := protoBuild(t, req, protoRes(nil))
	if _, has := body["model"]; has {
		t.Fatal("the model rides in the URL, never the body")
	}
	gen := body["generationConfig"].(map[string]any)
	if gen["temperature"] != 0.2 || gen["thinkingConfig"].(map[string]any)["thinkingBudget"] != llm.ReasoningBudget("high") {
		t.Fatalf("generationConfig = %v", gen)
	}
	if tools := body["tools"].([]map[string]any); len(tools) != 1 || tools[0]["google_search"] == nil {
		t.Fatalf("google_search expected without function tools: %v", body["tools"])
	}
	if body["safetySettings"] == nil || body["systemInstruction"] == nil {
		t.Fatalf("provider options and system instruction: %v", body)
	}

	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	if tools := protoBuild(t, req, protoRes(nil))["tools"].([]map[string]any); len(tools) != 1 || tools[0]["functionDeclarations"] == nil {
		t.Fatalf("google_search never rides with function declarations: %v", tools)
	}
	req.Tools = nil
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("WebSearch=false drops google_search")
	}

	none := protoReq("none")
	if _, has := protoBuild(t, none, protoRes(nil))["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("none sends no thinkingConfig")
	}
	off := protoBuild(t, protoReq("high"), protoRes(func(c *registry.Caps) { c.Reasoning = new(false) }))
	if _, has := off["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("Reasoning=false sends no thinkingConfig")
	}
}

func TestProtocolBuildBody_NullableSchemasOnGeminiWire(t *testing.T) {
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{
		Name: "read_transcript",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"format": map[string]any{
					"type": []any{"string", "null"},
					"enum": []any{"outline", "markdown", nil},
				},
				"output_match": map[string]any{"type": []any{"string", "null"}},
			},
		},
	}}

	tools := protoBuild(t, req, protoRes(nil))["tools"].([]map[string]any)
	params := tools[0]["functionDeclarations"].([]map[string]any)[0]["parameters"].(map[string]any)
	format := params["properties"].(map[string]any)["format"].(map[string]any)
	if got, want := format["type"], "string"; got != want {
		t.Fatalf("Gemini format type = %#v, want %q", got, want)
	}
	if got, want := format["nullable"], true; got != want {
		t.Fatalf("Gemini format nullable = %#v, want %t", got, want)
	}
	if got, want := format["enum"], []any{"outline", "markdown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Gemini format enum = %#v, want %#v", got, want)
	}
	outputMatch := params["properties"].(map[string]any)["output_match"].(map[string]any)
	if got, want := outputMatch["type"], "string"; got != want {
		t.Fatalf("Gemini output_match type = %#v, want %q", got, want)
	}
	if got, want := outputMatch["nullable"], true; got != want {
		t.Fatalf("Gemini output_match nullable = %#v, want %t", got, want)
	}
}

// TestProtocolBuildBody_WebSearchNilCapsIsFailOpen pins the mechanism
// behind issue #738's endpoint gate on this protocol too (the Responses
// and Anthropic twins share the name): the gate here,
// req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
// (protocol.go), treats an unset capability as permissive - trust the
// caller's own req.WebSearch - so the registry's endpoint gate
// (llm/registry/resolve.go, gateWebSearch) can never represent "denied
// because this endpoint is not the vendor's first-party API" as a bare
// nil: a caller setting req.WebSearch = true without consulting
// registry.Caps would still get google_search sent to an endpoint that
// rejects it. The registry closes this by carrying an explicit false,
// never nil; this test pins why that is necessary at this layer and that
// the explicit false actually holds the tool back.
func TestProtocolBuildBody_WebSearchNilCapsIsFailOpen(t *testing.T) {
	req := protoReq("")
	req.WebSearch = true
	res := protoRes(nil) // Caps.WebSearch left nil, the state a gated instance must never carry
	if res.Caps.WebSearch != nil {
		t.Fatal("test setup: protoRes(nil) must leave WebSearch nil")
	}
	tools, _ := protoBuild(t, req, res)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["google_search"] == nil {
		t.Fatalf("nil Caps.WebSearch is fail-open at this layer: a caller setting req.WebSearch still gets google_search: %v", tools)
	}
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("an explicit false must hold google_search back")
	}
}

func TestProtocolBuildBody_MultimodalToolResultsCap(t *testing.T) {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shot", Arguments: []byte(`{}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shot", Content: "ok", ImageData: []byte{1, 2, 3}, ImageMediaType: "image/png"}}}},
	}}
	if _, err := (&Protocol{}).BuildBody(req, protoRes(nil)); err == nil {
		t.Fatal("tool-result images need MultimodalToolResults")
	}
	body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.MultimodalToolResults = new(true) }))
	parts := body["contents"].([]map[string]any)[1]["parts"].([]map[string]any)
	fr := parts[0]["functionResponse"].(map[string]any)
	if fr["parts"] == nil {
		t.Fatalf("inlineData must nest inside functionResponse: %v", fr)
	}
}

func TestProtocolBuildBody_ProviderOptionMaxOutputTokensRespectsCapsAndLowerWireValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire int
		want int
	}{
		{name: "capped by MaxOutputTokens", wire: 1000, want: 50},
		{name: "keeps lower positive wire value", wire: 25, want: 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := protoReq("")
			req.MaxTokens = new(100)
			req.ProviderOptions = map[string]any{registry.ProtocolGoogle: map[string]any{
				"generationConfig": map[string]any{"maxOutputTokens": tc.wire},
			}}
			body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.MaxOutputTokens = new(50) }))
			gen := body["generationConfig"].(map[string]any)
			if got := gen["maxOutputTokens"]; got != tc.want {
				t.Fatalf("generationConfig.maxOutputTokens = %v, want %d", got, tc.want)
			}
		})
	}
}

func TestProtocolBuildBody_GenerationConfigProviderOptionsAreNotMutated(t *testing.T) {
	options := map[string]any{"generationConfig": map[string]any{"maxOutputTokens": 1000}}
	buildWith := func(admitted int) int {
		req := protoReq("")
		req.MaxTokens = new(admitted)
		req.ProviderOptions = map[string]any{registry.ProtocolGoogle: options}
		body := protoBuild(t, req, protoRes(nil))
		return body["generationConfig"].(map[string]any)["maxOutputTokens"].(int)
	}
	if got := buildWith(100); got != 100 {
		t.Fatalf("first maxOutputTokens = %d, want 100", got)
	}
	if got := buildWith(500); got != 500 {
		t.Fatalf("second maxOutputTokens = %d, want 500", got)
	}
	if got := options["generationConfig"].(map[string]any)["maxOutputTokens"]; got != 1000 {
		t.Fatalf("provider options mutated: maxOutputTokens = %v, want 1000", got)
	}
}

func TestProtocolBuildBody_RejectsMalformedGenerationConfigProviderOption(t *testing.T) {
	for _, malformed := range []any{nil, "not-an-object"} {
		req := protoReq("")
		req.ProviderOptions = map[string]any{registry.ProtocolGoogle: map[string]any{
			"generationConfig": malformed,
		}}
		body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, protoRes(nil)), protoRes(nil))
		if body != nil {
			t.Fatalf("generationConfig %T produced body %v, want no request", malformed, body)
		}
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("generationConfig %T error = %v, want *llm.ConfigurationError", malformed, err)
		}
	}
}

// TestProtocolUnsupportedToolChoiceCarriesTheInstance pins the spec §7.5
// rule that every error stamp is res.Instance, not a provider literal.
func TestProtocolUnsupportedToolChoiceCarriesTheInstance(t *testing.T) {
	res := protoRes(nil)
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "sometimes"}
	_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.Provider() != res.Instance {
		t.Fatalf("err = %v provider = %v, want %q", err, ok, res.Instance)
	}
}
