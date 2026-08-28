package launchconfig

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// --- launchSchemaRows: empty schema ---

func TestCovLaunchSchemaRowsEmpty(t *testing.T) {
	rows := launchSchemaRows(nil, appwire.LaunchConfigLayer{}, "global", launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if rows != nil {
		t.Fatalf("empty schema should return nil, got %v", rows)
	}
}

// --- layerRowForOption: label fallback to field ---

func TestCovLayerRowForOptionLabelFallback(t *testing.T) {
	opt := appwire.LaunchOption{Field: "my_field", Label: "", Kind: "text"}
	row := layerRowForOption(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if row.label != "my_field" {
		t.Fatalf("label fallback = %q, want my_field", row.label)
	}
}

func TestCovLayerRowForOptionLabelUsed(t *testing.T) {
	opt := appwire.LaunchOption{Field: "my_field", Label: "My Field", Kind: "text"}
	row := layerRowForOption(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if row.label != "My Field" {
		t.Fatalf("label = %q, want My Field", row.label)
	}
}

// --- launchOptionValue: unsupported field ---

func TestCovLaunchOptionValueUnsupported(t *testing.T) {
	opt := appwire.LaunchOption{Field: "totally_unknown", Kind: "text"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if val != "(unsupported)" {
		t.Fatalf("unsupported value = %q, want (unsupported)", val)
	}
	if editVal != "" {
		t.Fatalf("unsupported editValue = %q, want empty", editVal)
	}
}

// --- launchOptionValue: string fields with values ---

func TestCovLaunchOptionValueAgent(t *testing.T) {
	opt := appwire.LaunchOption{Field: "agent", Kind: "text"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{Agent: "evener"}, appwire.LaunchConfigLayer{})
	if val != "evener" || editVal != "evener" {
		t.Fatalf("agent = %q/%q", val, editVal)
	}
}

func TestCovLaunchOptionValueModel(t *testing.T) {
	opt := appwire.LaunchOption{Field: "model", Kind: "modelPicker"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{Model: "gpt-5"}, appwire.LaunchConfigLayer{})
	if val != "gpt-5" || editVal != "gpt-5" {
		t.Fatalf("model = %q/%q", val, editVal)
	}
}

func TestCovLaunchOptionValueSandbox(t *testing.T) {
	opt := appwire.LaunchOption{Field: "sandbox", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{Sandbox: "read-only"}, appwire.LaunchConfigLayer{})
	if val != "read-only" {
		t.Fatalf("sandbox = %q, want read-only", val)
	}
}

func TestCovLaunchOptionValueReasoningEffort(t *testing.T) {
	opt := appwire.LaunchOption{Field: "reasoning_effort", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{ReasoningEffort: "high"}, appwire.LaunchConfigLayer{})
	if val != "high" {
		t.Fatalf("reasoning_effort = %q, want high", val)
	}
}

func TestCovLaunchOptionValueFastCheapModel(t *testing.T) {
	opt := appwire.LaunchOption{Field: "fast_cheap_model", Kind: "modelPicker"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{FastCheapModel: "mini"}, appwire.LaunchConfigLayer{})
	if val != "mini" {
		t.Fatalf("fast_cheap_model = %q, want mini", val)
	}
}

func TestCovLaunchOptionValueContextStrategy(t *testing.T) {
	opt := appwire.LaunchOption{Field: "context_strategy", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{ContextStrategy: "compact"}, appwire.LaunchConfigLayer{})
	if val != "compact" {
		t.Fatalf("context_strategy = %q, want compact", val)
	}
}

func TestCovLaunchOptionValueOpenAIResponsesContinuation(t *testing.T) {
	opt := appwire.LaunchOption{Field: "openai_responses_continuation", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{OpenAIResponsesContinuation: "auto"}, appwire.LaunchConfigLayer{})
	if val != "auto" {
		t.Fatalf("openai_responses_continuation = %q, want auto", val)
	}
}

func TestCovLaunchOptionValueSystemPromptMode(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_mode", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptMode: "custom"}, appwire.LaunchConfigLayer{})
	if val != "custom" {
		t.Fatalf("system_prompt_mode = %q, want custom", val)
	}
}

func TestCovLaunchOptionValueSystemPromptFile(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_file", Kind: "path"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptFile: "/path/to/file"}, appwire.LaunchConfigLayer{})
	if val != "/path/to/file" {
		t.Fatalf("system_prompt_file = %q, want /path/to/file", val)
	}
}

func TestCovLaunchOptionValueSystemPromptText(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_text", Kind: "text"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptText: "hello\nworld"}, appwire.LaunchConfigLayer{})
	if val != "11 chars, 2 lines" || editVal != "hello\nworld" {
		t.Fatalf("system_prompt_text = %q/%q, want exact summary and edit text", val, editVal)
	}
}

func TestCovLaunchOptionValueSystemPromptAppendMode(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_append_mode", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptAppendMode: "append"}, appwire.LaunchConfigLayer{})
	if val != "append" {
		t.Fatalf("system_prompt_append_mode = %q, want append", val)
	}
}

func TestCovLaunchOptionValueSystemPromptAppendFile(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_append_file", Kind: "path"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptAppendFile: "/append"}, appwire.LaunchConfigLayer{})
	if val != "/append" {
		t.Fatalf("system_prompt_append_file = %q, want /append", val)
	}
}

func TestCovLaunchOptionValueSystemPromptAppendText(t *testing.T) {
	opt := appwire.LaunchOption{Field: "system_prompt_append_text", Kind: "text"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{SystemPromptAppendText: "append"}, appwire.LaunchConfigLayer{})
	if val != "6 chars, 1 lines" || editVal != "append" {
		t.Fatalf("system_prompt_append_text = %q/%q, want exact summary and edit text", val, editVal)
	}
}

func TestCovLaunchOptionValueSkillsDirs(t *testing.T) {
	opt := appwire.LaunchOption{Field: "skills_dirs", Kind: "pathList"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{SkillsDirs: []string{"/a", "/b"}}, appwire.LaunchConfigLayer{})
	if val != "2 entries" || editVal != "/a, /b" {
		t.Fatalf("skills_dirs = %q/%q", val, editVal)
	}
}

func TestCovLaunchOptionValuePluginDirs(t *testing.T) {
	opt := appwire.LaunchOption{Field: "plugin_dirs", Kind: "pathList"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{PluginDirs: []string{"/p"}}, appwire.LaunchConfigLayer{})
	if val != "1 entries" {
		t.Fatalf("plugin_dirs = %q, want 1 entries", val)
	}
}

func TestCovLaunchOptionValueMCPConfigs(t *testing.T) {
	opt := appwire.LaunchOption{Field: "mcp_configs", Kind: "pathList"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{MCPConfigs: []string{"/c"}}, appwire.LaunchConfigLayer{})
	if val != "1 entries" {
		t.Fatalf("mcp_configs = %q, want 1 entries", val)
	}
}

func TestCovLaunchOptionValueMCPServers(t *testing.T) {
	opt := appwire.LaunchOption{Field: "mcps", Kind: "mcpServerList"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{MCPs: []appwire.MCPServerSpec{{Name: "x", Command: "c"}}}, appwire.LaunchConfigLayer{})
	if val != "1 entries" {
		t.Fatalf("mcps = %q, want 1 entries", val)
	}
}

func TestCovLaunchOptionValueEnv(t *testing.T) {
	opt := appwire.LaunchOption{Field: "env", Kind: "envMap"}
	val, editVal := launchOptionValue(opt, appwire.LaunchConfigLayer{Env: map[string]string{"KEY": "val"}}, appwire.LaunchConfigLayer{})
	if val != "1 entries" || editVal != "KEY=val" {
		t.Fatalf("env = %q/%q", val, editVal)
	}
}

func TestCovLaunchOptionValueVerbose(t *testing.T) {
	opt := appwire.LaunchOption{Field: "verbose", Kind: "boolean"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if val != "(default)" {
		t.Fatalf("nil verbose = %q, want (default)", val)
	}
}

func TestCovLaunchOptionValueTraceFile(t *testing.T) {
	opt := appwire.LaunchOption{Field: "trace_file", Kind: "path"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{TraceFile: "/trace"}, appwire.LaunchConfigLayer{})
	if val != "/trace" {
		t.Fatalf("trace_file = %q, want /trace", val)
	}
}

func TestCovLaunchOptionValueCPUProfile(t *testing.T) {
	opt := appwire.LaunchOption{Field: "cpu_profile", Kind: "path"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{CPUProfile: "/cpu"}, appwire.LaunchConfigLayer{})
	if val != "/cpu" {
		t.Fatalf("cpu_profile = %q, want /cpu", val)
	}
}

func TestCovLaunchOptionValueExportATIFPath(t *testing.T) {
	opt := appwire.LaunchOption{Field: "export_atif_path", Kind: "path"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{ExportATIFPath: "/atif"}, appwire.LaunchConfigLayer{})
	if val != "/atif" {
		t.Fatalf("export_atif_path = %q, want /atif", val)
	}
}

func TestCovLaunchOptionValueExportATIFProviderHandles(t *testing.T) {
	opt := appwire.LaunchOption{Field: "export_atif_provider_handles", Kind: "select"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{ExportATIFProviderHandles: "raw"}, appwire.LaunchConfigLayer{})
	if val != "raw" {
		t.Fatalf("export_atif_provider_handles = %q, want raw", val)
	}
}

func TestCovLaunchOptionValueSandboxNet(t *testing.T) {
	opt := appwire.LaunchOption{Field: "sandbox_net", Kind: "boolean"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if val != "(default)" {
		t.Fatalf("nil sandbox_net = %q, want (default)", val)
	}
}

func TestCovLaunchOptionValueMaxRounds(t *testing.T) {
	opt := appwire.LaunchOption{Field: "max_rounds", Kind: "integer"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if val != "(default)" {
		t.Fatalf("nil max_rounds = %q, want (default)", val)
	}
}

func TestCovLaunchOptionValueAppReplaySize(t *testing.T) {
	opt := appwire.LaunchOption{Field: "app_replay_size", Kind: "integer"}
	val, _ := launchOptionValue(opt, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{})
	if val != "(default)" {
		t.Fatalf("nil app_replay_size = %q, want (default)", val)
	}
}

// --- defaultString ---

func TestCovDefaultStringEmpty(t *testing.T) {
	if got := defaultString(""); got != "(default)" {
		t.Fatalf("defaultString(\"\") = %q", got)
	}
}

func TestCovDefaultStringNonEmpty(t *testing.T) {
	if got := defaultString("x"); got != "x" {
		t.Fatalf("defaultString(\"x\") = %q", got)
	}
}

// --- multilineSummary ---

func TestCovMultilineSummaryEmpty(t *testing.T) {
	if got := multilineSummary(""); got != "(default)" {
		t.Fatalf("multilineSummary(\"\") = %q", got)
	}
}

func TestCovMultilineSummaryWhitespace(t *testing.T) {
	if got := multilineSummary("   \n  "); got != "(default)" {
		t.Fatalf("multilineSummary whitespace = %q", got)
	}
}

func TestCovMultilineSummaryMultiLine(t *testing.T) {
	got := multilineSummary("line1\nline2\nline3")
	if got != "17 chars, 3 lines" {
		t.Fatalf("multilineSummary multiline = %q, want %q", got, "17 chars, 3 lines")
	}
}

// --- envEditValue ---

func TestCovEnvEditValueEmpty(t *testing.T) {
	if got := envEditValue(nil); got != "" {
		t.Fatalf("envEditValue(nil) = %q", got)
	}
}

func TestCovEnvEditValueSorted(t *testing.T) {
	got := envEditValue(map[string]string{"B": "2", "A": "1"})
	if got != "A=1, B=2" {
		t.Fatalf("envEditValue = %q, want %q", got, "A=1, B=2")
	}
}
