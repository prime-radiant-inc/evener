package launchconfig

import (
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
)

func checkLaunchOptionSchema_FieldCoverage(t *testing.T) {
	got := map[string]bool{}
	for _, opt := range LaunchOptionSchema() {
		got[opt.Field] = true
	}
	want := []string{
		"agent", "model", "reasoning_effort", "fast_cheap_model",
		"context_strategy", "openai_responses_continuation", "max_rounds", "max_subagent_depth",
		"max_concurrent_delegate_turns", "max_retained_terminal",
		"no_project_prompts", "non_interactive", "app_replay_size",
		"system_prompt_mode", "system_prompt_file", "system_prompt_text",
		"system_prompt_append_mode", "system_prompt_append_file", "system_prompt_append_text",
		"skills_dirs", "plugin_dirs", "mcp_configs", "mcps",
		"model_fallbacks", "env",
		"sandbox", "sandbox_net",
		"verbose", "trace_file", "cpu_profile", "export_atif_path", "export_atif_provider_handles",
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

func checkLaunchOptionSchema_OpenAIResponsesContinuation(t *testing.T) {
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

func checkLaunchOptionSchema_ExportATIFProviderHandles(t *testing.T) {
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

func checkLaunchOptionSchema_Sandbox(t *testing.T) {
	opts := LaunchOptionSchema()

	sb := indexOption(opts, "sandbox")
	if sb < 0 {
		t.Fatal("schema missing sandbox")
	}
	if opts[sb].WireField != "sandbox" {
		t.Errorf("sandbox WireField = %q, want sandbox", opts[sb].WireField)
	}
	if opts[sb].Group != LaunchGroupSandbox {
		t.Errorf("sandbox Group = %q, want %q", opts[sb].Group, LaunchGroupSandbox)
	}
	if opts[sb].Kind != LaunchControlSelect {
		t.Errorf("sandbox Kind = %q, want %q", opts[sb].Kind, LaunchControlSelect)
	}
	if !opts[sb].PerLaunch || opts[sb].DebugOnly {
		t.Errorf("sandbox PerLaunch/DebugOnly = %v/%v, want true/false", opts[sb].PerLaunch, opts[sb].DebugOnly)
	}
	wantChoices := []string{"", "off", "read-only", "workspace-write", "restricted"}
	if len(opts[sb].Choices) != len(wantChoices) {
		t.Fatalf("sandbox Choices = %+v, want values %v", opts[sb].Choices, wantChoices)
	}
	for i, want := range wantChoices {
		if opts[sb].Choices[i].Value != want {
			t.Fatalf("sandbox Choices = %+v, want values %v", opts[sb].Choices, wantChoices)
		}
	}

	// The description must be accurate per policy.go mode semantics: read-only and
	// workspace-write read anywhere-minus-denylist (NOT worktree-only), and read-only
	// denies ALL worktree writes. The old universal "reads outside the sandbox are
	// denied and writes are confined to the working tree" is false and must not
	// return.
	desc := opts[sb].Description
	if strings.Contains(desc, "reads outside the sandbox are denied") {
		t.Errorf("sandbox description repeats the false universal read claim: %q", desc)
	}
	if strings.Contains(desc, "never escape") {
		t.Errorf("sandbox description repeats the false 'writes never escape the working tree' claim (temp is outside): %q", desc)
	}
	for _, want := range []string{"no writes", "working tree", "credential", "shell", "separate toggle"} {
		if !strings.Contains(desc, want) {
			t.Errorf("sandbox description must accurately gloss the modes (missing %q), got %q", want, desc)
		}
	}

	// The empty choice is inherit, not "default: off" (false at project/launch).
	if opts[sb].Choices[0].Value != "" || opts[sb].Choices[0].Label != "(inherit)" {
		t.Errorf("first sandbox choice must be inherit, got %+v", opts[sb].Choices[0])
	}

	sn := indexOption(opts, "sandbox_net")
	if sn < 0 {
		t.Fatal("schema missing sandbox_net")
	}
	if opts[sn].WireField != "sandboxNet" {
		t.Errorf("sandbox_net WireField = %q, want sandboxNet", opts[sn].WireField)
	}
	if opts[sn].Group != LaunchGroupSandbox {
		t.Errorf("sandbox_net Group = %q, want %q", opts[sn].Group, LaunchGroupSandbox)
	}
	if opts[sn].Kind != LaunchControlBoolean {
		t.Errorf("sandbox_net Kind = %q, want %q", opts[sn].Kind, LaunchControlBoolean)
	}
}

func checkLaunchOptionSchema_OmitsObsoleteRawHTTPLogging(t *testing.T) {
	if indexOption(LaunchOptionSchema(), "raw_http_logging") >= 0 {
		t.Fatal("launch schema still exposes the obsolete raw HTTP logging control")
	}
}

func checkLaunchOptionSchema_GroupOrder(t *testing.T) {
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

func checkLaunchOptionSchema_ExclusionsAreExplicit(t *testing.T) {
	excluded := LaunchOptionExclusions()
	for _, flag := range []string{"addr", "run_dir", "resume", "resume_last", "state_dir", "system_prompt_as_user", "output_schema", "result_tool_name", "share_task_store"} {
		if excluded[flag] == "" {
			t.Fatalf("missing exclusion reason for %q", flag)
		}
	}
}

func checkContextChoices_NoRecall(t *testing.T) {
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
