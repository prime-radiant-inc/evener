package launchconfig

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

// TestLaunchOptionValue_AllFields drives every field branch of the
// launchOptionValue switch, checking both the display value and the edit value.
func TestLaunchOptionValue_AllFields(t *testing.T) {
	layer := appwire.LaunchConfigLayer{
		Agent:                       "myagent",
		Model:                       "gpt-5",
		ReasoningEffort:             "high",
		FastCheapModel:              "mini",
		ContextStrategy:             "trim",
		OpenAIResponsesContinuation: "on",
		MaxRounds:                   intPtr(10),
		MaxSubagentDepth:            intPtr(3),
		NoProjectPrompts:            boolPtr(true),
		AppReplaySize:               intPtr(5),
		SystemPromptMode:            "custom",
		SystemPromptFile:            "/p",
		SystemPromptText:            "line1\nline2",
		SystemPromptAppendMode:      "append",
		SystemPromptAppendFile:      "/a",
		SystemPromptAppendText:      "a1\na2\na3",
		SkillsDirs:                  []string{"s1", "s2"},
		PluginDirs:                  []string{"p1"},
		MCPConfigs:                  []string{"m1.json"},
		ModelFallbacks:              []string{"f1", "f2"},
		Env:                         map[string]string{"B": "2", "A": "1"},
		Verbose:                     boolPtr(false),
		RawHTTPLogging:              boolPtr(true),
		TraceFile:                   "/t",
		CPUProfile:                  "/c",
		ExportATIFPath:              "/e",
		ExportATIFProviderHandles:   "handles",
	}

	tests := []struct {
		field         string
		wantValue     string
		wantEditValue string
	}{
		{"agent", "myagent", "myagent"},
		{"model", "gpt-5", "gpt-5"},
		{"reasoning_effort", "high", "high"},
		{"fast_cheap_model", "mini", "mini"},
		{"context_strategy", "trim", "trim"},
		{"openai_responses_continuation", "on", "on"},
		{"max_rounds", "10", "10"},
		{"max_subagent_depth", "3", "3"},
		{"no_project_prompts", "true", "true"},
		{"app_replay_size", "5", "5"},
		{"system_prompt_mode", "custom", "custom"},
		{"system_prompt_file", "/p", "/p"},
		{"system_prompt_text", "11 chars, 2 lines", "line1\nline2"},
		{"system_prompt_append_mode", "append", "append"},
		{"system_prompt_append_file", "/a", "/a"},
		{"system_prompt_append_text", "8 chars, 3 lines", "a1\na2\na3"},
		{"skills_dirs", "2 entries", "s1, s2"},
		{"plugin_dirs", "1 entries", "p1"},
		{"mcp_configs", "1 entries", "m1.json"},
		{"model_fallbacks", "2 entries", "f1, f2"},
		{"env", "2 entries", "A=1, B=2"},
		{"verbose", "false", "false"},
		{"raw_http_logging", "true", "true"},
		{"trace_file", "/t", "/t"},
		{"cpu_profile", "/c", "/c"},
		{"export_atif_path", "/e", "/e"},
		{"export_atif_provider_handles", "handles", "handles"},
		{"unknown_field", "(unsupported)", ""},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			value, editValue := launchOptionValue(appwire.LaunchOption{Field: tc.field}, layer)
			if value != tc.wantValue {
				t.Errorf("value = %q, want %q", value, tc.wantValue)
			}
			if editValue != tc.wantEditValue {
				t.Errorf("editValue = %q, want %q", editValue, tc.wantEditValue)
			}
		})
	}
}

func TestLaunchOptionValue_Defaults(t *testing.T) {
	empty := appwire.LaunchConfigLayer{}
	for _, field := range []string{"agent", "max_rounds", "no_project_prompts", "verbose", "system_prompt_text"} {
		value, _ := launchOptionValue(appwire.LaunchOption{Field: field}, empty)
		if value != "(default)" {
			t.Errorf("field %q value = %q, want (default)", field, value)
		}
	}
}

func TestLaunchOptionValue_ModelFallbacksVariants(t *testing.T) {
	// nil → unset default; empty non-nil → explicit empty; populated → count.
	nilLayer := appwire.LaunchConfigLayer{ModelFallbacks: nil}
	if v, e := launchOptionValue(appwire.LaunchOption{Field: "model_fallbacks"}, nilLayer); v != "(default)" || e != "(default)" {
		t.Fatalf("nil fallbacks = (%q,%q), want (default),(default)", v, e)
	}
	emptyLayer := appwire.LaunchConfigLayer{ModelFallbacks: []string{}}
	if v, e := launchOptionValue(appwire.LaunchOption{Field: "model_fallbacks"}, emptyLayer); v != "0 entries (explicit)" || e != "[]" {
		t.Fatalf("empty fallbacks = (%q,%q), want explicit,[]", v, e)
	}
}

func TestMultilineSummary(t *testing.T) {
	if got := multilineSummary("   "); got != "(default)" {
		t.Fatalf("blank = %q, want (default)", got)
	}
	if got := multilineSummary("one line"); got != "8 chars, 1 lines" {
		t.Fatalf("single = %q, want 8 chars, 1 lines", got)
	}
}

func TestEnvEditValue(t *testing.T) {
	if got := envEditValue(nil); got != "" {
		t.Fatalf("nil env = %q, want empty", got)
	}
	got := envEditValue(map[string]string{"Z": "26", "A": "1"})
	if got != "A=1, Z=26" {
		t.Fatalf("envEditValue = %q, want sorted A=1, Z=26", got)
	}
}

func TestLaunchOptionUsesPathCompletion(t *testing.T) {
	tests := []struct {
		name string
		opt  appwire.LaunchOption
		want bool
	}{
		{"path with kind", appwire.LaunchOption{Kind: "path", PathKind: "file"}, true},
		{"pathList with kind", appwire.LaunchOption{Kind: "pathList", PathKind: "dir"}, true},
		{"path without kind", appwire.LaunchOption{Kind: "path"}, false},
		{"non-path kind", appwire.LaunchOption{Kind: "string", PathKind: "file"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchOptionUsesPathCompletion(tc.opt); got != tc.want {
				t.Fatalf("launchOptionUsesPathCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLaunchSettingsFieldUsesPathCompletion(t *testing.T) {
	for _, field := range []string{"skills_dirs", "plugin_dirs", "mcp_configs", "system_prompt_file", "system_prompt_append_file", "trace_file", "cpu_profile", "export_atif_path"} {
		if !LaunchSettingsFieldUsesPathCompletion(field) {
			t.Errorf("field %q should use path completion", field)
		}
	}
	for _, field := range []string{"model", "agent", "", "env"} {
		if LaunchSettingsFieldUsesPathCompletion(field) {
			t.Errorf("field %q should not use path completion", field)
		}
	}
}

func TestLaunchOptionDefaultableInLayer(t *testing.T) {
	opt := appwire.LaunchOption{DefaultableLayers: []string{"global", "project"}}
	if !launchOptionDefaultableInLayer(opt, "project") {
		t.Fatal("project should be defaultable")
	}
	if launchOptionDefaultableInLayer(opt, "launch") {
		t.Fatal("launch should not be defaultable")
	}
}
