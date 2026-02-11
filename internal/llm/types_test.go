package llm

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeFinishReason(t *testing.T) {
	cases := []struct {
		provider string
		raw      string
		want     string
	}{
		// OpenAI - already canonical
		{"openai", "stop", "stop"},
		{"openai", "length", "length"},
		{"openai", "tool_calls", "tool_calls"},
		{"openai", "content_filter", "content_filter"},

		// Anthropic
		{"anthropic", "end_turn", "stop"},
		{"anthropic", "stop_sequence", "stop"},
		{"anthropic", "max_tokens", "length"},
		{"anthropic", "tool_use", "tool_calls"},

		// Google/Gemini
		{"google", "STOP", "stop"},
		{"google", "MAX_TOKENS", "length"},
		{"google", "SAFETY", "content_filter"},
		{"google", "RECITATION", "content_filter"},

		// Unrecognized -> other
		{"openai", "weird_value", "other"},
		{"anthropic", "unknown", "other"},
		{"google", "BLOCKLIST", "other"},

		// Empty -> stop (default)
		{"openai", "", "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.raw, func(t *testing.T) {
			got := NormalizeFinishReason(tc.provider, tc.raw)
			if got.Reason != tc.want {
				t.Fatalf("NormalizeFinishReason(%q, %q).Reason = %q, want %q", tc.provider, tc.raw, got.Reason, tc.want)
			}
			if tc.raw != "" && got.Raw != tc.raw {
				t.Fatalf("NormalizeFinishReason(%q, %q).Raw = %q, want %q", tc.provider, tc.raw, got.Raw, tc.raw)
			}
		})
	}
}

func TestContentPart_WebSearch_JSONRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"type":"web_search_call","id":"ws_1","action":{"type":"search","query":"go error handling"}}`)
	part := ContentPart{
		Kind: ContentWebSearch,
		WebSearch: &WebSearchData{
			Query: "go error handling",
			Raw:   raw,
		},
	}
	b, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ContentPart
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != ContentWebSearch {
		t.Fatalf("kind: got %q want %q", got.Kind, ContentWebSearch)
	}
	if got.WebSearch == nil {
		t.Fatalf("web_search is nil")
	}
	if got.WebSearch.Query != "go error handling" {
		t.Fatalf("query: got %q", got.WebSearch.Query)
	}
	if string(got.WebSearch.Raw) != string(raw) {
		t.Fatalf("raw: got %s", got.WebSearch.Raw)
	}
}

func TestNormalizeFinishReason_PauseTurn(t *testing.T) {
	got := NormalizeFinishReason("anthropic", "pause_turn")
	if got.Reason != FinishReasonPauseTurn {
		t.Fatalf("Reason = %q, want %q", got.Reason, FinishReasonPauseTurn)
	}
	if got.Raw != "pause_turn" {
		t.Fatalf("Raw = %q, want %q", got.Raw, "pause_turn")
	}
}

func TestAdapterTimeout_Defaults(t *testing.T) {
	at := DefaultAdapterTimeout()
	if at.Connect != 10*time.Second {
		t.Fatalf("Connect = %v, want 10s", at.Connect)
	}
	if at.Request != 120*time.Second {
		t.Fatalf("Request = %v, want 120s", at.Request)
	}
	if at.StreamRead != 30*time.Second {
		t.Fatalf("StreamRead = %v, want 30s", at.StreamRead)
	}
}

func TestRequest_Validate_WithWebSearch(t *testing.T) {
	req := Request{
		Model:     "test-model",
		Messages:  []Message{User("hello")},
		WebSearch: true,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate with WebSearch=true: %v", err)
	}
}
