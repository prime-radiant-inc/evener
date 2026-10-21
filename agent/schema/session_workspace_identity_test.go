package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMetaPersistsWorkspaceID(t *testing.T) {
	const workspaceID = "original-workspace"
	stateDir := t.TempDir()
	meta := SessionMeta{ID: "session-one", WorkspaceID: workspaceID}
	if err := SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	persisted, err := LoadSessionMeta(stateDir, meta.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if persisted.WorkspaceID != workspaceID {
		t.Fatalf("loaded workspace_id = %q, want %q", persisted.WorkspaceID, workspaceID)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "sessions", meta.ID+".meta.json"))
	if err != nil {
		t.Fatalf("read saved metadata: %v", err)
	}
	var persistedJSON map[string]any
	if err := json.Unmarshal(data, &persistedJSON); err != nil {
		t.Fatal(err)
	}
	if got, _ := persistedJSON["workspace_id"].(string); got != workspaceID {
		t.Fatalf("saved workspace_id = %q, want %q", got, workspaceID)
	}
}
