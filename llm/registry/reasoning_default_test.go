package registry

import "testing"

// adaptiveClaudeRefs are the rows the overlay states a default effort for,
// once per spelling an instance can serve them under.
var adaptiveClaudeRefs = []string{
	"anthropic/claude-opus-4-6",
	"anthropic/claude-sonnet-4-6",
	"anthropic/claude-opus-4-7",
	"anthropic/claude-opus-4-8",
	"anthropic/claude-opus-5",
	"anthropic/claude-sonnet-5",
	"anthropic/claude-fable-5",
	"anthropic/claude-mythos-5",
	"anthropic/claude-mythos-preview",
	"openrouter/anthropic/claude-opus-4.6",
	"openrouter/anthropic/claude-sonnet-4.6",
	"openrouter/anthropic/claude-opus-4.7",
	"openrouter/anthropic/claude-opus-4.8",
	"openrouter/anthropic/claude-opus-5",
	"openrouter/anthropic/claude-sonnet-5",
	"openrouter/anthropic/claude-fable-5",
	"amazon-bedrock/us.anthropic.claude-opus-4-6-v1",
	"amazon-bedrock/global.anthropic.claude-sonnet-5",
	"google-vertex-anthropic/claude-opus-4-6@default",
	"google-vertex-anthropic/claude-opus-5@default",
}

// models.dev states no default effort for any row, so every one of these is
// the curated overlay's word. Adaptive Claude's server-side default when
// output_config.effort is omitted is high; a row that loses it drops to the
// request rule's medium fallback, which is a silent downgrade. OpenRouter
// spells the versions with dots where Anthropic uses dashes, Bedrock prefixes
// the region and vendor, and Vertex suffixes @default — the fact has to reach
// all of them.
func TestResolve_DefaultEffortIsCuratedNotUpstream(t *testing.T) {
	r := fixtureLoad(t, map[string]string{
		"AWS_REGION": "us-east-1", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global",
	}, "")
	for _, ref := range adaptiveClaudeRefs {
		res := mustResolve(t, r, ref)
		if StringValue(res.Caps.DefaultEffort) != "high" {
			t.Errorf("%s: default_effort = %v, want high from the overlay", ref, res.Caps.DefaultEffort)
		}
	}
	// Rows nobody curated a default for keep none, and the request rule
	// applies its own fallback instead. The 4.5 generation is budget-shaped,
	// not adaptive, so it is deliberately not on the list.
	for _, ref := range []string{
		"anthropic/claude-opus-4-5", "anthropic/claude-sonnet-4-5", "anthropic/claude-haiku-4-5",
		"openai/gpt-5.6", "zai/glm-5.3", "openrouter/anthropic/claude-opus-4.5",
	} {
		if res := mustResolve(t, r, ref); res.Caps.DefaultEffort != nil {
			t.Errorf("%s: default_effort = %q, want unset", ref, *res.Caps.DefaultEffort)
		}
	}
}

// The cap layers like every other: a user row is the last word, and an alias
// inherits the target's value as a model fact (spec §4.2).
func TestResolve_DefaultEffortLayers(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndefault_effort = \"low\"\n")
	res := mustResolve(t, r, "anthropic/claude-opus-4-6")
	if StringValue(res.Caps.DefaultEffort) != "low" || res.Provenance["DefaultEffort"] != "config/row" {
		t.Fatalf("user layer wins: %v %q", res.Caps.DefaultEffort, res.Provenance["DefaultEffort"])
	}
	alias := mustResolve(t, fixtureLoad(t, nil,
		"[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-5\"\n"), "anthropic/house-model")
	if StringValue(alias.Caps.DefaultEffort) != "high" {
		t.Fatalf("alias inherits the target's default effort: %v", alias.Caps.DefaultEffort)
	}
}

// A live listing may state the default too (the Codex backend's
// default_reasoning_level), so liveFacts has to carry it past the filter or
// the fact is dropped between the adapter and Resolve.
func TestResolve_DefaultEffortFromLiveListing(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	r.ApplyLive("openai-codex", []Model{{ID: "gpt-5.6-sol", Caps: Caps{DefaultEffort: new("low")}}})
	res := mustResolve(t, r, "openai-codex/gpt-5.6-sol")
	if StringValue(res.Caps.DefaultEffort) != "low" || res.Provenance["DefaultEffort"] != "live" {
		t.Fatalf("live default effort: %v %q", res.Caps.DefaultEffort, res.Provenance["DefaultEffort"])
	}
}

// The off level is a real tier: a model that lists it can be told to stop
// reasoning, and the adapters need the ladder to say so before they put an
// off on the wire. Dropping it in the converter would make the explicit off
// unreachable for every cataloged model that accepts one (gpt-5.1 and later).
func TestResolve_OffLevelSurvivesTheConverter(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	for _, ref := range []string{"openai/gpt-5.1", "openai/gpt-5.6", "openrouter/openai/gpt-5.5"} {
		res := mustResolve(t, r, ref)
		if !res.Caps.EffortOffCapable() {
			t.Errorf("%s: effort_values = %v, want the off level listed", ref, res.Caps.EffortValues)
		}
		if res.Caps.EffortValues[0] != "none" {
			t.Errorf("%s: effort_values = %v, want none first (models.dev order)", ref, res.Caps.EffortValues)
		}
	}
	// A model with no off level says so, and the ladder it does state is
	// unaffected.
	claude := mustResolve(t, r, "anthropic/claude-opus-4-6")
	if claude.Caps.EffortOffCapable() {
		t.Errorf("claude-opus-4-6: effort_values = %v, want no off level", claude.Caps.EffortValues)
	}
}
