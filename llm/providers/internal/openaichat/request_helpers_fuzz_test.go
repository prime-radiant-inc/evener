package openaichat

import (
	"bytes"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzToChatResponseFormat drives ToChatResponseFormat, the shared helper that
// maps an llm.ResponseFormat onto the Chat Completions "response_format" object.
// Both the openai and openaicompat adapters route through it, so a wire-shape
// drift here desyncs every OpenAI-compatible request.
//
// Oracles beyond no-panic:
//   - the result is always a non-nil, JSON-marshalable map carrying a "type"
//     that is one of the three legal wire values ("json_object", "json_schema",
//     "text") — never an empty or invented type;
//   - "json_schema" with a non-nil schema nests {name:"response", schema, strict?}
//     and only sets "strict" when the request asked for it;
//   - the mapping is deterministic for a given ResponseFormat.
func FuzzToChatResponseFormat(f *testing.F) {
	f.Add("json", []byte(`{"type":"object"}`), false)
	f.Add("json_object", []byte(`null`), true)
	f.Add("json_schema", []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), true)
	f.Add("json_schema", []byte(`not json`), false)
	f.Add("text", []byte(``), false)
	f.Add("weird-unknown", []byte(`{}`), true)

	f.Fuzz(func(t *testing.T, typ string, schemaBytes []byte, strict bool) {
		var schema map[string]any
		_ = json.Unmarshal(schemaBytes, &schema)

		rf := llm.ResponseFormat{Type: typ, JSONSchema: schema, Strict: strict}
		got := ToChatResponseFormat(rf)
		if got == nil {
			t.Fatalf("ToChatResponseFormat returned nil for %#v", rf)
		}

		// Must be JSON-marshalable wire output.
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("ToChatResponseFormat produced an unmarshalable map: %v\nmap=%#v", err, got)
		}

		wireType, _ := got["type"].(string)
		switch wireType {
		case "json_object", "json_schema", "text":
		default:
			t.Fatalf("ToChatResponseFormat type=%q, want one of json_object/json_schema/text\njson=%s", wireType, b)
		}

		if wireType == "json_schema" && schema != nil {
			sub, ok := got["json_schema"].(map[string]any)
			if !ok {
				t.Fatalf("json_schema output missing nested json_schema object\njson=%s", b)
			}
			if name, _ := sub["name"].(string); name != "response" {
				t.Fatalf("json_schema name=%q, want \"response\"\njson=%s", name, b)
			}
			_, hasStrict := sub["strict"]
			if hasStrict != strict {
				t.Fatalf("json_schema strict-present=%v, want %v (Strict=%v)\njson=%s", hasStrict, strict, strict, b)
			}
		}

		if again := ToChatResponseFormat(rf); !jsonEqual(t, got, again) {
			t.Fatalf("ToChatResponseFormat not deterministic for %#v", rf)
		}
	})
}

// FuzzToChatTools drives ToChatTools, the shared helper that converts tool
// definitions into the Chat Completions tools array. Both OpenAI-family adapters
// rely on it for an exact, lossless tool translation.
//
// Oracles beyond no-panic:
//   - count preservation: len(out) == len(tools) (no tool dropped or invented);
//   - each entry is {type:"function", function:{name, description?, parameters?}}
//     with function.name byte-identical to the source tool's Name;
//   - description/parameters appear iff the source field was non-empty/non-nil;
//   - the whole array re-marshals to valid JSON.
func FuzzToChatTools(f *testing.F) {
	f.Add("shell", "run a command", []byte(`{"type":"object"}`), "read", "", []byte(`null`))
	f.Add("", "", []byte(`not json`), "", "desc", []byte(`{"x":1}`))
	f.Add("t\x00name", "d", []byte(`{}`), "u", "d2", []byte(``))

	f.Fuzz(func(t *testing.T, n1, d1 string, p1 []byte, n2, d2 string, p2 []byte) {
		tools := []llm.ToolDefinition{
			{Name: n1, Description: d1, Parameters: unmarshalParams(p1)},
			{Name: n2, Description: d2, Parameters: unmarshalParams(p2)},
		}

		out := ToChatTools(tools)
		if len(out) != len(tools) {
			t.Fatalf("ToChatTools len=%d, want %d", len(out), len(tools))
		}

		for i, entry := range out {
			if entry["type"] != "function" {
				t.Fatalf("tool[%d] type=%v, want function", i, entry["type"])
			}
			fn, ok := entry["function"].(map[string]any)
			if !ok {
				t.Fatalf("tool[%d] function is %T, want map", i, entry["function"])
			}
			if name, _ := fn["name"].(string); name != tools[i].Name {
				t.Fatalf("tool[%d] name=%q, want %q", i, name, tools[i].Name)
			}
			_, hasDesc := fn["description"]
			if hasDesc != (tools[i].Description != "") {
				t.Fatalf("tool[%d] description-present=%v, want %v", i, hasDesc, tools[i].Description != "")
			}
			_, hasParams := fn["parameters"]
			if hasParams != (tools[i].Parameters != nil) {
				t.Fatalf("tool[%d] parameters-present=%v, want %v", i, hasParams, tools[i].Parameters != nil)
			}
		}

		if _, err := json.Marshal(out); err != nil {
			t.Fatalf("ToChatTools produced an unmarshalable array: %v\nout=%#v", err, out)
		}
	})
}

func unmarshalParams(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func jsonEqual(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
