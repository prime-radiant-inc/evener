// Package foldcache caches the result of incrementally folding an
// append-only journal file, so repeated reads of the same file cost O(bytes
// appended since the last read) instead of O(file size).
//
// This mirrors two proven patterns already in this codebase rather than
// inventing a third: jobstore.Store's fileCursor (agent/internal/jobstore/
// store.go) — trust a cached prefix only while the file's (size, mtime)
// still match what was last observed, and fall back to a full reread the
// moment they don't — and cmd/evener-hub's navigationRepresentationCache
// (cmd/evener-hub/navigation_cache.go) — a container/list LRU guarded by one
// mutex, with golang.org/x/sync/singleflight coalescing concurrent callers
// onto a single in-flight build. foldcache's Cache is the same shape as the
// latter, generalized to incremental extension instead of build-from-
// scratch, and using the former's staleness rule to decide when extension is
// safe.
//
// Unlike jobstore.Store's cursor, a foldcache.Cache does not own the file —
// it has no writer to cross-check against, so every Get restats the file
// rather than trusting an append it made itself. That is the right tradeoff
// here: the cache lives in a reader process (the hub, or an agent process
// reading a sibling session's journal) that never writes the journals it
// reads.
package foldcache

import (
	"container/list"
	"context"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Extend brings value up to date with path's current contents. fromOffset is
// where to resume reading — 0 for a fresh fold, in which case prior is the
// zero value of T. Extend must return the new folded value and the byte
// offset it has now consumed through. That offset need not be the file's
// full current size: a genuine torn (unterminated) trailing line is never
// safe to treat as consumed, so an Extend that tolerates one should stop
// short of it and let the next call pick it up once it is complete.
//
// ctx is checked by Cache.Get before an Extend call begins, but Extend owns
// checking ctx during its own read (a large delta can still take a while to
// decode).
type Extend[T any] func(ctx context.Context, path string, fromOffset int64, prior T) (value T, toOffset int64, err error)

// Result is what Get returns.
type Result[T any] struct {
	Value  T
	Offset int64
	// Epoch counts how many times this path's cached fold has been
	// DISCARDED and restarted from byte zero because the file was not a
	// pure append of what the cache had — shrunk, or rewritten at the same
	// size (jobstore.Store's fileCursor documents this exact residual: a
	// same-length, same-mtime rewrite is the one case no timestamp-based
	// scheme can see). A resumable position derived from one Result (e.g. a
	// count of entries already rendered from Value) stays safe to apply to
	// a LATER Result for the same path for exactly as long as Epoch is
	// unchanged: an ordinary append never reorders or invalidates anything
	// already folded, so Epoch does not move for it, only for a discard.
	Epoch uint64
}

// Stats reports counters for tests and diagnostics.
type Stats struct {
	Entries     int
	Hits        int // (size, mtime) unchanged since the last Get: no Extend call at all
	Misses      int // this call became the owner of an Extend call (fresh path, growth, or a forced rescan)
	Coalesced   int // this call joined another in-flight owner's Extend call instead of starting its own
	Evictions   int
	FullRescans int // Extend was called with fromOffset 0 for a path this cache had already cached (excludes true first touches)
}

type entry[T any] struct {
	path  string
	value T
	// offset/size/mod/valid mirror jobstore.Store's fileCursor fields
	// exactly (agent/internal/jobstore/store.go) — same coherence rule,
	// same staleness check, applied by a reader instead of the owning
	// writer.
	offset int64
	size   int64
	mod    time.Time
	valid  bool
	epoch  uint64
}

// Cache incrementally folds append-only files, keyed by path, bounded to a
// fixed number of entries evicted least-recently-used.
type Cache[T any] struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List // most-recently-used at the front
	maxEntries int

	flights map[string]struct{}
	group   singleflight.Group

	hits, misses, coalesced, evictions, fullRescans int
}

// New returns a Cache bounded to maxEntries paths. maxEntries <= 0 disables
// caching (every Get reads from byte zero and nothing is retained) rather
// than panicking or growing unboundedly — a safe, inert default.
func New[T any](maxEntries int) *Cache[T] {
	return &Cache[T]{
		entries:    make(map[string]*list.Element),
		order:      list.New(),
		maxEntries: maxEntries,
		flights:    make(map[string]struct{}),
	}
}

// Get returns the up-to-date folded value for path, calling extend to read
// only what changed since the last successful Get for this path (or the
// whole file, on first touch or a detected rewrite). Concurrent Get calls
// for the same path share one Extend call: whichever caller's Get happens to
// register it first becomes the owner and its ctx is the one Extend runs
// with; every other concurrent caller only waits for that result, and its
// own ctx governs nothing but its own wait (canceling a waiter never cancels
// the owner's in-flight Extend call).
func (c *Cache[T]) Get(ctx context.Context, path string, extend Extend[T]) (Result[T], error) {
	if err := ctx.Err(); err != nil {
		var zero Result[T]
		return zero, err
	}
	if c.maxEntries <= 0 {
		return c.readUncached(ctx, path, extend)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			c.drop(path)
			return Result[T]{}, nil
		}
		var zero Result[T]
		return zero, statErr
	}

	c.mu.Lock()
	if element, ok := c.entries[path]; ok {
		e := element.Value.(*entry[T])
		if e.valid && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
			c.order.MoveToFront(element)
			c.hits++
			result := Result[T]{Value: e.value, Offset: e.offset, Epoch: e.epoch}
			c.mu.Unlock()
			return result, nil
		}
	}

	flightKey := path
	if _, inflight := c.flights[flightKey]; inflight {
		c.coalesced++
	} else {
		c.flights[flightKey] = struct{}{}
		c.misses++
	}
	resultCh := c.group.DoChan(flightKey, func() (any, error) {
		result, err := c.refresh(ctx, path, info, extend)
		c.finishFlight(flightKey)
		if err != nil {
			return nil, err
		}
		return result, nil
	})
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		var zero Result[T]
		return zero, ctx.Err()
	case res := <-resultCh:
		if res.Err != nil {
			var zero Result[T]
			return zero, res.Err
		}
		return res.Val.(Result[T]), nil
	}
}

// refresh does the actual (possibly incremental) read. It runs inside the
// singleflight-owned closure, so exactly one goroutine executes it per path
// at a time; mu is NOT held while it runs (Extend may take a while).
func (c *Cache[T]) refresh(ctx context.Context, path string, info os.FileInfo, extend Extend[T]) (Result[T], error) {
	c.mu.Lock()
	var prior T
	fromOffset := int64(0)
	epoch := uint64(0)
	wasCached := false
	if element, ok := c.entries[path]; ok {
		e := element.Value.(*entry[T])
		epoch = e.epoch
		wasCached = e.valid
		switch {
		case !e.valid:
			// Never successfully read (an earlier attempt errored, or this
			// is a stale placeholder): start clean, no epoch bump — nothing
			// valid existed to invalidate.
		case info.Size() < e.size:
			// Shrunk: definitely not a pure append. Discard and bump epoch
			// so an outstanding continuation keyed to the old content is
			// detectably stale.
			epoch++
		case info.Size() == e.size && !e.mod.Equal(info.ModTime()):
			// Same length, different mtime: a rewrite that happens to match
			// the old size. Discard and bump epoch, same as a shrink. (The
			// remaining case — same length AND same mtime — is Get's own
			// early-return "true hit" above; refresh is never reached for
			// it, so the switch has no branch for it.)
			epoch++
		default:
			// info.Size() > e.size. If mtime moved, this is an ordinary,
			// safe-to-extend append. If mtime did NOT move, the filesystem
			// cannot resolve the write (jobstore.Store's fileCursor
			// documents the identical case for its own, writer-owned
			// cursor) -- fall back to a full reread rather than risk
			// double-counting or skipping bytes. This is NOT treated as a
			// proven rewrite: append-only order is unaffected either way,
			// so epoch does not bump.
			if e.mod.Equal(info.ModTime()) {
				fromOffset = 0
			} else {
				prior, fromOffset = e.value, e.offset
			}
		}
	}
	if wasCached && fromOffset == 0 {
		c.fullRescans++
	}
	c.mu.Unlock()

	value, offset, err := extend(ctx, path, fromOffset, prior)
	if err != nil {
		return Result[T]{}, err
	}

	c.mu.Lock()
	c.publishLocked(path, entry[T]{
		path: path, value: value, offset: offset,
		size: info.Size(), mod: info.ModTime(), valid: true, epoch: epoch,
	})
	c.mu.Unlock()
	return Result[T]{Value: value, Offset: offset, Epoch: epoch}, nil
}

// readUncached is New(0)'s degenerate path: always a full read, nothing
// retained. Kept simple and separate rather than threading a "disabled"
// flag through the cached path's locking and eviction logic.
func (c *Cache[T]) readUncached(ctx context.Context, path string, extend Extend[T]) (Result[T], error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Result[T]{}, nil
		}
		var zero Result[T]
		return zero, err
	}
	var zero T
	value, offset, err := extend(ctx, path, 0, zero)
	if err != nil {
		var zeroResult Result[T]
		return zeroResult, err
	}
	return Result[T]{Value: value, Offset: offset}, nil
}

func (c *Cache[T]) publishLocked(path string, e entry[T]) {
	if element, ok := c.entries[path]; ok {
		*element.Value.(*entry[T]) = e
		c.order.MoveToFront(element)
		return
	}
	for c.order.Len() >= c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		old := oldest.Value.(*entry[T])
		delete(c.entries, old.path)
		c.order.Remove(oldest)
		c.evictions++
	}
	element := c.order.PushFront(&e)
	c.entries[path] = element
}

// drop removes path's entry (if any) — used when the file no longer exists,
// so a later Get for a path that reappears starts clean rather than
// resuming from a cursor over bytes that may no longer be the same file at
// all. Unlike the *Locked methods above, drop manages its own locking: its
// only caller (Get, on ErrNotExist) does not hold mu.
func (c *Cache[T]) drop(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[path]; ok {
		delete(c.entries, path)
		c.order.Remove(element)
	}
}

func (c *Cache[T]) finishFlight(flightKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Forget before releasing the lock, same reasoning as
	// navigationRepresentationCache.finishFlight: this closes the gap
	// between the owner returning and singleflight dropping its call so a
	// subsequent Get cannot join a call that already finished.
	c.group.Forget(flightKey)
	delete(c.flights, flightKey)
}

// Stats returns a snapshot of this cache's counters.
func (c *Cache[T]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries:     c.order.Len(),
		Hits:        c.hits,
		Misses:      c.misses,
		Coalesced:   c.coalesced,
		Evictions:   c.evictions,
		FullRescans: c.fullRescans,
	}
}
