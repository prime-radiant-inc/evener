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
func reconcileArchivedScratch(past *hubcore.PastIndex, archive *hubcore.ArchiveStore, roster *hubcore.Roster, now time.Time) {
	if past == nil || archive == nil {
		return
	}
	decisions, err := archive.Decisions()
	if err != nil {
		return
	}
	daemonTempDirs := recordedScratchTempDirs(past)
	for _, id := range agentsandbox.SessionScratchTreeRootIDs(daemonTempDirs...) {
		entry, known := past.FindIndexed(id)
		if !known {
			continue
		}
		// A daemon that died stays listed, marked Crashed; only a running one
		// keeps its session's scratch.
		if roster != nil {
			if live, listed := roster.Find(id); listed && !live.Crashed {
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
	past    *hubcore.PastIndex
	archive *hubcore.ArchiveStore
	roster  *hubcore.Roster
	kick    chan struct{}
}

func newScratchReconciler(past *hubcore.PastIndex, archive *hubcore.ArchiveStore, roster *hubcore.Roster) *scratchReconciler {
	return &scratchReconciler{past: past, archive: archive, roster: roster, kick: make(chan struct{}, 1)}
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
			reconcileArchivedScratch(r.past, r.archive, r.roster, time.Now())
		}
	}
}
