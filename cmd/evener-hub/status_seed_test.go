package hub

import daemonserver "primeradiant.com/evener/server"

// seedDaemonStatus sets a fresh daemon Server's session status through the
// setters the daemon uses at runtime.
func seedDaemonStatus(s *daemonserver.Server, info daemonserver.StatusInfo) {
	s.UpdateSessionInfo(info.SessionID, info.Model, info.Profile)
	s.SetWorkingDir(info.WorkingDir)
	s.SetState(info.State)
}
