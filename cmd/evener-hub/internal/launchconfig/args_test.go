package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

func checkToArgs_AllFields(t *testing.T) {
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
		ModelFallbacks:              &[]string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"},
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

func checkToArgs_InlinePromptTextDoesNotEmitArgv(t *testing.T) {
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

func checkToArgs_SkipsUnset(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{Model: "openai/gpt-5"}})
	want := []string{"--model", "openai/gpt-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs = %v, want %v", got, want)
	}
}

// TestToArgs_Sandbox: a launch-config sandbox choice must reach the spawned
// `serf serve`. An explicit mode emits `--sandbox <mode>` (including off, so a
// launch layer can override a global default back to off); an unset mode emits
// nothing. sandbox_net is a tri-state: true/false emit `--sandbox-net on|off`,
// nil emits nothing.
func checkToArgs_Sandbox(t *testing.T) {
	cases := []struct {
		name  string
		layer Layer
		want  []string
	}{
		{"unset", Layer{}, nil},
		{"restricted", Layer{Sandbox: "restricted"}, []string{"--sandbox", "restricted"}},
		{"explicit off", Layer{Sandbox: "off"}, []string{"--sandbox", "off"}},
		// sandbox_net without a non-off mode is suppressed: serf ignores the flag
		// without a sandbox, so passing it alone would be a silent no-op.
		{"net on, no mode", Layer{SandboxNet: ptrBool(true)}, nil},
		{"net off, no mode", Layer{SandboxNet: ptrBool(false)}, nil},
		{"net with off mode", Layer{Sandbox: "off", SandboxNet: ptrBool(false)}, []string{"--sandbox", "off"}},
		{"mode and net", Layer{Sandbox: "workspace-write", SandboxNet: ptrBool(false)}, []string{"--sandbox", "workspace-write", "--sandbox-net", "off"}},
		{"restricted and net on", Layer{Sandbox: "restricted", SandboxNet: ptrBool(true)}, []string{"--sandbox", "restricted", "--sandbox-net", "on"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToArgs(Resolved{Effective: tc.layer})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ToArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func checkToArgs_BoolFalseDoesNotEmitFlag(t *testing.T) {
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
