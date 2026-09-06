package launchconfig

import (
	"sync"

	"primeradiant.com/evener/envvars"
)

type LaunchControlKind string
type LaunchPathKind string
type LaunchGroup string
type LaunchLayerSupport string

const (
	LaunchControlModelPicker LaunchControlKind = "modelPicker"
	LaunchControlText        LaunchControlKind = "text"
	LaunchControlMultiline   LaunchControlKind = "multilineText"
	LaunchControlInteger     LaunchControlKind = "integer"
	LaunchControlBoolean     LaunchControlKind = "boolean"
	LaunchControlSelect      LaunchControlKind = "select"
	LaunchControlRadio       LaunchControlKind = "radio"
	LaunchControlPath        LaunchControlKind = "path"
	LaunchControlPathList    LaunchControlKind = "pathList"
	LaunchControlModelList   LaunchControlKind = "modelList"
	LaunchControlMCPList     LaunchControlKind = "mcpServerList"
	LaunchControlEnvMap      LaunchControlKind = "envMap"
)

const LaunchControlPluginSelection LaunchControlKind = "pluginSelection"

const (
	LaunchPathNone       LaunchPathKind = ""
	LaunchPathDir        LaunchPathKind = "dir"
	LaunchPathFile       LaunchPathKind = "file"
	LaunchPathOutputFile LaunchPathKind = "outputFile"
	LaunchPathCommand    LaunchPathKind = "command"
)

const (
	LaunchGroupAgent        LaunchGroup = "Agent"
	LaunchGroupModel        LaunchGroup = "Model"
	LaunchGroupLimits       LaunchGroup = "Limits"
	LaunchGroupSystemPrompt LaunchGroup = "System Prompt"
	LaunchGroupResources    LaunchGroup = "Resources"
	LaunchGroupEnvironment  LaunchGroup = "Environment"
	LaunchGroupSandbox      LaunchGroup = "Sandbox"
	LaunchGroupDebugLogging LaunchGroup = "Debug Logging"
)

const (
	LaunchLayerGlobal  LaunchLayerSupport = "global"
	LaunchLayerProject LaunchLayerSupport = "project"
	LaunchLayerLaunch  LaunchLayerSupport = "launch"
)

type LaunchOptionChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type LaunchOptionEnvFallback struct {
	Name string `json:"name"`
}

type LaunchOption struct {
	Field             string                   `json:"field"`
	WireField         string                   `json:"wireField"`
	Label             string                   `json:"label"`
	Description       string                   `json:"description,omitempty"`
	Group             LaunchGroup              `json:"group"`
	Kind              LaunchControlKind        `json:"kind"`
	PathKind          LaunchPathKind           `json:"pathKind,omitempty"`
	Repeatable        bool                     `json:"repeatable,omitempty"`
	DefaultableLayers []LaunchLayerSupport     `json:"defaultableLayers,omitempty"`
	PerLaunch         bool                     `json:"perLaunch"`
	DebugOnly         bool                     `json:"debugOnly,omitempty"`
	EnvFallback       *LaunchOptionEnvFallback `json:"envFallback,omitempty"`
	Choices           []LaunchOptionChoice     `json:"choices,omitempty"`
	DriverSupport     map[string]bool          `json:"driverSupport,omitempty"`
	// BuiltinDefault is the agent's own default for this field when flag,
	// env, and every layer leave it unset — the floor of the fallback
	// chain. String fields only; int/bool fields use BuiltinDefaultInt and
	// BuiltinDefaultBool. A field with no builtin default (model, reasoning
	// effort, trace file) leaves all three nil, so the resolve reports it
	// genuinely unset rather than inventing a value. Sourced from the
	// agent's own code (applyDefaults, flag defaults, use-site resolution).
	BuiltinDefault     string `json:"builtinDefault,omitempty"`
	BuiltinDefaultInt  *int   `json:"builtinDefaultInt,omitempty"`
	BuiltinDefaultBool *bool  `json:"builtinDefaultBool,omitempty"`
	// BuiltinDefaultLabel names a dynamic default that can't be expressed as
	// a static value because it depends on runtime state the resolve doesn't
	// have (e.g. fast_cheap_model falls back to the primary model). The
	// effective layer stays unset; the frontend renders this label so
	// "(default)" still names a real answer instead of standing in for one.
	BuiltinDefaultLabel string `json:"builtinDefaultLabel,omitempty"`
}

// cachedLaunchOptionSchema is the static schema allocated once on first use.
// The schema is immutable (no per-request state), so callers share the same
// slice; none mutate it (the RPC handlers iterate and copy into wire types,
// and ApplyRuntimeDefaults/ApplyEnvDefaults only read it). This avoids
// rebuilding ~30 entries with heap-allocated pointer defaults on every RPC.
var cachedLaunchOptionSchema = sync.OnceValue(buildLaunchOptionSchema)

// LaunchOptionSchema returns the cached launch option schema. The returned
// slice is shared and must not be mutated.
func LaunchOptionSchema() []LaunchOption {
	return cachedLaunchOptionSchema()
}

func buildLaunchOptionSchema() []LaunchOption {
	defaultLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject}
	allLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject, LaunchLayerLaunch}
	evenerOnly := map[string]bool{"evener": true}
	return []LaunchOption{
		{Field: "agent", WireField: "agent", Label: "Agent", Description: "Name of the agent binary to run. Defaults to evener.", Group: LaunchGroupAgent, Kind: LaunchControlText, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefault: "default"},
		{Field: "model", WireField: "model", Label: "Model", Description: "Primary model used for the main reasoning loop. Overrides " + envvars.EVENERModel.Name + " env var.", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENERModel.Name}, DriverSupport: evenerOnly},
		{Field: "reasoning_effort", WireField: "reasoningEffort", Label: "Reasoning effort", Description: "Extended thinking budget for models that support it. Higher effort increases cost and latency.", Group: LaunchGroupModel, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENERReasoningEffort.Name}, Choices: reasoningChoices(), DriverSupport: evenerOnly},
		{Field: "fast_cheap_model", WireField: "fastCheapModel", Label: "Fast cheap model", Description: "Lightweight model for quick sub-tasks like file triage. Falls back to the primary model if unset.", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultLabel: "primary model"},
		{Field: "context_strategy", WireField: "contextStrategy", Label: "Context strategy", Description: "How evener manages context window pressure. compact prunes aggressively; session-log and ooda use alternative strategies.", Group: LaunchGroupLimits, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: contextChoices(), DriverSupport: evenerOnly, BuiltinDefault: "compact"},
		{Field: "provider_idle_timeout", WireField: "providerIdleTimeout", Label: "Provider idle timeout", Description: "Maximum time without incoming response bytes, including heartbeat bytes. Positive Go duration (for example 10m or 45s); no total request duration limit. Explicit resume values override persisted settings.", Group: LaunchGroupLimits, Kind: LaunchControlText, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefault: "10m"},
		{Field: "openai_responses_continuation", WireField: "openAIResponsesContinuation", Label: "OpenAI Responses continuation", Description: "Controls whether eligible OpenAI Responses sessions may use provider-side continuation. Values are off or auto; default off preserves full-history behavior. Launch settings override " + envvars.EVENEROpenAIResponsesContinuation.Name + " and explicit resume values override persisted snapshots. Future auto enablement may allow provider-side storage/retention and affect provider-token/cost behavior.", Group: LaunchGroupLimits, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: envvars.EVENEROpenAIResponsesContinuation.Name}, Choices: openAIResponsesContinuationChoices(), DriverSupport: evenerOnly, BuiltinDefault: "off"},
		{Field: "max_rounds", WireField: "maxRounds", Label: "Max rounds", Description: "Hard cap on the number of model turns per session. The session ends with an error if the limit is reached.", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultInt: &builtinIntNeg1},
		{Field: "max_subagent_depth", WireField: "maxSubagentDepth", Label: "Max subagent depth", Description: "How many levels of nested subagent spawns are allowed. 0 disables subagents entirely.", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultInt: &builtinInt2},
		{Field: "max_concurrent_delegate_turns", WireField: "maxConcurrentDelegateTurns", Label: "Max concurrent delegates", Description: "How many delegate turns may run concurrently across the session tree. Idle delegates do not count.", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultInt: &builtinInt50},
		{Field: "max_retained_terminal", WireField: "maxRetainedTerminal", Label: "Max retained terminal delegates", Description: "How many finished delegate records a session retains before oldest reclaimable ones are evicted.", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultInt: &builtinInt2048},
		{Field: "no_project_prompts", WireField: "noProjectPrompts", Label: "Suppress .evener/prompts loading", Description: "When true, evener ignores any .evener/prompts directory in the working tree. Useful for sandboxed or audited runs.", Group: LaunchGroupLimits, Kind: LaunchControlBoolean, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultBool: &builtinBoolFalse},
		{Field: "non_interactive", WireField: "nonInteractive", Label: "Non-interactive mode", Description: "Add headless-mode guidance to the system prompt for unattended automation runs.", Group: LaunchGroupLimits, Kind: LaunchControlBoolean, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "app_replay_size", WireField: "appReplaySize", Label: "App replay size", Description: "Number of recent events replayed to a reconnecting browser. Lower values reduce memory use.", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: []LaunchLayerSupport{LaunchLayerGlobal}, PerLaunch: false, DriverSupport: evenerOnly, BuiltinDefaultInt: &builtinInt1000},
		{Field: "system_prompt_mode", WireField: "systemPromptMode", Label: "System prompt", Description: "Override the built-in system prompt. Choose a file or enter text inline; leave at default to use evener's built-in prompt.", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: systemPromptModeChoices(), DriverSupport: evenerOnly},
		{Field: "system_prompt_file", WireField: "systemPromptFile", Label: "System prompt file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "system_prompt_text", WireField: "systemPromptText", Label: "System prompt text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "system_prompt_append_mode", WireField: "systemPromptAppendMode", Label: "Append to system prompt", Description: "Append extra instructions to whichever system prompt is active without replacing it entirely.", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: appendModeChoices(), DriverSupport: evenerOnly},
		{Field: "system_prompt_append_file", WireField: "systemPromptAppendFile", Label: "Append file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "system_prompt_append_text", WireField: "systemPromptAppendText", Label: "Append text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "skills_dirs", WireField: "skillsDirs", Label: "Skill directories", Description: "Extra directories evener scans for skill files at spawn time, in addition to the default locations.", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "plugin_dirs", WireField: "pluginDirs", Label: "Plugin directories", Description: "Extra directories evener scans for plugin executables at spawn time.", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "mcp_configs", WireField: "mcpConfigs", Label: "MCP config files", Description: "JSON config files declaring MCP servers. Each file may define one or more servers.", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathFile, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "mcps", WireField: "mcps", Label: "MCP servers", Description: "Inline MCP server definitions. Each entry specifies a name, command, and optional arguments.", Group: LaunchGroupResources, Kind: LaunchControlMCPList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "model_fallbacks", WireField: "modelFallbacks", Label: "Model fallbacks", Description: "Ordered list of alternative models evener tries when the primary model is unavailable or rate-limited.", Group: LaunchGroupResources, Kind: LaunchControlModelList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "enabled_plugins", WireField: "enabledPlugins", Label: "Enabled plugins", Description: "Select the installed plugins enabled for this launch. An empty selection disables all plugins.", Group: LaunchGroupResources, Kind: LaunchControlPluginSelection, Repeatable: true, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "env", WireField: "env", Label: "Environment variables", Description: "Extra environment variables injected into the evener process. Do not store credentials here; use the Providers page instead.", Group: LaunchGroupEnvironment, Kind: LaunchControlEnvMap, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly},
		{Field: "sandbox", WireField: "sandbox", Label: "Sandbox", Description: "Confine the session to a sandbox mode. off = no confinement; read-only = reads anywhere but secret paths, no writes (a private temp dir only); workspace-write = reads anywhere but secret paths, writes the working tree; restricted = reads and writes only the working tree. All sandboxed modes mask credential and secret paths, give a private temp dir, and confine spawned shell commands too. Network egress is a separate toggle (Sandbox network egress). Default off.", Group: LaunchGroupSandbox, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: sandboxChoices(), DriverSupport: evenerOnly, BuiltinDefault: "off"},
		{Field: "sandbox_net", WireField: "sandboxNet", Label: "Sandbox network egress", Description: "Allow network egress when sandboxed. Default on. Has no effect unless a sandbox mode is set.", Group: LaunchGroupSandbox, Kind: LaunchControlBoolean, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: evenerOnly, BuiltinDefaultBool: &builtinBoolTrue},
		{Field: "verbose", WireField: "verbose", Label: "Verbose event log", Description: "Emit all internal events to the debug log. Useful for diagnosing unexpected behaviour.", Group: LaunchGroupDebugLogging, Kind: LaunchControlBoolean, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: evenerOnly, BuiltinDefaultBool: &builtinBoolFalse},
		{Field: "trace_file", WireField: "traceFile", Label: "Trace file", Description: "Write a structured execution trace to this file. Suitable for post-mortem analysis with evener trace tooling.", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: evenerOnly},
		{Field: "cpu_profile", WireField: "cpuProfile", Label: "CPU profile", Description: "Write a Go pprof CPU profile to this path. Only useful when profiling evener itself.", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: evenerOnly},
		{Field: "export_atif_path", WireField: "exportATIFPath", Label: "Export ATIF path", Description: "Write the session's agent-tool interaction format (ATIF) log to this file after the session ends.", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: evenerOnly},
		{Field: "export_atif_provider_handles", WireField: "exportATIFProviderHandles", Label: "ATIF provider handles", Description: "Controls whether ATIF export redacts provider handles or includes raw local diagnostic handles.", Group: LaunchGroupDebugLogging, Kind: LaunchControlSelect, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, Choices: atifProviderHandleChoices(), DriverSupport: evenerOnly, BuiltinDefault: "redacted"},
	}
}

func reasoningChoices() []LaunchOptionChoice {
	// In layered launch config, "" ("(default)") means "no override → inherit the
	// global/project default", whereas "none" overrides an inherited level and
	// reaches the daemon as an explicit off (thinking disabled where the model
	// allows it, the field omitted otherwise). So both are kept and are
	// genuinely distinct here.
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "minimal", Label: "minimal"}, {Value: "low", Label: "low"}, {Value: "medium", Label: "medium"}, {Value: "high", Label: "high"}, {Value: "xhigh", Label: "xhigh"}, {Value: "max", Label: "max"}, {Value: "none", Label: "none (off where supported)"}}
}

func contextChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "compact", Label: "compact"}, {Value: "session-log", Label: "session-log"}, {Value: "ooda", Label: "ooda"}}
}

func openAIResponsesContinuationChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default: off)"}, {Value: "off", Label: "off"}, {Value: "auto", Label: "auto"}}
}

func sandboxChoices() []LaunchOptionChoice {
	// The empty value is "inherit the lower layer" (only the system-default global
	// layer treats absent as off); the explicit "off" entry is how a project/launch
	// layer clears a global default.
	return []LaunchOptionChoice{{Value: "", Label: "(inherit)"}, {Value: "off", Label: "off"}, {Value: "read-only", Label: "read-only"}, {Value: "workspace-write", Label: "workspace-write"}, {Value: "restricted", Label: "restricted"}}
}

func atifProviderHandleChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default: redacted)"}, {Value: "redacted", Label: "redacted"}, {Value: "raw-local", Label: "raw-local"}}
}

func systemPromptModeChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "Evener default"}, {Value: "file", Label: "Pick a file"}, {Value: "inline", Label: "Fill in text"}}
}

func appendModeChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "Do not append anything"}, {Value: "file", Label: "Pick a file"}, {Value: "inline", Label: "Fill in text"}}
}

func LaunchOptionExclusions() map[string]string {
	return map[string]string{
		"addr":                  "hub-owned process binding",
		"run_dir":               "hub-owned process state",
		"resume":                "hub-owned lifecycle control",
		"resume_last":           "hub-owned lifecycle control",
		"state_dir":             "hub-owned process state",
		"system_prompt_as_user": "CLI-only behavior flag excluded from this UI pass",
		"output_schema":         "CLI-only eval/result behavior excluded from this UI pass",
		"result_tool_name":      "CLI-only eval/result behavior excluded from this UI pass",
		"share_task_store":      "CLI-only task behavior excluded from this UI pass",
	}
}

// Pointer-typed builtin defaults for the schema. Package-level vars whose
// addresses are taken once: the schema is cached and immutable, so sharing
// the same pointer is safe, and the modernize linter does not flag &var.
var (
	builtinIntNeg1   = -1
	builtinInt2      = 2
	builtinInt50     = 50
	builtinInt2048   = 2048
	builtinInt1000   = 1000
	builtinBoolFalse = false
	builtinBoolTrue  = true
)
