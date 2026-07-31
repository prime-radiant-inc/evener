package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

// TestPastThreadReadsNameAReservedTurnTheSameWayWindowedOrNot pins the turn ids
// a saved session answers with across the three reads the hub serves it by.
//
// thread/read chooses between two projections on turnLimit alone: unlimited
// reads the whole transcript, limited reads a bounded window off the turn
// index, and thread/turns/list pages the same index. One saved session must
// name its turns identically under all three — a client that scrolls back into
// an older page must not find the turn it is replying to renamed.
//
// The turn at stake carries a persisted reserved id (appwire.ClientMutationTurnID,
// kata rk09), which lives outside the entry-index namespace by design.
func TestPastThreadReadsNameAReservedTurnTheSameWayWindowedOrNot(t *testing.T) {
	cfg, ref, reserved := seedPastThreadWithReservedTurnID(t)

	full, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	fullIDs := threadTurnIDs(full.Turns)
	if len(fullIDs) != 8 || fullIDs[6] != reserved {
		t.Fatalf("full read turn IDs = %v, want the persisted %q at the seventh entry", fullIDs, reserved)
	}

	windowed, ok := requirePastThreadReadResponse(t, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, TurnLimit: 3})
	if !ok {
		t.Fatal("past thread not found")
	}
	wantWindow, wantCursor := appwire.WindowTurns(full.Turns, 3)
	if !reflect.DeepEqual(threadTurnIDs(windowed.Thread.Turns), threadTurnIDs(wantWindow)) || windowed.OlderCursor != wantCursor {
		t.Fatalf("windowed thread/read turn IDs = %v (cursor %q), want the full read's %v (cursor %q)",
			threadTurnIDs(windowed.Thread.Turns), windowed.OlderCursor, threadTurnIDs(wantWindow), wantCursor)
	}

	page, ok := requirePastThreadTurnsList(t, cfg, appwire.ThreadTurnsListParams{Ref: ref, Cursor: windowed.OlderCursor, Limit: 3})
	if !ok {
		t.Fatal("past thread not found")
	}
	wantPage := appwire.PageTurns(full.Turns, windowed.OlderCursor, 3)
	if !reflect.DeepEqual(threadTurnIDs(page.Data), threadTurnIDs(wantPage.Data)) || page.NextCursor != wantPage.NextCursor {
		t.Fatalf("thread/turns/list turn IDs = %v (next %q), want the full read's %v (next %q)",
			threadTurnIDs(page.Data), page.NextCursor, threadTurnIDs(wantPage.Data), wantPage.NextCursor)
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
