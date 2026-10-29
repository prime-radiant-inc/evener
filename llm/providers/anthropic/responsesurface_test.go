package anthropic

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

func anthRaw(t *testing.T, rawJSON string) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		t.Fatalf("test fixture: %v", err)
	}
	return raw
}

func TestFromAnthropicResponseWebSearchContentBlocks(t *testing.T) {
	r := fromAnthropicResponse(anthRaw(t, `{"content":[
		{"type":"server_tool_use","id":"srvu_1","name":"web_search","input":{"query":"tokyo weather"}},
		{"type":"web_search_tool_result","content":[{"title":"t","url":"https://e/"}]}
	]}`), "m")
	var webSearches []*llm.WebSearchData
	for _, p := range r.Message.Content {
		if p.Kind == llm.ContentWebSearch {
			webSearches = append(webSearches, p.WebSearch)
		}
	}
	if len(webSearches) != 2 {
		t.Fatalf("content = %#v, want two web-search parts", r.Message.Content)
	}
	if webSearches[0].Query != "tokyo weather" || len(webSearches[0].Raw) == 0 {
		t.Fatalf("server_tool_use must surface its query and raw block: %#v", webSearches[0])
	}
	if webSearches[1].Query != "" || len(webSearches[1].Raw) == 0 {
		t.Fatalf("web_search_tool_result must surface its raw block: %#v", webSearches[1])
	}
}

func TestFromAnthropicResponseSkipsNonObjectContent(t *testing.T) {
	r := fromAnthropicResponse(anthRaw(t, `{"content":["nope",{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`), "m")
	if len(r.Message.Content) != 1 || r.Message.Content[0].Text != "hi" {
		t.Fatalf("content = %#v, want only the decodable text part", r.Message.Content)
	}
}

// A tool_use block with a stop reason no more specific than stop must fall back
// to the tool-calls finish; a genuine max_tokens stop must not be masked.
func TestFromAnthropicResponseToolUseFinishFallback(t *testing.T) {
	body := func(stop string) map[string]any {
		return anthRaw(t, `{"content":[{"type":"tool_use","id":"t1","name":"shell","input":{}}],"stop_reason":"`+stop+`"}`)
	}
	if r := fromAnthropicResponse(body("end_turn"), "m"); r.Finish.Reason != llm.FinishReasonToolCalls {
		t.Fatalf("end_turn + tool_use must fall back to tool_calls, got %q", r.Finish.Reason)
	}
	if r := fromAnthropicResponse(body("max_tokens"), "m"); r.Finish.Reason != llm.FinishReasonLength {
		t.Fatalf("max_tokens must survive the tool-call override, got %q", r.Finish.Reason)
	}
}

func TestFromAnthropicResponseStopDetails(t *testing.T) {
	if r := fromAnthropicResponse(anthRaw(t, `{"stop_reason":"end_turn","stop_details":{"type":"stop","detail":"x"}}`), "m"); len(r.Warnings) != 0 {
		t.Fatalf("stop_details that describe no refusal must add no warning: %+v", r.Warnings)
	}
	r := fromAnthropicResponse(anthRaw(t, `{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}}`), "m")
	if len(r.Warnings) != 1 || r.Warnings[0].Code != "refusal" {
		t.Fatalf("a refusal stop_details must add exactly one refusal warning: %+v", r.Warnings)
	}
}

func TestParseUsageCacheCreationArms(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want llm.Usage
	}{
		{"breakdown 5m", `{"input_tokens":1,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3}}`,
			llm.Usage{InputTokens: 1, OutputTokens: 2, CacheWriteTokens: new(3), TotalTokens: 6}},
		{"breakdown 1h", `{"input_tokens":1,"output_tokens":2,"cache_creation":{"ephemeral_1h_input_tokens":4}}`,
			llm.Usage{InputTokens: 1, OutputTokens: 2, CacheWrite1hTokens: new(4), TotalTokens: 7}},
		{"flat fallback", `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":5}`,
			llm.Usage{InputTokens: 1, OutputTokens: 2, CacheWriteTokens: new(5), TotalTokens: 8}},
		{"breakdown wins over flat", `{"input_tokens":1,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3},"cache_creation_input_tokens":9}`,
			llm.Usage{InputTokens: 1, OutputTokens: 2, CacheWriteTokens: new(3), TotalTokens: 6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUsage(anthRaw(t, tc.raw))
			if got.InputTokens != tc.want.InputTokens || got.OutputTokens != tc.want.OutputTokens || got.TotalTokens != tc.want.TotalTokens {
				t.Fatalf("parseUsage = %+v, want %+v", got, tc.want)
			}
			if !ptrEq(got.CacheWriteTokens, tc.want.CacheWriteTokens) || !ptrEq(got.CacheWrite1hTokens, tc.want.CacheWrite1hTokens) {
				t.Fatalf("parseUsage cache pointers = (%v, %v), want (%v, %v)",
					got.CacheWriteTokens, got.CacheWrite1hTokens, tc.want.CacheWriteTokens, tc.want.CacheWrite1hTokens)
			}
		})
	}
}

func ptrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
