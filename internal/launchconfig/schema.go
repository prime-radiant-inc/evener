package launchconfig

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
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

type LaunchOption struct {
	Field             string                   `json:"field"`
	WireField         string                   `json:"wireField"`
	Label             string                   `json:"label"`
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
}

func LaunchOptionSchema() []LaunchOption {
	defaultLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject}
	allLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject, LaunchLayerLaunch}
	serfOnly := map[string]bool{"serf": true}
	return []LaunchOption{
		{Field: "agent", WireField: "agent", Label: "Agent", Group: LaunchGroupAgent, Kind: LaunchControlText, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "model", WireField: "model", Label: "Model", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: "SERF_MODEL"}, DriverSupport: serfOnly},
		{Field: "reasoning_effort", WireField: "reasoningEffort", Label: "Reasoning effort", Group: LaunchGroupModel, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: "SERF_REASONING_EFFORT"}, Choices: reasoningChoices(), DriverSupport: serfOnly},
		{Field: "fast_cheap_model", WireField: "fastCheapModel", Label: "Fast cheap model", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "context_strategy", WireField: "contextStrategy", Label: "Context strategy", Group: LaunchGroupLimits, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: contextChoices(), DriverSupport: serfOnly},
		{Field: "max_rounds", WireField: "maxRounds", Label: "Max rounds", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "max_subagent_depth", WireField: "maxSubagentDepth", Label: "Max subagent depth", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "no_project_prompts", WireField: "noProjectPrompts", Label: "Suppress .serf/prompts loading", Group: LaunchGroupLimits, Kind: LaunchControlBoolean, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "app_replay_size", WireField: "appReplaySize", Label: "App replay size", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: []LaunchLayerSupport{LaunchLayerGlobal}, PerLaunch: false, DriverSupport: serfOnly},
		{Field: "system_prompt_mode", WireField: "systemPromptMode", Label: "System prompt", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: systemPromptModeChoices(), DriverSupport: serfOnly},
		{Field: "system_prompt_file", WireField: "systemPromptFile", Label: "System prompt file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_text", WireField: "systemPromptText", Label: "System prompt text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_append_mode", WireField: "systemPromptAppendMode", Label: "Append to system prompt", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: appendModeChoices(), DriverSupport: serfOnly},
		{Field: "system_prompt_append_file", WireField: "systemPromptAppendFile", Label: "Append file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_append_text", WireField: "systemPromptAppendText", Label: "Append text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "skills_dirs", WireField: "skillsDirs", Label: "Skill directories", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "plugin_dirs", WireField: "pluginDirs", Label: "Plugin directories", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "mcp_configs", WireField: "mcpConfigs", Label: "MCP config files", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathFile, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "mcps", WireField: "mcps", Label: "MCP servers", Group: LaunchGroupResources, Kind: LaunchControlMCPList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "model_fallbacks", WireField: "modelFallbacks", Label: "Model fallbacks", Group: LaunchGroupResources, Kind: LaunchControlModelList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "env", WireField: "env", Label: "Environment variables", Group: LaunchGroupEnvironment, Kind: LaunchControlEnvMap, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "verbose", WireField: "verbose", Label: "Verbose event log", Group: LaunchGroupDebugLogging, Kind: LaunchControlBoolean, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "trace_file", WireField: "traceFile", Label: "Trace file", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "cpu_profile", WireField: "cpuProfile", Label: "CPU profile", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "export_atif_path", WireField: "exportATIFPath", Label: "Export ATIF path", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
	}
}

func reasoningChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "low", Label: "low"}, {Value: "medium", Label: "medium"}, {Value: "high", Label: "high"}, {Value: "xhigh", Label: "xhigh"}, {Value: "none", Label: "none"}}
}

func contextChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "compact", Label: "compact"}, {Value: "recall", Label: "recall"}, {Value: "session-log", Label: "session-log"}, {Value: "ooda", Label: "ooda"}}
}

func systemPromptModeChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "Serf default"}, {Value: "file", Label: "Pick a file"}, {Value: "inline", Label: "Fill in text"}}
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
