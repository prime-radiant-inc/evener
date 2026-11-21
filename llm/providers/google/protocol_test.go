package google

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

func protoRes(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolGoogle), Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{
		Instance: "gemini-prod",
		Protocol: registry.ProtocolGoogle,
		ModelID:  "gemini-x",
		WireID:   "gemini-x",
		Transport: registry.Transport{
			Endpoint:            "/interactions",
			StreamEndpoint:      "/interactions",
			ModelsEndpoint:      "/models",
			CountTokensEndpoint: registry.EndpointUnsupported,
		},
		Caps: caps,
	}
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
	t.Parallel()
	p, ok := llm.ProtocolFor(registry.ProtocolGoogle)
	if !ok {
		t.Fatal("google not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolGoogle); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}

func TestBuildBody_NoEffortVerdictSendsNoLevel(t *testing.T) {
	t.Parallel()
	unsupported := true
	levelShape := func(c *registry.Caps) {
		c.ThinkingShape = new("level")
		c.EffortValues = []string{}
		c.ReasoningControls = []string{"toggle"}
		c.EffortParameterUnsupported = &unsupported
	}
	body := protoBuild(t, protoReq("high"), protoRes(levelShape))
	gen, _ := body["generation_config"].(map[string]any)
	if gen == nil {
		t.Fatal("generation_config missing")
	}
	if _, has := gen["thinking_level"]; has {
		t.Fatalf("generation_config = %v, want no thinking_level on a no-effort row", gen)
	}
	if gen["thinking_summaries"] != "auto" {
		t.Fatalf("generation_config = %v, want thinking_summaries=auto kept", gen)
	}
}

func TestProtocolBuildBody(t *testing.T) {
	t.Parallel()
	req := protoReq("high")
	req.WebSearch = true
	req.Temperature = new(0.2)
	req.ProviderOptions = map[string]any{"google": map[string]any{"safety_settings": []any{map[string]any{"type": "harassment", "threshold": "block_none"}}}}
	levelShape := func(c *registry.Caps) { c.ThinkingShape = new("level") }
	body := protoBuild(t, req, protoRes(levelShape))
	if body["model"] != "gemini-x" {
		t.Fatalf("model = %v, want gemini-x unconditionally in body", body["model"])
	}
	if body["store"] != false {
		t.Fatalf("unanchored default store = %v, want false", body["store"])
	}
	gen := body["generation_config"].(map[string]any)
	if gen["temperature"] != 0.2 || gen["thinking_level"] != "high" || gen["thinking_summaries"] != "auto" {
		t.Fatalf("generation_config = %v", gen)
	}
	if tools := body["tools"].([]map[string]any); len(tools) != 1 || tools[0]["type"] != "google_search" {
		t.Fatalf("google_search expected without function tools: %v", body["tools"])
	}
	if body["safety_settings"] == nil || body["system_instruction"] != "sys" {
		t.Fatalf("provider options and system_instruction: %v", body)
	}

	// Without CombinedSearchAndFunctions, function tools omit google_search.
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	if tools := protoBuild(t, req, protoRes(nil))["tools"].([]map[string]any); len(tools) != 1 || tools[0]["type"] != "function" {
		t.Fatalf("google_search omitted when CombinedSearchAndFunctions=false: %v", tools)
	}

	// With CombinedSearchAndFunctions=true and store=true, both google_search and function tools ride with tool_choice=validated.
	reqStore := req
	reqStore.Store = new(true)
	combinedRes := protoRes(func(c *registry.Caps) {
		c.CombinedSearchAndFunctions = new(true)
		c.Fields["previous_interaction_id"] = true
	})
	combinedBody := protoBuild(t, reqStore, combinedRes)
	if tools := combinedBody["tools"].([]map[string]any); len(tools) != 2 || tools[0]["type"] != "function" || tools[1]["type"] != "google_search" {
		t.Fatalf("combined tools = %v", tools)
	}
	if combinedGen := combinedBody["generation_config"].(map[string]any); combinedGen["tool_choice"] != "validated" {
		t.Fatalf("combined tool_choice = %v, want validated", combinedGen["tool_choice"])
	}

	// Combined search + functions without store=true fails locally.
	if _, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, combinedRes), combinedRes); err == nil {
		t.Fatal("combined search + functions with store=false must fail")
	}

	req.Tools = nil
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("WebSearch=false drops google_search")
	}

	none := protoReq("none")
	noneGen := protoBuild(t, none, protoRes(levelShape))["generation_config"].(map[string]any)
	if _, has := noneGen["thinking_level"]; has || noneGen["thinking_summaries"] != "none" {
		t.Fatalf("none must omit thinking_level and set thinking_summaries=none, got %v", noneGen)
	}
	off := protoBuild(t, protoReq("high"), protoRes(func(c *registry.Caps) { c.Reasoning = new(false) }))
	if genOff, ok := off["generation_config"].(map[string]any); ok {
		if _, has := genOff["thinking_level"]; has {
			t.Fatalf("Reasoning=false must not send thinking_level: %v", genOff)
		}
		if _, has := genOff["thinking_summaries"]; has {
			t.Fatalf("Reasoning=false must not send thinking_summaries: %v", genOff)
		}
	}
}

func TestProtocolBuildBody_ThinkingShape(t *testing.T) {
	t.Parallel()
	level := func(t *testing.T, body map[string]any) any {
		t.Helper()
		gen, _ := body["generation_config"].(map[string]any)
		if gen == nil {
			t.Fatalf("expected generation_config, body: %v", body)
		}
		return gen["thinking_level"]
	}
	levelRow := func(mutate func(c *registry.Caps)) registry.Resolved {
		return protoRes(func(c *registry.Caps) {
			c.ThinkingShape = new("level")
			if mutate != nil {
				mutate(c)
			}
		})
	}

	row := levelRow(func(c *registry.Caps) { c.EffortValues = []string{"minimal", "low", "medium", "high", "xhigh"} })
	for _, want := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		if got := level(t, protoBuild(t, protoReq(want), row)); got != want {
			t.Fatalf("thinking_level = %v, want %q", got, want)
		}
	}
	if got := level(t, protoBuild(t, protoReq("max"), row)); got != "xhigh" {
		t.Fatalf("max must clamp down to the ladder's ceiling xhigh, got %v", got)
	}

	// Ladderless row clamps max -> high and drops unrecognized values.
	if got := level(t, protoBuild(t, protoReq("max"), levelRow(nil))); got != "high" {
		t.Fatalf("ladderless max must clamp to high, got %v", got)
	}
	for _, effort := range []string{"unknown", "extreme"} {
		body := protoBuild(t, protoReq(effort), levelRow(nil))
		if _, has := body["generation_config"].(map[string]any)["thinking_level"]; has {
			t.Fatalf("unrecognized effort %q must not send thinking_level: %v", effort, body["generation_config"])
		}
	}

	// Unrecognized thinking_shape values (e.g. legacy "budget", "adaptive") omit thinking_level while keeping thinking_summaries.
	for name, mutate := range map[string]func(*registry.Caps){
		"toggle-only":   func(c *registry.Caps) { c.ReasoningControls = []string{"toggle"} },
		"uncataloged":   func(c *registry.Caps) { c.Reasoning = nil; c.ReasoningControls = []string{"effort"} },
		"budget":        func(c *registry.Caps) { c.ThinkingShape = new("budget") },
		"adaptive":      func(c *registry.Caps) { c.ThinkingShape = new("adaptive") },
		"budget+effort": func(c *registry.Caps) { c.ThinkingShape = new("budget+effort") },
	} {
		body := protoBuild(t, protoReq("high"), protoRes(mutate))
		gen := body["generation_config"].(map[string]any)
		if _, has := gen["thinking_level"]; has {
			t.Fatalf("%s row must not send thinking_level: %v", name, gen)
		}
		if gen["thinking_summaries"] != "auto" {
			t.Fatalf("%s row must still send thinking_summaries=auto: %v", name, gen)
		}
	}
}

func TestProtocolBuildBody_CatalogRowThinkingShape(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(registry.WithNoUserLayer(), registry.WithoutCache(), registry.WithOffline(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{
		"google/gemini-2.5-computer-use-preview-10-2025",
		"google/gemini-2.5-flash",
		"google/gemini-3.1-pro-preview",
		"google/gemini-4-neon",
	} {
		res, err := r.Resolve(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(protoReq("high"), res), res)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		gen, _ := body["generation_config"].(map[string]any)
		switch ref {
		case "google/gemini-2.5-computer-use-preview-10-2025":
			if _, has := gen["thinking_level"]; has {
				t.Fatalf("%s must omit thinking_level, got %v", ref, gen)
			}
		case "google/gemini-2.5-flash", "google/gemini-3.1-pro-preview", "google/gemini-4-neon":
			if gen["thinking_level"] != "high" {
				t.Fatalf("%s thinking_level = %v, want high", ref, gen["thinking_level"])
			}
		}
	}
}

func TestProtocolBuildBody_DropsGeminiSchemaMetaKeysAndPreservesJSONSchemaResponseFormat(t *testing.T) {
	t.Parallel()
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{
		Name: "use_browser",
		Parameters: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id":     "https://example.test/use_browser.json",
			"type":    "object",
			"properties": map[string]any{
				"$id":    map[string]any{"type": "string"},
				"action": map[string]any{"$comment": "the action to run", "type": "string"},
			},
			"required": []any{"$id", "action"},
		},
	}}
	req.ResponseFormat = &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: map[string]any{
			"$schema":              "https://json-schema.org/draft/2020-12/schema",
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"nullable_str": map[string]any{"type": []any{"string", "null"}},
				"tuple":        map[string]any{"type": "array", "prefixItems": []any{map[string]any{"type": "string"}}, "minItems": 1},
				"$id":          map[string]any{"type": "string"},
			},
		},
	}

	body := protoBuild(t, req, protoRes(nil))
	tools := body["tools"].([]map[string]any)
	params := tools[0]["parameters"].(map[string]any)
	for _, key := range []string{"$schema", "$id"} {
		if _, ok := params[key]; ok {
			t.Fatalf("parameters must not carry %q: %#v", key, params)
		}
	}
	props := params["properties"].(map[string]any)
	if _, ok := props["$id"]; !ok {
		t.Fatalf("property named $id must survive: %#v", props)
	}
	if _, ok := props["action"].(map[string]any)["$comment"]; ok {
		t.Fatalf("nested $comment must be dropped")
	}

	rf := body["response_format"].(map[string]any)
	if rf["type"] != "text" || rf["mime_type"] != "application/json" {
		t.Fatalf("response_format = %#v", rf)
	}
	schema := rf["schema"].(map[string]any)
	if _, ok := schema["$schema"]; ok {
		t.Fatalf("response_format schema must drop $schema: %#v", schema)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("response_format schema must preserve additionalProperties: %#v", schema)
	}
	rfProps := schema["properties"].(map[string]any)
	if _, ok := rfProps["$id"]; !ok {
		t.Fatalf("response_format schema must keep property named $id: %#v", rfProps)
	}
	if ntype := rfProps["nullable_str"].(map[string]any)["type"]; !reflect.DeepEqual(ntype, []any{"string", "null"}) {
		t.Fatalf("response_format schema must preserve type array, got %#v", ntype)
	}
	if tuple := rfProps["tuple"].(map[string]any); tuple["prefixItems"] == nil || tuple["minItems"] != 1 {
		t.Fatalf("response_format schema must preserve prefixItems/minItems: %#v", tuple)
	}
}

func TestProtocolBuildBody_RejectsCyclicToolAndResponseSchemas(t *testing.T) {
	t.Parallel()
	cyclic := map[string]any{"type": "object"}
	cyclic["self"] = cyclic

	reqTool := protoReq("")
	reqTool.Tools = []llm.ToolDefinition{{Name: "cyc", Parameters: cyclic}}
	if _, err := (&Protocol{}).BuildBody(reqTool, protoRes(nil)); !errors.Is(err, openaichat.ErrCyclicSchema) {
		t.Fatalf("cyclic tool schema err = %v, want ErrCyclicSchema", err)
	}

	reqRF := protoReq("")
	reqRF.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: cyclic}
	if _, err := (&Protocol{}).BuildBody(reqRF, protoRes(nil)); !errors.Is(err, openaichat.ErrCyclicSchema) {
		t.Fatalf("cyclic response_format err = %v, want ErrCyclicSchema", err)
	}
}

func TestProtocolBuildBody_AnchorStoreAndExecutionModeGuards(t *testing.T) {
	t.Parallel()
	// Anchored call with fields.previous_interaction_id=true sets store=true and previous_interaction_id.
	resAnchored := protoRes(func(c *registry.Caps) { c.Fields["previous_interaction_id"] = true })
	req := protoReq("")
	req.PreviousResponseID = "int_prev"
	body := protoBuild(t, req, resAnchored)
	if body["previous_interaction_id"] != "int_prev" || body["store"] != true {
		t.Fatalf("anchored body = %v", body)
	}

	// Anchored with explicit store=false fails.
	reqBadStore := req
	reqBadStore.Store = new(false)
	if _, err := (&Protocol{}).BuildBody(reqBadStore, resAnchored); err == nil {
		t.Fatal("anchored call with store=false must fail")
	}

	// Anchored when fields.previous_interaction_id=false fails.
	if _, err := (&Protocol{}).BuildBody(req, protoRes(nil)); err == nil {
		t.Fatal("anchored call with fields.previous_interaction_id=false must fail")
	}

	// Unanchored with fields.store=false omits store, and rejects explicit req.Store.
	resNoStore := protoRes(func(c *registry.Caps) { c.Fields["store"] = false })
	bodyNoStore := protoBuild(t, protoReq(""), resNoStore)
	if _, has := bodyNoStore["store"]; has {
		t.Fatalf("fields.store=false must omit store: %v", bodyNoStore)
	}
	reqExplicitStore := protoReq("")
	reqExplicitStore.Store = new(true)
	if _, err := (&Protocol{}).BuildBody(reqExplicitStore, resNoStore); err == nil {
		t.Fatal("explicit store with fields.store=false must fail")
	}

	// Unsupported modes: agent, background=true, continuation_token, cached_content, service_tier=deferred, retired camelCase options.
	for name, mutate := range map[string]func(*llm.Request){
		"agent": func(r *llm.Request) {
			r.ProviderOptions = map[string]any{"google": map[string]any{"agent": "deep-research"}}
		},
		"background": func(r *llm.Request) { r.Background = new(true) },
		"continuation": func(r *llm.Request) {
			r.ProviderOptions = map[string]any{"google": map[string]any{"continuation_token": "tok"}}
		},
		"cached_content": func(r *llm.Request) {
			r.ProviderOptions = map[string]any{"google": map[string]any{"cached_content": "cc/1"}}
		},
		"deferred tier": func(r *llm.Request) { r.ServiceTier = "Deferred" },
		"retired camelCase": func(r *llm.Request) {
			r.ProviderOptions = map[string]any{"google": map[string]any{"generationConfig": map[string]any{}}}
		},
		"owned model": func(r *llm.Request) { r.ProviderOptions = map[string]any{"google": map[string]any{"model": "other"}} },
	} {
		t.Run(name, func(t *testing.T) {
			r := protoReq("")
			mutate(&r)
			if _, err := (&Protocol{}).BuildBody(r, resAnchored); err == nil {
				t.Fatalf("%s must fail BuildBody", name)
			}
		})
	}
}

func TestProtocolBuildBody_LabelsUnicodeAndProviderOptionsDeepClone(t *testing.T) {
	t.Parallel()
	req := protoReq("")
	origMeta := map[string]string{
		"équipe":   "alpha-1",
		"team/id":  "bad-slash",
		"trace_id": "trace/123",
		"EmptyVal": "ok", // uppercase first char invalid for Google labels
	}
	req.Metadata = origMeta
	body := protoBuild(t, req, protoRes(nil))
	labels, _ := body["labels"].(map[string]any)
	if len(labels) != 1 || labels["équipe"] != "alpha-1" {
		t.Fatalf("filtered labels = %#v, want map[équipe:alpha-1]", labels)
	}
	if len(origMeta) != 4 {
		t.Fatalf("req.Metadata was mutated: %#v", origMeta)
	}

	// Explicit invalid label in ProviderOptions fails with ConfigurationError.
	badReq := protoReq("")
	badReq.ProviderOptions = map[string]any{"google": map[string]any{
		"labels": map[string]string{"team/id": "ok"},
	}}
	if _, err := (&Protocol{}).BuildBody(badReq, protoRes(nil)); err == nil {
		t.Fatal("invalid explicit labels in ProviderOptions must fail")
	}

	// Deep clone check on ProviderOptions["google"].
	opts := map[string]any{
		"generation_config": map[string]any{"max_output_tokens": 1000},
		"labels":            map[string]string{"env": "prod"},
	}
	cloneReq := protoReq("")
	cloneReq.MaxTokens = new(100)
	cloneReq.ProviderOptions = map[string]any{"google": opts}
	cloneBody := protoBuild(t, cloneReq, protoRes(nil))
	cloneBody["labels"].(map[string]any)["env"] = "mutated"
	if opts["labels"].(map[string]string)["env"] != "prod" {
		t.Fatal("ProviderOptions labels mutated")
	}
	if opts["generation_config"].(map[string]any)["max_output_tokens"] != 1000 {
		t.Fatal("ProviderOptions generation_config mutated")
	}
}

func TestProtocolBuildBody_LazyUserInputCoalescingAndStepBoundaries(t *testing.T) {
	t.Parallel()
	req := llm.Request{
		Model: "gemini-3.8-flash",
		Messages: []llm.Message{
			llm.User("first user"),
			// Assistant message with only skipped non-Interactions thinking must NOT split user_input!
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "foreign", Signature: "sig-other"}},
			}},
			llm.User("second user"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "step1", GoogleStep: 1},
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/a.png"}, GoogleStep: 1},
				{Kind: llm.ContentText, Text: "step2", GoogleStep: 2},
			}},
		},
	}
	body := protoBuild(t, req, protoRes(nil))
	steps := body["input"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps len = %d, want 3 (1 coalesced user_input + 2 model_output): %#v", len(steps), steps)
	}
	u0 := steps[0].(map[string]any)
	if u0["type"] != "user_input" || len(u0["content"].([]any)) != 2 {
		t.Fatalf("coalesced user_input = %#v", u0)
	}
	m1 := steps[1].(map[string]any)
	m2 := steps[2].(map[string]any)
	if m1["type"] != "model_output" || len(m1["content"].([]any)) != 2 || m2["type"] != "model_output" || len(m2["content"].([]any)) != 1 {
		t.Fatalf("model_output step boundaries = %#v %#v", m1, m2)
	}
}

func TestProtocolBuildBody_InstanceWireIDProvenanceAndWebSearchRefusal(t *testing.T) {
	t.Parallel()
	res := protoRes(func(c *registry.Caps) { c.Reasoning = new(false) }) // Decoupled from reasoning=false!
	req := llm.Request{
		Model: "gemini-x",
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					GoogleInteraction: true, GoogleInstance: "gemini-prod", GoogleModel: "gemini-x",
					Signature: "sig_match", Summary: []string{"plan"}, Text: "plan",
				}},
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					GoogleInteraction: true, GoogleInstance: "gemini-prod", GoogleModel: "other-model",
					Signature: "sig_wrong_model", Summary: []string{"wrong"}, Text: "wrong",
				}},
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					GoogleInteraction: false, Signature: "sig_legacy", Text: "legacy",
				}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					GoogleInteraction: true, GoogleInstance: "gemini-prod", GoogleModel: "gemini-x",
					ID: "c_match", Name: "f", Arguments: []byte(`{}`), ThoughtSignature: "tc_sig_match",
				}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					GoogleInteraction: true, GoogleInstance: "other-inst", GoogleModel: "gemini-x",
					ID: "c_wrong_inst", Name: "f", Arguments: []byte(`{}`), ThoughtSignature: "tc_sig_wrong_inst",
				}},
			}},
		},
	}
	body := protoBuild(t, req, res)
	steps := body["input"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps = %#v, want 1 matching thought + 2 function_calls", steps)
	}
	if steps[0].(map[string]any)["type"] != "thought" || steps[0].(map[string]any)["signature"] != "sig_match" {
		t.Fatalf("step 0 = %#v", steps[0])
	}
	if steps[1].(map[string]any)["signature"] != "tc_sig_match" {
		t.Fatalf("step 1 missing matching signature: %#v", steps[1])
	}
	if _, has := steps[2].(map[string]any)["signature"]; has {
		t.Fatalf("step 2 must drop mismatched-instance signature: %#v", steps[2])
	}

	// RoleAssistant carrying ContentWebSearch must be rejected with ConfigurationError.
	wsReq := llm.Request{
		Model: "gemini-x",
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q"}}}},
		},
	}
	if _, err := (&Protocol{}).BuildBody(wsReq, res); err == nil {
		t.Fatal("ContentWebSearch replay must be rejected with ConfigurationError")
	}
}

func TestProtocolBuildBody_WebSearchNilCapsIsFailOpen(t *testing.T) {
	t.Parallel()
	req := protoReq("")
	req.WebSearch = true
	res := protoRes(nil)
	tools, _ := protoBuild(t, req, res)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["type"] != "google_search" {
		t.Fatalf("nil Caps.WebSearch is fail-open: %v", tools)
	}
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("explicit false must hold google_search back")
	}
	// Vertex Gemini Omni drops google_search even when WebSearch=true.
	omniRes := protoRes(func(c *registry.Caps) { c.WebSearch = new(true) })
	omniRes.Transport.Preset = registry.PresetVertexGemini
	omniRes.WireID = "gemini-omni-1.2-flash"
	if _, has := protoBuild(t, req, omniRes)["tools"]; has {
		t.Fatal("Vertex Gemini Omni must omit google_search")
	}
}

func TestProtocolBuildBody_MultimodalToolResultsCap(t *testing.T) {
	t.Parallel()
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shot", Arguments: []byte(`{}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shot", Content: "ok", ImageData: []byte{1, 2, 3}, ImageMediaType: "image/png"}}}},
	}}
	if _, err := (&Protocol{}).BuildBody(req, protoRes(nil)); err == nil {
		t.Fatal("tool-result images need MultimodalToolResults")
	}
	body := protoBuild(t, req, protoRes(func(c *registry.Caps) {
		c.InputModalities = []string{"text", "image"}
		c.MultimodalToolResults = new(true)
	}))
	fr := body["input"].([]any)[1].(map[string]any)
	sub, ok := fr["result"].([]map[string]any)
	if !ok || len(sub) != 2 || sub[1]["type"] != "image" {
		t.Fatalf("subcontent image expected inside function_result.result: %#v", fr)
	}
}

func TestProtocolBuildBody_ProviderOptionMaxOutputTokensRespectsCapsAndLowerWireValue(t *testing.T) {
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
			req := protoReq("")
			req.MaxTokens = new(100)
			req.ProviderOptions = map[string]any{registry.ProtocolGoogle: map[string]any{
				"generation_config": map[string]any{"max_output_tokens": tc.wire},
			}}
			body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.MaxOutputTokens = new(50) }))
			gen := body["generation_config"].(map[string]any)
			if got := gen["max_output_tokens"]; got != tc.want {
				t.Fatalf("generation_config.max_output_tokens = %v, want %d", got, tc.want)
			}
		})
	}
}

func TestProtocolUnsupportedToolChoiceCarriesTheInstance(t *testing.T) {
	t.Parallel()
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
