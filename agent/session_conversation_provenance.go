package agent

import (
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// conversationRequest is private to one actual provider attempt. Its inputs
// come from the final presented history, not from later admission or appends.
type conversationRequest struct {
	provenance schema.ConversationOutputProvenance
	frontier   *schema.ConversationFrontier
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
	revision           uint64
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
		}
	}
	s.stageConversationInputs(s.history)
}

func (s *Session) noteConversationTurnLocked(turn schema.Turn) {
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
	} else if frontierID != s.conversation.frontierID {
		s.conversation.unknown = true
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
			// An interrupted start may have stopped before its input was
			// recorded. Only that journal-only input can no longer be consumed,
			// while canonical inputs must keep fencing stale replies after stop.
			if record.ExecutionState == "canceled" || (record.ExecutionState == "interrupted" && !s.conversation.admitted[id]) {
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
	return state
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
	ids := slices.Clone(s.conversation.preparedInputs)
	request := &conversationRequest{inputs: ids, provenance: schema.ConversationOutputProvenance{
		Version: 1, InstanceID: s.id, RunGeneration: s.conversation.generation,
		RequestID: identifier.MustNewTurnID(), FrontierID: s.conversation.frontierID,
	}}
	request.frontier = s.conversationDeltaLocked(request.provenance.RequestID, ids)
	if request.frontier != nil {
		request.provenance.FrontierID = request.frontier.ID
	}
	return request
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
