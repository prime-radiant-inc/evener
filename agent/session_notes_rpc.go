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
	stored, changed, human, agentNote := s.storeHumanNoteSerialized(note)
	if err := s.persistNotesMeta(); err != nil {
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
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	return s.deliverNotesHumanSetSteer(lease, outerID, stored)
}

// resumeNotesHumanSetDelivery completes a delivery the previous attempt
// journaled: re-emit the current snapshot and re-drive the derived steer for
// the RECORDED value, never re-applying the caller's (possibly older) input
// to the store.
func (s *Session) resumeNotesHumanSetDelivery(lease *clientMutationLease, outerID, stored string) (string, error) {
	human, agentNote := s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	return s.deliverNotesHumanSetSteer(lease, outerID, stored)
}

// deliverNotesHumanSetSteer drives the derived inner steer for an already
// stored (and journaled) note value, then commits the applied result. A
// refusal releases the reservation WITHOUT recording, keeping delivery
// pending so the next retry resumes it.
func (s *Session) deliverNotesHumanSetSteer(lease *clientMutationLease, outerID, stored string) (string, error) {
	text := "human updated their whiteboard: " + stored
	if stored == "" {
		text = "human updated their whiteboard: (whiteboard cleared)"
	}
	// The steer rides the durable client-mutation path (Reuse the steering
	// path; do not fork it): the mutation store dedupes a hub retry on the
	// derived inner id, so one outer id produces a single steer.
	innerID := strings.TrimSpace(outerID) + "/note-steer"
	steerID, err := s.acceptNotesSteer(lookupSteerID(outerID, innerID, lease), text)
	if err != nil {
		// Delivery refused: release the reservation without recording, so a
		// retry takes the record over and replays the inner steer instead
		// of converging on a failure the fence may have lifted.
		lease.Release()
		return stored, fmt.Errorf("notes/human/set: inject human-note steer: %w", err)
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

// lookupSteerID derives the inner steer id for one owned outer attempt: the
// base id on the first attempt, suffixed with the outer AttemptGeneration on
// a takeover retry. A rejected steer record replays its rejection forever —
// recovery keys on AttemptGeneration, which only advances on a NEW id — so
// a retry after a lifted fence must steer under a fresh id to deliver. The
// generation suffix keeps that fresh while staying deterministic: one outer
// attempt produces exactly one steer id, and a hub replay of an APPLIED
// outer record never reaches this path at all.
func lookupSteerID(outerID, innerID string, lease *clientMutationLease) string {
	if lease != nil && lease.attemptGeneration > 1 {
		return innerID + "/attempt-" + strconv.FormatUint(lease.attemptGeneration, 10)
	}
	_ = outerID
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

// storeHumanNoteSerialized stores the normalized note and captures its
// snapshot serialized with the mutation, mirroring the goal-update pattern
// (goalUpdateMu + mu, emit after release). Callers hold no lock; the update
// lock is held across the mutation AND the snapshot capture, so concurrent
// saves publish in store order and a stale event never wins at the
// projector. Emission itself runs after release.
func (s *Session) storeHumanNoteSerialized(note string) (stored string, changed bool, human, agentNote string) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	stored, changed = s.setHumanNote(note)
	s.mu.Lock()
	human, agentNote = s.humanNote, s.agentNote
	s.mu.Unlock()
	return stored, changed, human, agentNote
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
	removed, urls := s.removeSessionURLSerialized(id)
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
	// and a retry still owns the removal.
	if err := s.persistNotesMeta(); err != nil {
		lookup.Lease.Release()
		return false, err
	}
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(urls))
	return s.applyUrlsRemoveResult(lookup.Lease, outerID)
}

// removeSessionURLSerialized deletes the entry with id and captures the
// resulting list serialized with the mutation, mirroring the goal-update
// pattern (notesUpdateMu + mu, emit after release). Callers hold no lock;
// the update lock is held across the mutation AND the snapshot capture, so
// concurrent URL mutations publish in store order. Emission itself runs
// after release.
func (s *Session) removeSessionURLSerialized(id string) (bool, []schema.SessionURL) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	removed := s.removeSessionURL(id)
	s.mu.Lock()
	urls := append([]schema.SessionURL(nil), s.sessionURLs...)
	s.mu.Unlock()
	return removed, urls
}

// setAgentNoteSerialized stores the agent note and captures the notes
// snapshot serialized with the mutation, mirroring the goal-update pattern
// (notesUpdateMu + mu, emit after release). Callers hold no lock; emission
// itself runs after release.
func (s *Session) setAgentNoteSerialized(note string) (stored string, changed bool, human, agent string) {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	stored, changed = s.setAgentNote(note)
	s.mu.Lock()
	human, agent = s.humanNote, s.agentNote
	s.mu.Unlock()
	return stored, changed, human, agent
}

// addSessionURLSerialized appends the URL entry and captures the resulting
// list serialized with the mutation, mirroring the goal-update pattern
// (notesUpdateMu + mu, emit after release). Callers hold no lock; emission
// itself runs after release.
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
