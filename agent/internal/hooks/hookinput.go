package hooks

import "primeradiant.com/evener/agent/plugin"

// Input is the JSON payload piped to command hooks via stdin.
type Input struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	Message       string         `json:"message,omitempty"`
	Reason        string         `json:"reason,omitempty"`

	// Official Claude-compatible fields (claude-compatible-subset).
	// These are additive; hooks may read them when present.
	TranscriptPath string `json:"transcript_path,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
	ToolResponse   string `json:"tool_response,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
	// Effort is the reasoning effort for the current session, as Claude's
	// effort object. A nil pointer emits no field; a non-pointer struct with
	// omitempty would serialize {"level":""} on every event.
	Effort *Effort `json:"effort,omitempty"`
	// Prompt is the official Claude UserPromptSubmit field. It carries the same
	// value as the legacy UserPrompt alias; Claude-style hooks read "prompt".
	Prompt string `json:"prompt,omitempty"`

	// Event-specific fields, all omitempty. Pointers where false or 0 is
	// meaningful and absence differs from zero.
	Trigger              string          `json:"trigger,omitempty"`
	CustomInstructions   *string         `json:"custom_instructions,omitempty"`
	CompactSummary       string          `json:"compact_summary,omitempty"`
	StopHookActive       *bool           `json:"stop_hook_active,omitempty"`
	LastAssistantMessage string          `json:"last_assistant_message,omitempty"`
	Error                string          `json:"error,omitempty"`
	ErrorDetails         string          `json:"error_details,omitempty"`
	IsInterrupt          *bool           `json:"is_interrupt,omitempty"`
	DurationMS           *int64          `json:"duration_ms,omitempty"`
	ToolCalls            []BatchToolCall `json:"tool_calls,omitempty"`
	NotificationType     string          `json:"notification_type,omitempty"`
	Title                string          `json:"title,omitempty"`

	// Legacy Evener aliases retained during migration.
	// tool_result = tool_response; user_prompt = prompt.
	ToolResult string `json:"tool_result,omitempty"`
	UserPrompt string `json:"user_prompt,omitempty"`
}

// Effort is the Claude-documented effort object: the level under "level".
type Effort struct {
	Level string `json:"level"`
}

// BatchToolCall is one entry of PostToolBatch's tool_calls array.
type BatchToolCall struct {
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input,omitempty"`
	ToolUseID    string         `json:"tool_use_id"`
	ToolResponse string         `json:"tool_response"`
}

// continueDiscardedEvents are the events where Claude discards continue/stopReason.
var continueDiscardedEvents = map[plugin.HookEvent]bool{
	plugin.HookStopFailure:  true,
	plugin.HookNotification: true,
	plugin.HookPreCompact:   true,
	plugin.HookPostCompact:  true,
	plugin.HookSessionEnd:   true,
}

// HonorsContinue reports whether continue:false stops the action on this event.
func HonorsContinue(e plugin.HookEvent) bool { return !continueDiscardedEvents[e] }
