package agent

import "primeradiant.com/evener/agent/events"

// transcriptFrontierBinding belongs to one runtime, not its durable session ID.
// The closures are fixed before attach and retain the server's registration
// identity across reclamation, replacement and duplicate teardown.
type transcriptFrontierBinding struct {
	register func(func() events.ExecutionFrontier)
	publish  func(string)
	release  func()
}

// registerTranscriptFrontier runs outside Session.mu before attach flushes any
// held records. Reopens reinstall the writer hook without registering again.
func (s *Session) registerTranscriptFrontier() {
	if register := s.transcriptFrontier.register; register != nil {
		register(s.LatestExecutionFrontier)
	}
}

func (s *Session) releaseTranscriptFrontier() {
	if release := s.transcriptFrontier.release; release != nil {
		release()
	}
}
