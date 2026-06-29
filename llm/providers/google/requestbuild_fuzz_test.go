package google

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildFuzzRequest assembles a moderately rich llm.Request from fuzz primitives,
// routing the fuzzer's bytes into tool-call argument JSON, tool parameter
// schemas, and JSON-schema response formats — the spots the Gemini request
// translation (toGeminiContents + buildRequestBody + sanitizeGeminiSchema) has
// to survive.
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

// FuzzGoogleRequestBuild drives the real Gemini request-marshalling seam
// (toGeminiContents → buildRequestBody → encoding/json.Marshal), the path
// Complete/Stream use to turn an llm.Request into generateContent wire JSON.
//
// Oracles:
//   - never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON and
//     preserves the required "contents" field.
func FuzzGoogleRequestBuild(f *testing.F) {
	f.Add("gemini-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0))
	f.Add("gemini-2.5-pro", "", "", []byte(`{}`), []byte(`{"type":["string","null"]}`), byte(3))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`{"$ref":"#/x"}`), byte(11))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20))

	a := &Adapter{}

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)

		sys, contents, err := toGeminiContents(req.Messages)
		if err != nil {
			return // unsupported content shape is an acceptable structured error.
		}
		body, err := a.buildRequestBody(req, sys, contents)
		if err != nil {
			return
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildRequestBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if _, ok := round["contents"]; !ok {
			t.Fatalf("request body missing required field \"contents\"\njson=%s", b)
		}
	})
}
