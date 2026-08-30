package openaicompat

import (
	"reflect"
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
			want := tc.want
			if want == nil {
				want = map[string]any{}
			}
			if !reflect.DeepEqual(body, want) {
				t.Fatalf("body = %#v, want %#v", body, want)
			}
		})
	}
}

// A mandatory-thinking model with no effort on the request gets the medium
// backstop. The session resolves an explicit off to a nil effort when the
// ladder has no none level, so by design the backstop also covers that case:
// the model cannot run without thinking, and the adapter cannot see whether
// nil meant "unset" or "off" (spec: model-level-reasoning-defaults).
func TestApplyThinkingFormat_ThinkingAlwaysOnBackstop(t *testing.T) {
	body := map[string]any{}
	mc := ModelCompat{Quirks: ProviderQuirks{ThinkingFormat: "openai"}, ThinkingAlwaysOn: true}
	applyThinkingFormat(body, llm.Request{}, mc)
	if body["reasoning_effort"] != "medium" {
		t.Fatalf("body = %#v, want the medium backstop for a mandatory-thinking model", body)
	}
}

// The off-guard keys on the configured effort, not the post-translation wire
// string: a thinking_levels entry may spell a real thinking-on tier as the
// provider's literal "none", and that tier must still emit its wire shape.
func TestApplyThinkingFormat_WireSpelledNoneIsNotOff(t *testing.T) {
	minimal := "minimal"
	body := map[string]any{}
	mc := ModelCompat{
		Quirks:         ProviderQuirks{ThinkingFormat: "zai"},
		ThinkingLevels: map[string]string{"minimal": "none", "high": "high"},
	}
	applyThinkingFormat(body, llm.Request{ReasoningEffort: &minimal}, mc)
	thinking, _ := body["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "enabled" {
		t.Fatalf("body = %#v, want thinking enabled for a wire-spelled none that means the lowest tier", body)
	}
}
