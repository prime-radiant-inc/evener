package atif

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestConvertToATIF_RoundTimingsPreservesStructuredMetadata(t *testing.T) {
	payload := &schema.RoundTimings{
		Round: 7, SystemPrompt: time.Nanosecond, ContextMgmt: 2 * time.Nanosecond,
		HistoryExpand: 3 * time.Nanosecond, ToolDefs: 4 * time.Nanosecond,
		LLMCall: 5 * time.Nanosecond, ToolExec: 6 * time.Nanosecond,
		Persistence: 7 * time.Nanosecond, AfterAction: 8 * time.Nanosecond,
		LoopOverhead: 9 * time.Nanosecond, TotalRound: 10 * time.Nanosecond,
	}
	turn := schema.NewTurn(schema.TurnRoundTimings, llm.System(payload.Announcement()))
	turn.RoundTimings = payload
	traj := Convert(transcript.Header{SessionID: "session"}, []transcript.Entry{{Turn: turn}})
	if len(traj.Steps) != 1 {
		t.Fatalf("steps = %d, want one timing step", len(traj.Steps))
	}
	if _, ok := traj.Steps[0].Extra["round_timings"].(*schema.RoundTimings); !ok {
		t.Fatalf("round_timings metadata = %#v, want structured payload", traj.Steps[0].Extra["round_timings"])
	}
	blob, err := json.Marshal(traj)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Trajectory
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	raw, ok := decoded.Steps[0].Extra["round_timings"].(map[string]any)
	if !ok {
		t.Fatalf("serialized round_timings = %#v, want structured duration fields", decoded.Steps[0].Extra["round_timings"])
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got schema.RoundTimings
	if err := json.Unmarshal(rawJSON, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, *payload) {
		t.Fatalf("serialized round_timings = %#v, want all fields %#v", got, *payload)
	}
}

func TestConvertToATIF_RoundTimingsDoesNotBreakToolObservationPair(t *testing.T) {
	assistant := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-1", Name: "lookup", Arguments: []byte(`{}`)}}}}}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System("timing"))
	timing.RoundTimings = &schema.RoundTimings{Round: 1}
	result := schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "observation-sentinel", false))
	traj := Convert(transcript.Header{SessionID: "session"}, []transcript.Entry{{Turn: assistant}, {Turn: timing}, {Turn: result}})
	if len(traj.Steps) != 2 || traj.Steps[0].Observation == nil || len(traj.Steps[0].Observation.Results) != 1 || traj.Steps[0].Observation.Results[0].Content != "observation-sentinel" {
		t.Fatalf("steps = %#v, want assistant observation paired across timing marker", traj.Steps)
	}
	if traj.Steps[1].Extra["round_timings"] == nil {
		t.Fatalf("timing step metadata = %#v, want preserved interleaved timing", traj.Steps[1].Extra)
	}
}

func TestConvertToATIF_ContextRecoveryCopiesDoNotDuplicateStepsOrUsage(t *testing.T) {
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer-sentinel"))
	assistant.Usage = llm.Usage{InputTokens: 7, OutputTokens: 3}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System("timing-sentinel"))
	timing.RoundTimings = &schema.RoundTimings{Round: 1}
	header := transcript.Header{SessionID: "session"}
	entries := []transcript.Entry{{Turn: assistant}, {Turn: timing}}
	want := Convert(header, entries)
	for _, entry := range entries {
		entry.Turn.ContextReplay = true
		entries = append(entries, entry)
	}
	if got := Convert(header, entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery copies changed the trajectory: steps=%d, want %d", len(got.Steps), len(want.Steps))
	}
}
