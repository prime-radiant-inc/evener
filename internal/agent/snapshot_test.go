package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prime-radiant/serf/internal/llm"
)

func testSnapshot() SessionSnapshot {
	return SessionSnapshot{
		ID:        "01JTEST000000000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: SessionConfig{
			MaxToolRoundsPerInput: 200,
			ReasoningEffort:       "high",
		},
		EnvInfo: EnvironmentInfo{
			WorkingDir: "/tmp/test",
			Platform:   "linux",
			IsGitRepo:  true,
			GitBranch:  "main",
		},
		History: []Turn{
			{Kind: TurnUserInput, Message: llm.User("hello")},
			{Kind: TurnAssistant, Message: llm.Assistant("hi there")},
		},
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
}

func TestSessionSnapshot_JSONRoundTrip(t *testing.T) {
	orig := testSnapshot()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SessionSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID {
		t.Fatalf("id: got %q want %q", got.ID, orig.ID)
	}
	if got.ProfileID != orig.ProfileID {
		t.Fatalf("profile_id: got %q want %q", got.ProfileID, orig.ProfileID)
	}
	if got.Model != orig.Model {
		t.Fatalf("model: got %q want %q", got.Model, orig.Model)
	}
	if len(got.History) != len(orig.History) {
		t.Fatalf("history length: got %d want %d", len(got.History), len(orig.History))
	}
	if got.History[0].Kind != TurnUserInput {
		t.Fatalf("history[0].kind: got %q want %q", got.History[0].Kind, TurnUserInput)
	}
	if got.History[1].Message.Text() != "hi there" {
		t.Fatalf("history[1].text: got %q want %q", got.History[1].Message.Text(), "hi there")
	}
	if got.TurnCount != 2 {
		t.Fatalf("turn_count: got %d want %d", got.TurnCount, 2)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("created_at: got %v want %v", got.CreatedAt, orig.CreatedAt)
	}
}

func TestSaveSession_CreatesFileAtomically(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()

	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// File should exist at .serf/sessions/<id>.json
	path := filepath.Join(dir, ".serf", "sessions", snap.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got SessionSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != snap.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, snap.ID)
	}

	// No .tmp files should remain.
	entries, _ := os.ReadDir(filepath.Join(dir, ".serf", "sessions"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file not cleaned up: %s", e.Name())
		}
	}
}

func TestSaveSession_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()

	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession first: %v", err)
	}

	// Update and save again.
	snap.TurnCount = 10
	snap.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)
	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession second: %v", err)
	}

	loaded, err := LoadSession(dir, snap.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.TurnCount != 10 {
		t.Fatalf("turn_count after overwrite: got %d want 10", loaded.TurnCount)
	}
}

func TestLoadSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSession(dir, "NONEXISTENT")
	if err == nil {
		t.Fatalf("expected error for nonexistent session")
	}
}

func TestLoadSession_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, ".serf", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "CORRUPT.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := LoadSession(dir, "CORRUPT")
	if err == nil {
		t.Fatalf("expected error for corrupt JSON")
	}
}

func TestListSessions_SortedByUpdatedAt(t *testing.T) {
	dir := t.TempDir()

	snap1 := testSnapshot()
	snap1.ID = "01JTEST000000000000000001"
	snap1.UpdatedAt = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	snap2 := testSnapshot()
	snap2.ID = "01JTEST000000000000000002"
	snap2.UpdatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	snap3 := testSnapshot()
	snap3.ID = "01JTEST000000000000000003"
	snap3.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	for _, s := range []SessionSnapshot{snap1, snap2, snap3} {
		if err := SaveSession(dir, s); err != nil {
			t.Fatalf("SaveSession %s: %v", s.ID, err)
		}
	}

	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list length: got %d want 3", len(list))
	}
	// Most recently updated first.
	if list[0].ID != snap2.ID {
		t.Fatalf("list[0].id: got %q want %q", list[0].ID, snap2.ID)
	}
	if list[1].ID != snap3.ID {
		t.Fatalf("list[1].id: got %q want %q", list[1].ID, snap3.ID)
	}
	if list[2].ID != snap1.ID {
		t.Fatalf("list[2].id: got %q want %q", list[2].ID, snap1.ID)
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestListSessions_SkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()

	// Save a valid snapshot.
	snap := testSnapshot()
	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Add a corrupt file.
	sessDir := filepath.Join(dir, ".serf", "sessions")
	if err := os.WriteFile(filepath.Join(sessDir, "CORRUPT.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 valid session, got %d", len(list))
	}
	if list[0].ID != snap.ID {
		t.Fatalf("id: got %q want %q", list[0].ID, snap.ID)
	}
}
