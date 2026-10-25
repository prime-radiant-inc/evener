package appserver

// Subscription seams the in-package tests drive directly. Production
// subscribes through CaptureSubscription and the thread/read admission path.

func (c *Connection) ReplaceSubscriptions(threadID string) {
	c.server.projectionMu.Lock()
	defer c.server.projectionMu.Unlock()
	c.server.deliveryMu.Lock()
	defer c.server.deliveryMu.Unlock()
	c.server.subs.ReplaceConnectionSubscriptions(c.id, threadID)
}

func (s *Subscriptions) ReplaceConnectionSubscriptions(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeConnectionLocked(connID)
	if threadID != "" {
		s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
	}
}

// IsSubscribed reports whether the connection holds a live interest in the
// thread; a withdrawn mid-capture entry does not count (see
// subscription.withdrawn).
func (s *Subscriptions) IsSubscribed(connID, threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.byConn[connID][threadID]
	return ok && !sub.withdrawn
}

func (s *Subscriptions) Connections(threadID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conns := make([]string, 0, len(s.byThread[threadID]))
	for connID, sub := range s.byThread[threadID] {
		if sub.withdrawn {
			continue
		}
		conns = append(conns, connID)
	}
	return conns
}
