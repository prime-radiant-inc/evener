package hub

import (
	"fmt"
	"sync"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const (
	// A completed build is logged when it took at least this long, or when no
	// build has been logged for navigationBuildStatsInterval (measured from the
	// service's start, so the first build alone never logs). Timeouts always log.
	navigationBuildStatsSlow     = 500 * time.Millisecond
	navigationBuildStatsInterval = 10 * time.Minute
)

// navigationBuildStats sizes one navigation build: where the time went and how
// many sessions of each kind it handled. Durations are those of the last
// capture attempt; Restarts counts attempts discarded for a stale revision.
type navigationBuildStats struct {
	Total, Inputs, Resolve, Tree, Projection, Fingerprints, NextStates time.Duration
	Restarts                                                           int

	Metas, Subagents, Roots         int
	ArchivedRoots                   int
	ArchivedRootsInArchivedProjects int
	ArchivedRootsInActiveProjects   int
	Dirs, Live, Resources           int
	phase                           string // the phase in progress, for a timeout line
}

// countNavigationSessions fills the session counts from data the capture
// already holds: one pass over the metas, and the archived tier of each
// project in the built tree. It does no I/O.
func (s *navigationBuildStats) countNavigationSessions(metas []schema.SessionMeta, live []hubcore.LiveEntry, tree hubcore.Tree) {
	dirs := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		if meta.IsSubagent {
			s.Subagents++
		}
		dirs[hubcore.EffectiveWorkingDir(meta)] = struct{}{}
	}
	delete(dirs, "")
	s.Metas = len(metas)
	s.Roots = s.Metas - s.Subagents
	s.Dirs = len(dirs)
	s.Live = len(live)
	for _, project := range tree.Projects {
		rows, _ := project.TierRows("archived") // "archived" is always a known tier
		s.ArchivedRootsInActiveProjects += len(rows)
	}
	for _, project := range tree.ArchivedProjects {
		rows, _ := project.TierRows("archived") // "archived" is always a known tier
		s.ArchivedRootsInArchivedProjects += len(rows)
	}
	s.ArchivedRoots = s.ArchivedRootsInActiveProjects + s.ArchivedRootsInArchivedProjects
}

func (s navigationBuildStats) fields() string {
	return fmt.Sprintf("total=%s inputs=%s resolve=%s tree=%s projection=%s fingerprints=%s next_states=%s restarts=%d "+
		"metas=%d subagents=%d roots=%d archived_roots=%d archived_roots_archived_projects=%d archived_roots_active_projects=%d "+
		"dirs=%d live=%d resources=%d",
		s.Total.Round(time.Millisecond), s.Inputs.Round(time.Millisecond), s.Resolve.Round(time.Millisecond),
		s.Tree.Round(time.Millisecond), s.Projection.Round(time.Millisecond), s.Fingerprints.Round(time.Millisecond),
		s.NextStates.Round(time.Millisecond), s.Restarts,
		s.Metas, s.Subagents, s.Roots, s.ArchivedRoots, s.ArchivedRootsInArchivedProjects, s.ArchivedRootsInActiveProjects,
		s.Dirs, s.Live, s.Resources)
}

// navigationBuildStatsLog decides which builds get a log line and writes it.
// A finalized flight's tail can overlap the next flight's build, so the
// last-logged time is guarded.
type navigationBuildStatsLog struct {
	mu         sync.Mutex // guards lastLogged
	logf       func(format string, args ...any)
	slow       time.Duration // zero means navigationBuildStatsSlow
	lastLogged time.Time     // the last line, or the service's start
}

func (l *navigationBuildStatsLog) completed(stats navigationBuildStats, now time.Time) {
	if l.logf == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	slow := l.slow
	if slow == 0 {
		slow = navigationBuildStatsSlow
	}
	if stats.Total < slow && now.Sub(l.lastLogged) < navigationBuildStatsInterval {
		return
	}
	l.lastLogged = now
	l.logf("navigation build: %s", stats.fields())
}

func (l *navigationBuildStatsLog) timedOut(stats navigationBuildStats) {
	if l.logf == nil {
		return
	}
	l.logf("navigation build timed out in %s: %s", stats.phase, stats.fields())
}
