package chatcompletions

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// emptyLadderCaps is the synthesized/uncatalogued shape that produced the
// lunaroute 400: effort-capable, but no effort ladder to vouch for a level.
func emptyLadderCaps(format string) registry.Resolved {
	return resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		if format != "" {
			c.ThinkingFormat = new(format)
		}
	})
}

// A row that lists no effort ladder vouches for no level, so no name-carrying
// reasoning field may reach the wire — the bug class that let a task override
// of "medium" 400 on an uncatalogued gateway model. openrouter is the
// documented exception and is covered separately.
func TestBuildBody_EmptyLadderNeverWritesAnEffortName(t *testing.T) {
	high := "high"
	for _, format := range []string{"", "openai", "zai", "deepseek", "together", "string-thinking"} {
		t.Run(format, func(t *testing.T) {
			req := userReq("hi")
			req.ReasoningEffort = &high
			body := build(t, req, emptyLadderCaps(format))
			if v, has := body["reasoning_effort"]; has {
				t.Fatalf("empty-ladder row wrote reasoning_effort = %v: %v", v, body)
			}
			// string-thinking spells the level on "thinking"; the other
			// dialects write an enable OBJECT there (or on "reasoning"),
			// which forces no level and is allowed.
			if format == "string-thinking" {
				if v, has := body["thinking"]; has {
					t.Fatalf("empty-ladder string-thinking row wrote thinking = %v", v)
				}
			}
		})
	}
}

// The mandatory-thinking backstop must not force its default onto a row that
// cannot accept a level: an empty-ladder always-on row gets no effort name.
func TestBuildBody_EmptyLadderAlwaysOnBackstopWritesNothing(t *testing.T) {
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.ThinkingFormat = new("openai")
		c.ThinkingAlwaysOn = new(true)
	})
	body := build(t, userReq("hi"), res)
	if v, has := body["reasoning_effort"]; has {
		t.Fatalf("empty-ladder always-on row wrote reasoning_effort = %v", v)
	}
}

// openrouter is the documented exception to the vouch gate: it normalizes
// reasoning.effort itself and its model listing often omits supported efforts,
// so an explicit level passes through even with an empty ladder
// (docs/llm-providers.md pins the dialect as "unconditionally"). With no
// effort at all, an always-on row still gets its enable object.
func TestBuildBody_OpenrouterKeepsExplicitEffortOnAnEmptyLadder(t *testing.T) {
	high := "high"
	req := userReq("hi")
	req.ReasoningEffort = &high
	body := build(t, req, emptyLadderCaps("openrouter"))
	if got := jsonOf(t, body["reasoning"]); got != jsonOf(t, map[string]any{"effort": "high"}) {
		t.Fatalf("openrouter reasoning = %s, want the requested effort passed through", got)
	}

	alwaysOn := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.ThinkingFormat = new("openrouter")
		c.ThinkingAlwaysOn = new(true)
	})
	if got := jsonOf(t, build(t, userReq("hi"), alwaysOn)["reasoning"]); got != jsonOf(t, map[string]any{"enabled": true}) {
		t.Fatalf("always-on row with no effort reasoning = %s, want the enable object", got)
	}
}

// A vouched level is still clamped within the row's ladder.
func TestBuildBody_VouchedEffortClampsToTheLadder(t *testing.T) {
	xhigh := "xhigh"
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.EffortValues = []string{"low", "high"}
	})
	req := userReq("hi")
	req.ReasoningEffort = &xhigh
	body := build(t, req, res)
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high (clamped into the ladder)", body["reasoning_effort"])
	}
}
