package appserver

import "sync"

type Subscriptions struct {
	mu       sync.RWMutex
	byConn   map[string]map[string]struct{}
	byThread map[string]map[string]struct{}
}

func NewSubscriptions() *Subscriptions {
	return &Subscriptions{
		byConn:   map[string]map[string]struct{}{},
		byThread: map[string]map[string]struct{}{},
	}
}

func (s *Subscriptions) Subscribe(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byConn[connID] == nil {
		s.byConn[connID] = map[string]struct{}{}
	}
	if s.byThread[threadID] == nil {
		s.byThread[threadID] = map[string]struct{}{}
	}
	s.byConn[connID][threadID] = struct{}{}
	s.byThread[threadID][connID] = struct{}{}
}

func (s *Subscriptions) IsSubscribed(connID, threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byConn[connID][threadID]
	return ok
}

func (s *Subscriptions) Threads(connID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	threads := make([]string, 0, len(s.byConn[connID]))
	for threadID := range s.byConn[connID] {
		threads = append(threads, threadID)
	}
	return threads
}

func (s *Subscriptions) Connections(threadID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conns := make([]string, 0, len(s.byThread[threadID]))
	for connID := range s.byThread[threadID] {
		conns = append(conns, connID)
	}
	return conns
}

func (s *Subscriptions) ConnectionCount(threadID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byThread[threadID])
}

func (s *Subscriptions) RemoveConnection(connID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for threadID := range s.byConn[connID] {
		delete(s.byThread[threadID], connID)
		if len(s.byThread[threadID]) == 0 {
			delete(s.byThread, threadID)
		}
	}
	delete(s.byConn, connID)
}
