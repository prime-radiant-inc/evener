package main

import "flag"

// launchFlags are the flags `evener` and `evener serve` both take to describe
// the session they launch. One definition keeps their defaults and help text
// from drifting apart; each command registers only its own flags after these.
type launchFlags struct {
	model                       *string
	fastCheapModel              *string
	visionModel                 *string
	workDir                     *string
	systemPrompt                *string
	stateDir                    *string
	disableMemory               *bool
	resume                      *string
	resumeLast                  *bool
	maxRounds                   *int
	maxSubagentDepth            *int
	maxConcurrentDelegates      *int
	maxRetainedTerminal         *int
	shareTaskStore              *bool
	resultToolName              *string
	reasoningEffort             *string
	exportATIF                  *string
	exportATIFProviderHandles   *string
	contextStrategy             *string
	outputSchema                *string
	verbose                     *bool
	agentName                   *string
	skillsDirs                  stringSliceFlag
	mcpServers                  stringSliceFlag
	mcpConfigs                  stringSliceFlag
	pluginDirs                  stringSliceFlag
	enabledPlugins              pluginSelectionFlag
	systemPromptAsUser          *bool
	openAIResponsesContinuation *string
	providerIdleTimeout         *string
	cpuProfile                  *string
	traceFile                   *string
	systemPromptAppend          stringSliceFlag
	sandbox                     *string
	sandboxNet                  *string
	apiLog                      *string
}

func registerLaunchFlags(fs *flag.FlagSet) *launchFlags {
	flags := &launchFlags{}

	flags.model = fs.String("model", "", "LLM model identifier (`provider/model`)")
	flags.fastCheapModel = fs.String("fast-cheap-model", "", "auxiliary model for side calls (naming, summarization, web fetch); 'provider/model' may use a different provider than --model, or a bare 'model' for the active provider")
	flags.visionModel = fs.String("vision-model", "", "vision side-channel model: 'off' disables image description, 'provider/model' or bare 'model' routes it (default: the session model)")
	flags.workDir = fs.String("dir", "", "working `directory` (default: current directory)")
	flags.systemPrompt = fs.String("system-prompt", "", "path to a custom system prompt `file`")
	flags.stateDir = fs.String("state-dir", "", "override runtime state `directory` (default: XDG-computed)")
	flags.disableMemory = fs.Bool("disable-memory", false, "disable native memory for this session, including on resume")
	flags.resume = fs.String("resume", "", "resume a previous session by `id`")
	flags.resumeLast = fs.Bool("resume-last", false, "resume the most recent session")
	flags.maxRounds = fs.Int("max-rounds", -1, "max tool rounds per input (-1=default: unlimited, 0=unlimited)")
	flags.maxSubagentDepth = fs.Int("max-subagent-depth", -1, "max subagent nesting depth (default: 2)")
	flags.maxConcurrentDelegates = fs.Int("max-concurrent-delegates", -1, "max concurrently running delegate turns per session tree (default: 50)")
	flags.maxRetainedTerminal = fs.Int("max-retained-terminal", -1, "max retained terminal delegate records per session (default: 2048)")
	flags.shareTaskStore = fs.Bool("share-task-store", false, "share task list between parent and child sessions")
	flags.resultToolName = fs.String("result-tool-name", "", "override the result tool `name` (default: communicate)")
	flags.reasoningEffort = fs.String("reasoning-effort", "", "reasoning effort `level`: minimal|low|medium|high|xhigh|max|none")
	flags.exportATIF = fs.String("export-atif", "", "export ATIF v1.7 trajectory to this `path` on session close")
	flags.exportATIFProviderHandles = fs.String("export-atif-provider-handles", "", "ATIF provider handle export `mode`: redacted|raw-local (default: redacted)")
	flags.contextStrategy = fs.String("context-strategy", "", "context management `strategy`: compact|session-log|ooda (default: compact)")
	flags.outputSchema = fs.String("output-schema", "", "inline JSON Schema `document` applied to the communicate tool's output field (replaces the default schema)")
	flags.verbose = fs.Bool("verbose", false, "emit NDJSON events to stderr")
	flags.agentName = fs.String("agent", "", "agent persona `name`: default (default), explorer, or another available agent name")
	fs.Var(&flags.skillsDirs, "skills-dir", "extra skill `directory` (repeatable)")
	fs.Var(&flags.mcpServers, "mcp", "MCP server `spec` (repeatable, format: name:command args...)")
	fs.Var(&flags.mcpConfigs, "mcp-config", "path to .mcp.json `file` (repeatable)")
	fs.Var(&flags.pluginDirs, "plugin-dir", "plugin `directory` (repeatable)")
	fs.Var(&flags.enabledPlugins, "enabled-plugins", "comma-separated plugin names to enable (empty selects none)")
	flags.systemPromptAsUser = fs.Bool("system-prompt-as-user", false, "deliver system prompt as first user message instead of system instructions")
	flags.providerIdleTimeout = fs.String("provider-idle-timeout", "", "Provider response-byte idle duration (default: 10m; no total request limit)")
	flags.openAIResponsesContinuation = fs.String("openai-responses-continuation", "", "OpenAI Responses continuation `mode`: off|auto (default: off)")
	flags.cpuProfile = fs.String("cpu-profile", "", "write CPU profile to this `file` path")
	flags.traceFile = fs.String("trace", "", "write execution trace to this `file` path")
	fs.Var(&flags.systemPromptAppend, "system-prompt-append", "path to append to system prompt `file` (repeatable)")
	flags.sandbox = fs.String("sandbox", "off", "sandbox `mode`: off (default), read-only, workspace-write, or restricted")
	flags.sandboxNet = fs.String("sandbox-net", "on", "sandbox network egress `on|off` (default on; only applies with a non-off --sandbox mode)")
	flags.apiLog = fs.String("api-log", "off", "durable API request logging `on|off` (default off; on records every provider request and response to <state-dir>/sessions/<id>.api.jsonl)")
	return flags
}
