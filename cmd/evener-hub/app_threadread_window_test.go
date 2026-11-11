package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestSavedThreadReadExactWindowPreservesEmptyCheckpointTurn(t *testing.T) {
	const id = "02wMz5Txv5aIxgf9yVdd0P"
	root := t.TempDir()
	state := filepath.Join(root, "projects", "project-empty-checkpoint")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", id+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_empty", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnCheckpoint,
	}
	if err := writer.Append(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, state, id)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	response, ok, err := pastThreadReadResponse(t.Context(), hubcore.WebConfig{Past: past}, appwire.ThreadReadParams{
		Ref: "local:" + id, IncludeTurns: true, TurnWindow: &appwire.ThreadTurnWindow{Turn: "turn_empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("saved session was not found")
	}
	if len(response.Thread.Turns) != 1 {
		t.Fatalf("turns = %+v, want one selected turn shell", response.Thread.Turns)
	}
	if turn := response.Thread.Turns[0]; turn.ID != "turn_empty" || len(turn.Items) != 0 {
		t.Fatalf("selected turn = %+v, want empty turn_empty shell", turn)
	}
}
