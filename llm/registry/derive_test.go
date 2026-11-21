package registry

import (
	"reflect"
	"testing"
)

func anth(fam string, controls ...string) (Caps, Model, deriveInput) {
	return Caps{ReasoningControls: controls}, Model{Family: fam}, deriveInput{Protocol: ProtocolAnthropic, ProviderSurface: SurfaceAnthropic, ProviderFamily: "claude"}
}

func TestDerive_GoogleThinkingShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		controls []string
		synth    bool
		want     string
	}{
		{"cataloged 3.x effort-only", []string{"effort"}, false, "level"},
		{"cataloged 2.5 budget-only derives no level", []string{"budget_tokens"}, false, ""},
		{"cataloged 2.5 budget+toggle derives no level", []string{"budget_tokens", "toggle"}, false, ""},
		{"cataloged reasoning, options unrecorded", nil, false, ""},
		{"toggle-only row", []string{"toggle"}, false, ""},
		{"uncataloged live-listed", nil, true, ""},
	}
	for _, c := range cases {
		// A row with toggle-only controls but an inherited effort ladder:
		// the pass-through appends effort for the ladder, but that invented
		// control is not evidence for the shape (roborev round 12).
		if c.name == "toggle-only row" {
			t.Run("toggle-only with inherited ladder", func(t *testing.T) {
				caps := Caps{Reasoning: new(true), ReasoningControls: []string{"toggle"}, EffortValues: []string{"low", "high"}}
				m := Model{Family: "gemini"}
				in := deriveInput{Protocol: ProtocolGoogle, ProviderSurface: SurfaceGoogle}
				prov := map[string]string{}
				derive(&caps, &m, in, prov)
				if caps.ThinkingShape != nil {
					t.Fatalf("toggle-only row with an inherited ladder must derive no shape, got %v (controls %v, prov %v)", *caps.ThinkingShape, caps.ReasoningControls, prov)
				}
			})
		}
		t.Run(c.name, func(t *testing.T) {
			caps := Caps{Reasoning: new(true), ReasoningControls: c.controls}
			m := Model{Family: "gemini"}
			in := deriveInput{Protocol: ProtocolGoogle, ProviderSurface: SurfaceGoogle, Synthesized: c.synth}
			prov := map[string]string{}
			derive(&caps, &m, in, prov)
			got := ""
			if caps.ThinkingShape != nil {
				got = *caps.ThinkingShape
			}
			if got != c.want {
				t.Fatalf("shape = %q, want %q (controls %v)", got, c.want, caps.ReasoningControls)
			}
			if got != "" && prov["ThinkingShape"] != "derived" {
				t.Fatalf("provenance = %v", prov)
			}
		})
	}
}

func TestDerive_ThinkingShapes(t *testing.T) {
	t.Parallel()
	type want struct {
		shape, display string
		alwaysOn       bool
	}
	cases := []struct {
		name     string
		fam      string
		controls []string
		synth    bool
		provFam  string
		want     want
	}{
		{"sonnet-4-5 budget only", "claude-sonnet", []string{"budget_tokens"}, false, "claude", want{"budget", "", false}},
		{"haiku-4-5", "claude-haiku", []string{"budget_tokens"}, false, "claude", want{"budget", "", false}},
		{"opus-4-6 effort+budget", "claude-opus", []string{"effort", "budget_tokens"}, false, "claude", want{"adaptive", "", true}},
		{"opus-4-7 effort only", "claude-opus", []string{"effort"}, false, "claude", want{"adaptive", "summarized", true}},
		{"sonnet-5 toggle+effort", "claude-sonnet", []string{"toggle", "effort"}, false, "claude", want{"adaptive", "summarized", true}},
		{"minimax toggle only", "minimax", []string{"toggle"}, false, "", want{"budget", "", false}},
		{"kimi k3 effort+toggle, not claude", "kimi-k3", []string{"toggle", "effort"}, false, "", want{"", "", false}},
		{"uncataloged claude on anthropic", "", nil, true, "claude", want{"adaptive", "summarized", true}},
		{"uncataloged on anthropic-compatible", "", nil, true, "", want{"budget", "", false}},
		{"cataloged, controls empty, not claude", "glm", nil, false, "", want{"budget", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps, m, in := anth(c.fam, c.controls...)
			in.Synthesized, in.ProviderFamily = c.synth, c.provFam
			prov := map[string]string{}
			derive(&caps, &m, in, prov)
			got := want{}
			if caps.ThinkingShape != nil {
				got.shape = *caps.ThinkingShape
			}
			if caps.ThinkingDisplay != nil {
				got.display = *caps.ThinkingDisplay
			}
			got.alwaysOn = caps.ThinkingAlwaysOn != nil && *caps.ThinkingAlwaysOn
			if got != c.want {
				t.Fatalf("got %+v want %+v (controls now %v)", got, c.want, caps.ReasoningControls)
			}
			if got.shape != "" && prov["ThinkingShape"] != "derived" {
				t.Fatalf("provenance = %v", prov)
			}
		})
	}
}

func TestDerive_PinnedShapeAndOtherProtocolsUntouched(t *testing.T) {
	t.Parallel()
	shape := "budget+effort"
	caps, m, in := anth("claude-opus", "effort", "budget_tokens")
	caps.ThinkingShape = &shape
	derive(&caps, &m, in, map[string]string{})
	if *caps.ThinkingShape != "budget+effort" || caps.ThinkingAlwaysOn != nil || caps.ThinkingDisplay != nil {
		t.Fatalf("a pinned shape must be kept and never gain always-on: %+v", caps)
	}
	caps = Caps{ReasoningControls: []string{"effort"}}
	m = Model{Family: "claude-opus"}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat, ProviderSurface: SurfaceGeneric}, map[string]string{})
	if caps.ThinkingShape != nil || caps.ThinkingAlwaysOn != nil {
		t.Fatal("thinking shape is derived on the anthropic protocol only")
	}
}

func TestDerive_ReasoningFalseClearsEverything(t *testing.T) {
	t.Parallel()
	caps, m, in := anth("claude-opus", "effort")
	caps.Reasoning = new(false)
	caps.EffortValues = []string{"low", "high"}
	derive(&caps, &m, in, map[string]string{})
	if caps.ReasoningControls != nil || caps.EffortValues != nil || caps.ThinkingShape != nil || caps.ThinkingAlwaysOn != nil {
		t.Fatalf("reasoning = false must clear controls, ladder, and shape: %+v", caps)
	}
}

func TestDerive_EffortControlRules(t *testing.T) {
	t.Parallel()
	caps := Caps{}
	m := Model{}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, map[string]string{})
	if !reflect.DeepEqual(caps.ReasoningControls, []string{"effort"}) {
		t.Fatalf("empty controls must pass effort through: %v", caps.ReasoningControls)
	}
	caps = Caps{ReasoningControls: []string{"toggle"}, EffortValues: []string{"high", "max"}}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, map[string]string{})
	if !reflect.DeepEqual(caps.ReasoningControls, []string{"toggle", "effort"}) {
		t.Fatalf("a non-empty ladder implies effort: %v", caps.ReasoningControls)
	}
	caps = Caps{ReasoningControls: []string{"budget_tokens"}}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, map[string]string{})
	if !reflect.DeepEqual(caps.ReasoningControls, []string{"budget_tokens"}) {
		t.Fatalf("explicit controls without effort stay as listed: %v", caps.ReasoningControls)
	}
}

func TestDerive_MaxOutputTokensJunkCap(t *testing.T) {
	t.Parallel()
	caps := Caps{ContextWindow: new(262144), MaxOutputTokens: new(262144)}
	m := Model{}
	prov := map[string]string{"MaxOutputTokens": "snapshot/row"}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, prov)
	if caps.MaxOutputTokens != nil || prov["MaxOutputTokens"] != "derived" {
		t.Fatalf("catalog cap ≥ window must be cleared: %v %v", caps.MaxOutputTokens, prov)
	}
	caps = Caps{ContextWindow: new(1000), MaxOutputTokens: new(2000)}
	prov = map[string]string{"MaxOutputTokens": "config/row"}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, prov)
	if caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 2000 {
		t.Fatal("a user-layer cap is kept as written")
	}
	caps = Caps{ContextWindow: new(1000), MaxOutputTokens: new(2000)}
	prov = map[string]string{"MaxOutputTokens": "live"}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, prov)
	if caps.MaxOutputTokens != nil {
		t.Fatal("a live cap ≥ window is cleared")
	}
}

func TestDerive_SurfaceFallback(t *testing.T) {
	t.Parallel()
	caps := Caps{}
	m := Model{}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat, ProviderSurface: SurfaceAnthropic}, map[string]string{})
	if m.Surface != SurfaceAnthropic {
		t.Fatalf("provider surface must apply to a family-less row, got %q", m.Surface)
	}
	m = Model{}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat}, map[string]string{})
	if m.Surface != SurfaceGeneric {
		t.Fatalf("generic fallback, got %q", m.Surface)
	}
	m = Model{Surface: SurfaceGoogle}
	derive(&caps, &m, deriveInput{Protocol: ProtocolOpenAIChat, ProviderSurface: SurfaceAnthropic}, map[string]string{})
	if m.Surface != SurfaceGoogle {
		t.Fatal("a row surface wins")
	}
}

// TestDerive_CombinedSearchAndFunctionsAndVertexGeminiOmni pins the two
// capability rules Stage 1 lands: the new pairing cap decodes from TOML and
// layers with provenance like every other optional cap, a row that takes no
// tools drops it, and Vertex Gemini Omni rows drop web search however the
// provider-wide config set it.
func TestDerive_CombinedSearchAndFunctionsAndVertexGeminiOmni(t *testing.T) {
	t.Parallel()

	t.Run("layers from TOML at provider and row", func(t *testing.T) {
		l, err := ParseConfig([]byte(`
[providers.acme]
protocol = "google"
combined_search_and_functions = true

[providers.acme.models."gemini-x"]
combined_search_and_functions = false
`))
		if err != nil {
			t.Fatal(err)
		}
		provider := l.Providers["acme"]
		if provider.Caps.CombinedSearchAndFunctions == nil || !*provider.Caps.CombinedSearchAndFunctions {
			t.Fatalf("provider cap = %v, want true", provider.Caps.CombinedSearchAndFunctions)
		}
		row := provider.Models["gemini-x"]
		if row.Caps.CombinedSearchAndFunctions == nil || *row.Caps.CombinedSearchAndFunctions {
			t.Fatalf("row cap = %v, want the explicit false", row.Caps.CombinedSearchAndFunctions)
		}
	})

	t.Run("layers and is provenanced like the other caps", func(t *testing.T) {
		var caps Caps
		prov := map[string]string{}
		mergeCaps(&caps, Caps{CombinedSearchAndFunctions: new(true)}, LayerConfig+"/provider", prov)
		if !BoolValue(caps.CombinedSearchAndFunctions) || prov["CombinedSearchAndFunctions"] != LayerConfig+"/provider" {
			t.Fatalf("provider layer: cap = %v prov = %v", BoolValue(caps.CombinedSearchAndFunctions), prov)
		}
		mergeCaps(&caps, Caps{CombinedSearchAndFunctions: new(false)}, LayerConfig+"/row", prov)
		if BoolValue(caps.CombinedSearchAndFunctions) || prov["CombinedSearchAndFunctions"] != LayerConfig+"/row" {
			t.Fatalf("row layer: cap = %v prov = %v", BoolValue(caps.CombinedSearchAndFunctions), prov)
		}
	})

	t.Run("a tools-less row drops the pairing cap", func(t *testing.T) {
		caps := Caps{Tools: new(false), CombinedSearchAndFunctions: new(true)}
		prov := map[string]string{}
		derive(&caps, &Model{WireID: "gemini-3.8-flash"}, deriveInput{Protocol: ProtocolGoogle}, prov)
		if BoolValue(caps.CombinedSearchAndFunctions) || prov["CombinedSearchAndFunctions"] != provDerived {
			t.Fatalf("cap = %v prov = %v, want a derived false", BoolValue(caps.CombinedSearchAndFunctions), prov)
		}
	})

	t.Run("tools true or unset keeps the pairing cap", func(t *testing.T) {
		caps := Caps{Tools: new(true), CombinedSearchAndFunctions: new(true)}
		derive(&caps, &Model{WireID: "gemini-3.8-flash"}, deriveInput{Protocol: ProtocolGoogle}, map[string]string{})
		if !BoolValue(caps.CombinedSearchAndFunctions) {
			t.Fatal("a tool-capable row keeps the pairing cap")
		}
		caps = Caps{CombinedSearchAndFunctions: new(true)}
		derive(&caps, &Model{WireID: "gemini-3.8-flash"}, deriveInput{Protocol: ProtocolGoogle}, map[string]string{})
		if !BoolValue(caps.CombinedSearchAndFunctions) {
			t.Fatal("an unstated Tools cap is not a refusal")
		}
		caps = Caps{Tools: new(false)}
		derive(&caps, &Model{WireID: "gemini-3.8-flash"}, deriveInput{Protocol: ProtocolGoogle}, map[string]string{})
		if caps.CombinedSearchAndFunctions != nil {
			t.Fatalf("derivation must not invent a cap: %v", caps.CombinedSearchAndFunctions)
		}
	})

	t.Run("IsVertexGeminiOmni", func(t *testing.T) {
		if !IsVertexGeminiOmni(PresetVertexGemini, "gemini-omni-1.2-flash") {
			t.Fatal("a Vertex preset with a gemini-omni id is Vertex Gemini Omni")
		}
		if !IsVertexGeminiOmni(PresetVertexGemini, " Gemini-Omni-1.2-Flash ") {
			t.Fatal("IsVertexGeminiOmni must trim whitespace and match case-insensitively")
		}
		if IsVertexGeminiOmni("", "acme-omni-chat") || IsVertexGeminiOmni("", "gemini-omni-1.2-flash") {
			t.Fatal("the predicate needs both the Vertex preset and the gemini-omni prefix")
		}
	})

	t.Run("vertex omni clamps web search", func(t *testing.T) {
		caps := Caps{WebSearch: new(true)}
		prov := map[string]string{"WebSearch": LayerConfig + "/provider"}
		derive(&caps, &Model{WireID: "gemini-omni-1.2-flash"}, deriveInput{Protocol: ProtocolGoogle, TransportPreset: PresetVertexGemini}, prov)
		if BoolValue(caps.WebSearch) || caps.WebSearch == nil || prov["WebSearch"] != provDerived {
			t.Fatalf("web search = %v prov = %v, want a derived false", caps.WebSearch, prov)
		}
	})

	t.Run("non-omni rows keep web search", func(t *testing.T) {
		caps := Caps{WebSearch: new(true)}
		prov := map[string]string{"WebSearch": LayerConfig + "/provider"}
		derive(&caps, &Model{WireID: "gemini-omni-1.2-flash"}, deriveInput{Protocol: ProtocolGoogle}, prov)
		if !BoolValue(caps.WebSearch) || prov["WebSearch"] != LayerConfig+"/provider" {
			t.Fatalf("a non-Vertex omni row keeps web search: %v %v", caps.WebSearch, prov)
		}
		caps = Caps{WebSearch: new(true)}
		prov = map[string]string{"WebSearch": LayerConfig + "/provider"}
		derive(&caps, &Model{WireID: "acme-omni-chat"}, deriveInput{Protocol: ProtocolGoogle, TransportPreset: PresetVertexGemini}, prov)
		if !BoolValue(caps.WebSearch) || prov["WebSearch"] != LayerConfig+"/provider" {
			t.Fatalf("a non-gemini omni row keeps web search: %v %v", caps.WebSearch, prov)
		}
	})

	// The clamp is only worth anything if resolution hands derivation the
	// resolved transport's preset, so resolve a real Vertex Omni row whose
	// provider-wide config turns search on.
	t.Run("resolve threads the vertex preset into the clamp", func(t *testing.T) {
		r := fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global"}, `
[providers.google-vertex]
web_search = true
`)
		res := mustResolve(t, r, "google-vertex/gemini-omni-flash-preview")
		if res.Transport.Preset != PresetVertexGemini {
			t.Fatalf("transport preset = %q, want %q", res.Transport.Preset, PresetVertexGemini)
		}
		if !IsVertexGeminiOmni(res.Transport.Preset, res.WireID) {
			t.Fatalf("resolved row %q under preset %q is not Vertex Gemini Omni", res.WireID, res.Transport.Preset)
		}
		if BoolValue(res.Caps.WebSearch) || res.Caps.WebSearch == nil {
			t.Fatalf("web search = %v, want an explicit false", res.Caps.WebSearch)
		}
		if res.Provenance["WebSearch"] != provDerived {
			t.Fatalf("WebSearch provenance = %q, want %q", res.Provenance["WebSearch"], provDerived)
		}
	})
}
