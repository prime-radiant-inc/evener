package openaichat

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestToolArgumentsString(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "object", raw: json.RawMessage(`{"status":"in_progress"}`), want: `{"status":"in_progress"}`},
		{name: "empty", raw: nil, want: `{}`},
		{name: "malformed", raw: json.RawMessage(`{"status": in_progress"}`), want: `{}`},
		{name: "non_object", raw: json.RawMessage(`["status"]`), want: `{}`},
		{name: "null", raw: json.RawMessage(`null`), want: `{}`},
		// whitespace_object exercises the bytes.TrimSpace path: trimmed != raw
		{name: "whitespace_object", raw: json.RawMessage("  {\"k\":\"v\"}  "), want: `{"k":"v"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolArgumentsString(tc.raw); got != tc.want {
				t.Fatalf("ToolArgumentsString(%q) = %q, want %q", string(tc.raw), got, tc.want)
			}
		})
	}
}

func TestToChatResponseFormat(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}

	for _, tc := range []struct {
		name      string
		rf        llm.ResponseFormat
		wantType  string
		wantKeys  []string
		absentKey string
		strictVal any // nil means don't check
	}{
		{
			name:     "json_alias",
			rf:       llm.ResponseFormat{Type: "json"},
			wantType: "json_object",
		},
		{
			name:     "json_object",
			rf:       llm.ResponseFormat{Type: "json_object"},
			wantType: "json_object",
		},
		{
			name:      "json_schema_no_schema",
			rf:        llm.ResponseFormat{Type: "json_schema"},
			wantType:  "json_schema",
			absentKey: "json_schema",
		},
		{
			name:      "json_schema_with_schema_non_strict",
			rf:        llm.ResponseFormat{Type: "json_schema", JSONSchema: schema},
			wantType:  "json_schema",
			wantKeys:  []string{"json_schema"},
			strictVal: nil, // strict key must be absent
		},
		{
			name:      "json_schema_with_schema_strict",
			rf:        llm.ResponseFormat{Type: "json_schema", JSONSchema: schema, Strict: true},
			wantType:  "json_schema",
			wantKeys:  []string{"json_schema"},
			strictVal: true,
		},
		{
			name:     "text",
			rf:       llm.ResponseFormat{Type: "text"},
			wantType: "text",
		},
		{
			name:     "default_empty",
			rf:       llm.ResponseFormat{},
			wantType: "text",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ToChatResponseFormat(tc.rf)

			// exact "type" value
			if got["type"] != tc.wantType {
				t.Fatalf("type = %q, want %q", got["type"], tc.wantType)
			}

			// required top-level keys present
			for _, k := range tc.wantKeys {
				if _, ok := got[k]; !ok {
					t.Fatalf("key %q missing from result", k)
				}
			}

			// key that must be absent
			if tc.absentKey != "" {
				if _, ok := got[tc.absentKey]; ok {
					t.Fatalf("key %q should be absent but is present", tc.absentKey)
				}
			}

			// json_schema inner object checks
			if jsch, ok := got["json_schema"]; ok {
				inner, ok := jsch.(map[string]any)
				if !ok {
					t.Fatalf("json_schema value is %T, want map[string]any", jsch)
				}
				// must have exactly these keys at the wire level
				if inner["name"] != "response" {
					t.Fatalf("json_schema.name = %q, want \"response\"", inner["name"])
				}
				if inner["schema"] == nil {
					t.Fatalf("json_schema.schema is nil")
				}
				if tc.strictVal == nil {
					if _, present := inner["strict"]; present {
						t.Fatalf("json_schema.strict present but should be absent for non-strict")
					}
				} else {
					if inner["strict"] != tc.strictVal {
						t.Fatalf("json_schema.strict = %v, want %v", inner["strict"], tc.strictVal)
					}
				}
			}
		})
	}
}

func TestToChatTools(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := ToChatTools(nil)
		if len(got) != 0 {
			t.Fatalf("expected empty slice, got len=%d", len(got))
		}
	})

	t.Run("name_only", func(t *testing.T) {
		tools := []llm.ToolDefinition{{Name: "my_tool"}}
		got := ToChatTools(tools)
		if len(got) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(got))
		}
		outer := got[0]
		// outer type must be exactly "function"
		if outer["type"] != "function" {
			t.Fatalf("outer type = %q, want \"function\"", outer["type"])
		}
		fn, ok := outer["function"].(map[string]any)
		if !ok {
			t.Fatalf("function value is %T, want map[string]any", outer["function"])
		}
		if fn["name"] != "my_tool" {
			t.Fatalf("function.name = %q, want \"my_tool\"", fn["name"])
		}
		// description and parameters must be absent when not provided
		if _, ok := fn["description"]; ok {
			t.Fatalf("description key present but should be absent")
		}
		if _, ok := fn["parameters"]; ok {
			t.Fatalf("parameters key present but should be absent")
		}
	})

	t.Run("all_fields", func(t *testing.T) {
		params := map[string]any{"type": "object"}
		tools := []llm.ToolDefinition{{Name: "calc", Description: "does math", Parameters: params}}
		got := ToChatTools(tools)
		if len(got) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(got))
		}
		outer := got[0]
		if outer["type"] != "function" {
			t.Fatalf("outer type = %q, want \"function\"", outer["type"])
		}
		fn, ok := outer["function"].(map[string]any)
		if !ok {
			t.Fatalf("function value is %T, want map[string]any", outer["function"])
		}
		if fn["name"] != "calc" {
			t.Fatalf("function.name = %q, want \"calc\"", fn["name"])
		}
		if fn["description"] != "does math" {
			t.Fatalf("function.description = %q, want \"does math\"", fn["description"])
		}
		if fn["parameters"] == nil {
			t.Fatalf("function.parameters is nil, want non-nil")
		}
	})
}
