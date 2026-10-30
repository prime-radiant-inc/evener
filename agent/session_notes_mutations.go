package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
		stored := normalizeWhiteboard(note)
		changed = stored != current
		if changed && snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		projection := appwire.MutationProjectionRemoved
		if changed {
			reserveClientMutationTurnID(snapshot, record)
			record.SteeringKind = events.SteeringKindHumanNote
			// The note's lines indent under the prefix as the notes block
			// indents them; clients that show the note strip that indent.
			var text string
			if stored == "" {
				text = humanNoteSteerPrefix + " (whiteboard cleared)"
			} else {
				text = formatNotesField(humanNoteSteerPrefix, stored)
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
		// The commit landed (a post-rename error still reports it through the
		// store), so the canonical note is now readable. Publish before the
		// emission so the emitted snapshot and every reader agree on it.
		s.publishCommittedHumanNoteLocked()
		s.reflectDurableClientSteering()
		s.wakeForPendingSteering()
		if changed {
			human, agentNote := s.notesSnapshot()
			s.emit(notesUpdatedData(human, agentNote))
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
	// A replayed result was journaled by whichever binary served the original
	// call, and one that predates the write-path strip can carry controls; the
	// response is rendered by clients, so the value handed out is stripped while
	// the journal keeps its historical record. Stripping is deliberately not
	// normalizeWhiteboard: the journaled value was already normalized by the
	// binary that served the call, and an older binary's rule differs (its
	// clamp could leave a trailing space, and it joined lines), so
	// re-normalizing could hand back a different value than the original call
	// returned.
	response.Note = stripTextControls(response.Note)
	disposition := appwire.MutationDispositionApplied
	if lookup.Disposition == clientMutationDispositionReplayed {
		disposition = appwire.MutationDispositionReplayed
	}
	response.Receipt = mutationReceipt(s.ID(), lookup.Record, disposition, lookup.Record.ProjectionState)
	return response, nil
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
	// The id is caller-supplied and this message is printed by terminals (the
	// TUI renders RPC errors), so quote it: %q escapes any control sequence
	// instead of handing the terminal something to execute.
	unknown := appwire.InvalidParams(fmt.Sprintf("no URL entry with id %q", id))
	s.notesUpdateMu.Lock()
	urls := s.snapshotSessionURLsLocked()
	lookup, err := s.clientMutations.reservePrepared(request, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
		if !slices.ContainsFunc(urls, func(entry schema.SessionURL) bool { return entry.ID == id }) {
			rejectClientMutation(record, unknown)
		}
		return nil
	})
	s.notesUpdateMu.Unlock()
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
	removed := s.stageSessionURLRemove(id)
	if !removed {
		s.metaSaveMu.Unlock()
		if lookup.Record.AttemptGeneration > 1 {
			// The prepare callback validated the target before the first
			// in-flight reservation. Its absence on takeover therefore
			// satisfies that accepted removal, including a metadata save
			// that completed before the receipt could be persisted.
			if err := s.persistNotesMeta(); err != nil {
				lookup.Lease.Release()
				return false, err
			}
			// The removal's metadata write landed on the earlier attempt, so
			// publish the store as committed before announcing the removal.
			s.publishCommittedNotesLocked()
			// The removal's announcement belongs to the removal, not to the
			// crash: re-emit the current list so the projector converges on
			// the post-removal state the success journal records (G2).
			s.emit(urlsUpdatedData(s.snapshotSessionURLsLocked()))
			return s.applyUrlsRemoveResult(lookup.Lease, outerID)
		}
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
	// Publish only after the save returned nil: a reader that already saw the
	// pre-removal list must never see it retracted by a failed write.
	s.publishCommittedNotesLocked()
	s.emit(urlsUpdatedData(s.snapshotSessionURLsLocked()))
	return s.applyUrlsRemoveResult(lookup.Lease, outerID)
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

// mutateAgentNoteSerialized stores the agent note, persists it, and emits
// the resulting snapshot as one serialized unit under notesUpdateMu, so
// concurrent mutations publish in store order (G1). The emission runs
// without Session.mu (emit re-acquires it). On a persistence failure the
// store rolls back and the caller reports the error with no emission. The
// committed notes cut is published after the save returns nil, so readers
// keep seeing the previous value while the save is in flight and after it
// fails.
func (s *Session) mutateAgentNoteSerialized(note string) (stored string, changed bool, human, agent string, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	_, prevAgent := s.notesSnapshot()
	stored, changed = s.stageAgentNote(note)
	if err = s.persistNotesMetaLocked(); err != nil {
		s.mu.Lock()
		s.agentNote = prevAgent
		s.mu.Unlock()
		s.metaSaveMu.Unlock()
		return stored, changed, "", "", err
	}
	s.metaSaveMu.Unlock()
	// Publish only after the save returned nil: readers must never observe a
	// staged value whose save later fails, and the failure branch above never
	// publishes at all.
	s.publishCommittedNotesLocked()
	if !changed {
		human, agentNote := s.notesSnapshot()
		return stored, false, human, agentNote, nil
	}
	human, agent = s.notesSnapshot()
	s.emit(notesUpdatedData(human, agent))
	return stored, true, human, agent, nil
}

// mutateSessionURLAddSerialized appends the URL entry, persists it, and emits
// the resulting list as one serialized unit under notesUpdateMu, so
// concurrent URL mutations publish in store order (G1). The emission runs
// without Session.mu (emit re-acquires it). On a persistence failure the
// store rolls back to the pre-add list and the caller reports the error
// with no emission, and the published committed cut is left untouched.
func (s *Session) mutateSessionURLAddSerialized(rawURL, label string) (entry schema.SessionURL, urls []schema.SessionURL, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	prev := s.snapshotSessionURLsLocked()
	entry, err = s.stageSessionURLAdd(rawURL, label)
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
	// Publish only after the save returned nil; the failure branch above
	// restores the live list without touching the published cut.
	s.publishCommittedNotesLocked()
	urls = s.snapshotSessionURLsLocked()
	s.emit(urlsUpdatedData(urls))
	return entry, urls, nil
}

// mutateSessionURLRemoveSerialized deletes the entry with id, persists the
// removal, and emits the resulting list as one serialized unit under
// notesUpdateMu, so concurrent URL mutations publish in store order (G1).
// The emission runs without Session.mu (emit re-acquires it). On a
// persistence failure the store rolls back to the pre-removal list and the
// caller reports the error with no emission, and the published committed cut
// is left untouched.
func (s *Session) mutateSessionURLRemoveSerialized(id string) (removed bool, urls []schema.SessionURL, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.metaSaveMu.Lock()
	prev := s.snapshotSessionURLsLocked()
	removed = s.stageSessionURLRemove(id)
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
	// Publish only after the save returned nil; a failed removal restores the
	// live list and leaves the published cut on the previous committed value.
	s.publishCommittedNotesLocked()
	urls = s.snapshotSessionURLsLocked()
	s.emit(urlsUpdatedData(urls))
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

// The literal framing shared-notes blocks are rendered with. Kept as constants
// so the escaping helper can re-frame a raw block exactly as the renderer wrote
// it, without parsing attacker-influenced text.
const (
	notesBlockOpen  = "<shared-notes>\n"
	notesBlockClose = "</shared-notes>"
)
