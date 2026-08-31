package provider

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// Reasoning support is a model fact the registry resolves, not a per-provider
// permission. A row the catalog marks non-reasoning resolves
// SupportsReasoning() == false with an empty ladder, so no effort control
// ever reaches it; a reasoning row carries whatever controls its data states;
// and a model nobody has data on stays permitted rather than being assumed
// non-reasoning.
func TestProfile_ReasoningFactsResolvedFromTheRegistry(t *testing.T) {
	r := fixtureRegistry(t)
	cases := []struct {
		ref           string
		wantReasoning bool
		wantLadder    bool
	}{
		// models.dev carries no non-reasoning google row, so the
		// cataloged-non-reasoning half of the property is shown on the two
		// openai rows that have it.
		{"openai/gpt-4.1", false, false},
		{"openai/gpt-4o", false, false},
		{"openai/gpt-5.5", true, true},
		{"anthropic/claude-opus-4-6", true, true},
		{"google/gemini-3.1-pro-preview", true, true},
		// A budget-only reasoning row reasons but takes no effort: the
		// ladder is empty and EffortCapable() is false for it (spec §7.4).
		{"google/gemini-2.5-pro", true, false},
		{"anthropic/claude-sonnet-4-5", true, false},
		// Uncataloged, on a gateway that serves anything.
		{"work/glm-5.3", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			p := mustResolve(t, r, tc.ref)
			if got := p.SupportsReasoning(); got != tc.wantReasoning {
				t.Fatalf("SupportsReasoning() = %v, want %v", got, tc.wantReasoning)
			}
			if got := len(p.ReasoningEffortLevels()) > 0; got != tc.wantLadder {
				t.Fatalf("ReasoningEffortLevels() = %v, want a ladder: %v", p.ReasoningEffortLevels(), tc.wantLadder)
			}
		})
	}
}

// The row's stated default effort reaches the profile, normalized, and a row
// nobody states one for reports none so the caller applies its own fallback.
func TestProfile_DefaultReasoningEffortFromTheRegistry(t *testing.T) {
	r := fixtureRegistry(t)
	if got := mustResolve(t, r, "anthropic/claude-opus-4-6").DefaultReasoningEffort(); got != "high" {
		t.Fatalf("claude-opus-4-6 DefaultReasoningEffort() = %q, want high (the overlay states it)", got)
	}
	if got := mustResolve(t, r, "openai/gpt-5.5").DefaultReasoningEffort(); got != "" {
		t.Fatalf("gpt-5.5 DefaultReasoningEffort() = %q, want empty (no source states one)", got)
	}
	// A hand-written value means what it says whatever its casing, and the
	// disable aliases reach the request rule as the canonical off.
	for written, want := range map[string]string{"High": "high", "OFF": "none", "none": "none"} {
		p := mustResolve(t, r, "work/glm-5")
		res := p.Resolved()
		res.Caps.DefaultEffort = new(written)
		if got := p.WithResolved(res).DefaultReasoningEffort(); got != want {
			t.Fatalf("default_effort = %q resolved to %q, want %q", written, got, want)
		}
	}
}

// An instance's own model row is explicit user intent and wins over the
// catalog in both directions (spec §5): reasoning = true on a row the catalog
// calls non-reasoning turns the control back on, and reasoning = false turns
// it off and clears the ladder with it.
func TestProfile_ConfiguredReasoningBeatsTheCatalog(t *testing.T) {
	r := reasoningFixture(t, map[string]registry.Model{
		"gpt-4.1": {Caps: registry.Caps{Reasoning: new(true)}},
		"gpt-5.5": {Caps: registry.Caps{Reasoning: new(false)}},
	})
	if forced := mustResolve(t, r, "gw/gpt-4.1"); !forced.SupportsReasoning() {
		t.Fatal("gpt-4.1 with reasoning = true: SupportsReasoning() = false, want true (the instance row wins)")
	}
	off := mustResolve(t, r, "gw/gpt-5.5")
	if off.SupportsReasoning() || len(off.ReasoningEffortLevels()) != 0 {
		t.Fatalf("gpt-5.5 with reasoning = false: %v %v, want off with no ladder", off.SupportsReasoning(), off.ReasoningEffortLevels())
	}
}

// A configured effort ladder is complete authority on the model's tiers,
// which entails the model takes an effort control: it stands in for the old
// thinking_levels map. On the shape that motivated it — a gateway serving a
// model no catalog has data on, where a wrong or absent verdict is exactly
// why the user wrote the ladder — the ladder alone is enough (spec §7.4,
// derivation step 1). Where the catalog states reasoning = false outright,
// Reasoning gates everything (§8.4) and reasoning = true is how the user
// overrides it; the ladder then survives alongside.
func TestProfile_ConfiguredEffortValuesImplyAnEffortControl(t *testing.T) {
	r := reasoningFixture(t, map[string]registry.Model{
		"glm-5.3": {Caps: registry.Caps{EffortValues: []string{"low", "high"}}},
		"gpt-4.1": {Caps: registry.Caps{Reasoning: new(true), EffortValues: []string{"low", "high"}}},
	})
	for _, ref := range []string{"gw/glm-5.3", "gw/gpt-4.1"} {
		t.Run(ref, func(t *testing.T) {
			p := mustResolve(t, r, ref)
			if !p.SupportsReasoning() {
				t.Fatal("SupportsReasoning() = false, want true (a configured ladder configures an effort control)")
			}
			if got := p.ReasoningEffortLevels(); len(got) != 2 || got[0] != "low" {
				t.Fatalf("ReasoningEffortLevels() = %v, want the configured [low high]", got)
			}
			if !p.Resolved().Caps.EffortCapable() {
				t.Fatalf("EffortCapable() = false for a configured ladder: %v", p.Resolved().Caps.ReasoningControls)
			}
			assertTaskListEffortEnum(t, p, []string{"low", "high"})
		})
	}
}

// A model switch re-resolves every reasoning fact from the new model's row.
// A stale ladder would defeat the clamp, and a stale default would keep an
// adaptive model's high on a model that never claimed it.
func TestProfile_WithModel_RederivesReasoningFacts(t *testing.T) {
	r := fixtureRegistry(t)
	adaptive := mustResolve(t, r, "anthropic/claude-opus-4-6")
	if adaptive.DefaultReasoningEffort() != "high" || len(adaptive.ReasoningEffortLevels()) == 0 {
		t.Fatalf("fixture: %q %v", adaptive.DefaultReasoningEffort(), adaptive.ReasoningEffortLevels())
	}
	budgetOnly := adaptive.WithModel("claude-sonnet-4-5")
	if got := budgetOnly.DefaultReasoningEffort(); got != "" {
		t.Fatalf("claude-sonnet-4-5 after WithModel: DefaultReasoningEffort() = %q, want empty", got)
	}
	if len(budgetOnly.ReasoningEffortLevels()) != 0 {
		t.Fatalf("claude-sonnet-4-5 after WithModel: ladder = %v, want empty", budgetOnly.ReasoningEffortLevels())
	}
	if back := budgetOnly.WithModel("claude-opus-4-6"); back.DefaultReasoningEffort() != "high" || len(back.ReasoningEffortLevels()) == 0 {
		t.Fatalf("switching back: %q %v", back.DefaultReasoningEffort(), back.ReasoningEffortLevels())
	}

	// A switch to a model nobody has data on must not hand it the
	// incumbent's narrower ladder: an inherited [low medium high max] would
	// clamp a request the unknown model might well accept.
	unknown := adaptive.WithModel("claude-new-thing")
	if slices.Equal(unknown.ReasoningEffortLevels(), adaptive.ReasoningEffortLevels()) {
		t.Fatalf("an uncataloged model kept the incumbent's ladder: %v", unknown.ReasoningEffortLevels())
	}
	if !unknown.SupportsReasoning() || unknown.DefaultReasoningEffort() != "" {
		t.Fatalf("uncataloged: permitted with no stated default, got %v %q", unknown.SupportsReasoning(), unknown.DefaultReasoningEffort())
	}
}

// The task_list effort enum follows the gated ladder: absent for a model that
// takes no effort, present and exact for one that does, so the per-task
// override the model can pick agrees with what the request builder will send.
func TestProfile_TaskListEffortEnumFollowsTheLadder(t *testing.T) {
	r := fixtureRegistry(t)
	assertTaskListEffortEnum(t, mustResolve(t, r, "openai/gpt-4.1"), nil)
	assertTaskListEffortEnum(t, mustResolve(t, r, "google/gemini-2.5-pro"), nil)
	assertTaskListEffortEnum(t, mustResolve(t, r, "openai/gpt-5.5"), mustResolve(t, r, "openai/gpt-5.5").ReasoningEffortLevels())
}

// reasoningFixture is the profile fixture registry plus a chat-completions
// gateway carrying the given per-model rows — the providers.toml
// [providers.gw.models.x] table, as the registry sees it.
func reasoningFixture(t *testing.T, models map[string]registry.Model) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(), registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"gw": {
			Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, APIKey: "k",
			Transport: registry.Transport{BaseURL: "https://gw.example.com/v1"},
			Models:    models,
		}}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}
