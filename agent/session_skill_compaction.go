package agent

import (
	"context"
	"fmt"
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// Origins and phases of a schema.SkillCompactionOperation.
const (
	skillCompactionOriginForced    = "forced"    // the compact_context tool
	skillCompactionOriginAutomatic = "automatic" // note-elicitation acceptance
	skillCompactionPhasePending    = "pending"
	skillCompactionPhasePublished  = "published"
)

// Cancellation reasons accepted by cancelSkillCompaction.
const (
	// skillCompactionCancelSupersededNote retires the pending automatic
	// operation associated with a note an explicit clear or replacement
	// retired. Clearing or replacing a note never cancels a forced operation.
	skillCompactionCancelSupersededNote = "superseded_note"
	// skillCompactionCancelForcedNotPublished retires a forced operation whose
	// owning fold never published. Declared here so the reason set is
	// complete; the generation-matched publication claim that issues it is the
	// fold transaction's (Task 8), not this file's.
	skillCompactionCancelForcedNotPublished = "forced_not_published"
	// skillCompactionCancelSaveFailed retires an accepted operation whose
	// metadata save failed, so no later autosave can expose an unsaved
	// success and no dispatch treats the intent as durable.
	skillCompactionCancelSaveFailed = "save_failed"
)

// skillCompactionSaveError is the typed outcome of an accepted compaction
// operation whose metadata save failed. It retains the failed operation's
// generation and retirement reason; the request is retryable because the
// operation was retired from memory, leaving the cycle free.
type skillCompactionSaveError struct {
	Generation uint64
	Reason     string
	Err        error
}

func (e *skillCompactionSaveError) Error() string {
	return fmt.Sprintf("compaction operation %d not durable (%s): %v", e.Generation, e.Reason, e.Err)
}

func (e *skillCompactionSaveError) Unwrap() error { return e.Err }

// pendingSkillCompactionSnapshot returns a detached copy of the operation
// owning the current compaction cycle, or nil when none is outstanding
// (either phase counts: an undelivered published operation still owns the
// cycle's elicitation latch).
func (s *Session) pendingSkillCompactionSnapshot() *schema.SkillCompactionOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillLifecycle.PendingCompaction == nil {
		return nil
	}
	op := *s.skillLifecycle.PendingCompaction
	op.Selection.Names = slices.Clone(op.Selection.Names)
	return &op
}

// skillCompactionCancelNotice emits the visible retirement notice. It names
// the operation's generation, origin, and discarded selection, and never
// claims no other compaction occurred.
func (s *Session) skillCompactionCancelNotice(op schema.SkillCompactionOperation, reason string) {
	lostSelection := ""
	if op.Selection.State != "absent" {
		lostSelection = fmt.Sprintf("; its reload selection (%s) was discarded", op.Selection.State)
	}
	s.emit(events.EventWarning, events.WarningData{
		Message: fmt.Sprintf("compaction intent retired (generation %d, origin %s, reason %s)%s — retiring the intent does not undo any compaction that already ran",
			op.Generation, op.Origin, reason, lostSelection),
	})
}

// cancelSkillCompactionLocked retires the pending operation with the given
// generation, together with the reload-selection slot that operation owned.
// A missing or generation-mismatched slot is left untouched. Callers hold
// s.mu; the returned copy is detached, nil when nothing was cancelled.
func (s *Session) cancelSkillCompactionLocked(generation uint64) *schema.SkillCompactionOperation {
	op := s.skillLifecycle.PendingCompaction
	if op == nil || op.Generation != generation {
		return nil
	}
	s.skillLifecycle.PendingCompaction = nil
	// The cancelled operation owned the selection slot: a cancelled intent
	// must not leak its selection into the next compaction cycle.
	s.skillLifecycle.PendingSelection = nil
	s.skillLifecycle.Revision++
	cancelled := *op
	cancelled.Selection.Names = slices.Clone(op.Selection.Names)
	return &cancelled
}

// cancelSkillCompaction retires the pending compaction operation with the
// given generation for the given reason, emits the visible retirement notice,
// and persists the cancellation. Cancelling a missing or generation-mismatched
// operation is a no-op. A failed persistence save is reported (and warned)
// rather than silently dropped, so a restart can reconcile the stale record.
func (s *Session) cancelSkillCompaction(ctx context.Context, generation uint64, reason string) error {
	_ = ctx
	s.mu.Lock()
	cancelled := s.cancelSkillCompactionLocked(generation)
	s.mu.Unlock()
	if cancelled == nil {
		return nil
	}
	s.skillCompactionCancelNotice(*cancelled, reason)
	if err := s.saveMeta(); err != nil {
		s.emit(events.EventWarning, warningDataFromError("persisting compaction cancellation failed", err))
		return err
	}
	return nil
}

// requestSkillCompaction records the compact tool's intent as a generation-
// owned compaction operation, atomically with the note it carries.
//
// An empty note with no instructions and an absent selection is the explicit
// clear: it clears the pinned note without requesting a compaction and
// returns generation 0, cancelling only the pending AUTOMATIC operation the
// cleared note generation was associated with — a forced operation (and its
// round-tail trigger) survives a clear.
//
// Otherwise the request mints the next operation generation, stores the note
// and its owning operation, arms the transient round-tail trigger, and
// persists the transition. A request over an existing FORCED operation, or any
// operation already in delivery, is rejected without mutating note,
// selection, or operation; a request over a pending AUTOMATIC operation
// supersedes it (the replacement retires the automatic acceptance with
// superseded_note). A failed metadata save retires the just-created operation
// (save_failed) together with its round trigger and returns a typed
// *skillCompactionSaveError — an unsaved success is never exposed, and the
// slot is left free so the caller can retry.
func (s *Session) requestSkillCompaction(ctx context.Context, note, instructions string, selection schema.SkillReloadSelection) (uint64, error) {
	clearOnly := note == "" && instructions == "" && selection.State == "absent"
	s.mu.Lock()
	existing := s.skillLifecycle.PendingCompaction
	if existing != nil && !clearOnly {
		switch {
		case existing.Phase == skillCompactionPhasePublished:
			// Delivery owns the slot until it completes; a new intent must wait.
			s.mu.Unlock()
			return 0, fmt.Errorf("compaction operation %d (%s) is still being delivered", existing.Generation, existing.Origin)
		case existing.Origin == skillCompactionOriginForced:
			// Distinct intents are never silently clobbered; nothing is mutated.
			s.mu.Unlock()
			return 0, fmt.Errorf("a forced compaction operation is already pending (generation %d)", existing.Generation)
		}
		// A pending automatic operation is superseded by this explicit request.
	}
	if clearOnly {
		s.pinnedNote = ""
		s.pinnedNoteGen++
		var cancelled *schema.SkillCompactionOperation
		if existing != nil && existing.Origin == skillCompactionOriginAutomatic && existing.Phase == skillCompactionPhasePending {
			// Clearing a note cancels only its associated automatic operation.
			cancelled = s.cancelSkillCompactionLocked(existing.Generation)
		}
		s.mu.Unlock()
		if cancelled != nil {
			s.skillCompactionCancelNotice(*cancelled, skillCompactionCancelSupersededNote)
			if err := s.saveMeta(); err != nil {
				return 0, &skillCompactionSaveError{Generation: cancelled.Generation, Reason: skillCompactionCancelSaveFailed, Err: err}
			}
		}
		return 0, nil
	}
	gen := s.skillLifecycle.NextOperationGen + 1
	s.skillLifecycle.NextOperationGen = gen
	var superseded *schema.SkillCompactionOperation
	if existing != nil {
		// The replacing note supersedes the pending automatic acceptance.
		superseded = s.cancelSkillCompactionLocked(existing.Generation)
	}
	s.pinnedNote = note
	s.pinnedNoteGen++
	op := &schema.SkillCompactionOperation{
		Generation:     gen,
		Origin:         skillCompactionOriginForced,
		Instructions:   instructions,
		NoteGeneration: s.pinnedNoteGen,
		Selection:      schema.SkillReloadSelection{State: selection.State, Names: slices.Clone(selection.Names), ErrorCode: selection.ErrorCode},
		Phase:          skillCompactionPhasePending,
	}
	s.skillLifecycle.PendingCompaction = op
	s.skillLifecycle.Revision++
	// The round-tail trigger is transient; the persisted operation is the
	// durable instruction owner the tail reads.
	s.forceRequested = true
	s.pendingInstructions = instructions
	s.mu.Unlock()
	if superseded != nil {
		s.skillCompactionCancelNotice(*superseded, skillCompactionCancelSupersededNote)
	}
	// The selection slot is stored after the ownership critical section (same
	// turn goroutine, before the save) exactly as Task 6 recorded it.
	s.setPendingSkillReloadSelection(selection)
	if err := s.saveMeta(); err != nil {
		// Retire the operation and its trigger: no dispatch may treat an
		// unsaved intent as durable, and the caller must be able to retry.
		s.mu.Lock()
		s.forceRequested = false
		s.pendingInstructions = ""
		s.mu.Unlock()
		_ = s.cancelSkillCompaction(ctx, gen, skillCompactionCancelSaveFailed)
		return gen, &skillCompactionSaveError{Generation: gen, Reason: skillCompactionCancelSaveFailed, Err: err}
	}
	return gen, nil
}

// acceptAutomaticSkillCompaction records an elicited compaction response as a
// pending automatic operation, atomically with the note it pinned. The guard
// is the brief's exact rule: the acceptance is rejected (false, nil) when the
// note generation changed since the caller captured it BEFORE the elicitor
// call, or when any operation already owns the cycle. Because a rejected
// acceptance stores nothing, the guard is also the first-wins rule for the
// selection: a later elicitation can never overwrite a concrete selection the
// cycle already owns.
//
// On a failed metadata save the operation is retired (save_failed) and a
// typed *skillCompactionSaveError is returned with accepted=false; the
// elicited note text itself stays pinned in memory, the same best-effort
// pinning a successful elicitation had before this persistence existed.
func (s *Session) acceptAutomaticSkillCompaction(ctx context.Context, capturedNoteGen uint64, note string, selection schema.SkillReloadSelection) (bool, error) {
	s.mu.Lock()
	if s.pinnedNoteGen != capturedNoteGen || s.skillLifecycle.PendingCompaction != nil {
		s.mu.Unlock()
		return false, nil
	}
	gen := s.skillLifecycle.NextOperationGen + 1
	s.skillLifecycle.NextOperationGen = gen
	s.pinnedNote = note
	s.pinnedNoteGen++
	op := &schema.SkillCompactionOperation{
		Generation:     gen,
		Origin:         skillCompactionOriginAutomatic,
		NoteGeneration: s.pinnedNoteGen,
		Selection:      schema.SkillReloadSelection{State: selection.State, Names: slices.Clone(selection.Names), ErrorCode: selection.ErrorCode},
		Phase:          skillCompactionPhasePending,
	}
	s.skillLifecycle.PendingCompaction = op
	s.skillLifecycle.Revision++
	s.mu.Unlock()
	if selection.State != "absent" {
		s.setPendingSkillReloadSelection(selection)
	}
	if err := s.saveMeta(); err != nil {
		_ = s.cancelSkillCompaction(ctx, gen, skillCompactionCancelSaveFailed)
		return false, &skillCompactionSaveError{Generation: gen, Reason: skillCompactionCancelSaveFailed, Err: err}
	}
	return true, nil
}

// resumeSkillCompaction restores a persisted compaction operation's runtime
// side effects after a restart. Restart is not cancellation:
//
//   - a pending FORCED operation re-arms the transient round-tail trigger from
//     the operation's own instructions, so the next round tail dispatches the
//     fold the crash interrupted;
//   - a pending AUTOMATIC operation needs no action: its restored record is
//     itself the elicitation latch, and it waits for normal pressure;
//   - a published operation is delivery-only — no re-arm, no cancellation;
//   - missing legacy lifecycle metadata resumes with fresh state and no
//     history backfill.
func (s *Session) resumeSkillCompaction(ctx context.Context) error {
	_ = ctx // reserved for the publication-claim path (Task 8); resume is pure memory
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.skillLifecycle.PendingCompaction
	if op == nil || op.Phase != skillCompactionPhasePending || op.Origin != skillCompactionOriginForced {
		return nil
	}
	s.forceRequested = true
	s.pendingInstructions = op.Instructions
	return nil
}
