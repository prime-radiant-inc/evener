// Package events defines the public session event stream: the [EventKind]
// taxonomy, the [SessionEvent] envelope, and the typed payload structs carried
// on a session's event channel. Consumers (projectors, CLI renderers, the
// server bridge) read these types directly; the agent package re-exports the
// whole cluster via type aliases for in-package use and source compatibility.
package events

import (
	"encoding/json"
	"time"

	"primeradiant.com/serf/llm"
)

// EventKind identifies the type of a SessionEvent.
type EventKind string

// Event kinds emitted on a session's event stream.
const (
	// EventSessionStart marks the start of a session.
	EventSessionStart EventKind = "SESSION_START"
	// EventSessionEnd marks the end of a session.
	EventSessionEnd EventKind = "SESSION_END"
	// EventUserInput carries a user input turn.
	EventUserInput EventKind = "USER_INPUT"
	// EventAssistantTextStart marks the start of an assistant text response.
	EventAssistantTextStart EventKind = "ASSISTANT_TEXT_START"
	// EventAssistantTextDelta carries an incremental chunk of assistant text.
	EventAssistantTextDelta EventKind = "ASSISTANT_TEXT_DELTA"
	// EventAssistantTextEnd marks the end of an assistant text response.
	EventAssistantTextEnd EventKind = "ASSISTANT_TEXT_END"
	// EventToolCallStart marks the start of a tool call.
	EventToolCallStart EventKind = "TOOL_CALL_START"
	// EventToolCallOutputDelta carries an incremental chunk of tool call output.
	EventToolCallOutputDelta EventKind = "TOOL_CALL_OUTPUT_DELTA"
	// EventToolCallEnd marks the end of a tool call.
	EventToolCallEnd EventKind = "TOOL_CALL_END"
	// EventSteeringInjected marks steering input injected into the session.
	EventSteeringInjected EventKind = "STEERING_INJECTED"
	// EventQueueChanged reports a change to the session's input queue.
	EventQueueChanged EventKind = "QUEUE_CHANGED"
	// EventTurnLimit reports turn or tool-round limits.
	EventTurnLimit EventKind = "TURN_LIMIT"
	// EventLoopDetection reports detection of a loop.
	EventLoopDetection EventKind = "LOOP_DETECTION"
	// EventCommunicate carries a communicate message.
	EventCommunicate EventKind = "COMMUNICATE"
	// EventSkillActivated reports activation of a skill.
	EventSkillActivated EventKind = "SKILL_ACTIVATED"
	// EventContextCompaction reports a context compaction.
	EventContextCompaction EventKind = "CONTEXT_COMPACTION"
	// EventCompactionTurn carries a turn produced by compaction.
	EventCompactionTurn EventKind = "COMPACTION_TURN"
	// EventWarning carries a warning.
	EventWarning EventKind = "WARNING"
	// EventError carries an error.
	EventError EventKind = "ERROR"
	// EventSubagentStart marks the start of a subagent.
	EventSubagentStart EventKind = "SUBAGENT_START"
	// EventSubagentEnd marks the end of a subagent.
	EventSubagentEnd EventKind = "SUBAGENT_END"
	// EventPluginLoaded reports that a plugin was loaded.
	EventPluginLoaded EventKind = "PLUGIN_LOADED"
	// EventHookStart marks the start of a hook execution.
	EventHookStart EventKind = "HOOK_START"
	// EventHookEnd marks the end of a hook execution.
	EventHookEnd EventKind = "HOOK_END"
	// EventForkSummary carries a fork summary.
	EventForkSummary EventKind = "FORK_SUMMARY"
	// EventPromptLoaded reports that a prompt was loaded.
	EventPromptLoaded EventKind = "PROMPT_LOADED"
	// EventRoundTimings carries round timing information.
	EventRoundTimings EventKind = "ROUND_TIMINGS"
)

// SessionEvent is a single timestamped event on a session's event stream,
// tagged by Kind and carrying a typed Data payload. Data is restricted to the
// sealed EventData set, so only this package's payload types can ride the
// stream and Kind is always consistent with the payload (see New).
type SessionEvent struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	Data      EventData `json:"data,omitempty"`
}

// DataMap returns Data as a map[string]any by marshaling the typed payload
// through JSON. Returns nil when Data is nil.
func (e SessionEvent) DataMap() map[string]any {
	if e.Data == nil {
		return nil
	}
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
