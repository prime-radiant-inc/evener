package requestutil

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestGatewayInputModalities(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		arch GatewayArchitecture
		caps GatewayCapabilities
		want []string
	}{
		{"architecture wins over capabilities", GatewayArchitecture{InputModalities: []string{"text", "pdf"}}, GatewayCapabilities{Vision: &yes}, []string{"text", "pdf"}},
		{"architecture deduped", GatewayArchitecture{InputModalities: []string{"text", "text", "image"}}, GatewayCapabilities{}, []string{"text", "image"}},
		{"only empty entries fall through to vision", GatewayArchitecture{InputModalities: []string{"", ""}}, GatewayCapabilities{Vision: &yes}, []string{"text", "image"}},
		{"only empty entries advertise nothing", GatewayArchitecture{InputModalities: []string{""}}, GatewayCapabilities{}, nil},
		{"vision true advertises text and image", GatewayArchitecture{}, GatewayCapabilities{Vision: &yes}, []string{"text", "image"}},
		{"vision false advertises nothing", GatewayArchitecture{}, GatewayCapabilities{Vision: &no}, nil},
		{"no facts advertises nothing", GatewayArchitecture{}, GatewayCapabilities{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Nil, not an empty slice, is what "advertises nothing" must mean:
			// anything else would erase a curated list on the live replay.
			got := gatewayInputModalities(tc.arch, tc.caps)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("gatewayInputModalities = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDedupStrings(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"drops duplicates and empties in first-seen order", []string{"low", "", "low", "high"}, []string{"low", "high"}},
		{"nothing survives", []string{"", ""}, nil},
		{"nil stays nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupStrings(tc.in)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("DedupStrings(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestGatewayEffortLadder(t *testing.T) {
	levels := func(m map[string]string) GatewayClientCompat {
		return GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingLevelMap: m}}
	}
	cases := []struct {
		name      string
		compat    GatewayClientCompat
		want      []string
		published bool
	}{
		{
			name: "map levels come back in rank order",
			compat: levels(map[string]string{
				"max": "max", "high": "high", "low": "low",
				"medium": "medium", "xhigh": "xhigh", "minimal": "", "off": "",
			}),
			want:      []string{"low", "medium", "high", "xhigh", "max"},
			published: true,
		},
		{
			name: "an explicit off level leads the ladder",
			compat: levels(map[string]string{
				"off": "none", "low": "low", "high": "high",
			}),
			want:      []string{"none", "low", "high"},
			published: true,
		},
		{
			name: "an off level spelled as another tier is not an off",
			compat: levels(map[string]string{
				"off": "low", "low": "low", "high": "high",
			}),
			want:      []string{"low", "high"},
			published: true,
		},
		{
			// glm maps medium→high, minimal→low and xhigh→max: the menu offers
			// the wire values the gateway accepts, once each.
			name: "aliases collapse to their wire values",
			compat: levels(map[string]string{
				"minimal": "low", "low": "low", "medium": "high",
				"high": "high", "xhigh": "max", "max": "max", "off": "none",
			}),
			want:      []string{"none", "low", "high", "max"},
			published: true,
		},
		{
			name: "a level spelled outside the vocabulary is dropped",
			compat: levels(map[string]string{
				"low": "low", "high": "turbo",
			}),
			want:      []string{"low"},
			published: true,
		},
		{
			name:   "a compat block without pi states no ladder",
			compat: GatewayClientCompat{},
			want:   nil,
		},
		{
			// A Pi block carrying other compat fields publishes no map: that is
			// silence about effort, not the model refusing every level, so the
			// caller must keep whatever ladder it inherited.
			name:   "a pi block without a map states no ladder",
			compat: GatewayClientCompat{Pi: &GatewayPiCompat{}},
			want:   nil,
		},
		{
			// A published map with every level null is the gateway stating that
			// the model takes no effort control. That is not the same as an
			// absent map: inheriting a curated ladder would send levels the
			// gateway marks unsupported.
			name:      "an all-null map states an empty ladder",
			compat:    levels(map[string]string{"low": "", "high": ""}),
			want:      []string{},
			published: true,
		},
		{
			name:      "an empty map states an empty ladder",
			compat:    levels(map[string]string{}),
			want:      []string{},
			published: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, published := gatewayEffortLadder(tc.compat)
			if published != tc.published {
				t.Fatalf("published = %v, want %v", published, tc.published)
			}
			if !slices.Equal(got, tc.want) || (!published && got != nil) {
				t.Fatalf("gatewayEffortLadder = %#v, want %#v (published %v)", got, tc.want, published)
			}
		})
	}
}

// TestApplyGatewayFacts_Dialect pins the two wire-shaping compat fields a
// gateway publishes beside its effort map. A dialect the chat builders do not
// know is dropped rather than carried: an unknown thinking_format writes no
// reasoning control at all, so passing one through would silently disable
// thinking on that row.
func TestApplyGatewayFacts_Dialect(t *testing.T) {
	cases := []struct {
		name       string
		compat     GatewayClientCompat
		wantFormat string
		wantMaxTok string
	}{
		{
			name:       "a known dialect and token spelling are carried",
			compat:     GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingFormat: "zai", MaxTokensField: "max_completion_tokens"}},
			wantFormat: "zai",
			wantMaxTok: "max_completion_tokens",
		},
		{
			name:       "an unknown dialect is dropped",
			compat:     GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingFormat: "claude", MaxTokensField: "max_tokens"}},
			wantMaxTok: "max_tokens",
		},
		{
			name:       "an unknown token spelling is dropped",
			compat:     GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingFormat: "openai", MaxTokensField: "max_output"}},
			wantFormat: "openai",
		},
		{
			// Both fields stay unset, so a curated row's own dialect survives.
			name:   "an absent block states no dialect",
			compat: GatewayClientCompat{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := registry.Caps{}
			ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{}, tc.compat)
			if got := registry.StringValue(caps.ThinkingFormat); got != tc.wantFormat {
				t.Fatalf("ThinkingFormat = %q, want %q", got, tc.wantFormat)
			}
			if got := registry.StringValue(caps.MaxTokensField); got != tc.wantMaxTok {
				t.Fatalf("MaxTokensField = %q, want %q", got, tc.wantMaxTok)
			}
		})
	}
}

// TestApplyGatewayFacts_EffortUnsupported pins a model the gateway says takes
// no effort parameter: derivation must not infer an effort control, and a map
// published beside the verdict cannot reach the wire, so neither the ladder nor
// the effort name survives.
func TestApplyGatewayFacts_EffortUnsupported(t *testing.T) {
	no := false
	cases := []struct {
		name   string
		compat GatewayClientCompat
	}{
		{
			name:   "no map",
			compat: GatewayClientCompat{Pi: &GatewayPiCompat{SupportsReasoningEffort: &no}},
		},
		{
			name: "a map beside the verdict",
			compat: GatewayClientCompat{Pi: &GatewayPiCompat{
				SupportsReasoningEffort: &no,
				ThinkingLevelMap:        map[string]string{"low": "low", "high": "high"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := registry.Caps{}
			ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{}, tc.compat)
			if len(caps.EffortValues) != 0 {
				t.Fatalf("EffortValues = %#v, want none", caps.EffortValues)
			}
			// The raw row states the verdict; derivation is what turns it into
			// toggle-only controls, because only derivation can preserve a
			// numeric budget control that came from another layer.
			if !registry.BoolValue(caps.EffortParameterUnsupported) {
				t.Fatal("the row must record the no-effort verdict")
			}
			// Non-nil, or mergeCaps inherits a curated ladder and derivation
			// re-adds the effort control the gateway just said not to use.
			if caps.EffortValues == nil {
				t.Fatal("a toggle-only row must publish an empty ladder, not a nil one")
			}
		})
	}
}

// TestApplyGatewayFacts_NonReasoningBeatsEffortVerdict pins the precedence the
// capability verdict has over the compat block: a gateway saying the model does
// not reason at all leaves no reasoning controls, whatever the block's effort
// field says. Controls on a Reasoning=false row are a shape derivation exists
// to forbid.
func TestApplyGatewayFacts_NonReasoningBeatsEffortVerdict(t *testing.T) {
	no := false
	caps := registry.Caps{}
	ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{Reasoning: &no}, GatewayClientCompat{
		Pi: &GatewayPiCompat{SupportsReasoningEffort: &no, ThinkingLevelMap: map[string]string{"low": "low"}},
	})
	if caps.Reasoning == nil || *caps.Reasoning {
		t.Fatalf("Reasoning = %v, want false", caps.Reasoning)
	}
	if len(caps.ReasoningControls) != 0 {
		t.Fatalf("ReasoningControls = %#v, want none on a non-reasoning row", caps.ReasoningControls)
	}
	if len(caps.EffortValues) != 0 {
		t.Fatalf("EffortValues = %#v, want none on a non-reasoning row", caps.EffortValues)
	}
}

// TestApplyGatewayFacts_EffortUnsupportedStillProvesReasoning pins that the
// effort verdict and the reasoning evidence are independent. A usable map
// proves the model reasons even when the gateway says it takes no effort
// parameter; keeping the weaker inferred verdict would make derivation clear
// the controls and request shaping drop reasoning entirely.
func TestApplyGatewayFacts_EffortUnsupportedStillProvesReasoning(t *testing.T) {
	no := false
	caps := registry.Caps{}
	ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{}, GatewayClientCompat{
		Pi: &GatewayPiCompat{
			SupportsReasoningEffort: &no,
			ThinkingLevelMap:        map[string]string{"low": "low", "high": "high"},
		},
	})
	if caps.Reasoning == nil || !*caps.Reasoning {
		t.Fatalf("Reasoning = %v, want the usable map to prove reasoning", caps.Reasoning)
	}
	if !registry.BoolValue(caps.EffortParameterUnsupported) {
		t.Fatal("the row must record the no-effort verdict")
	}
}

// TestApplyGatewayFacts_EmptyMapProvesNothing pins the other side of that
// evidence rule: a published map with no usable level says the model takes no
// effort control, and is not proof the model reasons at all. Raising the
// verdict for it would open a thinking-enable object on a non-reasoning model.
func TestApplyGatewayFacts_EmptyMapProvesNothing(t *testing.T) {
	no := false
	for name, levels := range map[string]map[string]string{
		"empty":   {},
		"all-nil": {"low": "", "high": ""},
	} {
		t.Run(name, func(t *testing.T) {
			caps := registry.Caps{}
			ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{}, GatewayClientCompat{
				Pi: &GatewayPiCompat{SupportsReasoningEffort: &no, ThinkingLevelMap: levels},
			})
			if caps.Reasoning != nil {
				t.Fatalf("Reasoning = %v, want the inferred verdict left alone", *caps.Reasoning)
			}
		})
	}
}

// TestApplyGatewayFacts_PositiveVerdictClearsInheritedRejection pins the
// verdict's other direction. An alias target or curated layer can carry a
// no-effort rejection; a gateway that states true knows better for this row, and
// leaving the stale rejection would make derivation discard the ladder the
// gateway just advertised.
func TestApplyGatewayFacts_PositiveVerdictClearsInheritedRejection(t *testing.T) {
	yes := true
	caps := registry.Caps{
		Reasoning:                  new(true),
		ReasoningControls:          []string{"effort"},
		EffortValues:               []string{"low", "high"},
		EffortParameterUnsupported: new(true),
	}
	ApplyGatewayFacts(&caps, GatewayArchitecture{}, GatewayCapabilities{}, GatewayClientCompat{
		Pi: &GatewayPiCompat{SupportsReasoningEffort: &yes, ThinkingLevelMap: map[string]string{"low": "low", "high": "high"}},
	})
	if registry.BoolValue(caps.EffortParameterUnsupported) {
		t.Fatal("a positive gateway verdict must clear an inherited rejection")
	}
	if !slices.Equal(caps.EffortValues, []string{"low", "high"}) {
		t.Fatalf("EffortValues = %#v, want the advertised ladder kept", caps.EffortValues)
	}
	if !caps.EffortCapable() {
		t.Fatal("the row must stay effort-capable once the rejection is cleared")
	}
}

func TestApplyGatewayFacts_Reasoning(t *testing.T) {
	yes, no := true, false
	levels := func(m map[string]string) GatewayClientCompat {
		return GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingLevelMap: m}}
	}
	ladder := map[string]string{"low": "low", "high": "high", "max": "max"}
	cases := []struct {
		name         string
		capsFacts    GatewayCapabilities
		compat       GatewayClientCompat
		before       []string
		beforeReason *bool
		wantReason   *bool
		wantLadder   []string
	}{
		{
			name:       "a verdict and map fill both facts",
			capsFacts:  GatewayCapabilities{Reasoning: &yes},
			compat:     levels(ladder),
			wantReason: &yes,
			wantLadder: []string{"low", "high", "max"},
		},
		{
			name:       "a non-reasoning verdict keeps no ladder",
			capsFacts:  GatewayCapabilities{Reasoning: &no},
			compat:     levels(ladder),
			wantReason: &no,
			wantLadder: nil,
		},
		{
			// A listing that publishes both supported_efforts and a
			// non-reasoning verdict must not leave the raw live row carrying a
			// ladder: it reaches ApplyLive before derivation clears one.
			name:       "a non-reasoning verdict clears an advertised ladder",
			capsFacts:  GatewayCapabilities{Reasoning: &no},
			before:     []string{"low", "high"},
			wantReason: &no,
			wantLadder: nil,
		},
		{
			name:       "no verdict still takes the map",
			compat:     levels(ladder),
			wantReason: &yes,
			wantLadder: []string{"low", "high", "max"},
		},
		{
			// A usable ladder is itself evidence the model tunes effort, even
			// when the row arrived with a false verdict inferred from a
			// provider-wide parameter list or the catalog.
			name:         "a usable ladder raises an inherited false verdict",
			compat:       levels(ladder),
			beforeReason: &no,
			wantReason:   &yes,
			wantLadder:   []string{"low", "high", "max"},
		},
		{
			// A published map with no usable level is the gateway saying the
			// model has no effort control: it must not inherit a curated
			// ladder, and it does not imply reasoning.
			name:       "a published empty map clears an inherited ladder",
			compat:     levels(map[string]string{"low": "", "high": ""}),
			before:     []string{"low", "medium", "high"},
			wantLadder: []string{},
		},
		{
			name:       "a verdict without a map states no ladder",
			capsFacts:  GatewayCapabilities{Reasoning: &yes},
			wantReason: &yes,
		},
		{
			name: "facts the listing does not state stay unset",
		},
		{
			// A Pi block with only other compat fields carries no effort
			// verdict: an inherited ladder must survive.
			name:       "a pi block without a map keeps an inherited ladder",
			compat:     GatewayClientCompat{Pi: &GatewayPiCompat{}},
			before:     []string{"low", "medium", "high"},
			wantLadder: []string{"low", "medium", "high"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := registry.Caps{EffortValues: tc.before, Reasoning: tc.beforeReason}
			ApplyGatewayFacts(&caps, GatewayArchitecture{}, tc.capsFacts, tc.compat)
			if (caps.Reasoning == nil) != (tc.wantReason == nil) || (caps.Reasoning != nil && *caps.Reasoning != *tc.wantReason) {
				t.Fatalf("Reasoning = %v, want %v", caps.Reasoning, tc.wantReason)
			}
			if !slices.Equal(caps.EffortValues, tc.wantLadder) {
				t.Fatalf("EffortValues = %v, want %v", caps.EffortValues, tc.wantLadder)
			}
			if tc.wantLadder == nil && caps.EffortValues != nil && len(caps.EffortValues) == 0 && tc.compat.Pi == nil {
				t.Fatalf("an absent map must leave the ladder unset, got %#v", caps.EffortValues)
			}
		})
	}
}
