package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

// A voice history or renewal read must not accept saved history when the live
// daemon read fails: the retryable transport error has to reach the bridge so
// it can hold speech instead of closing on a daemonless authority response.
func TestLiveOnlyReadRejectsSavedHistoryAfterOutage(t *testing.T) {
	const id = "02wMz5Txv5aIxgf9yVdd0N"
	root := t.TempDir()
	state := filepath.Join(root, "projects", "project-voice-0000000000")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", id+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_saved",
		TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput,
		Message: llm.User("saved history"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, state, id)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past}
	params := appwire.ThreadReadParams{
		Ref: "local:" + id, IncludeTurns: true, ItemLimit: 40, LiveOnly: true,
	}
	outage := appwire.RetryableSessionUnavailable("relay connection ended before route publication")
	if _, ok, err := unavailableThreadReadResponse(t.Context(), cfg, appsource.NewRegistry(), params); err != nil || ok {
		t.Fatalf("live-only read served saved history after an outage: ok=%v err=%v", ok, err)
	}
	if allowsPastFallbackAfterLiveReadFailure(nil, params, outage) {
		t.Fatal("a live-only read allowed saved-history fallback after a relay outage")
	}
}
