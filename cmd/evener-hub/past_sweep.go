package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/componentlog"
)

// hubPastSweep keeps the hub's two session-derived indexes in step with disk:
// the past index's full rebuild and the message-search refresh that reads the
// sessions it lists. They share one input set — the projects' sessions
// directories — and one interval, so a change is still seen within the bound it
// was seen in before.
//
// They do not share a goroutine. A refresh pass reads every transcript the
// index owes, and on a cold search.db that is the whole tree: 6.9 s on this
// host's 13k-session tree with every read refused before one started, and
// longer when it reads them. One goroutine running the rebuild and then the
// refresh made the past index's next scan wait behind that whole pass, so a hub
// that dropped its search.db stopped seeing new sessions for its duration
// (#6652). Only the first pass is ordered, and it has to be: the refresh cannot
// list sessions until a rebuild has populated the index, because an empty list
// reads as "every indexed session is gone".
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
type hubPastSweep struct {
	past  *hubcore.PastIndex
	index *hubcore.MessageSearch

	// refresh is one message-search pass. It is a field so a test can hold a
	// pass open and show that the rebuild does not wait behind it.
	refresh func(ctx context.Context)

	// lastStamp is the digest a rebuild that covered the tree observed. The
	// digest is taken *before* the scan: a write that lands during the scan then
	// makes the next tick differ and re-runs, where taking it after the scan
	// could swallow it. Zero until a rebuild covers the tree, and a real digest
	// is allowed to be zero, so seeded distinguishes "nothing recorded yet".
	//
	// It is recorded only when the rebuild covered the tree (see rebuild): a
	// rebuild that swapped nothing, or that dropped a session it could not
	// read, leaves the index short of the tree by an amount this digest cannot
	// express. The digest folds a directory's entries and their stats, so it
	// does record a sessions dir that disappears or goes unreadable
	// (foldSessionDirStamp folds an unreadable marker for it); what it cannot
	// see is an abort, or a per-file listing or read failure underneath a
	// readdir that succeeded. Recording the digest then would make every later
	// tick on a quiet tree skip exactly the re-scan the incomplete rebuild
	// needs.
	lastStamp uint64
	seeded    bool
	// rebuildsCount counts full rebuilds, for a test to assert the gate held
	// without timing anything.
	rebuildsCount atomic.Int64
}

// newHubPastSweep pairs the past index with the message-search index.
func newHubPastSweep(past *hubcore.PastIndex, index *hubcore.MessageSearch) *hubPastSweep {
	return &hubPastSweep{
		past:    past,
		index:   index,
		refresh: func(ctx context.Context) { refreshHubMessageSearchOnce(ctx, index, past) },
	}
}

// sessionTreeStamp digests the on-disk state the rebuild reads.
func (s *hubPastSweep) sessionTreeStamp() (uint64, error) {
	return hubcore.SessionTreeStamp(s.past.StateGlob())
}

// sweepRebuild is the rebuild half of a tick: rebuild the past index when the
// tree digest moved.
func (s *hubPastSweep) sweepRebuild(ctx context.Context) {
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
}

// sweepRefresh is the refresh half of a tick: one ungated message-search pass.
func (s *hubPastSweep) sweepRefresh(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.refresh(ctx)
}

// rebuildCount reports how many full rebuilds this sweep has run.
func (s *hubPastSweep) rebuildCount() int64 { return s.rebuildsCount.Load() }

// rebuild rescans the projects and records the digest the scan was taken
// against, but only once the scan has covered the tree. An incomplete rebuild
// leaves the digest unrecorded, so the next tick takes it again and re-scans;
// that is the whole of what a tick did before the gate existed, paid only by a
// tick whose rebuild the tree still owes work to.
func (s *hubPastSweep) rebuild(stamp uint64) {
	s.rebuildsCount.Add(1)
	_, _ = s.past.Rebuild()
	if s.past.RebuildCoveredTree() {
		s.lastStamp, s.seeded = stamp, true
		return
	}
	// Close the gate outright. Leaving a previous rebuild's digest in place
	// would let the next tick on a quiet tree compare equal and skip the
	// re-scan this incomplete rebuild still owes; clearing seeded forces that
	// tick to rebuild whatever the digest says, and a rebuild that then covers
	// the tree records a fresh digest and lets the gate close again.
	s.seeded = false
}

// run keeps both indexes in step with disk until ctx ends, each on its own
// loop. The first pass is not a tick: a hub that just started has a cold search
// index (a fresh or version-dropped search.db) that must be filled before the
// first interval elapses, or nothing is searchable until it is.
func (s *hubPastSweep) run(ctx context.Context, interval time.Duration) {
	if ctx.Err() != nil {
		return
	}
	firstRebuild := make(chan struct{})
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		s.runRebuilds(ctx, interval, firstRebuild)
	}()
	go func() {
		defer loops.Done()
		s.runRefreshes(ctx, interval, firstRebuild)
	}()
	loops.Wait()
}

// runRebuilds rebuilds on every interval until ctx ends, and closes
// firstPassDone once the index names sessions.
func (s *hubPastSweep) runRebuilds(ctx context.Context, interval time.Duration, firstPassDone chan struct{}) {
	s.sweepRebuild(ctx)
	close(firstPassDone)
	ticks, stop := hubTicker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sweepRebuild(ctx)
		}
	}
}

// runRefreshes refreshes on every interval until ctx ends, waiting for the
// rebuild's first pass so an empty index cannot read as "every indexed session
// was deleted".
func (s *hubPastSweep) runRefreshes(ctx context.Context, interval time.Duration, firstPassDone <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-firstPassDone:
	}
	s.sweepRefresh(ctx)
	ticks, stop := hubTicker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sweepRefresh(ctx)
		}
	}
}
