package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/installid"
	"primeradiant.com/evener/llm"
)

// TestSession_InstallationIDSurvivesRestore pins the end-to-end durability
// contract behind Session.installID: one state directory yields one
// installation ID across construction and restore, and that ID reaches provider
// metadata on both paths. The installid package tests prove the file is reused
// at the seam, and TestSession_PopulatesModelRequestMetadata proves a fresh
// session writes the file; neither constructs a second Session against the same
// directory, so a wiring regression that minted a fresh ID during restore (or
// stopped threading the persisted ID into request metadata) would leave every
// existing test green.
//
// ForceRealIO opts both constructions back into the on-disk persistence path
// (session_config.go); the test-speed default resolves the ID against an
// in-memory filesystem and would mint a new one on every construction, so it
// cannot observe the invariant.
func TestSession_InstallationIDSurvivesRestore(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dir := t.TempDir()

	createClient := llm.NewClient()
	createAdapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ok") },
	}}
	createClient.Register(createAdapter)

	created, err := NewSession(createClient, withTestSessionNamer(createClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	meta := created.Meta()
	if _, err := created.ProcessInput(context.Background(), "run", nil); err != nil {
		t.Fatalf("created ProcessInput: %v", err)
	}
	createdReqs := createAdapter.Requests()
	created.Close()
	if len(createdReqs) == 0 {
		t.Fatal("created session sent no request")
	}
	createdID := createdReqs[0].ClientMetadata[installid.CodexInstallationIDMetadataKey]
	if createdID == "" {
		t.Fatalf("created request metadata missing %s: %#v", installid.CodexInstallationIDMetadataKey, createdReqs[0].ClientMetadata)
	}

	// The metadata value must be the ID actually persisted under the state
	// directory, not merely some non-empty string.
	stored, err := os.ReadFile(filepath.Join(stateDir, "installation_id"))
	if err != nil {
		t.Fatalf("read persisted installation_id: %v", err)
	}
	if storedID := strings.TrimSpace(string(stored)); storedID != createdID {
		t.Fatalf("persisted installation ID = %q, created request metadata = %q", storedID, createdID)
	}

	restoreClient := llm.NewClient()
	restoreAdapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ok") },
	}}
	restoreClient.Register(restoreAdapter)

	restored, err := RestoreSessionFromMetaWithConfig(restoreClient, withTestSessionNamer(restoreClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	if _, err := restored.ProcessInput(context.Background(), "run again", nil); err != nil {
		t.Fatalf("restored ProcessInput: %v", err)
	}
	restoredReqs := restoreAdapter.Requests()
	restored.Close()
	if len(restoredReqs) == 0 {
		t.Fatal("restored session sent no request")
	}
	restoredID := restoredReqs[0].ClientMetadata[installid.CodexInstallationIDMetadataKey]
	if restoredID != createdID {
		t.Fatalf("installation ID after restore = %q, want the constructed ID %q", restoredID, createdID)
	}
}
