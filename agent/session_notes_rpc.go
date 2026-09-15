package agent

import (
	"encoding/json"
	"fmt"
	"slices"
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
			text := humanNoteSteerPrefix + " " + stored
			if stored == "" {
				text = humanNoteSteerPrefix + " (whiteboard cleared)"
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
	// A replayed result was journaled by whichever binary served the original
	// call, and one that predates the write-path strip can carry controls; the
	// response is rendered by clients, so the value handed out is stripped while
	// the journal keeps its historical record. Stripping is deliberately not
	// normalizeNote: the journaled value is already normalized (the clamp can
	// leave a trailing space that a second collapse would drop), so re-normalizing
	// would hand back a different value than the original call returned.
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
			s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLsLocked()))
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
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(s.snapshotSessionURLsLocked()))
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
	s.emit(events.EventNotesUpdated, notesUpdatedData(human, agent))
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
	s.emit(events.EventUrlsUpdated, urlsUpdatedData(urls))
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

// notesSnapshot reads the committed human and agent notes.
func (s *Session) notesSnapshot() (human, agentNote string) {
	human, agentNote, _ = s.notesSnapshotAll()
	return human, agentNote
}

// notesContextBlock renders the current notes plus URL list for agent context
// injection: beside the goal continuation-prompt rendering at turn start and
// on resume, refreshed from note events before the next round. Empty state
// renders the explicit cleared marker (see notesClearedBlock), except that a
// session whose store was NEVER non-empty this process renders nothing, so a
// fresh session's context is byte-identical to today.
func (s *Session) notesContextBlock() string {
	return s.renderNotesContextBlock()
}

// notesContextBlockForModel renders the model-facing copy of the block: the
// framing tags stay literal and everything between them is escaped, so a note or
// label carrying the closing tag cannot terminate the block and make the model
// read attacker-chosen text as harness-authored context (persistent indirect
// prompt injection). Only this copy escapes — the raw block is what persists,
// displays, and feeds tool output, so real URLs and text survive.
func (s *Session) notesContextBlockForModel() string {
	return escapeNotesContextBlock(s.renderNotesContextBlock())
}

// notesAngleBrackets escapes the literal spelling of the two framing characters.
var notesAngleBrackets = strings.NewReplacer("<", "&lt;", ">", "&gt;")

// neutralizeNotesFraming removes every way content could forge the block's framing
// or terminate it from inside: the literal angle brackets, and the character
// references that decode back into them. Every other byte passes through untouched
// — html.EscapeString would also rewrite "&" and the quote characters, handing the
// model a URL like "...?a=1&amp;b=2" that neither notes_read nor the UI would ever
// show it.
//
// References are canonicalized rather than escaped one layer deeper. Escaping the
// leading "&" only ever buys the next decode layer, so a note could always spell a
// tag behind one layer more than the escaper had seen; canonicalizing gives the
// copy exactly one spelling per angle bracket, whatever the input spelling and
// however many "amp;" layers it carried, and makes the pass idempotent. A reader
// that decodes one layer still sees what it saw before for a literal "<", which is
// the property this escape has always had.
func neutralizeNotesFraming(content string) string {
	// References first: canonicalizing them leaves nothing that decodes to an angle
	// bracket, and this runs before the literal pass so its own "&lt;" output is
	// not escaped a second time.
	return notesAngleBrackets.Replace(canonicalizeNotesAngleBracketReferences(content))
}

// canonicalizeNotesAngleBracketReferences rewrites every complete character
// reference that resolves to an angle bracket into the canonical escaped spelling
// of that angle bracket ("&lt;" or "&gt;"). Text that is not such a reference is
// copied byte for byte, so an innocent "?a=1&ltd=2" in a URL reaches the model
// exactly as it was stored.
func canonicalizeNotesAngleBracketReferences(content string) string {
	if !strings.Contains(content, "&") {
		return content
	}
	var canonical strings.Builder
	canonical.Grow(len(content))
	for i := 0; i < len(content); {
		if content[i] == '&' {
			if bracket, size, ok := parseNotesAngleBracketReference(content[i:]); ok {
				if bracket == '<' {
					canonical.WriteString("&lt;")
				} else {
					canonical.WriteString("&gt;")
				}
				i += size
				continue
			}
		}
		canonical.WriteByte(content[i])
		i++
	}
	return canonical.String()
}

// parseNotesAngleBracketReference parses one complete character reference at the
// start of s, which must begin with "&", and reports the angle bracket it resolves
// to together with the number of bytes the reference occupied.
//
// A reference is "&", any number of ampersand layers, then a base followed by the
// terminating ";": the named references lt/gt (HTML named references are
// case-insensitive, so "&LT;" and "&Lt;" count too), or a numeric reference of
// decimal or "x"/"X" hex digits ("&#60;", "&#060;", "&#x3c;", "&#X3C;"). A layer
// may itself be spelled numerically, which is why notesAmpLayerWidth exists. The
// terminator is required, the digits have to parse and stay in range, and the
// resolved value has to be an angle bracket: anything else is ordinary text that
// happens to contain an "&", and rewriting it would hand the model text the user
// never wrote.
func parseNotesAngleBracketReference(s string) (bracket rune, size int, ok bool) {
	rest := s[1:]
	size = 1
	for {
		width, layer := notesAmpLayerWidth(rest)
		if !layer {
			break
		}
		rest = rest[width:]
		size += width
	}
	switch {
	case len(rest) >= 3 && strings.EqualFold(rest[:3], "lt;"):
		return '<', size + 3, true
	case len(rest) >= 3 && strings.EqualFold(rest[:3], "gt;"):
		return '>', size + 3, true
	case strings.HasPrefix(rest, "#"):
		if value, width, valid := parseNotesNumericReference(rest[1:], size+1); valid && (value == '<' || value == '>') {
			return value, width, true
		}
	}
	return 0, 0, false
}

// notesAmpLayerWidth reports the width of an ampersand-encoding layer at the
// start of s: the named reference "amp;" in any case, or a complete numeric
// reference resolving to "&" ("&#38;", "&#038;", "&#x26;", "&#X26;"). The
// numeric spelling counts because "&#38;lt;" decodes to "&lt;" and then to "<",
// so following only the named spelling left a whole spelling of a framing tag
// passing through untouched (roborev's finding on the round that introduced
// this parser).
func notesAmpLayerWidth(s string) (int, bool) {
	if len(s) >= 4 && strings.EqualFold(s[:4], "amp;") {
		return 4, true
	}
	if strings.HasPrefix(s, "#") {
		if value, width, ok := parseNotesNumericReference(s[1:], 1); ok && value == '&' {
			return width, true
		}
	}
	return 0, false
}

// parseNotesNumericReference parses the digits of a numeric character reference
// and its required terminator, afterHash pointing just past the "#" and size
// counting the bytes consumed before them. It resolves any in-range value and
// leaves which values are in scope to the caller.
func parseNotesNumericReference(afterHash string, size int) (value rune, consumed int, ok bool) {
	base := 10
	digits := afterHash
	if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
		base = 16
		digits = digits[1:]
		size++
	}
	end := 0
	for end < len(digits) && isNotesReferenceDigit(digits[end], base) {
		end++
	}
	if end == 0 || end >= len(digits) || digits[end] != ';' {
		return 0, 0, false
	}
	parsed, err := strconv.ParseUint(digits[:end], base, 32)
	if err != nil {
		return 0, 0, false
	}
	return rune(parsed), size + end + 1, true
}

// isNotesReferenceDigit reports whether c is a digit of base 10 or 16.
func isNotesReferenceDigit(c byte, base int) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case base == 16 && c >= 'a' && c <= 'f':
		return true
	case base == 16 && c >= 'A' && c <= 'F':
		return true
	}
	return false
}

// escapeNotesContextBlock returns the model-facing copy of a rendered
// shared-notes block. It is the single definition of the framing-escape contract:
// the renderer applies it to the live copy, and every path that puts a persisted
// (raw) block back into model context applies it to that turn — a restored
// transcript turn or a forked delegate's inherited prefix. The framing tags sit at
// fixed ends of the block and the content between them is neutralized (see
// neutralizeNotesFraming), so the block can be neither terminated nor re-opened
// from inside. Text that is not a rendered block is neutralized whole rather than
// passed through raw.
func escapeNotesContextBlock(block string) string {
	inner, ok := strings.CutPrefix(block, notesBlockOpen)
	if ok {
		if body, closed := strings.CutSuffix(inner, notesBlockClose); closed {
			return notesBlockOpen + neutralizeNotesFraming(body) + notesBlockClose
		}
	}
	return neutralizeNotesFraming(block)
}

func (s *Session) renderNotesContextBlock() string {
	human, agentNote, urls, everProjected := s.notesProjectionSnapshot()
	if human == "" && agentNote == "" && len(urls) == 0 {
		if !everProjected {
			return ""
		}
		return notesClearedBlock
	}
	var b strings.Builder
	b.WriteString(notesBlockOpen)
	if human != "" {
		b.WriteString("Human: " + human + "\n")
	}
	if agentNote != "" {
		b.WriteString("Agent: " + agentNote + "\n")
	}
	for _, u := range urls {
		b.WriteString(formatNotesLinkLine(u) + "\n")
	}
	b.WriteString(notesBlockClose)
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
const notesClearedBlock = notesBlockOpen + "(empty — all shared notes and links cleared)\n" + notesBlockClose

// The literal framing shared-notes blocks are rendered with. Kept as constants
// so the escaping helper can re-frame a raw block exactly as the renderer wrote
// it, without parsing attacker-influenced text.
const (
	notesBlockOpen  = "<shared-notes>\n"
	notesBlockClose = "</shared-notes>"
)

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
// RestoreSessionFromMeta: tests construct &Session{} directly and seed the live
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

// escapeNotesHistoryTurns returns history with every NOTES_CONTEXT turn replaced
// by its model-facing escaped copy. A history built from a persisted source — a
// resumed transcript, or a forked delegate's inherited prefix — carries the raw
// block, which is only safe for display there: the raw block is what the
// transcript must keep so renderers and tool output show the user's real text,
// but model context must receive the escaped copy (see escapeNotesContextBlock),
// or a note carrying the closing tag regains the harness framing on every
// request the session serves. The input is not modified.
// humanNoteSteerPrefix opens the steering text a shared-notes update carries.
// A journal record persisted before SteeringKind was recorded has no kind to
// read, so the prefix is how a note-origin entry is recognized there.
const humanNoteSteerPrefix = "human updated their whiteboard:"

// isHumanNoteSteer reports whether steering text came from a shared-notes update,
// by kind or, for entries older than the kind, by its text. Only note-origin
// steering is normalized: ordinary steering keeps the bytes the user typed, which
// is what the live path and the persisted transcript already show.
func isHumanNoteSteer(kind, text string) bool {
	if kind != "" {
		// A recorded kind decides. Only the human-note kind is note-origin, so a
		// user's steering keeps its bytes even when it quotes the words back.
		return kind == events.SteeringKindHumanNote
	}
	// A record persisted before kinds existed has none to read, so the exact shape
	// the write path emits is the only marker left: the prefix followed by the
	// space the note text always comes after. A user text imitating that shape
	// exactly is indistinguishable and stays a documented ambiguity.
	return strings.HasPrefix(text, humanNoteSteerPrefix+" ")
}

// rebuiltSteeringText returns a rebuilt steering entry's text: note-origin text is
// stripped like every other load path, and ordinary steering keeps its bytes.
func rebuiltSteeringText(kind, text string) string {
	if !isHumanNoteSteer(kind, text) {
		return text
	}
	return stripTextControls(text)
}

func escapeNotesHistoryTurns(history []schema.Turn) []schema.Turn {
	out := make([]schema.Turn, len(history))
	copy(out, history)
	for i := range out {
		text := out[i].Message.Text()
		if text == "" {
			continue
		}
		switch out[i].Kind {
		case schema.TurnNotesContext:
			// A turn persisted before the write-path strip can still carry
			// controls, and the framing escape only knows the framing spellings:
			// strip the other controls as well before the model copy is built.
			out[i].Message = llm.User(escapeNotesContextBlock(stripTextControls(text)))
		case schema.TurnSteering:
			if !isHumanNoteSteer(out[i].SteeringKind, text) {
				// Ordinary steering is delivered exactly as the user typed it, live
				// and restored alike; only notes-derived text is normalized, so this
				// copy keeps its bytes.
				continue
			}
			// A human-note update rides a steering turn, and this copy is what
			// resumed requests and inherited prefixes hand to the model, so the
			// text parts are stripped as well. Only the text parts change, and
			// the slice is copied before the first change: llm.User(text) would
			// replace the whole message and drop the image parts a steering turn
			// can legitimately carry (attachments, or queued image-bearing input
			// drained as steer), which is data loss on restore or fork. The
			// stored turn keeps its bytes.
			var rewritten []llm.ContentPart
			for j, part := range out[i].Message.Content {
				if part.Kind != llm.ContentText {
					continue
				}
				stripped := stripTextControls(part.Text)
				if stripped == part.Text {
					continue
				}
				if rewritten == nil {
					rewritten = append([]llm.ContentPart(nil), out[i].Message.Content...)
				}
				rewritten[j].Text = stripped
			}
			if rewritten != nil {
				out[i].Message.Content = rewritten
			}
		}
	}
	return out
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
