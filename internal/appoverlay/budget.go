package appoverlay

import (
	"container/list"
	"sync"

	"primeradiant.com/evener/appwire"
)

// DefaultBudgetBytes is the daemon-wide cap on the encoded notices every
// thread's ring holds together.
const DefaultBudgetBytes = 16 << 20

// Budget is the daemon-wide notice byte cap shared by every thread's ring.
// Past its limit it evicts the oldest notice, whichever thread holds it.
//
// Lock order: an Overlay's mutex, then the Budget's. An Overlay charges and
// releases its notices while holding its own mutex; the Budget never takes an
// Overlay's mutex. So each notice's payload lives on its charge, under the
// Budget's mutex: an eviction drops the payload at once, freeing it even for
// a thread that never runs again, and the owning Overlay forgets the empty
// entry the next time it looks at its ring.
type Budget struct {
	mu sync.Mutex
	// limit never changes after NewBudget, so it is read without the mutex.
	limit int
	used  int
	// charges holds every live charge, oldest first, across all threads.
	charges list.List
}

// charge is one notice's share of the budget. Its fields are guarded by the
// Budget's mutex.
type charge struct {
	bytes int
	// element is the charge's place in Budget.charges; nil once it is
	// evicted or released.
	element *list.Element
	// item is the notice itself; nil once the charge is evicted or released.
	item *appwire.OverlayItem
}

// NewBudget returns a budget that evicts the oldest notice while more than
// limit bytes are charged.
func NewBudget(limit int) *Budget {
	return &Budget{limit: limit}
}

// Used reports the bytes currently charged against the budget, across every
// thread sharing it. For the acceptance measurement (spec: "16 MB
// daemon-wide").
func (b *Budget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// charge adds a notice of the given size as the newest, evicting the oldest
// notices until the budget is within its limit. The caller never charges more
// than the limit, so the new notice itself is never evicted here.
func (b *Budget) charge(item *appwire.OverlayItem, bytes int) *charge {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &charge{bytes: bytes, item: item}
	c.element = b.charges.PushBack(c)
	b.used += bytes
	for b.used > b.limit {
		b.remove(b.charges.Front().Value.(*charge))
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
	c.item = nil
	b.used -= c.bytes
}
