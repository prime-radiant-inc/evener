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
	"primeradiant.com/evener/llm"
)

// Compact forces context compaction regardless of current pressure.
// Runs all compaction layers (observation masking, thinking clearing,
// checkpoint, and LLM summarization). Safe to call while idle.
func (s *Session) Compact(ctx context.Context) error {
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
//     entries (its markers, its injected steering, and last of all its fold
//     record). A turn recorded concurrently (recordTurn: history append, then
//     transcript write) either completes entirely before this publish (its
//     entry precedes the fold's markers, and the fold's snapshot or merge-back
//     accounts for the turn itself, so the fold record names it by Seq) or has
//     its transcript write queue behind this transaction, sequencing its entry
//     after the fold record — where ResumeHistory reads it as live post-fold
//     history. A competing fold's own transaction queues the same way, so a
//     fold's markers and record always land as one contiguous, publish-ordered
//     run. The fold record names, by durable Seq, the entries its published
//     history is made of (markers at the head, then the pre-existing turns it
//     kept — including the pairs recorded during the fold, whose pre-marker
//     entries already hold the persisted form recordTurn wrote), so the
//     retained tail survives restart without any entry being re-appended.
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
func (s *Session) publishFoldTransaction(snapLen, snapRevision int, folded []schema.Turn, commit *foldCommit, onPublishLocked func(published []schema.Turn)) (published []schema.Turn, ok bool, refusal error) {
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
	if s.attachedTranscript().Poisoned() {
		s.attentionMu.Unlock()
		return nil, false, errTranscriptRefusesRecords()
	}
	s.mu.Lock()
	previousEnvironmentIDs := environmentTurnIDs(s.history)
	published, ok = s.publishFoldedHistory(snapLen, snapRevision, folded)
	if !ok {
		s.mu.Unlock()
		s.attentionMu.Unlock()
		return nil, false, nil
	}
	// The turns publishFoldedHistory merged back (recorded concurrently during
	// the fold) are exactly the tail past the fold's own result. They carry
	// durable entries (recordTurn wrote them), so the fold record names them by
	// their Seq unconditionally — unlike the strategy-kept tail, where an
	// unwritten (Seq-less) turn is a steering injection or, in tests that seed
	// history without a transcript, nothing to resume.
	commit.mergeBackCount = len(published) - len(folded)
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
	commit.commitTranscriptsLocked(published)
	s.attentionMu.Unlock()
	// The fold's buffered marker/steering writes and its durable merged-tail
	// copies each queue a diagnostic when a whole line landed but did not sync;
	// this is the fold's one owner for surfacing them, outside the door.
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
		s.mu.Unlock()

		compactionCtx, emitFn, commit, foldInjectedCount := s.stageCompactionEffects(ctx, &histCopy)
		commit.captured = captured
		s.contextMgr.ForceCompact(compactionCtx, &histCopy, instructions, emitFn)
		postLen := len(histCopy)
		injected := foldInjectedCount()

		_, published, refused := s.publishFoldTransaction(snapLen, snapRevision, histCopy, commit, func([]schema.Turn) {
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
	s.steerKindForFold("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"outline\"}), then read a focused range with read_transcript({\"transcript_ref\": \""+ref+"\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>", events.SteeringKindTranscriptPointer, publishedRevision)
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
	commitTranscriptsLocked      func(published []schema.Turn)
	flush                        func()
	resetEnvContextTrackerLocked func(bool)
	publishedRevision            int
	mergeBackCount               int
	actualCompaction             bool
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

func withNoteClaimRegistrar(ctx context.Context, register func(claimLocked func())) context.Context {
	return context.WithValue(ctx, noteClaimRegistrarKey{}, register)
}

// registerNoteClaim hands the fold's generation-checked note claim to the
// enclosing fold staging for its publication transaction. It reports false
// when no registrar is installed — a direct runPreCompactHook caller outside
// any publication transaction — in which case the claim belongs with the
// caller's own deferred commit.
func registerNoteClaim(ctx context.Context, claimLocked func()) bool {
	register, ok := ctx.Value(noteClaimRegistrarKey{}).(func(func()))
	if !ok {
		return false
	}
	register(claimLocked)
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
	var noteClaimLocked func()
	ctx = withNoteClaimRegistrar(ctx, func(claimLocked func()) { noteClaimLocked = claimLocked })

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
	// recorded after the fold). It also attaches the publication's staged
	// handoff receipt to each compaction turn, so the durable transcript
	// carries the typed record a restart reconciles from. Write errors are
	// carried into flush, where emitting is safe again.
	commit := &foldCommit{}
	var compactionTurnWriteErrs []error
	var steeringWriteErrs []error
	commitTranscriptsLocked := func(published []schema.Turn) {
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
		markerSeqs := make([]int, len(pendingCompactionTurns))
		for i, turn := range pendingCompactionTurns {
			seq, err := s.writeTranscriptLocked(turn)
			compactionTurnWriteErrs[i] = err
			markerSeqs[i] = recordedSeq(seq, err) // NoTranscriptEntrySeq on a failed write, so the fold names no phantom entry
		}
		steeringSeqs, errs := s.writeSteeringTurnRecordsLocked(pendingSteering)
		steeringWriteErrs = errs
		foldID := fmt.Sprintf("fold-%d", commit.publishedRevision)
		if commit.receipt != nil && commit.receipt.Operation.PublicationID != "" {
			foldID = commit.receipt.Operation.PublicationID
		}
		s.writeFoldRecordLocked(published, commit.mergeBackCount, pendingCompactionTurns, markerSeqs, steeringSeqs, foldID)
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
		if noteClaimLocked != nil {
			noteClaimLocked()
		}
	}
	commit.commitTranscriptsLocked = commitTranscriptsLocked
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
		if !registerNoteClaim(ctx, claimLocked) {
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

// writeSteeringTurnRecordsLocked appends the records' turns to the
// transcript. Callers hold attentionMu (the publication transaction's
// transcript-commit phase). The returned errors align with records; they are
// reported later by emitSteeringTurnRecords, outside the locks, where
// emitting is safe.
// It returns, aligned with records, each written entry's durable Seq (0 when
// the test seam intercepts the write) so the fold record can name the steering
// turns it kept by Seq.
func (s *Session) writeSteeringTurnRecordsLocked(records []steeringTurnRecord) (seqs []int, errs []error) {
	if len(records) == 0 {
		return nil, nil
	}
	seqs = make([]int, len(records))
	errs = make([]error, len(records))
	for i, record := range records {
		if appendTurn := s.cfg.testOnly.appendCompactionTurn; appendTurn != nil {
			errs[i] = appendTurn(record.turn)
			seqs[i] = recordedSeq(0, errs[i])
			continue
		}
		seq, err := s.writeTranscriptLocked(record.turn)
		seqs[i] = recordedSeq(seq, err) // NoTranscriptEntrySeq on a failed write
		errs[i] = err
	}
	return seqs, errs
}

// writeFoldRecordLocked appends the fold's durable manifest — the transcript
// entries its published history is made of, named by Seq — so a restart
// rebuilds exactly this history rather than anchoring on the last marker and
// dropping the retained tail (#1200). It runs inside the publication
// transaction's transcript-commit phase (attentionMu held), AFTER the fold's
// markers and injected steering turns are written and their Seqs learned, so
// the record is the last entry the fold writes and everything after it is live
// post-fold history.
//
// published is the folded history: [head marker, ...strategy-kept tail...,
// ...merge-back...]. mergeBackCount is how many of its trailing turns
// publishFoldedHistory merged back (recorded concurrently during the fold).
// The head marker's Seq comes from markers/markerSeqs, and is used only when
// the marker's own write recorded it (otherwise no record is written). In the
// strategy-kept tail each turn is classified by its Seq: a positive Seq is a
// pre-existing durable entry; NoTranscriptEntrySeq is a repair synthetic or
// strategy injection with no entry (omitted); Seq 0 is a steering turn the fold
// injected, whose durable entry is the next steering write's, taken from
// steeringSeqs in order (a failed steering write recorded nothing and is
// omitted). Merge-back turns went through recordTurn, so a recorded one is
// named by its real Seq (possibly 0 when a seeded prefix bypassed the
// transcript) and a failed one carries NoTranscriptEntrySeq and is omitted. A
// fold that produced no marker writes no record — there is no pre-marker tail
// for the anchor to drop.
func (s *Session) writeFoldRecordLocked(published []schema.Turn, mergeBackCount int, markers []schema.Turn, markerSeqs, steeringSeqs []int, foldID string) {
	if len(published) == 0 || len(markers) == 0 {
		return
	}
	head := published[0]
	if head.Kind != schema.TurnCheckpoint && head.Kind != schema.TurnSummary {
		return
	}
	headSeq, found := -1, false
	for i := range markers {
		if markers[i].Kind == head.Kind {
			headSeq, found = markerSeqs[i], true
		}
	}
	if !found || headSeq < 0 {
		// The head marker's own write recorded nothing (a failure, or a hold
		// before the transcript attached): its seq is NoTranscriptEntrySeq (or
		// the unspent next seq the fold record itself would take). There is no
		// durable marker to anchor a restart on, so write no record — resume
		// falls back to the pre-fold history rather than naming a phantom
		// entry (which the unspent-seq reuse would resolve to this very record).
		return
	}
	keptTail := published[1 : len(published)-mergeBackCount]
	mergeBack := published[len(published)-mergeBackCount:]
	retained := make([]int, 0, len(published)-1)
	steerIdx := 0
	for _, turn := range keptTail {
		switch {
		case turn.Seq > 0:
			// A pre-existing turn with a durable entry. (A kept turn's entry is
			// never Seq 0: entry 0 is the session's first turn, always in the
			// prefix a marker-producing fold discarded.)
			retained = append(retained, turn.Seq)
		case turn.Seq == schema.NoTranscriptEntrySeq:
			// A repair synthetic or strategy injection: no durable entry, omit.
		default: // turn.Seq == 0
			// A steering turn the fold injected: its history copy is unstamped,
			// so its durable entry's Seq is the next steering write's, consumed
			// in order. A write that recorded nothing carries
			// NoTranscriptEntrySeq and is omitted.
			if steerIdx < len(steeringSeqs) {
				if seq := steeringSeqs[steerIdx]; seq >= 0 {
					retained = append(retained, seq)
				}
				steerIdx++
			}
		}
	}
	for _, turn := range mergeBack {
		// Merge-back turns always went through recordTurn, so a recorded one
		// carries its real Seq (possibly 0 when a seeded prefix bypassed the
		// transcript) and a failed one carries NoTranscriptEntrySeq.
		if turn.Seq >= 0 {
			retained = append(retained, turn.Seq)
		}
	}
	record := schema.NewTurn(schema.TurnFoldRecord, llm.Message{})
	record.Fold = &schema.FoldRecord{FoldID: foldID, Layers: []int{headSeq}, RetainedSeqs: retained}
	_, _ = s.writeTranscriptDurableLocked(record)
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
// caller outside any publication transaction. No production caller remains
// since the transaction split — writeSteeringTurnRecordsLocked and
// emitSteeringTurnRecords own the fold path — so this survives as the
// one-step convenience the package tests drive directly.
func (s *Session) flushSteeringTurnRecords(records []steeringTurnRecord) {
	s.attentionMu.Lock()
	_, errs := s.writeSteeringTurnRecordsLocked(records)
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
