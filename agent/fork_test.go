package agent

import (
	"os"
	"path/filepath"
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

// TestForkSession_CopiesPrefixAndAppliesEdit verifies the core fork semantics:
// the child gets the first (divergenceTurn-1) USER_INPUT turns from the parent
// followed by the edited message, and meta is wired up correctly on both sides.
func TestForkSession_CopiesPrefixAndAppliesEdit(t *testing.T) {
	stateDir, parentID := buildParentSession(t)

	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	// childID must be non-empty and distinct from parentID.
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
	if childMeta.TurnCount != 1 {
		t.Errorf("child TurnCount: got %d, want 1", childMeta.TurnCount)
	}
	if childMeta.ForkLabel != "" {
		t.Errorf("child ForkLabel should be empty, got %q", childMeta.ForkLabel)
	}

	// Parent meta should have been updated with the fork label.
	parentMeta, err := LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	if parentMeta.ForkLabel != "before TDD" {
		t.Errorf("parent ForkLabel: got %q, want %q", parentMeta.ForkLabel, "before TDD")
	}

	// Child transcript must contain the edited message in its last entry line.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	data, err := os.ReadFile(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadFile(child transcript): %v", err)
	}
	if !contains(string(data), "second task, table-driven") {
		t.Error("child transcript does not contain the edited message text")
	}

	// Read child transcript via ReadTranscript and verify structure.
	_, entries, _, err := ReadTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadTranscript(child): %v", err)
	}

	// Expected entries:
	//   0: USER_INPUT "first task"         (turn 1 from parent)
	//   1: ASSISTANT  "first reply"        (non-USER turn, included in prefix)
	//   2: USER_INPUT "second task, table-driven"  (the edited divergence turn)
	if len(entries) != 3 {
		t.Fatalf("child transcript entry count: got %d, want 3", len(entries))
	}

	// First two entries are the prefix (turn 1 user + turn 1 assistant from parent).
	if entries[0].Turn.Kind != TurnUserInput {
		t.Errorf("entries[0].Kind: got %q, want %q", entries[0].Turn.Kind, TurnUserInput)
	}
	if entries[0].Turn.Message.Text() != "first task" {
		t.Errorf("entries[0] text: got %q, want %q", entries[0].Turn.Message.Text(), "first task")
	}
	if entries[1].Turn.Kind != TurnAssistant {
		t.Errorf("entries[1].Kind: got %q, want %q", entries[1].Turn.Kind, TurnAssistant)
	}

	// Third entry is the edited turn.
	if entries[2].Turn.Kind != TurnUserInput {
		t.Errorf("entries[2].Kind: got %q, want %q", entries[2].Turn.Kind, TurnUserInput)
	}
	if entries[2].Turn.Message.Text() != "second task, table-driven" {
		t.Errorf("entries[2] text: got %q, want %q", entries[2].Turn.Message.Text(), "second task, table-driven")
	}
}

func TestForkSession_ChildLineagePreservedAcrossMetaRewrite(t *testing.T) {
	stateDir, parentID := buildParentSession(t)
	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), childMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ParentSessionID != parentID {
		t.Errorf("rewritten ParentSessionID: got %q, want %q", rewritten.ParentSessionID, parentID)
	}
	if rewritten.DivergenceTurn != 3 {
		t.Errorf("rewritten DivergenceTurn: got %d, want 3", rewritten.DivergenceTurn)
	}
	if rewritten.IsSubagent {
		t.Error("rewritten IsSubagent: got true, want false for fork child")
	}
}

func TestForkSession_ParentForkLabelPreservedAcrossMetaRewrite(t *testing.T) {
	stateDir, parentID := buildParentSession(t)
	if _, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD"); err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	parentMeta, err := LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(t.TempDir()), parentMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ForkLabel != "before TDD" {
		t.Errorf("rewritten ForkLabel: got %q, want %q", rewritten.ForkLabel, "before TDD")
	}
}

// TestForkSession_RejectsOutOfRangeDivergence verifies that divergenceTurn=0
// and divergenceTurn exceeding the parent's USER_INPUT count both return errors.
func TestForkSession_RejectsOutOfRangeDivergence(t *testing.T) {
	stateDir, parentID := buildParentSession(t)

	// divergenceTurn=0 must error.
	_, err := ForkSession(stateDir, parentID, 0, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=0) should return an error")
	}

	// divergenceTurn exceeding count (parent has 2 USER_INPUT turns).
	_, err = ForkSession(stateDir, parentID, 10, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=10) should return an error when parent has only 2 USER_INPUT turns")
	}
}

// TestForkSession_RejectsMissingParent verifies that a missing parent transcript
// causes ForkSession to return an error.
func TestForkSession_RejectsMissingParent(t *testing.T) {
	stateDir := t.TempDir()

	_, err := ForkSession(stateDir, "NONEXISTENT_SESSION", 1, "hello", "")
	if err == nil {
		t.Error("ForkSession with missing parent should return an error")
	}
}
