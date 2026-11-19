package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func resolved(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses)}
	// The Responses builder's image-wire tests model a row that accepts image
	// input; request shaping sends tool-result media only to such a row (#4217).
	caps.InputModalities = []string{"text", "image"}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "oai-prod", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.5", WireID: "gpt-5.5", Transport: registry.Transport{Endpoint: "/responses"}, Caps: caps}
}

func openaiCaps(c *registry.Caps) {
	c.Reasoning = new(true)
	c.ReasoningControls = []string{"effort"}
	c.EffortValues = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
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

func TestCompletePreservesAssistantItemOrder(t *testing.T) {
	t.Parallel()
	thinking := func(blob string) llm.ContentPart {
		return llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_fixture", Text: "display", EncryptedContent: blob}}
	}
	commentary := func(text string) llm.ContentPart {
		return llm.ContentPart{Kind: llm.ContentText, Phase: "commentary", Text: text}
	}
	const arguments = `{ "id" : "\u0061" }`
	call := llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_fixture", ItemID: "fc_fixture", Name: "lookup", Arguments: json.RawMessage(arguments)}}
	result := llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_fixture", Content: "result"}}
	for _, tc := range []struct {
		name    string
		content []llm.ContentPart
		want    [][]string
	}{
		{
			name:    "encrypted",
			content: []llm.ContentPart{commentary("before"), thinking("opaque-fixture"), call, commentary("after")},
			want:    [][]string{{"message"}, {"message", "commentary", "before"}, {"reasoning"}, {"function_call"}, {"message", "commentary", "after"}, {"function_call_output"}},
		},
		{
			name:    "display only",
			content: []llm.ContentPart{commentary("before"), thinking(""), call, commentary("after")},
			want:    [][]string{{"message"}, {"message", "commentary", "before"}, {"function_call"}, {"message", "commentary", "after"}, {"function_call_output"}},
		},
		{
			name: "group across omitted parts",
			content: []llm.ContentPart{
				thinking("opaque-fixture"), commentary("first"), thinking(""), commentary("second"),
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(`[]`)}},
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(`null`)}},
				commentary("third"),
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(`{"type":"web_search_call","id":"ws_fixture","status":"completed"}`)}},
				commentary("fourth"),
				{Kind: llm.ContentText, Phase: "final_answer", Text: "final"},
				call,
			},
			want: [][]string{
				{"message"}, {"reasoning"}, {"message", "commentary", "first", "second", "third"}, {"web_search_call"},
				{"message", "commentary", "fourth"}, {"message", "final_answer", "final"}, {"function_call"}, {"function_call_output"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := userReq("input")
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: tc.content},
				llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{result}})
			srv, wire := server(t, http.StatusOK, responseJSON)
			res := liveRes(srv, openaiCaps)
			if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), llm.ShapeRequest(req, res), res); err != nil {
				t.Fatal(err)
			}
			var got [][]string
			for _, item := range wire.body["input"].([]any) {
				m, _ := item.(map[string]any)
				kind, _ := m["type"].(string)
				fields := []string{kind}
				if kind == "function_call" && (m["arguments"] != arguments || m["id"] != "fc_fixture") {
					t.Fatalf("function_call = %q, want the arguments and the item id", jsonOf(t, m))
				}
				if m["role"] == "assistant" {
					fields = append(fields, m["phase"].(string))
					for _, part := range m["content"].([]any) {
						fields = append(fields, part.(map[string]any)["text"].(string))
					}
				}
				got = append(got, fields)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("input items = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildBody_ReplaysResponsesPlaintextReasoning(t *testing.T) {
	t.Parallel()
	replay := func(th *llm.ThinkingData, reasoning bool) []any {
		req := userReq("input")
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: th},
		}})
		return build(t, req, resolved(func(c *registry.Caps) { openaiCaps(c); c.Reasoning = new(reasoning) }))["input"].([]any)
	}
	typed := &llm.ThinkingData{ID: "rs_1", Text: "  think  ",
		ResponsesContentParts: []llm.ResponsesReasoningPart{{Type: "reasoning_text", Text: "  think  "}},
		ResponsesSummaryParts: []llm.ResponsesReasoningPart{{Type: "summary_text", Text: " sum \n"}}}
	const want = `{"content":[{"text":"  think  ","type":"reasoning_text"}],"id":"rs_1","summary":[{"text":" sum \n","type":"summary_text"}],"type":"reasoning"}`
	if got := replay(typed, true); len(got) != 2 || jsonOf(t, got[1]) != want {
		t.Fatalf("input = %s, want the typed item verbatim: %s", jsonOf(t, got), want)
	}
	if got := replay(&llm.ThinkingData{ID: "rs_2", Text: "legacy"}, true); len(got) != 1 {
		t.Fatalf("a bare Text projection must not replay: %s", jsonOf(t, got))
	}
	if got := replay(typed, false); len(got) != 1 {
		t.Fatalf("reasoning = false input = %s, want the user message alone", jsonOf(t, got))
	}
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
	t.Parallel()
	high := "high"
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
	req.ReasoningEffort = &high
	req.MaxTokens = new(100)
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	req.StopSequences = []string{"x"}
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.EffortValues = []string{"low", "medium", "high"}
	})
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
	pruned := registry.Prune(body, res.Caps, res.Protocol)
	if len(pruned) != 1 || pruned[0] != "include" {
		t.Fatalf("the baseline prunes include and nothing else that was built: %v", pruned)
	}
}

func TestBuildBody_OpenAIRow(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	// Every property is required: a tool that leaves one optional has no legal
	// strict projection and is sent non-strict instead.
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
	}}}
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
	t.Parallel()
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
	t.Parallel()
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

// TestBuildBody_ToolResultImageDetail pins the builder's second image block: a
// tool-result image resolves its detail through the same rule as a user-message
// image, from the row's cap or the "high" default, and a row set to "omit"
// drops it there too. Both blocks call the shared rule with different arguments
// — a part detail there, none here — so each needs its own guard.
func TestBuildBody_ToolResultImageDetail(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "gpt-5.5", Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call1", Name: "shot", Content: "ok",
			ImageData: encoded.Bytes(), ImageMediaType: "image/png",
		}},
	}}}}
	detailOf := func(body map[string]any) any {
		output := body["input"].([]any)[0].(map[string]any)["output"].([]any)
		return output[1].(map[string]any)["detail"]
	}
	if d := detailOf(build(t, req, resolved(nil))); d != "high" {
		t.Fatalf("baseline tool-result detail = %v", d)
	}
	if d := detailOf(build(t, req, resolved(func(c *registry.Caps) { c.ImageDetail = new("low") }))); d != "low" {
		t.Fatalf("row detail on a tool-result image = %v, want low", d)
	}
	if d := detailOf(build(t, req, resolved(func(c *registry.Caps) { c.ImageDetail = new("omit") }))); d != nil {
		t.Fatalf("omit must drop the tool-result detail, got %v", d)
	}
}

func TestBuildBody_ImageDetailAndStructuredOutput(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
