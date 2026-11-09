package appprojector_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// A live communicate event must not manufacture an item with acceptance it
// cannot yet know. The recorded item becomes authoritative for both live
// history updates and a later snapshot after a disconnected client resyncs.
func TestManagerObservationLiveReplayStructure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	causal := provenance.WithWatch(nil, "watch-1", "generation-1", "delivery-1", "session-1", "job-1")
	recorded := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn-1", Kind: schema.TurnCommunicate,
		CausalKnown: true, Causal: causal, Communicate: &schema.CommunicateInfo{
			CallID: "call-1", EndTurn: true, EndReason: "needs_response", Message: "progress",
		}}
	if err := writer.Append(recorded); err != nil {
		t.Fatal(err)
	}
	projector := appprojector.NewAppEventProjector("session", "local:session")
	if notices := projector.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{
		CallID: "call-1", EndTurn: true, EndReason: "needs_response", Message: "progress",
	}}); len(notices) != 0 {
		t.Fatalf("unrecorded live communicate manufactured an item: %+v", notices)
	}
	live, ok := apptranscript.CommunicateItem("turn-1", 1, recorded)
	if !ok || live.Observation == nil || !live.Observation.CausalKnown || live.Observation.TerminalAccepted != nil {
		t.Fatalf("live recorded item invented or lost evidence: %+v", live)
	}
	index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	before, err := index.Latest(40)
	if err != nil || len(before.Candidates) != 1 || before.Candidates[0].Item.Observation == nil || before.Candidates[0].Item.Observation.TerminalAccepted != nil {
		t.Fatalf("live history invented acceptance before result: %+v, %v", before, err)
	}
	result := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn-1", Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`},
		}}}}
	if err := writer.Append(result); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := index.CatchUp(); err != nil {
		t.Fatal(err)
	}
	window, changes, err := index.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
	if err != nil || changes == nil || len(changes.Items) != 1 || changes.Items[0].Item.Observation == nil {
		t.Fatalf("live update did not revise recorded message: changes=%+v, err=%v", changes, err)
	}
	var replay appwire.ThreadItem
	for _, candidate := range window.Candidates {
		if candidate.Item.CallID == "call-1" && candidate.Item.Observation != nil {
			replay = candidate.Item
		}
	}
	if replay.Observation == nil || replay.Observation.TerminalAccepted == nil || !*replay.Observation.TerminalAccepted {
		t.Fatalf("snapshot lost accepted terminal result: %+v", replay)
	}
	if !reflect.DeepEqual(changes.Items[0].Item.Observation, replay.Observation) {
		t.Fatalf("live update %+v != resync %+v", changes.Items[0].Item.Observation, replay.Observation)
	}
	if !reflect.DeepEqual(replay.Observation.Causal, causal) {
		t.Fatalf("snapshot lost recorded ancestry: %+v", replay.Observation)
	}
	// The only fact not available at live-record time is acceptance; once the
	// matching typed result arrives the two projections have identical fields.
	live.Observation.TerminalAccepted = apptranscript.CommunicateTerminalAccepted(result, "call-1")
	liveJSON, err := json.Marshal(live.Observation)
	if err != nil {
		t.Fatal(err)
	}
	replayJSON, err := json.Marshal(replay.Observation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(liveJSON, replayJSON) {
		t.Fatalf("live observation %s != replay %s", liveJSON, replayJSON)
	}
}
