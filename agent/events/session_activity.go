package events

import "primeradiant.com/evener/appwire"

// EventSessionActivityChanged invalidates the affected logical owner's reads.
const EventSessionActivityChanged EventKind = "SESSION_ACTIVITY_CHANGED"

// SessionActivityChangedData names the logical receiver independently from
// SessionEvent.SessionID, which remains the physical emitting bridge owner.
type SessionActivityChangedData struct {
	ThreadID  string                            `json:"thread_id"`
	SessionID string                            `json:"session_id"`
	Ref       string                            `json:"ref"`
	Resources []appwire.SessionActivityResource `json:"resources"`
}

func (SessionActivityChangedData) eventKind() EventKind { return EventSessionActivityChanged }
