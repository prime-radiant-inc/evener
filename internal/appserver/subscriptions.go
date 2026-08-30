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
	// withdrawn tracks threads a connection explicitly unsubscribed while a
	// buffering capture held the entry, so the capture's abort restores its
	// displaced snapshot minus exactly what the client dropped.
	withdrawn map[string]map[string]struct{}
}

func NewSubscriptions() *Subscriptions {
	return &Subscriptions{
		byConn:    map[string]map[string]*subscription{},
		byThread:  map[string]map[string]*subscription{},
		withdrawn: map[string]map[string]struct{}{},
	}
}

func (s *Subscriptions) Subscribe(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
}

// Unsubscribe removes the connection's subscription to one thread. It is
// idempotent: unsubscribing a thread this connection never held is a no-op,
// so a client racing its own re-subscribe can never wedge the registry.
//
// A thread currently held by a BUFFERING capture generation records the drop
// and leaves the entry in place: the capture displaced the connection's
// previous subscriptions into its rollback snapshot, and removing the
// buffering entry here would strand that snapshot (withdrawBuffered matches
// on the live generation and would bail without restoring). The generation's
// own commit/abort resolves the entry, either way honoring the drop: commit
// (Release) removes the entry, abort restores the snapshot minus this
// thread.
func (s *Subscriptions) Unsubscribe(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.byConn[connID][threadID]
	if !ok {
		return
	}
	if sub.buffering {
		if s.withdrawn[connID] == nil {
			s.withdrawn[connID] = map[string]struct{}{}
		}
		s.withdrawn[connID][threadID] = struct{}{}
		return
	}
	s.removeThreadLocked(connID, threadID)
}

// withdrawnLocked reports whether the connection explicitly unsubscribed the
// thread while a buffering capture held it. Caller holds s.mu.
func (s *Subscriptions) withdrawnLocked(connID, threadID string) bool {
	_, ok := s.withdrawn[connID][threadID]
	return ok
}

func (s *Subscriptions) ReplaceConnectionSubscriptions(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeConnectionLocked(connID)
	if threadID != "" {
		s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
	}
}

func (s *Subscriptions) beginBuffered(connID, threadID string, replace bool, generation uint64) subscriptionCaptureRollback {
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

func (s *Subscriptions) withdrawBuffered(
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
			if s.withdrawnLocked(connID, sub.threadID) {
				// The client explicitly unsubscribed this thread mid-capture;
				// restoring it would resurrect a thread it asked to stop
				// receiving. Everything else the capture displaced comes back
				// unchanged.
				s.clearWithdrawnLocked(connID, sub.threadID)
				continue
			}
			s.subscribeLocked(&sub)
		}
		return true
	}
	s.removeThreadLocked(connID, threadID)
	if rollback.threadPrevious != nil {
		previous := *rollback.threadPrevious
		if s.withdrawnLocked(connID, previous.threadID) {
			s.clearWithdrawnLocked(connID, previous.threadID)
		} else {
			s.subscribeLocked(&previous)
		}
	}
	s.clearWithdrawnLocked(connID, threadID)
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
	if s.withdrawnLocked(connID, threadID) {
		// The client unsubscribed this thread while the capture buffered it.
		// Its unsubscribe already succeeded on the wire, so committing the
		// entry live would resurrect a subscription the client dropped:
		// honor the drop instead — remove the entry, release none of the
		// buffered records, and consume the withdrawal record.
		s.removeThreadLocked(connID, threadID)
		s.clearWithdrawnLocked(connID, threadID)
		return nil, true
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

// clearWithdrawnLocked drops one spent mid-capture unsubscribe record.
// Caller holds s.mu.
func (s *Subscriptions) clearWithdrawnLocked(connID, threadID string) {
	if threads := s.withdrawn[connID]; threads != nil {
		delete(threads, threadID)
		if len(threads) == 0 {
			delete(s.withdrawn, connID)
		}
	}
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

// ConnectionCount reports how many connections hold a live interest in the
// thread. An entry whose connection unsubscribed mid-capture does not count:
// that client's unsubscribe already succeeded, and the entry only lingers as
// bookkeeping until the capture's commit/abort resolves it — counting it
// would let a subscription the client dropped hold the relay open.
func (s *Subscriptions) ConnectionCount(threadID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for connID := range s.byThread[threadID] {
		if s.withdrawnLocked(connID, threadID) {
			continue
		}
		count++
	}
	return count
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
