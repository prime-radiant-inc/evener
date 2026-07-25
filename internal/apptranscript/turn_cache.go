package apptranscript

import (
	"os"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
)

const defaultTurnCacheSize = 32

// TurnCache memoizes TurnsFromFile by path and authoritative file metadata.
// Transcript files are append-only, so matching object identity, size, mtime,
// and platform change time means the parse is unchanged — a cache hit returns
// the previously parsed turns without re-reading and re-projecting the file.
//
// The returned slice is shared and MUST be treated as read-only by callers
// (WindowTurns/PageTurns slice it without mutating elements). A cache instance
// assumes a single EntryProjector, so give each call site its own cache.
type TurnCache struct {
	mu      sync.Mutex
	indexMu sync.Mutex // serializes suffix advancement and journal appends
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
	turnIndex      *turnIndexDisk
	// toolResolver is private scanning state. indexMu protects it; published
	// turnIndex snapshots never reference this mutable map.
	toolResolver map[string]string
}

// NewTurnCache returns a TurnCache bounded to a default number of transcripts.
func NewTurnCache() *TurnCache {
	return &TurnCache{entries: map[string]turnCacheEntry{}, max: defaultTurnCacheSize}
}

// TurnsFromFile returns the cached turns for path when its size and modtime
// match the cached entry, otherwise parses via the package TurnsFromFile and
// caches the result.
func (c *TurnCache) TurnsFromFile(path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error) {
	return c.load(path, func() ([]appwire.Turn, error) {
		return TurnsFromFile(path, maxLineBytes, project)
	})
}

// load is the cache core, split out so tests can supply a counting parse fn.
func (c *TurnCache) load(path string, parse func() ([]appwire.Turn, error)) ([]appwire.Turn, error) {
	fi, err := os.Stat(path)
	if err != nil {
		// Without a stable identity we can't cache safely; parse uncached.
		return parse()
	}
	c.mu.Lock()
	if e, ok := c.entries[path]; ok && e.full && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) &&
		e.fileIdentity == fileIdentity(fi) && e.changeIdentity == fileChangeIdentity(fi) {
		c.touch(path)
		turns := e.turns
		c.mu.Unlock()
		return turns, nil
	}
	c.mu.Unlock()

	// Parse outside the lock so a slow read doesn't block other sessions.
	turns, err := parse()
	if err != nil {
		c.invalidate(path)
		return nil, err
	}

	c.mu.Lock()
	entry := c.entries[path]
	entry.size = fi.Size()
	entry.mod = fi.ModTime()
	entry.fileIdentity = fileIdentity(fi)
	entry.changeIdentity = fileChangeIdentity(fi)
	entry.turns = turns
	entry.full = true
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return turns, nil
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
