package agent

import (
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

// SetHumanNote stores the human's session whiteboard for the daemon's
// notes/human/set RPC. outerID is the hub RPC's clientMutationId; the inner
// steer derives its own id from it (outer + "/note-steer") so a hub retry of
// the outer RPC dedupes in the mutation store instead of double-interrupting.
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
	stored, changed := s.setHumanNote(note)
	if !changed {
		return stored, nil
	}
	human, agentNote := s.notesSnapshot()
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agentNote))
	s.maybeAutoSave()
	var text string
	if stored == "" {
		text = "human updated their whiteboard: (whiteboard cleared)"
	} else {
		text = "human updated their whiteboard: " + stored
	}
	// The steer rides the durable client-mutation path (Reuse the steering
	// path; do not fork it): the mutation store dedupes a hub retry on the
	// derived inner id, so one outer id produces a single steer.
	innerID := strings.TrimSpace(outerID) + "/note-steer"
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: innerID,
		Input:            clientMutationInput(text, nil),
	}); err != nil {
		return stored, fmt.Errorf("notes/human/set: inject human-note steer: %w", err)
	}
	// The human-note injection carries its steering kind on the queued entry
	// itself: AcceptClientMutationSteer only transports text, so the durable
	// steering entry is annotated after acceptance. The kind labels the
	// rendered steering divider (events.SteeringKindHumanNote) instead of a
	// reader guessing it from the text's prose.
	s.annotateSteeringKind(innerID, events.SteeringKindHumanNote)
	return stored, nil
}

// RemoveSessionURL removes one URL list entry by id for the daemon's hub-side
// urls/remove RPC, emitting EventUrlsUpdated so the projector derives the
// evener/urls/updated push from the same shape the agent tools emit.
func (s *Session) RemoveSessionURL(id string) (bool, error) {
	removed := s.removeSessionURL(id)
	if !removed {
		return false, nil
	}
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLs()))
	s.maybeAutoSave()
	return true, nil
}

// AddSessionURLForTest adds one URL list entry, exposing the unexported store
// for the daemon's server-side tests (which live outside package agent).
func (s *Session) AddSessionURLForTest(rawURL, label string) (schema.SessionURL, error) {
	return s.addSessionURL(rawURL, label)
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

// notesSnapshot reads the human and agent notes under s.mu.
func (s *Session) notesSnapshot() (human, agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.humanNote, s.agentNote
}

// snapshotSessionURLs reads a copy of the session URL list under s.mu.
func (s *Session) snapshotSessionURLs() []schema.SessionURL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.SessionURL(nil), s.sessionURLs...)
}

// notesContextBlock renders the current notes plus URL list for agent context
// injection: beside the goal continuation-prompt rendering at turn start and
// on resume, refreshed from note events before the next round. Empty state
// renders nothing, so a fresh session's context is byte-identical to today.
func (s *Session) notesContextBlock() string {
	human, agentNote, urls := s.notesSnapshotAll()
	var b strings.Builder
	b.WriteString("<shared-notes>\n")
	if human != "" {
		b.WriteString("Human: " + human + "\n")
	}
	if agentNote != "" {
		b.WriteString("Agent: " + agentNote + "\n")
	}
	for _, u := range urls {
		line := "Link: " + u.URL
		if u.Label != "" {
			line = fmt.Sprintf("Link: %s (%s)", u.Label, u.URL)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("</shared-notes>")
	if b.String() == "<shared-notes>\n</shared-notes>" {
		return ""
	}
	return b.String()
}

// notesSnapshotAll reads human note, agent note, and URL list together.
func (s *Session) notesSnapshotAll() (human, agent string, urls []schema.SessionURL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.humanNote, s.agentNote, append([]schema.SessionURL(nil), s.sessionURLs...)
}

// maybeAppendNotesContext records a NOTES_CONTEXT turn carrying the current
// shared-notes block when it is non-empty. It runs beside the goal
// continuation-prompt rendering at turn start and on resume, and before the
// next round when a note event landed, so human URL removals reach the agent
// even though the wire pushes travel hub-ward. The persisted note is the
// source of truth; this turn is its per-turn projection.
func (s *Session) maybeAppendNotesContext() {
	block := s.notesContextBlock()
	if block == "" {
		return
	}
	s.appendTurn(schema.TurnNotesContext, llm.User(block))
}
