package responses

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// responsesFuzzImageDetails and responsesFuzzReasoningSummaries are the
// string-valued caps FuzzResponsesBuildBody flips, indexed by the fuzzed
// capSel byte (request.go: caps.ImageDetail, caps.ReasoningSummary).
var (
	responsesFuzzImageDetails       = []string{"high", "original", "omit"}
	responsesFuzzReasoningSummaries = []string{"", "auto", "detailed", "none"}
)

// buildFuzzResponsesRequest assembles a moderately rich llm.Request from
// fuzz primitives, routing the fuzzer's bytes into tool-call argument JSON,
// tool parameter schemas, and JSON-schema response formats, plus an image
// content part — the inputs buildBody (and toResponsesInput/toResponsesTools)
// have to survive. Ported from the openai adapter's buildFuzzRequest
// (requestbuild_fuzz_test.go).
func buildFuzzResponsesRequest(model, system, user string, toolArgs, toolParams []byte, sel byte) llm.Request {
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
	if sel&32 == 32 {
		// Image input, with the fuzzed string doubling as a detail hint —
		// exercises the per-model detail encoding (omitted on responses-lite).
		req.Messages[1].Content = append(req.Messages[1].Content, llm.ContentPart{
			Kind:  llm.ContentImage,
			Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01}, Detail: system},
		})
	}
	req.StopSequences = []string{system, user}
	return req
}

// FuzzResponsesBuildBody drives the Resolved-driven Responses request
// builder (buildBody), the path Complete/Stream use to turn an llm.Request
// into wire JSON. capSel additionally flips the row-level structural caps
// buildBody branches on: ResponsesLite, StrictTools, ImageDetail,
// ReasoningSummary, ThinkingAlwaysOn, StructuredOutput, and Reasoning.
//
// Oracles:
//   - buildBody never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON
//     and preserves "input";
//   - registry.Prune never panics on the built body;
//   - when ResponsesLite is set, input[0].type is "additional_tools" and
//     "tools" is absent from the top level (request.go's lite shape).
func FuzzResponsesBuildBody(f *testing.F) {
	f.Add("gpt-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0))
	f.Add("gpt-5", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3), byte(0))
	// gpt-5.6 takes the responses-lite request shape (no image detail and
	// reasoning always sent).
	f.Add("gpt-5.6-sol", "high", "look at this", []byte(`{"x":1}`), []byte(`{"type":"object"}`), byte(34), byte(1))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(0))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(22), byte(0))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel byte) {
		req := buildFuzzResponsesRequest(model, system, user, toolArgs, toolParams, sel)
		lite := capSel&1 == 1
		res := resolved(func(c *registry.Caps) {
			c.ResponsesLite = new(lite)
			c.StrictTools = new(capSel&2 == 2)
			c.ImageDetail = new(responsesFuzzImageDetails[int(capSel)%len(responsesFuzzImageDetails)])
			c.ReasoningSummary = new(responsesFuzzReasoningSummaries[int(capSel)%len(responsesFuzzReasoningSummaries)])
			c.ThinkingAlwaysOn = new(capSel&4 == 4)
			c.StructuredOutput = new(capSel&8 == 8)
			c.Reasoning = new(capSel&16 == 16)
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
		if _, ok := round["input"]; !ok {
			t.Fatalf("request body missing required field \"input\"\njson=%s", b)
		}

		registry.Prune(body, res.Caps) // must never panic

		if lite {
			input, _ := body["input"].([]any)
			if len(input) == 0 {
				t.Fatalf("lite body has no input: %#v", body)
			}
			first, _ := input[0].(map[string]any)
			if first["type"] != "additional_tools" {
				t.Fatalf("lite body's first input item must be additional_tools: %#v", first)
			}
			if _, has := body["tools"]; has {
				t.Fatalf("lite body must not carry top-level tools: %#v", body)
			}
		}
	})
}
