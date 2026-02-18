package server

import "sync"

// BufferItem wraps a value with its monotonic ID.
type BufferItem struct {
	ID    uint64
	Value any
}

// RingBuffer is a thread-safe circular buffer with monotonic IDs.
// Used to buffer SSE events so reconnecting clients can catch up.
type RingBuffer struct {
	mu    sync.RWMutex
	items []BufferItem
	cap   int
	next  uint64 // next ID to assign (1-indexed)
	head  int    // write position in items slice
	size  int    // current number of valid items
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		items: make([]BufferItem, capacity),
		cap:   capacity,
		next:  1,
	}
}

// Add appends a value and returns its assigned ID.
func (rb *RingBuffer) Add(value any) uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	id := rb.next
	rb.next++
	rb.items[rb.head] = BufferItem{ID: id, Value: value}
	rb.head = (rb.head + 1) % rb.cap
	if rb.size < rb.cap {
		rb.size++
	}
	return id
}

// After returns all items with ID > afterID, in order.
// If afterID is 0, returns all buffered items.
// If afterID has been evicted, returns all available items.
func (rb *RingBuffer) After(afterID uint64) []BufferItem {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 {
		return nil
	}

	// Start position of the oldest item in the circular buffer.
	start := (rb.head - rb.size + rb.cap) % rb.cap

	result := make([]BufferItem, 0, rb.size)
	for i := 0; i < rb.size; i++ {
		idx := (start + i) % rb.cap
		item := rb.items[idx]
		if item.ID > afterID {
			result = append(result, item)
		}
	}
	return result
}
