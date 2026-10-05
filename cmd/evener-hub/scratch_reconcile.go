package hub

import (
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// reconcileArchivedScratch removes the scratch tree of every session the rail
// files as archived, by any rule (an explicit decision, age, or an archived
// project; pastArchived), unless the session's daemon is still running. An
// explicit archive removes its session's tree at once; this pass catches the
// rest: sessions that aged into the archive, sessions in an archived project,
// and sessions whose daemon exited while the hub was down.
func reconcileArchivedScratch(cfg hubcore.WebConfig, now time.Time) {
	if cfg.Past == nil || cfg.Archive == nil {
		return
	}
	decisions, err := cfg.Archive.Decisions()
	if err != nil {
		return
	}
	for _, entry := range cfg.Past.All() {
		// A daemon that died stays listed, marked Crashed; only a running one
		// keeps its session's scratch.
		if cfg.Roster != nil {
			if live, listed := cfg.Roster.Find(entry.ID); listed && !live.Crashed {
				continue
			}
		}
		if pastArchived(entry, decisions, now) {
			removeArchivedSessionScratch(entry.ID)
		}
	}
}

// scratchReconciler runs reconcileArchivedScratch off the caller's path, one
// pass at a time. A kick that lands during a pass runs one more pass after it,
// so no archive the kick was for is missed.
type scratchReconciler struct {
	cfg hubcore.WebConfig

	mu      sync.Mutex
	running bool
	again   bool
}

func newScratchReconciler(cfg hubcore.WebConfig) *scratchReconciler {
	return &scratchReconciler{cfg: cfg}
}

// Kick asks for a reconcile pass.
func (r *scratchReconciler) Kick() {
	r.mu.Lock()
	if r.running {
		r.again = true
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go func() {
		for {
			reconcileArchivedScratch(r.cfg, time.Now())
			r.mu.Lock()
			if !r.again {
				r.running = false
				r.mu.Unlock()
				return
			}
			r.again = false
			r.mu.Unlock()
		}
	}()
}
