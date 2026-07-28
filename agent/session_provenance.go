package agent

import "primeradiant.com/serf/agent/provenance"

func (s *Session) setActiveEntryKind(kind EntryKind) {
	s.mu.Lock()
	s.activeEntryKind = kind
	s.mu.Unlock()
}

func (s *Session) currentEntryKind() EntryKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeEntryKind
}

func (s *Session) markWatchCallbackDeliveredForCurrentTurn() {
	s.mu.Lock()
	s.watchCallbackDelivered = true
	s.mu.Unlock()
}

func (s *Session) watchCallbackDeliveredForCurrentTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watchCallbackDelivered
}

// activeCausalProvenance returns a clone of the provenance carried by the input
// currently being processed, or nil when the active set is empty. Emitted events
// are stamped with this value (see sendEvent).
func (s *Session) activeCausalProvenance() *provenance.Causal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return provenance.Clone(provenance.NilIfEmpty(&s.activeProvenance))
}

func (s *Session) completedCausalProvenance() *provenance.Causal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return provenance.Clone(provenance.NilIfEmpty(&s.completedInputProvenance))
}

// replaceActiveProvenance overwrites the active provenance set. Each new external
// top-level input calls this with nil to reset provenance so a fresh turn does not
// inherit a prior watch origin.
func (s *Session) replaceActiveProvenance(p *provenance.Causal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeProvenance = provenance.Causal{}
	s.completedInputProvenance = provenance.Causal{}
	if cloned := provenance.Clone(p); cloned != nil {
		s.activeProvenance = *cloned
	}
}

func (s *Session) finishActiveProvenance() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedInputProvenance = provenance.Causal{}
	if cloned := provenance.Clone(provenance.NilIfEmpty(&s.activeProvenance)); cloned != nil {
		s.completedInputProvenance = *cloned
	}
	s.activeProvenance = provenance.Causal{}
}

// unionActiveProvenance merges p into the active provenance set. Consuming a
// steering message folds its provenance in so events emitted by the resulting turn
// carry the watch origins that drove them.
func (s *Session) unionActiveProvenance(p *provenance.Causal) {
	if s == nil || p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := provenance.Union(provenance.NilIfEmpty(&s.activeProvenance), p)
	s.activeProvenance = provenance.Causal{}
	if merged != nil {
		s.activeProvenance = *merged
	}
}

// drainSteeringForTurn drains the steering queue and unions each drained
// message's provenance into the active set before returning the messages. It is
// the provenance-aware drain used at every turn boundary and mid-turn injection
// point.
func (s *Session) drainSteeringForTurn() []steeringMessage {
	msgs := s.drainSteering()
	for _, msg := range msgs {
		s.unionActiveProvenance(msg.Provenance)
	}
	return msgs
}

// drainSteeringForCommunicate returns the steering text delivered in a
// terminal communicate inbox. Client-authored steering crosses the same
// durable claim, transcript, and incorporation boundary as every other
// steering consumer before it becomes visible in that inbox. Daemon steering
// remains a plain queue drain so communicate can preserve its existing image
// deferral behavior.
func (s *Session) drainSteeringForCommunicate() []steeringMessage {
	pending := s.peekSteeringForTurn()
	drained := make([]steeringMessage, 0, len(pending))
	for range pending {
		msg, ok := s.popSteeringHead()
		if !ok {
			break
		}
		if msg.ClientMutationID != "" && !s.consumeSteeringMessage(msg) {
			continue
		}
		drained = append(drained, msg)
	}
	return drained
}

// peekSteeringForTurn snapshots the pending steering batch WITHOUT removing it
// from the queue, folding every message's provenance into the active set
// UPFRONT — before any message is consumed — exactly as drainSteeringForTurn
// does for the whole batch. injectDrainedSteering pairs it with a
// pop-one/persist/consume loop (popSteeringHead) so the queue shrinks as each
// message is durably recorded, narrowing the crash window to a single in-flight
// message instead of the whole batch. Unioning here, upfront, rather than
// per-message at consume time, is what keeps emit()'s active-provenance stamp
// identical for every message in the batch — the watch-loop-suppression timing
// the causal-provenance machinery depends on (see the injectDrainedSteering
// crash-window note).
func (s *Session) peekSteeringForTurn() []steeringMessage {
	s.mu.Lock()
	msgs := append([]steeringMessage{}, s.steeringQueue...)
	s.mu.Unlock()
	for _, msg := range msgs {
		s.unionActiveProvenance(msg.Provenance)
	}
	return msgs
}
