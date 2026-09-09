package agent

import (
	"encoding/json"
	"fmt"
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
	stored, changed := s.setHumanNote(note)
	if !changed {
		return s.applyNotesHumanSetResult(lookup.Lease, outerID, stored)
	}
	human, agentNote := s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	s.maybeAutoSave()
	text := "human updated their whiteboard: " + stored
	if stored == "" {
		text = "human updated their whiteboard: (whiteboard cleared)"
	}
	// The steer rides the durable client-mutation path (Reuse the steering
	// path; do not fork it): the mutation store dedupes a hub retry on the
	// derived inner id, so one outer id produces a single steer.
	innerID := strings.TrimSpace(outerID) + "/note-steer"
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: innerID,
		Input:            clientMutationInput(text, nil),
	}); err != nil {
		// Delivery refused: release the reservation without recording, so a
		// retry takes the record over and replays the inner steer instead
		// of converging on a failure the fence may have lifted.
		lookup.Lease.Release()
		return stored, fmt.Errorf("notes/human/set: inject human-note steer: %w", err)
	}
	// The human-note injection carries its steering kind on the durable
	// inner-steer journal record (see setSteeringKindOnRecord) so durable
	// reconstruction restores it, and on the reflected in-memory entry for
	// the live path. The kind labels the rendered steering divider
	// (events.SteeringKindHumanNote) instead of a reader guessing it from
	// the text's prose.
	s.setSteeringKindOnRecord(innerID, events.SteeringKindHumanNote)
	s.annotateSteeringKind(innerID, events.SteeringKindHumanNote)
	return s.applyNotesHumanSetResult(lookup.Lease, outerID, stored)
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
	removed := s.removeSessionURL(id)
	if !removed {
		if lookup.Record.AttemptGeneration > 1 {
			// Crash-recovery takeover: the pre-crash attempt passed validation
			// (only unseen IDs reach the reservation) and removed the entry
			// before dying, so the entry's absence IS its success. An
			// AttemptGeneration of 1 is a fresh reservation, where absence
			// means a genuinely unknown id.
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
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLs()))
	s.maybeAutoSave()
	return s.applyUrlsRemoveResult(lookup.Lease, outerID)
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
