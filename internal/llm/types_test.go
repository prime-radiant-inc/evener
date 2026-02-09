package llm

import "testing"

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
