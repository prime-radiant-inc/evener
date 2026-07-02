package openaicompat

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildFuzzRequest assembles a moderately rich llm.Request from fuzz primitives,
// routing the fuzzer's bytes into tool-call argument JSON, tool parameter
// schemas, and JSON-schema response formats — the inputs the OpenAI-compatible
// chat request builder (buildRequestBody + toChatMessages) has to survive.
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
		req.ToolChoice = &llm.ToolChoice{Mode: model}
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
	return req
}

// FuzzOpenAICompatRequestBuild drives the real OpenAI-compatible chat request
// builder (buildRequestBody), the path Complete/Stream use to turn an llm.Request
// into Chat Completions wire JSON. This builder backs every openaicompat-derived
// provider (glm, ollama, openrouter, kimicoding, …).
//
// Oracles:
//   - never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON and
//     preserves the required "model" and "messages" fields.
func FuzzOpenAICompatRequestBuild(f *testing.F) {
	f.Add("compat-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0))
	f.Add("glm-4", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)

		body, err := buildRequestBody(req, sel&8 == 8, ModelCompat{})
		if err != nil {
			return // structured build error (e.g. unsupported tool_choice) is acceptable.
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildRequestBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		for _, k := range []string{"model", "messages"} {
			if _, ok := round[k]; !ok {
				t.Fatalf("request body missing required field %q\njson=%s", k, b)
			}
		}
	})
}
