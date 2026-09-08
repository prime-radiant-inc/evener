package hub

import (
	"encoding/json"
	"sync"

	"primeradiant.com/evener/appwire"
)

// hubThreadStartup holds the source's initial announcement until the creating
// connection has joined its relay. A relay retains this record so a delayed
// announcement cannot duplicate the creation snapshot sent to that connection.
type hubThreadStartup struct {
	mu       sync.Mutex
	thread   appwire.Thread
	original *appwire.Notification
	finished bool
	aborted  bool
}

func (s *hubThreadStartup) hold(notification appwire.Notification) bool {
	if s == nil || notification.Method != appwire.NotifyThreadStarted {
		return false
	}
	var params appwire.ThreadStartedParams
	if json.Unmarshal(notification.Params, &params) != nil ||
		params.Ref != threadRef(s.thread) || params.ThreadID != s.thread.ID ||
		params.Thread.Evener.InstanceID != s.thread.Evener.InstanceID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aborted {
		return false
	}
	if !s.finished && s.original == nil {
		retained := notification
		retained.Params = append(json.RawMessage(nil), notification.Params...)
		s.original = &retained
	}
	return true
}

// finish chooses the announcement once; callers publish it outside all relay
// locks. An original frame uses ordinary relay delivery, while a missing frame
// needs only the creating connection's authoritative response snapshot.
func (s *hubThreadStartup) finish(admitted bool) (*appwire.Notification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil, false
	}
	s.finished = true
	s.aborted = !admitted
	if !admitted {
		s.original = nil
		return nil, false
	}
	if s.original != nil {
		original := s.original
		s.original = nil
		return original, true
	}
	return appwire.NotificationMessage(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: s.thread.ID,
		Ref:      threadRef(s.thread),
		Thread:   s.thread,
	}).Notification, false
}
