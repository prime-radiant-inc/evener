package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Shared-notes daemon path: the hub RPCs (notes/human/set, hub-side
// urls/remove) land on the session here, beside the agent-tool handlers in
// session_tools_notes.go. The event shapes match the tool emissions exactly
// (notesUpdatedData/urlsUpdatedData), so the projector sees one stream no
// matter which channel mutated the store.

// Outer mutation methods for the daemon notes verbs. They name the
// retry-safe journal records below, mirroring the turn-verb method constants
// beside executeAtomic.
const (
	clientMutationMethodNotesHumanSet = "notes/human/set"
	clientMutationMethodUrlsRemove    = "urls/remove"
)

// SetHumanNote atomically accepts the canonical whiteboard and its notification.
// The raw request is journaled so retries cannot change their payload.
func (s *Session) SetHumanNote(clientMutationID, note string) (appwire.NotesHumanSetResponse, error) {
	clientMutationID = strings.TrimSpace(clientMutationID)
	if clientMutationID == "" {
		return appwire.NotesHumanSetResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.NotesHumanSetResponse{}, NormalizeClientMutationError(clientMutationID, err)
	}
	request, err := newClientMutationRequest(clientMutationMethodNotesHumanSet, clientMutationID, struct{ Note string }{Note: note})
	if err != nil {
		return appwire.NotesHumanSetResponse{}, NormalizeClientMutationError(clientMutationID, err)
	}
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	changed := false
	lookup, err := s.clientMutations.executeAtomic(request, nil, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		current := ""
		if snapshot.HumanNote != nil {
			current = *snapshot.HumanNote
		}
		stored := normalizeNote(note)
		changed = stored != current
		if changed && snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		projection := appwire.MutationProjectionRemoved
		if changed {
			reserveClientMutationTurnID(snapshot, record)
			record.SteeringKind = events.SteeringKindHumanNote
			text := "human updated their whiteboard: " + stored
			if stored == "" {
				text = "human updated their whiteboard: (whiteboard cleared)"
			}
			addPendingSteering(snapshot, record, []appwire.InputItem{{Type: "text", Text: text}})
			projection = acceptedClientMutationProjection(record.Method)
		}
		snapshot.HumanNote = &stored
		response := appwire.NotesHumanSetResponse{Note: stored, Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, projection)}
		result, err := json.Marshal(response)
		if err != nil {
			return err
		}
		applyClientMutationRecord(record, result, projection)
		return nil
	})
	// A post-rename error can still be a committed effect. Project and wake
	// that acceptance before reporting uncertainty, so live reads do not wait
	// for a client retry to discover durable state.
	if err != nil && changed {
		lookup.Record = s.clientMutations.snapshot().Journal[clientMutationID]
	}
	if lookup.Record.OperationState == clientMutationOperationApplied || lookup.Record.OperationState == clientMutationOperationTerminal {
		s.reflectDurableClientSteering()
		s.wakeForPendingSteering()
		if changed {
			human, agentNote := s.notesSnapshot()
			s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
		}
	}
	if err != nil {
		return appwire.NotesHumanSetResponse{}, NormalizeClientMutationError(clientMutationID, err)
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.NotesHumanSetResponse{}, clientMutationRejectionError(lookup.Record)
	}
	var response appwire.NotesHumanSetResponse
	if err := replayClientMutationResult(lookup.Record, &response); err != nil {
		return response, NormalizeClientMutationError(clientMutationID, err)
	}
	disposition := appwire.MutationDispositionApplied
	if lookup.Disposition == clientMutationDispositionReplayed {
		disposition = appwire.MutationDispositionReplayed
	}
	response.Receipt = mutationReceipt(s.ID(), lookup.Record, disposition, lookup.Record.ProjectionState)
	return response, nil
}

// notesSnapshotPair re-reads the snapshot for a return tuple.
func (s *Session) notesSnapshotPair(stored string, changed bool) (string, bool, string, string, error) {
	human, agentNote := s.notesSnapshot()
	return stored, changed, human, agentNote, nil
}

// persistNotesMeta persists the notes store, propagating a failure so the
// caller refuses to journal success for a write that never landed. A
// test-injected fault surfaces as the mutation error.
//
// persistNotesMetaLocked is the same write for callers that already hold
// metaSaveMu (a mutation+rollback critical section): the lock must stay held
// from before the tentative mutation through persistence and rollback, so a
// concurrent maybeAutoSave cannot snapshot and persist uncommitted state.
func (s *Session) persistNotesMeta() error {
	if fault := s.cfg.testOnly.notesAutoSaveFault; fault != nil {
		if err := fault(); err != nil {
			return err
		}
	}
	return s.autoSaveMeta()
}

func (s *Session) persistNotesMetaLocked() error {
	if fault := s.cfg.testOnly.notesAutoSaveFault; fault != nil {
		if err := fault(); err != nil {
			return err
		}
	}
	return s.autoSaveMetaLocked()
}

// RemoveSessionURL removes one URL list entry by id for the daemon's hub-side
// urls/remove RPC, emitting EventUrlsUpdated so the projector derives the
// evener/urls/updated push from the same shape the agent tools emit.
// outerID is the hub RPC's clientMutationId: success journals beside it even
// though the entry is gone, so a retry replays success instead of reporting
// an unknown id, and a reused ID with a different entry id conflicts without
// mutating.
func (s *Session) RemoveSessionURL(outerID, id string) (bool, error) {
	outerID = strings.TrimSpace(outerID)
	if outerID == "" {
		return false, appwire.InvalidParams("clientMutationId is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, appwire.InvalidParams("id is required")
	}
	if err := s.ensureClientMutationStore(); err != nil {
		return false, err
	}
	request, err := newClientMutationRequest(clientMutationMethodUrlsRemove, outerID, struct {
		ID string
	}{ID: id})
	if err != nil {
		return false, NormalizeClientMutationError(outerID, err)
	}
	lookup, err := s.clientMutations.reservePrepared(request, nil)
	if err != nil {
		// Same raw-mismatch contract as SetHumanNote.
		return false, err
	}
	switch {
	case lookup.Record.OperationState == clientMutationOperationRejected:
		return false, clientMutationRejectionError(lookup.Record)
	case lookup.Disposition == clientMutationDispositionReplayed:
		var recorded appwire.UrlsRemoveResponse
		if err := replayClientMutationResult(lookup.Record, &recorded); err != nil {
			return false, NormalizeClientMutationError(outerID, err)
		}
		return true, nil
	case lookup.Disposition == clientMutationDispositionJoined:
		<-lookup.OwnerDone
		return s.RemoveSessionURL(outerID, id)
	}
	if lookup.Lease == nil {
		return false, appwire.InternalError("notes mutation owner is missing")
	}
	// The removal, its persistence, and its announcement are one serialized
	// unit under notesUpdateMu, mirroring the human-note path: concurrent
	// URL mutations publish in store order and a stale event never wins at
	// the projector (G1).
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	prev := s.snapshotSessionURLsLocked()
	removed := s.removeSessionURL(id)
	if !removed {
		s.metaSaveMu.Unlock()
		if lookup.Record.AttemptGeneration > 1 {
			// Crash-recovery takeover: the pre-crash attempt passed validation
			// (only unseen IDs reach the reservation) and removed the entry
			// before dying, so the entry's absence IS its success. An
			// AttemptGeneration of 1 is a fresh reservation, where absence
			// means a genuinely unknown id.
			if err := s.persistNotesMeta(); err != nil {
				lookup.Lease.Release()
				return false, err
			}
			// The removal's announcement belongs to the removal, not to the
			// crash: re-emit the current list so the projector converges on
			// the post-removal state the success journal records (G2).
			s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLsLocked()))
			return s.applyUrlsRemoveResult(lookup.Lease, outerID)
		}
		unknown := appwire.InvalidParams("no URL entry with id " + id)
		if err := s.clientMutations.update(lookup.Lease, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
			rejectClientMutation(record, unknown)
			return nil
		}); err != nil {
			return false, NormalizeClientMutationError(outerID, err)
		}
		return false, unknown
	}
	// The metadata save gates the success journal: a persistence failure
	// releases the reservation with no recorded result, so the entry's
	// absence is not journaled as success for a write that never landed
	// and a retry still owns the removal. The store rolls back to the
	// pre-removal list, so the removed entry is present again for the retry
	// to own instead of standing removed-but-unannounced (G2). The
	// mutation+save+rollback holds metaSaveMu so a concurrent autosave cannot
	// snapshot the transient removal before the save or during rollback.
	if err := s.persistNotesMetaLocked(); err != nil {
		s.restoreSessionURLsLocked(prev)
		s.metaSaveMu.Unlock()
		lookup.Lease.Release()
		return false, err
	}
	s.metaSaveMu.Unlock()
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLsLocked()))
	return s.applyUrlsRemoveResult(lookup.Lease, outerID)
}

// removeSessionURLSerialized deletes the entry with id and captures the
// resulting list. It exists for the agent-tool surface, whose persistence
// runs in the tool handler: the update lock is held across the mutation AND
// the snapshot capture, so concurrent URL mutations snapshot in store
// order. The daemon RPC path above serializes further — through persistence
// and emission — and does not use this helper.
func (s *Session) removeSessionURLSerialized(id string) (bool, []schema.SessionURL) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	removed := s.removeSessionURL(id)
	s.mu.Lock()
	urls := append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	return removed, urls
}

// snapshotSessionURLsLocked reads a copy of the session URL list. Callers
// hold notesUpdateMu; the list itself is read under s.mu.
func (s *Session) snapshotSessionURLsLocked() []schema.SessionURL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.SessionURL(nil), s.sessionURLs...)
}

// restoreSessionURLsLocked replaces the session URL list. Callers hold
// notesUpdateMu; the write lands under s.mu.
func (s *Session) restoreSessionURLsLocked(urls []schema.SessionURL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionURLs = append([]schema.SessionURL(nil), urls...)
}

// setAgentNoteSerialized stores the agent note and captures the notes
// snapshot. It exists for direct (non-tool) callers; the agent tool below
// serializes further — through persistence and emission — via
// mutateAgentNoteSerialized.
func (s *Session) setAgentNoteSerialized(note string) (stored string, changed bool, human, agent string) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	stored, changed = s.setAgentNote(note)
	human, agent = s.notesSnapshot()
	return stored, changed, human, agent
}

// mutateAgentNoteSerialized stores the agent note, persists it, and emits
// the resulting snapshot as one serialized unit under notesUpdateMu, so
// concurrent mutations publish in store order (G1). The emission runs
// without Session.mu (emit re-acquires it). On a persistence failure the
// store rolls back and the caller reports the error with no emission.
func (s *Session) mutateAgentNoteSerialized(note string) (stored string, changed bool, human, agent string, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	_, prevAgent := s.notesSnapshot()
	stored, changed = s.setAgentNote(note)
	if err = s.persistNotesMetaLocked(); err != nil {
		s.mu.Lock()
		s.agentNote = prevAgent
		s.mu.Unlock()
		s.metaSaveMu.Unlock()
		return stored, changed, "", "", err
	}
	s.metaSaveMu.Unlock()
	if !changed {
		return s.notesSnapshotPair(stored, false)
	}
	human, agent = s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agent))
	return stored, true, human, agent, nil
}

// addSessionURLSerialized appends the URL entry and captures the resulting
// list. It exists for direct (non-tool) callers; the agent tool below
// serializes further — through persistence and emission — via
// mutateSessionURLAddSerialized.
func (s *Session) addSessionURLSerialized(rawURL, label string) (schema.SessionURL, []schema.SessionURL, error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	entry, err := s.addSessionURL(rawURL, label)
	if err != nil {
		return schema.SessionURL{}, nil, err
	}
	s.mu.Lock()
	urls := append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	return entry, urls, nil
}

// mutateSessionURLAddSerialized appends the URL entry, persists it, and emits
// the resulting list as one serialized unit under notesUpdateMu, so
// concurrent URL mutations publish in store order (G1). The emission runs
// without Session.mu (emit re-acquires it). On a persistence failure the
// store rolls back to the pre-add list and the caller reports the error
// with no emission.
func (s *Session) mutateSessionURLAddSerialized(rawURL, label string) (entry schema.SessionURL, urls []schema.SessionURL, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	prev := s.snapshotSessionURLsLocked()
	entry, err = s.addSessionURL(rawURL, label)
	if err != nil {
		s.metaSaveMu.Unlock()
		return schema.SessionURL{}, nil, err
	}
	if err = s.persistNotesMetaLocked(); err != nil {
		s.restoreSessionURLsLocked(prev)
		s.metaSaveMu.Unlock()
		return schema.SessionURL{}, nil, err
	}
	s.metaSaveMu.Unlock()
	urls = s.snapshotSessionURLsLocked()
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(urls))
	return entry, urls, nil
}

// mutateSessionURLRemoveSerialized deletes the entry with id, persists the
// removal, and emits the resulting list as one serialized unit under
// notesUpdateMu, so concurrent URL mutations publish in store order (G1).
// The emission runs without Session.mu (emit re-acquires it). On a
// persistence failure the store rolls back to the pre-removal list and the
// caller reports the error with no emission.
func (s *Session) mutateSessionURLRemoveSerialized(id string) (removed bool, urls []schema.SessionURL, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	prev := s.snapshotSessionURLsLocked()
	removed = s.removeSessionURL(id)
	if !removed {
		s.metaSaveMu.Unlock()
		return false, nil, nil
	}
	if err = s.persistNotesMetaLocked(); err != nil {
		s.restoreSessionURLsLocked(prev)
		s.metaSaveMu.Unlock()
		return false, nil, err
	}
	s.metaSaveMu.Unlock()
	urls = s.snapshotSessionURLsLocked()
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(urls))
	return true, urls, nil
}

// applyUrlsRemoveResult commits an empty success beside its outer reservation.
// The response carries no state; the record's presence is the replay marker.
func (s *Session) applyUrlsRemoveResult(lease *clientMutationLease, outerID string) (bool, error) {
	result, err := json.Marshal(appwire.UrlsRemoveResponse{})
	if err != nil {
		lease.Release()
		return true, NormalizeClientMutationError(outerID, err)
	}
	if err := s.clientMutations.update(lease, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
		return nil
	}); err != nil {
		return true, NormalizeClientMutationError(outerID, err)
	}
	return true, nil
}

// AddSessionURLForTest adds one URL list entry, exposing the unexported store
// for the daemon's server-side tests (which live outside package agent).
func (s *Session) AddSessionURLForTest(rawURL, label string) (schema.SessionURL, error) {
	return s.addSessionURL(rawURL, label)
}

// HumanNoteForTest reports the stored human note, exposing the unexported
// store for the daemon's server-side tests (which live outside package
// agent).
func (s *Session) HumanNoteForTest() string {
	human, _ := s.notesSnapshot()
	return human
}

// SessionURLsForTest reports a copy of the session URL list, exposing the
// unexported store for the daemon's server-side tests (which live outside
// package agent).
func (s *Session) SessionURLsForTest() []schema.SessionURL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.SessionURL(nil), s.sessionURLs...)
}

// notesSnapshot reads the human and agent notes under s.mu.
func (s *Session) notesSnapshot() (human, agentNote string) {
	human, agentNote, _ = s.notesSnapshotAll()
	return human, agentNote
}

// snapshotSessionURLs reads a copy of the session URL list under s.mu.
func (s *Session) snapshotSessionURLs() []schema.SessionURL {
	_, _, urls := s.notesSnapshotAll()
	return urls
}

// notesContextBlock renders the current notes plus URL list for agent context
// injection: beside the goal continuation-prompt rendering at turn start and
// on resume, refreshed from note events before the next round. Empty state
// renders the explicit cleared marker (see notesClearedBlock), except that a
// session whose store was NEVER non-empty this process renders nothing, so a
// fresh session's context is byte-identical to today.
func (s *Session) notesContextBlock() string {
	human, agentNote, urls := s.notesSnapshotAll()
	if human == "" && agentNote == "" && len(urls) == 0 {
		s.mu.Lock()
		everProjected := s.notesEverProjected
		s.mu.Unlock()
		if !everProjected {
			return ""
		}
		return notesClearedBlock
	}
	var b strings.Builder
	b.WriteString("<shared-notes>\n")
	if human != "" {
		b.WriteString("Human: " + human + "\n")
	}
	if agentNote != "" {
		b.WriteString("Agent: " + agentNote + "\n")
	}
	for _, u := range urls {
		b.WriteString(formatNotesLinkLine(u) + "\n")
	}
	b.WriteString("</shared-notes>")
	return b.String()
}

// notesClearedBlock is the explicit empty snapshot maybeAppendNotesContext
// appends when the store transitions to empty after having held content. A
// removal that empties the last populated field (final URL removed while both
// notes are empty, a note cleared while nothing else is set) must still reach
// the next model request as a turn: history is append-only and the model
// never re-reads the store, so without this the stale pre-removal snapshot
// would stand as the model's latest truth. Distinct from "" (never
// populated: project nothing) so the fill→remove-all→next-request sequence
// shows empty rather than stale rows.
const notesClearedBlock = "<shared-notes>\n(empty — all shared notes and links cleared)\n</shared-notes>"

// formatNotesLinkLine renders one session URL list entry for the model
// context block and the notes_read tool output. The entry id rides alongside
// the human-readable URL/label because the model never receives the State
// side-channel — only Output — yet urls_remove requires the id.
func formatNotesLinkLine(u schema.SessionURL) string {
	base := u.URL
	if u.Label != "" {
		base = fmt.Sprintf("%s (%s)", u.Label, u.URL)
	}
	if u.ID != "" {
		return fmt.Sprintf("Link: %s [id: %s]", base, u.ID)
	}
	return "Link: " + base
}

// notesSnapshotAll reads human note, agent note, and URL list together.
func (s *Session) notesSnapshotAll() (human, agent string, urls []schema.SessionURL) {
	s.mu.Lock()
	human, agent, urls = "", s.agentNote, append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	if s.clientMutations != nil {
		if canonical := s.clientMutations.snapshot().HumanNote; canonical != nil {
			human = *canonical
		}
	}
	return human, agent, urls
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
	s.mu.Unlock()
	s.appendTurn(turn, body)
}

// resetNotesProjectionAfterCompaction clears the last-projected notes record
// when a CHECKPOINT/SUMMARY turn replaces history: compaction folds away any
// previously appended NOTES_CONTEXT turns, so the model loses whatever state
// was last projected. Clearing forces the next maybeAppendNotesContext call
// to re-emit the full current block rather than staying silent on state the
// model can no longer see. Mirrors
// resetEnvContextTrackerAfterCompaction's contract for ENVIRONMENT turns.
func (s *Session) resetNotesProjectionAfterCompaction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notesLastProjected = ""
}

// seedNotesProjectionLocked records the last NOTES_CONTEXT turn of a restored
// history as the already-projected block, so a resumed session's first
// maybeAppendNotesContext call appends only when the current store differs
// from what the model last saw. Callers must hold no lock; the seeding takes
// s.mu itself (restore runs single-threaded before the session is visible).
func (s *Session) seedNotesProjectionLocked(history []schema.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range slices.Backward(history) {
		if history[i].Kind != schema.TurnNotesContext {
			continue
		}
		s.notesLastProjected = history[i].Message.Text()
		s.notesEverProjected = true
		return
	}
}
