package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// LastMessage persists as last_message and is absent until the session has
// written an agent message, so an older meta and a silent session read the
// same.
func TestSessionMeta_LastMessageRoundTrip(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(SessionMeta{ID: "01X", LastMessage: "Three layouts are ready for review."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"last_message":"Three layouts are ready for review."`) {
		t.Fatalf("meta = %s, want last_message", raw)
	}
	var back SessionMeta
	if err := json.Unmarshal(raw, &back); err != nil || back.LastMessage != "Three layouts are ready for review." {
		t.Fatalf("round trip = %q (%v)", back.LastMessage, err)
	}
	empty, err := json.Marshal(SessionMeta{ID: "01X"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "last_message") {
		t.Fatalf("a meta with no message carries the key: %s", empty)
	}
}
