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
		if !srv.acceptsSessionEvent(ev.SessionID) {
			continue
		}
		srv.RecordAppEvent(ev)

		switch ev.Kind {
		case events.EventSessionStart:
			if d, ok := ev.Data.(events.SessionStartData); ok {
				srv.UpdateSessionInfo(ev.SessionID, d.Model, d.Profile)
				srv.SetState(string(agent.SessionIdle))
			}
		case events.EventAssistantTextEnd:
			srv.IncrementTurns()
		case events.EventSessionEnd:
			if d, ok := ev.Data.(events.SessionEndData); ok {
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
