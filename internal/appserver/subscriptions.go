package appserver

import "sync"

type subscription struct {
	connID   string
	threadID string
	// lifecycleKey groups raw delivery aliases for connection-local unsubscribe.
	lifecycleKey string
	// rollback belongs to this buffering generation from beginBuffered onward,
	// including the snapshot interval before a response finalizer exists. Its
	// displaced entries share withdrawal marks with the finalizer's value copy.
	// Release or replacement drops this metadata; no tombstones outlive it.
	rollback   *subscriptionCaptureRollback
	buffering  bool
	generation uint64
	cut        uint64
	buffer     []SequencedNotification
	// withdrawn marks a buffering entry whose connection unsubscribed the
	// thread mid-capture. The client's unsubscribe already succeeded on the
	// wire, so the entry is capture bookkeeping, not a live interest:
	// queries skip it, no records buffer into it, and the capture's
	// commit/abort resolves it by dropping the thread rather than
	// resurrecting a subscription the client no longer holds. Living on the
	// entry, the mark shares its lifetime: removal and replacement dispose
	// of it for free.
	withdrawn bool
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

// Unsubscribe removes the connection's subscription to one thread. It is
// idempotent: unsubscribing a thread this connection never held is a no-op,
// so a client racing its own re-subscribe can never wedge the registry.
//
// A thread currently held by a BUFFERING capture generation marks the entry
// withdrawn and leaves it in place: the capture displaced the connection's
// previous subscriptions into its rollback snapshot, and removing the
// buffering entry here would strand that snapshot (withdrawBuffered matches
// on the live generation and would bail without restoring). The generation's
// own commit/abort resolves the entry, either way honoring the drop (see
// subscription.withdrawn).
func (s *Subscriptions) Unsubscribe(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubscribeMatchingLocked(connID, func(sub *subscription) bool { return sub.threadID == threadID })
}

// UnsubscribeLifecycle withdraws all delivery aliases owned by this lifecycle,
// including subscriptions displaced into an unresolved capture's rollback.
func (s *Subscriptions) UnsubscribeLifecycle(connID, lifecycleKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubscribeMatchingLocked(connID, func(sub *subscription) bool { return sub.lifecycleKey == lifecycleKey })
}

// UnsubscribeLifecycleAlias removes the requested delivery alias only when it
// belongs to the resolved lifecycle. Other aliases sharing that delivery key
// remain owned by their respective lifecycles.
func (s *Subscriptions) UnsubscribeLifecycleAlias(connID, deliveryKey, lifecycleKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubscribeMatchingLocked(connID, func(sub *subscription) bool {
		return sub.threadID == deliveryKey && sub.lifecycleKey == lifecycleKey
	})
}

func (s *Subscriptions) unsubscribeMatchingLocked(connID string, matches func(*subscription) bool) {
	var withdraw func(*subscription)
	withdraw = func(sub *subscription) {
		if matches(sub) {
			sub.withdrawn = true
		}
		if r := sub.rollback; r != nil {
			for i := range r.connection.subscriptions {
				withdraw(&r.connection.subscriptions[i])
			}
			if r.threadPrevious != nil {
				withdraw(r.threadPrevious)
			}
		}
	}
	for threadID, sub := range s.byConn[connID] {
		withdraw(sub)
		if sub.withdrawn && !sub.buffering {
			s.removeThreadLocked(connID, threadID)
		}
	}
}

func (s *Subscriptions) subscribeOwned(connID, threadID, lifecycleKey string, replace bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replace {
		s.removeConnectionLocked(connID)
	}
	s.subscribeLocked(&subscription{connID: connID, threadID: threadID, lifecycleKey: lifecycleKey})
}

func (s *Subscriptions) ReplaceConnectionSubscriptions(connID, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeConnectionLocked(connID)
	if threadID != "" {
		s.subscribeLocked(&subscription{connID: connID, threadID: threadID})
	}
}

func (s *Subscriptions) beginBuffered(connID, threadID string, replace bool, generation uint64, lifecycleKeys ...string) subscriptionCaptureRollback {
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
	owner := threadID
	if len(lifecycleKeys) != 0 {
		owner = lifecycleKeys[0]
	}
	s.subscribeLocked(&subscription{
		lifecycleKey: owner,
		rollback:     &rollback,
		connID:       connID,
		threadID:     threadID,
		buffering:    true,
		generation:   generation,
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
			if sub.withdrawn {
				// Withdrawal marks reach displaced entries directly. The current
				// generation may have a different lifecycle owner even when it
				// shares this delivery key, so its mark cannot suppress this entry.
				continue
			}
			s.subscribeLocked(&sub)
		}
		return true
	}
	s.removeThreadLocked(connID, threadID)
	if rollback.threadPrevious != nil && !rollback.threadPrevious.withdrawn {
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
			if !sub.withdrawn {
				sub.buffer = append(sub.buffer, record)
			}
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
	if sub.withdrawn {
		// The client unsubscribed mid-capture (see subscription.withdrawn):
		// drop the entry instead of committing it live.
		s.removeThreadLocked(connID, threadID)
		return nil, true
	}
	release := make([]SequencedNotification, 0, len(sub.buffer))
	for _, record := range sub.buffer {
		if record.Seq > sub.cut {
			release = append(release, record)
		}
	}
	sub.rollback = nil
	sub.buffering = false
	sub.buffer = nil
	return release, true
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

// Threads lists the threads the connection holds a live interest in; a
// withdrawn mid-capture entry does not count (see subscription.withdrawn).
func (s *Subscriptions) Threads(connID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	threads := make([]string, 0, len(s.byConn[connID]))
	for threadID, sub := range s.byConn[connID] {
		if sub.withdrawn {
			continue
		}
		threads = append(threads, threadID)
	}
	return threads
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

// ConnectionCount reports how many connections hold a live interest in the
// thread, skipping withdrawn entries (see subscription.withdrawn) — counting
// one would let a subscription the client dropped hold the relay open.
func (s *Subscriptions) ConnectionCount(threadID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, sub := range s.byThread[threadID] {
		if sub.withdrawn {
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
	if sub.lifecycleKey == "" {
		sub.lifecycleKey = sub.threadID
	}
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
