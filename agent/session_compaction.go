package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Compact forces context compaction regardless of current pressure.
// Runs all compaction layers (observation masking, thinking clearing,
// checkpoint, and LLM summarization). Safe to call while idle.
func (s *Session) Compact(ctx context.Context) error {
	release, admissionErr := s.beginRetirementMutation("turn")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
	// Attribute the summarizer's LLM side calls to this session in the
	// per-session API log (the per-attempt context only covers turn model calls).
	ctx = llm.WithAPILogContext(ctx, s.id)
	// Refused while a question is pending (spec §5.3): summarizing away the
	// transcript tail the pending question lives in would compact out from
	// under the user's reply. Returning before any history read/mutation
	// leaves the history and the pending question untouched — the reply or
	// Clear are the only ways forward (protecting the pending-ask tail through
	// compaction instead of refusing outright is the fast-follow). Keyed on
	// the pending-ask set (askPendingCount), not the awaiting rest state: under
	// attention-status-model v5, SessionAwaiting also covers a plain
	// output-producing rest with nothing pending, where Compact must proceed
	// normally.
	if s.askPendingCount() > 0 {
		return errors.New("a question is pending; reply or clear first")
	}

	if s.contextMgr == nil {
		return errors.New("context manager not initialized")
	}

	// /compact is server-exposed while a thread is active, and the
	// askPendingCount() guard above does not check for an active round loop —
	// so this can race another ForceCompact/ManageContext publisher
	// (applyPendingForceCompact, the content-filter retry, or the round
	// loop's own ManageContext) — and any other non-append history mutation
	// that bumps historyRevision (orphan repair, attention replacement) can
	// win the publication race just the same. foldWithForceCompact retries
	// once against the current history on conflict; report the general
	// conflict to the caller rather than silently no-op'ing if both attempts
	// lose. An explicit /compact captures no compaction operation: it must
	// never adopt a pending intent its caller did not request through.
	// A transcript that has stopped accepting records is the other
	// refusal, and it is reported as itself: telling the operator to try again
	// would send them back to a fold that can only fail the same way.
	if ok, refusal := s.foldWithForceCompact(ctx, "", nil); !ok {
		if refusal != nil {
			return refusal
		}
		return errors.New("a concurrent history change won the publication race; try again")
	}

	s.maybeAutoSave()
	return nil
}

// publishFoldedHistory publishes a fold's result, merging in any turns
// appended to s.history since the caller's snapshot — ordinary session
// activity (a tool result, a steering turn) only ever appends, so anything
// past snapLen is safe to carry forward onto the fold's result, exactly as
// prepareModelRequestWithError's ManageContext merge-back always has.
//
// It refuses to publish if a COMPETING fold already published since the
// snapshot: overwriting that fold's result would silently discard its work
// (a plain length check cannot see this: a competing fold can leave
// s.history the same length or even longer while still replacing its
// content). s.historyRevision — bumped by every successful publish here AND
// by every other non-append history mutation
// (orphaned-tool-result repair, attention-turn replace/remove, via
// bumpHistoryRevisionLocked), never by an ordinary append — is the signal:
// unchanged means neither a competing fold nor any such mutation landed, so
// the length-based merge is sound; changed means one did, and this reports
// the conflict instead of publishing.
//
// snapLen/snapRevision are s.history's length and s.historyRevision, both
// captured under s.mu at the snapshot the caller's (now-completed, unlocked)
// fold was based on. folded is the fold's own result. Returns the actually
// published history and ok=true on success (s.history and s.historyRevision
// are updated); ok=false on conflict (s.history is left untouched, folded is
// discarded) — the caller decides whether to retry the fold against the
// now-current history, or abort.
//
// The merged-in turns are, by definition, appended after everything the fold
// measured, so they never move where an earlier turn lands — a caller
// computing shrinkTurnHistoryBaseline's arguments from the fold's own
// pre/post lengths (not the published result's) needn't adjust for them.
//
// Callers must hold s.mu across this call and, on success, whatever baseline
// correction follows, so the publish and correction stay atomic relative to
// a competing fold's own publish+correction pair.
func (s *Session) publishFoldedHistory(snapLen, snapRevision int, folded []schema.Turn) (published []schema.Turn, ok bool) {
	if s.historyRevision != snapRevision {
		return nil, false
	}
	if len(s.history) > snapLen {
		folded = append(folded, s.history[snapLen:]...)
	}
	s.history = folded
	s.bumpHistoryRevisionLocked()
	return folded, true
}

// bumpHistoryRevisionLocked marks s.history as having changed in a way a
// concurrent fold's revision check must see: any mutation other than a pure
// append at the current end, which publishFoldedHistory's merge-back already
// tolerates safely (an appended turn can never be part of the prefix a fold
// snapshotted). Call this after replacing or removing an existing entry —
// orphaned-tool-result repair (which can splice a synthetic turn into the
// middle of history, not just the end) and attention-turn retain/remove (an
// in-place replacement or a deletion) are the two non-append mutation
// families in this codebase today. Without this, a fold snapshotted before
// such a change sees an unchanged revision, its equality check in
// publishFoldedHistory passes, and its publish either overwrites a same-length
// replacement or resurrects a deletion. Callers must hold s.mu.
func (s *Session) bumpHistoryRevisionLocked() {
	s.historyRevision++
}

// publishFoldTransaction commits one completed fold attempt atomically. The
// fold itself (the LLM calls, the layer work) stays optimistic and unlocked;
// only publication and commit serialize, in three nested phases:
//
//   - attentionMu — the transcript door every writeTranscript goes through —
//     is held across the publish decision AND the fold's own transcript
//     entries. A turn recorded concurrently (recordTurn: history append,
//     then transcript write) either completes entirely before this publish
//     (its entry precedes the fold's markers, and the fold's snapshot or
//     merge-back accounts for the turn itself) or has its transcript write
//     queue behind this transaction, sequencing its entry after the markers
//     — the order ResumeHistory needs, since it anchors on the LAST
//     compaction marker and discards every entry before it. A competing
//     fold's own transaction queues the same way, so compaction markers
//     always land in publish order. The transcript-commit phase also
//     re-appends the PERSISTED forms of the pairs recorded DURING the fold
//     (their original entries are already pre-marker) after the markers, so
//     they stay resume-visible too; the forms come from the session's pair
//     log — see the rewrite-set comment in the body — never from the live
//     turns.
//   - s.mu is nested inside (the codebase-wide attentionMu → s.mu order
//     writeTranscript itself established; no s.mu-holding caller can reach
//     attentionMu, since writeTranscript's internal s.mu use would already
//     self-deadlock such a caller) and held only for the memory-state
//     pieces: the revision check + history swap (publishFoldedHistory), the
//     caller's baseline correction, and the pinned-note claim — never
//     across file I/O.
//   - Everything else (events, session naming, hook user messages, the
//     nudge latch) runs after both locks release, via commit.flush: those
//     effects re-enter emit/steering/transcript machinery that itself takes
//     these locks, and none of them need the ordering guarantee. The lone
//     deliberate exception is appendEnvironmentContext's EventEnvironment,
//     which publishes under attentionMu because it DOES need the ordering
//     guarantee — the projector reads it as a turn boundary, so a live reader
//     must see it where the transcript holds it. It is one emit of known
//     content; this flush is unbounded work (naming, hook user messages) that
//     re-enters these very locks, so it stays out here.
//
// onPublishLocked, when non-nil, runs under s.mu immediately after a
// successful publish — the publisher's baseline correction, per
// publishFoldedHistory's contract. On conflict NOTHING is committed and
// ok=false; the caller retries against the now-current history or aborts.
//
// refusal names WHICH of the two ok=false outcomes happened. A publication
// race leaves it nil — that is the retryable one. A poisoned transcript sets
// it to ErrWriterPoisoned, which no retry can clear: the writer refuses every
// append for the rest of the session, so a caller that retries a fold against
// it burns another summarizer call to lose the same way, and a caller that
// reports the loss must not call it a race the operator can win.
// The returned published slice is a defensive copy taken under s.mu, never
// s.history's own backing array — callers may read it without locks.
func (s *Session) publishFoldTransaction(snapLen, snapRevision, snapAppends int, folded []schema.Turn, commit *foldCommit, onPublishLocked func(published []schema.Turn)) (published []schema.Turn, ok bool, refusal error) {
	s.attentionMu.Lock()
	// Fail closed on a transcript that has stopped accepting records, before
	// anything is published rather than after the markers fail to land. This
	// transaction swaps model history, resets the environment tracker and tells
	// every client the context was compacted, and only then writes the entries
	// that make the fold survive a restart; a poisoned writer refuses all of
	// them, so a fold published here is one the session announces, acts on, and
	// loses -- the restart anchors on the last marker that did land and brings
	// the pre-compaction history back. It is the turn loop's own admission rule
	// applied to the other durable write the session makes. Refusing reports the
	// publication lost, which is the answer both callers already handle: a fold
	// that did not publish runs neither commit phase.
	//
	// The read is under the transcript door, so no session append can poison the
	// writer between here and the entries below -- every one of them goes
	// through this lock.
	//
	// A closed writer is refused the same way: its ordinary appends are silent
	// no-ops, so the entries that make the fold survive a restart would not land
	// either, and the fold would be announced and lost exactly as it is under
	// poison. Both facts come from the one helper so the gate cannot know fewer
	// than the claims do.
	if refusal := refuseOnUnhealthyTranscript(s.attachedTranscript()); refusal != nil {
		s.attentionMu.Unlock()
		return nil, false, refusal
	}
	s.mu.Lock()
	previousEnvironmentIDs := environmentTurnIDs(s.history)
	published, ok = s.publishFoldedHistory(snapLen, snapRevision, folded)
	if !ok {
		s.mu.Unlock()
		s.attentionMu.Unlock()
		return nil, false, nil
	}
	// The merge-back rewrite set: the PERSISTED transcript forms of every
	// append/write pair since this fold's snapshot (snapAppends), taken from
	// the pair log — never the live history turns, whose tool results
	// deliberately retain private API-log evidence that the persisted
	// projection replaces with a re-read placeholder, and whose delegate
	// delivery commits live only on the persisted form. The pairs' original
	// entries sit BEFORE the compaction markers this transaction is about to
	// write — where ResumeHistory's last-marker anchor would silently drop
	// them on restart — so the transcript-commit phase below re-appends these
	// forms after the markers. Attention-retained turns and repair synthetics
	// never enter the log (they have no session-transcript pair: the
	// attention re-fold and ResumeHistory's own repair own their restart
	// stories), so the rewrite cannot manufacture entries for attention-owned
	// turns — and a turn the attention machinery deletes between publish and
	// rewrite cannot be resurrected. The log is pruned wholesale: a competing
	// fold still in flight must re-snapshot to publish after this
	// one (its revision check fails otherwise), so no older snapshot can
	// need the pruned entries — and snapAppends >= persistedAppendLogBase
	// for the same reason, since only publications advance the base. The
	// prune itself is deferred until the transcript batch below is durable:
	// an all-or-nothing batch that rolls back leaves these turns with no
	// replay-tail copies, so the log must still hold them for the next
	// publication to replay.
	rewriteTail := append([]schema.Turn(nil), s.persistedAppendLog[snapAppends-s.persistedAppendLogBase:]...)
	if onPublishLocked != nil {
		onPublishLocked(published)
	}
	commit.resetEnvContextTrackerLocked(environmentTurnsRemoved(previousEnvironmentIDs, published))
	commit.claimNoteLocked()
	commit.publishedRevision = s.historyRevision
	// The compaction-operation claim runs in the same s.mu critical section
	// as the history swap: actualCompaction comes from the staged
	// EventContextCompaction payloads (never from the successful publication
	// itself), and the claim is generation-matched against the operation this
	// fold's requesting caller captured — so an unchanged publication, a
	// losing fold, or a fold with no captured operation claims nothing.
	commit.actualCompaction = commit.stagedCompactionCount() > 0
	commit.claimCompactionLocked()
	// Publication-order marker for last-write-wins effect suppression, set
	// HERE — at publish, not at flush: an older fold whose deferred flush
	// runs after this publish must find it and stay silent, even before
	// this fold's own flush has run. Publications stamp strictly increasing
	// revisions, so plain assignment is monotone.
	s.newestPublishedFoldRevision = commit.publishedRevision
	// Return a defensive copy, taken under this same s.mu hold: the
	// published slice IS s.history's new backing array, and
	// delegate-attention delivery goroutines mutate s.history IN PLACE
	// under s.mu (retainDelegateAttentionTurn's element overwrite,
	// removeUnverifiedDelegateAttentionTurn's element shift). A caller
	// reading the returned slice unlocked — prepareModelRequestWithError
	// expands it into the model request — would otherwise race those
	// mutations with torn multi-word Turn reads.
	published = append([]schema.Turn(nil), published...)
	s.mu.Unlock()
	if hook := s.cfg.testOnly.beforeFoldTranscriptCommit; hook != nil {
		hook()
	}
	// The fold's transcript commit is ONE all-or-nothing durable batch: its
	// compaction markers and steering turns first, then the replay-tail copies
	// that carry the turns recorded during the fold past the marker. Writing
	// them as one batch is what keeps the marker and the copies it claims from
	// outliving each other. A durable write the writer cannot record rolls the
	// whole batch back, so a marker that discards the originals it replaces can
	// never land without those replacements — the loss this used to permit when
	// the marker (buffered) and the copy (durable) were written separately.
	commit.commitTranscriptsLocked(rewriteTail)
	switch {
	case commit.transcriptCommitErr != nil:
		// The batch rolled back: no marker landed, so the next resume replays
		// the pre-fold transcript from the originals. The steering turns the
		// fold injected (the pinned-note handoff among them) are still part of
		// its published history, so record them on their own — the note claim
		// already consumed in memory must not leave the note with no durable
		// handoff. If even that recovery cannot confirm the handoff durable,
		// the note must be put back: a crash in the window would otherwise
		// lose the only copy of a note this fold consumed.
		if !commit.recordSteeringAfterFailedBatchLocked() {
			s.mu.Lock()
			commit.restoreNoteLocked()
			s.mu.Unlock()
		}
		// The claimed operation's receipt lived on the rolled-back marker
		// turns, so return the cycle to pending; a restart re-arms it.
		s.mu.Lock()
		commit.restoreCompactionClaimLocked()
		s.mu.Unlock()
	case commit.transcriptRetainedUnsynced:
		// The batch is a record a returning reader finds, but neither its
		// fsync nor the recovery barrier made it durable. Keep the record (its
		// warning is already queued on the writer) and do NOT prune the
		// replay-tail log: pruning is a durability claim the batch has not
		// earned, and a crash that lost the unsynced copies must still be able
		// to replay them. The note's handoff is in that same unconfirmed batch,
		// so restore the note; the durability gate must not be able to disagree
		// with itself about a batch it just called non-durable.
		s.mu.Lock()
		commit.restoreNoteLocked()
		commit.restoreCompactionClaimLocked()
		s.mu.Unlock()
	default:
		// The batch is durable: the replay-tail copies are in the transcript
		// after the marker, so the persisted forms it replayed are now spent.
		// Advancing the base here (still under attentionMu, so no append can
		// interleave) drops them exactly as pruning at publish did.
		s.mu.Lock()
		s.persistedAppendLogBase += len(s.persistedAppendLog)
		s.persistedAppendLog = nil
		s.mu.Unlock()
	}
	s.attentionMu.Unlock()
	if err := commit.transcriptCommitErr; err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v; this compaction was not anchored on disk, so a restart replays the transcript from before it", err)})
	}
	// A whole line that landed but did not sync through the fold's durable
	// batch queues a diagnostic on the writer; this is the fold's one owner
	// for surfacing it, outside the door.
	s.surfaceTranscriptWarnings()
	if hook := s.cfg.testOnly.beforeFoldSideEffectsFlush; hook != nil {
		hook()
	}
	commit.flush()
	// After the locks release and the events flush, the winning publication's
	// handoff completes: the claimed operation's delivery finishes, its
	// receipt's phase advances, and the metadata save persists the result.
	s.commitSkillCompactionPublication(commit)
	return published, true, nil
}

// foldWithForceCompact snapshots s.history, runs ForceCompact with the given
// instructions, and publishes the result via publishFoldedHistory — retrying
// once (a fresh snapshot, a fresh fold) if a competing fold wins the publish
// race, since a stale fold result is worthless to re-publish. Every
// ForceCompact caller shares this exact shape: Compact,
// applyPendingForceCompact, and handleModelError's content-filter retry.
//
// captured is the compaction operation this fold's REQUESTING caller captured
// (a round-tail dispatch's pending forced operation); unrelated manual folds
// pass nil and can therefore claim nothing. On success, the winning
// publication claims the captured generation (inside the transaction) and
// applies shrinkTurnHistoryBaseline atomically with the publish (using the
// fold's own pre/post lengths, never the merged-in result — see
// publishFoldedHistory) and returns ok=true. On ok=false, both attempts lost
// the publish race: s.history is whatever the winning competitor left it as,
// and this fold's work — including the shrink it would have applied — is
// entirely discarded; the caller decides what that means for it. A captured
// FORCED operation is then terminally retired (forced_not_published): its own
// fold can never claim it, so leaving it pending would wedge the cycle
// forever. The retirement's terminal loss notice lands alongside any winner's
// handoff. A captured automatic intent (none exists today — the per-request
// fold owns that path) would stay pending for its next actual fold.
//
// refusal carries publishFoldTransaction's reason when ok=false: nil for the
// publication race this retries, and the poisoned-transcript error for the
// refusal it does not retry, since no second fold can make that writer accept
// the markers.
func (s *Session) foldWithForceCompact(ctx context.Context, instructions string, captured *schema.SkillCompactionOperation) (ok bool, refusal error) {
	const maxAttempts = 2
	for range maxAttempts {
		s.mu.Lock()
		histCopy := append([]schema.Turn{}, s.history...)
		snapLen := len(s.history)
		snapRevision := s.historyRevision
		snapAppends := s.persistedAppendLogBase + len(s.persistedAppendLog)
		s.mu.Unlock()

		compactionCtx, emitFn, commit, foldInjectedCount := s.stageCompactionEffects(ctx, &histCopy)
		commit.captured = captured
		s.contextMgr.ForceCompact(compactionCtx, &histCopy, instructions, emitFn)
		postLen := len(histCopy)
		injected := foldInjectedCount()

		_, published, refused := s.publishFoldTransaction(snapLen, snapRevision, snapAppends, histCopy, commit, func([]schema.Turn) {
			s.shrinkTurnHistoryBaseline(snapLen, postLen, injected)
		})
		if published {
			return true, nil
		}
		if refused != nil {
			return false, refused
		}
		// Conflict: loop retries against the now-current history. commit is
		// deliberately NOT run — this attempt's side effects must not take
		// effect for a fold that never published.
	}
	if captured != nil && captured.Origin == skillCompactionOriginForced {
		// Retry exhaustion cancels ONLY the forced owner's generation.
		// cancelSkillCompaction is generation-matched, emits the visible
		// terminal loss notice, records the cancelled receipt, and warns on
		// its own if the retirement cannot be persisted.
		_ = s.cancelSkillCompaction(ctx, captured.Generation, skillCompactionCancelForcedNotPublished)
	}
	return false, nil
}

// noteHandoffPrefix frames the agent's note as a message from its pre-compaction
// self when it is injected into the fresh post-compaction context.
const noteHandoffPrefix = "Here's your note to yourself from before compaction:"

func renderNoteHandoff(note string) string {
	return noteHandoffPrefix + "\n" + note
}

type steeringTurnRecord struct {
	turn schema.Turn
	text string
	kind string
}

// preCompactMessage pairs one pre-compact steering message with the
// events.SteeringKind* naming which of runPreCompactHook's three sources
// (plugin PreCompact output, the pinned-note handoff, the goal objective)
// produced it. Without this, every message merged into one batch reads as
// whatever kind the batch's caller hardcodes — a goal objective labeled as a
// plugin hook, for instance — regardless of which source actually built it.
type preCompactMessage struct {
	text string
	kind string
}

// steerCompactionTranscriptReminder queues the post-compaction transcript
// recipe unconditionally, for callers outside a fold publication.
func (s *Session) steerCompactionTranscriptReminder() {
	s.steerCompactionTranscriptReminderForFold(ungatedFoldRevision)
}

// steerCompactionTranscriptReminderForFold queues the recipe for the fold
// published at publishedRevision; the enqueue refuses it if a newer fold
// has published since.
func (s *Session) steerCompactionTranscriptReminderForFold(publishedRevision int) {
	if s.stateDir == "" || s.id == "" {
		return
	}
	// The reminder is a read_transcript call recipe end to end; a session
	// without that tool is told nothing rather than told to call it.
	if !s.canInstructTool("read_transcript") {
		return
	}
	ref := encodeRef("", s.id)
	if err := s.steerKindForFold("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"outline\"}), then read a focused range with read_transcript({\"transcript_ref\": \""+ref+"\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>", events.SteeringKindTranscriptPointer, publishedRevision); err != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("steering admission failed: %v", err)})
	}
}

// foldCommit carries one fold attempt's staged side effects to its
// publisher, split by the lock each phase needs (see publishFoldTransaction
// for the transaction that runs them). claimNoteLocked consumes the pinned
// note the fold captured (generation-checked; a no-op when none was
// captured) and MUST run inside the winning publish's s.mu critical section,
// atomically with the history swap — between a publish and a deferred clear
// the note would stay globally visible, so a concurrent fold could re-inject
// it, and an unconditional clear could erase a newer note pinned mid-fold.
// commitTranscriptsLocked appends the fold's own transcript entries and MUST
// run under the same attentionMu hold that decided the publish. flush
// commits the remaining deferred effects, outside the locks. A losing fold
// runs none of them.
//
// publishedRevision is the historyRevision this fold's publish produced,
// set by the publisher inside the publish's s.mu critical section: flush
// compares it against newestPublishedFoldRevision so any fold older than
// the newest PUBLICATION skips its last-write-wins effects, whichever flush
// runs first.
//
// actualCompaction reports whether this fold's layers actually compacted
// anything (staged EventContextCompaction payloads exist) — never merely
// that the history published: an unchanged publication sets it false.
// captured is the explicit operation snapshot the fold's REQUESTING caller
// captured (the round-tail dispatch's forced operation, or the per-request
// fold's pending automatic operation); an unrelated manual fold captures
// nothing and can therefore claim nothing. claimCompactionLocked runs the
// generation-matched publication claim inside the winning publish's s.mu
// critical section — minting the publication identity from the persisted
// lifecycle revision it stamps — and stages the resulting handoff receipt;
// the receipt is attached to the fold's compaction turns by
// commitTranscriptsLocked and delivered (with its metadata save) by
// commitSkillCompactionPublication after the flush.
type foldCommit struct {
	claimNoteLocked              func()
	commitTranscriptsLocked      func(tail []schema.Turn)
	flush                        func()
	resetEnvContextTrackerLocked func(bool)
	publishedRevision            int
	actualCompaction             bool
	// transcriptCommitErr is the error from the fold's one all-or-nothing
	// transcript batch, set by commitTranscriptsLocked. Non-nil means the
	// batch rolled back and the fold has no durable anchor. Read by the flush
	// publisher after the locks release, where emitting is safe.
	transcriptCommitErr error
	// transcriptRetainedUnsynced reports that the batch IS recorded (a
	// returning reader finds the marker and its copies) but could be made
	// durable neither by its own fsync nor by the recovery barrier. The
	// publisher keeps the log rather than pruning it on an unconfirmed
	// durability claim.
	transcriptRetainedUnsynced bool
	// recordSteeringAfterFailedBatchLocked re-records the fold's steering turns
	// (fail-closed durable) after transcriptCommitErr rolls their shared batch
	// back, so the turns the fold injected — the pinned-note handoff among
	// them — still have a durable record. It reports whether the note handoff
	// it wrote (when the fold produced one) is confirmed durable; false means
	// the caller must call restoreNoteLocked. Uncalled on a recorded batch.
	recordSteeringAfterFailedBatchLocked func() bool
	// restoreNoteLocked puts back the pinned note this fold claimed when its
	// transcript handoff could not be confirmed durable, so the note is
	// re-emitted rather than lost. A no-op when the fold claimed no note.
	restoreNoteLocked func()
	// restoreCompactionClaimLocked returns the compaction operation this fold
	// claimed to its pending phase when the batch carrying its receipt could
	// not be confirmed durable, so a restart re-arms it instead of finding a
	// published slot whose receipt never landed. A no-op when no operation was
	// claimed.
	restoreCompactionClaimLocked func()
	captured                     *schema.SkillCompactionOperation
	stagedCompactionCount        func() int
	claimCompactionLocked        func()
	receipt                      *schema.SkillCompactionReceipt
}

// foldPublicationID renders the winning publication's identity from the
// PERSISTED lifecycle revision stamped at its claim: that revision is saved
// in every lifecycle snapshot and receipt, and each recorded publication
// bumps it exactly once, so the identity is unique per winning publication
// and stable across restarts. The memory-only historyRevision is never
// seeded on restore — minting the identity from it would re-mint fold-1,
// fold-2… onto a restored session's publications and collide with its
// restored handoffs.
func foldPublicationID(lifecycleRevision uint64) string {
	return fmt.Sprintf("fold-%d", lifecycleRevision)
}

func environmentTurnIDs(history []schema.Turn) map[string]int {
	ids := make(map[string]int)
	for _, turn := range history {
		if turn.Kind == schema.TurnEnvironment {
			ids[turn.StableTurnID]++
		}
	}
	return ids
}

func environmentTurnsRemoved(previous map[string]int, published []schema.Turn) bool {
	if len(previous) == 0 {
		return false
	}
	present := environmentTurnIDs(published)
	if len(present) < len(previous) {
		return true
	}
	for id, count := range previous {
		if present[id] < count {
			return true
		}
	}
	return false
}

// noteClaimRegistrarKey carries the fold staging's registrar for the
// pinned-note claim from stageCompactionEffects down to runPreCompactHook.
type noteClaimRegistrarKey struct{}

// noteHandoffClaim couples the fold's generation-checked pinned-note claim
// with the restore that keeps the note fail-closed. The claim consumes the
// note atomically with the winning publish; restore puts it back when the
// fold's transcript handoff could not be confirmed durable, so a crash (or a
// lost unsynced write) re-emits the note instead of losing it.
type noteHandoffClaim struct {
	claimLocked   func()
	restoreLocked func()
}

func withNoteClaimRegistrar(ctx context.Context, register func(noteHandoffClaim)) context.Context {
	return context.WithValue(ctx, noteClaimRegistrarKey{}, register)
}

// registerNoteClaim hands the fold's generation-checked note claim to the
// enclosing fold staging for its publication transaction. It reports false
// when no registrar is installed — a direct runPreCompactHook caller outside
// any publication transaction — in which case the claim belongs with the
// caller's own deferred commit.
func registerNoteClaim(ctx context.Context, claim noteHandoffClaim) bool {
	register, ok := ctx.Value(noteClaimRegistrarKey{}).(func(noteHandoffClaim))
	if !ok {
		return false
	}
	register(claim)
	return true
}

func (s *Session) stageCompactionEffects(ctx context.Context, history *[]schema.Turn) (context.Context, func(events.EventKind, events.EventData), *foldCommit, func() int) {
	preCompactRan := false
	artifactProduced := false
	var existingArtifacts []schema.Turn
	if history != nil {
		for _, turn := range *history {
			if isSessionNameCompactionTurn(turn) {
				existingArtifacts = append(existingArtifacts, turn)
			}
		}
	}
	var pendingSteering []steeringTurnRecord
	strategyInjected := 0

	s.mu.Lock()
	liveBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	// Per-call compaction metadata, installed into the fold's context:
	// concurrent fold publishers must never share — or write — the
	// contextMgr.Meta field, whose unsynchronized cross-publisher use would
	// race. Every publisher stages through here, so this is the single choke
	// point.
	ctx = contextmgr.WithCompactionMeta(ctx, s.buildCompactionMeta())

	// The publication transaction claims the pinned note (if the hook below
	// captures one) atomically with the publish; the fold registers its
	// generation-checked claim here as it runs.
	var noteClaim *noteHandoffClaim
	ctx = withNoteClaimRegistrar(ctx, func(claim noteHandoffClaim) { noteClaim = &claim })

	// pendingCompactionTurns records checkpoint/summary turns the fold
	// produced; handleCompactionTurn's own side effects (transcript write,
	// EventCompactionTurn, session-naming launch, task-list steering) are
	// deferred into flush() below rather than run here, mid-fold. A losing
	// fold attempt must not have already written a transcript entry or
	// renamed the session for a compaction that gets discarded on conflict;
	// artifactProduced tracking itself is pure (compares against a snapshot,
	// no side effect) and stays inline.
	var pendingCompactionTurns []schema.Turn
	ctx = contextmgr.WithCompactionTurnCallback(ctx, func(turn schema.Turn) {
		pendingCompactionTurns = append(pendingCompactionTurns, turn)
		if isSessionNameCompactionTurn(turn) && !consumeMatchingCompactionArtifact(&existingArtifacts, turn) {
			artifactProduced = true
		}
	})
	// A non-default Strategy (memory-crystals, recursive-distill, ooda) can
	// append its own steering turn AFTER its fold layers run, inside its own
	// ManageContext — not through runPreCompactHook below, so it needs its own
	// reporting channel into the same injected-turn correction (issue #634).
	// See contextmgr.WithPostFoldInjectionCallback.
	ctx = contextmgr.WithPostFoldInjectionCallback(ctx, func(n int) { strategyInjected += n })
	// liveBaseline lets a self-injecting Strategy tell whether the marker
	// turn it's about to replace sits before or after the N4 boundary — the
	// net-delta report above can't see a marker-before-baseline removal (it
	// nets to zero against the marker's own re-append), so it needs the
	// boundary's CURRENT position, translated for every fold layer this
	// compaction has already run. See contextmgr.WithBaselineQuery.
	ctx = contextmgr.WithBaselineQuery(ctx, func() (int, bool) { return liveBaseline, true })
	// noteCommit performs runPreCompactHook's deferred side effects (plugin
	// hook user-message delivery; the note claim itself is registered above
	// and runs inside the publication transaction instead) — see there for
	// why they can't run until this fold wins. nil until the hook actually
	// runs (preCompactRan below), consistent with "nothing to commit" for a
	// fold that never got far enough to need one.
	var noteCommit func()
	// pendingCompactionEvents stages every EventContextCompaction this fold's
	// layers report. Emitting them live, mid-fold, let a LOSING attempt tell
	// every event consumer (and any metrics built on them) that a compaction
	// happened for a history change that was then discarded on conflict — so
	// they are buffered like every other staged
	// side effect and emitted by flush() only once this fold wins
	// publication. Non-compaction kinds (warnings from a failed summarize
	// layer, say) still pass through immediately: they describe the attempt
	// itself, which really did run, not a history change that may not stand.
	var pendingCompactionEvents []events.ContextCompactionData
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if ccd, ok := data.(events.ContextCompactionData); ok {
				if shrink := ccd.TurnsBefore - ccd.TurnsAfter; shrink > 0 {
					liveBaseline -= shrink
					if liveBaseline < 0 {
						liveBaseline = 0
					}
				}
				pendingCompactionEvents = append(pendingCompactionEvents, ccd)
			}
			if !preCompactRan {
				preCompactRan = true
				var records []steeringTurnRecord
				records, noteCommit = s.runPreCompactHook(ctx, history)
				pendingSteering = append(pendingSteering, records...)
			}
			return
		}
		s.emit(kind, data)
	}
	// commitTranscriptsLocked and flush are the two staged-commit phases a
	// winning publish runs, in order — a losing fold runs NEITHER: it must
	// leave the pinned note intact, write no transcript entries, emit no
	// compaction/steering events, launch no
	// session-naming, inject no task-list steering, and not reset the
	// self-compact nudge latch.
	//
	// commitTranscriptsLocked appends the fold's own transcript entries
	// (checkpoint/summary turns, then the steering turns the fold injected,
	// in their history order) while the publication transaction still holds
	// attentionMu — the transcript door — so no concurrently recorded
	// turn's entry can sequence between the publish and these markers
	// (ResumeHistory anchors on the LAST compaction marker and discards
	// everything before it, so a late marker would silently drop every turn
	// recorded after the fold). It writes those entries AND the caller's
	// replay-tail copies — the persisted forms of the turns recorded during
	// the fold, whose originals sit before the marker — as one all-or-nothing
	// durable batch, so a copy the writer cannot record rolls the marker back
	// with it rather than leaving an anchor that discards an original it has
	// nothing to replace. It also attaches the publication's staged handoff
	// receipt to each compaction turn, so the durable transcript carries the
	// typed record a restart reconciles from. Write errors are carried into
	// flush, where emitting is safe again; the batch's own error is also
	// recorded on the commit for the publisher to surface.
	commit := &foldCommit{}
	var compactionTurnWriteErrs []error
	var steeringWriteErrs []error
	commitTranscriptsLocked := func(tail []schema.Turn) {
		if commit.receipt != nil {
			for i := range pendingCompactionTurns {
				state := pendingCompactionTurns[i].SkillState.Clone()
				if state == nil {
					state = &schema.SkillTurnState{}
				}
				receipt := *commit.receipt
				receipt.Operation.Selection.Names = slices.Clone(commit.receipt.Operation.Selection.Names)
				state.Compaction = &receipt
				pendingCompactionTurns[i].SkillState = state
			}
		}
		compactionTurnWriteErrs = make([]error, len(pendingCompactionTurns))
		steeringWriteErrs = make([]error, len(pendingSteering))
		// Markers first, then steering, then the replay-tail copies: every
		// entry the marker must precede lands before it, and every copy the
		// marker must claim lands after it. The batch is all-or-nothing, so a
		// copy the writer cannot record takes the marker down with it.
		batch := make([]schema.Turn, 0, len(pendingCompactionTurns)+len(pendingSteering)+len(tail))
		batch = append(batch, pendingCompactionTurns...)
		for _, record := range pendingSteering {
			batch = append(batch, record.turn)
		}
		batch = append(batch, tail...)
		// Each seam is a FAILURE injector: a non-nil return stands in for a
		// durable write the writer could not record and aborts the whole batch
		// (nothing lands); a nil return injects nothing and the turn is written
		// for real in the batch. The seam does not replace the writer.
		var batchErr error
		injectedViaHook := false
		for i, record := range pendingSteering {
			if appendTurn := s.cfg.testOnly.appendCompactionTurn; appendTurn != nil {
				if err := appendTurn(record.turn); err != nil {
					steeringWriteErrs[i] = err
					batchErr = err
					injectedViaHook = true
				}
			}
		}
		if batchErr == nil {
			for _, turn := range tail {
				if appendTail := s.cfg.testOnly.appendFoldTailTurn; appendTail != nil {
					if err := appendTail(turn); err != nil {
						batchErr = err
						injectedViaHook = true
						break
					}
				}
			}
		}
		if injectedViaHook {
			// A hook-simulated failure must not be papered over by the real
			// writer below: nothing is written and the recovery path must
			// respect the same injected failure (see
			// recordSteeringAfterFailedBatchLocked).
			commit.transcriptCommitErr = batchErr
			for i := range compactionTurnWriteErrs {
				compactionTurnWriteErrs[i] = batchErr
			}
			for i := range steeringWriteErrs {
				if steeringWriteErrs[i] == nil {
					steeringWriteErrs[i] = batchErr
				}
			}
			return
		}
		batchErr = s.writeTranscriptBatchLocked(batch)
		if errors.Is(batchErr, transcript.ErrRetainedUnsynced) {
			// The whole batch IS a record — a returning reader finds the
			// marker and its copies — but neither its fsync nor the recovery
			// barrier made it durable. It is not a rollback: keep the record
			// and its steering, and let the publisher leave the replay-tail
			// log un-pruned.
			commit.transcriptRetainedUnsynced = true
			return
		}
		if batchErr != nil {
			commit.transcriptCommitErr = batchErr
			for i := range compactionTurnWriteErrs {
				compactionTurnWriteErrs[i] = batchErr
			}
			for i := range steeringWriteErrs {
				if steeringWriteErrs[i] == nil {
					steeringWriteErrs[i] = batchErr
				}
			}
		}
	}
	commit.resetEnvContextTrackerLocked = func(removed bool) {
		if removed && len(pendingCompactionTurns) > 0 {
			s.resetEnvContextTrackerLocked()
		}
	}
	flush := func() {
		// Deferred last-write-wins effects (compaction naming, task-list
		// and artifact steering) run only for the NEWEST published fold:
		// once a newer fold has published, an older fold's flush must stay
		// silent — whichever flush happens to run first. Binding the gate to
		// publication order (the marker is set at publish, inside
		// the transaction) makes both orderings one rule, and the newest
		// fold's own flush can never be suppressed: nothing newer exists at
		// its check, and any yet-newer publication's flush is itself never
		// suppressed, by induction. Additive effects — compaction events,
		// steering records, hook user messages, the nudge latch — still
		// run: they describe this fold's real, published work.
		s.mu.Lock()
		superseded := commit.publishedRevision < s.newestPublishedFoldRevision
		s.mu.Unlock()
		if hook := s.cfg.testOnly.afterFoldSupersessionCheck; hook != nil {
			hook()
		}
		for _, ccd := range pendingCompactionEvents {
			s.emit(events.EventContextCompaction, ccd)
		}
		for i, turn := range pendingCompactionTurns {
			s.handleCompactionTurnEffects(turn, compactionTurnWriteErrs[i], superseded, commit.publishedRevision)
		}
		s.emitSteeringTurnRecords(pendingSteering, steeringWriteErrs)
		if artifactProduced && !superseded {
			s.steerCompactionTranscriptReminderForFold(commit.publishedRevision)
		}
		if noteCommit != nil {
			noteCommit()
		}
		if preCompactRan {
			s.mu.Lock()
			s.nudgedSinceCompact = false // reset nudge latch on ANY compaction that actually took effect
			s.mu.Unlock()
		}
	}
	// injectedTurns reports how many turns were appended directly to *history
	// during this call from either source: runPreCompactHook (pinned-note
	// handoff, PreCompact plugin hook output, goal-objective steering —
	// appendSteeringMessagesToHistory above) or a Strategy's own post-fold
	// injection reported via WithPostFoldInjectionCallback (memory-crystals,
	// recursive-distill, ooda). Both land strictly after whatever the fold
	// preserved, so a caller that nets them against the fold's own removal (a
	// plain before/after turn-count delta) under-shrinks the N4 in-flight-turn
	// boundary by exactly this many turns (issue #634 Finding 1) — adding this
	// back recovers the count the fold actually removed. Safe to read any
	// time after the fold call returns:
	// runPreCompactHook fires at most once per compactionEmitFunc instance, on
	// the first EventContextCompaction, and strategyInjected accumulates
	// across however many times a strategy reports (ordinarily once).
	injectedTurns := func() int { return len(pendingSteering) + strategyInjected }
	// The generation-matched publication claim, run by the winning publish
	// inside its s.mu critical section (see foldCommit). actualCompaction is
	// decided by the publisher from the staged EventContextCompaction
	// payloads; the claim itself matches the captured operation's generation
	// AND note generation against the live pending operation, so a fold
	// whose intent was superseded or cleared mid-flight claims nothing. The
	// publication identity is minted from the lifecycle revision the claim
	// stamps — persisted in every snapshot and receipt, and bumped exactly
	// once per recorded publication, so a restored session's publications
	// can never re-mint a prior publication's identity. A real compaction
	// that captured no operation still records the absent-selection
	// reminder handoff.
	commit.stagedCompactionCount = func() int { return len(pendingCompactionEvents) }
	commit.claimCompactionLocked = func() {
		captured := commit.captured
		pending := s.skillLifecycle.PendingCompaction
		claim := commit.actualCompaction && pending != nil && captured != nil &&
			pending.Generation == captured.Generation && pending.NoteGeneration == captured.NoteGeneration
		var receipt *schema.SkillCompactionReceipt
		if claim {
			pending.Phase = skillCompactionPhasePublished
			s.skillLifecycle.Revision++
			pending.PublicationID = foldPublicationID(s.skillLifecycle.Revision)
			claimed := *pending
			claimed.Selection.Names = slices.Clone(pending.Selection.Names)
			receipt = &schema.SkillCompactionReceipt{
				Revision:  s.skillLifecycle.Revision,
				SessionID: s.id,
				Operation: claimed,
				Phase:     skillCompactionReceiptPublished,
			}
		} else if commit.actualCompaction {
			// The reminder handoff is itself a lifecycle mutation: it bumps
			// the revision like any other receipt, so a restart reconciles
			// it from the transcript even if the post-flush save never ran.
			s.skillLifecycle.Revision++
			receipt = &schema.SkillCompactionReceipt{
				Revision:  s.skillLifecycle.Revision,
				SessionID: s.id,
				Operation: schema.SkillCompactionOperation{PublicationID: foldPublicationID(s.skillLifecycle.Revision)},
				Phase:     skillCompactionReceiptPublished,
				Reason:    skillCompactionReminderNoOperation,
			}
		}
		if receipt != nil {
			s.recordSkillCompactionHandoffLocked(*receipt)
			commit.receipt = receipt
		}
	}
	commit.claimNoteLocked = func() {
		if noteClaim != nil {
			noteClaim.claimLocked()
		}
	}
	commit.restoreNoteLocked = func() {
		if noteClaim != nil {
			noteClaim.restoreLocked()
		}
	}
	commit.restoreCompactionClaimLocked = func() {
		receipt := commit.receipt
		if receipt == nil {
			return
		}
		// Only a claimed operation has a slot to return to pending and a forced
		// trigger to re-arm; a generation-zero reminder receipt has neither.
		if receipt.Operation.Generation != 0 {
			if op := s.skillLifecycle.PendingCompaction; op != nil &&
				op.Phase == skillCompactionPhasePublished && op.Generation == receipt.Operation.Generation {
				// Put the claimed cycle back to pending: its receipt lives only
				// on the batch's marker turns, so a restart must re-arm the
				// intent rather than find a published slot with no durable
				// receipt.
				op.Phase = skillCompactionPhasePending
				op.PublicationID = ""
				// A FORCED operation's dispatch consumed the transient
				// round-tail trigger (applyPendingForceCompact cleared it), so
				// returning the operation to pending without re-arming would
				// leave it pending with nothing to dispatch it until restart.
				// Re-arm it, but never clobber a newer request that already
				// armed its own trigger.
				if op.Origin == skillCompactionOriginForced && !s.forceRequested {
					s.forceRequested = true
					s.pendingInstructions = op.Instructions
				}
			}
		}
		// Withdraw the handoff this fold recorded in EVERY non-durable outcome,
		// including a generation-zero reminder receipt: its marker rolled back
		// or never synced, so a persisted receipt would make a later restart or
		// request emit a post-compaction reminder for a compaction that is not
		// durably recorded.
		s.removeSkillCompactionHandoffsLocked(map[string]bool{receipt.Operation.PublicationID: true})
	}
	commit.commitTranscriptsLocked = commitTranscriptsLocked
	// recordSteeringAfterFailedBatchLocked re-records the fold's steering turns
	// after a rolled-back batch and reports whether the pinned-note handoff it
	// wrote (when the fold produced one) is confirmed durable. A false return
	// means the caller must restore the note.
	commit.recordSteeringAfterFailedBatchLocked = func() (noteDurable bool) {
		// Optimistic: a fold with no note handoff has nothing to restore, and a
		// fold with one is disproved only by that handoff's own recovery write.
		noteDurable = true
		for i, record := range pendingSteering {
			// A hook-injected steering failure aborts the batch and must not
			// be papered over by re-writing the same turn for real.
			if appendTurn := s.cfg.testOnly.appendCompactionTurn; appendTurn != nil {
				if err := appendTurn(record.turn); err != nil {
					steeringWriteErrs[i] = err
					if record.kind == events.SteeringKindNoteHandoff {
						noteDurable = false
					}
					continue
				}
			}
			// The note claim already consumed the note, so its handoff must be
			// durable, not merely landed: a fail-closed synced write. A
			// recorded-but-unsynced result is NOT confirmed durability.
			err := s.writeTranscriptSyncedLocked(record.turn)
			if err != nil {
				steeringWriteErrs[i] = err
				if record.kind == events.SteeringKindNoteHandoff {
					noteDurable = false
				}
				continue
			}
			steeringWriteErrs[i] = nil
		}
		return noteDurable
	}
	commit.flush = flush
	return ctx, emitFn, commit, injectedTurns
}

func consumeMatchingCompactionArtifact(existing *[]schema.Turn, turn schema.Turn) bool {
	for i, candidate := range *existing {
		if reflect.DeepEqual(candidate, turn) {
			*existing = append((*existing)[:i], (*existing)[i+1:]...)
			return true
		}
	}
	return false
}

// runPreCompactHook gathers the steering messages re-injected once per fold
// attempt and appends them to history as TurnSteering turns — that part must
// happen unconditionally, since it's part of the content the fold produces.
// The order is plugin PreCompact output first, then the active goal
// objective last: appending the objective at the strongest recency position
// (the trailing steering turn that safeCutoff protects) is what lets it
// survive the same compaction. The goal path runs even with no plugins
// loaded; only the plugin part is guarded by a non-nil hookRunner. The three
// sources are genuinely different things, so each keeps its own
// events.SteeringKind* (precompact-hook / note-handoff / goal-objective)
// rather than being merged under one label.
//
// It also returns a separate commit func for the two side effects that must
// NOT take effect unless this fold wins publication: a losing fold must
// leave the pinned note intact (for the retry, or a competitor, to hand off)
// rather than have already consumed it, and must
// not have already delivered the plugin hook's user-facing messages for a
// compaction that never actually happened. commit is nil-safe to skip (the
// caller checks before calling) and idempotent to call at most once, since
// the caller (compactionEmitFunc's flush) only calls it on a winning
// publish.
//
// The hook's own execution (s.hookRunner.RunPreCompact) and the goal/note
// TEXT reads cannot be deferred: their output has to be part of the fold's
// content before publish is even attempted. A losing, retried fold re-runs
// the hook again next attempt — the same re-execution
// foldWithForceCompact's retry already accepts for the rest of the fold.
func (s *Session) runPreCompactHook(ctx context.Context, history *[]schema.Turn) (records []steeringTurnRecord, commit func()) {
	if history == nil {
		return nil, nil
	}
	var messages []preCompactMessage
	var deferred []func()
	if s.hookRunner != nil {
		compactResult := s.hookRunner.RunPreCompact(s.apiLogContext(ctx), s.hookInput(plugin.HookPreCompact))
		for _, m := range compactResult.ModelContext {
			messages = append(messages, preCompactMessage{text: wrapHookContext(m), kind: events.SteeringKindPrecompactHook})
		}
		for _, m := range compactResult.UserMessages {
			msg := m
			deferred = append(deferred, func() { s.deliverHookUserMessage(msg) })
		}
	}
	if note, gen := s.pinnedNoteSnapshot(); note != "" {
		messages = append(messages, preCompactMessage{text: renderNoteHandoff(note), kind: events.SteeringKindNoteHandoff})
		// One-shot handoff: consumed only once this compaction wins
		// publication, not eagerly here — see the doc comment above. The
		// claim is generation-checked so it consumes exactly the note this
		// fold captured, never a newer one pinned mid-fold; a fold publisher
		// runs it inside the publication transaction itself, while a direct
		// caller outside one commits it with the rest.
		claimLocked := func() { s.claimPinnedNoteLocked(gen) }
		// The restore travels with the claim so the publication transaction can
		// put the note back when its transcript handoff is not confirmed
		// durable. It runs under s.mu inside that transaction.
		restoreLocked := func() { s.restorePinnedNoteLocked(gen, note) }
		if !registerNoteClaim(ctx, noteHandoffClaim{claimLocked: claimLocked, restoreLocked: restoreLocked}) {
			deferred = append(deferred, func() {
				s.mu.Lock()
				claimLocked()
				s.mu.Unlock()
			})
		}
	}
	for _, m := range s.goalCompactionSteering() {
		messages = append(messages, preCompactMessage{text: m, kind: events.SteeringKindGoalObjective})
	}
	records = appendSteeringMessagesToHistory(history, messages)
	if len(deferred) > 0 {
		commit = func() {
			for _, c := range deferred {
				c()
			}
		}
	}
	return records, commit
}

func appendSteeringMessagesToHistory(history *[]schema.Turn, messages []preCompactMessage) []steeringTurnRecord {
	var records []steeringTurnRecord
	for _, msg := range messages {
		if strings.TrimSpace(msg.text) == "" {
			continue
		}
		turn := schema.NewTurn(schema.TurnSteering, llm.User(msg.text))
		turn.SteeringKind = msg.kind
		*history = append(*history, turn)
		records = append(records, steeringTurnRecord{turn: turn, text: msg.text, kind: msg.kind})
	}
	return records
}

// writeSteeringTurnRecordsLocked appends the records' turns to the transcript
// one at a time. It is no longer the fold path — the publication transaction
// now writes its steering turns inside the one durable batch its
// commitTranscriptsLocked builds — and survives only for the test-only
// flushSteeringTurnRecords convenience below. Callers hold attentionMu. The
// returned errors align with records; they are reported later by
// emitSteeringTurnRecords, outside the locks, where emitting is safe.
func (s *Session) writeSteeringTurnRecordsLocked(records []steeringTurnRecord) []error {
	if len(records) == 0 {
		return nil
	}
	errs := make([]error, len(records))
	for i, record := range records {
		// appendCompactionTurn is a FAILURE injector, matching the fold path: a
		// non-nil return stands in for a write failure, a nil return injects
		// nothing and the turn is written for real.
		if appendTurn := s.cfg.testOnly.appendCompactionTurn; appendTurn != nil {
			if err := appendTurn(record.turn); err != nil {
				errs[i] = err
				continue
			}
		}
		errs[i] = s.writeTranscriptLocked(record.turn)
	}
	return errs
}

// emitSteeringTurnRecords reports the records' transcript-write outcomes and
// their EventSteeringInjected events; errs aligns with records.
func (s *Session) emitSteeringTurnRecords(records []steeringTurnRecord, errs []error) {
	for i, record := range records {
		if i < len(errs) && errs[i] != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", errs[i])})
		}
		s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: record.text, Kind: record.kind})
	}
}

// flushSteeringTurnRecords writes and reports records in one step, for a
// caller outside any publication transaction. Test-only: the fold's steering
// turns now go through the publication transaction's durable batch
// (commitTranscriptsLocked), so no production caller remains and this
// survives as the one-step convenience the package tests drive directly.
func (s *Session) flushSteeringTurnRecords(records []steeringTurnRecord) {
	s.attentionMu.Lock()
	errs := s.writeSteeringTurnRecordsLocked(records)
	s.attentionMu.Unlock()
	s.emitSteeringTurnRecords(records, errs)
}

// buildCompactionMeta gathers session-level metadata for enriching compaction summaries.
func (s *Session) buildCompactionMeta() contextmgr.CompactionMeta {
	meta := contextmgr.CompactionMeta{}

	// Session id — only populated for persistent sessions (stateDir set), where transcript tools are available.
	if s.stateDir != "" {
		meta.SessionID = s.id
	}

	// A persistent session is not automatically a session that can READ its
	// transcript: a typed agent's tools: allowlist can drop either transcript
	// tool. The checkpoint's recovery instruction is worded from what is
	// actually registered here.
	for _, name := range []string{"read_transcript", "find_session_transcripts"} {
		if s.canInstructTool(name) {
			meta.AvailableTranscriptTools = append(meta.AvailableTranscriptTools, name)
		}
	}

	return meta
}
