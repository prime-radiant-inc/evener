package agent

import (
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// notesSnapshot reads the committed human and agent notes.
func (s *Session) notesSnapshot() (human, agentNote string) {
	human, agentNote, _ = s.notesSnapshotAll()
	return human, agentNote
}

// committedNotesState is one committed notes cut: every notes value a reader
// may observe, installed together through Session.notesCommitted.
//
// The human note carries no presence flag. Every reader surface (Meta,
// notesSnapshot, notesSnapshotAll, the projection renderer) collapses
// "authority not established" and "saved clear" to "", exactly as the reads did
// before this cut existed, so no consumer distinguishes them;
// ReadCanonicalHumanNote still reports presence from disk for callers that act
// on it.
type committedNotesState struct {
	human         string
	agent         string
	urls          []schema.SessionURL
	everProjected bool
}

// committedNotesBaseLocked builds the next published cut from the live store,
// carrying the previously published human note forward unless humanOverride is
// non-nil. Callers hold notesUpdateMu (construction is single-threaded and runs
// before the session is shared), which serializes every publisher, so the
// carried value cannot move underneath them.
func (s *Session) committedNotesBaseLocked(humanOverride *string) committedNotesState {
	s.mu.Lock()
	next := committedNotesState{
		agent:         s.agentNote,
		urls:          append([]schema.SessionURL(nil), s.sessionURLs...),
		everProjected: s.notesEverProjected,
	}
	s.mu.Unlock()
	if published := s.notesCommitted.Load(); published != nil {
		next.human = published.human
	}
	if humanOverride != nil {
		next.human = *humanOverride
	}
	return next
}

// publishCommittedNotesLocked publishes the live store as the committed cut.
// Callers hold notesUpdateMu and call it only at a durability point: after a
// notes mutator's metadata save returned nil, or when a projection records the
// ever-projected flag. The failure paths (a rolled-back mutation, a rolled-back
// URL removal) never call it, so a value a reader observed is never retracted;
// restoreSessionURLsLocked deliberately has no publish of its own.
func (s *Session) publishCommittedNotesLocked() {
	next := s.committedNotesBaseLocked(nil)
	s.notesCommitted.Store(&next)
}

// publishCommittedHumanNoteLocked publishes the mutation store's committed
// canonical human note as the cut's human field, carrying the agent note, URL
// list, and ever-projected flag forward. SetHumanNote is the only writer of the
// committed human note, and every notes publisher holds notesUpdateMu, so the
// carried fields cannot move underneath this publish.
func (s *Session) publishCommittedHumanNoteLocked() {
	human := ""
	if s.clientMutations != nil {
		human = s.clientMutations.committedHumanNote()
	}
	next := s.committedNotesBaseLocked(&human)
	s.notesCommitted.Store(&next)
}

// seedCommittedNotes installs the first published cut during session
// construction: the restored (or empty) live store plus the mutation store's
// committed human note. It runs before the session serves any reader, so it
// needs no publisher lock.
func (s *Session) seedCommittedNotes() {
	human := ""
	if s.clientMutations != nil {
		human = s.clientMutations.committedHumanNote()
	}
	next := s.committedNotesBaseLocked(&human)
	s.notesCommitted.Store(&next)
}

// notesLiveSnapshot reads the notes store as staged: the live agent note, URL
// list, and ever-projected flag, plus the mutation store's committed human
// note. The metadata write uses it, because that write is the durability point
// for a mutation whose value is still staged; readers take the published
// committed cut instead (notesProjectionSnapshot).
func (s *Session) notesLiveSnapshot() (human, agent string, urls []schema.SessionURL, everProjected bool) {
	s.mu.Lock()
	agent, everProjected = s.agentNote, s.notesEverProjected
	urls = append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	if s.clientMutations != nil {
		human = s.clientMutations.committedHumanNote()
	}
	return human, agent, urls, everProjected
}

// notesSnapshotAll reads the committed human note, agent note, and URL list as
// one cut.
func (s *Session) notesSnapshotAll() (human, agent string, urls []schema.SessionURL) {
	human, agent, urls, _ = s.notesProjectionSnapshot()
	return human, agent, urls
}

// notesProjectionSnapshot returns one committed notes cut: the human note,
// agent note, URL list, and ever-projected flag all come from a single atomic
// load of the published cut, so a concurrent mutation can no longer make the
// result a mix of pre- and post-mutation values — and a mutation that is still
// inside its metadata save is not visible at all until that save lands.
//
// The fallback covers a session that was never built through NewSession or
// RestoreSessionFromMetaWithConfig: tests construct &Session{} directly and seed the live
// fields, and those sessions have no published cut. It reads the live store
// exactly as every reader did before the cut existed.
func (s *Session) notesProjectionSnapshot() (human, agent string, urls []schema.SessionURL, everProjected bool) {
	if published := s.notesCommitted.Load(); published != nil {
		return published.human, published.agent, append([]schema.SessionURL(nil), published.urls...), published.everProjected
	}
	s.mu.Lock()
	agent, everProjected = s.agentNote, s.notesEverProjected
	urls = append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	if s.clientMutations != nil {
		s.clientMutations.stateMu.RLock()
		if canonical := s.clientMutations.state.HumanNote; canonical != nil {
			human = *canonical
		}
		s.clientMutations.stateMu.RUnlock()
	}
	return human, agent, urls, everProjected
}

// maybeAppendNotesContext records a NOTES_CONTEXT turn carrying the current
// shared-notes block. It runs beside the goal continuation-prompt rendering
// at turn start and on resume, and before the next round when a note event
// landed, so human URL removals reach the agent even though the wire pushes
// travel hub-ward. The persisted note is the source of truth; this turn is
// its per-turn projection.
//
// The projection appends only when the rendered block differs from the last
// projected block (a per-session last-projected record under mu): unchanged
// state across consecutive rounds appends nothing, bounding the steady-state
// cost that re-rendering a limit-sized block every round would impose. A
// transition to empty still appends the explicit cleared marker (M3), so the
// next model request reflects the cleared list instead of the stale snapshot.
// resetNotesProjectionAfterCompaction clears the record when compaction folds
// history away, so the next projection re-emits the full current state the
// model can no longer see (the resume/compaction guarantee).
func (s *Session) maybeAppendNotesContext() {
	// Render under the same update lock that serializes every notes store
	// mutation: a note/URL commit landing between a lock-free render and the
	// bookkeeping below would let this older snapshot append AFTER the newer
	// mutation's own projection and reach the next model request stale. The
	// joined render-compare-record runs as one unit, so the appended turn
	// always matches the last-projected record.
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	block := s.notesContextBlock()
	if block == "" {
		return
	}
	// Read beside the raw block and under the same lock, so the pair cannot
	// straddle a notes change; the escaped copy is what the model reads.
	modelBlock := s.notesContextBlockForModel()
	s.mu.Lock()
	if block == s.notesLastProjected {
		s.mu.Unlock()
		return
	}
	s.notesLastProjected = block
	s.notesEverProjected = true
	// appendTurn takes the history lock itself; holding s.mu across it would
	// invert the mu-before-history order the lifecycle path relies on. The
	// record above is set before the append: a concurrent projection attempt
	// sees the newer record and stays silent instead of double-appending.
	turn := schema.TurnNotesContext
	body := llm.User(block)
	modelBody := llm.User(modelBlock)
	s.mu.Unlock()
	// The ever-projected transition is part of the committed cut: readers use it
	// to decide whether an empty store renders the explicit cleared marker, so
	// publish it before the turn that records the projection. It publishes from
	// the cut already published rather than re-reading the live staging store, so
	// the transition can never carry a value a mutator has staged but not yet
	// saved — even if a future caller reaches this without notesUpdateMu.
	if published := s.notesCommitted.Load(); published != nil && !published.everProjected {
		next := *published
		next.everProjected = true
		s.notesCommitted.Store(&next)
	}
	s.appendTurnWithTranscriptMessage(turn, modelBody, body)
}

// resetNotesProjectionAfterCompaction clears the last-projected notes record
// when a CHECKPOINT/SUMMARY turn replaces history: compaction folds away any
// previously appended NOTES_CONTEXT turns, so the model loses whatever state
// was last projected. Clearing forces the next maybeAppendNotesContext call
// to re-emit the full current block rather than staying silent on state the
// model can no longer see.
func (s *Session) resetNotesProjectionAfterCompaction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notesLastProjected = ""
}

// lastNotesProjection returns the raw block of the last NOTES_CONTEXT turn in a
// restored history and whether any such turn was present. The projection record
// holds the raw render, because that is what a fresh render is compared against
// to gate the next append, so this must be read before escapeNotesHistoryTurns
// rewrites those turns for model context. Any NOTES_CONTEXT turn at all marks the
// store as having been projected (the transition-to-empty rule), even when the
// current store is empty.
func lastNotesProjection(history []schema.Turn) (block string, present bool) {
	for i := range slices.Backward(history) {
		if history[i].Kind != schema.TurnNotesContext {
			continue
		}
		return history[i].Message.Text(), true
	}
	return "", false
}
