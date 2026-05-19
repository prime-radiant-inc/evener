package server

import "primeradiant.com/serf/agent"

// Bridge reads events from a session event channel, records appwire
// notifications, and updates server status.
// It blocks until the events channel is closed.
func Bridge(srv *Server, events <-chan agent.SessionEvent) {
	BridgeWithObserver(srv, events, nil)
}

// BridgeWithObserver behaves like Bridge and also invokes observer for every
// event before broadcasting it. This is used to tee raw NDJSON event logs.
func BridgeWithObserver(srv *Server, events <-chan agent.SessionEvent, observer func(agent.SessionEvent)) {
	for ev := range events {
		if observer != nil {
			observer(ev)
		}
		if !srv.acceptsSessionEvent(ev.SessionID) {
			continue
		}
		srv.RecordAppEvent(ev)

		switch ev.Kind {
		case agent.EventSessionStart:
			if d, ok := ev.Data.(agent.SessionStartData); ok {
				srv.UpdateSessionInfo(ev.SessionID, d.Model, d.Profile)
				srv.SetState(string(agent.SessionIdle))
			}
		case agent.EventAssistantTextEnd:
			srv.IncrementTurns()
		case agent.EventSessionEnd:
			if d, ok := ev.Data.(agent.SessionEndData); ok {
				if d.Interrupted {
					continue
				}
				srv.SetProcessing(false)
				if d.State != "" {
					srv.SetState(d.State)
				} else {
					srv.SetState(string(agent.SessionClosed))
				}
			} else {
				srv.SetProcessing(false)
				srv.SetState(string(agent.SessionClosed))
			}
		}
	}
}
