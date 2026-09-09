package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
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

// SetHumanNote stores the human's session whiteboard for the daemon's
// notes/human/set RPC. outerID is the hub RPC's clientMutationId; the inner
// steer derives its own id from it (outer + "/note-steer") so a hub retry of
// the outer RPC dedupes in the mutation store instead of double-interrupting.
//
// The outer mutation itself is journaled in the client-mutation store before
// it changes state: the reservation claims outerID (same input-hash replays
// the recorded stored value without mutating; same ID with different input
// is rejected as a mismatch without mutating), and the stored result commits
// beside it, so save A, save B, retry A leaves B standing while replaying
// A's recorded result. A no-op save (post-clamp text already stored) records
// its result the same way, with no event and no steer.
//
// A save whose post-clamp text equals the stored note is a no-op: it returns
// the current value with no event and no steer, including an empty save on an
// already-empty note. A non-empty→empty transition notifies with the
// cleared marker instead of empty inline text.
//
// The note update itself always commits, even when the steer injection is
// refused (an interrupt fence, a held gate). The persisted note is the source
// of truth the agent re-reads; the steer is delivery, and a delivery refusal
// must not roll back storage.
//
// Storage and delivery journal separately: once the store commits, the
// reservation records the stored value with delivery pending. A retry that
// takes the record over (a released lease, a higher AttemptGeneration)
// completes the pending steer for the COMMITTED value without repeating the
// write, so an intervening save cannot be clobbered. The metadata save gates
// the success journal: a persistence failure releases the reservation with
// no recorded result, so a retry (or restart recovery) still owns the write.
//
// Daemon handlers never emit pushes directly: the EventNotesUpdated emission
// here is the projector's only input for the evener/notes/updated push.
func (s *Session) SetHumanNote(outerID, note string) (string, error) {
	outerID = strings.TrimSpace(outerID)
	if outerID == "" {
		return "", appwire.InvalidParams("clientMutationId is required")
	}
	if err := s.ensureClientMutationStore(); err != nil {
		return "", err
	}
	request, err := newClientMutationRequest(clientMutationMethodNotesHumanSet, outerID, struct {
		Note string
	}{Note: note})
	if err != nil {
		return "", NormalizeClientMutationError(outerID, err)
	}
	// Reserve (do not execute) under the store serializer: the session work
	// below re-enters this same store for the inner steer, which would
	// self-deadlock inside executeAtomic's held serializer.
	lookup, err := s.clientMutations.reservePrepared(request, nil)
	if err != nil {
		// A reused ID with different input surfaces the raw mismatch: like
		// the turn verbs (clientMutationQueue et al.), which return the
		// store error unwrapped so errors.Is matches the sentinel.
		return "", err
	}
	switch {
	case lookup.Record.OperationState == clientMutationOperationRejected:
		return "", clientMutationRejectionError(lookup.Record)
	case lookup.Disposition == clientMutationDispositionReplayed:
		var recorded appwire.NotesHumanSetResponse
		if err := replayClientMutationResult(lookup.Record, &recorded); err != nil {
			return "", NormalizeClientMutationError(outerID, err)
		}
		return recorded.Note, nil
	case lookup.Disposition == clientMutationDispositionJoined:
		// A concurrent same-ID call owns the record; its release publishes
		// the outcome, so wait for it and re-enter (replay or takeover).
		<-lookup.OwnerDone
		return s.SetHumanNote(outerID, note)
	}
	if lookup.Lease == nil {
		return "", appwire.InternalError("notes mutation owner is missing")
	}
	return s.completeNotesHumanSet(lookup.Lease, outerID, note, lookup.Record.AttemptGeneration)
}

// completeNotesHumanSet applies one owned notes/human/set attempt. A takeover
// (AttemptGeneration above 1) completes the pending delivery the previous
// attempt journaled — stored value, emission, steer — without repeating the
// storage write, so an intervening save is never clobbered.
func (s *Session) completeNotesHumanSet(lease *clientMutationLease, outerID, note string, generation uint64) (string, error) {
	// A takeover resumes the previous attempt's committed storage: the record
	// it takes over already carries the stored value with delivery pending.
	if generation > 1 {
		if stored, ok := s.notesDeliveryPending(outerID); ok {
			return s.resumeNotesHumanSetDelivery(lease, outerID, stored)
		}
	}
	// The mutation, its persistence, and its announcement are one serialized
	// unit under notesUpdateMu: concurrent saves publish in store order and a
	// stale event never wins at the projector. Neither the metadata save nor
	// the emission may run outside the hold — an interleaving save between
	// them would publish in reverse order (G1).
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	prev, _ := s.notesSnapshot()
	stored, changed := s.setHumanNote(note)
	if err := s.persistNotesMeta(); err != nil {
		// The write never landed, so the store rolls back to the value the
		// persistence still holds: a retry must see the write as unfinished
		// (changed) and complete its emission + steer, instead of converging
		// on a silent success for a note nobody was told about (G2).
		s.setHumanNote(prev)
		lease.Release()
		return stored, err
	}
	if !changed {
		return s.applyNotesHumanSetResult(lease, outerID, stored)
	}
	// Storage is durable now; journal the stored value with delivery pending
	// BEFORE emitting or steering, so a crash or refusal between here and
	// the applied result still resumes delivery for this value.
	if err := s.markNotesDeliveryPending(outerID, stored); err != nil {
		lease.Release()
		return stored, NormalizeClientMutationError(outerID, err)
	}
	human, agentNote := s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	return s.deliverNotesHumanSetSteer(lease, outerID, stored)
}

// resumeNotesHumanSetDelivery completes a delivery the previous attempt
// journaled: re-emit the current snapshot and re-drive the derived steer for
// the RECORDED value, never re-applying the caller's (possibly older) input
// to the store.
func (s *Session) resumeNotesHumanSetDelivery(lease *clientMutationLease, outerID, stored string) (string, error) {
	// The re-emission serializes with every other notes mutation under the
	// same update lock as a fresh save, so a takeover cannot publish out of
	// store order either (G1).
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	human, agentNote := s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	return s.deliverNotesHumanSetSteer(lease, outerID, stored)
}

// deliverNotesHumanSetSteer drives the derived inner steer for an already
// stored (and journaled) note value, then commits the applied result. A
// refusal releases the reservation WITHOUT recording, keeping delivery
// pending so the next retry resumes it.
//
// Callers hold notesUpdateMu: the steer acceptance joins the same serialized
// unit as the mutation and its emission.
func (s *Session) deliverNotesHumanSetSteer(lease *clientMutationLease, outerID, stored string) (string, error) {
	text := "human updated their whiteboard: " + stored
	if stored == "" {
		text = "human updated their whiteboard: (whiteboard cleared)"
	}
	// The steer rides the durable client-mutation path (Reuse the steering
	// path; do not fork it): the mutation store dedupes a hub retry on the
	// derived inner id, so one outer id produces a single steer.
	innerID := strings.TrimSpace(outerID) + "/note-steer"
	steerID, err := s.acceptNotesSteer(s.lookupSteerID(outerID, innerID, lease), text)
	if err != nil {
		// Delivery refused: release the reservation without recording, so a
		// retry takes the record over and replays the inner steer instead
		// of converging on a failure the fence may have lifted.
		lease.Release()
		return stored, fmt.Errorf("notes/human/set: inject human-note steer: %w", err)
	}
	// The inner steer is accepted: persist which id carries it BEFORE the
	// outer success journals, so a crash (or outer-journal failure) between
	// here and the applied result still reuses this id instead of accepting
	// a second steer under a fresh id (G3). A failure here releases without
	// recording, and the retry rediscovers the accepted id by scan.
	if err := s.markNotesSteerAccepted(outerID, steerID); err != nil {
		lease.Release()
		return stored, NormalizeClientMutationError(outerID, err)
	}
	// The human-note injection carries its steering kind on the durable
	// inner-steer journal record (see setSteeringKindOnRecord) so durable
	// reconstruction restores it, and on the reflected in-memory entry for
	// the live path. The kind labels the rendered steering divider
	// (events.SteeringKindHumanNote) instead of a reader guessing it from
	// the text's prose.
	s.setSteeringKindOnRecord(steerID, events.SteeringKindHumanNote)
	s.annotateSteeringKind(steerID, events.SteeringKindHumanNote)
	return s.applyNotesHumanSetResult(lease, outerID, stored)
}

// lookupSteerID resolves the inner steer id for one owned outer attempt. An
// already-accepted id is always reused: the id the journal names
// (markNotesSteerAccepted), or — when a crash landed between the inner
// acceptance and that mark — the earlier candidate the scan finds applied.
// Re-accepting an applied id replays it without queueing a second steer, so
// recovery converges on single delivery (G3).
//
// Otherwise the id is the base id on the first attempt, suffixed with the
// outer AttemptGeneration on a takeover retry. A rejected steer record
// replays its rejection forever — recovery keys on AttemptGeneration, which
// only advances on a NEW id — so a retry after a lifted fence must steer
// under a fresh id to deliver. The generation suffix keeps that fresh while
// staying deterministic: one outer attempt produces exactly one steer id,
// and a hub replay of an APPLIED outer record never reaches this path at all.
func (s *Session) lookupSteerID(outerID, innerID string, lease *clientMutationLease) string {
	var generation uint64 = 1
	if lease != nil {
		generation = lease.attemptGeneration
	}
	if s.clientMutations != nil {
		snapshot := s.clientMutations.snapshot()
		if record, ok := snapshot.Journal[strings.TrimSpace(outerID)]; ok &&
			strings.TrimSpace(record.NotesInnerSteerID) != "" {
			return record.NotesInnerSteerID
		}
		for g := uint64(1); g < generation; g++ {
			candidate := innerID
			if g > 1 {
				candidate = innerID + "/attempt-" + strconv.FormatUint(g, 10)
			}
			if record, ok := snapshot.Journal[candidate]; ok &&
				record.OperationState == clientMutationOperationApplied {
				return candidate
			}
		}
	}
	if generation > 1 {
		return innerID + "/attempt-" + strconv.FormatUint(generation, 10)
	}
	return innerID
}

// acceptNotesSteer drives the derived inner steer under steerID.
func (s *Session) acceptNotesSteer(steerID, text string) (string, error) {
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: steerID,
		Input:            clientMutationInput(text, nil),
	}); err != nil {
		return "", err
	}
	return steerID, nil
}

// notesDeliveryPending reports whether the outer notes/human/set record
// carries a committed stored value with delivery still pending.
func (s *Session) notesDeliveryPending(outerID string) (string, bool) {
	if s.clientMutations == nil {
		return "", false
	}
	record, exists := s.clientMutations.snapshot().Journal[outerID]
	if !exists || !record.NotesDeliveryPending {
		return "", false
	}
	return record.NotesStoredValue, true
}

// markNotesDeliveryPending journals the committed stored value with delivery
// pending beside the owner's reservation. It runs inside the store
// serializer via mutate, so it cannot interleave with a concurrent reserve
// of the same record; the owner lease stays open for the final applied
// update (mutate, unlike update, settles no ownership).
func (s *Session) markNotesDeliveryPending(outerID, stored string) error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		record, ok := snapshot.Journal[outerID]
		if !ok || record.Method != clientMutationMethodNotesHumanSet ||
			record.OperationState != clientMutationOperationInFlight {
			return errClientMutationOwner
		}
		record.NotesDeliveryPending = true
		record.NotesStoredValue = stored
		snapshot.Journal[outerID] = record
		return nil
	})
}

// markNotesSteerAccepted records which inner steer id the outer notes/human/set
// attempt accepted, beside the owner's still-open reservation. It runs inside
// the store serializer via mutate, so it cannot interleave with a concurrent
// reserve of the same record; the owner lease stays open for the final applied
// update (mutate, unlike update, settles no ownership). A later attempt of the
// same outer id reuses this id via lookupSteerID instead of allocating a
// fresh one, so crash recovery between the inner acceptance and the outer
// success cannot accept a second steer (G3).
func (s *Session) markNotesSteerAccepted(outerID, steerID string) error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		record, ok := snapshot.Journal[outerID]
		if !ok || record.Method != clientMutationMethodNotesHumanSet ||
			record.OperationState != clientMutationOperationInFlight {
			return errClientMutationOwner
		}
		record.NotesInnerSteerID = steerID
		snapshot.Journal[outerID] = record
		return nil
	})
}

// storeHumanNoteSerialized stores the normalized note and captures its
// snapshot. It exists for the agent-tool surface, whose persistence runs in
// the tool handler: the update lock is held across the mutation AND the
// snapshot capture, so concurrent saves snapshot in store order. The human
// RPC path above serializes further — through persistence and emission —
// and does not use this helper.
func (s *Session) storeHumanNoteSerialized(note string) (stored string, changed bool, human, agentNote string) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	stored, changed = s.setHumanNote(note)
	s.mu.Lock()
	human, agentNote = s.humanNote, s.agentNote
	s.mu.Unlock()
	return stored, changed, human, agentNote
}

// mutateHumanNoteSerialized stores the normalized note, persists it, and
// emits the resulting snapshot as one serialized unit: unlike the
// store-then-save-then-emit split above, the persistence and the emission
// also run under notesUpdateMu, so concurrent mutations publish in store
// order and a stale event never wins at the projector (G1). The emission
// runs without Session.mu (emit re-acquires it), so the hold deadlocks
// loudly instead of wedging the daemon if that invariant ever breaks.
//
// On a persistence failure the store rolls back to prev, so the failed
// write never stands in memory without its announcement; the caller reports
// the error and emits nothing.
func (s *Session) mutateHumanNoteSerialized(note string) (stored string, changed bool, human, agentNote string, err error) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	prev, _ := s.notesSnapshot()
	stored, changed = s.setHumanNote(note)
	if err = s.persistNotesMeta(); err != nil {
		s.setHumanNote(prev)
		return stored, changed, "", "", err
	}
	if !changed {
		return s.notesSnapshotPair(stored, false)
	}
	human, agentNote = s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	return stored, true, human, agentNote, nil
}

// notesSnapshotPair re-reads the snapshot for a return tuple.
func (s *Session) notesSnapshotPair(stored string, changed bool) (string, bool, string, string, error) {
	human, agentNote := s.notesSnapshot()
	return stored, changed, human, agentNote, nil
}

// persistNotesMeta persists the notes store, propagating a failure so the
// caller refuses to journal success for a write that never landed. A
// test-injected fault surfaces as the mutation error.
func (s *Session) persistNotesMeta() error {
	if fault := s.cfg.testOnly.notesAutoSaveFault; fault != nil {
		if err := fault(); err != nil {
			return err
		}
	}
	return s.autoSaveMeta()
}

// annotateSteeringKind stamps kind onto the queued steering entry for
// clientMutationID, when that entry is still pending delivery. It runs after
// AcceptClientMutationSteer because that path only transports input text;
// the kind annotation is what the drain path persists onto the steering turn
// and emits on SteeringInjectedData, so the UI labels the divider from the
// wire instead of pattern-matching its prose. A missing entry (already
// drained, or never accepted) is a no-op: the store remains authoritative.
func (s *Session) annotateSteeringKind(clientMutationID, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.steeringQueue {
		if s.steeringQueue[i].ClientMutationID == clientMutationID {
			s.steeringQueue[i].Kind = kind
			return
		}
	}
}

// applyNotesHumanSetResult commits the stored note beside its outer
// reservation and reports it. The update releases the reservation's owner, so
// every path through SetHumanNote settles ownership exactly once.
func (s *Session) applyNotesHumanSetResult(lease *clientMutationLease, outerID, stored string) (string, error) {
	result, err := json.Marshal(appwire.NotesHumanSetResponse{Note: stored})
	if err != nil {
		lease.Release()
		return stored, NormalizeClientMutationError(outerID, err)
	}
	if err := s.clientMutations.update(lease, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
		return nil
	}); err != nil {
		return stored, NormalizeClientMutationError(outerID, err)
	}
	return stored, nil
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
	prev := s.snapshotSessionURLsLocked()
	removed := s.removeSessionURL(id)
	if !removed {
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
	// to own instead of standing removed-but-unannounced (G2).
	if err := s.persistNotesMeta(); err != nil {
		s.restoreSessionURLsLocked(prev)
		lookup.Lease.Release()
		return false, err
	}
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
	s.mu.Lock()
	human, agent = s.humanNote, s.agentNote
	s.mu.Unlock()
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
	prevHuman, prevAgent := s.notesSnapshot()
	stored, changed = s.setAgentNote(note)
	if err = s.persistNotesMeta(); err != nil {
		s.mu.Lock()
		s.humanNote, s.agentNote = prevHuman, prevAgent
		s.mu.Unlock()
		return stored, changed, "", "", err
	}
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
	prev := s.snapshotSessionURLsLocked()
	entry, err = s.addSessionURL(rawURL, label)
	if err != nil {
		return schema.SessionURL{}, nil, err
	}
	if err = s.persistNotesMeta(); err != nil {
		s.restoreSessionURLsLocked(prev)
		return schema.SessionURL{}, nil, err
	}
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
	prev := s.snapshotSessionURLsLocked()
	removed = s.removeSessionURL(id)
	if !removed {
		return false, nil, nil
	}
	if err = s.persistNotesMeta(); err != nil {
		s.restoreSessionURLsLocked(prev)
		return false, nil, err
	}
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
	defer s.mu.Unlock()
	return s.humanNote, s.agentNote, append([]schema.SessionURL(nil), s.sessionURLs...)
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
	s.mu.Unlock()
	s.appendTurn(schema.TurnNotesContext, llm.User(block))
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
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind != schema.TurnNotesContext {
			continue
		}
		s.notesLastProjected = history[i].Message.Text()
		s.notesEverProjected = true
		return
	}
}
