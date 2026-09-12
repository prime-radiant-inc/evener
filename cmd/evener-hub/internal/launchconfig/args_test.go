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
		MaxRounds:                   new(200),
		MaxSubagentDepth:            new(2),
		NoProjectPrompts:            new(true),
		NonInteractive:              new(true),
		AppReplaySize:               new(4096),
		SystemPromptMode:            "file",
		SystemPromptFile:            "/system.md",
		SystemPromptAppendMode:      "file",
		SystemPromptAppendFile:      "/append.md",
		Verbose:                     new(true),
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
		"--daemon-idle-timeout", "0s",
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
	want := []string{"--model", "openai/gpt-5", "--daemon-idle-timeout", "0s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs = %v, want %v", got, want)
	}
}

func TestToArgsEnabledPluginsPresence(t *testing.T) {
	// --daemon-idle-timeout is the one unconditional pair (zero renders "0s"
	// explicitly); everything else is still omitted when unset.
	if got := ToArgs(Resolved{Effective: Layer{}}); !reflect.DeepEqual(got, []string{"--daemon-idle-timeout", "0s"}) {
		t.Fatalf("unset args = %v, want only the unconditional daemon-idle-timeout pair", got)
	}
	empty := []string{}
	if got := ToArgs(Resolved{Effective: Layer{EnabledPlugins: &empty}}); !reflect.DeepEqual(got, []string{"--enabled-plugins=", "--daemon-idle-timeout", "0s"}) {
		t.Fatalf("empty args = %v", got)
	}
	names := []string{"alpha", "beta"}
	if got := ToArgs(Resolved{Effective: Layer{EnabledPlugins: &names}}); !reflect.DeepEqual(got, []string{"--enabled-plugins=alpha,beta", "--daemon-idle-timeout", "0s"}) {
		t.Fatalf("named args = %v", got)
	}
}

// TestToArgs_Sandbox: a launch-config sandbox choice must reach the spawned
// `evener serve`. An explicit mode emits `--sandbox <mode>` (including off, so a
// launch layer can override a global default back to off); an unset mode emits
// nothing. sandbox_net is a tri-state: true/false emit `--sandbox-net on|off`,
// nil emits nothing.
func checkToArgs_Sandbox(t *testing.T) {
	cases := []struct {
		name  string
		layer Layer
		want  []string
	}{
		{"unset", Layer{}, []string{"--daemon-idle-timeout", "0s"}},
		{"restricted", Layer{Sandbox: "restricted"}, []string{"--sandbox", "restricted", "--daemon-idle-timeout", "0s"}},
		{"explicit off", Layer{Sandbox: "off"}, []string{"--sandbox", "off", "--daemon-idle-timeout", "0s"}},
		// sandbox_net without a non-off mode is suppressed: evener ignores the flag
		// without a sandbox, so passing it alone would be a silent no-op.
		{"net on, no mode", Layer{SandboxNet: new(true)}, []string{"--daemon-idle-timeout", "0s"}},
		{"net off, no mode", Layer{SandboxNet: new(false)}, []string{"--daemon-idle-timeout", "0s"}},
		{"net with off mode", Layer{Sandbox: "off", SandboxNet: new(false)}, []string{"--sandbox", "off", "--daemon-idle-timeout", "0s"}},
		{"mode and net", Layer{Sandbox: "workspace-write", SandboxNet: new(false)}, []string{"--sandbox", "workspace-write", "--sandbox-net", "off", "--daemon-idle-timeout", "0s"}},
		{"restricted and net on", Layer{Sandbox: "restricted", SandboxNet: new(true)}, []string{"--sandbox", "restricted", "--sandbox-net", "on", "--daemon-idle-timeout", "0s"}},
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
	got := ToArgs(Resolved{Effective: Layer{NoProjectPrompts: new(false), NonInteractive: new(false)}})
	for _, a := range got {
		if a == "--no-project-prompts" {
			t.Errorf("ToArgs should not emit --no-project-prompts when value is false; got %v", got)
		}
		if a == "--non-interactive" {
			t.Errorf("ToArgs should not emit --non-interactive when value is false; got %v", got)
		}
	}
}
