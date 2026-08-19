package anthropic

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// ================== Opus 4.7/4.8 request shape (issue #169) ==================
//
// Opus 4.7 and 4.8 reject temperature/top_p just like Claude 5+ models, but the
// catalog flag Claude5RequestShape and the isClaude5OrNewer generation parse
// both classify them as old-shape. That sends temperature (e.g. session_namer's
// 0.0) to models that 400 on it. These tests assert the correct behavior:
// Opus 4.7+ is Claude-5-request-shape, 4.6 stays old-shape.

func TestIsClaude5OrNewer_Opus47And48(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Opus 4.7 and 4.8 reject sampling params like Claude 5+.
		{"claude-opus-4-7", true},
		{"claude-opus-4-8", true},
		// Regression guard: 4.6 is old-shape — only 4.7+ changed.
		{"claude-opus-4-6", false},
		// No catalog entry at all: the ID-generation fallback stays
		// conservative for pre-5 generations (the catalog, not the parse,
		// owns the 4.7+ boundary). Pins the fallback's false branch, which
		// every catalogued pre-5 ID skips.
		{"claude-opus-4-3", false},
	}
	for _, tc := range cases {
		if got := isClaude5OrNewer(tc.model); got != tc.want {
			t.Errorf("isClaude5OrNewer(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestBuildRequestBody_Opus47_OmitsSamplingParams(t *testing.T) {
	// Opus 4.7 400s on non-default temperature/top_p, so the request must omit
	// them — same wire shape as Claude 5 (see TestBuildRequestBody_Claude5_OmitsSamplingParams).
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	temp := 0.7
	topP := 0.9
	req := llm.Request{
		Model:       "claude-opus-4-7",
		Messages:    []llm.Message{llm.User("hi")},
		Temperature: &temp,
		TopP:        &topP,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if _, has := body["temperature"]; has {
		t.Error("claude-opus-4-7 request must omit temperature")
	}
	if _, has := body["top_p"]; has {
		t.Error("claude-opus-4-7 request must omit top_p")
	}
}
