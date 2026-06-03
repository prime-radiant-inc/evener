package contextmgr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func testSnapshot() Snapshot {
	return Snapshot{
		ID:        "01JTEST000000000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: schema.ConfigSnapshot{
			MaxToolRoundsPerInput: 200,
			ReasoningEffort:       "high",
		},
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir: "/tmp/test",
			Platform:   "linux",
			IsGitRepo:  true,
			GitBranch:  "main",
		},
		History: []schema.Turn{
			{Kind: schema.TurnUserInput, Message: llm.User("hello")},
			{Kind: schema.TurnAssistant, Message: llm.Assistant("hi there")},
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
	var got Snapshot
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
	if got.History[0].Kind != schema.TurnUserInput {
		t.Fatalf("history[0].kind: got %q want %q", got.History[0].Kind, schema.TurnUserInput)
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

	if err := Save(dir, snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File should exist at sessions/<id>.json
	path := filepath.Join(dir, "sessions", snap.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != snap.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, snap.ID)
	}

	// No .tmp files should remain.
	entries, _ := os.ReadDir(filepath.Join(dir, "sessions"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file not cleaned up: %s", e.Name())
		}
	}
}

func TestSaveSession_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()

	if err := Save(dir, snap); err != nil {
		t.Fatalf("Save first: %v", err)
	}

	// Update and save again.
	snap.TurnCount = 10
	snap.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)
	if err := Save(dir, snap); err != nil {
		t.Fatalf("Save second: %v", err)
	}

	path := filepath.Join(dir, "sessions", snap.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var loaded Snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if loaded.TurnCount != 10 {
		t.Fatalf("turn_count after overwrite: got %d want 10", loaded.TurnCount)
	}
}
