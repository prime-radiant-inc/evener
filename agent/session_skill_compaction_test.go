package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// TestSkillCompaction_AcceptanceSurvivesMetadataRoundTrip proves a skill-only
// empty selection accepted by requestSkillCompaction is persisted through the
// REAL StateDir incremental persistence: the operation must be readable from
// disk (LoadSessionMeta) immediately after the call, before any compaction
// runs, with its minted generation, forced origin, pending phase and valid
// empty selection intact.
func TestSkillCompaction_AcceptanceSurvivesMetadataRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	generation, err := s.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skills == nil || loaded.Skills.PendingCompaction == nil {
		t.Fatal("operation not saved")
	}
	pending := loaded.Skills.PendingCompaction
	if pending.Generation != generation || pending.Origin != "forced" || pending.Phase != "pending" || pending.Selection.State != "valid" {
		t.Fatalf("pending=%+v", pending)
	}
}

// TestSkillCompaction_AcceptanceAutomaticSelectionOnlyBeforePublication: an
// automatic (elicited) selection-only acceptance with an explicit empty list
// must be persisted as a pending automatic operation before any publication —
// no fold runs as part of acceptance.
func TestSkillCompaction_AcceptanceAutomaticSelectionOnlyBeforePublication(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("selection-only automatic acceptance must be accepted")
	}
	loaded, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skills == nil || loaded.Skills.PendingCompaction == nil {
		t.Fatal("automatic acceptance not saved")
	}
	pending := loaded.Skills.PendingCompaction
	if pending.Origin != "automatic" || pending.Phase != "pending" {
		t.Fatalf("pending=%+v, want an unpublished automatic operation", pending)
	}
	if pending.Selection.State != "valid" || len(pending.Selection.Names) != 0 {
		t.Fatalf("pending selection = %+v, want a valid empty list", pending.Selection)
	}
	_, liveNoteGen := s.pinnedNoteSnapshot()
	if pending.NoteGeneration != liveNoteGen {
		t.Fatalf("operation note generation = %d, want the live pinned note generation %d", pending.NoteGeneration, liveNoteGen)
	}
	if len(currentHistory(t, s)) != 0 {
		t.Fatal("acceptance must not run a compaction")
	}
}

// TestSkillCompaction_AcceptanceSecondRequestRejected: a second request while
// a forced operation is pending is rejected without minting a generation, and
// the first request's note, selection and operation stand unchanged.
func TestSkillCompaction_AcceptanceSecondRequestRejected(t *testing.T) {
	s := newTestSession(t)
	first, err := s.requestSkillCompaction(context.Background(), "first", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.requestSkillCompaction(context.Background(), "second", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err == nil {
		t.Fatal("a second request while an operation is pending must be rejected")
	}
	if second != 0 {
		t.Fatalf("rejected request must not mint a generation, got %d", second)
	}
	if got := s.PinnedNote(); got != "first" {
		t.Fatalf("rejected request mutated the note: %q", got)
	}
	sel := s.pendingSkillReloadSelection()
	if sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("rejected request mutated the selection: %+v", sel)
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != first || op.Origin != "forced" {
		t.Fatalf("pending operation = %+v, want the first forced operation unchanged", op)
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("the accepted first request's round trigger must stand")
	}
}

// TestSkillCompaction_AcceptanceForcedRequestSupersedesAutomaticOperation:
// replacing the note with an explicit request retires the pending automatic
// acceptance (superseded_note) — its selection is discarded, never leaked into
// the new cycle — and installs the forced operation.
func TestSkillCompaction_AcceptanceForcedRequestSupersedesAutomaticOperation(t *testing.T) {
	s := newTestSession(t)
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "automatic-note",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil || !accepted {
		t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
	}
	gen, err := s.requestSkillCompaction(context.Background(), "replacing-note", "opaque-instructions",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("a forced request must supersede a pending automatic operation: %v", err)
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != gen || op.Origin != "forced" || op.Instructions != "opaque-instructions" {
		t.Fatalf("pending operation = %+v, want the superseding forced operation", op)
	}
	if got := s.PinnedNote(); got != "replacing-note" {
		t.Fatalf("replacing note = %q", got)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("the superseded automatic operation's selection must be discarded, got %+v", sel)
	}
}

// TestSkillCompaction_StaleCapturedGenerationRejected: an automatic
// acceptance carrying a note generation captured before the note changed is
// rejected, storing nothing.
func TestSkillCompaction_StaleCapturedGenerationRejected(t *testing.T) {
	s := newTestSession(t)
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	s.setPinnedNote("changed mid-elicitation")
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "elicited",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("a stale captured note generation must be rejected")
	}
	if got := s.PinnedNote(); got != "changed mid-elicitation" {
		t.Fatalf("rejected acceptance mutated the note: %q", got)
	}
	if s.pendingSkillCompactionSnapshot() != nil {
		t.Fatal("rejected acceptance must not create an operation")
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("rejected acceptance must not store a selection: %+v", sel)
	}
}

// TestSkillCompaction_StaleAcceptanceRejectedWhileOperationPending: the
// acceptance guard's second arm — an automatic response is superseded when an
// operation (here a forced one) already owns the cycle.
func TestSkillCompaction_StaleAcceptanceRejectedWhileOperationPending(t *testing.T) {
	s := newTestSession(t)
	if _, err := s.requestSkillCompaction(context.Background(), "forced-note", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatal(err)
	}
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "automatic",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("an acceptance while an operation is already pending must be rejected")
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Origin != "forced" || op.NoteGeneration != capturedNoteGen {
		t.Fatalf("pending operation = %+v, want the original forced operation untouched", op)
	}
}

// TestSkillCompaction_LatchNonemptyNoteSkipsElicitation pins the nonempty-note
// latch: high pressure must not re-elicit over an already-pinned note.
func TestSkillCompaction_LatchNonemptyNoteSkipsElicitation(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "ELICITED — MUST NOT REPLACE", nil
	}
	s.setPinnedNote("AGENT'S OWN NOTE")
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if called {
		t.Fatal("elicitor must not be called when a note is already set")
	}
	if got := s.PinnedNote(); got != "AGENT'S OWN NOTE" {
		t.Fatalf("agent note must be preserved, got %q", got)
	}
}

// TestSkillCompaction_LatchPendingOperationSkipsElicitation pins the
// pending-operation latch: with an empty note but a pending compaction
// operation, high pressure must not fire the elicitor at all.
func TestSkillCompaction_LatchPendingOperationSkipsElicitation(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "ELICITED — MUST NOT FIRE", nil
	}
	if _, err := s.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}}); err != nil {
		t.Fatal(err)
	}
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	if called {
		t.Fatal("a pending compaction operation must latch elicitation")
	}
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("empty note must stay empty, got %q", got)
	}
}

// TestSkillCompaction_LatchFirstSelectionWins pins the first-wins rule for the
// deferred Task 6 question (invalid-elicitation-overwrites-valid): once a
// concrete reload selection is pending for the current compaction cycle, a
// later elicitation in the same cycle must not overwrite it — not even to
// replace it with an invalid one. A selection-only first elicitation leaves no
// pinned note, so only the pending-operation latch can hold the cycle.
func TestSkillCompaction_LatchFirstSelectionWins(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	calls := 0
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		calls++
		if calls == 1 {
			return `<skill-reload-selection>{"reload_skills":["scope:probe"]}</skill-reload-selection>`, nil
		}
		return "later note\n<skill-reload-selection>{\"reload_skills\":[\"scope:missing\"]}</skill-reload-selection>", nil
	}
	seedSessionHistory(t, s, 10)
	forcePressureAbove(t, s, s.contextMgr.CheckpointThreshold)

	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)
	s.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, s), 0)

	if calls != 1 {
		t.Fatalf("second elicitation in the same cycle must be latched, elicitor called %d times", calls)
	}
	sel := s.pendingSkillReloadSelection()
	if sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("pending selection = %+v, want the first valid selection preserved", sel)
	}
}

// TestSkillCompaction_ClearNoteCancelsAutomaticOperation: an explicit clear
// (empty note, no instructions, absent selection) cancels the associated
// pending automatic operation — and its selection — durably, without
// requesting a compaction or minting an operation generation.
func TestSkillCompaction_ClearNoteCancelsAutomaticOperation(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "automatic-note",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil || !accepted {
		t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
	}
	loaded, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skills == nil || loaded.Skills.PendingCompaction == nil {
		t.Fatal("test setup: automatic operation not saved")
	}

	generation, err := s.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if generation != 0 {
		t.Fatalf("clear must not mint an operation generation, got %d", generation)
	}
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("clear must empty the note, got %q", got)
	}
	if s.pendingSkillCompactionSnapshot() != nil {
		t.Fatal("clear must cancel the associated automatic operation")
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("the cancelled operation's selection must be discarded, got %+v", sel)
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("clear must not request a compaction")
	}
	loaded, err = schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skills == nil || loaded.Skills.PendingCompaction != nil {
		t.Fatal("the cancellation must be persisted: operation still on disk")
	}
	if loaded.PinnedNote != "" {
		t.Fatalf("the cleared note must be persisted empty, got %q", loaded.PinnedNote)
	}
}

// TestSkillCompaction_ClearNoteLeavesForcedOperation: a clear cancels only its
// associated automatic operation — a pending forced operation (and its
// round-tail trigger) survives the note clear.
func TestSkillCompaction_ClearNoteLeavesForcedOperation(t *testing.T) {
	s := newTestSession(t)
	gen, err := s.requestSkillCompaction(context.Background(), "keep", "opaque-instructions",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatal(err)
	}
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("note must be cleared, got %q", got)
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != gen || op.Origin != "forced" || op.Instructions != "opaque-instructions" {
		t.Fatalf("clear must not cancel a forced operation, got %+v", op)
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("the forced operation's round trigger must survive the note clear")
	}
}

// TestSkillCompaction_ClearNotePersistsWithoutPendingOperation: a clear-only
// request that cancels NO operation (the note's operation already retired,
// e.g. delivered but not yet fold-consumed) mutates the durable pinned note —
// it must be persisted, or the stale note reappears after a restart.
func TestSkillCompaction_ClearNotePersistsWithoutPendingOperation(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	gen, err := s.requestSkillCompaction(context.Background(), "keep", "opaque-instructions",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	// Retire the pending operation while the note stays pinned (a delivered
	// operation leaves the note pinned until a fold consumes it).
	if err := s.cancelSkillCompaction(context.Background(), gen, "test"); err != nil {
		t.Fatal(err)
	}
	if got := s.PinnedNote(); got != "keep" {
		t.Fatalf("note must still be pinned before the clear, got %q", got)
	}
	if _, err := s.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatal(err)
	}
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("note must be cleared, got %q", got)
	}
	loaded, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PinnedNote != "" {
		t.Fatalf("the cleared note must be persisted empty even when no operation was cancelled, got %q", loaded.PinnedNote)
	}
}

// breakSessionMetaPath replaces the session's real meta.json path with a
// directory so the real filesystem refuses every subsequent metadata write,
// and returns the repair that restores writability.
func breakSessionMetaPath(t *testing.T, s *Session) func() {
	t.Helper()
	metaPath := filepath.Join(s.stateDir, sessionsSubdir, s.Meta().ID+".meta.json")
	if err := os.Remove(metaPath); err != nil {
		t.Fatalf("remove meta file: %v", err)
	}
	if err := os.Mkdir(metaPath, 0o755); err != nil {
		t.Fatalf("break meta path: %v", err)
	}
	return func() { _ = os.Remove(metaPath) }
}

// TestSkillCompaction_SaveFailedRequestIsTypedAndRetryable: a real-filesystem
// save failure must surface a typed outcome (never an unsaved success), retire
// the operation in memory so dispatch cannot treat it as durable, and leave
// the request retryable.
func TestSkillCompaction_SaveFailedRequestIsTypedAndRetryable(t *testing.T) {
	t.Run("forced", func(t *testing.T) {
		stateDir := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
		repair := breakSessionMetaPath(t, s)

		generation, err := s.requestSkillCompaction(context.Background(), "keep", "opaque-instructions",
			schema.SkillReloadSelection{State: "valid", Names: []string{}})
		if err == nil {
			t.Fatal("a failed metadata save must not report success")
		}
		saveErr, ok := errors.AsType[*skillCompactionSaveError](err)
		if !ok {
			t.Fatalf("save failure must carry a typed outcome, got %T: %v", err, err)
		}
		if saveErr.Generation != generation {
			t.Fatalf("typed outcome generation = %d, want %d", saveErr.Generation, generation)
		}
		if saveErr.Reason != skillCompactionCancelSaveFailed {
			t.Fatalf("typed outcome reason = %q, want %q", saveErr.Reason, skillCompactionCancelSaveFailed)
		}
		if s.pendingSkillCompactionSnapshot() != nil {
			t.Fatal("an unsaved operation must not stay pending in memory")
		}
		if _, ok := s.takeForceRequest(); ok {
			t.Fatal("an unsaved operation must not arm the round-tail dispatch")
		}

		repair()
		if _, err := s.requestSkillCompaction(context.Background(), "keep", "opaque-instructions",
			schema.SkillReloadSelection{State: "valid", Names: []string{}}); err != nil {
			t.Fatalf("retry after the failure must succeed: %v", err)
		}
		if s.pendingSkillCompactionSnapshot() == nil {
			t.Fatal("the retried request must leave a pending operation")
		}
	})
	t.Run("automatic", func(t *testing.T) {
		stateDir := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
		repair := breakSessionMetaPath(t, s)
		_, capturedNoteGen := s.pinnedNoteSnapshot()

		accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "auto-note",
			schema.SkillReloadSelection{State: "valid", Names: []string{}})
		if err == nil || accepted {
			t.Fatalf("a failed metadata save must not report an accepted elicitation: accepted=%v err=%v", accepted, err)
		}
		if _, ok := errors.AsType[*skillCompactionSaveError](err); !ok {
			t.Fatalf("save failure must carry a typed outcome, got %T: %v", err, err)
		}
		if s.pendingSkillCompactionSnapshot() != nil {
			t.Fatal("an unsaved automatic operation must not stay pending in memory")
		}
		// The elicited note itself survives in memory — the same best-effort
		// pinning a successful elicitation always had before persistence
		// existed; only the durable operation record is retired.
		if got := s.PinnedNote(); got != "auto-note" {
			t.Fatalf("elicited note must survive as the best-effort pin, got %q", got)
		}

		repair()
		_, capturedNoteGen = s.pinnedNoteSnapshot()
		if accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "auto-note",
			schema.SkillReloadSelection{State: "valid", Names: []string{}}); err != nil || !accepted {
			t.Fatalf("retry after the failure must accept: accepted=%v err=%v", accepted, err)
		}
	})
}
