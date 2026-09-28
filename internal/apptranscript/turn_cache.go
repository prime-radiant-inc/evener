package apptranscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
)

const defaultTurnCacheSize = 32

// TurnCache memoizes native item transcript projections by path and
// authoritative file metadata.
// Transcript files are append-only, so matching object identity, size, mtime,
// and platform change time means the parse is unchanged — a cache hit returns
// the previously parsed turns without re-reading and re-projecting the file.
//
// The returned slice is shared and MUST be treated as read-only by callers.
type TurnCache struct {
	mu      sync.Mutex
	entries map[string]turnCacheEntry
	order   []string // least-recently-used first, for bounded eviction
	max     int
}

type turnCacheEntry struct {
	size           int64
	mod            time.Time
	fileIdentity   string
	changeIdentity string
	turns          []appwire.Turn
	full           bool
	// usageTotal memoizes UsageTotalFromFile's full-transcript token sum for
	// one file identity and divergence ordinal. Kept alongside the parse memo
	// so both are evicted together, and separate from it because the sum is a
	// different projection of the same immutable bytes.
	usageTotal *scanMemo[*appwire.EvenerUsage]
	// failedToolCalls memoizes FailedToolCallsFromFile's full-transcript
	// failure count, on the same terms as usageTotal above.
	failedToolCalls *scanMemo[int]
	// derivedTotals memoizes DerivedTotalsFromFile's combined single-pass scan
	// (usage sum and failure count together), on the same terms as usageTotal
	// and failedToolCalls above.
	derivedTotals *scanMemo[derivedTotals]
}

// NewTurnCache returns a TurnCache bounded to a default number of transcripts.
func NewTurnCache() *TurnCache {
	return &TurnCache{entries: map[string]turnCacheEntry{}, max: defaultTurnCacheSize}
}

// scanMemoKey is the file identity a memoized full-transcript scan (the usage
// total, the failed-tool-call count, or the combined derived totals) is valid
// for. It mirrors the turn cache's own parse-validity gate (object identity,
// size, mtime, platform change time) and adds the divergence ordinal, since
// two ordinals over one file are two different answers. mtime is held as nanos
// so the key stays comparable with == (a time.Time compares its
// monotonic/location fields too, which would spuriously miss).
type scanMemoKey struct {
	size           int64
	modUnixNano    int64
	fileIdentity   string
	changeIdentity string
	fromOrdinal    int
}

// scanMemo is one memoized full-transcript scan value, valid while the file
// identity and divergence ordinal it was computed for still hold. One generic
// type serves all three scans (usage total, failure count, combined derived
// totals), so their validity gate is a single implementation rather than three
// copies that can drift apart.
type scanMemo[T any] struct {
	key   scanMemoKey
	value T
}

// scanMemoIdentity builds the scanMemoKey for one stat result and divergence
// ordinal. All three full-transcript scan memos key on exactly this, so the
// combined memo can never outlive the two it consolidates.
func scanMemoIdentity(info os.FileInfo, fromEntryOrdinal int) scanMemoKey {
	return scanMemoKey{
		size:           info.Size(),
		modUnixNano:    info.ModTime().UnixNano(),
		fileIdentity:   FileIdentity(info),
		changeIdentity: fileChangeIdentity(info),
		fromOrdinal:    fromEntryOrdinal,
	}
}

// memoizeScan is the one implementation behind every memoized full-transcript
// scan: stat the file, key the memo on its identity plus the divergence
// ordinal, serve a matching memo, and otherwise run scan once outside the lock
// and store the result under that key. The callers differ only in which entry
// field holds their memo, how their value is copied for the caller, and the
// scan itself — those are the parameters; the file-identity gate lives here
// once rather than three times over.
//
// clone, when non-nil, hands each caller its own copy so mutating a returned
// value cannot corrupt the memo others share; pass nil when the value is
// trivially copied already (an int).
//
// scan runs outside c.mu so a slow read does not block other sessions, and the
// result is stored into whatever entry c.entries[path] holds by then, the same
// way loadItemProjectionContext merges into a concurrent parse.
func memoizeScan[T any](
	c *TurnCache,
	path string,
	fromEntryOrdinal int,
	memoOf func(*turnCacheEntry) *scanMemo[T],
	setMemo func(*turnCacheEntry, *scanMemo[T]),
	clone func(T) T,
	scan func() (T, error),
) (T, error) {
	var zero T
	info, err := os.Stat(path)
	if err != nil {
		return zero, fmt.Errorf("stat transcript: %w", err)
	}
	identity := scanMemoIdentity(info, fromEntryOrdinal)
	cloneValue := func(value T) T {
		if clone == nil {
			return value
		}
		return clone(value)
	}

	c.mu.Lock()
	if entry, ok := c.entries[path]; ok {
		if memo := memoOf(&entry); memo != nil && memo.key == identity {
			value := memo.value
			c.touch(path)
			c.mu.Unlock()
			return cloneValue(value), nil
		}
	}
	c.mu.Unlock()

	value, err := scan()
	if err != nil {
		return zero, err
	}

	c.mu.Lock()
	entry := c.entries[path]
	setMemo(&entry, &scanMemo[T]{key: identity, value: value})
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return cloneValue(value), nil
}

// ItemTurnsFromFile returns the cached logical item turns for path when its
// authoritative file metadata matches the cached entry, otherwise parses via
// the package ItemTurnsFromFile and caches the result.
func (c *TurnCache) ItemTurnsFromFile(path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error) {
	return c.itemTurnsFromFileContext(context.Background(), path, maxLineBytes, project)
}

func (c *TurnCache) itemTurnsFromFileContext(ctx context.Context, path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error) {
	return c.loadItemProjectionContext(ctx, path, func() ([]appwire.Turn, error) {
		return itemTurnsFromFileContext(ctx, path, maxLineBytes, project)
	})
}

// loadItemProjection is the cache core, split out so tests can supply a
// counting parse function.
func (c *TurnCache) loadItemProjection(path string, parse func() ([]appwire.Turn, error)) ([]appwire.Turn, error) {
	return c.loadItemProjectionContext(context.Background(), path, parse)
}

func (c *TurnCache) loadItemProjectionContext(ctx context.Context, path string, parse func() ([]appwire.Turn, error)) ([]appwire.Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		// Without a stable identity we can't cache safely; parse uncached.
		return parse()
	}
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if e, ok := c.entries[path]; ok && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) &&
		e.fileIdentity == FileIdentity(fi) && e.changeIdentity == fileChangeIdentity(fi) &&
		e.full {
		c.touch(path)
		c.mu.Unlock()
		return e.turns, nil
	}
	c.mu.Unlock()

	// Parse outside the lock so a slow read doesn't block other sessions.
	turns, err := parse()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if !isContextError(err) {
			c.invalidate(path)
		}
		return nil, err
	}

	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	entry := c.entries[path]
	if entry.size != fi.Size() || !entry.mod.Equal(fi.ModTime()) || entry.fileIdentity != FileIdentity(fi) || entry.changeIdentity != fileChangeIdentity(fi) {
		entry.turns = nil
		entry.full = false
	}
	entry.size = fi.Size()
	entry.mod = fi.ModTime()
	entry.fileIdentity = FileIdentity(fi)
	entry.changeIdentity = fileChangeIdentity(fi)
	entry.turns = turns
	entry.full = true
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return turns, nil
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (c *TurnCache) invalidate(path string) {
	c.mu.Lock()
	delete(c.entries, path)
	for i, candidate := range c.order {
		if candidate == path {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	_ = os.Remove(path + ".appwire-index.json")
	_ = os.Remove(path + ".appwire-index.json.journal")
}

func (c *TurnCache) touch(path string) {
	for i, p := range c.order {
		if p == path {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, path)
}

func (c *TurnCache) evictLocked() {
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}
