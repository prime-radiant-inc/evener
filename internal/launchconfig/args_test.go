package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestToArgs_AllFields(t *testing.T) {
	r := Resolved{Effective: Layer{
		Model:              "openai/gpt-5",
		FastCheapModel:     "openai/gpt-5-mini",
		Agent:              "default",
		ReasoningEffort:    "medium",
		ContextStrategy:    "compact",
		MaxRounds:          ptrInt(200),
		MaxSubagentDepth:   ptrInt(2),
		NoProjectPrompts:   ptrBool(true),
		AppReplaySize:      ptrInt(4096),
		SkillsDirs:         []string{"/s1", "/s2"},
		PluginDirs:         []string{"/p"},
		MCPConfigs:         []string{"/m.json"},
		SystemPromptAppend: []string{"/sp"},
		ModelFallbacks:     []string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"},
		MCPs: []MCPServerSpec{
			{Name: "github", Command: "gh-mcp", Args: []string{"--token-from-env", "GITHUB_TOKEN"}},
		},
	}}
	got := ToArgs(r)
	want := []string{
		"--model", "openai/gpt-5",
		"--fast-cheap-model", "openai/gpt-5-mini",
		"--agent", "default",
		"--reasoning-effort", "medium",
		"--context-strategy", "compact",
		"--max-rounds", "200",
		"--max-subagent-depth", "2",
		"--no-project-prompts",
		"--app-replay-size", "4096",
		"--skills-dir", "/s1",
		"--skills-dir", "/s2",
		"--plugin-dir", "/p",
		"--mcp-config", "/m.json",
		"--system-prompt-append", "/sp",
		"--model-fallback", "openai/gpt-5.4",
		"--model-fallback", "anthropic/claude-haiku-4-5",
		"--mcp", "github:gh-mcp --token-from-env GITHUB_TOKEN",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs =\n%s\nwant\n%s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestToArgs_SkipsUnset(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{Model: "openai/gpt-5"}})
	want := []string{"--model", "openai/gpt-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs = %v, want %v", got, want)
	}
}

func TestToArgs_BoolFalseDoesNotEmitFlag(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{NoProjectPrompts: ptrBool(false)}})
	for _, a := range got {
		if a == "--no-project-prompts" {
			t.Errorf("ToArgs should not emit --no-project-prompts when value is false; got %v", got)
		}
	}
}
