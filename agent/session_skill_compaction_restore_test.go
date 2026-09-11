package agent

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// restoreSkillCompactionSession resumes the session persisted under stateDir
// through the real restore path (LoadSessionMeta + RestoreSessionFromMeta),
// the same plumbing a restart uses.
func restoreSkillCompactionSession(t *testing.T, stateDir, id string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	return restored
}

// TestSkillCompactionRestore_ForcedOperationReArmedBeforeDispatch: an
// unpublished forced operation persisted before the round tail ever dispatched
// must resume with its transient round trigger re-armed from the operation's
// own instructions, so the next round tail dispatches the fold. Restart is not
// cancellation: the operation itself stays pending.
func TestSkillCompactionRestore_ForcedOperationReArmedBeforeDispatch(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	if _, err := s.requestSkillCompaction(context.Background(), "opaque-note", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{}}); err != nil {
		t.Fatal(err)
	}
	// Crash before the round tail ran: no fold, no publication, no consumption.
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	instructions, armed := restored.takeForceRequest()
	if !armed || instructions != "opaque-instructions" {
		t.Fatalf("restored forced operation must re-arm the round trigger from its instructions, got %q, %v", instructions, armed)
	}
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Origin != "forced" || op.Phase != "pending" || op.Instructions != "opaque-instructions" {
		t.Fatalf("restored operation = %+v, want the pending forced operation", op)
	}
}

// TestSkillCompactionRestore_AutomaticLatchRestoredWithoutForceFold: a
// persisted pending automatic operation restores its elicitation latch through
// the restored operation itself — no transient trigger is armed, no fold is
// forced, and pressure alone must not re-elicit over it.
func TestSkillCompactionRestore_AutomaticLatchRestoredWithoutForceFold(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	// Selection-only acceptance keeps the note empty, so only the restored
	// operation can latch a later elicitation.
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil || !accepted {
		t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
	}
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("an automatic operation must restore without forcing a fold")
	}
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Origin != "automatic" || op.Phase != "pending" {
		t.Fatalf("restored automatic operation = %+v", op)
	}

	called := false
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "ELICITED — MUST NOT FIRE", nil
	}
	seedSessionHistory(t, restored, 10)
	forcePressureAbove(t, restored, restored.contextMgr.CheckpointThreshold)
	before := len(currentHistory(t, restored))
	restored.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, restored), 0)
	if called {
		t.Fatal("the restored automatic operation must latch elicitation")
	}
	restored.applyPendingForceCompact(context.Background())
	if len(currentHistory(t, restored)) != before {
		t.Fatal("restoring an automatic operation must not force a fold")
	}
}

// TestSkillCompactionRestore_PublishedOperationIsDeliveryOnly: a persisted
// operation already in the published phase is delivery-only — restart neither
// cancels it nor re-arms a fold — while it still latches elicitation until its
// delivery completes.
func TestSkillCompactionRestore_PublishedOperationIsDeliveryOnly(t *testing.T) {
	meta := schema.SessionMeta{
		ID:        "resume-published-compaction",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Skills: &schema.SkillLifecycleSnapshot{
			Inventory:        map[string]schema.SkillInventoryEntry{},
			NextOperationGen: 7,
			PinnedNoteGen:    3,
			PendingCompaction: &schema.SkillCompactionOperation{
				Generation:     7,
				Origin:         "forced",
				Instructions:   "opaque-instructions",
				NoteGeneration: 3,
				Selection:      schema.SkillReloadSelection{State: "valid", Names: []string{}},
				Phase:          "published",
				PublicationID:  "pub-opaque-1",
			},
		},
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("a published operation is delivery-only: restart must not re-arm a fold")
	}
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Phase != "published" || op.PublicationID != "pub-opaque-1" {
		t.Fatalf("restart is not cancellation: published operation = %+v", op)
	}

	called := false
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "ELICITED — MUST NOT FIRE", nil
	}
	seedSessionHistory(t, restored, 10)
	forcePressureAbove(t, restored, restored.contextMgr.CheckpointThreshold)
	restored.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, restored), 0)
	if called {
		t.Fatal("an undelivered published operation must still latch elicitation")
	}
}

// TestSkillCompactionRestore_MissingLegacyMetadataResumesWithoutBackfill: a
// session persisted before lifecycle metadata existed resumes with fresh
// compaction state — no operation is reconstructed from history, no trigger is
// armed, and elicitation behaves as in a brand-new session.
func TestSkillCompactionRestore_MissingLegacyMetadataResumesWithoutBackfill(t *testing.T) {
	meta := schema.SessionMeta{
		ID:        "resume-legacy-compaction",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		// Skills nil: legacy sessions predate lifecycle metadata.
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, t.TempDir())
	if err != nil {
		t.Fatalf("legacy session must resume without lifecycle metadata: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	if restored.pendingSkillCompactionSnapshot() != nil {
		t.Fatal("no compaction operation may be backfilled for a legacy session")
	}
	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("a legacy session must not arm a compaction")
	}

	called := false
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "STUB legacy note", nil
	}
	seedSessionHistory(t, restored, 10)
	forcePressureAbove(t, restored, restored.contextMgr.CheckpointThreshold)
	restored.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, restored), 0)
	if !called {
		t.Fatal("a legacy session must elicit as a fresh session does — no phantom latch")
	}
	if got := restored.PinnedNote(); got != "STUB legacy note" {
		t.Fatalf("legacy session note = %q", got)
	}
}

// TestSkillCompactionRestore_PendingSelectionRoundTrip carries the Task 6
// deferred minor: the pending reload selection recorded with a compaction
// request survives save and the real restart path.
func TestSkillCompactionRestore_PendingSelectionRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	if _, err := s.requestSkillCompaction(context.Background(), "opaque-note", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	sel := restored.pendingSkillReloadSelection()
	if sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("pending selection = %+v, want valid [scope:probe] across restart", sel)
	}
}
