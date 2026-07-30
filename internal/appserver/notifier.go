package appserver

import (
	"sync"

	"primeradiant.com/serf/appwire"
)

type SequencedNotification struct {
	Seq          uint64               `json:"seq"`
	ThreadID     string               `json:"threadId,omitempty"`
	Notification appwire.Notification `json:"notification"`
}

// RetainedNotificationWindow is an atomic view of the notifier's globally
// retained sequence boundary and the retained records for one thread.
type RetainedNotificationWindow struct {
	LowerSeq uint64
	UpperSeq uint64
	Records  []SequencedNotification
}

type Notifier struct {
	mu      sync.RWMutex
	nextSeq uint64
	limit   int
	history []SequencedNotification
}

func NewNotifier(limit int) *Notifier {
	if limit <= 0 {
		limit = 1000
	}
	return &Notifier{limit: limit}
}

// Callers must route the returned record under the same projectionMu hold
// that allocated it (return it from a CommitProjection commit closure).
// Broadcast's routedSeq high-water mark assumes every allocated sequence is
// already routed; one dropped before routing would let a later Broadcast
// land at or below an already-issued cut.
func (n *Notifier) Record(threadID, method string, params any) SequencedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nextSeq++
	msg := appwire.NotificationMessage(method, params)
	record := SequencedNotification{
		Seq:          n.nextSeq,
		ThreadID:     threadID,
		Notification: *msg.Notification,
	}
	n.history = append(n.history, record)
	if len(n.history) > n.limit {
		n.history = n.history[len(n.history)-n.limit:]
	}
	return record
}

func (n *Notifier) CurrentSequence() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.nextSeq
}

func (n *Notifier) ReplayAfter(cursor uint64, threadID string) []SequencedNotification {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]SequencedNotification, 0, len(n.history))
	for _, record := range n.history {
		if record.Seq <= cursor {
			continue
		}
		if threadID != "" && record.ThreadID != threadID {
			continue
		}
		out = append(out, record)
	}
	return out
}

func (n *Notifier) RetainedWindow(threadID string) RetainedNotificationWindow {
	n.mu.RLock()
	defer n.mu.RUnlock()
	window := RetainedNotificationWindow{UpperSeq: n.nextSeq}
	if len(n.history) > 0 {
		window.LowerSeq = n.history[0].Seq
	} else if n.nextSeq > 0 {
		window.LowerSeq = n.nextSeq + 1
	}
	window.Records = make([]SequencedNotification, 0, len(n.history))
	for _, record := range n.history {
		if threadID != "" && record.ThreadID != threadID {
			continue
		}
		window.Records = append(window.Records, record)
	}
	return window
}

// RetainedWindowCurrent reports whether no notification has been recorded
// since a RetainedWindow carrying upperSeq was captured.
func (n *Notifier) RetainedWindowCurrent(upperSeq uint64) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.nextSeq == upperSeq
}
