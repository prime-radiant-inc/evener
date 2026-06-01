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
