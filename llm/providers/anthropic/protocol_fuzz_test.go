package anthropic

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// anthropicFuzzThinkingShapes are indexed by FuzzAnthropicProtocolBuildBody's
// capSel byte to drive registry.Caps.ThinkingShape (protocol_request.go's
// applyThinkingShape): "" leaves the cap unset.
var anthropicFuzzThinkingShapes = []string{"", "adaptive", "budget", "budget+effort"}

// anthropicFuzzThinkingDisplays are indexed the same way for ThinkingDisplay.
var anthropicFuzzThinkingDisplays = []string{"", "summarized", "omitted"}

// anthropicFuzzCacheTTLs are indexed the same way for CacheTTL.
var anthropicFuzzCacheTTLs = []string{"", "5m", "1h"}

// FuzzAnthropicProtocolBuildBody drives the Resolved-driven Messages request
// builder (buildProtocolBody), the path Complete/Stream use to turn an
// llm.Request into wire JSON. It reuses buildFuzzRequest
// (requestbuild_fuzz_test.go) for the request bytes and additionally flips,
// via capSel, the row-level caps buildProtocolBody and applyThinkingShape
// branch on: ThinkingShape, ThinkingDisplay, ThinkingAlwaysOn, WebSearch,
// CacheTTL, and MaxOutputTokens.
//
// Oracles:
//   - buildProtocolBody never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON;
//   - registry.Prune never panics on the built body;
//   - when the body carries thinking.budget_tokens, max_tokens strictly
//     exceeds it (the contract reconcileThinkingContract enforces).
func FuzzAnthropicProtocolBuildBody(f *testing.F) {
	f.Add("claude-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0))
	f.Add("claude-opus-4-6", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3), byte(255))
	f.Add("model[1m]", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(2))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20), byte(64))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		res := protoRes(func(c *registry.Caps) {
			if shape := anthropicFuzzThinkingShapes[int(capSel)%len(anthropicFuzzThinkingShapes)]; shape != "" {
				c.ThinkingShape = new(shape)
			}
			if display := anthropicFuzzThinkingDisplays[int(capSel>>2)%len(anthropicFuzzThinkingDisplays)]; display != "" {
				c.ThinkingDisplay = new(display)
			}
			c.ThinkingAlwaysOn = new(capSel&8 == 8)
			c.WebSearch = new(capSel&16 == 16)
			if ttl := anthropicFuzzCacheTTLs[int(capSel>>5)%len(anthropicFuzzCacheTTLs)]; ttl != "" {
				c.CacheTTL = new(ttl)
			}
			if capSel&64 == 64 {
				maxOut := int(capSel) * 512
				c.MaxOutputTokens = new(maxOut)
			}
		})

		body, err := buildProtocolBody(req, res)
		if err != nil {
			return // a structured build error (e.g. unsupported tool_choice) is acceptable.
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildProtocolBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}

		registry.Prune(body, res.Caps) // must never panic

		if thinking, ok := body["thinking"].(map[string]any); ok {
			if budget, ok := thinking["budget_tokens"].(int); ok {
				mt, _ := body["max_tokens"].(int)
				if mt <= budget {
					t.Fatalf("max_tokens %d does not exceed thinking budget %d: body=%#v", mt, budget, body)
				}
			}
		}
	})
}

// buildFuzzRequest assembles a moderately rich llm.Request from fuzz primitives:
// a system+user+assistant(tool_call)+tool-result conversation, one tool whose
// JSON-Schema parameters come straight from the fuzzer, and tool-choice /
// response-format / sampling knobs steered by sel. It deliberately routes the
// fuzzer's bytes into the bug-prone spots — tool-call argument JSON, tool
// parameter schemas, and JSON-schema response formats — that the request
// marshaller has to handle.
func buildFuzzRequest(model, system, user string, toolArgs, toolParams []byte, sel byte) llm.Request {
	var params map[string]any
	_ = json.Unmarshal(toolParams, &params)

	req := llm.Request{
		Model: model,
		Messages: []llm.Message{
			llm.System(system),
			llm.User(user),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "t",
					Arguments: json.RawMessage(toolArgs),
					Type:      "function",
				},
			}}},
			llm.ToolResult("call_1", user, sel&1 == 1),
		},
		Tools: []llm.ToolDefinition{{Name: "t", Description: system, Parameters: params}},
	}

	switch sel % 5 {
	case 0:
		req.ToolChoice = &llm.ToolChoice{Mode: "auto"}
	case 1:
		req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	case 2:
		req.ToolChoice = &llm.ToolChoice{Mode: "none"}
	case 3:
		req.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "t"}
	case 4:
		req.ToolChoice = &llm.ToolChoice{Mode: model} // arbitrary / unsupported mode
	}

	switch (sel >> 3) % 4 {
	case 1:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json"}
	case 2:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: params}
	case 3:
		req.ResponseFormat = &llm.ResponseFormat{Type: model}
	}

	temp := 0.5
	req.Temperature = &temp
	tp := 0.9
	req.TopP = &tp
	mt := int(sel) * 8
	req.MaxTokens = &mt
	if sel&2 == 2 {
		eff := "high"
		req.ReasoningEffort = &eff
	}
	req.WebSearch = sel&4 == 4
	req.StopSequences = []string{system, user}

	// Provider options are merged into the body AFTER the contract guards run, so
	// route the fuzzer into anthropic overrides that can re-set tool_choice /
	// max_tokens — the spot a forced choice or a sub-budget max_tokens could slip
	// past the guards and produce a request Anthropic would 400.
	switch (sel >> 5) % 4 {
	case 1:
		req.ProviderOptions = map[string]any{"anthropic": map[string]any{"tool_choice": map[string]any{"type": "any"}}}
	case 2:
		req.ProviderOptions = map[string]any{"anthropic": map[string]any{"tool_choice": map[string]any{"type": "tool", "name": "t"}}}
	case 3:
		req.ProviderOptions = map[string]any{"anthropic": map[string]any{"max_tokens": 0}}
	}
	return req
}
