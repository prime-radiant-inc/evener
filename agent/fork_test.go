package agent

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// buildParentSession creates a parent session with a 4-turn transcript
// (USER, ASSISTANT, USER, ASSISTANT) and a corresponding meta file.
// Returns the parentID and stateDir.
func buildParentSession(t *testing.T) (stateDir, parentID string) {
	t.Helper()
	stateDir = t.TempDir()
	parentID = "01PARENT00000000000000001"

	tpath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}

	turns := []Turn{
		NewTurn(TurnUserInput, llm.User("first task")),
		NewTurn(TurnAssistant, llm.Assistant("first reply")),
		NewTurn(TurnUserInput, llm.User("second task")),
		NewTurn(TurnAssistant, llm.Assistant("second reply")),
	}
	for _, turn := range turns {
		if err := tw.Append(turn); err != nil {
			t.Fatalf("Append turn: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	meta := SessionMeta{
		ID:        parentID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 50},
		EnvInfo:   EnvironmentInfo{WorkingDir: "/tmp/test"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		TurnCount: 2,
	}
	if err := SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	return stateDir, parentID
}

// TestForkSession_CopiesPrefixOnly verifies the new fork semantics: the child
// gets only the prefix entries from the parent (everything strictly before the
// divergence turn). The caller is responsible for whatever comes next — there
// is no "edited message" parameter.
func TestForkSession_CopiesPrefixOnly(t *testing.T) {
	stateDir, parentID := buildParentSession(t)

	// Snapshot parent meta before fork to verify it is not mutated.
	parentBefore, err := LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent before): %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	if childID == "" {
		t.Fatal("childID is empty")
	}
	if childID == parentID {
		t.Fatalf("childID should differ from parentID, got %q", childID)
	}

	// Child meta assertions.
	childMeta, err := LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Errorf("child ParentSessionID: got %q, want %q", childMeta.ParentSessionID, parentID)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Errorf("child DivergenceTurn: got %d, want 3", childMeta.DivergenceTurn)
	}

	// Forking must not mutate the parent meta.
	parentAfter, err := LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent after): %v", err)
	}
	if !reflect.DeepEqual(parentAfter, parentBefore) {
		t.Errorf("parent meta mutated by ForkSession:\n before=%+v\n after=%+v", parentBefore, parentAfter)
	}

	// Child transcript must contain only the prefix — no edited message text.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadTranscript(child): %v", err)
	}

	// Expected entries (parent transcript [U1, A1, U2, A2], divergenceTurn=3):
	//   0: USER_INPUT  "first task"
	//   1: ASSISTANT   "first reply"
	// Entry at divergenceTurn-1 (the original U2) is NOT copied.
	if len(entries) != 2 {
		t.Fatalf("child transcript entry count: got %d, want 2 (prefix only)", len(entries))
	}
	if entries[0].Turn.Kind != TurnUserInput || entries[0].Turn.Message.Text() != "first task" {
		t.Errorf("entries[0]: got %s %q, want USER_INPUT 'first task'", entries[0].Turn.Kind, entries[0].Turn.Message.Text())
	}
	if entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("entries[1].Kind: got %q, want %q", entries[1].Turn.Kind, TurnAssistant)
	}
}

// TestForkSession_RejectsOutOfRangeDivergence verifies that divergenceTurn=0
// and divergenceTurn exceeding the parent's USER_INPUT count both return errors.
func TestForkSession_RejectsOutOfRangeDivergence(t *testing.T) {
	stateDir, parentID := buildParentSession(t)

	// divergenceTurn=0 must error.
	_, err := ForkSession(stateDir, parentID, 0)
	if err == nil {
		t.Error("ForkSession(divergenceTurn=0) should return an error")
	}

	// divergenceTurn exceeding count (parent has 2 USER_INPUT turns).
	_, err = ForkSession(stateDir, parentID, 10)
	if err == nil {
		t.Error("ForkSession(divergenceTurn=10) should return an error when parent has only 2 USER_INPUT turns")
	}
}

// TestForkSession_RejectsMissingParent verifies that a missing parent transcript
// causes ForkSession to return an error.
func TestForkSession_RejectsMissingParent(t *testing.T) {
	stateDir := t.TempDir()

	_, err := ForkSession(stateDir, "NONEXISTENT_SESSION", 1)
	if err == nil {
		t.Error("ForkSession with missing parent should return an error")
	}
}

