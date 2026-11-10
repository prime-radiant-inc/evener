package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// --export-atif is json:"-" on SessionConfig, so the saved meta cannot carry it
// into a resumed session. A restoring invocation that asks for a trajectory
// must still get one at Close (#4672).
func TestRestoreSessionExportsATIFWhenTheInvocationAsks(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, ProfileID: "openai", Model: "gpt-5.2", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User("resume me"))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", liveModels: attemptRecordingLiveModels("openai", "gpt-5.2")})
	exportPath := filepath.Join(t.TempDir(), "trajectory.json")
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir:       stateDir,
		ExportATIFPath: exportPath,
		testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	restored.Close()
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("restored session wrote no ATIF trajectory: %v", err)
	}
}
