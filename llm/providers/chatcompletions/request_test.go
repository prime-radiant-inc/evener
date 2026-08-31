package chatcompletions

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// resolved builds a Resolved record for the openai-chat protocol with the
// baseline Fields table and the given cap overrides.
func resolved(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "work", Protocol: registry.ProtocolOpenAIChat, ModelID: "m", WireID: "m-wire", Transport: registry.Transport{Endpoint: "/chat/completions"}, Caps: caps}
}

func userReq(text string) llm.Request {
	return llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: text}}}}}
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

func TestBuildBody_ThinkingFormats(t *testing.T) {
	high := "high"
	type tc struct {
		format   string
		explicit bool // effort "high" set on the request
		alwaysOn bool
		want     map[string]any // reasoning-related keys only
	}
	cases := []tc{
		{"", true, false, map[string]any{"reasoning_effort": "high"}},
		{"openai", false, true, map[string]any{"reasoning_effort": "medium"}},
		{"openai", false, false, map[string]any{}},
		{"openrouter", true, false, map[string]any{"reasoning": map[string]any{"effort": "high"}}},
		{"openrouter", false, true, map[string]any{"reasoning": map[string]any{"enabled": true}}},
		{"zai", true, false, map[string]any{"thinking": map[string]any{"type": "enabled", "clear_thinking": false}, "reasoning_effort": "high"}},
		{"zai", false, true, map[string]any{"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}},
		{"deepseek", true, false, map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high"}},
		{"together", false, true, map[string]any{"reasoning": map[string]any{"enabled": true}}},
		{"qwen", true, false, map[string]any{"enable_thinking": true}},
		{"qwen-chat-template", false, true, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true, "preserve_thinking": true}}},
		{"chat-template", true, false, map[string]any{"chat_template_kwargs": map[string]any{"thinking": true}}},
		{"string-thinking", true, false, map[string]any{"thinking": "high"}},
		{"string-thinking", false, true, map[string]any{"thinking": "medium"}},
	}
	keys := []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "chat_template_kwargs"}
	for _, c := range cases {
		t.Run(c.format+"/explicit="+jsonOf(t, c.explicit)+"/alwaysOn="+jsonOf(t, c.alwaysOn), func(t *testing.T) {
			res := resolved(func(caps *registry.Caps) {
				caps.Reasoning = new(true)
				caps.ReasoningControls = []string{"effort"}
				if c.format != "" {
					caps.ThinkingFormat = new(c.format)
				}
				if c.alwaysOn {
					caps.ThinkingAlwaysOn = new(true)
				}
				if c.format == "chat-template" {
					caps.ChatTemplateKwargs = map[string]any{"thinking": true}
				}
			})
			req := userReq("hi")
			if c.explicit {
				req.ReasoningEffort = &high
			}
			body := build(t, req, res)
			got := map[string]any{}
			for _, k := range keys {
				if v, ok := body[k]; ok {
					got[k] = v
				}
			}
			if jsonOf(t, got) != jsonOf(t, c.want) {
				t.Fatalf("got %s want %s", jsonOf(t, got), jsonOf(t, c.want))
			}
		})
	}
}

func TestBuildBody_ReasoningGates(t *testing.T) {
	high, none := "high", "none"
	off := resolved(func(c *registry.Caps) { c.Reasoning = new(false); c.ThinkingFormat = new("zai") })
	req := userReq("hi")
	req.ReasoningEffort = &high
	// ProviderOptions is scoped to this one build via offReq: req itself must
	// stay free of a "reasoning" provider option below, or its mere presence
	// would flip useReasoningDetails on for every later reasoning-on subtest
	// (buildBody: `if _, ok := options["reasoning"]; ok && !reasoningOff`),
	// skipping applyThinkingFormat for reasons unrelated to what they test.
	offReq := req
	offReq.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"reasoning": map[string]any{"effort": "high"}, "top_k": 3}}
	body := build(t, offReq, off)
	for _, k := range []string{"reasoning_effort", "reasoning", "thinking"} {
		if _, has := body[k]; has {
			t.Fatalf("Reasoning=false must strip %s: %v", k, body)
		}
	}
	if body["top_k"] != 3 {
		t.Fatal("non-reasoning provider options survive")
	}

	toggleOnly := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"toggle"}
		c.ThinkingFormat = new("deepseek")
	})
	body = build(t, req, toggleOnly)
	if _, has := body["reasoning_effort"]; has || body["thinking"] == nil {
		t.Fatalf("toggle-only rows enable without an effort: %v", body)
	}

	req.ReasoningEffort = &none
	body = build(t, req, resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.ThinkingAlwaysOn = new(true)
	}))
	if _, has := body["reasoning_effort"]; has {
		t.Fatalf("none sends nothing: %v", body)
	}

	unknown := resolved(nil) // Reasoning nil, no controls: an explicit effort passes through
	req.ReasoningEffort = &high
	if body := build(t, req, unknown); body["reasoning_effort"] != "high" {
		t.Fatalf("unknown row must pass an explicit effort through: %v", body)
	}
}

func TestBuildBody_CapsShapeStructure(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f", Description: "d", Parameters: schema}}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: schema}
	req.MaxTokens = new(50)
	req.Metadata = map[string]string{"k": "v"}
	req.SessionID = "sess"
	req.PromptCacheRetention = "24h"

	base := build(t, req, resolved(nil))
	fn := base["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if _, has := fn["strict"]; has || base["tool_choice"] != "required" || base["max_tokens"] != 50 {
		t.Fatalf("baseline: %s", jsonOf(t, base))
	}
	if base["response_format"].(map[string]any)["type"] != "json_schema" || base["store"] != nil || base["prompt_cache_key"] != nil {
		t.Fatalf("baseline: %s", jsonOf(t, base))
	}
	if base["metadata"] == nil {
		t.Fatalf("prunable paths are emitted for the prune to decide: %s", jsonOf(t, base))
	}
	if base["prompt_cache_retention"] != nil {
		t.Fatalf("baseline caps gate prompt_cache_retention in ShapeRequest, before the builder ever sees it: %s", jsonOf(t, base))
	}
	if base["model"] != "m-wire" {
		t.Fatalf("model must be the wire id: %v", base["model"])
	}

	shaped := build(t, req, resolved(func(c *registry.Caps) {
		c.StrictTools = new(true)
		c.StructuredOutput = new(false)
		c.ToolChoiceForcing = new(false)
		c.MaxTokensField = new("max_completion_tokens")
		c.ToolStream = new(true)
		c.Fields["store"] = true
		c.Fields["prompt_cache_key"] = true
		c.Fields["prompt_cache_retention"] = true
	}))
	fn = shaped["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if fn["strict"] != true || fn["parameters"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("strict tools: %s", jsonOf(t, shaped))
	}
	if shaped["tool_choice"] != "auto" || shaped["response_format"].(map[string]any)["type"] != "json_object" || shaped["max_completion_tokens"] != 50 || shaped["max_tokens"] != nil || shaped["tool_stream"] != true {
		t.Fatalf("shaped: %s", jsonOf(t, shaped))
	}
	if shaped["store"] != false || shaped["prompt_cache_key"] != "evener-session-sess" || shaped["prompt_cache_retention"] != "24h" {
		t.Fatalf("store/prompt cache: %s", jsonOf(t, shaped))
	}
	named := req
	named.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "f"}
	if b := build(t, named, resolved(func(c *registry.Caps) { c.ToolChoiceForcing = new(false) })); b["tool_choice"] != "auto" {
		t.Fatalf("named choice must downgrade: %v", b["tool_choice"])
	}

	streaming, err := buildBody(llm.ShapeRequest(req, resolved(nil)), resolved(nil), true)
	if err != nil || streaming["stream"] != true || !reflect.DeepEqual(streaming["stream_options"], map[string]any{"include_usage": true}) {
		t.Fatalf("stream: %v %s", err, jsonOf(t, streaming))
	}

	inPath := resolved(nil)
	inPath.Transport.Endpoint = "/models/{model}/chat"
	if b := build(t, req, inPath); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}
}

func TestBuildBody_AnthropicCacheControl(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
	}, Tools: []llm.ToolDefinition{{Name: "f"}}}
	body := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic"); c.CacheTTL = new("1h") }))
	msgs := body["messages"].([]map[string]any)
	sys := msgs[0]["content"].([]map[string]any)[0]["cache_control"].(map[string]any)
	if sys["type"] != "ephemeral" || sys["ttl"] != "1h" {
		t.Fatalf("system marker: %s", jsonOf(t, body))
	}
	if tool := body["tools"].([]map[string]any)[0]; tool["cache_control"] == nil {
		t.Fatalf("last tool marker missing: %s", jsonOf(t, body))
	}
	plain := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic") }))
	if m := plain["messages"].([]map[string]any)[0]["content"].([]map[string]any)[0]["cache_control"].(map[string]any); m["ttl"] != nil {
		t.Fatal("no ttl without CacheTTL")
	}
}

// TestBuildBody_ReasoningDetailsRowKeepsTheDialectControl covers the review
// finding on ReasoningField's double duty: a row that declares
// ReasoningField = "reasoning_details" for the assistant-replay shape must
// still get its dialect's enable control written by applyThinkingFormat —
// useReasoningDetails alone must not silently suppress it. Only an actual
// ProviderOptions "reasoning" object (optionCarriesReasoning) means
// applyThinkingFormat is redundant, because that object itself reaches the
// wire through the passthrough loop.
func TestBuildBody_ReasoningDetailsRowKeepsTheDialectControl(t *testing.T) {
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"toggle"}
		c.ThinkingFormat = new("openrouter")
		c.ThinkingAlwaysOn = new(true)
		c.ReasoningField = new("reasoning_details")
	})
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		assistantTurn("prior thought", "", "prior answer"),
		llm.User("q2"),
	}}

	body := build(t, req, res)
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["enabled"] != true {
		t.Fatalf("row declaring reasoning_details must still get its dialect control: %v", body["reasoning"])
	}
	msgs := body["messages"].([]map[string]any)
	if _, ok := msgs[1]["reasoning_details"]; !ok {
		t.Fatalf("assistant turn must replay through reasoning_details: %v", msgs[1])
	}

	// A request-level "reasoning" provider option is the other trigger for
	// useReasoningDetails, but it means the OPTION's object is what reaches
	// the wire — applyThinkingFormat must be skipped rather than writing a
	// second, conflicting control (the behavior the old
	// TestBuildBody_ReasoningGates guarded before it was rewritten).
	req.ProviderOptions = map[string]any{registry.ProtocolOpenAIChat: map[string]any{"reasoning": map[string]any{"effort": "high"}}}
	body = build(t, req, res)
	if got := body["reasoning"]; !reflect.DeepEqual(got, map[string]any{"effort": "high"}) {
		t.Fatalf("provider option's reasoning object must reach the wire unchanged: %v", got)
	}
}

// TestUnsupportedToolChoiceCarriesTheInstance pins the spec §7.5 rule that
// every error stamp is res.Instance, not the protocol id.
func TestUnsupportedToolChoiceCarriesTheInstance(t *testing.T) {
	res := resolved(nil)
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "sometimes"}
	_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.Provider() != res.Instance {
		t.Fatalf("err = %v provider = %v, want %q", err, ok, res.Instance)
	}
}
