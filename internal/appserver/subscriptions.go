package appserver

import "sync"

type subscription struct {
	connID     string
	threadID   string
	buffering  bool
	generation uint64
	cut        uint64
	buffer     []SequencedNotification
}

type connectionSubscriptionSnapshot struct {
	subscriptions []subscription
}

type subscriptionCaptureRollback struct {
	replace        bool
	connection     connectionSubscriptionSnapshot
	threadPrevious *subscription
}

type Subscriptions struct {
	mu       sync.RWMutex
	byConn   map[string]map[string]*subscription
	byThread map[string]map[string]*subscription
}

func NewSubscriptions() *Subscriptions {
	return &Subscriptions{
		byConn:   map[string]map[string]*subscription{},
		byThread: map[string]map[string]*subscription{},
	}
}

func (s *Subscriptions) Subscribe(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
}

func (s *Subscriptions) ReplaceConnectionSubscriptions(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeConnectionLocked(connID)
	if threadID != "" {
		s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
	}
}

func (s *Subscriptions) BeginBuffered(connID, threadID string, replace bool, generation uint64) subscriptionCaptureRollback {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollback := subscriptionCaptureRollback{replace: replace}
	if replace {
		rollback.connection = s.connectionSnapshotLocked(connID)
		s.removeConnectionLocked(connID)
	} else {
		if previous := s.byConn[connID][threadID]; previous != nil {
			clone := *previous
			clone.buffer = append([]SequencedNotification(nil), previous.buffer...)
			rollback.threadPrevious = &clone
		}
		s.removeThreadLocked(connID, threadID)
	}
	s.subscribeLocked(&subscription{
		connID:     connID,
		threadID:   threadID,
		buffering:  true,
		generation: generation,
	})
	return rollback
}

func (s *Subscriptions) WithdrawBuffered(
	connID, threadID string,
	generation uint64,
	rollback subscriptionCaptureRollback,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.byConn[connID][threadID]
	if current == nil || !current.buffering || current.generation != generation {
		return false
	}
	if rollback.replace {
		s.removeConnectionLocked(connID)
		for i := range rollback.connection.subscriptions {
			sub := rollback.connection.subscriptions[i]
			s.subscribeLocked(&sub)
		}
		return true
	}
	s.removeThreadLocked(connID, threadID)
	if rollback.threadPrevious != nil {
		previous := *rollback.threadPrevious
		s.subscribeLocked(&previous)
	}
	return true
}

func (s *Subscriptions) SetCut(connID, threadID string, generation, cut uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.byConn[connID][threadID]
	if sub == nil || !sub.buffering || sub.generation != generation {
		return false
	}
	sub.cut = cut
	return true
}

// Route inserts a committed record into every buffering subscriber and returns
// the live connection owners that should receive it immediately.
func (s *Subscriptions) Route(record SequencedNotification) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var live []string
	for connID, sub := range s.byThread[record.ThreadID] {
		if sub.buffering {
			sub.buffer = append(sub.buffer, record)
			continue
		}
		live = append(live, connID)
	}
	return live
}

func (s *Subscriptions) Release(connID, threadID string, generation uint64) ([]SequencedNotification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.byConn[connID][threadID]
	if sub == nil || !sub.buffering || sub.generation != generation {
		return nil, false
	}
	release := make([]SequencedNotification, 0, len(sub.buffer))
	for _, record := range sub.buffer {
		if record.Seq > sub.cut {
			release = append(release, record)
		}
	}
	sub.buffering = false
	sub.buffer = nil
	return release, true
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
	s.removeConnectionLocked(connID)
}

func (s *Subscriptions) subscribeLocked(sub *subscription) {
	if s.byConn[sub.connID] == nil {
		s.byConn[sub.connID] = map[string]*subscription{}
	}
	if s.byThread[sub.threadID] == nil {
		s.byThread[sub.threadID] = map[string]*subscription{}
	}
	s.byConn[sub.connID][sub.threadID] = sub
	s.byThread[sub.threadID][sub.connID] = sub
}

func (s *Subscriptions) removeConnectionLocked(connID string) {
	for threadID := range s.byConn[connID] {
		s.removeThreadLocked(connID, threadID)
	}
	delete(s.byConn, connID)
}

func (s *Subscriptions) removeThreadLocked(connID, threadID string) {
	delete(s.byConn[connID], threadID)
	if len(s.byConn[connID]) == 0 {
		delete(s.byConn, connID)
	}
	delete(s.byThread[threadID], connID)
	if len(s.byThread[threadID]) == 0 {
		delete(s.byThread, threadID)
	}
}

func (s *Subscriptions) connectionSnapshotLocked(connID string) connectionSubscriptionSnapshot {
	snapshot := connectionSubscriptionSnapshot{
		subscriptions: make([]subscription, 0, len(s.byConn[connID])),
	}
	for _, current := range s.byConn[connID] {
		clone := *current
		clone.buffer = append([]SequencedNotification(nil), current.buffer...)
		snapshot.subscriptions = append(snapshot.subscriptions, clone)
	}
	return snapshot
}
