package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
)

// TestForkSessionAtUserTurn_CopiesPrefixWithoutAppendingInput verifies the
// deferred-input fork (issue #42): the child transcript contains only the
// entries BEFORE the divergence turn — the diverging USER_INPUT turn itself
// is NOT copied or replaced — and the original text of that turn is returned
// so the caller can hand it back for editing before submission. The forked
// session must therefore never auto-run the message on open.
func TestForkSessionAtUserTurn_CopiesPrefixWithoutAppendingInput(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	childID, originalInput, err := ForkSessionAtUserTurn(stateDir, parentID, 3, "original branch")
	if err != nil {
		t.Fatalf("ForkSessionAtUserTurn: %v", err)
	}
	if err := identifier.ValidateSessionID(childID); err != nil {
		t.Fatalf("child session ID %q: %v", childID, err)
	}
	if childID == parentID {
		t.Fatalf("childID should differ from parentID, got %q", childID)
	}
	if originalInput != "second task" {
		t.Errorf("originalInput: got %q, want %q", originalInput, "second task")
	}

	// Child meta assertions.
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Errorf("child ParentSessionID: got %q, want %q", childMeta.ParentSessionID, parentID)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Errorf("child DivergenceTurn: got %d, want 3", childMeta.DivergenceTurn)
	}
	// Prefix [U1, A1] has one assistant turn and one accepted user input.
	if childMeta.TurnCount != 1 {
		t.Errorf("child TurnCount: got %d, want 1", childMeta.TurnCount)
	}
	if childMeta.AcceptedInputTurns != 1 {
		t.Errorf("child AcceptedInputTurns: got %d, want 1", childMeta.AcceptedInputTurns)
	}

	// Parent meta carries the fork label.
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	if parentMeta.ForkLabel != "original branch" {
		t.Errorf("parent ForkLabel: got %q, want %q", parentMeta.ForkLabel, "original branch")
	}

	// Child transcript must contain ONLY the prefix entries [U1, A1] — no
	// trailing USER_INPUT turn that would auto-run on resume.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	_, entries, _, err := readTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("readTranscript(child): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("child transcript entry count: got %d, want 2 (prefix only)", len(entries))
	}
	if entries[0].Turn.Kind != schema.TurnUserInput || entries[0].Turn.Message.Text() != "first task" {
		t.Errorf("entries[0]: got kind=%q text=%q, want USER_INPUT %q", entries[0].Turn.Kind, entries[0].Turn.Message.Text(), "first task")
	}
	if entries[1].Turn.Kind != schema.TurnAssistant || entries[1].Turn.Message.Text() != "first reply" {
		t.Errorf("entries[1]: got kind=%q text=%q, want ASSISTANT %q", entries[1].Turn.Kind, entries[1].Turn.Message.Text(), "first reply")
	}
	data, err := os.ReadFile(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadFile(child transcript): %v", err)
	}
	if strings.Contains(string(data), "second task") {
		t.Error("child transcript must not contain the diverging user message")
	}
}

// TestForkSessionAtUserTurn_FirstTurnProducesEmptyChild verifies that forking
// at the very first user message yields a child with no transcript entries —
// the conversation rewinds to before anything was entered.
func TestForkSessionAtUserTurn_FirstTurnProducesEmptyChild(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	childID, originalInput, err := ForkSessionAtUserTurn(stateDir, parentID, 1, "")
	if err != nil {
		t.Fatalf("ForkSessionAtUserTurn: %v", err)
	}
	if originalInput != "first task" {
		t.Errorf("originalInput: got %q, want %q", originalInput, "first task")
	}
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	_, entries, _, err := readTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("readTranscript(child): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("child transcript entry count: got %d, want 0", len(entries))
	}
}

// TestForkSessionAtUserTurn_RejectsNonUserDivergence verifies the divergence
// turn must point at a USER_INPUT entry, matching ForkSession semantics.
func TestForkSessionAtUserTurn_RejectsNonUserDivergence(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	_, _, err := ForkSessionAtUserTurn(stateDir, parentID, 2, "")
	if err == nil {
		t.Fatal("ForkSessionAtUserTurn(divergenceTurn=2, an ASSISTANT entry) should return an error")
	}
	if !strings.Contains(err.Error(), "USER_INPUT") {
		t.Errorf("error should mention USER_INPUT, got %v", err)
	}
}

// TestForkSessionAtUserTurn_RejectsOutOfRange verifies divergenceTurn=0 and
// divergenceTurn beyond the parent's entry count both error.
func TestForkSessionAtUserTurn_RejectsOutOfRange(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	if _, _, err := ForkSessionAtUserTurn(stateDir, parentID, 0, ""); err == nil {
		t.Error("ForkSessionAtUserTurn(divergenceTurn=0) should return an error")
	}
	if _, _, err := ForkSessionAtUserTurn(stateDir, parentID, 10, ""); err == nil {
		t.Error("ForkSessionAtUserTurn(divergenceTurn=10) should return an error when the parent has 4 entries")
	}
}

// TestForkSessionAtUserTurn_RejectsMissingParent verifies a missing parent
// transcript errors instead of creating a child.
func TestForkSessionAtUserTurn_RejectsMissingParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	if _, _, err := ForkSessionAtUserTurn(stateDir, "NONEXISTENT_SESSION", 1, ""); err == nil {
		t.Error("ForkSessionAtUserTurn with missing parent should return an error")
	}
}
