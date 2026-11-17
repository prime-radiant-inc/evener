package agent

import (
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all" // the reporting rule is checked against the real builders
	"primeradiant.com/evener/llm/registry"
)

// resolveRequestEffort is the one rule for what effort a request carries.
func TestResolveRequestEffort(t *testing.T) {
	t.Parallel()
	levels := []string{"low", "medium", "high"}
	withNone := []string{"none", "low", "high"}
	str := func(s string) *string { return &s }
	cases := []struct {
		name         string
		configured   string
		supports     bool
		levels       []string
		modelDefault string
		want         *string
	}{
		{"non-reasoning model gets nothing even when configured", "high", false, []string{}, "", nil},
		// The off is carried whatever the ladder holds; which models can be
		// told off on the wire, and how, is the adapters' call (spec §8.4).
		{"explicit none is carried when the model has no off level", "none", true, levels, "", str("none")},
		{"explicit none is carried when the model lists it", "none", true, withNone, "", str("none")},
		{"a mixed-case configured off is still an off", "None", true, levels, "", str("none")},
		{"a ladder-cased off level still carries the explicit off", "none", true, []string{"None", "low", "high"}, "", str("none")},
		{"a stored disable alias is still an off", "off", true, levels, "", str("none")},
		{"an off is never clamped into a thinking tier", "none", true, []string{"high", "max"}, "", str("none")},
		{"configured effort is clamped", "xhigh", true, levels, "", str("high")},
		{"unset uses the model's stated default", "", true, levels, "high", str("high")},
		{"a model default of none is held to the same off rule", "", true, levels, "none", str("none")},
		{"a model default of none is carried when the model lists it", "", true, withNone, "none", str("none")},
		{"unset falls back to medium", "", true, levels, "", str("medium")},
		{"fallback medium is clamped to the model's levels", "", true, []string{"high", "max"}, "", str("high")},
		{"unset with unknown levels still sends medium", "", true, nil, "", str("medium")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRequestEffort(tc.configured, tc.supports, tc.levels, tc.modelDefault)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("got nil, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("got %q, want %q", *got, *tc.want)
			}
		})
	}
}

// reasoningProfile resolves model on a lunaroute-style gateway whose row
// carries caps, so the request-effort rule runs against facts that came
// through registry.Resolve rather than a hand-built profile.
func reasoningProfile(model string, caps registry.Caps) *provider.Profile {
	caps.Reasoning = new(true)
	return resolveTestProfile("lunaroute", lunarouteInstance(map[string]registry.Model{model: {Caps: caps}}), model)
}

// buildModelRequest feeds the profile's model facts into that rule.
func TestBuildModelRequest_AppliesRequestEffortRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		profile    *provider.Profile
		configured string
		want       string // "" means no effort on the request
	}{
		{
			// A gateway-fronted glm-5.3 spent 25k reasoning tokens on one turn
			// when the request carried no effort; the default bounds that.
			name:       "unset gets the default clamped to the model's levels",
			profile:    reasoningProfile("glm-5.3", registry.Caps{EffortValues: []string{"high", "max"}}),
			configured: "",
			want:       "high",
		},
		{
			name:       "unset uses the model's stated default",
			profile:    reasoningProfile("gateway-model", registry.Caps{EffortValues: []string{"low", "medium", "high"}, DefaultEffort: new("high")}),
			configured: "",
			want:       "high",
		},
		{
			// The explicit off rides the request as "none" whatever the
			// model's ladder says. It is never replaced by the default, and
			// the adapters need it distinguishable from "nothing configured":
			// a mandatory-thinking row's backstop fires on the latter, which
			// would switch thinking on against the user's stated intent.
			name:       "explicit none rides the request as the off sentinel",
			profile:    reasoningProfile("glm-5.3", registry.Caps{EffortValues: []string{"low", "medium", "high"}}),
			configured: "none",
			want:       "none",
		},
		{
			name:       "an always-on row with no ladder still carries the explicit off",
			profile:    reasoningProfile("minimax-m2.7", registry.Caps{ReasoningControls: []string{"toggle"}, ThinkingAlwaysOn: new(true)}),
			configured: "off",
			want:       "none",
		},
		{
			name:       "a model declared non-reasoning gets no default",
			profile:    nonReasoningProfile(t, "tiny-chat"),
			configured: "",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{}
			req := s.buildModelRequest(tc.profile, "sys", []llm.Message{llm.User("hi")}, nil, tc.configured, 0)
			switch {
			case tc.want == "" && req.ReasoningEffort != nil:
				t.Fatalf("ReasoningEffort = %q, want none on the request", *req.ReasoningEffort)
			case tc.want != "" && req.ReasoningEffort == nil:
				t.Fatalf("ReasoningEffort = nil, want %q", tc.want)
			case tc.want != "" && *req.ReasoningEffort != tc.want:
				t.Fatalf("ReasoningEffort = %q, want %q", *req.ReasoningEffort, tc.want)
			}
		})
	}
}

// wireEffortEvidence reads the reasoning tier a built request body carries: the
// level names the dialects spell, and the numeric thinking budgets they size
// from one. Both are empty when the body carries no tier at all.
func wireEffortEvidence(body map[string]any) (names []string, budgets []int) {
	addName := func(v any) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			names = append(names, strings.ToLower(strings.TrimSpace(s)))
		}
	}
	addName(body["reasoning_effort"])
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		addName(reasoning["effort"])
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		if budget, ok := thinking["budget_tokens"].(int); ok {
			budgets = append(budgets, budget)
		}
	} else {
		addName(body["thinking"])
	}
	if cfg, ok := body["output_config"].(map[string]any); ok {
		addName(cfg["effort"])
	}
	if gen, ok := body["generationConfig"].(map[string]any); ok {
		if thinking, ok := gen["thinkingConfig"].(map[string]any); ok {
			addName(thinking["thinkingLevel"])
			if budget, ok := thinking["thinkingBudget"].(int); ok {
				budgets = append(budgets, budget)
			}
		}
	}
	return names, budgets
}

// DefaultReasoningEffort is what clients show for an unset ("(default)")
// session effort, so it may name only a tier the session's requests actually
// put to work. The request itself always carries resolveRequestEffort's answer
// — budget-shaped thinking sizes its budget from it, and the explicit off must
// stay distinguishable from "nothing configured" — but a dialect that drops a
// level its ladder cannot vouch for leaves the provider at a default evener
// did not choose, and no tier may be claimed then (#6557). Each case builds the
// request and the real protocol body, then checks the reported tier against
// what that body carries.
func TestDefaultReasoningEffort_NamesTheTierTheWireCarries(t *testing.T) {
	t.Parallel()
	// An openai instance with a row the catalog does not know: the Responses
	// protocol, reasoning-capable, no ladder and no stated default.
	responsesGateway := map[string]registry.Provider{"responses-gateway": {
		Base: "openai", APIKey: "test",
		Models: map[string]registry.Model{"house-model": {}},
	}}
	chatDialect := func(format string, caps registry.Caps) *provider.Profile {
		caps.ThinkingFormat = new(format)
		return reasoningProfile("house-model", caps)
	}
	cases := []struct {
		name    string
		profile *provider.Profile
		want    string
		// wantCarried is the wire invariant: the reported tier must be one the
		// built body puts to work. A model's own stated default is exempt from
		// it (see the case below), because that fact is the row's, not the
		// request's.
		wantCarried bool
	}{
		{
			// The reported bug: the fallback is evener's own choice, not the
			// model's, and with no ladder to vouch for it the Chat
			// Completions builder writes no reasoning_effort at all, so the
			// provider's unknown default is all that runs.
			name:        "no ladder and no stated default claims no tier on the name-gated dialect",
			profile:     reasoningProfile("house-model", registry.Caps{}),
			want:        "",
			wantCarried: false,
		},
		{
			name:        "the fallback the model's ladder vouches for",
			profile:     reasoningProfile("glm-5.3", registry.Caps{EffortValues: []string{"high", "max"}}),
			want:        "high",
			wantCarried: true,
		},
		{
			// A stated default is the model's own statement about the tier it
			// runs at, so it stands even where evener cannot spell it: the
			// provider runs at that tier whether or not the request names it.
			name:        "the model's stated default with no ladder",
			profile:     reasoningProfile("gateway-model", registry.Caps{DefaultEffort: new("High")}),
			want:        "high",
			wantCarried: false,
		},
		{
			// OpenRouter normalizes reasoning.effort itself and its listing
			// often omits supported efforts, so the builder passes the
			// unvouched level through.
			name:        "OpenRouter consumes a fallback its row does not list",
			profile:     resolveTestProfile("openrouter", nil, "house-model"),
			want:        "medium",
			wantCarried: true,
		},
		{
			// The budget path sizes thinking.budget_tokens from the level, so
			// the level is in effect even though no name reaches the wire.
			name:        "a budget-shaped row sizes its budget from the fallback",
			profile:     newAnthropicProfile("claude-sonnet-4-5"),
			want:        "medium",
			wantCarried: true,
		},
		{
			// Google's level shape spells a level whenever the row names no
			// ladder to gate it.
			name:        "Google's level shape spells a fallback its row does not list",
			profile:     withEffortLevels(newGeminiProfile("gemini-3.8-flash")),
			want:        "medium",
			wantCarried: true,
		},
		{
			name:        "the Responses builder drops a level no ladder vouches for",
			profile:     resolveTestProfile("responses-gateway", responsesGateway, "house-model"),
			want:        "",
			wantCarried: false,
		},
		{
			// The adaptive body survives a dropped effort name, but the level
			// itself does not: an uncatalogued Claude row runs at whatever
			// Anthropic defaults to.
			name:        "an adaptive row with no ladder drops the level name",
			profile:     newAnthropicProfile("claude-unknown-model"),
			want:        "",
			wantCarried: false,
		},
		{
			name:        "a budget-shaped Google row sizes its budget from the fallback",
			profile:     newGeminiProfile("gemini-2.5-flash"),
			want:        "medium",
			wantCarried: true,
		},
		{
			// The enable-object dialects keep thinking on without naming a
			// tier the row cannot vouch for.
			name:        "a zai row with no ladder keeps thinking on without a tier",
			profile:     chatDialect("zai", registry.Caps{}),
			want:        "",
			wantCarried: false,
		},
		{
			name:        "a zai row whose ladder vouches for the fallback",
			profile:     chatDialect("zai", registry.Caps{EffortValues: []string{"low", "medium", "high"}}),
			want:        "medium",
			wantCarried: true,
		},
		{
			// qwen's dialect writes only enable_thinking: there is no tier on
			// its wire at all, ladder or not.
			name:        "a qwen row never carries a tier",
			profile:     chatDialect("qwen", registry.Caps{EffortValues: []string{"low", "medium", "high"}}),
			want:        "",
			wantCarried: false,
		},
		{
			name:        "a gateway's no-effort verdict carries no tier",
			profile:     reasoningProfile("house-model", registry.Caps{EffortParameterUnsupported: new(true)}),
			want:        "",
			wantCarried: false,
		},
		{
			name:        "a stated off",
			profile:     reasoningProfile("gateway-model", registry.Caps{EffortValues: []string{"none", "low", "high"}, DefaultEffort: new("none")}),
			want:        "none",
			wantCarried: true,
		},
		{
			name:        "nothing for a model that does not reason",
			profile:     nonReasoningProfile(t, "tiny-chat"),
			want:        "",
			wantCarried: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Session{}
			req := s.buildModelRequest(tc.profile, "sys", []llm.Message{llm.User("hi")}, nil, "", 0)
			res := tc.profile.Resolved()
			proto, ok := llm.ProtocolFor(res.Protocol)
			if !ok {
				t.Fatalf("no protocol registered for %q", res.Protocol)
			}
			body, err := proto.BuildBody(llm.ShapeRequest(req, res), res)
			if err != nil {
				t.Fatalf("BuildBody: %v", err)
			}
			names, budgets := wireEffortEvidence(body)
			got := profileDefaultReasoningEffort(tc.profile)
			if got != tc.want {
				t.Fatalf("DefaultReasoningEffort = %q, want %q", got, tc.want)
			}
			carried := slices.Contains(names, tc.want) ||
				(tc.want != "" && tc.want != llm.ReasoningEffortNone && slices.Contains(budgets, llm.ReasoningBudget(tc.want)))
			if carried != tc.wantCarried {
				t.Fatalf("the %s body carries effort names %v and thinking budgets %v; wantCarried = %v for %q",
					res.Protocol, names, budgets, tc.wantCarried, tc.want)
			}
			// The reported tier is right only if the predicate agrees with the
			// body this same request built. Checking only the reported tier
			// would let a wrong predicate pass wherever the two happen to
			// coincide; this is the check that a branch restating a builder's
			// gate is still true to it.
			if tc.want != "" {
				if reached := llm.EffortReachesWire(tc.want, res); reached != carried {
					t.Fatalf("EffortReachesWire(%q) = %t but the %s body that carries effort names %v and thinking budgets %v answers %t",
						tc.want, reached, res.Protocol, names, budgets, carried)
				}
			}
		})
	}
}
