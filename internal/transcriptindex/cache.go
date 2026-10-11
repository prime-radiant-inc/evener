package transcriptindex

import (
	"errors"
	"sync"
)

// DefaultCacheCapacity is the index handle cache's default size: the spec's
// bound on live index memory, "A bounded cache of 64 open index handles plus
// their headers is the only index memory."
const DefaultCacheCapacity = 64

// cacheOpenHook, when set, runs synchronously just before Acquire opens a
// path for the first time: never while another Acquire is already opening
// it, since that one waits instead of calling Open itself. Test seam only;
// nil in production.
var cacheOpenHook func(path string)

// cacheOpenedHook, when set, runs synchronously right after Open returns,
// before open() re-takes the cache lock to decide whether to publish the
// result. Test seam only, for forcing a Close into that window; nil in
// production.
var cacheOpenedHook func(path string)

// cachePublishedHook, when set, runs synchronously right after open()
// unlocks having already decided, under the lock, whether to publish. Test
// seam only, for forcing a Close into the window a past bug read c.closed a
// second time, unsynchronized, to make the same decision again; nil in
// production.
var cachePublishedHook func(path string)

// Cache holds up to capacity open indexes, least recently used first out. A
// handle currently acquired is never closed; Release returns it to the pool
// of idle handles the cache may evict from.
type Cache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*cacheEntry
	// idleHead/idleTail thread the entries with no outstanding acquire into a
	// doubly linked list in release order: idleHead is the least recently
	// released, and so the next evicted.
	idleHead, idleTail *cacheEntry
	closed             bool
}

// cacheEntry is one cached or in-flight handle.
type cacheEntry struct {
	path string
	x    *Index
	refs int
	// ready is non-nil while a goroutine is inside Open for this path;
	// every other Acquire of the same path waits on it instead of calling
	// Open itself, then re-checks the entry once it closes.
	ready chan struct{}
	// idle and prev/next place this entry in the cache's idle list; only
	// meaningful while ready is nil and refs is 0.
	idle       bool
	prev, next *cacheEntry
}

// NewCache returns a cache holding up to capacity open indexes.
func NewCache(capacity int) *Cache {
	return &Cache{capacity: capacity, entries: map[string]*cacheEntry{}}
}

// Acquire returns the open index for path, opening it (Open(path,
// DirFor(path))) on a miss. Open runs without the cache's lock held, so a
// slow open (a large rebuild) never blocks another path's Acquire or
// Release; concurrent Acquires of the same path share one Open call and both
// get the resulting handle, refcounted once each. The caller must Release it
// when done.
func (c *Cache) Acquire(path string) (*Index, error) {
	c.mu.Lock()
	for {
		if c.closed {
			c.mu.Unlock()
			return nil, errors.New("transcript index cache is closed")
		}
		e, ok := c.entries[path]
		if !ok {
			c.evict()
			e = &cacheEntry{path: path, ready: make(chan struct{})}
			c.entries[path] = e
			c.mu.Unlock()
			return c.open(path, e)
		}
		if e.ready == nil {
			c.idleRemove(e)
			e.refs++
			c.mu.Unlock()
			return e.x, nil
		}
		// Another Acquire is already opening this path: wait for it to
		// finish, then re-check from the top (it may have failed, or the
		// cache may have closed meanwhile).
		ready := e.ready
		c.mu.Unlock()
		<-ready
		c.mu.Lock()
	}
}

// open runs path's miss outside the cache lock and publishes the result: on
// success e becomes usable, with one reference for this call; on failure, or
// if the cache closed while this ran, e is removed so the next Acquire
// retries and this call errors. Whether to publish is one decision made
// under the lock, so a Close that lands in the window between Open
// returning and this re-taking the lock is never read twice with different
// answers: either it is not yet visible and this publishes normally, or it
// is, and this call sees exactly what every other Acquire from here on
// sees. Either way every Acquire waiting on e.ready wakes once this returns.
func (c *Cache) open(path string, e *cacheEntry) (*Index, error) {
	if cacheOpenHook != nil {
		cacheOpenHook(path)
	}
	x, err := Open(path, DirFor(path))
	if cacheOpenedHook != nil {
		cacheOpenedHook(path)
	}

	c.mu.Lock()
	ready := e.ready
	e.ready = nil
	published := err == nil && !c.closed
	switch {
	case published:
		e.x, e.refs = x, 1
	default:
		delete(c.entries, path)
	}
	c.mu.Unlock()
	close(ready)
	if cachePublishedHook != nil {
		// Test seam only: exercises the window where a bug once re-read
		// c.closed here, unsynchronized and after publishing had already
		// decided under the lock, and so could still discard an already
		// published handle. published is a local copy of that one locked
		// decision, so nothing read after this point can change the answer.
		cachePublishedHook(path)
	}

	if err != nil {
		return nil, err
	}
	if !published {
		_ = x.Close()
		return nil, errors.New("transcript index cache is closed")
	}
	return x, nil
}

// Release returns a handle Acquire returned. Once nothing holds it, a later
// miss may evict it, or Close, having already run, closes it now. Releasing
// a handle the cache did not hand out, including a second release of one
// already fully released, panics rather than corrupt the cache's bookkeeping.
func (c *Cache) Release(x *Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[x.path]
	if !ok || e.x != x || e.refs <= 0 {
		panic("transcriptindex: Release of a handle the cache did not acquire")
	}
	e.refs--
	if e.refs > 0 {
		return
	}
	if c.closed {
		delete(c.entries, x.path)
		_ = x.Close()
		return
	}
	c.idlePushBack(e)
}

// evict closes idle handles until there is room for one more entry. An
// acquired or opening handle is never evicted, so the cache can grow past
// capacity while every handle is in use or being opened.
func (c *Cache) evict() {
	for len(c.entries) >= c.capacity {
		e := c.idleHead
		if e == nil {
			return
		}
		c.idleRemove(e)
		delete(c.entries, e.path)
		_ = e.x.Close()
	}
}

// OpenHandles reports how many paths currently hold a cache entry: idle,
// acquired or still opening, each counted once. Diagnostic/test seam: a
// server-level registry over several transcripts (internal/transcriptindex's
// own tests already cover eviction at capacity) uses it to assert the
// registry's shared cache never grows past its capacity.
func (c *Cache) OpenHandles() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Forget closes the handle for path if nothing holds it and no Acquire is
// still opening it: a caller uses it for a session that is gone, so the
// cache does not keep a stale sidecar for a deleted transcript.
func (c *Cache) Forget(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok || e.refs > 0 || e.ready != nil {
		return
	}
	c.idleRemove(e)
	delete(c.entries, path)
	_ = e.x.Close()
}

// Close closes every idle handle and marks the cache closed: further
// Acquires error, a handle still acquired closes on its Release, and an open
// already in flight closes its handle as soon as it finishes.
func (c *Cache) Close() error {
	c.mu.Lock()
	c.closed = true
	var err error
	for e := c.idleHead; e != nil; {
		next := e.next
		err = errors.Join(err, e.x.Close())
		delete(c.entries, e.path)
		e.prev, e.next, e.idle = nil, nil, false
		e = next
	}
	c.idleHead, c.idleTail = nil, nil
	c.mu.Unlock()
	return err
}

// idlePushBack appends e to the idle list, as the most recently released.
func (c *Cache) idlePushBack(e *cacheEntry) {
	e.idle = true
	e.prev, e.next = c.idleTail, nil
	if c.idleTail != nil {
		c.idleTail.next = e
	} else {
		c.idleHead = e
	}
	c.idleTail = e
}

// idleRemove takes e out of the idle list; a no-op when e is not in it.
func (c *Cache) idleRemove(e *cacheEntry) {
	if !e.idle {
		return
	}
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.idleHead = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.idleTail = e.prev
	}
	e.prev, e.next, e.idle = nil, nil, false
}
