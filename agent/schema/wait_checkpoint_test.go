package schema_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func TestWaitCheckpointRoundTrip(t *testing.T) {
	t.Parallel()
	checkpoint := schema.WaitCheckpointInfo{
		SessionID: "session-1", DelegateID: "delegate-1", Generation: 7,
		ExecutionTurnID: "execution-1", CommunicateCallID: "call-1",
		WaitingOn: "job-1", ResumedByTurnID: "execution-2",
	}
	turn := schema.Turn{Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint}
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Kind       string         `json:"kind"`
		Checkpoint map[string]any `json:"wait_checkpoint"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"session_id": "session-1", "delegate_id": "delegate-1", "generation": float64(7),
		"execution_turn_id": "execution-1", "communicate_call_id": "call-1",
		"waiting_on": "job-1", "resumed_by_turn_id": "execution-2",
	}
	if raw.Kind != "WAIT_CHECKPOINT" || !reflect.DeepEqual(raw.Checkpoint, want) {
		t.Fatalf("wire record = %+v, want WAIT_CHECKPOINT with %+v", raw, want)
	}
	var decoded schema.Turn
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != turn.Kind || !reflect.DeepEqual(decoded.WaitCheckpoint, &checkpoint) {
		t.Fatalf("decoded checkpoint = %+v", decoded)
	}
	if !decoded.Kind.TranscriptOnly() || decoded.Kind.PublicTranscript() {
		t.Fatal("wait checkpoint must remain transcript-only")
	}

	minimal, err := json.Marshal(schema.WaitCheckpointInfo{SessionID: "s", ExecutionTurnID: "e", CommunicateCallID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(minimal, &fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields, map[string]any{"session_id": "s", "execution_turn_id": "e", "communicate_call_id": "c"}) {
		t.Fatalf("minimal checkpoint fields = %+v", fields)
	}
}
