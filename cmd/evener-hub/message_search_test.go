package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/llm"
)

// seedSearchSession saves a past session whose transcript holds turns, with a
// system prompt, so the read's prelude takes the first entry ordinal and
// every later item's position moves past it.
func seedSearchSession(t *testing.T, projectsRoot, readable string, updated time.Time, turns ...schema.Turn) (hubcore.PastEntry, string) {
	t.Helper()
	stateDir := hubtest.ProjectDir(t, projectsRoot, readable)
	sessionID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
		Name: "Settle " + readable, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + readable},
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: updated.Add(-time.Hour), ProfileID: "openai", Model: "gpt-5",
		SystemPrompt: "You are a careful engineer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return hubcore.PastEntry{ID: sessionID, StateDir: stateDir}, sessionID
}

// A message hit opens its session at the item a thread read shows for that
// message: the index reads transcripts through the thread read's own
// projection, so its key and position are the reader's, prelude, tool calls
// and continuation turns included.
func TestMessageSearchHitsAreTheItemsAThreadReadShows(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", time.Now().Add(-time.Hour),
		schema.NewTurn(schema.TurnUserInput, llm.User("Why does the settle pass race the drain?")),
		schema.Turn{Kind: schema.TurnAssistant, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Checking the settle pass."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"grep settle"}`)}},
		}}},
		schema.Turn{Kind: schema.TurnToolResults, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_1", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "shell", Content: "settle.go:12"},
		}}}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass takes the tree lock first.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Then settle it.")),
	)
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if failures, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil || len(failures) != 0 {
		t.Fatalf("Refresh failures %+v, err %v", failures, err)
	}
	matches, err := index.Match(context.Background(), "settle", 10)
	if err != nil {
		t.Fatal(err)
	}
	hits := matches[sessionID].Hits
	if len(hits) != 4 {
		t.Fatalf("hits = %+v, want the four messages that say settle", hits)
	}

	read, found := requirePastThreadReadResponse(t, hubcore.WebConfig{Past: past}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !found {
		t.Fatal("the past session was not found for reading")
	}
	shown := map[string]appwire.ThreadItem{}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			shown[item.TranscriptKey] = item
		}
	}
	for _, hit := range hits {
		item, ok := shown[hit.TranscriptKey]
		if !ok || item.Position == nil || *item.Position != hit.Position || !strings.Contains(strings.ToLower(item.Text), "settle") {
			t.Fatalf("hit %+v is not an item the thread read shows saying settle (read: %+v)", hit, shown)
		}
	}
}

// Deleting a session takes its words out of search at once, not at the next
// refresh: the deletion's scrub forgets it in the message index.
func TestDeletingASessionForgetsItsMessages(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", time.Now(), schema.NewTurn(schema.TurnUserInput, llm.User("settle the drain")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), MessageSearch: index})
	if errs := web.scrubSessionDecisions(sessionID); len(errs) != 0 {
		t.Fatalf("scrub errors: %v", errs)
	}
	if matches, err := index.Match(context.Background(), "settle", 3); err != nil || len(matches) != 0 {
		t.Fatalf("matches after the scrub = %+v (%v), want none", matches, err)
	}
}
