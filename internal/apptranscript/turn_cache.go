package apptranscript

import (
	"os"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
)

const defaultTurnCacheSize = 32

// TurnCache memoizes TurnsFromFile by file identity (path + size + modtime).
// Transcript files are append-only, so a matching size+modtime means the parse
// is unchanged — a cache hit returns the previously parsed turns without
// re-reading and re-projecting the whole file. This makes repeated reads of one
// session (lazy paging scrolls back through its transcript, re-requesting it per
// page) O(1) instead of re-parsing the entire transcript each time.
//
// The returned slice is shared and MUST be treated as read-only by callers
// (WindowTurns/PageTurns slice it without mutating elements). A cache instance
// assumes a single EntryProjector, so give each call site its own cache.
type TurnCache struct {
	mu      sync.Mutex
	entries map[string]turnCacheEntry
	order   []string // least-recently-used first, for bounded eviction
	max     int
}

type turnCacheEntry struct {
	size  int64
	mod   time.Time
	turns []appwire.Turn
}

// NewTurnCache returns a TurnCache bounded to a default number of transcripts.
func NewTurnCache() *TurnCache {
	return &TurnCache{entries: map[string]turnCacheEntry{}, max: defaultTurnCacheSize}
}

// TurnsFromFile returns the cached turns for path when its size and modtime
// match the cached entry, otherwise parses via the package TurnsFromFile and
// caches the result.
func (c *TurnCache) TurnsFromFile(path string, maxLineBytes int, project EntryProjector) []appwire.Turn {
	return c.load(path, func() []appwire.Turn {
		return TurnsFromFile(path, maxLineBytes, project)
	})
}

// load is the cache core, split out so tests can supply a counting parse fn.
func (c *TurnCache) load(path string, parse func() []appwire.Turn) []appwire.Turn {
	fi, err := os.Stat(path)
	if err != nil {
		// Without a stable identity we can't cache safely; parse uncached.
		return parse()
	}
	c.mu.Lock()
	if e, ok := c.entries[path]; ok && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
		c.touch(path)
		turns := e.turns
		c.mu.Unlock()
		return turns
	}
	c.mu.Unlock()

	// Parse outside the lock so a slow read doesn't block other sessions.
	turns := parse()

	c.mu.Lock()
	c.entries[path] = turnCacheEntry{size: fi.Size(), mod: fi.ModTime(), turns: turns}
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return turns
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
