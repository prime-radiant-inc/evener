package anthropic

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

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

// FuzzAnthropicRequestBuild drives the real Anthropic request-marshalling seam
// (buildRequestBody → encoding/json.Marshal), the path Complete/Stream use to
// turn an llm.Request into Messages-API wire JSON.
//
// Oracles:
//   - never panics for any constructed request (floor);
//   - when buildRequestBody succeeds, the result always re-marshals to valid
//     JSON (no unmarshalable values leak into the body) and preserves the
//     required "model", "max_tokens", and "messages" fields.
func FuzzAnthropicRequestBuild(f *testing.F) {
	f.Add("claude-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0))
	f.Add("claude-opus-4-6", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3))
	f.Add("model[1m]", "sys", "u", []byte(`not json`), []byte(`null`), byte(11))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20))

	a := &Adapter{}

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)

		body, err := a.buildRequestBody(req)
		if err != nil {
			return // a structured build error (e.g. unsupported tool_choice) is acceptable.
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildRequestBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		for _, k := range []string{"model", "max_tokens", "messages"} {
			if _, ok := round[k]; !ok {
				t.Fatalf("request body missing required field %q\njson=%s", k, b)
			}
		}
	})
}
