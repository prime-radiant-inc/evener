package agent

import "primeradiant.com/serf/agent/provenance"

// activeCausalProvenance returns a clone of the provenance carried by the input
// currently being processed, or nil when the active set is empty. Emitted events
// are stamped with this value (see sendEvent).
func (s *Session) activeCausalProvenance() *provenance.Causal {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return provenance.Clone(provenance.NilIfEmpty(&s.activeProvenance))
}

func (s *Session) completedCausalProvenance() *provenance.Causal {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return provenance.Clone(provenance.NilIfEmpty(&s.completedInputProvenance))
}

// replaceActiveProvenance overwrites the active provenance set. Each new external
// top-level input calls this with nil to reset provenance so a fresh turn does not
// inherit a prior watch origin.
func (s *Session) replaceActiveProvenance(p *provenance.Causal) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeProvenance = provenance.Causal{}
	s.completedInputProvenance = provenance.Causal{}
	if cloned := provenance.Clone(p); cloned != nil {
		s.activeProvenance = *cloned
	}
}

func (s *Session) finishActiveProvenance() {
	if s == nil {
		return
	}
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
