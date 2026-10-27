package hub

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubHistoryContentSavedTranscriptAndMetadata(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "selected-past-0000000000")
	id := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	mustInitializeHubClient(t, client)
	no := false
	options := appwire.ThreadContentParams{IncludeThinking: &no, IncludeToolCalls: &no, IncludeToolCallIntents: &no, IncludeSystemMessages: &no, IncludeDiagnostics: &no}
	params := appwire.ThreadReadParams{ThreadContentParams: options, Ref: "local:" + id, IncludeTurns: true, ItemLimit: 1}
	r, err := client.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Thread.Turns) != 1 || len(r.Thread.Turns[0].Items) != 1 || r.Thread.Turns[0].Items[0].Type != "userMessage" || r.OlderCursor == "" {
		t.Fatalf("selected window=%+v", r)
	}
	page, err := client.ThreadTurnsList(t.Context(), appwire.ThreadTurnsListParams{ThreadContentParams: options, Ref: params.Ref, Cursor: r.OlderCursor, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || len(page.Data[0].Items) != 1 || page.Data[0].Items[0].Type != "agentMessage" {
		t.Fatalf("selected backfill=%+v", page)
	}
	params.IncludeTurns = false
	metadata, err := client.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Thread.Evener.Diagnostics != nil || len(metadata.Thread.Turns) != 0 || metadata.Thread.ID != id {
		t.Fatal("metadata selection changed thread identity or fetched history")
	}
	params.IncludeTurns = true
	params.IncludeConversation = &no
	empty, err := client.ThreadRead(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Thread.Turns) != 0 || empty.OlderCursor != "" || empty.Thread.ID != id {
		t.Fatal("all excluded view not empty or identity lost")
	}
}
