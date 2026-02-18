package server

import "sync"

// Broadcaster fans out events to multiple SSE subscribers.
// It stores events in a ring buffer for catchup on reconnect.
type Broadcaster struct {
	mu          sync.RWMutex
	ring        *RingBuffer
	subscribers map[uint64]chan BufferItem
	nextSubID   uint64
}

// NewBroadcaster creates a broadcaster with the given ring buffer capacity.
func NewBroadcaster(ringCapacity int) *Broadcaster {
	return &Broadcaster{
		ring:        NewRingBuffer(ringCapacity),
		subscribers: make(map[uint64]chan BufferItem),
	}
}

// Send stores a value in the ring buffer and sends it to all subscribers.
// The ring buffer assigns the monotonic ID. Non-blocking: slow subscribers
// will have events dropped.
func (b *Broadcaster) Send(value any) BufferItem {
	id := b.ring.Add(value)
	item := BufferItem{ID: id, Value: value}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- item:
		default:
			// Drop for slow consumers
		}
	}
	return item
}

// Subscribe returns a channel that receives events and an unsubscribe function.
// If lastID > 0, buffered events after that ID are sent as catchup.
func (b *Broadcaster) Subscribe(lastID uint64) (<-chan BufferItem, func()) {
	ch := make(chan BufferItem, 64)

	b.mu.Lock()
	id := b.nextSubID
	b.nextSubID++
	b.subscribers[id] = ch
	b.mu.Unlock()

	// Send catchup events from ring buffer
	if lastID > 0 {
		items := b.ring.After(lastID)
		for _, item := range items {
			select {
			case ch <- item:
			default:
			}
		}
	}

	unsub := func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

// SubscriberCount returns the number of active subscribers.
func (b *Broadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
