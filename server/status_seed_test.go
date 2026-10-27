package server

// seedStatus sets a fresh Server's session status through the setters the
// daemon uses at runtime.
func seedStatus(s *Server, info StatusInfo) {
	s.UpdateSessionInfo(info.SessionID, info.Model, info.Profile)
	s.SetWorkingDir(info.WorkingDir)
	s.SetState(info.State)
	for range info.Turns {
		s.IncrementTurns()
	}
}
