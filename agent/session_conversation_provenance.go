package agent

import (
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// conversationRequest is private to one actual provider attempt. Its inputs
// come from the final presented history, not from later admission or appends.
type conversationRequest struct {
	provenance schema.ConversationOutputProvenance
	inputs     []string
}

type conversationState struct {
	admitted           map[string]bool
	unknown            bool
	consumedFrontierID string
	generation         uint64
	preparedInputs     []string
	active             *schema.ConversationOutputProvenance
	frontierID         string
	inputs             []string
	frontiers          []schema.ConversationFrontier
	// retractedFrontierID names a frontier node in frontiers whose durable
	// carrier was retracted: a fold whose transcript commit was withheld
	// minted the node and keeps it in the chain — the in-memory fold's live
	// marker references it for the folded inputs' coverage — but the frontier
	// ID and covered inputs rolled back, so no later record chains onto it.
	// The next node a record actually carries takes its place in the chain and
	// inherits its live references (replaceRetractedFrontierLocked). Empty on
	// every durable rebuild: a resume replays the pre-compaction transcript,
	// where the node never existed.
	retractedFrontierID string
	revision            uint64
	snapshot            *appwire.ConversationProvenanceState
}

// restoreConversation replays canonical records, not their fold copies. The
// latest frontier is sufficient to continue the delta chain without rewriting
// every earlier input on the next request.
func (s *Session) restoreConversation(entries []transcript.Entry) {
	for _, entry := range entries {
		t := entry.Turn
		if t.OriginalOrdinal != nil {
			continue
		}
		s.noteConversationTurnLocked(t)
	}
	if s.conversation.frontierID != "" && !s.conversation.unknown {
		if _, ok := appwire.ResolveConversationFrontier(s.conversationSnapshot(clientMutationSnapshot{}), s.conversation.frontierID); !ok {
			s.conversation.unknown = true
			s.conversation.revision++
		}
	}
	s.stageConversationInputs(s.history)
}

// ConversationTurnAffectsProvenance identifies records that require Session
// adoption before their byte boundary can be paired with a provenance snapshot.
// Fold copies retain their original coverage and do not create a new frontier.
func ConversationTurnAffectsProvenance(turn schema.Turn) bool {
	return turn.OriginalOrdinal == nil && (len(turn.ConversationInputIDs) != 0 || turn.Kind == schema.TurnUserInput || turn.ConversationProvenance != nil || turn.ConversationCoverageRef != "")
}

func (s *Session) noteConversationTurnLocked(turn schema.Turn) {
	if !ConversationTurnAffectsProvenance(turn) {
		return
	}
	s.conversation.revision++
	if s.conversation.admitted == nil {
		s.conversation.admitted = make(map[string]bool)
	}
	for _, id := range turn.ConversationInputIDs {
		s.conversation.admitted[id] = true
	}
	if turn.Kind == schema.TurnUserInput && len(turn.ConversationInputIDs) == 0 {
		s.conversation.unknown = true
	}
	if turn.Kind == schema.TurnAssistant && turn.ConversationProvenance != nil {
		s.conversation.consumedFrontierID = turn.ConversationProvenance.FrontierID
	}
	p := turn.ConversationProvenance
	frontierID := turn.ConversationCoverageRef
	if p != nil && p.Version == 1 && p.InstanceID == s.id {
		s.conversation.generation = max(s.conversation.generation, p.RunGeneration)
		frontierID = p.FrontierID
		if turn.Kind == schema.TurnAssistant {
			provenance := *p
			s.conversation.active = &provenance
		}
	}
	if frontierID == "" || (turn.Kind != schema.TurnAssistant && turn.Kind != schema.TurnSummary && turn.Kind != schema.TurnCheckpoint) {
		return
	}
	if node := turn.ConversationFrontier; node != nil {
		if node.BaseID != s.conversation.frontierID || node.ID != frontierID {
			s.conversation.unknown = true
			return
		}
		for _, id := range node.RemovedInputs {
			if i, ok := slices.BinarySearch(s.conversation.inputs, id); ok {
				s.conversation.inputs = slices.Delete(s.conversation.inputs, i, i+1)
			}
		}
		s.conversation.inputs = append(s.conversation.inputs, node.AddedInputs...)
		slices.Sort(s.conversation.inputs)
		s.conversation.inputs = slices.Compact(s.conversation.inputs)
		s.conversation.frontiers = append(s.conversation.frontiers, *node)
		s.conversation.frontierID = node.ID
		s.replaceRetractedFrontierLocked(node.ID)
	} else if frontierID != s.conversation.frontierID {
		s.conversation.unknown = true
	}
}

// replaceRetractedFrontierLocked settles a retracted fold's live-only frontier
// node once a record carrying its own node lands: the incoming node extends
// the same predecessor and IS durably carried, so it takes the retracted
// node's place in the chain — which must stay strictly linear — and inherits
// the live turns that referenced the retracted one, whose claimed coverage
// resolves through the incoming node instead. Callers hold s.mu.
func (s *Session) replaceRetractedFrontierLocked(newID string) {
	retracted := s.conversation.retractedFrontierID
	if retracted == "" || retracted == newID {
		return
	}
	i := len(s.conversation.frontiers) - 2
	if i < 0 || s.conversation.frontiers[i].ID != retracted ||
		s.conversation.frontiers[i].BaseID != s.conversation.frontiers[len(s.conversation.frontiers)-1].BaseID {
		return
	}
	s.conversation.frontiers = slices.Delete(s.conversation.frontiers, i, i+1)
	s.conversation.retractedFrontierID = ""
	for j := range s.history {
		if s.history[j].ConversationCoverageRef == retracted {
			s.history[j].ConversationCoverageRef = newID
		}
	}
}

// ConversationProvenance samples only in-memory published state. A revision
// check prevents mixed admission cuts without nesting the session and journal
// locks or waiting for the filesystem serializer.
func (s *Session) ConversationProvenance() *appwire.ConversationProvenanceState {
	for {
		var journal clientMutationSnapshot
		if s.clientMutations != nil {
			journal = s.clientMutations.snapshot()
		}
		state := s.conversationSnapshot(journal)
		if s.clientMutations == nil {
			return state
		}
		s.clientMutations.stateMu.RLock()
		unchanged := journal.ConversationRevision == s.clientMutations.state.ConversationRevision
		s.clientMutations.stateMu.RUnlock()
		if unchanged {
			return state
		}
	}
}

func (s *Session) conversationSnapshot(journal clientMutationSnapshot) *appwire.ConversationProvenanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached := s.conversation.snapshot; cached != nil && cached.Revision == s.conversation.revision+journal.ConversationRevision {
		return cached
	}
	state := &appwire.ConversationProvenanceState{Version: 1, Authoritative: !s.conversation.unknown, InstanceID: s.id,
		Revision: s.conversation.revision + journal.ConversationRevision, ConsumedFrontierID: s.conversation.consumedFrontierID,
		AdmittedInputs: []string{}, RetiredInputs: []string{}, Frontiers: []appwire.ConversationFrontier{}}
	admitted := make(map[string]bool, len(s.conversation.admitted))
	retired := make(map[string]bool)
	for id := range s.conversation.admitted {
		admitted[id] = true
	}
	for _, entry := range journal.InputQueue {
		if len(journal.Journal[entry.ClientMutationID].ConversationInputIDs) == 0 {
			state.Authoritative = false
		}
	}
	for id, pending := range journal.PendingExecutions {
		if len(pending.Input) > 0 && len(journal.Journal[id].ConversationInputIDs) == 0 {
			state.Authoritative = false
		}
	}
	for _, record := range journal.Journal {
		if record.OperationState == clientMutationOperationRejected || record.OperationState == clientMutationOperationInFlight {
			continue
		}
		for _, id := range record.ConversationInputIDs {
			admitted[id] = true
			// Failed preparation and interruption can end an input before it
			// reaches canonical history. Only those terminal journal-only IDs
			// can no longer be consumed; recorded inputs still fence stale replies.
			unrecordedTerminal := record.OperationState == clientMutationOperationTerminal &&
				(record.ExecutionState == "interrupted" || record.ExecutionState == "failed") && !s.conversation.admitted[id]
			if record.ExecutionState == "canceled" || unrecordedTerminal {
				retired[id] = true
			}
		}
	}
	for id := range admitted {
		state.AdmittedInputs = append(state.AdmittedInputs, id)
	}
	for id := range retired {
		state.RetiredInputs = append(state.RetiredInputs, id)
	}
	slices.Sort(state.AdmittedInputs)
	slices.Sort(state.RetiredInputs)
	for _, node := range s.conversation.frontiers {
		state.Frontiers = append(state.Frontiers, appwire.ConversationFrontier{ID: node.ID, BaseID: node.BaseID, AddedInputs: slices.Clone(node.AddedInputs), RemovedInputs: slices.Clone(node.RemovedInputs)})
	}
	s.conversation.snapshot = state
	return state
}

// SetTranscriptPublishedFunc installs the completed-publication handoff and
// seeds it at one canonical boundary. The snapshot is immutable. fn may take
// only its owner's leaf lock, never call Session or enter a projection gate.
// The raw SetTranscriptRecordedFunc hook retains its append-lock contract.
func (s *Session) SetTranscriptPublishedFunc(fn func(int64, *appwire.ConversationProvenanceState)) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	s.mu.Lock()
	s.transcriptPublished = fn
	s.mu.Unlock()
	s.publishTranscriptLocked(s.TranscriptRecordedLength())
}

func (s *Session) publishTranscriptLocked(length int64) {
	s.mu.Lock()
	fn := s.transcriptPublished
	s.mu.Unlock()
	if fn != nil {
		fn(length, s.ConversationProvenance())
	}
}

// TranscriptPublicationHooks binds publication and frontier registration to one
// producer lifetime. Recorded retains Writer.OnRecorded's leaf-lock contract.
// Published runs after adoption with the immutable snapshot and byte boundary.
// RegisterFrontier runs at attach, outside Session.mu. A nil register means no
// registration and no release. PublishFrontier and ReleaseFrontier retain this
// runtime's identity, so neither can alter a replacement's registration.
type TranscriptPublicationHooks struct {
	Recorded         func(transcript.Record)
	Published        func(int64, *appwire.ConversationProvenanceState)
	RegisterFrontier func(func() events.ExecutionFrontier)
	PublishFrontier  func(string)
	ReleaseFrontier  func()
}

// SetDescendantTranscriptPublicationFunc installs a factory invoked once per
// new or restored descendant, before its writer attaches. Each invocation
// binds fresh hooks, so a retired producer cannot publish into its replacement.
func (s *Session) SetDescendantTranscriptPublicationFunc(fn func(string) TranscriptPublicationHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.spawn.descendantPublication = fn
}

func (s *Session) bindDescendantTranscriptPublication() {
	if fn := s.cfg.spawn.descendantPublication; fn != nil {
		hooks := fn(s.id)
		s.transcriptRecorded, s.transcriptPublished = hooks.Recorded, hooks.Published
		if hooks.RegisterFrontier != nil {
			s.transcriptFrontier = transcriptFrontierBinding{hooks.RegisterFrontier, hooks.PublishFrontier, hooks.ReleaseFrontier}
		}
	}
}

func (s *Session) stageConversationInputs(turns []schema.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasUnknown := s.conversation.unknown
	for _, turn := range turns {
		if (turn.Kind == schema.TurnSummary || turn.Kind == schema.TurnCheckpoint) && turn.ConversationCoverageRef == "" {
			s.conversation.unknown = true
		}
	}
	s.conversation.preparedInputs = s.conversationInputsLocked(turns)
	if s.conversation.unknown && !wasUnknown {
		s.conversation.revision++
	}
}

func (s *Session) conversationInputsLocked(turns []schema.Turn) []string {
	var ids []string
	for _, turn := range turns {
		ids = append(ids, turn.ConversationInputIDs...)
		if turn.ConversationCoverageRef != "" {
			ids = append(ids, s.resolveConversationFrontierLocked(turn.ConversationCoverageRef)...)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func (s *Session) resolveConversationFrontierLocked(id string) []string {
	var chain []schema.ConversationFrontier
	for i := len(s.conversation.frontiers) - 1; i >= 0 && id != ""; i-- {
		node := s.conversation.frontiers[i]
		if node.ID == id {
			chain = append(chain, node)
			id = node.BaseID
		}
	}
	if id != "" {
		s.conversation.unknown = true
		return nil
	}
	set := make(map[string]bool)
	for _, node := range slices.Backward(chain) {
		for _, input := range node.RemovedInputs {
			delete(set, input)
		}
		for _, input := range node.AddedInputs {
			set[input] = true
		}
	}
	ids := make([]string, 0, len(set))
	for input := range set {
		ids = append(ids, input)
	}
	slices.Sort(ids)
	return ids
}

func (s *Session) freezeConversationRequest() *conversationRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &conversationRequest{inputs: slices.Clone(s.conversation.preparedInputs), provenance: schema.ConversationOutputProvenance{
		Version: 1, InstanceID: s.id, RunGeneration: s.conversation.generation,
		RequestID: identifier.MustNewTurnID(),
	}}
}

func (s *Session) conversationDeltaLocked(id string, ids []string) *schema.ConversationFrontier {
	if s.conversation.frontierID == "" || !slices.Equal(ids, s.conversation.inputs) {
		node := &schema.ConversationFrontier{ID: id, BaseID: s.conversation.frontierID}
		for _, id := range ids {
			if _, found := slices.BinarySearch(s.conversation.inputs, id); !found {
				node.AddedInputs = append(node.AddedInputs, id)
			}
		}
		for _, id := range s.conversation.inputs {
			if _, found := slices.BinarySearch(ids, id); !found {
				node.RemovedInputs = append(node.RemovedInputs, id)
			}
		}
		return node
	}
	return nil
}

func (s *Session) conversationInputIDs(mutationID string) []string {
	if mutationID != "" {
		if s.clientMutations == nil {
			return nil
		}
		return s.clientMutations.conversationInputIDs(mutationID)
	}
	return []string{identifier.MustNewTurnID()}
}

func (s *Session) activeConversationProvenance() *schema.ConversationOutputProvenance {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conversation.active == nil {
		return nil
	}
	provenance := *s.conversation.active
	return &provenance
}
