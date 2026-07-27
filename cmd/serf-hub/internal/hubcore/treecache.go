package hubcore

import (
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/serf/appwire"
)

// treeBucketSeconds is the wall-clock granularity at which a fresh tree is
// recomputed even without an inputs change, so relative ages and 24h/2wk tier
// boundaries advance. The memoized tree carries Age strings up to one bucket
// stale (round-2 A6); the web renders ages client-side and ignores them.
const treeBucketSeconds = 30

// InputsVersion is a monotonically increasing counter bumped whenever an input
// to the tree changes (past-index content delta, roster membership/state delta,
// archive/favorite writes, attention poke). The TreeCache keys on it.
type InputsVersion struct{ v atomic.Uint64 }

func (iv *InputsVersion) Bump()        { iv.v.Add(1) }
func (iv *InputsVersion) Load() uint64 { return iv.v.Load() }

// TreeCacheKey keeps independently advancing tree inputs distinct. A struct
// key avoids collisions that can occur when multiple versions are folded into
// one arithmetic value.
type TreeCacheKey struct {
	InputsVersion    uint64
	RemoteGeneration uint64
}

// TreeCacheValue is the read-only result of one navigation snapshot. Callers
// must treat its slices and nested values as immutable; the cache owns the
// stored generation and all consumers only read it during response shaping.
type TreeCacheValue struct {
	Tree              Tree
	AttentionSummary  appwire.AttentionSummary
	Live              []LiveEntry
	FavoriteAuthority FavoriteAuthority
}

// TreeCache memoizes one complete navigation value per (inputs-version, remote
// generation, 30s time bucket). The cached value owns the tree, attention,
// live-entry, and favorite-authority generation; only response formatting and
// other presentation-only shaping happen post-memo.
type TreeCache struct {
	mu     sync.Mutex
	valid  bool
	key    TreeCacheKey
	bucket int64
	value  TreeCacheValue
}

// Get returns the memoized navigation value, recomputing via compute only when
// the inputs version, remote generation, or 30s time bucket has changed.
func (c *TreeCache) Get(key TreeCacheKey, now time.Time, compute func() TreeCacheValue) TreeCacheValue {
	bucket := now.Unix() / treeBucketSeconds
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && c.key == key && c.bucket == bucket {
		return c.value
	}
	c.value, c.key, c.bucket, c.valid = compute(), key, bucket, true
	return c.value
}
