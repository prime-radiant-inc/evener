package agent

import (
	"encoding/json"
	"time"

	"primeradiant.com/serf/llm"
)

type EventKind string

const (
	EventSessionStart        EventKind = "SESSION_START"
	EventSessionEnd          EventKind = "SESSION_END"
	EventUserInput           EventKind = "USER_INPUT"
	EventAssistantTextStart  EventKind = "ASSISTANT_TEXT_START"
	EventAssistantTextDelta  EventKind = "ASSISTANT_TEXT_DELTA"
	EventAssistantTextEnd    EventKind = "ASSISTANT_TEXT_END"
	EventToolCallStart       EventKind = "TOOL_CALL_START"
	EventToolCallOutputDelta EventKind = "TOOL_CALL_OUTPUT_DELTA"
	EventToolCallEnd         EventKind = "TOOL_CALL_END"
	EventSteeringInjected    EventKind = "STEERING_INJECTED"
	EventQueueChanged        EventKind = "QUEUE_CHANGED"
	EventTurnLimit           EventKind = "TURN_LIMIT"
	EventLoopDetection       EventKind = "LOOP_DETECTION"
	EventCommunicate         EventKind = "COMMUNICATE"
	EventSkillActivated      EventKind = "SKILL_ACTIVATED"
	EventContextCompaction   EventKind = "CONTEXT_COMPACTION"
	EventCompactionTurn      EventKind = "COMPACTION_TURN"
	EventWarning             EventKind = "WARNING"
	EventError               EventKind = "ERROR"
	EventSubagentStart       EventKind = "SUBAGENT_START"
	EventSubagentEnd         EventKind = "SUBAGENT_END"
	EventPluginLoaded        EventKind = "PLUGIN_LOADED"
	EventHookStart           EventKind = "HOOK_START"
	EventHookEnd             EventKind = "HOOK_END"
	EventForkSummary         EventKind = "FORK_SUMMARY"
	EventPromptLoaded        EventKind = "PROMPT_LOADED"
	EventRoundTimings        EventKind = "ROUND_TIMINGS"
)

type SessionEvent struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	Data      any       `json:"data,omitempty"`
}

// DataMap returns Data as map[string]any for backward compatibility.
func (e SessionEvent) DataMap() map[string]any {
	if e.Data == nil {
		return nil
	}
	if m, ok := e.Data.(map[string]any); ok {
		return m
	}
	// For typed structs, marshal/unmarshal through JSON.
	b, err := json.Marshal(e.Data)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ToStreamEvent maps this agent-level event to an llm.StreamEvent.
// Returns nil for agent-only events that have no LLM-layer equivalent.
func (e SessionEvent) ToStreamEvent() *llm.StreamEvent {
	switch e.Kind {
	case EventAssistantTextStart:
		return &llm.StreamEvent{Type: llm.StreamEventTextStart}
	case EventAssistantTextDelta:
		if d, ok := e.Data.(AssistantTextDeltaData); ok {
			return &llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: d.Delta}
		}
	case EventAssistantTextEnd:
		return &llm.StreamEvent{Type: llm.StreamEventTextEnd}
	case EventToolCallStart:
		if d, ok := e.Data.(ToolCallStartData); ok {
			return &llm.StreamEvent{
				Type:     llm.StreamEventToolCallStart,
				ToolCall: &llm.ToolCallData{ID: d.CallID, Name: d.ToolName},
			}
		}
	case EventToolCallEnd:
		if d, ok := e.Data.(ToolCallEndData); ok {
			return &llm.StreamEvent{
				Type:     llm.StreamEventToolCallEnd,
				ToolCall: &llm.ToolCallData{ID: d.CallID, Name: d.ToolName},
			}
		}
	case EventSessionStart:
		return &llm.StreamEvent{Type: llm.StreamEventStreamStart}
	case EventSessionEnd:
		return &llm.StreamEvent{Type: llm.StreamEventFinish}
	}
	return nil // Agent-only events don't map to StreamEvent.
}

// Typed event payload structs. JSON tags match the map keys used previously.

type SessionStartData struct {
	Profile           string `json:"profile"`
	Model             string `json:"model"`
	Restored          bool   `json:"restored,omitempty"`
	Turns             int    `json:"turns,omitempty"`
	LastInputTokens   int    `json:"last_input_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
}

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

type AssistantTextStartData struct {
	Model string `json:"model"`
}

type AssistantTextDeltaData struct {
	Delta string `json:"delta"`
}

type AssistantTextEndData struct {
	Text         string `json:"text"`
	Usage        any    `json:"usage,omitempty"`
	FinishReason string `json:"finish_reason"`
	Model        string `json:"model"`
	Reasoning    string `json:"reasoning,omitempty"`
}

type ToolCallStartData struct {
	ToolName      string `json:"tool_name"`
	CallID        string `json:"call_id"`
	ArgumentsJSON string `json:"arguments_json,omitempty"`
	Description   string `json:"description,omitempty"`
}

type ToolCallOutputDeltaData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Delta    string `json:"delta"`
}

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

type TurnLimitData struct {
	MaxTurns              int `json:"max_turns,omitempty"`
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
}

type LoopDetectionData struct {
	Message string `json:"message"`
}

type CommunicateData struct {
	AwaitReply bool   `json:"await_reply"`
	Message    string `json:"message"`
}

type SkillActivatedData struct {
	Name string `json:"name"`
}

type ContextCompactionData struct {
	Layer           string `json:"layer,omitempty"`
	TurnsBefore     int    `json:"turns_before,omitempty"`
	TurnsAfter      int    `json:"turns_after,omitempty"`
	EstTokensBefore int    `json:"est_tokens_before,omitempty"`
	EstTokensAfter  int    `json:"est_tokens_after,omitempty"`
}

type CompactionTurnData struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type WarningData struct {
	Message           string `json:"message"`
	Source            string `json:"source,omitempty"`
	Title             string `json:"title,omitempty"`
	Hint              string `json:"hint,omitempty"`
	ApproxTokens      int    `json:"approx_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
	Percent           int    `json:"percent,omitempty"`
}

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

type SubagentStartData struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
}

type SubagentEndData struct {
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turns_used"`
}

type PluginLoadedData struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	SkillCount int    `json:"skill_count"`
	AgentCount int    `json:"agent_count"`
	MCPCount   int    `json:"mcp_count"`
}

type HookStartData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
}

type HookEndData struct {
	Event      string `json:"event"`
	HookType   string `json:"hook_type"`
	Matcher    string `json:"matcher"`
	PluginName string `json:"plugin_name"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}

type ForkSummaryData struct {
	Turn int `json:"turn"`
}

type PromptLoadedData struct {
	Label string `json:"label"`
	Size  int    `json:"size"`
}
