package agent

import (
	"errors"
	"sync/atomic"
)

// errTreeAtCapacity is returned when a spawn or resume cannot claim a tree-counter
// slot because the session tree already holds the cap (16) of concurrently running
// delegate turns (spec §4). The text drops the "you are notified automatically"
// phrasing — at saturation that is untrue; completions free slots and the caller
// must retry. The exact text is spec-mandated and asserted by tests, including the
// trailing period, so ST1005's no-trailing-punctuation rule is suppressed here.
//
//nolint:staticcheck // ST1005: model-facing text is spec-pinned, period included.
var errTreeAtCapacity = errors.New("tree_at_capacity: 16 delegate jobs running across this session tree. " +
	"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.")

// treeCounter is a tree-wide running count of active delegate turns. It is
// created once by the root session and handed down through spawnConfig so
// every session in the tree shares the same atomic counter.
//
// The cap is fixed at 16 (spec §4). reserve/release are called by the paths
// that create or terminate running delegate turns: the spawn paths
// (reserveTreeSlot in subagents.go), the drive/delegate path (job_delegate.go),
// and the finalize/abandon release paths (jobs.go).
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

// reserveTreeSlot claims a tree-counter slot for a running delegate turn on this
// session's tree (spec §4). It returns a treeReservation and true on success, or
// nil and false when the tree is at capacity. A session with no counter (never
// minted) is unbounded and always succeeds with a nil reservation that releases
// to a no-op.
func (s *Session) reserveTreeSlot() (*treeReservation, bool) {
	if s == nil || s.treeCounter == nil {
		return nil, true
	}
	if !s.treeCounter.reserve() {
		return nil, false
	}
	return &treeReservation{counter: s.treeCounter}, true
}

// releasePreparedTreeSlot returns the tree-counter slot a prepared spawn reserved
// when the prepared run is discarded before its reservation transfers to a
// delegate runningJob — an error path between prepare and attach, or the legacy
// in-process spawn that mints no delegate job. Idempotent and nil-safe.
func releasePreparedTreeSlot(prepared *preparedSubagentRun) {
	if prepared == nil {
		return
	}
	prepared.treeSlot.release()
	prepared.treeSlot = nil
}
