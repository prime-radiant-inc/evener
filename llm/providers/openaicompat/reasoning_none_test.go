package openaicompat

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// An explicit off must never invert into thinking-on. Only the dialects with
// a real off level (openai's reasoning_effort: none on gpt-5.1+, OpenRouter's
// unified vocabulary) put "none" on the wire; every other format has no known
// thinking-off shape, so the request carries nothing and the provider default
// applies.
func TestApplyThinkingFormat_ExplicitNone(t *testing.T) {
	none := llm.ReasoningEffortNone
	req := llm.Request{ReasoningEffort: &none}
	cases := []struct {
		format string
		want   map[string]any // nil means the body must stay empty
	}{
		{format: "openai", want: map[string]any{"reasoning_effort": "none"}},
		{format: "openrouter", want: map[string]any{"reasoning": map[string]any{"effort": "none"}}},
		{format: "zai", want: nil},
		{format: "deepseek", want: nil},
		{format: "qwen", want: nil},
		{format: "qwen-chat-template", want: nil},
		{format: "together", want: nil},
		{format: "string-thinking", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			body := map[string]any{}
			mc := ModelCompat{Quirks: ProviderQuirks{ThinkingFormat: tc.format}}
			applyThinkingFormat(body, req, mc)
			if tc.want == nil {
				if len(body) != 0 {
					t.Fatalf("body = %#v, want empty (no thinking-off wire shape for %s)", body, tc.format)
				}
				return
			}
			for k, v := range tc.want {
				got, ok := body[k]
				if !ok {
					t.Fatalf("body missing %q: %#v", k, body)
				}
				switch wantV := v.(type) {
				case map[string]any:
					gotM, _ := got.(map[string]any)
					for kk, vv := range wantV {
						if gotM[kk] != vv {
							t.Fatalf("body[%q][%q] = %#v, want %#v", k, kk, gotM[kk], vv)
						}
					}
				default:
					if got != v {
						t.Fatalf("body[%q] = %#v, want %#v", k, got, v)
					}
				}
			}
		})
	}
}
