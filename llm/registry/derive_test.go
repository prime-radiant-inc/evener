package registry

import (
	"reflect"
	"testing"
)

func anth(fam string, controls ...string) (Caps, Model, deriveInput) {
	return Caps{ReasoningControls: controls}, Model{Family: fam}, deriveInput{Protocol: ProtocolAnthropic, ProviderSurface: SurfaceAnthropic, ProviderFamily: "claude"}
}

func TestDerive_ThinkingShapes(t *testing.T) {
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
	caps, m, in := anth("claude-opus", "effort")
	caps.Reasoning = new(false)
	caps.EffortValues = []string{"low", "high"}
	derive(&caps, &m, in, map[string]string{})
	if caps.ReasoningControls != nil || caps.EffortValues != nil || caps.ThinkingShape != nil || caps.ThinkingAlwaysOn != nil {
		t.Fatalf("reasoning = false must clear controls, ladder, and shape: %+v", caps)
	}
}

func TestDerive_EffortControlRules(t *testing.T) {
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
