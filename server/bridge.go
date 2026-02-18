package server

import (
	"encoding/json"

	"primeradiant.com/serf/agent"
)

// Bridge reads events from a session event channel, stores them in the
// server's ring buffer via the broadcaster, and updates server status.
// It blocks until the events channel is closed.
func Bridge(srv *Server, events <-chan agent.SessionEvent) {
	for ev := range events {
		// Marshal event data to JSON for SSE transport.
		// Using json.RawMessage prevents double-encoding in the SSE handler.
		data, _ := json.Marshal(ev.Data)

		srv.Broadcast(string(ev.Kind), json.RawMessage(data))

		switch ev.Kind {
		case agent.EventSessionStart:
			if d, ok := ev.Data.(agent.SessionStartData); ok {
				srv.mu.Lock()
				srv.status.SessionID = ev.SessionID
				srv.status.Model = d.Model
				srv.status.Profile = d.Profile
				srv.status.State = "IDLE"
				srv.mu.Unlock()
			}
		case agent.EventUserInput:
			srv.SetProcessing(true)
			srv.mu.Lock()
			srv.status.State = "PROCESSING"
			srv.mu.Unlock()
		case agent.EventAssistantTextEnd:
			srv.mu.Lock()
			srv.status.Turns++
			srv.mu.Unlock()
		case agent.EventSessionEnd:
			srv.SetProcessing(false)
			srv.mu.Lock()
			srv.status.State = "CLOSED"
			srv.mu.Unlock()
		}
	}
}
