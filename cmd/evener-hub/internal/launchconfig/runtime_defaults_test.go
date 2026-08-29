package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/envvars"
)

func envOf(entries map[string]string) func(string) string {
	return func(name string) string { return entries[name] }
}

// schemaForTest builds a minimal LaunchOption schema exercising the
// builtin-default and env-fallback mechanisms, keyed by wire field.
func schemaForTest() []LaunchOption {
	return []LaunchOption{
		{Field: "model", WireField: "model", Kind: LaunchControlModelPicker, EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENERModel.Name}},
		{Field: "agent", WireField: "agent", Kind: LaunchControlText, BuiltinDefault: "default"},
		{Field: "fast_cheap_model", WireField: "fastCheapModel", Kind: LaunchControlModelPicker, BuiltinDefaultLabel: "primary model"},
		{Field: "reasoning_effort", WireField: "reasoningEffort", Kind: LaunchControlSelect, EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENERReasoningEffort.Name}},
		{Field: "context_strategy", WireField: "contextStrategy", Kind: LaunchControlSelect, BuiltinDefault: "compact"},
		{Field: "sandbox", WireField: "sandbox", Kind: LaunchControlSelect, BuiltinDefault: "off"},
		{Field: "sandbox_net", WireField: "sandboxNet", Kind: LaunchControlBoolean, BuiltinDefaultBool: &builtinBoolTrue},
		{Field: "openai_responses_continuation", WireField: "openAIResponsesContinuation", Kind: LaunchControlSelect, BuiltinDefault: "off", EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENEROpenAIResponsesContinuation.Name}},
		{Field: "max_rounds", WireField: "maxRounds", Kind: LaunchControlInteger, BuiltinDefaultInt: &builtinIntNeg1},
		{Field: "max_subagent_depth", WireField: "maxSubagentDepth", Kind: LaunchControlInteger, BuiltinDefaultInt: &builtinInt2},
		{Field: "max_concurrent_delegate_turns", WireField: "maxConcurrentDelegateTurns", Kind: LaunchControlInteger, BuiltinDefaultInt: &builtinInt50},
		{Field: "max_retained_terminal", WireField: "maxRetainedTerminal", Kind: LaunchControlInteger, BuiltinDefaultInt: &builtinInt2048},
		{Field: "no_project_prompts", WireField: "noProjectPrompts", Kind: LaunchControlBoolean, BuiltinDefaultBool: &builtinBoolFalse},
		{Field: "verbose", WireField: "verbose", Kind: LaunchControlBoolean, BuiltinDefaultBool: &builtinBoolFalse},
		{Field: "app_replay_size", WireField: "appReplaySize", Kind: LaunchControlInteger, BuiltinDefaultInt: &builtinInt1000},
		{Field: "export_atif_provider_handles", WireField: "exportATIFProviderHandles", Kind: LaunchControlSelect, BuiltinDefault: "redacted"},
		// A field with neither env nor builtin stays unset.
		{Field: "trace_file", WireField: "traceFile", Kind: LaunchControlPath},
	}
}

func TestApplyRuntimeDefaultsFillsBuiltinsFromSchema(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyRuntimeDefaults(resolved, envOf(nil), schemaForTest())

	if got.Effective.Agent != "default" {
		t.Errorf("Agent = %q, want builtin 'default'", got.Effective.Agent)
	}
	if got.Effective.ContextStrategy != "compact" {
		t.Errorf("ContextStrategy = %q, want builtin compact", got.Effective.ContextStrategy)
	}
	if got.Effective.Sandbox != "off" {
		t.Errorf("Sandbox = %q, want builtin off", got.Effective.Sandbox)
	}
	if got.Effective.SandboxNet == nil || !*got.Effective.SandboxNet {
		t.Errorf("SandboxNet = %v, want builtin true", got.Effective.SandboxNet)
	}
	if got.Effective.OpenAIResponsesContinuation != "off" {
		t.Errorf("OpenAIResponsesContinuation = %q, want builtin off", got.Effective.OpenAIResponsesContinuation)
	}
	if got.Effective.MaxRounds == nil || *got.Effective.MaxRounds != -1 {
		t.Errorf("MaxRounds = %v, want builtin -1", got.Effective.MaxRounds)
	}
	if got.Effective.MaxSubagentDepth == nil || *got.Effective.MaxSubagentDepth != 2 {
		t.Errorf("MaxSubagentDepth = %v, want builtin 2", got.Effective.MaxSubagentDepth)
	}
	if got.Effective.MaxConcurrentDelegateTurns == nil || *got.Effective.MaxConcurrentDelegateTurns != 50 {
		t.Errorf("MaxConcurrentDelegateTurns = %v, want builtin 50", got.Effective.MaxConcurrentDelegateTurns)
	}
	if got.Effective.MaxRetainedTerminal == nil || *got.Effective.MaxRetainedTerminal != 2048 {
		t.Errorf("MaxRetainedTerminal = %v, want builtin 2048", got.Effective.MaxRetainedTerminal)
	}
	if got.Effective.NoProjectPrompts == nil || *got.Effective.NoProjectPrompts {
		t.Errorf("NoProjectPrompts = %v, want builtin false", got.Effective.NoProjectPrompts)
	}
	if got.Effective.Verbose == nil || *got.Effective.Verbose {
		t.Errorf("Verbose = %v, want builtin false", got.Effective.Verbose)
	}
	if got.Effective.AppReplaySize == nil || *got.Effective.AppReplaySize != 1000 {
		t.Errorf("AppReplaySize = %v, want builtin 1000", got.Effective.AppReplaySize)
	}
	if got.Effective.ExportATIFProviderHandles != "redacted" {
		t.Errorf("ExportATIFProviderHandles = %q, want builtin redacted", got.Effective.ExportATIFProviderHandles)
	}
	// fast_cheap_model has no static value (dynamic: primary model), so it
	// stays unset in the effective layer; its LABEL is what the frontend
	// renders, derived from BuiltinDefaultLabel.
	if got.Effective.FastCheapModel != "" {
		t.Errorf("FastCheapModel = %q, want unset (dynamic label only)", got.Effective.FastCheapModel)
	}
	// Fields with no env/builtin stay unset.
	if got.Effective.TraceFile != "" {
		t.Errorf("TraceFile = %q, want unset", got.Effective.TraceFile)
	}
	if got.Effective.Model != "" {
		t.Errorf("Model = %q, want unset without env", got.Effective.Model)
	}
}

func TestApplyRuntimeDefaultsProvenance(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyRuntimeDefaults(resolved, envOf(nil), schemaForTest())

	for field, want := range map[string]LayerName{
		"agent":                         LayerBuiltin,
		"context_strategy":              LayerBuiltin,
		"sandbox":                       LayerBuiltin,
		"sandbox_net":                   LayerBuiltin,
		"openai_responses_continuation": LayerBuiltin,
		"max_rounds":                    LayerBuiltin,
		"max_subagent_depth":            LayerBuiltin,
		"max_concurrent_delegate_turns": LayerBuiltin,
		"max_retained_terminal":         LayerBuiltin,
		"no_project_prompts":            LayerBuiltin,
		"verbose":                       LayerBuiltin,
		"app_replay_size":               LayerBuiltin,
		"export_atif_provider_handles":  LayerBuiltin,
	} {
		if got.Provenance[field] != want {
			t.Errorf("Provenance[%s] = %q, want %q", field, got.Provenance[field], want)
		}
	}
}

func TestApplyRuntimeDefaultsEnvFillsFromEnv(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyRuntimeDefaults(resolved, envOf(map[string]string{
		envvars.EVENERModel.Name:                       "anthropic/claude-sonnet-4",
		envvars.EVENERReasoningEffort.Name:             "high",
		envvars.EVENEROpenAIResponsesContinuation.Name: "auto",
	}), schemaForTest())

	if got.Effective.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q, want env model", got.Effective.Model)
	}
	if got.Effective.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want env high", got.Effective.ReasoningEffort)
	}
	if got.Effective.OpenAIResponsesContinuation != "auto" {
		t.Errorf("OpenAIResponsesContinuation = %q, want env auto", got.Effective.OpenAIResponsesContinuation)
	}
	for field, want := range map[string]LayerName{
		"model":                         LayerEnv,
		"reasoning_effort":              LayerEnv,
		"openai_responses_continuation": LayerEnv,
	} {
		if got.Provenance[field] != want {
			t.Errorf("Provenance[%s] = %q, want %q", field, got.Provenance[field], want)
		}
	}
}

func TestApplyRuntimeDefaultsLayerWinsOverEnvAndBuiltin(t *testing.T) {
	resolved := Resolved{
		Effective: Layer{Model: "layer/model", Agent: "custom", ContextStrategy: "ooda", Sandbox: "read-only"},
		Provenance: map[string]LayerName{
			"model":            LayerGlobal,
			"agent":            LayerProject,
			"context_strategy": LayerGlobal,
		},
	}
	got := ApplyRuntimeDefaults(resolved, envOf(map[string]string{
		envvars.EVENERModel.Name: "env/model",
	}), schemaForTest())

	if got.Effective.Model != "layer/model" {
		t.Errorf("Model = %q, want layer value", got.Effective.Model)
	}
	if got.Effective.Agent != "custom" {
		t.Errorf("Agent = %q, want layer value", got.Effective.Agent)
	}
	if got.Effective.ContextStrategy != "ooda" {
		t.Errorf("ContextStrategy = %q, want layer value", got.Effective.ContextStrategy)
	}
	if got.Effective.Sandbox != "read-only" {
		t.Errorf("Sandbox = %q, want layer value", got.Effective.Sandbox)
	}
	// Layer provenance preserved.
	if got.Provenance["model"] != LayerGlobal || got.Provenance["agent"] != LayerProject {
		t.Errorf("Provenance = %#v, want original preserved", got.Provenance)
	}
}

func TestApplyRuntimeDefaultsWhitespaceOnlyEnvIgnored(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyRuntimeDefaults(resolved, envOf(map[string]string{
		envvars.EVENERModel.Name:           "   ",
		envvars.EVENERReasoningEffort.Name: "\t",
	}), schemaForTest())

	if got.Effective.Model != "" || got.Effective.ReasoningEffort != "" {
		t.Errorf("whitespace env leaked: %#v", got.Effective)
	}
	if _, ok := got.Provenance["model"]; ok {
		t.Errorf("Provenance should not mark model for whitespace env")
	}
}

func TestApplyEnvDefaultsFillsOnlyEnv(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyEnvDefaults(resolved, envOf(map[string]string{
		envvars.EVENERModel.Name: "openai/gpt-5",
	}), schemaForTest())

	if got.Effective.Model != "openai/gpt-5" {
		t.Fatalf("Model = %q, want env model", got.Effective.Model)
	}
	// No builtins applied in the env-only path.
	if got.Effective.Agent != "" || got.Effective.ContextStrategy != "" || got.Effective.SandboxNet != nil {
		t.Fatalf("builtin fields unexpectedly filled: %#v", got.Effective)
	}
	if got.Effective.MaxRounds != nil || got.Effective.AppReplaySize != nil {
		t.Fatalf("builtin ints unexpectedly filled: %#v", got.Effective)
	}
	if len(got.Provenance) != 1 || got.Provenance["model"] != LayerEnv {
		t.Fatalf("Provenance = %#v, want only model=env", got.Provenance)
	}
}

func TestApplyRuntimeDefaultsReturnsEmptySchemaGracefully(t *testing.T) {
	resolved := Resolved{Effective: Layer{}}
	got := ApplyRuntimeDefaults(resolved, envOf(nil), nil)
	if !reflect.DeepEqual(got.Effective, Layer{}) {
		t.Errorf("empty schema should produce no changes: %#v", got.Effective)
	}
}

// TestSchemaBuiltinsMatchAgentDefaults is a tripwire: the schema's
// BuiltinDefault/BuiltinDefaultInt/BuiltinDefaultBool values must match the
// agent's own defaults. Int/bool fields come from applyDefaults (session_config.go)
// and constants (tree_counter.go, subagent_manager.go); string fields come from
// flag defaults (session_tools.go: defaultAgentName="default"), server.NewServer,
// and the agent's own hardcoded defaults. The agent's applyDefaults is
// unexported, so this test hard-codes the expected values alongside their source
// location. If either side changes, this test fails and forces a deliberate sync —
// the schema is a display-only copy, not the agent's source of truth, so drift
// would make the resolve preview lie about what a spawned session would run with.
// Note: this test catches intended drift (someone changes the schema without
// updating the agent or vice versa) but cannot catch unintended drift in the
// agent's applyDefaults that no one mirrors here — it is a best-effort guard,
// not a structural guarantee.
func TestSchemaBuiltinsMatchAgentDefaults(t *testing.T) {
	schema := LaunchOptionSchema()
	opt := func(field string) LaunchOption {
		for _, o := range schema {
			if o.Field == field {
				return o
			}
		}
		t.Fatalf("field %q not in schema", field)
		return LaunchOption{}
	}

	// Int fields: agent/session_config.go applyDefaults + agent/tree_counter.go,
	// agent/subagent_manager.go constants.
	if got := opt("max_rounds"); got.BuiltinDefaultInt == nil || *got.BuiltinDefaultInt != -1 {
		t.Errorf("max_rounds builtin = %v, want -1 (applyDefaults: 0→-1 unlimited)", got.BuiltinDefaultInt)
	}
	if got := opt("max_subagent_depth"); got.BuiltinDefaultInt == nil || *got.BuiltinDefaultInt != 2 {
		t.Errorf("max_subagent_depth builtin = %v, want 2 (applyDefaults: ≤0→2)", got.BuiltinDefaultInt)
	}
	if got := opt("max_concurrent_delegate_turns"); got.BuiltinDefaultInt == nil || *got.BuiltinDefaultInt != 50 {
		t.Errorf("max_concurrent_delegate_turns builtin = %v, want 50 (defaultMaxConcurrentDelegateTurns)", got.BuiltinDefaultInt)
	}
	if got := opt("max_retained_terminal"); got.BuiltinDefaultInt == nil || *got.BuiltinDefaultInt != 2048 {
		t.Errorf("max_retained_terminal builtin = %v, want 2048 (defaultMaxRetainedTerminal)", got.BuiltinDefaultInt)
	}
	if got := opt("app_replay_size"); got.BuiltinDefaultInt == nil || *got.BuiltinDefaultInt != 1000 {
		t.Errorf("app_replay_size builtin = %v, want 1000", got.BuiltinDefaultInt)
	}

	// Bool fields: agent applyDefaults (EnableLoopDetection true) and flag
	// defaults (verbose=false, sandbox_net=true, no_project_prompts=false).
	if got := opt("sandbox_net"); got.BuiltinDefaultBool == nil || *got.BuiltinDefaultBool != true {
		t.Errorf("sandbox_net builtin = %v, want true", got.BuiltinDefaultBool)
	}
	if got := opt("verbose"); got.BuiltinDefaultBool == nil || *got.BuiltinDefaultBool != false {
		t.Errorf("verbose builtin = %v, want false", got.BuiltinDefaultBool)
	}
	if got := opt("no_project_prompts"); got.BuiltinDefaultBool == nil || *got.BuiltinDefaultBool != false {
		t.Errorf("no_project_prompts builtin = %v, want false", got.BuiltinDefaultBool)
	}

	// String fields: agent flag defaults (session_tools.go: defaultAgentName),
	// server.NewServer defaults, and agent hardcoded defaults.
	if got := opt("agent"); got.BuiltinDefault != "default" {
		t.Errorf("agent builtin = %q, want \"default\"", got.BuiltinDefault)
	}
	if got := opt("context_strategy"); got.BuiltinDefault != "compact" {
		t.Errorf("context_strategy builtin = %q, want \"compact\"", got.BuiltinDefault)
	}
	if got := opt("sandbox"); got.BuiltinDefault != "off" {
		t.Errorf("sandbox builtin = %q, want \"off\"", got.BuiltinDefault)
	}
	if got := opt("openai_responses_continuation"); got.BuiltinDefault != "off" {
		t.Errorf("openai_responses_continuation builtin = %q, want \"off\"", got.BuiltinDefault)
	}
	if got := opt("export_atif_provider_handles"); got.BuiltinDefault != "redacted" {
		t.Errorf("export_atif_provider_handles builtin = %q, want \"redacted\"", got.BuiltinDefault)
	}

	// non_interactive has no builtin: it's per-launch only with no
	// DefaultableLayers, so the resolve preview must not show a builtin.
	if got := opt("non_interactive"); got.BuiltinDefaultBool != nil {
		t.Errorf("non_interactive should have no BuiltinDefaultBool (per-launch only, not layer-settable), got %v", got.BuiltinDefaultBool)
	}
}
