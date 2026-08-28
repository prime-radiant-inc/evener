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

// TestPrepareAppIdentityFromEntriesMatchesFileProjection is the differential
// proof for the resume handoff: the entries form must produce the identical
// PreparedAppIdentity the file form does, over the same transcript bytes.
func TestPrepareAppIdentityFromEntriesMatchesFileProjection(t *testing.T) {
	const sessionID = "th_diff"
	path := writeDifferentialFixture(t, sessionID)
	header, entries := restoreFixtureEntries(t, path, sessionID)
	if len(entries) == 0 {
		t.Fatal("fixture produced no entries")
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	fromFile, err := PrepareAppIdentityForRef("local", sessionID, ref, path)
	if err != nil {
		t.Fatalf("file form: %v", err)
	}
	fromEntries, err := PrepareAppIdentityFromEntries("local", sessionID, ref, header, entries)
	if err != nil {
		t.Fatalf("entries form: %v", err)
	}

	fileSnapshot := snapshotTurns(t, fromFile)
	entriesSnapshot := snapshotTurns(t, fromEntries)
	if !reflect.DeepEqual(fileSnapshot, entriesSnapshot) {
		t.Fatalf("turn snapshots diverge:\nfile:    %#v\nentries: %#v", fileSnapshot, entriesSnapshot)
	}
	if fromFile.threadID != fromEntries.threadID || fromFile.ref != fromEntries.ref {
		t.Fatalf("identity diverges: %#v vs %#v", fromFile, fromEntries)
	}
	if len(fileSnapshot) == 0 {
		t.Fatal("fixture projected to zero turns; differential proof is vacuous")
	}
}

// TestPrepareAppIdentityFromEntriesMatchesFileTurnIDs pins the persisted
// turn ids specifically: every id the entries form yields must be the exact
// id the file form yields, in order (turn_<1-based entry index> or a reserved
// StableTurnID), so the daemon fences live turn ids above the same floor
// however the resume projection ran.
func TestPrepareAppIdentityFromEntriesMatchesFileTurnIDs(t *testing.T) {
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
	// The id floor both forms report must also agree: it is what
	// SeedPersistedTurns fences live turn ids above.
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

// TestPrepareAppIdentityFromEntriesStillEmitsPrelude pins that the entries
// form keeps the file form's prelude emission from the header.
func TestPrepareAppIdentityFromEntriesStillEmitsPrelude(t *testing.T) {
	const sessionID = "th_prelude"
	path := writeDifferentialFixture(t, sessionID)
	header, entries := restoreFixtureEntries(t, path, sessionID)

	ref := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	fromEntries, err := PrepareAppIdentityFromEntries("local", sessionID, ref, header, entries)
	if err != nil {
		t.Fatalf("entries form: %v", err)
	}
	found := false
	for _, turn := range snapshotTurns(t, fromEntries) {
		if turn.ID == appwire.SystemPreludeTurnID {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries form dropped the prelude turn")
	}
}

func snapshotTurns(t *testing.T, prepared PreparedAppIdentity) []appwire.Turn {
	t.Helper()
	if prepared.turns == nil {
		t.Fatal("prepared identity carries no turn snapshot")
	}
	prepared.turns.mu.Lock()
	defer prepared.turns.mu.Unlock()
	return append([]appwire.Turn(nil), prepared.turns.turns...)
}

// TestPrepareAppIdentityFromEntriesRejectsForeignHeader keeps the file form's
// session-identity error contract for the entries path.
func TestPrepareAppIdentityFromEntriesRejectsForeignHeader(t *testing.T) {
	const sessionID = "th_mismatch"
	path := writeDifferentialFixture(t, sessionID)
	header, entries := restoreFixtureEntries(t, path, sessionID)
	header.SessionID = "th_other"
	if _, err := PrepareAppIdentityFromEntries("local", sessionID, appwire.Ref{SourceID: "local", ThreadID: sessionID}.String(), header, entries); err == nil {
		t.Fatal("entries form accepted a header naming a different session")
	}
}
