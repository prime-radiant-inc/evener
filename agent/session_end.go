package agent

// The end-of-input announcement (SESSION_END) is emitted once per input. The
// latch below is the only place sessionEndEmitted is read or written; it is
// guarded by mu, and the helpers named Locked expect the caller to hold it.
//
// A path that ends an input claims the announcement under mu and emits after
// releasing it. A path that settles an input without announcing it (an empty
// notification wake, a discarded candidate) suppresses the latch instead. The
// next input re-arms it.

// claimSessionEndLocked reports whether the caller owns announcing the end and
// latches it. force claims even when it is already latched (a terminal close
// that must still tell the stream the session is over).
func (s *Session) claimSessionEndLocked(force bool) bool {
	owned := force || !s.sessionEndEmitted
	s.sessionEndEmitted = true
	return owned
}

// suppressSessionEndLocked latches the announcement without owning it, so the
// input being settled ends silently.
func (s *Session) suppressSessionEndLocked() {
	s.sessionEndEmitted = true
}

// suppressSessionEnd is suppressSessionEndLocked taking mu itself.
func (s *Session) suppressSessionEnd() {
	s.mu.Lock()
	s.suppressSessionEndLocked()
	s.mu.Unlock()
}

// rearmSessionEndLocked opens the latch for the input about to run.
func (s *Session) rearmSessionEndLocked() {
	s.sessionEndEmitted = false
}

// rearmSessionEnd is rearmSessionEndLocked taking mu itself.
func (s *Session) rearmSessionEnd() {
	s.mu.Lock()
	s.rearmSessionEndLocked()
	s.mu.Unlock()
}

// rearmSessionEndUnlessClosing re-arms for a continuation about to run, except
// on a closing session, whose close owns the final announcement.
func (s *Session) rearmSessionEndUnlessClosing() {
	s.mu.Lock()
	if !s.closingOrClosedLocked() {
		s.rearmSessionEndLocked()
	}
	s.mu.Unlock()
}
