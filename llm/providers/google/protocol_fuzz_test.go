package google

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func FuzzGoogleProtocolBuildBody(f *testing.F) {
	f.Add("gemini-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0))
	f.Add("gemini-3.8-flash", "", "", []byte(`{}`), []byte(`{"type":["string","null"]}`), byte(3), byte(255))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`{"$ref":"#/x"}`), byte(11), byte(2))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20), byte(4))
	f.Add("gemini-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(2), byte(0))

	p := &Protocol{}

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		res := protoRes(func(c *registry.Caps) {
			c.MultimodalToolResults = new(capSel&1 == 1)
			c.WebSearch = new(capSel&2 == 2)
			c.Reasoning = new(capSel&4 == 4)
			c.CombinedSearchAndFunctions = new(capSel&8 == 8)
		})
		if capSel&16 == 16 {
			req.Store = new(true)
			res.Caps.Fields["previous_interaction_id"] = true
		}

		body, err := p.BuildBody(req, res)
		if err != nil {
			return
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("BuildBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if round["model"] != res.WireID {
			t.Fatalf("request body must carry model=%q: json=%s", res.WireID, b)
		}
		if tools, ok := round["tools"].([]any); ok && !registry.BoolValue(res.Caps.CombinedSearchAndFunctions) {
			hasSearch, hasFuncs := false, false
			for _, toolAny := range tools {
				tool, ok := toolAny.(map[string]any)
				if !ok {
					continue
				}
				switch tool["type"] {
				case "google_search":
					hasSearch = true
				case "function":
					hasFuncs = true
				}
			}
			if hasSearch && hasFuncs {
				t.Fatalf("tools must not combine google_search and function when CombinedSearchAndFunctions=false: json=%s", b)
			}
		}
	})
}

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
