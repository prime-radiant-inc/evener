package chatcompletions

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// An explicit off must never invert into thinking-on. Every dialect below
// switches thinking ON in one shape or another, so an off carries nothing at
// all and the provider's own default applies (spec §8.4: none clears the
// control on every protocol, and nothing is ever sent to force thinking off).
// The request rule upstream already decided that an off reaches the adapter
// only where the model has an off level to be cleared.
func TestApplyThinkingFormat_ExplicitNoneSendsNothing(t *testing.T) {
	none := llm.ReasoningEffortNone
	req := userReq("hi")
	req.ReasoningEffort = &none
	for _, format := range []string{
		"", "openai", "openrouter", "zai", "deepseek", "qwen", "qwen-chat-template",
		"chat-template", "together", "string-thinking",
	} {
		t.Run(format, func(t *testing.T) {
			res := resolved(func(c *registry.Caps) {
				c.Reasoning = new(true)
				c.ReasoningControls = []string{"effort"}
				c.EffortValues = []string{"none", "low", "high"}
				if format != "" {
					c.ThinkingFormat = new(format)
				}
				c.ChatTemplateKwargs = map[string]any{"enable_thinking": true}
			})
			body := build(t, req, res)
			for _, k := range []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "chat_template_kwargs"} {
				if v, ok := body[k]; ok {
					t.Fatalf("explicit off put %s = %#v on the wire", k, v)
				}
			}
		})
	}
}

// The mandatory-thinking backstop on the "openai" dialect: with no effort on
// the request the format emits its medium default, and when the row takes no
// effort control the backstop has no field to ride and the body stays empty —
// a known gap pinned here so it cannot change silently. Such a model still
// thinks; it is the provider that decides how much.
func TestApplyThinkingFormat_MandatoryBackstopNeedsAnEffortField(t *testing.T) {
	alwaysOn := func(controls ...string) registry.Resolved {
		return resolved(func(c *registry.Caps) {
			c.Reasoning = new(true)
			c.ReasoningControls = controls
			c.ThinkingAlwaysOn = new(true)
			c.ThinkingFormat = new("openai")
		})
	}
	if body := build(t, userReq("hi"), alwaysOn("effort")); body["reasoning_effort"] != "medium" {
		t.Fatalf("body = %#v, want the medium backstop on the openai dialect", body)
	}
	body := build(t, userReq("hi"), alwaysOn("toggle"))
	for _, k := range []string{"reasoning_effort", "reasoning", "thinking"} {
		if v, ok := body[k]; ok {
			t.Fatalf("a row that takes no effort has no field for the backstop to ride, got %s = %#v", k, v)
		}
	}
}
