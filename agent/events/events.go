// Package events defines the public session event stream: the [EventKind]
// taxonomy, the [SessionEvent] envelope, and the typed payload structs carried
// on a session's event channel. Consumers (the agent package, projectors, CLI
// renderers, the server bridge) import this package and use these types
// directly.
package events

import (
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
	// EventAssistantTextReset discards the in-progress assistant message: a model
	// call retried after streaming partial output emits this so the retry's
	// output replaces, rather than appends to, the partial that was shown.
	EventAssistantTextReset EventKind = "ASSISTANT_TEXT_RESET"
	// EventReasoningSummaryDelta carries an incremental chunk of the model's
	// reasoning summary (the readable "thinking" stream hosted models emit).
	EventReasoningSummaryDelta EventKind = "REASONING_SUMMARY_DELTA"
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
	// EventJobStarted marks the start of a job.
	EventJobStarted EventKind = "JOB_STARTED"
	// EventJobFinished marks the end of a job.
	EventJobFinished EventKind = "JOB_FINISHED"
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
	// EventGoalContinuation marks a system-framed continuation turn injected by
	// the goal engine.
	EventGoalContinuation EventKind = "GOAL_CONTINUATION"
	// EventGoalEnded reports that the goal engine stopped, carrying the terminal
	// status and reason.
	EventGoalEnded EventKind = "GOAL_ENDED"
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
