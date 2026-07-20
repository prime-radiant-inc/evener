package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// buildAsideParentSession creates a parent session with a 4-entry transcript
// (USER, ASSISTANT, USER, ASSISTANT) and a meta carrying a distinctive config
// (sandbox mode + tool-round cap) so tests can assert the aside child inherits
// the parent's permissions and configuration verbatim.
func buildAsideParentSession(t *testing.T) (stateDir, parentID string) {
	t.Helper()
	stateDir, parentID = buildParentSession(t)

	meta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	meta.Config.Sandbox = "workspace-write"
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta(parent): %v", err)
	}
	return stateDir, parentID
}

// TestAsideSession_CopiesFullTranscriptAtTip verifies the aside contract: the
// child is a complete copy of the parent transcript — including the trailing
// assistant turn a divergent fork cannot end on — and inherits the parent's
// profile, model, and config (permissions) through its meta.
func TestAsideSession_CopiesFullTranscriptAtTip(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildAsideParentSession(t)

	childID, err := AsideSession(stateDir, parentID)
	if err != nil {
		t.Fatalf("AsideSession: %v", err)
	}
	if err := identifier.ValidateSessionID(childID); err != nil {
		t.Fatalf("child session ID %q: %v", childID, err)
	}
	if childID == parentID {
		t.Fatalf("childID should differ from parentID, got %q", childID)
	}

	// Child transcript is the full parent transcript: 4 entries ending on the
	// final ASSISTANT turn.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	header, entries, _, err := readTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("readTranscript(child): %v", err)
	}
	if header.ParentSessionID != parentID {
		t.Errorf("child header ParentSessionID: got %q, want %q", header.ParentSessionID, parentID)
	}
	if len(entries) != 4 {
		t.Fatalf("child transcript entry count: got %d, want 4", len(entries))
	}
	last := entries[len(entries)-1]
	if last.Turn.Kind != schema.TurnAssistant || last.Turn.Message.Text() != "second reply" {
		t.Errorf("last child entry: got kind=%q text=%q, want assistant %q",
			last.Turn.Kind, last.Turn.Message.Text(), "second reply")
	}

	// Child meta: lineage at the tip, inherited config, fresh counters derived
	// from the copied entries.
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Errorf("child ParentSessionID: got %q, want %q", childMeta.ParentSessionID, parentID)
	}
	if childMeta.DivergenceTurn != 5 {
		t.Errorf("child DivergenceTurn: got %d, want 5 (first turn past the shared tip)", childMeta.DivergenceTurn)
	}
	if childMeta.TurnCount != 2 {
		t.Errorf("child TurnCount: got %d, want 2", childMeta.TurnCount)
	}
	if childMeta.AcceptedInputTurns != 2 {
		t.Errorf("child AcceptedInputTurns: got %d, want 2", childMeta.AcceptedInputTurns)
	}
	if childMeta.ProfileID != "openai" || childMeta.Model != "gpt-5.2" {
		t.Errorf("child profile/model: got %q/%q, want openai/gpt-5.2", childMeta.ProfileID, childMeta.Model)
	}
	if childMeta.Config.Sandbox != "workspace-write" {
		t.Errorf("child sandbox mode: got %q, want %q (inherited permissions)", childMeta.Config.Sandbox, "workspace-write")
	}
	if childMeta.Config.MaxToolRoundsPerInput != 50 {
		t.Errorf("child MaxToolRoundsPerInput: got %d, want 50", childMeta.Config.MaxToolRoundsPerInput)
	}
	if childMeta.ForkLabel != "" {
		t.Errorf("child ForkLabel should be empty, got %q", childMeta.ForkLabel)
	}

	// The parent meta is untouched: an aside does not relabel the main branch.
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	if parentMeta.ForkLabel != "" {
		t.Errorf("parent ForkLabel: got %q, want empty (aside leaves the parent untouched)", parentMeta.ForkLabel)
	}
}

// TestAsideSession_ChildRestoresAsSideThread proves the aside child is a
// resumable session whose lineage survives the restore+meta-rewrite cycle,
// i.e. it stays addressable as a child of the main session in the tree.
func TestAsideSession_ChildRestoresAsSideThread(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildAsideParentSession(t)

	childID, err := AsideSession(stateDir, parentID)
	if err != nil {
		t.Fatalf("AsideSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}

	sess, err := RestoreSessionFromMeta(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), childMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ParentSessionID != parentID {
		t.Errorf("rewritten ParentSessionID: got %q, want %q", rewritten.ParentSessionID, parentID)
	}
	if rewritten.DivergenceTurn != 5 {
		t.Errorf("rewritten DivergenceTurn: got %d, want 5", rewritten.DivergenceTurn)
	}
	if rewritten.Config.Sandbox != "workspace-write" {
		t.Errorf("rewritten sandbox mode: got %q, want inherited %q", rewritten.Config.Sandbox, "workspace-write")
	}
}

// TestAsideSession_EmptyParentTranscript pins behavior for a session with no
// turns yet: the aside child is an empty copy diverging at turn 1.
func TestAsideSession_EmptyParentTranscript(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	parentID := "01PARENTEMPTY0000000000AS"

	tw, err := transcript.NewWriter(filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl"), transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        parentID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	childID, err := AsideSession(stateDir, parentID)
	if err != nil {
		t.Fatalf("AsideSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.DivergenceTurn != 1 || childMeta.TurnCount != 0 || childMeta.AcceptedInputTurns != 0 {
		t.Errorf("child meta divergence/turns: got %d/%d/%d, want 1/0/0",
			childMeta.DivergenceTurn, childMeta.TurnCount, childMeta.AcceptedInputTurns)
	}
}

// TestAsideSession_RejectsMissingParent verifies a missing parent transcript
// returns an error instead of creating an orphaned child.
func TestAsideSession_RejectsMissingParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	childID, err := AsideSession(stateDir, "NONEXISTENT_SESSION")
	if err == nil {
		t.Error("AsideSession with missing parent should return an error")
	}
	if childID != "" {
		t.Errorf("AsideSession child id = %q, want empty on error", childID)
	}
	if entries, _ := os.ReadDir(filepath.Join(stateDir, sessionsSubdir)); len(entries) != 0 {
		t.Errorf("aside created artifacts for a missing parent: %v", entries)
	}
}
