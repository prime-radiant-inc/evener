package schema

// ConfigSnapshot is the persisted, wire-serializable projection of an agent
// session's configuration. It holds exactly the json-tagged fields of the
// engine's SessionConfig — same names, same tags, same declaration order — so a
// session's meta.json and snapshot files round-trip byte-for-byte across the
// type boundary. Engine-only fields (StateDir, ResolveProfile, retry policy,
// spawn linkage, test hooks) are never serialized and so do not appear here.
// See the engine's SessionConfig for the authoritative semantics and defaults
// of each field.
type ConfigSnapshot struct {
	MaxToolRoundsPerInput       int                        `json:"max_tool_rounds_per_input,omitempty"`     // tool-call rounds per ProcessInput before TURN_LIMIT
	MaxTurns                    int                        `json:"max_turns,omitempty"`                     // lifetime cap on user inputs (0 = unlimited)
	DefaultCommandTimeoutMS     int                        `json:"default_command_timeout_ms,omitempty"`    // default shell/exec timeout
	MaxCommandTimeoutMS         int                        `json:"max_command_timeout_ms,omitempty"`        // ceiling on a requested per-command timeout
	MaxSubagentDepth            int                        `json:"max_subagent_depth,omitempty"`            // how deeply sub-agents may nest
	ToolOutputLimits            map[string]ToolOutputLimit `json:"tool_output_limits,omitempty"`            // per-tool output truncation overrides
	UserInstructionOverride     string                     `json:"user_instruction_override,omitempty"`     // text appended at the end of the system prompt
	AgentName                   string                     `json:"agent_name,omitempty"`                    // persona selected for prompt composition
	ReasoningEffort             string                     `json:"reasoning_effort,omitempty"`              // reasoning-effort passthrough (low|medium|high)
	SkillsDirs                  []string                   `json:"skills_dirs,omitempty"`                   // extra directories scanned for skills
	MCPConfigFiles              []string                   `json:"mcp_config_files,omitempty"`              // paths to .mcp.json files
	MCPInline                   []string                   `json:"mcp_inline,omitempty"`                    // inline MCP server specs
	PluginDirs                  []string                   `json:"plugin_dirs,omitempty"`                   // directories scanned for plugins
	SystemPromptFile            string                     `json:"system_prompt_file,omitempty"`            // replacement base instruction prelude
	SystemPromptAppend          []string                   `json:"system_prompt_append,omitempty"`          // file paths appended to the system prompt
	NoProjectPrompts            bool                       `json:"no_project_prompts,omitempty"`            // suppress loading .serf/prompts/
	NonInteractive              bool                       `json:"non_interactive,omitempty"`               // no human available for questions/confirmation
	ContextStrategy             string                     `json:"context_strategy,omitempty"`              // context-management strategy
	ShareTasksWithChildren      bool                       `json:"share_tasks_with_children,omitempty"`     // pass the task store to spawned children
	ResultToolName              string                     `json:"result_tool_name,omitempty"`              // override for the result tool name
	EnableLoopDetection         *bool                      `json:"enable_loop_detection,omitempty"`         // repeated-tool-call loop detection (nil = enabled)
	LoopDetectionWindow         int                        `json:"loop_detection_window,omitempty"`         // recent tool-call signatures examined
	ModelFallbacks              []string                   `json:"model_fallbacks,omitempty"`               // provider/model chain tried on permanent errors
	SystemPromptAsUser          bool                       `json:"system_prompt_as_user,omitempty"`         // fold the system prompt into the first user message
	OpenAIResponsesContinuation string                     `json:"openai_responses_continuation,omitempty"` // OpenAI Responses continuation mode: off|auto
}
