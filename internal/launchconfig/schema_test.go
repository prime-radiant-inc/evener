package launchconfig

import "testing"

func TestLaunchOptionSchema_FieldCoverage(t *testing.T) {
	got := map[string]bool{}
	for _, opt := range LaunchOptionSchema() {
		got[opt.Field] = true
	}
	want := []string{
		"agent", "model", "reasoning_effort", "fast_cheap_model",
		"context_strategy", "max_rounds", "max_subagent_depth",
		"no_project_prompts", "app_replay_size",
		"system_prompt_mode", "system_prompt_file", "system_prompt_text",
		"system_prompt_append_mode", "system_prompt_append_file", "system_prompt_append_text",
		"skills_dirs", "plugin_dirs", "mcp_configs", "mcps",
		"model_fallbacks", "env",
		"verbose", "trace_file", "cpu_profile", "export_atif_path",
	}
	for _, field := range want {
		if !got[field] {
			t.Fatalf("schema missing field %q", field)
		}
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

func indexOption(opts []LaunchOption, field string) int {
	for i, opt := range opts {
		if opt.Field == field {
			return i
		}
	}
	return -1
}
