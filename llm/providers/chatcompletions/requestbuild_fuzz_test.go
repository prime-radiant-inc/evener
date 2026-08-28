package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// chatCompletionsFuzzDialects are the nine reasoning-control dialects
// applyThinkingFormat switches on (reasoning.go): "" and "openai" share one
// behavior, so "openai" stands in for both.
var chatCompletionsFuzzDialects = []string{
	"openai", "openrouter", "zai", "deepseek", "together",
	"qwen", "qwen-chat-template", "chat-template", "string-thinking",
}

// buildFuzzChatCompletionsRequest assembles a moderately rich llm.Request from
// fuzz primitives, routing the fuzzer's bytes into tool-call argument JSON,
// tool parameter schemas, and JSON-schema response formats — the inputs
// buildBody (and toChatMessages) have to survive. Ported from openaicompat's
// buildFuzzRequest (requestbuild_fuzz_test.go).
func buildFuzzChatCompletionsRequest(model, system, user string, toolArgs, toolParams []byte, sel byte) llm.Request {
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

// FuzzChatCompletionsBuildBody drives the Resolved-driven Chat Completions
// request builder (buildBody), the path Complete/Stream use to turn an
// llm.Request into wire JSON. capSel additionally flips the row-level
// structural caps buildBody branches on: StrictTools, StructuredOutput,
// ToolChoiceForcing, ThinkingFormat (indexed into the nine dialects),
// ThinkingAlwaysOn, Reasoning, and CacheControl.
//
// Oracles:
//   - buildBody never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON;
//   - registry.Prune never panics on the built body.
func FuzzChatCompletionsBuildBody(f *testing.F) {
	f.Add("compat-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0))
	f.Add("glm-4", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3), byte(255))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(17))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20), byte(42))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel byte) {
		req := buildFuzzChatCompletionsRequest(model, system, user, toolArgs, toolParams, sel)
		res := resolved(func(c *registry.Caps) {
			c.StrictTools = new(capSel&1 == 1)
			c.StructuredOutput = new(capSel&2 == 2)
			c.ToolChoiceForcing = new(capSel&4 == 4)
			c.ThinkingAlwaysOn = new(capSel&8 == 8)
			c.Reasoning = new(capSel&16 == 16)
			if capSel&32 == 32 {
				c.CacheControl = new("anthropic")
			}
			c.ThinkingFormat = new(chatCompletionsFuzzDialects[int(capSel)%len(chatCompletionsFuzzDialects)])
		})

		body, err := buildBody(req, res, sel&8 == 8)
		if err != nil {
			return // structured build error (e.g. unsupported tool_choice) is acceptable.
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}

		registry.Prune(body, res.Caps) // must never panic
	})
}
