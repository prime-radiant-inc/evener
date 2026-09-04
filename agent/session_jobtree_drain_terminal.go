package agent

// acceptTerminalCommunicate records that the model has explicitly ended the
// turn that ends the process: a communicate with end_turn=true was accepted
// while TurnEndsProcess. It is sticky for the life of the session.
func (s *Session) acceptTerminalCommunicate() {
	s.mu.Lock()
	s.terminalCommunicateAccepted = true
	s.mu.Unlock()
}

// hasAcceptedTerminalCommunicate reports whether the model has explicitly
// ended the turn that ends the process. It does not mean no further model
// turn can run: a completion the model was never shown — one that landed
// while its answer was being generated, or later during the drain — is still
// delivered on a notification turn, and that turn's reply becomes the run's
// answer (#865). What it does settle is residue (pending watch sends, stale
// delegate attention, a child's undeliverable leftovers), which the drain
// abandons once nothing live remains, and an empty reply to a post-terminal
// notification turn, which finishes idle instead of being retried (issue
// #329).
func (s *Session) hasAcceptedTerminalCommunicate() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalCommunicateAccepted
}

// subtreeHasLiveTerminalDrainWork reports whether anything in this session's
// managed-job subtree can still PRODUCE a deliverable after the model has
// ended the process-ending turn: an outstanding managed job of its own
// (running, or terminal with an undelivered owner notification the drain
// settles on its own), or an active / non-gated descendant. It is
// treeHasOutstandingWork minus the residue signals — pending watch sends,
// queued-but-stale notifications, pending delegate deliveries and attention —
// which, with no live work behind them, have no future model turn to be
// delivered to (issue #329). Deliverable-right-now items never reach this
// check: the drain loop's own turn gate (peekNotifications / root attention)
// runs first each pass.
//
// It walks the same liveDirectSubagents path as treeHasOutstandingWork,
// skipping stop-gated, fatally failed, and drain-abandoned children
// identically: they are never driven, so nothing they hold can convert.
func (s *Session) subtreeHasLiveTerminalDrainWork() (bool, error) {
	if s.jobManager != nil {
		n, err := s.jobManager.outstandingDrainJobCount()
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		active := sub.running || sub.finalizing || sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id)) {
			continue
		}
		if active {
			return true, nil
		}
		if child != nil {
			live, err := child.subtreeHasLiveTerminalDrainWork()
			if err != nil {
				return false, err
			}
			if live {
				return true, nil
			}
		}
	}
	return false, nil
}
