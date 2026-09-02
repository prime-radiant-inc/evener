package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

	s.contextMgr.Meta = s.buildCompactionMeta()

	// /compact is server-exposed while a thread is active, and the
	// askPendingCount() guard above does not check for an active round loop —
	// so this can race another ForceCompact/ManageContext publisher
	// (applyPendingForceCompact, the content-filter retry, or the round
	// loop's own ManageContext). foldWithForceCompact retries once against
	// the current history on conflict; report to the
	// caller rather than silently no-op'ing if both attempts lose the race.
	if !s.foldWithForceCompact(ctx, "") {
		return errors.New("a concurrent compaction is already in progress; try again")
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
// (a plain length check cannot see this: a competing fold can leave s.history
// the same length or even longer while still replacing its content). s.historyRevision — bumped only by a
// successful publish here, never by an ordinary append elsewhere — is the
// signal: unchanged means no competing fold published, so the length-based
// merge is sound; changed means one did, and this reports the conflict
// instead of publishing.
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
//     compaction marker and discards every entry before it. A competing fold's own transaction queues the same way, so
//     compaction markers always land in publish order.
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
//     these locks, and none of them need the ordering guarantee.
//
// onPublishLocked, when non-nil, runs under s.mu immediately after a
// successful publish — the publisher's baseline correction, per
// publishFoldedHistory's contract. On conflict NOTHING is committed and
// ok=false; the caller retries against the now-current history or aborts.
func (s *Session) publishFoldTransaction(snapLen, snapRevision int, folded []schema.Turn, commit *foldCommit, onPublishLocked func(published []schema.Turn)) (published []schema.Turn, ok bool) {
	s.attentionMu.Lock()
	s.mu.Lock()
	published, ok = s.publishFoldedHistory(snapLen, snapRevision, folded)
	if !ok {
		s.mu.Unlock()
		s.attentionMu.Unlock()
		return nil, false
	}
	if onPublishLocked != nil {
		onPublishLocked(published)
	}
	commit.claimNoteLocked()
	s.mu.Unlock()
	if hook := s.cfg.testOnly.beforeFoldTranscriptCommit; hook != nil {
		hook()
	}
	commit.commitTranscriptsLocked()
	s.attentionMu.Unlock()
	if hook := s.cfg.testOnly.beforeFoldSideEffectsFlush; hook != nil {
		hook()
	}
	commit.flush()
	return published, true
}

// foldWithForceCompact snapshots s.history, runs ForceCompact with the given
// instructions, and publishes the result via publishFoldedHistory — retrying
// once (a fresh snapshot, a fresh fold) if a competing fold wins the publish
// race, since a stale fold result is worthless to re-publish. Every
// ForceCompact caller shares this exact shape: Compact,
// applyPendingForceCompact, and handleModelError's content-filter retry.
//
// On success, applies shrinkTurnHistoryBaseline atomically with the publish
// (using the fold's own pre/post lengths, never the merged-in result — see
// publishFoldedHistory) and returns ok=true. On ok=false, both attempts lost
// the publish race: s.history is whatever the winning competitor left it as,
// and this fold's work — including the shrink it would have applied — is
// entirely discarded; the caller decides what that means for it.
func (s *Session) foldWithForceCompact(ctx context.Context, instructions string) (ok bool) {
	const maxAttempts = 2
	for range maxAttempts {
		s.mu.Lock()
		histCopy := append([]schema.Turn{}, s.history...)
		snapLen := len(s.history)
		snapRevision := s.historyRevision
		s.mu.Unlock()

		compactionCtx, emitFn, commit, foldInjectedCount := s.stageCompactionEffects(ctx, &histCopy)
		s.contextMgr.ForceCompact(compactionCtx, &histCopy, instructions, emitFn)
		postLen := len(histCopy)
		injected := foldInjectedCount()

		if _, published := s.publishFoldTransaction(snapLen, snapRevision, histCopy, commit, func([]schema.Turn) {
			s.shrinkTurnHistoryBaseline(snapLen, postLen, injected)
		}); published {
			return true
		}
		// Conflict: loop retries against the now-current history. commit is
		// deliberately NOT run — this attempt's side effects must not take
		// effect for a fold that never published.
	}
	return false
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

func (s *Session) steerCompactionTranscriptReminder() {
	if s.stateDir == "" || s.id == "" {
		return
	}
	// The reminder is a read_transcript call recipe end to end; a session
	// without that tool is told nothing rather than told to call it.
	if !s.canInstructTool("read_transcript") {
		return
	}
	ref := encodeRef("", s.id)
	s.SteerKind("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"outline\"}), then read a focused range with read_transcript({\"transcript_ref\": \""+ref+"\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>", events.SteeringKindTranscriptPointer)
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
type foldCommit struct {
	claimNoteLocked         func()
	commitTranscriptsLocked func()
	flush                   func()
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

// compactionEmitFunc is stageCompactionEffects for a caller OUTSIDE a
// publication transaction: the returned commit closure performs the full
// commit immediately — the note claim (under s.mu) and then the deferred
// flush. Production publishers use stageCompactionEffects directly so the
// claim runs inside their publish critical section.
func (s *Session) compactionEmitFunc(ctx context.Context, history *[]schema.Turn) (context.Context, func(events.EventKind, events.EventData), func(), func() int) {
	ctx, emitFn, commit, injected := s.stageCompactionEffects(ctx, history)
	commitNow := func() {
		s.mu.Lock()
		commit.claimNoteLocked()
		s.mu.Unlock()
		s.attentionMu.Lock()
		commit.commitTranscriptsLocked()
		s.attentionMu.Unlock()
		commit.flush()
	}
	return ctx, emitFn, commitNow, injected
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
	// recorded after the fold). Write errors are carried into flush, where
	// emitting is safe again.
	var compactionTurnWriteErrs []error
	var steeringWriteErrs []error
	commitTranscriptsLocked := func() {
		compactionTurnWriteErrs = make([]error, len(pendingCompactionTurns))
		for i, turn := range pendingCompactionTurns {
			compactionTurnWriteErrs[i] = s.writeTranscriptLocked(turn)
		}
		steeringWriteErrs = s.writeSteeringTurnRecordsLocked(pendingSteering)
	}
	flush := func() {
		for _, ccd := range pendingCompactionEvents {
			s.emit(events.EventContextCompaction, ccd)
		}
		for i, turn := range pendingCompactionTurns {
			s.handleCompactionTurnEffects(turn, compactionTurnWriteErrs[i])
		}
		s.emitSteeringTurnRecords(pendingSteering, steeringWriteErrs)
		if artifactProduced {
			s.steerCompactionTranscriptReminder()
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
	commit := &foldCommit{
		claimNoteLocked: func() {
			if noteClaimLocked != nil {
				noteClaimLocked()
			}
		},
		commitTranscriptsLocked: commitTranscriptsLocked,
		flush:                   flush,
	}
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
func (s *Session) writeSteeringTurnRecordsLocked(records []steeringTurnRecord) []error {
	if len(records) == 0 {
		return nil
	}
	errs := make([]error, len(records))
	for i, record := range records {
		if appendTurn := s.cfg.testOnly.appendCompactionTurn; appendTurn != nil {
			errs[i] = appendTurn(record.turn)
			continue
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
// caller outside any publication transaction.
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
