package events_test

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
)

func TestSessionEventCarriesCausalProvenanceOnEnvelope(t *testing.T) {
	ev := events.New(events.CommunicateData{EndTurn: false, Message: "actually alpha marker"})
	ev.SessionID = "session_1"
	ev.Provenance = provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"provenance"`, `"watch_id":"watch_A"`, `"watch_generation":"wg_1"`, `"delivery_id":"wd_1"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("event JSON missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"data":{"provenance"`) {
		t.Fatalf("provenance must live on event envelope, not payload: %s", s)
	}
}

func TestNewDerivesCorrectKind(t *testing.T) {
	tests := []struct {
		name     string
		data     events.EventData
		wantKind events.EventKind
	}{
		{"SessionStart", events.SessionStartData{Profile: "test", Model: "m"}, events.EventSessionStart},
		{"SessionEnd", events.SessionEndData{Reason: "done"}, events.EventSessionEnd},
		{"UserInput", events.UserInputData{Text: "hello"}, events.EventUserInput},
		{"AssistantTextStart", events.AssistantTextStartData{Model: "m"}, events.EventAssistantTextStart},
		{"AssistantTextDelta", events.AssistantTextDeltaData{Delta: "hi"}, events.EventAssistantTextDelta},
		{"AssistantTextEnd", events.AssistantTextEndData{Text: "done", Model: "m"}, events.EventAssistantTextEnd},
		{"AssistantTextReset", events.AssistantTextResetData{}, events.EventAssistantTextReset},
		{"ReasoningSummaryDelta", events.ReasoningSummaryDeltaData{Delta: "think", SummaryIndex: 1}, events.EventReasoningSummaryDelta},
		{"ToolCallStart", events.ToolCallStartData{ToolName: "t", CallID: "c"}, events.EventToolCallStart},
		{"ToolCallOutputDelta", events.ToolCallOutputDeltaData{ToolName: "t", CallID: "c", Delta: "d"}, events.EventToolCallOutputDelta},
		{"ToolCallEnd", events.ToolCallEndData{ToolName: "t", CallID: "c"}, events.EventToolCallEnd},
		{"SteeringInjected", events.SteeringInjectedData{Text: "go"}, events.EventSteeringInjected},
		{"QueueChanged", events.QueueChangedData{Depth: 2}, events.EventQueueChanged},
		{"TurnLimit", events.TurnLimitData{MaxTurns: 10}, events.EventTurnLimit},
		{"LoopDetection", events.LoopDetectionData{Message: "loop"}, events.EventLoopDetection},
		{"Communicate", events.CommunicateData{EndTurn: false, Message: "msg"}, events.EventCommunicate},
		{"SkillActivated", events.SkillActivatedData{Name: "skill"}, events.EventSkillActivated},
		{"ContextCompaction", events.ContextCompactionData{Layer: "layer"}, events.EventContextCompaction},
		{"CompactionTurn", events.CompactionTurnData{Kind: "k", Text: "t"}, events.EventCompactionTurn},
		{"Warning", events.WarningData{Message: "warn"}, events.EventWarning},
		{"Error", events.ErrorData{Error: "err"}, events.EventError},
		{"JobStarted", events.JobStartedData{JobID: "j", JobType: "t"}, events.EventJobStarted},
		{"JobFinished", events.JobFinishedData{JobID: "j", JobType: "t", Status: "done", Reason: "r"}, events.EventJobFinished},
		{"PluginLoaded", events.PluginLoadedData{Name: "p", Dir: "/p"}, events.EventPluginLoaded},
		{"HookStart", events.HookStartData{Event: "e", HookType: "t", Matcher: "m", PluginName: "p"}, events.EventHookStart},
		{"HookEnd", events.HookEndData{Event: "e", HookType: "t", Matcher: "m", PluginName: "p", ExitCode: 0}, events.EventHookEnd},
		{"ForkSummary", events.ForkSummaryData{Turn: 1}, events.EventForkSummary},
		{"PromptLoaded", events.PromptLoadedData{Label: "l", Size: 1}, events.EventPromptLoaded},
		{"RoundTimings", events.RoundTimings{Round: 1}, events.EventRoundTimings},
		{"GoalContinuation", events.GoalContinuationData{Text: "continue"}, events.EventGoalContinuation},
		{"GoalEnded", events.GoalEndedData{Status: "done", Iterations: 1}, events.EventGoalEnded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := events.New(tt.data)
			if ev.Kind != tt.wantKind {
				t.Errorf("New(%T).Kind = %q, want %q", tt.data, ev.Kind, tt.wantKind)
			}
			if ev.Data == nil {
				t.Errorf("New(%T).Data = nil, want non-nil", tt.data)
			}
			if ev.Timestamp.IsZero() {
				t.Errorf("New(%T).Timestamp is zero", tt.data)
			}
		})
	}
}

func TestToStreamEvent_Mappings(t *testing.T) {
	tests := []struct {
		name      string
		event     events.SessionEvent
		wantType  llm.StreamEventType
		wantDelta string
		wantTool  *llm.ToolCallData
	}{
		{
			name:     "AssistantTextStart",
			event:    events.New(events.AssistantTextStartData{Model: "m"}),
			wantType: llm.StreamEventTextStart,
		},
		{
			name:      "AssistantTextDelta",
			event:     events.New(events.AssistantTextDeltaData{Delta: "hello"}),
			wantType:  llm.StreamEventTextDelta,
			wantDelta: "hello",
		},
		{
			name:     "AssistantTextEnd",
			event:    events.New(events.AssistantTextEndData{Text: "done", Model: "m"}),
			wantType: llm.StreamEventTextEnd,
		},
		{
			name:     "ToolCallStart",
			event:    events.New(events.ToolCallStartData{ToolName: "read_file", CallID: "call-1"}),
			wantType: llm.StreamEventToolCallStart,
			wantTool: &llm.ToolCallData{ID: "call-1", Name: "read_file"},
		},
		{
			name:     "ToolCallEnd",
			event:    events.New(events.ToolCallEndData{ToolName: "read_file", CallID: "call-1"}),
			wantType: llm.StreamEventToolCallEnd,
			wantTool: &llm.ToolCallData{ID: "call-1", Name: "read_file"},
		},
		{
			name:     "SessionStart",
			event:    events.New(events.SessionStartData{Profile: "p", Model: "m"}),
			wantType: llm.StreamEventStreamStart,
		},
		{
			name:     "SessionEnd",
			event:    events.New(events.SessionEndData{Reason: "done"}),
			wantType: llm.StreamEventFinish,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.event.ToStreamEvent()
			if got == nil {
				t.Fatalf("ToStreamEvent() = nil, want non-nil")
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
			if tt.wantDelta != "" {
				if got.Delta != tt.wantDelta {
					t.Errorf("Delta = %q, want %q", got.Delta, tt.wantDelta)
				}
			} else if got.Delta != "" {
				t.Errorf("Delta = %q, want empty", got.Delta)
			}
			if tt.wantTool != nil {
				if got.ToolCall == nil {
					t.Fatalf("ToolCall = nil, want non-nil")
				}
				if got.ToolCall.ID != tt.wantTool.ID || got.ToolCall.Name != tt.wantTool.Name {
					t.Errorf("ToolCall = %+v, want %+v", got.ToolCall, tt.wantTool)
				}
			} else if got.ToolCall != nil {
				t.Errorf("ToolCall = %+v, want nil", got.ToolCall)
			}
		})
	}
}

func TestToStreamEvent_AgentOnlyEventsReturnNil(t *testing.T) {
	agentOnly := []events.EventData{
		events.UserInputData{Text: "hi"},
		events.AssistantTextResetData{},
		events.ReasoningSummaryDeltaData{Delta: "d"},
		events.ToolCallOutputDeltaData{Delta: "d"},
		events.SteeringInjectedData{Text: "s"},
		events.QueueChangedData{Depth: 1},
		events.TurnLimitData{},
		events.LoopDetectionData{Message: "m"},
		events.CommunicateData{Message: "m"},
		events.SkillActivatedData{Name: "n"},
		events.ContextCompactionData{},
		events.CompactionTurnData{},
		events.WarningData{Message: "w"},
		events.ErrorData{Error: "e"},
		events.JobStartedData{JobID: "j"},
		events.JobFinishedData{JobID: "j"},
		events.PluginLoadedData{Name: "p"},
		events.HookStartData{},
		events.HookEndData{},
		events.ForkSummaryData{},
		events.PromptLoadedData{},
		events.RoundTimings{},
		events.GoalContinuationData{},
		events.GoalEndedData{},
	}
	for _, data := range agentOnly {
		t.Run(string(events.New(data).Kind), func(t *testing.T) {
			ev := events.New(data)
			if got := ev.ToStreamEvent(); got != nil {
				t.Errorf("ToStreamEvent() = %+v, want nil", got)
			}
		})
	}
}

func TestToStreamEvent_WrongPayloadReturnsNil(t *testing.T) {
	// EventAssistantTextDelta expects AssistantTextDeltaData, not SessionStartData
	ev := events.SessionEvent{
		Kind: events.EventAssistantTextDelta,
		Data: events.SessionStartData{Profile: "p"},
	}
	if got := ev.ToStreamEvent(); got != nil {
		t.Errorf("ToStreamEvent() = %+v, want nil", got)
	}

	// EventToolCallStart expects ToolCallStartData, not ToolCallEndData
	ev = events.SessionEvent{
		Kind: events.EventToolCallStart,
		Data: events.ToolCallEndData{ToolName: "t", CallID: "c"},
	}
	if got := ev.ToStreamEvent(); got != nil {
		t.Errorf("ToStreamEvent() = %+v, want nil", got)
	}

	// EventToolCallEnd expects ToolCallEndData, not ToolCallStartData
	ev = events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: events.ToolCallStartData{ToolName: "t", CallID: "c"},
	}
	if got := ev.ToStreamEvent(); got != nil {
		t.Errorf("ToStreamEvent() = %+v, want nil", got)
	}
}
