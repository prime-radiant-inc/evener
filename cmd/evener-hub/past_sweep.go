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
// A tick does not cost a full pass when nothing changed. SessionTreeStamp folds
// every file either sweep reads (each session's meta.json and transcript) into
// one digest for a readdir per project and an lstat per entry; an unchanged
// digest means neither sweep could find anything new, so the tick skips them.
// Measured on this host's 13k-session tree in steady state (every transcript
// already indexed), the stamp costs 188 ms of CPU where the full pass costs
// 2.69 s, and the tick interval is unchanged, so a change is still seen within
// exactly the bound it was seen in before.
type hubPastSweep struct {
	past  *hubcore.PastIndex
	index *hubcore.MessageSearch

	// stamp reads the tree digest. It is a field so a test can count the ticks
	// that reached the full pass without timing anything; production uses
	// hubcore.SessionTreeStamp.
	stamp func() (uint64, error)
	// lastStamp is the digest the last full pass observed. A full pass records
	// the digest *before* it scans: a write that lands during the pass then
	// makes the next tick differ and re-runs, where recording after the pass
	// could swallow it. Zero until the first pass runs, and a real digest is
	// allowed to be zero, so seeded distinguishes "no pass yet".
	lastStamp uint64
	seeded    bool
	// retry forces the next tick past the gate after a pass the message search
	// could not complete. Refresh retries a failed transcript only on its next
	// call, so skipping the pass after a failed one would strand that retry
	// until unrelated disk activity happened to move the digest.
	retry bool
	// fullPassesCount counts full sweeps, for a test to assert the gate held
	// without timing anything.
	fullPassesCount atomic.Int64
}

// newHubPastSweep pairs the past index with the message-search index.
func newHubPastSweep(past *hubcore.PastIndex, index *hubcore.MessageSearch) *hubPastSweep {
	return &hubPastSweep{
		past:  past,
		index: index,
		stamp: func() (uint64, error) { return hubcore.SessionTreeStamp(past.StateGlob()) },
	}
}

// sweep is one tick's work. It is a no-op when the tree digest is unchanged
// since the last full pass, and otherwise rescans the projects and re-reads the
// transcripts that changed.
func (s *hubPastSweep) sweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	stamp, err := s.stamp()
	if err != nil {
		// A stamp that cannot be taken must not silence the sweeps: a glob
		// error also fails Rebuild, which reports it, so fall through to the
		// full pass and let it say what is wrong.
		componentlog.New("hub").Info("past sweep: session tree stamp", "err", err)
	} else if s.seeded && stamp == s.lastStamp && !s.retry {
		return
	}
	s.fullPass(ctx, stamp)
}

// fullPassCount reports how many full passes this sweep has run.
func (s *hubPastSweep) fullPassCount() int64 { return s.fullPassesCount.Load() }

// fullPass runs both sweeps and records the digest they were run against. It is
// what a tick did before the gate existed, and the startup pass uses it too.
func (s *hubPastSweep) fullPass(ctx context.Context, stamp uint64) {
	s.lastStamp, s.seeded = stamp, true
	s.fullPassesCount.Add(1)
	_, _ = s.past.Rebuild()
	s.retry = refreshHubMessageSearchOnce(ctx, s.index, s.past)
}

// run sweeps every interval until ctx ends. The first pass is not a tick: a hub
// that just started has a cold search index (a fresh or version-dropped
// search.db) that must be filled before the first interval elapses, or nothing
// is searchable until it is.
func (s *hubPastSweep) run(ctx context.Context, interval time.Duration) {
	if ctx.Err() != nil {
		return
	}
	stamp, err := s.stamp()
	if err != nil {
		componentlog.New("hub").Info("past sweep: session tree stamp", "err", err)
	}
	s.fullPass(ctx, stamp)
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
