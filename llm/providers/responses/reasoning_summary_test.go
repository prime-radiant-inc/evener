package responses

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestBuildBody_ReasoningSummaryNoneMeansNoSummary pins the ReasoningSummary
// "none" mapping: a row that must always think sends no summary object at all
// when the configured summary is "none" — the value is not merely forwarded,
// it suppresses the field the way an empty value does.
func TestBuildBody_ReasoningSummaryNoneMeansNoSummary(t *testing.T) {
	req := userReq("hi")
	summaryCaps := func(value string) registry.Resolved {
		return resolved(func(c *registry.Caps) {
			c.Reasoning = new(true)
			c.ThinkingAlwaysOn = new(true)
			c.ReasoningSummary = new(value)
		})
	}

	body := build(t, req, summaryCaps("none"))
	if _, has := body["reasoning"]; has {
		t.Fatalf("reasoning = %#v, want none: a \"none\" summary must suppress the object entirely", body["reasoning"])
	}

	body = build(t, req, summaryCaps("auto"))
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %#v, want the always-on summary", body["reasoning"])
	}
}
