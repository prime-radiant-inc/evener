package agent

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// defaultMaxConcurrentDelegateTurns is the default tree-wide cap on
// concurrently running delegate turns (spec §4). Configurable per root session
// via SessionConfig.MaxConcurrentDelegateTurns.
const defaultMaxConcurrentDelegateTurns = 50

// defaultMaxConcurrentDriveTurns caps concurrently running drive-down
// notification turns tree-wide. Drives budget separately from spawns so
// notification maintenance can never starve user fan-out (and vice versa).
const defaultMaxConcurrentDriveTurns = 8

// errTreeAtCapacity is the sentinel matched by errors.Is when a spawn or
// resume cannot claim a tree-counter slot. The model-facing text is formatted
// by treeCapacityError with the live cap; the "tree_at_capacity" prefix token
// is preserved for matchers, including the trailing period, so ST1005's
// no-trailing-punctuation rule is suppressed here.
//
//nolint:staticcheck // ST1005: model-facing text is spec-pinned, period included.
var errTreeAtCapacity = errors.New("tree_at_capacity")

// treeCapacityError is the formatted spawn/resume failure at tree capacity.
// It carries the live cap and the job/drive occupancy split so the error names
// the configured limit and what is holding it, rather than a hardcoded one.
type treeCapacityError struct {
	cap    int64
	jobs   int64
	drives int64
}

func (e *treeCapacityError) Error() string {
	return fmt.Sprintf("tree_at_capacity: %d delegate turn slots in use across this session tree (%d delegate jobs, %d drive turns). "+
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.", e.cap, e.jobs, e.drives)
}

// Is lets errors.Is(err, errTreeAtCapacity) match the formatted error.
func (e *treeCapacityError) Is(target error) bool { return target == errTreeAtCapacity }

// treeCounter is a tree-wide running count of active delegate turns. It is
// created once by the root session and handed down through spawnConfig so
// every session in the tree shares the same atomic counter.
//
// The cap is configured per root session (default 50, spec §4). reserve/release
// are called by the paths that create or terminate running delegate turns: the
// spawn paths (reserveTreeSlot in subagents.go), the drive/delegate path
// (job_delegate.go), and the finalize/abandon release paths (jobs.go).
// slotKind identifies what holds a tree-counter slot: a running delegate job
// turn (spawn/resume) or a drive-down notification turn. The split exists for
// diagnostics: a saturated tree with zero running jobs is visibly drive-held.
type slotKind int

const (
	slotKindJob slotKind = iota
	slotKindDrive
)

type treeCounter struct {
	n      atomic.Int64
	jobs   atomic.Int64
	drives atomic.Int64
	cap    int64
}

// newTreeCounter returns a treeCounter with the given cap; cap <= 0 selects
// the default (defaultMaxConcurrentDelegateTurns).
func newTreeCounter(cap int64) *treeCounter {
	if cap <= 0 {
		cap = defaultMaxConcurrentDelegateTurns
	}
	return &treeCounter{cap: cap}
}

// reserve atomically increments the counter if below cap, attributing the slot
// to kind. Returns true if the slot was claimed, false if the tree is already
// at capacity.
func (c *treeCounter) reserve(kind slotKind) bool {
	for {
		cur := c.n.Load()
		if cur >= c.cap {
			return false
		}
		if c.n.CompareAndSwap(cur, cur+1) {
			c.kindCounter(kind).Add(1)
			return true
		}
	}
}

// releaseKind decrements the counter and its per-kind tally, returning a slot
// to the tree budget. Both floors clamp at zero so a stray double release
// cannot drive occupancy negative.
func (c *treeCounter) releaseKind(kind slotKind) {
	decClamped(&c.n)
	decClamped(c.kindCounter(kind))
}

func (c *treeCounter) kindCounter(kind slotKind) *atomic.Int64 {
	if kind == slotKindDrive {
		return &c.drives
	}
	return &c.jobs
}

func decClamped(v *atomic.Int64) {
	for {
		cur := v.Load()
		if cur <= 0 {
			return
		}
		if v.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// occupancy reports the current slot usage split by holder kind plus the cap.
// Reads are approximate under concurrent reserve/release; the tuple is
// diagnostic, not authoritative.
func (c *treeCounter) occupancy() (total, jobs, drives, cap int64) {
	return c.n.Load(), c.jobs.Load(), c.drives.Load(), c.cap
}

// reserveTreeSlot claims a tree-counter slot for a running delegate turn on this
// session's tree (spec §4), attributed to kind (job turn vs drive turn). It
// returns a treeReservation and true on success, or nil and false when the tree
// is at capacity. A session with no counter (never minted) is unbounded and
// always succeeds with a nil reservation that releases to a no-op.
func (s *Session) reserveTreeSlot(kind slotKind) (*treeReservation, bool) {
	if s == nil || s.treeCounter == nil {
		return nil, true
	}
	if !s.treeCounter.reserve(kind) {
		return nil, false
	}
	return &treeReservation{counter: s.treeCounter, kind: kind}, true
}

// treeCapacityErrorFor formats the capacity failure for this session's tree
// from the counter's live occupancy.
func (s *Session) treeCapacityErrorFor() error {
	_, jobs, drives, cap := s.treeCounter.occupancy()
	return &treeCapacityError{cap: cap, jobs: jobs, drives: drives}
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

// reserveDriveSlot claims a drive-budget slot for a drive-down notification
// turn, mirroring reserveTreeSlot against the session's driveCounter. A
// session with no drive counter (never minted) is unbounded and always
// succeeds with a nil reservation that releases to a no-op.
func (s *Session) reserveDriveSlot() (*treeReservation, bool) {
	if s == nil || s.driveCounter == nil {
		return nil, true
	}
	if !s.driveCounter.reserve(slotKindDrive) {
		return nil, false
	}
	return &treeReservation{counter: s.driveCounter, kind: slotKindDrive}, true
}
