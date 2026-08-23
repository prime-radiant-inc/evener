package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// TestAsideSession_LoadMetaError covers the error path in asideSessionFS
// (aside.go lines 34-35) when LoadSessionMetaWithFS fails because the
// parent session meta file is corrupt.
func TestAsideSession_LoadMetaError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	parentID := "01PARENT00000000000000002"

	// Create a minimal valid parent transcript so readForkParent succeeds.
	tpath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(tpath), 0o755); err != nil {
		t.Fatal(err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID:  parentID,
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Write a corrupt session meta JSON so LoadSessionMetaWithFS fails.
	metaPath := filepath.Join(stateDir, sessionsSubdir, parentID+".meta.json")
	if err := os.WriteFile(metaPath, []byte("{corrupt json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = asideSessionFS(afero.NewOsFs(), stateDir, parentID)
	if err == nil {
		t.Fatal("expected error for corrupt parent meta")
	}
}

// TestForkSessionAtUserTurn_LoadMetaError covers the error path in
// forkSessionAtUserTurnFS (fork.go lines 72-74) when LoadSessionMetaWithFS fails.
func TestForkSessionAtUserTurn_LoadMetaError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	parentID := "01PARENT00000000000000003"

	// Create a minimal valid parent transcript with a USER_INPUT turn at
	// divergence position 1.
	tpath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(tpath), 0o755); err != nil {
		t.Fatal(err)
	}
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID:  parentID,
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Write a corrupt session meta JSON.
	metaPath := filepath.Join(stateDir, sessionsSubdir, parentID+".meta.json")
	if err := os.WriteFile(metaPath, []byte("{corrupt json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = forkSessionAtUserTurnFS(afero.NewOsFs(), stateDir, parentID, 1, "")
	if err == nil {
		t.Fatal("expected error for corrupt parent meta")
	}
}

// TestForkSessionAtUserTurn_NotUserInput covers the error path where the
// divergence turn is not a USER_INPUT turn (fork.go lines 67-68).
func TestForkSessionAtUserTurn_NotUserInput(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	// divergenceTurn=2 is an ASSISTANT turn, not USER_INPUT.
	_, _, err := forkSessionAtUserTurnFS(afero.NewOsFs(), stateDir, parentID, 2, "")
	if err == nil {
		t.Fatal("expected error for non-USER_INPUT divergence turn")
	}
}

// TestForkSessionAtUserTurn_DivergenceExceedsParent covers the error path
// where divergenceTurn exceeds the parent's turn count (fork.go lines 60-61).
func TestForkSessionAtUserTurn_DivergenceExceedsParent(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	// Parent has 4 turns; 5 exceeds.
	_, _, err := forkSessionAtUserTurnFS(afero.NewOsFs(), stateDir, parentID, 5, "")
	if err == nil {
		t.Fatal("expected error for divergenceTurn exceeding parent turns")
	}
}

// TestForkSessionAtUserTurn_DivergenceZero covers the < 1 guard
// (fork.go lines 50-51).
func TestForkSessionAtUserTurn_DivergenceZero(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	_, _, err := forkSessionAtUserTurnFS(afero.NewOsFs(), stateDir, parentID, 0, "")
	if err == nil {
		t.Fatal("expected error for divergenceTurn < 1")
	}
}

// Ensure identifier is used (for the buildParentSession helper that uses it).
var _ = identifier.MustNewSessionID
