package google

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// FuzzGoogleProtocolBuildBody drives the Resolved-driven generateContent
// request builder (Protocol.BuildBody), the path Complete/Stream use to turn
// an llm.Request into Gemini wire JSON. It reuses buildFuzzRequest
// (requestbuild_fuzz_test.go) for the request bytes and additionally flips,
// via capSel, the row-level caps BuildBody and toGeminiContents branch on:
// MultimodalToolResults, WebSearch, and Reasoning.
//
// Oracles:
//   - BuildBody never panics (floor);
//   - when the build succeeds, the result always re-marshals to valid JSON;
//   - the body never carries a "model" key (the model rides in the URL);
//   - "tools" never carries both google_search and functionDeclarations.
func FuzzGoogleProtocolBuildBody(f *testing.F) {
	f.Add("gemini-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0))
	f.Add("gemini-2.5-pro", "", "", []byte(`{}`), []byte(`{"type":["string","null"]}`), byte(3), byte(255))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`{"$ref":"#/x"}`), byte(11), byte(2))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20), byte(4))

	p := &Protocol{}

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		res := protoRes(func(c *registry.Caps) {
			c.MultimodalToolResults = new(capSel&1 == 1)
			c.WebSearch = new(capSel&2 == 2)
			c.Reasoning = new(capSel&4 == 4)
		})

		body, err := p.BuildBody(req, res)
		if err != nil {
			return // a structured build error (e.g. unsupported tool_choice, tool-result image without the cap) is acceptable.
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("BuildBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if _, has := round["model"]; has {
			t.Fatalf("request body must never carry a model key: json=%s", b)
		}
		if tools, ok := round["tools"].([]any); ok {
			hasSearch, hasFuncs := false, false
			for _, toolAny := range tools {
				tool, ok := toolAny.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := tool["google_search"]; ok {
					hasSearch = true
				}
				if _, ok := tool["functionDeclarations"]; ok {
					hasFuncs = true
				}
			}
			if hasSearch && hasFuncs {
				t.Fatalf("tools must never carry both google_search and functionDeclarations: json=%s", b)
			}
		}
	})
}
