package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestFindSessionTranscriptsSkipsDurableRoundTimingMetadata(t *testing.T) {
	dir := t.TempDir()
	id := "01KROUND-TIMING-FIND-0000000000"
	path := transcriptPath(dir, id)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	payload := schema.RoundTimings{Round: 7, TotalRound: time.Second}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System(payload.Announcement()))
	timing.RoundTimings = &payload
	user := schema.NewTurn(schema.TurnUserInput, llm.User("please explain Round 7 results"))
	if err := writer.AppendDurable(timing); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendDurable(user); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	candidate := findCandidate{bucketDir: dir, meta: schema.SessionMeta{ID: id, ProfileID: "profile"}}
	scanned, truncated := 0, false
	snippets, matched := matchCandidate(candidate, "Round 7", "round 7", &scanned, &truncated)
	if !matched || len(snippets) != 1 || snippets[0].Role != "user" {
		t.Fatalf("search matches = %v snippets = %+v, want only user conversation", matched, snippets)
	}
	if scanned != 1 {
		t.Fatalf("scanned = %d, want 1", scanned)
	}
}
