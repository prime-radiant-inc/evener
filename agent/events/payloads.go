package events

import (
	"encoding/json"

	"primeradiant.com/serf/llm"
)

// Typed event payload structs. JSON tags match the map keys used previously.

// SessionStartData is the payload for an EventSessionStart event.
type SessionStartData struct {
	Profile           string `json:"profile"`
	Model             string `json:"model"`
	Restored          bool   `json:"restored,omitempty"`
	Turns             int    `json:"turns,omitempty"`
	LastInputTokens   int    `json:"last_input_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
}

// SessionEndData is the payload for an EventSessionEnd event.
type SessionEndData struct {
	Reason string `json:"reason"`
	State  string `json:"state,omitempty"`
	Turns  int    `json:"turns,omitempty"`
	// Interrupted is true when the input was aborted via the interrupt
	// signal (POST /interrupt or TurnInterrupt RPC). When set, the
	// session remains alive (State="idle") but the active turn was cut
	// short; the appwire projection maps this to a "canceled" turn
	// status while the thread status stays idle.
	Interrupted bool `json:"interrupted,omitempty"`
}

// UserInputData is the payload for an EventUserInput event.
type UserInputData struct {
	Text   string           `json:"text"`
	Images []UserInputImage `json:"images,omitempty"`
	// Turn is the 1-based transcript entry index for this USER_INPUT turn.
	// The hub renderer uses it for turn-targeted operations such as fork.
	Turn int `json:"turn,omitempty"`
}

// UserInputImage carries enough metadata for the UI to render a thumbnail.
// MediaType is e.g. "image/png"; Data is the raw bytes (JSON un/marshals as
// base64); Name is the original filename when known.
type UserInputImage struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data,omitempty"`
	Name      string `json:"name,omitempty"`
}

// AssistantTextStartData is the payload for an EventAssistantTextStart event.
type AssistantTextStartData struct {
	Model string `json:"model"`
}

// AssistantTextDeltaData is the payload for an EventAssistantTextDelta event.
type AssistantTextDeltaData struct {
	Delta string `json:"delta"`
}

// AssistantTextEndData is the payload for an EventAssistantTextEnd event.
type AssistantTextEndData struct {
	Text         string    `json:"text"`
	Usage        llm.Usage `json:"usage,omitempty"`
	FinishReason string    `json:"finish_reason"`
	Model        string    `json:"model"`
	Reasoning    string    `json:"reasoning,omitempty"`
}

// ToolCallStartData is the payload for an EventToolCallStart event.
type ToolCallStartData struct {
	ToolName      string `json:"tool_name"`
	CallID        string `json:"call_id"`
	ArgumentsJSON string `json:"arguments_json,omitempty"`
	Description   string `json:"description,omitempty"`
}

// ToolCallOutputDeltaData is the payload for an EventToolCallOutputDelta event.
type ToolCallOutputDeltaData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Delta    string `json:"delta"`
}

// ToolCallEndData is the payload for an EventToolCallEnd event.
type ToolCallEndData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`

	// ToolState is an optional JSON snapshot produced by the tool
	// executor. Dashboards and other consumers render from this directly
	// rather than reconstructing state by replaying the event stream.
	// The LLM never sees this field.
	ToolState json.RawMessage `json:"tool_state,omitempty"`
}

// SteeringInjectedData is the payload for an EventSteeringInjected event.
type SteeringInjectedData struct {
	Text   string           `json:"text"`
	Images []UserInputImage `json:"images,omitempty"`
}

// QueueChangedData carries an authoritative snapshot of the per-session
// input queue after a mutation (kata r80p). Preview entries are FIFO with
// the head at index 0 and have been collapsed to a single line.
type QueueChangedData struct {
	Depth   int      `json:"depth"`
	Preview []string `json:"preview,omitempty"`
}

// TurnLimitData is the payload for an EventTurnLimit event.
type TurnLimitData struct {
	MaxTurns              int `json:"max_turns,omitempty"`
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
}

// LoopDetectionData is the payload for an EventLoopDetection event.
type LoopDetectionData struct {
	Message string `json:"message"`
}

// CommunicateData is the payload for an EventCommunicate event.
type CommunicateData struct {
	AwaitReply bool   `json:"await_reply"`
	Message    string `json:"message"`
}

// SkillActivatedData is the payload for an EventSkillActivated event.
type SkillActivatedData struct {
	Name string `json:"name"`
}

// ContextCompactionData is the payload for an EventContextCompaction event.
type ContextCompactionData struct {
	Layer           string `json:"layer,omitempty"`
	TurnsBefore     int    `json:"turns_before,omitempty"`
	TurnsAfter      int    `json:"turns_after,omitempty"`
	EstTokensBefore int    `json:"est_tokens_before,omitempty"`
	EstTokensAfter  int    `json:"est_tokens_after,omitempty"`
}

// CompactionTurnData is the payload for an EventCompactionTurn event.
type CompactionTurnData struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// WarningData is the payload for an EventWarning event.
type WarningData struct {
	Message           string `json:"message"`
	Source            string `json:"source,omitempty"`
	Title             string `json:"title,omitempty"`
	Hint              string `json:"hint,omitempty"`
	ApproxTokens      int    `json:"approx_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
	Percent           int    `json:"percent,omitempty"`
	// PluginName names the plugin a hook-configuration warning is about (empty
	// for warnings unrelated to plugin hooks). Carries only the plugin name, no
	// hook payload or secrets.
	PluginName string `json:"plugin_name,omitempty"`
	// EventName is the offending hook event name (or, for an invalid matcher,
	// the event the matcher was declared under) for a hook-configuration
	// warning. Carries only the name, no hook payload or secrets.
	EventName string `json:"event_name,omitempty"`
}

// ErrorData is the payload for an EventError event.
type ErrorData struct {
	Error  string `json:"error"`
	Source string `json:"source,omitempty"`
	Title  string `json:"title,omitempty"`
	Hint   string `json:"hint,omitempty"`
	// Cause is an optional structured classifier. Today only provider
	// failures populate it; consumers can typed-branch on Cause.Kind
	// instead of substring-matching the Error message (kata ts0x). A nil
	// Cause means the failure source is unknown (back-compat default).
	Cause *ErrorCause `json:"cause,omitempty"`
}

// ErrorCause describes a structured root cause for an EventError. Today the
// only Kind is "provider" (HTTP failure from an LLM adapter); additional
// kinds can be added as further consumers need typed dispatch (kata ts0x).
type ErrorCause struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
}

// SubagentStartData is the payload for an EventSubagentStart event.
type SubagentStartData struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
}

// SubagentEndData is the payload for an EventSubagentEnd event. Status carries the
// run outcome (completed|failed|cancelled).
type SubagentEndData struct {
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turns_used"`
}

// PluginLoadedData is the payload for an EventPluginLoaded event.
type PluginLoadedData struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	SkillCount int    `json:"skill_count"`
	AgentCount int    `json:"agent_count"`
	MCPCount   int    `json:"mcp_count"`
}

// HookStartData is the payload for an EventHookStart event.
type HookStartData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
}

// HookEndData is the payload for an EventHookEnd event.
type HookEndData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}

// ForkSummaryData is the payload for an EventForkSummary event.
type ForkSummaryData struct {
	Turn int `json:"turn"`
}

// PromptLoadedData is the payload for an EventPromptLoaded event.
type PromptLoadedData struct {
	Label string `json:"label"`
	Size  int    `json:"size"`
}

// GoalContinuationData is the payload for an EventGoalContinuation event. Text is
// a compact human-facing marker for the continuation (e.g. "Continuing toward:
// <objective>"), NOT the full rendered continuation prompt — that scaffolding goes
// to the model via the steering turn and is kept out of the UI projection.
type GoalContinuationData struct {
	Text string `json:"text"`
}

// GoalEndedData is the payload for an EventGoalEnded event. It reports the
// terminal goal status, the reason the loop stopped, and how many continuation
// turns were taken.
type GoalEndedData struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Iterations int    `json:"iterations"`
}
