package hub

import (
	"context"
	"slices"
	"time"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// reconcileArchivedScratch removes the scratch tree of every session the rail
// files as archived, by any rule (an explicit decision, age, or an archived
// project; pastArchived), unless the session's daemon is still running. It runs
// after every archive and daemon exit and at startup, so it also catches
// sessions that aged into the archive and daemons that exited while the hub
// was down. It visits only the trees that exist, not every recorded session.
func reconcileArchivedScratch(cfg hubcore.WebConfig, now time.Time) {
	if cfg.Past == nil || cfg.Archive == nil {
		return
	}
	decisions, err := cfg.Archive.Decisions()
	if err != nil {
		return
	}
	daemonTempDirs := recordedScratchTempDirs(cfg.Past)
	for _, id := range agentsandbox.SessionScratchTreeRootIDs(daemonTempDirs...) {
		entry, known := cfg.Past.FindIndexed(id)
		if !known {
			continue
		}
		// A daemon that died stays listed, marked Crashed; only a running one
		// keeps its session's scratch.
		if cfg.Roster != nil {
			if live, listed := cfg.Roster.Find(id); listed && !live.Crashed {
				continue
			}
		}
		// Best-effort: a tree it cannot take now is retried on the next pass.
		if pastArchived(entry, decisions, now) {
			_ = agentsandbox.RemoveSessionScratchTree(id, daemonTempDirs...)
		}
	}
}

// recordedScratchTempDirs is each distinct temp dir the past sessions' daemons
// recorded (SessionMeta.ScratchTempDir). A daemon started with its own TMPDIR
// keeps its scratch there, out of the hub's own temp dir.
func recordedScratchTempDirs(past *hubcore.PastIndex) []string {
	var dirs []string
	for _, entry := range past.All() {
		if dir := entry.Meta.ScratchTempDir; dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// scratchReconciler runs reconcileArchivedScratch on its own goroutine, one
// pass at a time. Kicks that land during a pass collapse into one more pass.
type scratchReconciler struct {
	cfg  hubcore.WebConfig
	kick chan struct{}
}

func newScratchReconciler(cfg hubcore.WebConfig) *scratchReconciler {
	return &scratchReconciler{cfg: cfg, kick: make(chan struct{}, 1)}
}

// Kick asks for a reconcile pass without waiting for it.
func (r *scratchReconciler) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run serves kicks until ctx ends.
func (r *scratchReconciler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
			reconcileArchivedScratch(r.cfg, time.Now())
		}
	}
}
