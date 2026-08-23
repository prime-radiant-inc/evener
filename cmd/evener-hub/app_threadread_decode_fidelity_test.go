package hub

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// hubDecodedTurn writes a turn as the daemon persists it and reads it back the
// way the hub's reload path does, so a test asserting on the result is
// asserting on what a returning reader actually gets.
func hubDecodedTurn(t *testing.T, persisted schema.Turn) schema.Turn {
	t.Helper()
	raw, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 1, Turn: persisted})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	turn, ok := decodeTranscriptTurn(raw)
	if !ok {
		t.Fatalf("hub decode rejected the entry:\n%s", raw)
	}
	return turn
}
