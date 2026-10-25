package agent

// acceptTerminalCommunicate records that the model has explicitly ended the
// turn that ends the process: a communicate with end_turn=true was accepted
// while TurnEndsProcess. It is sticky for the life of the session.
func (s *Session) acceptTerminalCommunicate() {
	s.mu.Lock()
	s.terminalCommunicateAccepted = true
	s.mu.Unlock()
}

// hasAcceptedTerminalCommunicate affects empty-response retry policy only.
// It grants no permission to discard an unobserved notification.
func (s *Session) hasAcceptedTerminalCommunicate() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalCommunicateAccepted
}
