package launchconfig

import (
	"testing"

	"primeradiant.com/serf/envvars"
)

func TestLaunchOptionSchema_FieldCoverage(t *testing.T) {
	got := map[string]bool{}
	for _, opt := range LaunchOptionSchema() {
		got[opt.Field] = true
	}
	want := []string{
		"agent", "model", "reasoning_effort", "fast_cheap_model",
		"context_strategy", "openai_responses_continuation", "max_rounds", "max_subagent_depth",
		"no_project_prompts", "non_interactive", "app_replay_size",
		"system_prompt_mode", "system_prompt_file", "system_prompt_text",
		"system_prompt_append_mode", "system_prompt_append_file", "system_prompt_append_text",
		"skills_dirs", "plugin_dirs", "mcp_configs", "mcps",
		"model_fallbacks", "env",
		"verbose", "raw_http_logging", "trace_file", "cpu_profile", "export_atif_path", "export_atif_provider_handles",
	}
	wantSet := map[string]bool{}
	for _, field := range want {
		wantSet[field] = true
		if !got[field] {
			t.Fatalf("schema missing field %q", field)
		}
	}
	// Bidirectional: every schema field must appear in want so that new fields
	// are not silently skipped by this coverage check.
	for field := range got {
		if !wantSet[field] {
			t.Errorf("schema has unexpected field %q — add it to the want list or LaunchOptionExclusions", field)
		}
	}
	if len(got) != len(wantSet) {
		t.Errorf("schema has %d unique fields, want %d", len(got), len(wantSet))
	}
}

func TestLaunchOptionSchema_OpenAIResponsesContinuation(t *testing.T) {
	opts := LaunchOptionSchema()
	idx := indexOption(opts, "openai_responses_continuation")
	if idx < 0 {
		t.Fatal("schema missing openai_responses_continuation")
	}
	opt := opts[idx]
	if opt.WireField != "openAIResponsesContinuation" {
		t.Fatalf("WireField = %q, want openAIResponsesContinuation", opt.WireField)
	}
	if opt.Group != LaunchGroupLimits {
		t.Fatalf("Group = %q, want %q", opt.Group, LaunchGroupLimits)
	}
	if opt.Kind != LaunchControlSelect {
		t.Fatalf("Kind = %q, want %q", opt.Kind, LaunchControlSelect)
	}
	if !opt.PerLaunch || opt.DebugOnly {
		t.Fatalf("PerLaunch/DebugOnly = %v/%v, want true/false", opt.PerLaunch, opt.DebugOnly)
	}
	wantChoices := []string{"", "off", "auto"}
	if len(opt.Choices) != len(wantChoices) {
		t.Fatalf("Choices = %+v, want values %v", opt.Choices, wantChoices)
	}
	for i, want := range wantChoices {
		if opt.Choices[i].Value != want {
			t.Fatalf("Choices = %+v, want values %v", opt.Choices, wantChoices)
		}
	}
	if opt.EnvFallback == nil || opt.EnvFallback.Name != envvars.SERFOpenAIResponsesContinuation.Name || opt.EnvFallback.Secret {
		t.Fatalf("EnvFallback = %+v, want public %s", opt.EnvFallback, envvars.SERFOpenAIResponsesContinuation.Name)
	}
}

func TestLaunchOptionSchema_ExportATIFProviderHandles(t *testing.T) {
	opts := LaunchOptionSchema()
	idx := indexOption(opts, "export_atif_provider_handles")
	if idx < 0 {
		t.Fatal("schema missing export_atif_provider_handles")
	}
	opt := opts[idx]
	if opt.WireField != "exportATIFProviderHandles" {
		t.Fatalf("WireField = %q, want exportATIFProviderHandles", opt.WireField)
	}
	if opt.Group != LaunchGroupDebugLogging {
		t.Fatalf("Group = %q, want %q", opt.Group, LaunchGroupDebugLogging)
	}
	if opt.Kind != LaunchControlSelect {
		t.Fatalf("Kind = %q, want %q", opt.Kind, LaunchControlSelect)
	}
	if !opt.PerLaunch || !opt.DebugOnly {
		t.Fatalf("PerLaunch/DebugOnly = %v/%v, want true/true", opt.PerLaunch, opt.DebugOnly)
	}
	wantChoices := []string{"", "redacted", "raw-local"}
	if len(opt.Choices) != len(wantChoices) {
		t.Fatalf("Choices = %+v, want values %v", opt.Choices, wantChoices)
	}
	for i, want := range wantChoices {
		if opt.Choices[i].Value != want {
			t.Fatalf("Choices = %+v, want values %v", opt.Choices, wantChoices)
		}
	}
}

func TestLaunchOptionSchema_RawHTTPLoggingIsDebugLaunchSetting(t *testing.T) {
	opts := LaunchOptionSchema()
	idx := indexOption(opts, "raw_http_logging")
	if idx < 0 {
		t.Fatal("schema missing raw_http_logging")
	}
	opt := opts[idx]
	if opt.WireField != "rawHTTPLogging" {
		t.Fatalf("WireField = %q, want rawHTTPLogging", opt.WireField)
	}
	if opt.Group != LaunchGroupDebugLogging {
		t.Fatalf("Group = %q, want %q", opt.Group, LaunchGroupDebugLogging)
	}
	if opt.Kind != LaunchControlBoolean {
		t.Fatalf("Kind = %q, want %q", opt.Kind, LaunchControlBoolean)
	}
	if !opt.PerLaunch || !opt.DebugOnly {
		t.Fatalf("PerLaunch/DebugOnly = %v/%v, want true/true", opt.PerLaunch, opt.DebugOnly)
	}
	wantLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject, LaunchLayerLaunch}
	if len(opt.DefaultableLayers) != len(wantLayers) {
		t.Fatalf("DefaultableLayers = %v, want %v", opt.DefaultableLayers, wantLayers)
	}
	for i := range wantLayers {
		if opt.DefaultableLayers[i] != wantLayers[i] {
			t.Fatalf("DefaultableLayers = %v, want %v", opt.DefaultableLayers, wantLayers)
		}
	}
	if opt.EnvFallback == nil || opt.EnvFallback.Name != envvars.SERFLogRawHTTP.Name || opt.EnvFallback.Secret {
		t.Fatalf("EnvFallback = %+v, want public %s", opt.EnvFallback, envvars.SERFLogRawHTTP.Name)
	}
}

func TestLaunchOptionSchema_GroupOrder(t *testing.T) {
	opts := LaunchOptionSchema()
	if len(opts) == 0 {
		t.Fatal("empty schema")
	}
	if opts[0].Group != LaunchGroupAgent || opts[0].Field != "agent" {
		t.Fatalf("first option = %s/%s, want Agent/agent", opts[0].Group, opts[0].Field)
	}
	modelIndex := indexOption(opts, "model")
	reasoningIndex := indexOption(opts, "reasoning_effort")
	fastIndex := indexOption(opts, "fast_cheap_model")
	if modelIndex < 0 || reasoningIndex < 0 || fastIndex < 0 {
		t.Fatalf("missing model group fields")
	}
	if opts[reasoningIndex].Group != LaunchGroupModel {
		t.Fatalf("reasoning_effort group = %q, want %q", opts[reasoningIndex].Group, LaunchGroupModel)
	}
	if reasoningIndex > fastIndex {
		t.Fatalf("reasoning_effort should appear with primary model before fast_cheap_model")
	}
}

func TestLaunchOptionSchema_ExclusionsAreExplicit(t *testing.T) {
	excluded := LaunchOptionExclusions()
	for _, flag := range []string{"addr", "run_dir", "resume", "resume_last", "state_dir", "system_prompt_as_user", "output_schema", "result_tool_name", "share_task_store"} {
		if excluded[flag] == "" {
			t.Fatalf("missing exclusion reason for %q", flag)
		}
	}
}

func TestContextChoices_NoRecall(t *testing.T) {
	choices := contextChoices()
	wantValues := map[string]bool{"": true, "compact": true, "session-log": true, "ooda": true}
	for _, c := range choices {
		if c.Value == "recall" {
			t.Errorf("contextChoices() must not contain recall (it is a compat alias, not an advertised strategy)")
		}
		if !wantValues[c.Value] {
			t.Errorf("contextChoices() has unexpected value %q", c.Value)
		}
	}
	for want := range wantValues {
		found := false
		for _, c := range choices {
			if c.Value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("contextChoices() missing expected value %q", want)
		}
	}
}

func indexOption(opts []LaunchOption, field string) int {
	for i, opt := range opts {
		if opt.Field == field {
			return i
		}
	}
	return -1
}
