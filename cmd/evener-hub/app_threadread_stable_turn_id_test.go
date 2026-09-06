package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

// TestPastThreadReadsNameAReservedTurnAcrossItemPages pins the turn identity
// a saved session answers with across the bounded read and continuation.
//
// The turn at stake carries a persisted reserved id (appwire.ClientMutationTurnID,
// kata rk09), which lives outside the entry-index namespace by design.
func TestPastThreadReadsNameAReservedTurnAcrossItemPages(t *testing.T) {
	cfg, ref, reserved := seedPastThreadWithReservedTurnID(t)

	full, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	// Four user+assistant exchanges are four logical turns; the last one
	// carries the reserved id its client-authored user input persisted.
	fullIDs := threadTurnIDs(full.Turns)
	if len(fullIDs) != 4 || fullIDs[3] != reserved {
		t.Fatalf("full read turn IDs = %v, want the persisted %q at the last logical turn", fullIDs, reserved)
	}

	windowed, ok := requirePastThreadReadResponse(t, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 3})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(flattenTestItems(windowed.Thread.Turns)) != 3 || windowed.OlderCursor == "" {
		t.Fatalf("item thread/read = %d items (cursor %q), want 3 items and continuation", len(flattenTestItems(windowed.Thread.Turns)), windowed.OlderCursor)
	}

	page, ok := requirePastThreadTurnsList(t, cfg, appwire.ThreadTurnsListParams{Ref: ref, Cursor: windowed.OlderCursor, ItemLimit: 3})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(flattenTestItems(page.Data)) != 3 {
		t.Fatalf("thread/turns/list = %d items, want 3", len(flattenTestItems(page.Data)))
	}
}

func threadTurnIDs(turns []appwire.Turn) []string {
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	return ids
}

// seedPastThreadWithReservedTurnID writes a dormant local session whose fourth
// user input was a client-authored reply, so its turn carries the reserved id
// the daemon persisted for it.
func seedPastThreadWithReservedTurnID(t *testing.T) (hubcore.WebConfig, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-reserved-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5", TurnCount: 8,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	reserved := appwire.ClientMutationTurnID(2)
	for i := range 4 {
		user := schema.NewTurn(schema.TurnUserInput, llm.User("in"))
		if i == 3 {
			user.ClientMutationID = "reply-1"
			user.StableTurnID = reserved
		}
		if err := w.Append(user); err != nil {
			t.Fatal(err)
		}
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("out"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, "local:" + sessionID, reserved
}
