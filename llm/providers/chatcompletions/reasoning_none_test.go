package chatcompletions

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// offReq is a request carrying the explicit off the agent sends when the user
// turned thinking off.
func offReq() llm.Request {
	none := llm.ReasoningEffortNone
	req := userReq("hi")
	req.ReasoningEffort = &none
	return req
}

// A model whose ladder lists the off level can be told to stop thinking, and
// two dialects have a wire shape that says so. On the rest the off has no
// spelling, so the request carries nothing and the provider's default applies.
func TestApplyThinkingFormat_ExplicitOffOnTheWire(t *testing.T) {
	cases := map[string]map[string]any{
		"":                   {"reasoning_effort": "none"},
		"openai":             {"reasoning_effort": "none"},
		"openrouter":         {"reasoning": map[string]any{"effort": "none"}},
		"zai":                nil,
		"deepseek":           nil,
		"together":           nil,
		"qwen":               nil,
		"qwen-chat-template": nil,
		"chat-template":      nil,
		"string-thinking":    nil,
	}
	for format, want := range cases {
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
			got := reasoningKeys(build(t, offReq(), res))
			if want == nil {
				want = map[string]any{}
			}
			if jsonOf(t, got) != jsonOf(t, want) {
				t.Fatalf("got %s want %s", jsonOf(t, got), jsonOf(t, want))
			}
		})
	}
}

// A model whose ladder has no off level cannot be told to stop, so the request
// carries nothing — and, critically, the explicit off must not fall through to
// the mandatory-thinking backstop, which would switch thinking ON against the
// user's stated intent. openrouter/minimax/minimax-m2.7 is the shape that
// makes it concrete: reasoning = true, toggle-only controls, always-on, no
// ladder, the openrouter dialect.
func TestApplyThinkingFormat_ExplicitOffNeverSwitchesThinkingOn(t *testing.T) {
	for _, format := range []string{"", "openai", "openrouter", "zai", "deepseek", "together", "qwen", "qwen-chat-template", "chat-template", "string-thinking"} {
		t.Run(format, func(t *testing.T) {
			res := resolved(func(c *registry.Caps) {
				c.Reasoning = new(true)
				c.ReasoningControls = []string{"toggle"}
				c.ThinkingAlwaysOn = new(true)
				if format != "" {
					c.ThinkingFormat = new(format)
				}
				c.ChatTemplateKwargs = map[string]any{"enable_thinking": true}
			})
			if got := reasoningKeys(build(t, offReq(), res)); len(got) != 0 {
				t.Fatalf("explicit off put %s on the wire for a model with no off level", jsonOf(t, got))
			}
		})
	}
}

// The mandatory-thinking backstop on the "openai" dialect still fires for a
// request that reaches the adapter with no effort at all: the format emits its
// medium default. When the row takes no effort control the backstop has no
// field to ride and the body stays empty — a known gap pinned here so it
// cannot change silently.
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
	if got := reasoningKeys(build(t, userReq("hi"), alwaysOn("toggle"))); len(got) != 0 {
		t.Fatalf("a row that takes no effort has no field for the backstop to ride, got %s", jsonOf(t, got))
	}
}

// reasoningKeys narrows a built body to the reasoning-related keys.
func reasoningKeys(body map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "chat_template_kwargs"} {
		if v, ok := body[k]; ok {
			out[k] = v
		}
	}
	return out
}
