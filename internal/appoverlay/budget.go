package appoverlay

import (
	"container/list"
	"sync"
)

// DefaultBudgetBytes is the daemon-wide cap on the encoded notices every
// thread's ring holds together.
const DefaultBudgetBytes = 16 << 20

// Budget is the daemon-wide notice byte cap shared by every thread's ring.
// Past its limit it evicts the oldest notice, whichever thread holds it.
//
// Lock order: an Overlay's mutex, then the Budget's. An Overlay charges and
// releases its notices while holding its own mutex; the Budget never takes an
// Overlay's mutex. So an eviction only marks the evicted charge, and the
// owning Overlay drops the notice the next time it looks at its ring (under
// its own mutex, consulting the marks under the Budget's).
type Budget struct {
	mu    sync.Mutex
	used  int
	limit int
	// charges holds every live charge, oldest first, across all threads.
	charges list.List
}

// charge is one notice's share of the budget. Its fields are guarded by the
// Budget's mutex.
type charge struct {
	bytes   int
	element *list.Element
	evicted bool
}

// NewBudget returns a budget that evicts the oldest notice while more than
// limit bytes are charged.
func NewBudget(limit int) *Budget {
	return &Budget{limit: limit}
}

// charge adds a notice of the given size as the newest, evicting the oldest
// notices, possibly this one, until the budget is within its limit.
func (b *Budget) charge(bytes int) *charge {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &charge{bytes: bytes}
	c.element = b.charges.PushBack(c)
	b.used += bytes
	for b.used > b.limit {
		oldest := b.charges.Front().Value.(*charge)
		b.remove(oldest)
		oldest.evicted = true
	}
	return c
}

// release returns a notice's bytes. Releasing an evicted or already released
// charge does nothing.
func (b *Budget) release(c *charge) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c.element != nil {
		b.remove(c)
	}
}

func (b *Budget) remove(c *charge) {
	b.charges.Remove(c.element)
	c.element = nil
	b.used -= c.bytes
}
