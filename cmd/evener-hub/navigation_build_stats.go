package hub

import (
	"log/slog"
	"sync"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const (
	// A completed build is logged when it took at least this long, or when no
	// build has been logged for navigationBuildStatsInterval (measured from the
	// service's start, so the first build alone never logs). Timeouts and other
	// failures always log; a build canceled by the service stopping does not.
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

// attrs renders the build's timing and session counts as structured fields, so
// a log sink (and a test) reads each value by name instead of parsing prose.
func (s navigationBuildStats) attrs() []any {
	return []any{
		"total", s.Total.Round(time.Millisecond),
		"inputs", s.Inputs.Round(time.Millisecond),
		"resolve", s.Resolve.Round(time.Millisecond),
		"tree", s.Tree.Round(time.Millisecond),
		"projection", s.Projection.Round(time.Millisecond),
		"fingerprints", s.Fingerprints.Round(time.Millisecond),
		"next_states", s.NextStates.Round(time.Millisecond),
		"restarts", s.Restarts,
		"metas", s.Metas,
		"subagents", s.Subagents,
		"roots", s.Roots,
		"archived_roots", s.ArchivedRoots,
		"archived_roots_archived_projects", s.ArchivedRootsInArchivedProjects,
		"archived_roots_active_projects", s.ArchivedRootsInActiveProjects,
		"dirs", s.Dirs,
		"live", s.Live,
		"resources", s.Resources,
	}
}

// navigationBuildStatsLog decides which builds get a log line and writes it.
// A finalized flight's tail can overlap the next flight's build, so the
// last-logged time is guarded.
type navigationBuildStatsLog struct {
	mu         sync.Mutex // guards lastLogged
	log        *slog.Logger
	slow       time.Duration // zero means navigationBuildStatsSlow
	lastLogged time.Time     // the last line, or the service's start
}

func (l *navigationBuildStatsLog) completed(stats navigationBuildStats, now time.Time) {
	if l.log == nil {
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
	l.log.Info("navigation build", append([]any{"event", "completed"}, stats.attrs()...)...)
}

func (l *navigationBuildStatsLog) timedOut(stats navigationBuildStats) {
	if l.log == nil {
		return
	}
	l.log.Info("navigation build timed out", append([]any{"event", "timeout", "phase", stats.phase}, stats.attrs()...)...)
}

func (l *navigationBuildStatsLog) failed(stats navigationBuildStats, err error) {
	if l.log == nil {
		return
	}
	l.log.Info("navigation build failed", append(append([]any{"event", "failed", "phase", stats.phase}, stats.attrs()...), "error", err)...)
}
