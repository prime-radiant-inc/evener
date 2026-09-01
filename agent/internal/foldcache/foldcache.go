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
// reads. Because of that, (size, mtime) alone is not quite enough: a
// same-size or coarse-mtime-coincidental rewrite by another process needs a
// cheap tail probe on top (see epochState) to stay detectable — a residual
// jobstore.Store's writer-owned cursor accepts because only a foreign
// process could ever trigger it there, but which is this cache's ordinary,
// designed-for threat model.
package foldcache

import (
	"bytes"
	"container/list"
	"context"
	"io"
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
// decode). The ctx an Extend call actually runs with is detached from any
// single caller's cancellation (context.WithoutCancel) — see Get's doc
// comment.
type Extend[T any] func(ctx context.Context, path string, fromOffset int64, prior T) (value T, toOffset int64, err error)

// Result is what Get returns.
type Result[T any] struct {
	Value  T
	Offset int64
	// Epoch counts how many times this path's cached fold has been
	// DISCARDED and restarted from byte zero because the file was not a
	// pure append of what the cache had — shrunk, rewritten at the same
	// size, or grown by a rewrite whose mtime happened to land in the same
	// bucket as the content it replaced (jobstore.Store's fileCursor
	// documents the same-size/same-mtime residual for its own, writer-owned
	// cursor; this cache's tail probe additionally resolves the two cases
	// above that mtime alone cannot). A resumable position derived from one
	// Result (e.g. a count of entries already rendered from Value) stays
	// safe to apply to a LATER Result for the same path for exactly as long
	// as Epoch is unchanged: an ordinary append never reorders or
	// invalidates anything already folded, so Epoch does not move for it,
	// only for a discard. This holds across eviction too: Epoch's
	// bookkeeping (epochState) is retained separately from the (possibly
	// large) folded value, so reclaiming memory for an idle path never
	// resets its epoch back to a value a later, unrelated rewrite could
	// coincidentally match.
	Epoch uint64
}

// Stats reports counters for tests and diagnostics.
type Stats struct {
	Entries     int
	Hits        int // confirmed unchanged since the last Get (via (size, mtime) and, when ambiguous, a tail-probe match): no Extend call at all
	Misses      int // this call became the owner of an Extend call (fresh path, growth, or a forced rescan)
	Coalesced   int // this call joined another in-flight owner's Extend call instead of starting its own
	Evictions   int
	FullRescans int // Extend was called with fromOffset 0 for a path this cache had already cached (excludes true first touches)
}

// tailProbeBytes is how many bytes ending at the cached offset are
// fingerprinted to detect a rewrite (size, mtime) alone cannot resolve: a
// same-size + same-mtime rewrite (Get's fast path previously trusted
// unconditionally — the classic jobstore.Store fileCursor residual) and a
// larger-but-mtime-unresolvable rewrite (refresh's growth branch previously
// assumed safe by construction, even though it cannot actually observe
// that). Raw bytes, not a hash: at this size a hash buys nothing — comparing
// 64 bytes via bytes.Equal costs about the same as comparing a fixed-size
// digest, with zero collision risk and no hash function to pick — so storing
// the bytes themselves is both simpler and strictly more precise. Cost per
// Get: exactly one extra open+seek+read of at most tailProbeBytes bytes,
// paid only when (size, mtime) alone cannot already resolve the comparison
// (an ordinary append that moves mtime forward, the common case, never pays
// it); a confirmed hit still pays it once (Get's own fast path), a
// concurrent pile of confirmed hits on the same path pays it once total
// (coalesced through the same singleflight flight as any other refresh).
const tailProbeBytes = 64

// epochState is a path's staleness bookkeeping: the (size, mtime) pair
// jobstore.Store's fileCursor rule compares against, the tail-probe bytes
// that resolve what that pair alone cannot, and the epoch counter both
// feed. It is held in its OWN map, separately from entries/order, so it
// survives LRU eviction of the (potentially large) folded value — eviction
// only reclaims memory for T's cost, never the O(1) signal a resumable
// continuation's staleness check depends on staying correct across it
// (roborev's #448 round-2 coherence finding: eviction used to reset epoch
// to 0, indistinguishable from "never seen," silently hiding a rewrite that
// raced an eviction).
//
// Unbounded in count — one entry per distinct path this process has ever
// Get'd, never pruned except via drop on ErrNotExist (see drop) — which is
// a deliberate, accepted tradeoff, not an oversight: at
// tailProbeBytes+~40 bytes each, even tens of thousands of distinct paths
// cost single-digit megabytes, and pruning on anything OTHER than a
// confirmed deletion would reopen exactly the bug this type exists to
// close (an evicted-and-forgotten path silently starting back at epoch 0).
type epochState struct {
	size   int64
	mod    time.Time
	offset int64  // where tail ends; mirrors the entry's offset at the time this was captured
	tail   []byte // last min(tailProbeBytes, offset) bytes ending at offset
	epoch  uint64
}

type entry[T any] struct {
	path   string
	value  T
	offset int64
	valid  bool
}

// Cache incrementally folds append-only files, keyed by path, bounded to a
// fixed number of entries evicted least-recently-used.
type Cache[T any] struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List // most-recently-used at the front
	maxEntries int

	// epochStates is intentionally NOT bounded alongside entries/order —
	// see epochState's doc comment.
	epochStates map[string]*epochState

	flights map[string]struct{}
	group   singleflight.Group

	hits, misses, coalesced, evictions, fullRescans int
}

// New returns a Cache bounded to maxEntries paths. maxEntries <= 0 disables
// caching (every Get reads from byte zero and nothing is retained) rather
// than panicking or growing unboundedly — a safe, inert default.
func New[T any](maxEntries int) *Cache[T] {
	return &Cache[T]{
		entries:     make(map[string]*list.Element),
		order:       list.New(),
		maxEntries:  maxEntries,
		epochStates: make(map[string]*epochState),
		flights:     make(map[string]struct{}),
	}
}

// Get returns the up-to-date folded value for path, calling extend to read
// only what changed since the last successful Get for this path (or the
// whole file, on first touch or a detected rewrite). Concurrent Get calls
// for the same path share one Extend call, coalesced via singleflight — but
// the shared call itself runs on a context detached from every individual
// caller (context.WithoutCancel): canceling any ONE caller — owner or
// waiter — must never poison the shared result for every OTHER, still-live
// caller (roborev's #448 round-2 coherence finding). Each caller still
// races its own ctx against that shared result independently, so a caller
// whose own context is canceled or expires still returns promptly with its
// own ctx.Err(), regardless of what the shared call is doing.
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

	if result, ok, err := c.tryFastHit(path, info); err != nil {
		var zero Result[T]
		return zero, err
	} else if ok {
		return result, nil
	}

	c.mu.Lock()
	flightKey := path
	if _, inflight := c.flights[flightKey]; inflight {
		c.coalesced++
	} else {
		c.flights[flightKey] = struct{}{}
		c.misses++
	}
	detached := context.WithoutCancel(ctx)
	resultCh := c.group.DoChan(flightKey, func() (any, error) {
		result, err := c.refresh(detached, path, info, extend)
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

// tryFastHit attempts the "nothing changed" shortcut without ever
// registering a singleflight flight: if the cached entry is still valid and
// both (size, mtime) AND a tail probe (see epochState) confirm nothing
// changed, it returns the cached value directly. Any inconclusive outcome —
// no cached entry, (size, mtime) differ, or the probe itself finds a
// mismatch — returns ok=false so Get falls through to the ordinary
// singleflight+refresh path, which is the single authoritative
// decision-maker for every other case (including one that reaches the same
// (size, mtime)-match-but-tail-probe-needed state a different way, e.g. no
// entry resident at all post-eviction — see refresh).
func (c *Cache[T]) tryFastHit(path string, info os.FileInfo) (Result[T], bool, error) {
	c.mu.Lock()
	element, hasElement := c.entries[path]
	st := c.epochStates[path]
	if !hasElement || st == nil {
		c.mu.Unlock()
		return Result[T]{}, false, nil
	}
	e := element.Value.(*entry[T])
	if !e.valid || st.size != info.Size() || !st.mod.Equal(info.ModTime()) {
		c.mu.Unlock()
		return Result[T]{}, false, nil
	}
	value, offset, epoch, tail := e.value, e.offset, st.epoch, st.tail
	c.mu.Unlock()

	match, err := tailProbeMatches(path, offset, tail)
	if err != nil {
		return Result[T]{}, false, err
	}
	if !match {
		return Result[T]{}, false, nil
	}

	c.mu.Lock()
	// Re-verify this is still the live entry for path: a concurrent
	// refresh could have replaced or evicted it while the tail-probe read
	// above ran without the lock held.
	if el2, ok2 := c.entries[path]; ok2 && el2 == element {
		c.order.MoveToFront(element)
		c.hits++
		c.mu.Unlock()
		return Result[T]{Value: value, Offset: offset, Epoch: epoch}, true, nil
	}
	c.mu.Unlock()
	return Result[T]{}, false, nil
}

// refresh does the actual (possibly incremental) read. It runs inside the
// singleflight-owned closure, so exactly one goroutine executes it per path
// at a time; mu is NOT held while it runs (Extend may take a while).
func (c *Cache[T]) refresh(ctx context.Context, path string, info os.FileInfo, extend Extend[T]) (Result[T], error) {
	c.mu.Lock()
	st := c.epochStates[path]
	epoch := uint64(0)
	if st != nil {
		epoch = st.epoch
	}
	var element *list.Element
	if el, ok := c.entries[path]; ok {
		element = el
	}
	c.mu.Unlock()

	var prior T
	fromOffset := int64(0)
	sameSizeAmbiguous := false
	growthAmbiguous := false
	if st != nil {
		switch {
		case info.Size() < st.size:
			// Shrunk: definitely not a pure append. Discard and bump epoch
			// so an outstanding continuation keyed to the old content is
			// detectably stale.
			epoch++
		case info.Size() == st.size:
			if st.mod.Equal(info.ModTime()) {
				// Same length AND same mtime: mtime alone cannot resolve
				// this (the classic jobstore.Store fileCursor residual) —
				// needs the tail probe below.
				sameSizeAmbiguous = true
			} else {
				// Same length, different mtime: a rewrite that happens to
				// match the old size. Discard and bump epoch, same as a
				// shrink.
				epoch++
			}
		default: // info.Size() > st.size
			if st.mod.Equal(info.ModTime()) {
				// Grew, but mtime gives no signal either way: could be a
				// pure append (mtime just didn't move) or a
				// truncate-and-rewrite-larger that coincidentally landed
				// in the same mtime bucket. Needs the tail probe below —
				// unlike the same-size case, SOMETHING must still be read
				// (the file is genuinely larger), so this can never
				// short-circuit to a cached value the way sameSizeAmbiguous
				// can; the probe here only decides the epoch signal.
				growthAmbiguous = true
			} else if element != nil {
				if e := element.Value.(*entry[T]); e.valid {
					prior, fromOffset = e.value, e.offset
				}
			}
		}
	}

	if sameSizeAmbiguous {
		match, tailErr := tailProbeMatches(path, st.offset, st.tail)
		if tailErr != nil {
			var zero Result[T]
			return zero, tailErr
		}
		if !match {
			epoch++
		} else if element != nil {
			if e := element.Value.(*entry[T]); e.valid && e.offset == st.offset {
				// Confirmed unchanged, and the cached value is still
				// resident: a true hit reached via refresh instead of
				// Get's own fast path (e.g. a concurrent evict/replace
				// raced tryFastHit's own probe). Nothing to extend.
				c.mu.Lock()
				c.hits++
				c.mu.Unlock()
				return Result[T]{Value: e.value, Offset: e.offset, Epoch: epoch}, nil
			}
		}
		// Confirmed unchanged but nothing resident to return (evicted): a
		// full reread is required regardless (nothing to resume from),
		// but epoch does not bump, since the content itself is confirmed
		// the same. fromOffset stays 0 either way this branch exits.
	} else if growthAmbiguous {
		match, tailErr := tailProbeMatches(path, st.offset, st.tail)
		if tailErr != nil {
			var zero Result[T]
			return zero, tailErr
		}
		if !match {
			epoch++
		}
		// fromOffset stays 0 regardless of match: unlike the same-size
		// case, the file is genuinely larger, so there is always new data
		// to read via extend — the probe only decides whether the OLD
		// prefix is trustworthy enough that epoch should stay put,
		// matching the existing, deliberate choice to always fall back to
		// a full reread here rather than resume incrementally off an
		// mtime-unresolvable growth.
	}

	wasCached := st != nil
	if wasCached && fromOffset == 0 {
		c.mu.Lock()
		c.fullRescans++
		c.mu.Unlock()
	}

	value, offset, err := extend(ctx, path, fromOffset, prior)
	if err != nil {
		return Result[T]{}, err
	}

	tail, tailErr := captureTailProbe(path, offset)
	if tailErr != nil {
		return Result[T]{}, tailErr
	}

	c.mu.Lock()
	c.epochStates[path] = &epochState{size: info.Size(), mod: info.ModTime(), offset: offset, tail: tail, epoch: epoch}
	c.publishLocked(path, entry[T]{path: path, value: value, offset: offset, valid: true})
	c.mu.Unlock()
	return Result[T]{Value: value, Offset: offset, Epoch: epoch}, nil
}

// tailProbeMatches reports whether the tailProbeBytes bytes ending at
// offset in the file at path still equal want (see epochState). A file
// shorter than expected is treated as a mismatch, not an error: something
// about the file changed underneath regardless of exactly how, and the
// caller's job either way is "force a full rescan."
func tailProbeMatches(path string, offset int64, want []byte) (bool, error) {
	if len(want) == 0 {
		// Nothing was ever read from this path before (offset 0) -- there
		// is no prior content to compare against, so there is nothing to
		// contradict.
		return true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()
	start := offset - int64(len(want))
	if start < 0 {
		return false, nil
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return false, err
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(f, got); err != nil {
		return false, nil // shorter than expected: file changed underneath
	}
	return bytes.Equal(got, want), nil
}

// captureTailProbe reads the last min(tailProbeBytes, offset) bytes ending
// at offset, for storage in epochState — called right after a successful
// extend, while the file is known to be at least offset bytes long (extend
// just read through it).
func captureTailProbe(path string, offset int64) ([]byte, error) {
	if offset <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	n := int64(tailProbeBytes)
	start := offset - n
	if start < 0 {
		start = 0
		n = offset
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readUncached is New(0)'s degenerate path: always a full read, nothing
// retained. Kept simple and separate rather than threading a "disabled"
// flag through the cached path's locking and eviction logic. It never
// coalesces concurrent callers (each gets its own extend call, with its own
// ctx), so the leader-cancellation isolation Get documents for the cached
// path does not apply here — there is no shared flight for one caller's
// cancellation to poison in the first place.
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

// drop removes path's entry AND its epochState (if any) — used when the
// file no longer exists, so a later Get for a path that reappears starts
// completely clean rather than resuming from a cursor, or comparing against
// a tail probe, over bytes that may no longer be the same file at all. This
// is also epochStates' one pruning path (see epochState's doc comment):
// deletion is a genuine, detectable "this path is done" signal, unlike
// eviction, which only means "not resident right now." Unlike the
// *Locked methods above, drop manages its own locking: its only caller
// (Get, on ErrNotExist) does not hold mu.
func (c *Cache[T]) drop(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[path]; ok {
		delete(c.entries, path)
		c.order.Remove(element)
	}
	delete(c.epochStates, path)
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
