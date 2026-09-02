package anthropic

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func protoRes(mutate func(c *registry.Caps)) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolAnthropic), MaxOutputTokens: new(64000), Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if mutate != nil {
		mutate(&caps)
	}
	return registry.Resolved{Instance: "anthropic-prod", Protocol: registry.ProtocolAnthropic, ModelID: "claude-x", WireID: "claude-x-wire", Transport: registry.Transport{Endpoint: "/messages"}, Caps: caps}
}

func protoReq(effort string) llm.Request {
	req := llm.Request{Model: "claude-x", Messages: []llm.Message{
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
	p, ok := llm.ProtocolFor(registry.ProtocolAnthropic)
	if !ok {
		t.Fatal("anthropic not registered")
	}
	if got, want := p.PrunablePaths(), registry.PrunablePaths(registry.ProtocolAnthropic); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrunablePaths = %v, want %v", got, want)
	}
}

// TestProtocolBuildBody_WebSearchNilCapsIsFailOpen pins the mechanism
// behind issue #738's endpoint gate on this protocol too (the Responses
// twin is TestBuildBody_WebSearchNilCapsIsFailOpen): the gate here,
// req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
// (protocol_request.go), treats an unset capability as permissive. That is
// the right default for a model this adapter has no catalog opinion about -
// trust the caller's own req.WebSearch - but it means the registry's
// endpoint gate (llm/registry/resolve.go, gateWebSearch) can never
// represent "denied because this endpoint is not the vendor's first-party
// API" as a bare nil: a caller that sets req.WebSearch = true without
// consulting registry.Caps would still get the hosted tool sent to an
// endpoint that rejects it. The registry closes this by carrying an
// explicit false, never nil; this test pins why that is necessary at this
// layer - nil is let through right here, by design - and that the
// explicit false actually holds the tool back.
func TestProtocolBuildBody_WebSearchNilCapsIsFailOpen(t *testing.T) {
	req := protoReq("")
	req.WebSearch = true
	res := protoRes(nil) // Caps.WebSearch left nil, the state a gated instance must never carry
	if res.Caps.WebSearch != nil {
		t.Fatal("test setup: protoRes(nil) must leave WebSearch nil")
	}
	tools, _ := protoBuild(t, req, res)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["type"] != "web_search_20250305" {
		t.Fatalf("nil Caps.WebSearch is fail-open at this layer: a caller setting req.WebSearch still gets the tool: %v", tools)
	}
	if _, has := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.WebSearch = new(false) }))["tools"]; has {
		t.Fatal("an explicit false must hold the web search tool back")
	}
}

func TestProtocolBuildBody_ThinkingShapes(t *testing.T) {
	cases := []struct {
		name         string
		shape        string
		display      string
		alwaysOn     bool
		effort       string
		wantThinking map[string]any
		wantEffort   any // output_config.effort; nil means absent
	}{
		{"unset shape sends nothing", "", "", false, "high", nil, nil},
		{"adaptive always-on no effort no display", "adaptive", "", true, "", map[string]any{"type": "adaptive"}, nil},
		{"adaptive with display and effort", "adaptive", "summarized", true, "high", map[string]any{"type": "adaptive", "display": "summarized"}, "high"},
		{"adaptive not always-on and no effort", "adaptive", "summarized", false, "", nil, nil},
		{"budget", "budget", "", false, "high", map[string]any{"type": "enabled", "budget_tokens": float64(llm.ReasoningBudget("high"))}, nil},
		{"budget without effort", "budget", "", false, "", nil, nil},
		{"budget+effort", "budget+effort", "", false, "high", map[string]any{"type": "enabled", "budget_tokens": float64(llm.ReasoningBudget("high"))}, "high"},
		// An explicit off means the user turned thinking off, so it must not
		// fall through to the always-on body: no thinking object at all.
		{"none clears everything, always-on included", "adaptive", "summarized", true, "none", nil, nil},
		{"none clears the budget shape too", "budget+effort", "", false, "none", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := protoRes(func(caps *registry.Caps) {
				if c.shape != "" {
					caps.ThinkingShape = new(c.shape)
				}
				if c.display != "" {
					caps.ThinkingDisplay = new(c.display)
				}
				if c.alwaysOn {
					caps.ThinkingAlwaysOn = new(true)
				}
			})
			raw, _ := json.Marshal(protoBuild(t, protoReq(c.effort), res))
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if !reflect.DeepEqual(body["thinking"], anyOrNil(c.wantThinking)) {
				t.Fatalf("thinking = %v, want %v", body["thinking"], c.wantThinking)
			}
			var gotEffort any
			if oc, ok := body["output_config"].(map[string]any); ok {
				gotEffort = oc["effort"]
			}
			if gotEffort != c.wantEffort {
				t.Fatalf("output_config.effort = %v, want %v", gotEffort, c.wantEffort)
			}
		})
	}
}

func anyOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func TestProtocolBuildBody_CapsAndRequestFields(t *testing.T) {
	req := protoReq("high")
	req.MaxTokens = new(64000)
	req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.WebSearch = true
	req.Metadata = map[string]string{"user_id": "u1", "trace": "t"}
	req.ServiceTier = "auto"
	req.StopSequences = []string{"END"}
	res := protoRes(func(c *registry.Caps) { c.ThinkingShape = new("budget"); c.CacheTTL = new("1h") })
	body := protoBuild(t, req, res)
	if body["model"] != "claude-x-wire" || body["max_tokens"] != 64000 {
		t.Fatalf("model/max_tokens: %v %v", body["model"], body["max_tokens"])
	}
	if body["metadata"].(map[string]any)["user_id"] != "u1" || len(body["metadata"].(map[string]any)) != 1 || body["service_tier"] != "auto" {
		t.Fatalf("metadata/service_tier: %v %v", body["metadata"], body["service_tier"])
	}
	tools := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[1]["type"] != "web_search_20250305" || tools[1]["cache_control"].(map[string]any)["ttl"] != "1h" {
		t.Fatalf("tools: %v", tools)
	}
	if tc := body["tool_choice"].(map[string]any); tc["type"] != "auto" {
		t.Fatalf("forcing must downgrade to auto while thinking is on: %v", tc)
	}
	if sys := body["system"].([]map[string]any)[0]["cache_control"].(map[string]any); sys["ttl"] != "1h" {
		t.Fatalf("system marker: %v", sys)
	}
	if got := registry.Prune(body, res.Caps); !reflect.DeepEqual(got, []string{"metadata", "service_tier"}) {
		t.Fatalf("baseline prunes metadata and service_tier: %v", got)
	}

	noWeb := protoRes(func(c *registry.Caps) { c.WebSearch = new(false) })
	if tools := protoBuild(t, req, noWeb)["tools"].([]map[string]any); len(tools) != 1 {
		t.Fatalf("WebSearch=false drops the web search tool: %v", tools)
	}

	vertex := protoRes(nil)
	vertex.Transport.Endpoint = "/publishers/anthropic/models/{model}:rawPredict"
	if b := protoBuild(t, protoReq(""), vertex); b["model"] != nil {
		t.Fatal("a {model} endpoint sends no model in the body")
	}

	unknown := protoRes(func(c *registry.Caps) { c.MaxOutputTokens = nil })
	if b := protoBuild(t, protoReq(""), unknown); b["max_tokens"] != fallbackMaxTokens {
		t.Fatalf("max_tokens fallback = %v", b["max_tokens"])
	}
}

func TestProtocolBuildBody_HighThinkingRejectsUnknownOutputCap(t *testing.T) {
	res := protoRes(func(c *registry.Caps) {
		c.MaxOutputTokens = nil
		c.ThinkingShape = new("budget")
	})
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(protoReq("high"), res), res)
	if body != nil {
		t.Fatalf("body = %v, want no unsafe request", body)
	}
	var budgetErr *llm.ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v, want *llm.ContextBudgetError", err)
	}
	if budgetErr.Limit != "max_output_tokens" || budgetErr.Maximum != fallbackMaxTokens || budgetErr.OutputTokens != llm.ReasoningBudget("high")+1 {
		t.Fatalf("ContextBudgetError = %+v, want fail-closed unknown-cap high effort", budgetErr)
	}
}

func TestProtocolBuildBody_ProviderOptionMaxTokensRespectsCapsAndLowerWireValue(t *testing.T) {
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
			req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{"max_tokens": tc.wire}}
			body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.MaxOutputTokens = new(50) }))
			if got := body["max_tokens"]; got != tc.want {
				t.Fatalf("max_tokens = %v, want %d", got, tc.want)
			}
		})
	}
}

func TestProtocolBuildBody_ProviderOnlyThinkingBudgetFailsWithinAdmittedMax(t *testing.T) {
	req := protoReq("")
	req.MaxTokens = new(1000)
	req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
	}}
	res := protoRes(nil)
	_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	budgetErr, ok := errors.AsType[*llm.ContextBudgetError](err)
	if !ok {
		t.Fatalf("error = %v, want *llm.ContextBudgetError", err)
	}
	if llm.Kind(err) != llm.KindContextLength {
		t.Fatalf("Kind(err) = %v, want %v", llm.Kind(err), llm.KindContextLength)
	}
	if budgetErr.Provider != "anthropic-prod" || budgetErr.Model != "claude-x" || budgetErr.Limit != "max_output_tokens" || budgetErr.Maximum != 1000 || budgetErr.OutputTokens != 1025 {
		t.Fatalf("ContextBudgetError = %+v, want provider=anthropic-prod model=claude-x limit=max_output_tokens maximum=1000 output=1025", budgetErr)
	}
}

func TestProtocolBuildBody_MixedCaseProviderThinkingBudgetFailsWithinAdmittedMax(t *testing.T) {
	req := protoReq("")
	req.MaxTokens = new(1000)
	req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{
		"thinking": map[string]any{"type": " ENABLED ", "budget_tokens": 1024},
	}}
	_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, protoRes(nil)), protoRes(nil))
	if _, ok := errors.AsType[*llm.ContextBudgetError](err); !ok {
		t.Fatalf("error = %v, want *llm.ContextBudgetError", err)
	}
}

func TestProtocolBuildBody_FinalThinkingOverlayStateControlsReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		thinking       any
		wantToolChoice string
		wantBudget     any
	}{
		{name: "overlay lowers shaped budget", thinking: map[string]any{"type": "enabled", "budget_tokens": 512}, wantToolChoice: "auto", wantBudget: 512},
		{name: "overlay replaces shaped budget with adaptive thinking", thinking: map[string]any{"type": "adaptive"}, wantToolChoice: "auto", wantBudget: nil},
		{name: "overlay disables shaped thinking", thinking: map[string]any{"type": "disabled"}, wantToolChoice: "any", wantBudget: nil},
		{name: "overlay removes shaped thinking", thinking: nil, wantToolChoice: "any", wantBudget: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := protoReq("low")
			req.MaxTokens = new(1000)
			req.Tools = []llm.ToolDefinition{{Name: "f", Parameters: map[string]any{"type": "object"}}}
			req.ToolChoice = &llm.ToolChoice{Mode: "required"}
			req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{"thinking": tc.thinking}}
			body := protoBuild(t, req, protoRes(func(c *registry.Caps) { c.ThinkingShape = new("budget") }))
			if got := body["tool_choice"].(map[string]any)["type"]; got != tc.wantToolChoice {
				t.Fatalf("tool_choice.type = %v, want %q", got, tc.wantToolChoice)
			}
			thinking, _ := body["thinking"].(map[string]any)
			got := any(nil)
			if thinking != nil {
				got = thinking["budget_tokens"]
			}
			if !reflect.DeepEqual(got, tc.wantBudget) {
				t.Fatalf("thinking.budget_tokens = %v, want %v (body=%v)", got, tc.wantBudget, body["thinking"])
			}
		})
	}
}

func TestBetaHeaderMergesRowAndCallerBetas(t *testing.T) {
	res := protoRes(nil)
	res.Headers = map[string]string{"anthropic-beta": "context-1m-2025-08-07"}
	req := protoReq("")
	req.ProviderOptions = map[string]any{"anthropic": map[string]any{"beta_headers": []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"}}}
	if got := betaHeader(res, req); got != "context-1m-2025-08-07,interleaved-thinking-2025-05-14" {
		t.Fatalf("beta header = %q", got)
	}
	if got := betaHeader(protoRes(nil), protoReq("")); got != "" {
		t.Fatalf("no betas = %q", got)
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
