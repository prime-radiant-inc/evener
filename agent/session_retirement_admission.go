package agent

// beginRetirementMutation must be called before any owner lock or first effect.
// Ordinary sessions have no process controller and preserve their existing path.
func (s *Session) beginRetirementMutation(category string) (func(), error) {
	if c := s.retirementController.Load(); c != nil {
		return c.BeginMutation(s.id, category)
	}
	return func() {}, nil
}

// retirementInputBlockers is the root-only predicate. The full runtime-tree
// proof is supplied by later retirement tasks; this does not enable retirement.
// The caller has closed admission, but holds no controller or session lock.
func (s *Session) retirementInputBlockers() []RetirementBlocker {
	var blockers []RetirementBlocker
	s.mu.Lock()
	if s.state == SessionProcessing || s.goalInTurn || !s.turnStartedAt.IsZero() {
		blockers = append(blockers, RetirementBlocker{Category: "turn", SessionID: s.id})
	}
	if len(s.inputQueue) != 0 || len(s.steeringQueue) != 0 || len(s.followups) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "input", SessionID: s.id})
	}
	s.mu.Unlock()
	s.clientMutationsInitMu.Lock()
	store := s.clientMutations
	s.clientMutationsInitMu.Unlock()
	if store != nil && store.retirementInputPending() {
		blockers = append(blockers, RetirementBlocker{Category: "input", SessionID: s.id})
	}
	return blockers
}

// Read only eligibility evidence, not historical payloads/results/input bytes.
// Like queueHeld, this reads the committed generation under stateMu, never the
// serializer that may be holding an in-progress filesystem write.
func (s *clientMutationStore) retirementInputPending() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	state := &s.state
	if state.ActiveTurnID != "" || len(state.InputQueue) != 0 || len(state.PendingExecutions) != 0 || len(state.BudgetReservations) != 0 || state.InterruptFence != nil || len(state.SteeringOrder) != 0 {
		return true
	}
	for _, record := range state.Journal {
		if record.OperationState == clientMutationOperationInFlight || record.ExecutionState == "accepted" || record.ExecutionState == "claimed" {
			return true
		}
	}
	return false
}
