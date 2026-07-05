package events_test

import (
	"encoding/json"
	"reflect"
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

	// Parse the envelope as a flat map to verify structural placement.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	// Provenance must be a non-null top-level key on the envelope.
	provRaw, ok := envelope["provenance"]
	if !ok {
		t.Fatalf("envelope missing top-level \"provenance\" key: %s", b)
	}
	if string(provRaw) == "null" {
		t.Fatalf("envelope \"provenance\" is null: %s", b)
	}

	// Verify the expected provenance fields appear inside the provenance JSON.
	ps := string(provRaw)
	for _, want := range []string{`"watch_id":"watch_A"`, `"watch_generation":"wg_1"`, `"delivery_id":"wd_1"`} {
		if !strings.Contains(ps, want) {
			t.Errorf("provenance JSON missing %s: %s", want, ps)
		}
	}

	// Data must NOT contain a "provenance" sub-key: provenance belongs on the
	// envelope, not the payload.
	dataRaw, ok := envelope["data"]
	if !ok {
		t.Fatalf("envelope missing \"data\" key: %s", b)
	}
	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(dataRaw, &dataMap); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if _, hasProv := dataMap["provenance"]; hasProv {
		t.Fatalf("provenance must live on event envelope, not payload: %s", b)
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
			if !reflect.DeepEqual(ev.Data, tt.data) {
				t.Errorf("New(%T).Data = %+v, want %+v", tt.data, ev.Data, tt.data)
			}
			if ev.Timestamp.IsZero() {
				t.Errorf("New(%T).Timestamp is zero", tt.data)
			}
		})
	}
}

func TestEventKindWireStrings(t *testing.T) {
	// Verify that EventKind constants equal their wire strings. An accidental
	// rename (e.g. "SESSION_START" → "SESSION_STARTED") must fail here rather
	// than silently breaking JSON consumers.
	tests := []struct {
		kind events.EventKind
		want string
	}{
		{events.EventSessionStart, "SESSION_START"},
		{events.EventSessionEnd, "SESSION_END"},
		{events.EventUserInput, "USER_INPUT"},
		{events.EventAssistantTextStart, "ASSISTANT_TEXT_START"},
		{events.EventAssistantTextDelta, "ASSISTANT_TEXT_DELTA"},
		{events.EventAssistantTextEnd, "ASSISTANT_TEXT_END"},
		{events.EventCommunicate, "COMMUNICATE"},
		{events.EventWarning, "WARNING"},
		{events.EventError, "ERROR"},
	}
	for _, tt := range tests {
		if string(tt.kind) != tt.want {
			t.Errorf("EventKind %q has wire string %q, want %q", tt.kind, string(tt.kind), tt.want)
		}
	}

	// Confirm the wire string appears literally in marshaled JSON.
	ev := events.New(events.SessionStartData{Profile: "p", Model: "m"})
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal SessionStart event: %v", err)
	}
	if want := `"kind":"SESSION_START"`; !strings.Contains(string(b), want) {
		t.Errorf("marshaled SessionStart event missing %s: %s", want, b)
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

func TestToolCallRepairedData_Kind(t *testing.T) {
	ev := events.New(events.ToolCallRepairedData{ToolName: "edit_file", CallID: "c1", Changes: []string{"alias:old_string:old_str→old_string"}})
	if ev.Kind != events.EventToolCallRepaired {
		t.Fatalf("kind = %s", ev.Kind)
	}
	d, ok := ev.Data.(events.ToolCallRepairedData)
	if !ok || d.ToolName != "edit_file" || len(d.Changes) != 1 {
		t.Fatalf("data = %+v", ev.Data)
	}
}

func TestTurnEnded_KindAndPayload(t *testing.T) {
	ev := events.New(events.TurnEndedData{TurnDurationMS: 1234})
	if ev.Kind != events.EventTurnEnded {
		t.Errorf("New(TurnEndedData).Kind = %q, want %q", ev.Kind, events.EventTurnEnded)
	}

	// Verify the payload round-trips correctly
	data, ok := ev.Data.(events.TurnEndedData)
	if !ok {
		t.Fatalf("ev.Data is %T, want TurnEndedData", ev.Data)
	}
	if data.TurnDurationMS != 1234 {
		t.Errorf("data.TurnDurationMS = %d, want 1234", data.TurnDurationMS)
	}

	// Verify JSON marshaling includes the payload field
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if !strings.Contains(string(b), `"turn_duration_ms":1234`) {
		t.Errorf("marshaled event missing turn_duration_ms: %s", b)
	}
}
