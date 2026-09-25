package server

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// writeDifferentialFixture writes a transcript exercising every shape the
// app-identity projection handles: prelude content (system prompt + agent
// tasks) in the header, a user input, an assistant tool call and its result,
// steering, a failed turn, and a usage-carrying model response.
func writeDifferentialFixture(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".transcript.jsonl")
	header := transcript.Header{
		SessionID:    sessionID,
		CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
		ProfileID:    "openai",
		Model:        "gpt-test",
		SystemPrompt: "You are Evener.",
	}
	tw, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	appendTurn := func(turn schema.Turn) {
		t.Helper()
		if err := tw.Append(turn); err != nil {
			t.Fatalf("append %s: %v", turn.Kind, err)
		}
	}
	appendTurn(schema.NewTurn(schema.TurnUserInput, llm.User("run the fixture")))
	call := llm.ToolCallData{ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	appendTurn(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}})
	appendTurn(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_read", "read_file", "line 1", false)))
	appendTurn(schema.NewTurn(schema.TurnSteering, llm.User("steer")))
	appendTurn(schema.NewTurn(schema.TurnFailure, llm.User("fails")))
	appendTurn(schema.NewTurn(schema.TurnAssistant, llm.Assistant("done")))
	usageTurn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("counted"))
	usageTurn.Usage = llm.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12}
	usageTurn.Timestamp = time.Unix(1_700_000_100, 0).UTC()
	appendTurn(usageTurn)
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// restoreFixtureEntries opens the same fixture the way resume does, so the
// entries handed to the entries form are the entries restore would retain.
func restoreFixtureEntries(t *testing.T, path, sessionID string) (transcript.Header, []transcript.Entry) {
	t.Helper()
	w, entries, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("open for session: %v", err)
	}
	defer w.Close() //nolint:errcheck // test fixture read
	return w.Header(), entries
}

// TestAppTurnsFromEntriesMatchesFileTurnIDs pins the persisted turn ids of
// the legacy entry projection: every id the entries form yields must be the
// exact id the file form yields, in order (turn_<1-based entry index> or a
// reserved StableTurnID).
func TestAppTurnsFromEntriesMatchesFileTurnIDs(t *testing.T) {
	const sessionID = "th_ids"
	path := writeDifferentialFixture(t, sessionID)
	header, entries := restoreFixtureEntries(t, path, sessionID)

	fileTurns, _, err := appTurnsFromTranscriptFile(path)
	if err != nil {
		t.Fatalf("file projection: %v", err)
	}
	entryTurns, _, err := appTurnsFromEntries(header, entries)
	if err != nil {
		t.Fatalf("entries projection: %v", err)
	}
	if len(fileTurns) == 0 {
		t.Fatal("fixture projected to zero turns")
	}
	var fileIDs, entryIDs []string
	for _, turn := range fileTurns {
		fileIDs = append(fileIDs, turn.ID)
	}
	for _, turn := range entryTurns {
		entryIDs = append(entryIDs, turn.ID)
	}
	if !reflect.DeepEqual(fileIDs, entryIDs) {
		t.Fatalf("turn ids diverge:\nfile:    %v\nentries: %v", fileIDs, entryIDs)
	}
	// The entry count both forms report must also agree.
	_, fileHighest, err := appTurnsFromTranscriptFile(path)
	if err != nil {
		t.Fatalf("file projection (floor): %v", err)
	}
	_, entriesHighest, err := appTurnsFromEntries(header, entries)
	if err != nil {
		t.Fatalf("entries projection (floor): %v", err)
	}
	if fileHighest != entriesHighest {
		t.Fatalf("persisted entry floors diverge: file=%d entries=%d", fileHighest, entriesHighest)
	}
}

// TestServedTranscriptReadEmitsThePrelude pins that a thread served from a
// transcript reads its header-derived prelude.
func TestServedTranscriptReadEmitsThePrelude(t *testing.T) {
	const sessionID = "th_prelude"
	path := writeDifferentialFixture(t, sessionID)
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	installTranscriptIdentity(t, srv, sessionID, path)
	read, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: sessionID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range read.Thread.Turns {
		if turn.ID == appwire.SystemPreludeTurnID {
			return
		}
	}
	t.Fatalf("the read has no prelude turn: %+v", read.Thread.Turns)
}
