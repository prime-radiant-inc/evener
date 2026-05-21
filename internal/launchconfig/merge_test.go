package launchconfig

import (
	"reflect"
	"testing"
)

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }

func TestMerge_ScalarPrecedence(t *testing.T) {
	g := Layer{Model: "g-model", FastCheapModel: "g-fast", ReasoningEffort: "low"}
	r := Layer{Model: "r-model", FastCheapModel: "r-fast"}
	p := Layer{}
	l := Layer{Model: "l-model", FastCheapModel: "l-fast"}
	got, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal: g, LayerRepo: r, LayerProject: p, LayerLaunch: l,
	})
	if got.Effective.Model != "l-model" {
		t.Errorf("Model = %q, want l-model (per-launch wins)", got.Effective.Model)
	}
	if got.Effective.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q, want low (only global set)", got.Effective.ReasoningEffort)
	}
	if got.Effective.FastCheapModel != "l-fast" {
		t.Errorf("FastCheapModel = %q, want l-fast (per-launch wins)", got.Effective.FastCheapModel)
	}
	if got.Provenance["model"] != LayerLaunch {
		t.Errorf("Provenance[model] = %q, want launch", got.Provenance["model"])
	}
	if got.Provenance["fast_cheap_model"] != LayerLaunch {
		t.Errorf("Provenance[fast_cheap_model] = %q, want launch", got.Provenance["fast_cheap_model"])
	}
	if got.Provenance["reasoning_effort"] != LayerGlobal {
		t.Errorf("Provenance[reasoning_effort] = %q, want global", got.Provenance["reasoning_effort"])
	}
}

func TestMerge_ScalarPointerSemantics(t *testing.T) {
	g := Layer{MaxRounds: ptrInt(200)}
	l := Layer{}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.MaxRounds == nil || *got.Effective.MaxRounds != 200 {
		t.Errorf("MaxRounds = %v, want 200 (launch did not override)", got.Effective.MaxRounds)
	}
}

func TestMergeLayers_SystemPromptModesOverrideByLayer(t *testing.T) {
	resolved, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal:  {SystemPromptMode: "file", SystemPromptFile: "/global.md", SystemPromptAppendMode: "file", SystemPromptAppendFile: "/global-append.md"},
		LayerProject: {SystemPromptMode: "inline", SystemPromptText: "project", SystemPromptAppendMode: "inline", SystemPromptAppendText: "project append"},
	})
	if resolved.Effective.SystemPromptMode != "inline" || resolved.Effective.SystemPromptText != "project" {
		t.Fatalf("effective system prompt = %#v", resolved.Effective)
	}
	if resolved.Effective.SystemPromptAppendMode != "inline" || resolved.Effective.SystemPromptAppendText != "project append" {
		t.Fatalf("effective append prompt = %#v", resolved.Effective)
	}
}

func TestMerge_ListAppendInLayerOrder(t *testing.T) {
	g := Layer{SkillsDirs: []string{"/g1", "/g2"}}
	r := Layer{SkillsDirs: []string{"/r1"}}
	p := Layer{SkillsDirs: []string{"/p1"}}
	l := Layer{SkillsDirs: []string{"/l1"}}
	got, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal: g, LayerRepo: r, LayerProject: p, LayerLaunch: l,
	})
	want := []string{"/g1", "/g2", "/r1", "/p1", "/l1"}
	if !reflect.DeepEqual(got.Effective.SkillsDirs, want) {
		t.Errorf("SkillsDirs = %v, want %v", got.Effective.SkillsDirs, want)
	}
}

func TestMerge_EnvMapLastWriteWins(t *testing.T) {
	g := Layer{Env: map[string]string{"A": "g", "B": "g"}}
	p := Layer{Env: map[string]string{"A": "p"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerProject: p})
	if got.Effective.Env["A"] != "p" {
		t.Errorf("Env[A] = %q, want p", got.Effective.Env["A"])
	}
	if got.Effective.Env["B"] != "g" {
		t.Errorf("Env[B] = %q, want g", got.Effective.Env["B"])
	}
}

func TestMerge_MCPsAppendWithDuplicateDiagnostic(t *testing.T) {
	g := Layer{MCPs: []MCPServerSpec{{Name: "x", Command: "x1"}}}
	p := Layer{MCPs: []MCPServerSpec{{Name: "x", Command: "x2"}}}
	got, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerProject: p})
	if len(got.Effective.MCPs) != 2 {
		t.Errorf("len(MCPs) = %d, want 2 (append, no dedup)", len(got.Effective.MCPs))
	}
	var seen bool
	for _, d := range diags {
		if d.Field == "mcps" && d.Layer == LayerProject {
			seen = true
		}
	}
	if !seen {
		t.Errorf("expected diagnostic for duplicate mcp name, got %v", diags)
	}
}

func TestMerge_ModelFallbacksReplaceNotAppend(t *testing.T) {
	// Kata cxw8: ModelFallbacks REPLACES rather than appends. Setting a
	// fallback chain at a higher-precedence layer (e.g. launch) replaces
	// any chain inherited from a lower layer.
	g := Layer{ModelFallbacks: []string{"openai/gpt-5.2", "anthropic/claude-opus-4-6"}}
	l := Layer{ModelFallbacks: []string{"openai/gpt-5.4"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	want := []string{"openai/gpt-5.4"}
	if !reflect.DeepEqual(got.Effective.ModelFallbacks, want) {
		t.Errorf("ModelFallbacks = %v, want %v (replace, not append)", got.Effective.ModelFallbacks, want)
	}
	if got.Provenance["model_fallbacks"] != LayerLaunch {
		t.Errorf("provenance[model_fallbacks] = %q, want %q", got.Provenance["model_fallbacks"], LayerLaunch)
	}
}

func TestMerge_ModelFallbacksGlobalOnly(t *testing.T) {
	g := Layer{ModelFallbacks: []string{"openai/gpt-5.4"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	want := []string{"openai/gpt-5.4"}
	if !reflect.DeepEqual(got.Effective.ModelFallbacks, want) {
		t.Errorf("ModelFallbacks = %v, want %v", got.Effective.ModelFallbacks, want)
	}
}

func TestMerge_EmptyModelFallbacksClearsInherited(t *testing.T) {
	g := Layer{ModelFallbacks: []string{"openai/gpt-5.4"}}
	l := Layer{ModelFallbacks: []string{}, ModelFallbacksSet: true}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.ModelFallbacks == nil {
		t.Fatalf("ModelFallbacks = nil, want explicit empty slice")
	}
	if len(got.Effective.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %v, want empty", got.Effective.ModelFallbacks)
	}
	if got.Provenance["model_fallbacks"] != LayerLaunch {
		t.Errorf("provenance[model_fallbacks] = %q, want %q", got.Provenance["model_fallbacks"], LayerLaunch)
	}
}

func TestMerge_BlockedCredentialEnvKeys(t *testing.T) {
	g := Layer{Env: map[string]string{"OPENAI_API_KEY": "leak"}}
	_, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	if len(diags) == 0 || diags[0].Field != "env.OPENAI_API_KEY" {
		t.Errorf("expected blocklist diagnostic, got %v", diags)
	}
}
