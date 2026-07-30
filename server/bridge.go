package server

import (
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// Bridge reads events from a session event channel, records appwire
// notifications, and updates server status.
// It blocks until the events channel is closed.
func Bridge(srv *Server, eventCh <-chan events.SessionEvent) {
	BridgeWithObserver(srv, eventCh, nil)
}

// BridgeWithObserver behaves like Bridge and also invokes observer for every
// event before broadcasting it. This is used to tee raw NDJSON event logs.
func BridgeWithObserver(srv *Server, eventCh <-chan events.SessionEvent, observer func(events.SessionEvent)) {
	for ev := range eventCh {
		if observer != nil {
			observer(ev)
		}
		// A cheap early-out for an event the daemon does not serve. It is not
		// the guard: applySessionEventStatus and RecordAppEvent each re-test
		// acceptance under the lock they mutate, because this one runs unlocked
		// and an identity can be replaced between it and them.
		if !srv.acceptsSessionEvent(ev.SessionID) {
			continue
		}
		srv.applySessionEventStatus(ev)
		srv.RecordAppEvent(ev)
	}
}

// applySessionEventStatus writes the status an event announces, before the
// projection commit that publishes the announcement.
//
// Two orderings ride on this one critical section.
//
// Status before notification. The two reach a client by different routes: the
// notification arrives on the subscription, the status it describes is read
// back from thread/read and /status. Projecting first leaves a window where a
// client that reduces thread/started and immediately re-reads sees the
// PREVIOUS session's model, profile and state — and across /clear that window
// spans a whole identity, because ReplaceAppIdentity has already moved
// status.SessionID to the replacement while the rest still describes what it
// replaced.
//
// Acceptance with application. The bridge tests acceptance unlocked, so an
// event admitted a moment before an identity replacement would otherwise write
// the REPLACED session's metadata on top of the replacement's — a straggling
// SESSION_START from the session /clear just retired renaming the thread that
// retired it, with nothing to correct it afterwards. Re-testing here, in the
// same hold as the writes, drops it instead.
//
// It takes only s.mu and notifies nothing: the single notification egress stays
// RecordAppEvent's projection commit, which acquires projectionMu first.
func (s *Server) applySessionEventStatus(ev events.SessionEvent) {
	effect := sessionEventStatusEffect(ev)
	if effect == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.acceptsSessionEventLocked(ev.SessionID) {
		return
	}
	effect(s)
}

// sessionEventStatusEffect returns the status write an event announces, or nil
// when it announces none.
//
// Choosing the effect before the lock keeps every event that has none -- which
// is nearly all of them, every token delta included -- off s.mu entirely. This
// loop is the only consumer draining the session event channel, and that
// channel drops on overflow, so anything that slows the drain shortens the
// distance to a silently lost event.
//
// This is deliberately one switch rather than a kind test in front of the
// existing one: a second list of the status-bearing kinds would drift from this
// one silently, and the drift would look like an event that simply stopped
// updating status.
func sessionEventStatusEffect(ev events.SessionEvent) func(*Server) {
	switch ev.Kind {
	case events.EventSessionStart:
		d, ok := ev.Data.(events.SessionStartData)
		if !ok {
			return nil
		}
		return func(s *Server) {
			s.updateSessionInfoLocked(ev.SessionID, d.Model, d.Profile)
			s.status.State = d.State
			if d.State == "" {
				s.status.State = string(agent.SessionIdle)
			}
		}
	case events.EventAssistantTextEnd:
		return func(s *Server) { s.status.Turns++ }
	case events.EventSessionEnd:
		d, ok := ev.Data.(events.SessionEndData)
		if ok && d.Interrupted {
			// An interrupted end closes nothing: the session stays live and the
			// cancelled turn is still unwinding. The event is still projected by
			// the caller — only its status effects are skipped.
			return nil
		}
		return func(s *Server) {
			s.setProcessingLocked(false)
			s.status.State = string(agent.SessionClosed)
			if ok && d.State != "" {
				s.status.State = d.State
			}
		}
	default:
		return nil
	}
}
