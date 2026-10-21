package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
)

func TestWorkspaceIdentityNewRootUsesSessionID(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	if got, want := s.WorkspaceID(), s.ID(); got != want {
		t.Fatalf("WorkspaceID() = %q, want new root ID %q", got, want)
	}
	if got := s.Meta().WorkspaceID; got != s.ID() {
		t.Fatalf("saved metadata workspace ID = %q, want %q", got, s.ID())
	}
	persisted, err := schema.LoadSessionMeta(stateDir, s.ID())
	if err != nil {
		t.Fatalf("load new-root metadata: %v", err)
	}
	if persisted.WorkspaceID != s.ID() {
		t.Fatalf("persisted new-root workspace ID = %q, want %q", persisted.WorkspaceID, s.ID())
	}
	configJSON, err := json.Marshal(s.Meta().Config)
	if err != nil {
		t.Fatal(err)
	}
	var configFields map[string]any
	if err := json.Unmarshal(configJSON, &configFields); err != nil {
		t.Fatal(err)
	}
	if _, exists := configFields["workspace_id"]; exists {
		t.Fatal("workspace identity leaked into inherited config snapshot")
	}
}

func TestWorkspaceIdentityLegacyMetadataIsPersistedBeforeRestoreReturns(t *testing.T) {
	stateDir := t.TempDir()
	base := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	meta := base.Meta()
	base.Close()
	meta.WorkspaceID = ""
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("write old-format metadata: %v", err)
	}
	transcriptPath := filepath.Join(stateDir, "sessions", meta.ID+".transcript.jsonl")
	transcriptBefore, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read original transcript: %v", err)
	}
	legacyMetaBytes, err := os.ReadFile(filepath.Join(stateDir, "sessions", meta.ID+".meta.json"))
	if err != nil {
		t.Fatalf("read legacy metadata fixture: %v", err)
	}
	var legacyFields map[string]any
	if err := json.Unmarshal(legacyMetaBytes, &legacyFields); err != nil {
		t.Fatal(err)
	}
	if _, exists := legacyFields["workspace_id"]; exists {
		t.Fatal("legacy metadata fixture unexpectedly contains workspace_id")
	}

	restored, err := RestoreSessionFromMetaWithConfig(base.client, base.profile,
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore old-format session: %v", err)
	}
	defer restored.Close()
	if got, want := restored.WorkspaceID(), meta.ID; got != want {
		t.Fatalf("restored WorkspaceID() = %q, want saved session ID %q", got, want)
	}
	persisted, err := schema.LoadSessionMeta(stateDir, meta.ID)
	if err != nil {
		t.Fatalf("load restored metadata: %v", err)
	}
	if persisted.WorkspaceID != meta.ID {
		t.Fatalf("persisted workspace ID = %q, want saved session ID %q", persisted.WorkspaceID, meta.ID)
	}
	transcriptAfter, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript after restore: %v", err)
	}
	if string(transcriptAfter) != string(transcriptBefore) {
		t.Fatal("legacy identity initialization rewrote transcript history")
	}
	restored.Close()

	const existingWorkspaceID = "preexisting-workspace"
	persisted.WorkspaceID = existingWorkspaceID
	if err := schema.SaveSessionMeta(stateDir, persisted); err != nil {
		t.Fatalf("save existing workspace identity: %v", err)
	}
	retained, err := RestoreSessionFromMetaWithConfig(base.client, base.profile,
		execenv.NewLocalExecutionEnvironment(t.TempDir()), persisted, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore existing workspace identity: %v", err)
	}
	defer retained.Close()
	if retained.WorkspaceID() != existingWorkspaceID {
		t.Fatalf("restored existing workspace ID = %q, want %q", retained.WorkspaceID(), existingWorkspaceID)
	}
}

func TestWorkspaceIdentityForkStartsAtChildID(t *testing.T) {
	stateDir, parentID := buildParentSession(t)
	childID, err := ForkSession(stateDir, parentID, 3, "edited task", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	child, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if child.WorkspaceID != childID {
		t.Fatalf("fork workspace ID = %q, want child ID %q", child.WorkspaceID, childID)
	}
}
