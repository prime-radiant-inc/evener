package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestToArgs_AllFields(t *testing.T) {
	r := Resolved{Effective: Layer{
		Model:                       "openai/gpt-5",
		FastCheapModel:              "openai/gpt-5-mini",
		Agent:                       "default",
		ReasoningEffort:             "medium",
		ContextStrategy:             "compact",
		OpenAIResponsesContinuation: "auto",
		MaxRounds:                   ptrInt(200),
		MaxSubagentDepth:            ptrInt(2),
		NoProjectPrompts:            ptrBool(true),
		NonInteractive:              ptrBool(true),
		AppReplaySize:               ptrInt(4096),
		SystemPromptMode:            "file",
		SystemPromptFile:            "/system.md",
		SystemPromptAppendMode:      "file",
		SystemPromptAppendFile:      "/append.md",
		Verbose:                     ptrBool(true),
		TraceFile:                   "/tmp/trace.out",
		CPUProfile:                  "/tmp/cpu.pprof",
		ExportATIFPath:              "/tmp/session.atif.json",
		ExportATIFProviderHandles:   "raw-local",
		SkillsDirs:                  []string{"/s1", "/s2"},
		PluginDirs:                  []string{"/p"},
		MCPConfigs:                  []string{"/m.json"},
		SystemPromptAppend:          []string{"/sp"},
		ModelFallbacks:              []string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"},
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
		"--openai-responses-continuation", "auto",
		"--max-rounds", "200",
		"--max-subagent-depth", "2",
		"--no-project-prompts",
		"--non-interactive",
		"--app-replay-size", "4096",
		"--system-prompt", "/system.md",
		"--system-prompt-append", "/append.md",
		"--verbose",
		"--trace", "/tmp/trace.out",
		"--cpu-profile", "/tmp/cpu.pprof",
		"--export-atif", "/tmp/session.atif.json",
		"--export-atif-provider-handles", "raw-local",
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

func TestToArgs_InlinePromptTextDoesNotEmitArgv(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{
		SystemPromptMode:       "inline",
		SystemPromptText:       "do not leak me",
		SystemPromptAppendMode: "inline",
		SystemPromptAppendText: "also secret-ish",
	}})
	for _, arg := range got {
		if strings.Contains(arg, "do not leak") || strings.Contains(arg, "also secret") {
			t.Fatalf("ToArgs leaked inline prompt text in argv: %#v", got)
		}
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
	got := ToArgs(Resolved{Effective: Layer{NoProjectPrompts: ptrBool(false), NonInteractive: ptrBool(false)}})
	for _, a := range got {
		if a == "--no-project-prompts" {
			t.Errorf("ToArgs should not emit --no-project-prompts when value is false; got %v", got)
		}
		if a == "--non-interactive" {
			t.Errorf("ToArgs should not emit --non-interactive when value is false; got %v", got)
		}
	}
}
