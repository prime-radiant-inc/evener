package openai

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildFuzzRequest assembles a moderately rich llm.Request from fuzz primitives,
// routing the fuzzer's bytes into tool-call argument JSON, tool parameter
// schemas, and JSON-schema response formats — the inputs the OpenAI request
// builders (buildRequestBody for Responses, buildChatCompletionsBody for the
// Chat Completions fallback) have to survive.
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
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: params, Strict: sel&16 == 16}
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

func fuzzRequestSeeds(f *testing.F) {
	f.Add("gpt-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0))
	f.Add("gpt-5", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(22))
}

func assertValidJSONBody(t *testing.T, body map[string]any, required ...string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("builder produced an unmarshalable body: %v\nbody=%#v", err, body)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
	}
	for _, k := range required {
		if _, ok := round[k]; !ok {
			t.Fatalf("request body missing required field %q\njson=%s", k, b)
		}
	}
}

// FuzzOpenAIResponsesRequestBuild drives the real Responses request-marshalling
// seam (buildRequestBody → toResponsesInput/toResponsesTools/strictifyJSONSchema),
// the path Complete/Stream use to build Responses-API wire JSON.
//
// Oracles: never panics; a successful build always re-marshals to valid JSON and
// preserves "model" and "input".
func FuzzOpenAIResponsesRequestBuild(f *testing.F) {
	fuzzRequestSeeds(f)
	a := &Adapter{}
	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		body, err := a.buildRequestBody(req)
		if err != nil {
			return
		}
		assertValidJSONBody(t, body, "model", "input")
	})
}

// FuzzOpenAIChatCompletionsRequestBuild drives the real Chat Completions
// fallback request builder (buildChatCompletionsBody → toChatMessages).
//
// Oracles: never panics; a successful build always re-marshals to valid JSON and
// preserves "model" and "messages".
func FuzzOpenAIChatCompletionsRequestBuild(f *testing.F) {
	fuzzRequestSeeds(f)
	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		body, err := buildChatCompletionsBody(req, sel&8 == 8)
		if err != nil {
			return
		}
		assertValidJSONBody(t, body, "model", "messages")
	})
}
