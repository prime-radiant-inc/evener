package hub

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"
)

// navigationRepresentation is immutable after it is returned by a cache
// builder. Object, JSON, and Gzip are shared between callers and MUST NOT be
// mutated. One representation owns both wire encodings so encoding is not a
// cache-key dimension.
type navigationRepresentation struct {
	Object       any
	JSON         []byte
	Gzip         []byte
	ETag         string
	Generation   string
	Revision     uint64
	SizeEstimate int64
}

type navigationCacheStats struct {
	Entries   int
	Bytes     int64
	Hits      uint64
	Misses    uint64
	Coalesced uint64
	Evictions uint64
}

type navigationCacheEntry struct {
	key            navigationResourceKey
	representation navigationRepresentation
}

type navigationCacheFlight struct{}

type navigationRepresentationCache struct {
	mu         sync.Mutex
	entries    map[navigationResourceKey]*list.Element
	order      *list.List // most-recently-used at the front
	bytes      int64
	maxEntries int
	maxBytes   int64

	hits      uint64
	misses    uint64
	coalesced uint64
	evictions uint64

	flights map[string]*navigationCacheFlight
	group   singleflight.Group
}

func newNavigationRepresentationCache(maxEntries int, maxBytes int64) *navigationRepresentationCache {
	if maxEntries < 0 {
		maxEntries = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	return &navigationRepresentationCache{
		entries:    make(map[navigationResourceKey]*list.Element),
		order:      list.New(),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		flights:    make(map[string]*navigationCacheFlight),
	}
}

// Get returns one immutable representation for key. A cold owner is counted as
// a miss; callers joining that owner's build are counted as coalesced. A
// waiting caller's context only controls that caller's wait and never cancels
// the owner build. The owner's context is passed to build.
func (c *navigationRepresentationCache) Get(
	ctx context.Context,
	key navigationResourceKey,
	build func(context.Context) (navigationRepresentation, error),
) (navigationRepresentation, error) {
	if ctx == nil {
		return navigationRepresentation{}, errors.New("navigation cache: nil context")
	}
	if build == nil {
		return navigationRepresentation{}, errors.New("navigation cache: nil build function")
	}

	key = key.canonical()
	flightKey := key.String()

	c.mu.Lock()
	if element, ok := c.entries[key]; ok {
		c.order.MoveToFront(element)
		c.hits++
		representation := element.Value.(navigationCacheEntry).representation
		c.mu.Unlock()
		return representation, nil
	}
	if _, ok := c.flights[flightKey]; ok {
		c.coalesced++
	} else {
		c.flights[flightKey] = &navigationCacheFlight{}
		c.misses++
	}
	c.mu.Unlock()

	result := c.group.DoChan(flightKey, func() (any, error) {
		representation, err := build(ctx)
		if err == nil {
			if representation.SizeEstimate < 0 {
				err = errors.New("navigation cache: negative size estimate")
			} else {
				// The ETag is owned by the cache boundary, ensuring every
				// representation uses the exact weak-tag contract.
				representation.ETag = navigationETag(key, representation.Generation, representation.Revision)
				c.publish(key, representation)
			}
		}
		c.finishFlight(flightKey)
		if err != nil {
			return nil, err
		}
		return representation, nil
	})

	select {
	case <-ctx.Done():
		return navigationRepresentation{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return navigationRepresentation{}, result.Err
		}
		return result.Val.(navigationRepresentation), nil
	}
}

func (c *navigationRepresentationCache) publish(key navigationResourceKey, representation navigationRepresentation) {
	if representation.SizeEstimate > c.maxBytes || c.maxEntries == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// A representation is only inserted when its size can fit after removing
	// older entries. This subtraction form is overflow-safe even at MaxInt64.
	for c.order.Len() >= c.maxEntries || representation.SizeEstimate > c.maxBytes-c.bytes {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(navigationCacheEntry)
		delete(c.entries, entry.key)
		c.order.Remove(oldest)
		c.bytes -= entry.representation.SizeEstimate
		c.evictions++
	}

	entry := navigationCacheEntry{key: key, representation: representation}
	element := c.order.PushFront(entry)
	c.entries[key] = element
	c.bytes += representation.SizeEstimate
}

func (c *navigationRepresentationCache) finishFlight(flightKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Forget before releasing the cache lock. This closes the small failure
	// window between the builder returning and singleflight deleting its call;
	// a subsequent caller can safely start a fresh build after this point.
	c.group.Forget(flightKey)
	delete(c.flights, flightKey)
}

func (c *navigationRepresentationCache) Stats() navigationCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return navigationCacheStats{
		Entries:   c.order.Len(),
		Bytes:     c.bytes,
		Hits:      c.hits,
		Misses:    c.misses,
		Coalesced: c.coalesced,
		Evictions: c.evictions,
	}
}

// canonical returns the representation identity after applying the same
// effective limits as the projector. Fields irrelevant to a resource kind are
// cleared so equivalent route forms share one entry.
func (key navigationResourceKey) canonical() navigationResourceKey {
	canonical := navigationResourceKey{
		Kind:       key.Kind,
		Generation: key.Generation,
		Revision:   key.Revision,
	}
	switch key.Kind {
	case navigationResourceManifest:
		return canonical
	case navigationResourceLive, navigationResourceNeedsYou:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourcePinCatalog:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationCatalogRows)
	case navigationResourcePinSection:
		canonical.SectionID = key.SectionID
		if canonical.SectionID == "" {
			canonical.SectionID = key.ID
		}
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationCatalogRows)
	case navigationResourceProject:
		canonical.ProjectKey = key.ProjectKey
	case navigationResourceProjectPage:
		canonical.ProjectKey = key.ProjectKey
		canonical.Tier = key.Tier
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourceLocation:
		canonical.ID = key.ID
	default:
		return key
	}
	return canonical
}

func canonicalNavigationLimit(limit, maximum uint32) uint32 {
	if limit == 0 || limit > maximum {
		return maximum
	}
	return limit
}

// String is a collision-safe canonical encoding. JSON supplies explicit field
// boundaries and deterministic struct-field ordering; raw IDs never become
// delimiters or metric labels.
func (key navigationResourceKey) String() string {
	key = key.canonical()
	identity := struct {
		Kind       navigationResourceKind `json:"kind"`
		ID         string                 `json:"id,omitempty"`
		SectionID  string                 `json:"section_id,omitempty"`
		ProjectKey string                 `json:"project_key,omitempty"`
		Tier       string                 `json:"tier,omitempty"`
		Offset     uint32                 `json:"offset,omitempty"`
		Limit      uint32                 `json:"limit,omitempty"`
		Generation string                 `json:"generation"`
		Revision   uint64                 `json:"revision"`
	}{
		Kind: key.Kind, ID: key.ID, SectionID: key.SectionID,
		ProjectKey: key.ProjectKey, Tier: key.Tier, Offset: key.Offset,
		Limit: key.Limit, Generation: key.Generation, Revision: key.Revision,
	}
	encoded, _ := json.Marshal(identity)
	return string(encoded)
}

func navigationETag(key navigationResourceKey, generation string, revision uint64) string {
	return fmt.Sprintf(`W/"nav-%s-%x-%d"`, generation, sha256.Sum256([]byte(key.String())), revision)
}
