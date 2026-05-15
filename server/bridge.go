package server

import (
	"encoding/json"

	"primeradiant.com/serf/agent"
)

// sessionStartSSE enriches SessionStartData with session_id from the event envelope.
type sessionStartSSE struct {
	SessionID         string `json:"session_id"`
	Profile           string `json:"profile"`
	Model             string `json:"model"`
	Restored          bool   `json:"restored,omitempty"`
	Turns             int    `json:"turns,omitempty"`
	LastInputTokens   int    `json:"last_input_tokens,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitempty"`
}

// Bridge reads events from a session event channel, stores them in the
// server's ring buffer via the broadcaster, and updates server status.
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
		data, _ := json.Marshal(ev.Data)

		switch ev.Kind {
		case agent.EventSessionStart:
			if d, ok := ev.Data.(agent.SessionStartData); ok {
				srv.UpdateSessionInfo(ev.SessionID, d.Model, d.Profile)
				srv.SetState("IDLE")
				// Enrich SSE payload with session_id from event envelope.
				enriched := sessionStartSSE{
					SessionID:         ev.SessionID,
					Profile:           d.Profile,
					Model:             d.Model,
					Restored:          d.Restored,
					Turns:             d.Turns,
					LastInputTokens:   d.LastInputTokens,
					ContextWindowSize: d.ContextWindowSize,
				}
				data, _ = json.Marshal(enriched)
			}
		case agent.EventAssistantTextEnd:
			srv.IncrementTurns()
		case agent.EventSessionEnd:
			srv.SetProcessing(false)
			if d, ok := ev.Data.(agent.SessionEndData); ok && d.State != "" {
				srv.SetState(d.State)
			} else {
				srv.SetState("CLOSED")
			}
		}

		srv.Broadcast(string(ev.Kind), json.RawMessage(data))
	}
}
