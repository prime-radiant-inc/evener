//go:build serffuzz

package events

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// FuzzEventDataProgram runs every member of the sealed event payload set
// through the public envelope constructor. The variant data makes the payload
// fields non-constant while the table ensures a seed exercises every payload
// marker, including agent-only event kinds.
func FuzzEventDataProgram(f *testing.F) {
	f.Add("event text", "session-1", 7, true)
	f.Add("", "", 0, false)

	f.Fuzz(func(t *testing.T, text, sessionID string, n int, flag bool) {
		for _, tc := range eventDataProgramCases(text, n, flag) {
			ev := New(tc.data)
			ev.SessionID = sessionID
			if ev.Kind != tc.kind {
				t.Fatalf("New(%T).Kind = %q, want %q", tc.data, ev.Kind, tc.kind)
			}
			if reflect.TypeOf(ev.Data) != reflect.TypeOf(tc.data) {
				t.Fatalf("New(%T).Data type = %T, want %T", tc.data, ev.Data, tc.data)
			}
			if ev.Timestamp.IsZero() || ev.Timestamp.Location() != time.UTC {
				t.Fatalf("New(%T).Timestamp = %v, want non-zero UTC", tc.data, ev.Timestamp)
			}
			if _, err := json.Marshal(ev); err != nil {
				t.Fatalf("json.Marshal(SessionEvent{%T}): %v", tc.data, err)
			}
		}
		assertEventDataProgramStreamCases(t, text)
	})
}

func assertEventDataProgramStreamCases(t *testing.T, text string) {
	t.Helper()
	cases := []struct {
		event SessionEvent
		want  *llm.StreamEvent
	}{
		{SessionEvent{Kind: EventSessionStart, Data: SessionStartData{}}, &llm.StreamEvent{Type: llm.StreamEventStreamStart}},
		{SessionEvent{Kind: EventSessionEnd, Data: SessionEndData{}}, &llm.StreamEvent{Type: llm.StreamEventFinish}},
		{SessionEvent{Kind: EventAssistantTextStart, Data: AssistantTextStartData{}}, &llm.StreamEvent{Type: llm.StreamEventTextStart}},
		{SessionEvent{Kind: EventAssistantTextDelta, Data: AssistantTextDeltaData{Delta: text}}, &llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: text}},
		{SessionEvent{Kind: EventAssistantTextEnd, Data: AssistantTextEndData{}}, &llm.StreamEvent{Type: llm.StreamEventTextEnd}},
		{SessionEvent{Kind: EventToolCallStart, Data: ToolCallStartData{CallID: text, ToolName: text}}, &llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: text, Name: text}}},
		{SessionEvent{Kind: EventToolCallEnd, Data: ToolCallEndData{CallID: text, ToolName: text}}, &llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: text, Name: text}}},
		{SessionEvent{Kind: EventAssistantTextDelta, Data: ToolCallStartData{}}, nil},
		{SessionEvent{Kind: EventToolCallStart, Data: AssistantTextDeltaData{}}, nil},
		{SessionEvent{Kind: EventToolCallEnd, Data: AssistantTextDeltaData{}}, nil},
		{SessionEvent{Kind: EventWarning, Data: WarningData{}}, nil},
	}
	for _, tc := range cases {
		got := tc.event.ToStreamEvent()
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ToStreamEvent(%q, %T) = %#v, want %#v", tc.event.Kind, tc.event.Data, got, tc.want)
		}
	}
}

type eventDataProgramCase struct {
	data EventData
	kind EventKind
}

func eventDataProgramCases(text string, n int, flag bool) []eventDataProgramCase {
	exitCode := n
	return []eventDataProgramCase{
		{SessionStartData{Profile: text, Model: text, Restored: flag, Turns: n}, EventSessionStart},
		{SessionEndData{Reason: text, State: text, Turns: n, Interrupted: flag}, EventSessionEnd},
		{UserInputData{Text: text, Turn: n}, EventUserInput},
		{AssistantTextStartData{Model: text}, EventAssistantTextStart},
		{AssistantTextDeltaData{Delta: text}, EventAssistantTextDelta},
		{AssistantTextEndData{Text: text, FinishReason: text, Model: text}, EventAssistantTextEnd},
		{AssistantTextResetData{}, EventAssistantTextReset},
		{ReasoningSummaryDeltaData{Delta: text, SummaryIndex: n}, EventReasoningSummaryDelta},
		{ToolCallStartData{ToolName: text, CallID: text, ArgumentsJSON: text}, EventToolCallStart},
		{ToolCallOutputDeltaData{ToolName: text, CallID: text, Delta: text}, EventToolCallOutputDelta},
		{ToolCallEndData{ToolName: text, CallID: text, Output: text, Error: text}, EventToolCallEnd},
		{ToolCallRepairedData{ToolName: text, CallID: text, Changes: []string{text}}, EventToolCallRepaired},
		{SteeringInjectedData{Text: text}, EventSteeringInjected},
		{QueueChangedData{Depth: n, Preview: []string{text}}, EventQueueChanged},
		{TaskUpdatedData{Total: n, Done: n}, EventTaskUpdated},
		{SessionNameChangedData{Name: text, Source: text}, EventSessionNameChanged},
		{ModelChangedData{OldProvider: text, OldModel: text, NewProvider: text, NewModel: text, ReasoningEffortLevels: []string{text}, SupportsReasoning: flag}, EventModelChanged},
		{ReasoningEffortChangedData{ReasoningEffort: text}, EventReasoningEffortChanged},
		{TurnLimitData{MaxTurns: n, MaxToolRoundsPerInput: n}, EventTurnLimit},
		{LoopDetectionData{Message: text}, EventLoopDetection},
		{CommunicateData{EndTurn: flag, Message: text}, EventCommunicate},
		{SkillActivatedData{Name: text}, EventSkillActivated},
		{ContextCompactionData{Layer: text, TurnsBefore: n, TurnsAfter: n}, EventContextCompaction},
		{CompactionTurnData{Kind: text, Text: text}, EventCompactionTurn},
		{WarningData{Message: text, Source: text, ApproxTokens: n}, EventWarning},
		{ErrorData{Error: text, Source: text, Cause: &ErrorCause{Kind: text, Status: n}}, EventError},
		{JobStartedData{JobID: text, JobType: text, Status: text, Background: flag}, EventJobStarted},
		{JobFinishedData{JobID: text, JobType: text, Status: text, Reason: text, ExitCode: &exitCode}, EventJobFinished},
		{PluginLoadedData{Name: text, Dir: text, SkillCount: n}, EventPluginLoaded},
		{HookStartData{Event: text, HookType: text, Matcher: text, PluginName: text}, EventHookStart},
		{HookEndData{Event: text, HookType: text, Matcher: text, PluginName: text, ExitCode: n, DurationMS: int64(n)}, EventHookEnd},
		{ForkSummaryData{Turn: n}, EventForkSummary},
		{PromptLoadedData{Label: text, Size: n}, EventPromptLoaded},
		{RoundTimings{Round: n}, EventRoundTimings},
		{TurnEndedData{TurnDurationMS: int64(n)}, EventTurnEnded},
		{GoalContinuationData{Text: text}, EventGoalContinuation},
		{GoalEndedData{Status: text, Reason: text, Iterations: n}, EventGoalEnded},
		{SandboxEscalationRequestedData{EscalationID: text, Mode: text, Tool: text, PartiallyRan: flag}, EventSandboxEscalationRequested},
	}
}
