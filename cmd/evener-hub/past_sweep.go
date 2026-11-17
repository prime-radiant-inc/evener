package hub

import (
	"context"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/componentlog"
)

// hubPastSweep keeps the hub's two session-derived indexes in step with disk:
// the past index's full rebuild and the message-search refresh that reads the
// sessions it lists. They run on one ticker because they share one input set —
// the projects' sessions directories — and because the refresh has to read the
// rebuilt index to know which transcripts it covers.
//
// Only the rebuild is gated on the tree digest. SessionTreeStamp folds every
// file the rebuild reads (each session's meta.json) into one digest for a
// readdir per project and an lstat per entry, so an unchanged digest means the
// rebuild could not find anything and the tick skips it. The refresh runs every
// tick regardless: what it defers is not visible in the digest at all. A
// transcript whose sidecar this projection has never built is refused with
// ErrRebuildRequired, a contended one with ErrLockBusy, a paging race with a
// stale cursor — each defers the session for the cycle without recording it,
// and the repair that clears the first case writes only the excluded
// .index-<projection> directory. A digest gate over the refresh therefore has
// no fact to read for "a session still owes a read", and a tree that stopped
// moving would strand every such session.
//
// Refresh is cheap to run when nothing changed: it compares each transcript's
// size and modification time against the stamp it recorded and reads only what
// moved, which is the same pair the tree digest folds. So the ungated half
// costs a stat sweep, not a read of every transcript.
//
// The tick interval is unchanged, so a change is still seen within exactly the
// bound it was seen in before.
type hubPastSweep struct {
	past  *hubcore.PastIndex
	index *hubcore.MessageSearch

	// lastStamp is the digest the last rebuild observed. A rebuild records the
	// digest *before* it scans: a write that lands during the scan then makes
	// the next tick differ and re-runs, where recording after the scan could
	// swallow it. Zero until the first rebuild runs, and a real digest is
	// allowed to be zero, so seeded distinguishes "no rebuild yet".
	lastStamp uint64
	seeded    bool
	// rebuildsCount counts full rebuilds, for a test to assert the gate held
	// without timing anything.
	rebuildsCount atomic.Int64
}

// newHubPastSweep pairs the past index with the message-search index.
func newHubPastSweep(past *hubcore.PastIndex, index *hubcore.MessageSearch) *hubPastSweep {
	return &hubPastSweep{past: past, index: index}
}

// sessionTreeStamp digests the on-disk state the rebuild reads.
func (s *hubPastSweep) sessionTreeStamp() (uint64, error) {
	return hubcore.SessionTreeStamp(s.past.StateGlob())
}

// sweep is one tick's work: rebuild the past index when the tree digest moved,
// then refresh the message search unconditionally.
func (s *hubPastSweep) sweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	stamp, err := s.sessionTreeStamp()
	if err != nil {
		// A stamp that cannot be taken must not silence the rebuild: a glob
		// error also fails Rebuild, which reports it, so fall through to the
		// rebuild and let it say what is wrong.
		componentlog.New("hub").Info("past sweep: session tree stamp", "err", err)
	}
	if !s.seeded || err != nil || stamp != s.lastStamp {
		s.rebuild(stamp)
	}
	refreshHubMessageSearchOnce(ctx, s.index, s.past)
}

// rebuildCount reports how many full rebuilds this sweep has run.
func (s *hubPastSweep) rebuildCount() int64 { return s.rebuildsCount.Load() }

// rebuild rescans the projects and records the digest the scan was taken
// against. It is the whole of what a tick did before the gate existed.
func (s *hubPastSweep) rebuild(stamp uint64) {
	s.lastStamp, s.seeded = stamp, true
	s.rebuildsCount.Add(1)
	_, _ = s.past.Rebuild()
}

// run sweeps every interval until ctx ends. The first pass is not a tick: a hub
// that just started has a cold search index (a fresh or version-dropped
// search.db) that must be filled before the first interval elapses, or nothing
// is searchable until it is.
func (s *hubPastSweep) run(ctx context.Context, interval time.Duration) {
	if ctx.Err() != nil {
		return
	}
	stamp, err := s.sessionTreeStamp()
	if err != nil {
		componentlog.New("hub").Info("past sweep: session tree stamp", "err", err)
	}
	s.rebuild(stamp)
	refreshHubMessageSearchOnce(ctx, s.index, s.past)
	ticks, stop := hubTicker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sweep(ctx)
		}
	}
}
