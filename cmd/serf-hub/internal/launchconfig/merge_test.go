package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }

func TestMerge_ScalarPrecedence(t *testing.T) {
	g := Layer{Model: "g-model", FastCheapModel: "g-fast", ReasoningEffort: "low", OpenAIResponsesContinuation: "off"}
	r := Layer{Model: "r-model", FastCheapModel: "r-fast"}
	p := Layer{}
	l := Layer{Model: "l-model", FastCheapModel: "l-fast", OpenAIResponsesContinuation: "auto"}
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
	if got.Effective.OpenAIResponsesContinuation != "auto" {
		t.Errorf("OpenAIResponsesContinuation = %q, want auto (per-launch wins)", got.Effective.OpenAIResponsesContinuation)
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
	if got.Provenance["openai_responses_continuation"] != LayerLaunch {
		t.Errorf("Provenance[openai_responses_continuation] = %q, want launch", got.Provenance["openai_responses_continuation"])
	}
}

func TestMerge_ScalarPointerSemantics(t *testing.T) {
	g := Layer{MaxRounds: ptrInt(200), NonInteractive: ptrBool(true), RawHTTPLogging: ptrBool(true)}
	l := Layer{NonInteractive: ptrBool(false), RawHTTPLogging: ptrBool(false)}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.MaxRounds == nil || *got.Effective.MaxRounds != 200 {
		t.Errorf("MaxRounds = %v, want 200 (launch did not override)", got.Effective.MaxRounds)
	}
	if got.Effective.NonInteractive == nil || *got.Effective.NonInteractive {
		t.Errorf("NonInteractive = %v, want explicit launch false", got.Effective.NonInteractive)
	}
	if got.Effective.RawHTTPLogging == nil || *got.Effective.RawHTTPLogging {
		t.Errorf("RawHTTPLogging = %v, want explicit launch false", got.Effective.RawHTTPLogging)
	}
	if got.Provenance["raw_http_logging"] != LayerLaunch {
		t.Errorf("Provenance[raw_http_logging] = %q, want launch", got.Provenance["raw_http_logging"])
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

func TestMergeLayers_LegacySystemPromptAppendMigratesOneEntry(t *testing.T) {
	resolved, diags := mergeLayers(map[LayerName]Layer{
		LayerGlobal: {SystemPromptAppend: []string{"/legacy-append.md"}},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if resolved.Effective.SystemPromptAppendMode != "file" || resolved.Effective.SystemPromptAppendFile != "/legacy-append.md" {
		t.Fatalf("effective append prompt = %#v", resolved.Effective)
	}
	if len(resolved.Effective.SystemPromptAppend) != 0 {
		t.Fatalf("legacy append list should not remain effective: %#v", resolved.Effective.SystemPromptAppend)
	}
	if resolved.Provenance["system_prompt_append_mode"] != LayerGlobal {
		t.Fatalf("provenance[system_prompt_append_mode] = %q, want global", resolved.Provenance["system_prompt_append_mode"])
	}
}

func TestMergeLayers_LegacySystemPromptAppendMultiEntryUsesFirstWithDiagnostic(t *testing.T) {
	resolved, diags := mergeLayers(map[LayerName]Layer{
		LayerProject: {SystemPromptAppend: []string{"/first.md", "/second.md"}},
	})
	if resolved.Effective.SystemPromptAppendMode != "file" || resolved.Effective.SystemPromptAppendFile != "/first.md" {
		t.Fatalf("effective append prompt = %#v", resolved.Effective)
	}
	var seen bool
	for _, d := range diags {
		if d.Layer == LayerProject && d.Field == "system_prompt_append" && strings.Contains(d.Message, "UI supports one append source") {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("expected multi-entry legacy diagnostic, got %v", diags)
	}
}

func TestMergeLayers_ExplicitAppendModeBeatsLegacyAppendSameLayer(t *testing.T) {
	resolved, diags := mergeLayers(map[LayerName]Layer{
		LayerLaunch: {
			SystemPromptAppendMode: "inline",
			SystemPromptAppendText: "new append",
			SystemPromptAppend:     []string{"/legacy.md", "/ignored.md"},
		},
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if resolved.Effective.SystemPromptAppendMode != "inline" || resolved.Effective.SystemPromptAppendText != "new append" {
		t.Fatalf("effective append prompt = %#v", resolved.Effective)
	}
	if resolved.Effective.SystemPromptAppendFile != "" || len(resolved.Effective.SystemPromptAppend) != 0 {
		t.Fatalf("legacy append should not contribute with explicit mode: %#v", resolved.Effective)
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
	g := Layer{ModelFallbacks: &[]string{"openai/gpt-5.2", "anthropic/claude-opus-4-6"}}
	l := Layer{ModelFallbacks: &[]string{"openai/gpt-5.4"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	want := []string{"openai/gpt-5.4"}
	if got.Effective.ModelFallbacks == nil || !reflect.DeepEqual(*got.Effective.ModelFallbacks, want) {
		t.Errorf("ModelFallbacks = %v, want %v (replace, not append)", got.Effective.ModelFallbacks, want)
	}
	if got.Provenance["model_fallbacks"] != LayerLaunch {
		t.Errorf("provenance[model_fallbacks] = %q, want %q", got.Provenance["model_fallbacks"], LayerLaunch)
	}
}

func TestMerge_ModelFallbacksGlobalOnly(t *testing.T) {
	g := Layer{ModelFallbacks: &[]string{"openai/gpt-5.4"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	want := []string{"openai/gpt-5.4"}
	if got.Effective.ModelFallbacks == nil || !reflect.DeepEqual(*got.Effective.ModelFallbacks, want) {
		t.Errorf("ModelFallbacks = %v, want %v", got.Effective.ModelFallbacks, want)
	}
}

func TestMerge_EmptyModelFallbacksClearsInherited(t *testing.T) {
	g := Layer{ModelFallbacks: &[]string{"openai/gpt-5.4"}}
	l := Layer{ModelFallbacks: &[]string{}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.ModelFallbacks == nil {
		t.Fatalf("ModelFallbacks = nil, want explicit empty slice")
	}
	if len(*got.Effective.ModelFallbacks) != 0 {
		t.Fatalf("ModelFallbacks = %v, want empty", *got.Effective.ModelFallbacks)
	}
	if got.Provenance["model_fallbacks"] != LayerLaunch {
		t.Errorf("provenance[model_fallbacks] = %q, want %q", got.Provenance["model_fallbacks"], LayerLaunch)
	}
}

func TestMerge_BlockedCredentialEnvKeys(t *testing.T) {
	g := Layer{Env: map[string]string{"OPENAI_API_KEY": "leak"}}
	got, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	if len(diags) == 0 || diags[0].Field != "env.OPENAI_API_KEY" {
		t.Errorf("expected blocklist diagnostic, got %v", diags)
	}
	if v, present := got.Effective.Env["OPENAI_API_KEY"]; present {
		t.Errorf("credential key must be absent from Effective.Env, got %q", v)
	}
}

func TestMerge_CoversRemainingScalarAndListFields(t *testing.T) {
	g := Layer{
		Agent:            "global-agent",
		ContextStrategy:  "global-ctx",
		MaxSubagentDepth: ptrInt(3),
		NoProjectPrompts: ptrBool(false),
		PluginDirs:       []string{"/g/plugin"},
		MCPConfigs:       []string{"/g/mcp.json"},
	}
	l := Layer{
		Agent:            "launch-agent",
		ContextStrategy:  "launch-ctx",
		MaxSubagentDepth: ptrInt(5),
		NoProjectPrompts: ptrBool(true),
		PluginDirs:       []string{"/l/plugin"},
		MCPConfigs:       []string{"/l/mcp.json"},
	}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.Agent != "launch-agent" {
		t.Errorf("Agent = %q", got.Effective.Agent)
	}
	if got.Effective.ContextStrategy != "launch-ctx" {
		t.Errorf("ContextStrategy = %q", got.Effective.ContextStrategy)
	}
	if got.Effective.MaxSubagentDepth == nil || *got.Effective.MaxSubagentDepth != 5 {
		t.Errorf("MaxSubagentDepth = %v", got.Effective.MaxSubagentDepth)
	}
	if got.Effective.NoProjectPrompts == nil || *got.Effective.NoProjectPrompts != true {
		t.Errorf("NoProjectPrompts = %v", got.Effective.NoProjectPrompts)
	}
	wantPlugins := []string{"/g/plugin", "/l/plugin"}
	if !reflect.DeepEqual(got.Effective.PluginDirs, wantPlugins) {
		t.Errorf("PluginDirs = %v, want %v", got.Effective.PluginDirs, wantPlugins)
	}
	wantMCPConfigs := []string{"/g/mcp.json", "/l/mcp.json"}
	if !reflect.DeepEqual(got.Effective.MCPConfigs, wantMCPConfigs) {
		t.Errorf("MCPConfigs = %v, want %v", got.Effective.MCPConfigs, wantMCPConfigs)
	}
}

func TestMerge_AppReplaySizeNonGlobalDiagnostic(t *testing.T) {
	l := Layer{AppReplaySize: ptrInt(10)}
	got, diags := mergeLayers(map[LayerName]Layer{LayerLaunch: l})
	var seen bool
	for _, d := range diags {
		if d.Field == "app_replay_size" && d.Layer == LayerLaunch {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("expected app_replay_size diagnostic for non-global layer, got %v", diags)
	}
	if got.Effective.AppReplaySize != nil {
		t.Errorf("non-global app_replay_size must be rejected, got Effective.AppReplaySize = %v", *got.Effective.AppReplaySize)
	}
	if name, ok := got.Provenance["app_replay_size"]; ok {
		t.Errorf("rejected app_replay_size must not appear in provenance, got %q", name)
	}
}

func TestMerge_AppReplaySizeGlobalApplied(t *testing.T) {
	g := Layer{AppReplaySize: ptrInt(10)}
	got, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	for _, d := range diags {
		if d.Field == "app_replay_size" {
			t.Errorf("global app_replay_size must not emit a diagnostic, got %v", d)
		}
	}
	if got.Effective.AppReplaySize == nil || *got.Effective.AppReplaySize != 10 {
		t.Fatalf("global app_replay_size must be applied, got Effective.AppReplaySize = %v", got.Effective.AppReplaySize)
	}
	if got.Provenance["app_replay_size"] != LayerGlobal {
		t.Errorf("provenance[app_replay_size] = %q, want %q", got.Provenance["app_replay_size"], LayerGlobal)
	}
}

func TestIsCredentialEnvKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"OPENAI_API_KEY", true},
		{"MY_TOKEN", true},
		{"SECRET", true},
		{"PASSWORD", true},
		{"CREDENTIAL", true},
		{"SOME_KEY", true},
		{"HOME", false},
		{"PATH", false},
		{"USER", false},
	}
	for _, c := range cases {
		if got := IsCredentialEnvKey(c.key); got != c.want {
			t.Errorf("IsCredentialEnvKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}
