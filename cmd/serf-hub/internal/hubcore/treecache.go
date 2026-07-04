package hubcore

import (
	"sync"
	"sync/atomic"
	"time"
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

// TreeCache memoizes one (Tree, AttentionSummary) per (inputs-version, 30s time
// bucket). Response shaping and volatile derivations happen post-memo.
type TreeCache struct {
	mu      sync.Mutex
	valid   bool
	version uint64
	bucket  int64
	tree    Tree
	summary AttentionSummary
}

// Get returns the memoized value, recomputing via compute only when the inputs
// version or the 30s time bucket has changed.
func (c *TreeCache) Get(version uint64, now time.Time, compute func() (Tree, AttentionSummary)) (Tree, AttentionSummary) {
	bucket := now.Unix() / treeBucketSeconds
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && c.version == version && c.bucket == bucket {
		return c.tree, c.summary
	}
	tree, sum := compute()
	c.tree, c.summary, c.version, c.bucket, c.valid = tree, sum, version, bucket, true
	return tree, sum
}
