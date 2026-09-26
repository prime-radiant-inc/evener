package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// LastTurnEndedAt persists as last_turn_ended_at at millisecond precision and
// is absent until a turn has ended, so an older meta and a session that never
// ran read the same.
func TestSessionMeta_LastTurnEndedAtRoundTrip(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	raw, err := json.Marshal(SessionMeta{ID: "01X", LastTurnEndedAt: ended})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"last_turn_ended_at":"2026-09-26T12:00:00.123Z"`) {
		t.Fatalf("meta = %s, want last_turn_ended_at", raw)
	}
	var back SessionMeta
	if err := json.Unmarshal(raw, &back); err != nil || !back.LastTurnEndedAt.Equal(ended) {
		t.Fatalf("round trip = %v (%v), want %v", back.LastTurnEndedAt, err, ended)
	}
	empty, err := json.Marshal(SessionMeta{ID: "01X"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "last_turn_ended_at") {
		t.Fatalf("a meta with no turn end carries the key: %s", empty)
	}
}
