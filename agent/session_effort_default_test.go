package agent

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
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
		{"explicit none omits the field when the model has no off level", "none", true, levels, "", nil},
		{"explicit none is sent when the model lists it", "none", true, withNone, "", str("none")},
		{"a mixed-case configured off is still an off", "None", true, levels, "", nil},
		{"a ladder-cased off level still carries the explicit off", "none", true, []string{"None", "low", "high"}, "", str("none")},
		{"a stored disable alias is still an off", "off", true, levels, "", nil},
		{"configured effort is clamped", "xhigh", true, levels, "", str("high")},
		{"unset uses the model's stated default", "", true, levels, "high", str("high")},
		{"a model default of none is held to the same off rule", "", true, levels, "none", nil},
		{"a model default of none is sent when the model lists it", "", true, withNone, "none", str("none")},
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
			name:       "explicit none is never overridden by the default",
			profile:    reasoningProfile("glm-5.3", registry.Caps{EffortValues: []string{"low", "medium", "high"}}),
			configured: "none",
			want:       "",
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
			req := s.buildModelRequest(tc.profile, "sys", []llm.Message{llm.User("hi")}, nil, tc.configured)
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
