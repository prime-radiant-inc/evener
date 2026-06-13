package agent

import "sync/atomic"

// treeCounter is a tree-wide running count of active delegate turns. It is
// created once by the root session and handed down through spawnConfig so
// every session in the tree shares the same atomic counter.
//
// The cap is fixed at 16 (spec §4). reserve/release are called by the paths
// that create or terminate running delegate turns — that wiring is Task 16.
// This file is the DORMANT SCAFFOLD; the counter exists but is not yet called
// from any production path.
type treeCounter struct {
	n   atomic.Int64
	cap int64
}

// newTreeCounter returns a treeCounter with cap 16.
func newTreeCounter() *treeCounter {
	return &treeCounter{cap: 16}
}

// reserve atomically increments the counter if below cap. Returns true if the
// slot was claimed, false if the tree is already at capacity.
func (c *treeCounter) reserve() bool {
	for {
		cur := c.n.Load()
		if cur >= c.cap {
			return false
		}
		if c.n.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// release decrements the counter, returning a slot to the tree budget.
func (c *treeCounter) release() {
	c.n.Add(-1)
}
