package openaichat

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestParseChatUsageSubtractsCachedTokensFromPrompt(t *testing.T) {
	// prompt_tokens is total-including-cached on these endpoints, while
	// llm.Usage.InputTokens means NEW uncached input. Skipping the subtraction
	// double-counts every cached token and inflates spend accounting.
	usage := ParseChatUsage(map[string]any{
		"prompt_tokens":         float64(1000),
		"completion_tokens":     float64(50),
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(900)},
	})
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (1000 prompt - 900 cached)", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
	// The total stays the provider's own total, cached tokens included.
	if usage.TotalTokens != 1050 {
		t.Errorf("TotalTokens = %d, want 1050", usage.TotalTokens)
	}

	// A payload reporting more cached than prompt tokens must clamp at zero
	// rather than report a negative input count.
	clamped := ParseChatUsage(map[string]any{
		"prompt_tokens":         float64(10),
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(99)},
	})
	if clamped.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 when cached exceeds prompt", clamped.InputTokens)
	}

	if empty := ParseChatUsage(nil); empty.InputTokens != 0 || empty.OutputTokens != 0 {
		t.Errorf("ParseChatUsage(nil) = %+v, want zeroed counts", empty)
	}
}

func TestInbandErrorStatusCodeAcceptsOnlyHTTPRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want int
	}{
		{"bare integer", `503`, 503},
		{"digit string", `"429"`, 429},
		{"below HTTP range", `200`, 0},
		{"above HTTP range", `600`, 0},
		{"non-numeric string", `"rate_limit_exceeded"`, 0},
		{"absent", ``, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &InbandError{}
			if tc.code != "" {
				e.Code = json.RawMessage(tc.code)
			}
			// Anything outside 400..599 must read as 0 so it classifies as
			// retryable-unknown with the code preserved, rather than being
			// mistaken for a real HTTP status the transport would act on.
			if got := e.StatusCode(); got != tc.want {
				t.Fatalf("StatusCode() for code %s = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

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
