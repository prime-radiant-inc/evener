package transcriptindex

import (
	"container/list"
	"errors"
	"sync"
)

// DefaultCacheCapacity is the index handle cache's default size: the spec's
// bound on live index memory, "A bounded cache of 64 open index handles plus
// their headers is the only index memory."
const DefaultCacheCapacity = 64

// Cache holds up to capacity open indexes, least recently used first out. A
// handle currently acquired is never closed; Release returns it to the pool
// of idle handles the cache may evict from.
type Cache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*cacheEntry
	// idle holds entries with no outstanding acquire, in release order: the
	// front is the least recently released, and so the next evicted.
	idle   *list.List
	closed bool
}

// cacheEntry is one cached handle. elem is its position in idle while refs is
// 0 (idle, evictable), and nil while refs > 0 (acquired).
type cacheEntry struct {
	path string
	x    *Index
	refs int
	elem *list.Element
}

// NewCache returns a cache holding up to capacity open indexes.
func NewCache(capacity int) *Cache {
	return &Cache{capacity: capacity, entries: map[string]*cacheEntry{}, idle: list.New()}
}

// Acquire returns the open index for path, opening it (Open(path,
// DirFor(path))) on a miss. The caller must Release it when done.
func (c *Cache) Acquire(path string) (*Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("transcript index cache is closed")
	}
	if e, ok := c.entries[path]; ok {
		if e.elem != nil {
			c.idle.Remove(e.elem)
			e.elem = nil
		}
		e.refs++
		return e.x, nil
	}
	x, err := Open(path, DirFor(path))
	if err != nil {
		return nil, err
	}
	c.evict()
	c.entries[path] = &cacheEntry{path: path, x: x, refs: 1}
	return x, nil
}

// Release returns a handle Acquire returned. Once nothing holds it, a later
// miss may evict it.
func (c *Cache) Release(x *Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[x.path]
	if !ok || e.x != x {
		return
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
	e.elem = c.idle.PushBack(e)
}

// evict closes idle handles until there is room for one more entry. An
// acquired handle is never evicted, so the cache can grow past capacity while
// every handle is in use.
func (c *Cache) evict() {
	for len(c.entries) >= c.capacity {
		front := c.idle.Front()
		if front == nil {
			return
		}
		e := front.Value.(*cacheEntry) //nolint:errcheck // idle only ever holds *cacheEntry
		c.idle.Remove(front)
		delete(c.entries, e.path)
		_ = e.x.Close()
	}
}

// Forget closes the handle for path if nothing holds it: a caller uses it for
// a session that is gone, so the cache does not keep a stale sidecar for a
// deleted transcript.
func (c *Cache) Forget(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok || e.refs > 0 {
		return
	}
	if e.elem != nil {
		c.idle.Remove(e.elem)
	}
	delete(c.entries, path)
	_ = e.x.Close()
}

// Close closes every idle handle and marks the cache closed: further
// Acquires error, and a handle still acquired closes on its Release.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	var err error
	for e := c.idle.Front(); e != nil; e = e.Next() {
		entry := e.Value.(*cacheEntry) //nolint:errcheck // idle only ever holds *cacheEntry
		err = errors.Join(err, entry.x.Close())
		delete(c.entries, entry.path)
	}
	c.idle.Init()
	return err
}
