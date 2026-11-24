package hubcore

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

// SessionProject is one session's project identity as the launch rule sees it:
// which project, which source owns that project, when the session was created,
// and whether it is a subagent.
type SessionProject struct {
	ProjectID string
	Source    string
	CreatedAt time.Time
	Subagent  bool
}

// ProjectsToUnarchive returns the archived projects a top-level session has
// been launched into since the archive decision: the user starting a session
// in the project is using it again, so navigation must stop filing that
// session as archived. Each returned key appears once, sorted by project ID,
// then source.
//
// Only a session created strictly after the decision counts. An existing
// session, a resumed one, or a re-indexed one does not clear an archive the
// user chose while it existed; the comparison is at the decision row's
// one-second precision, so a session created in the decision's own second
// counts as covered. A subagent's spawn is not a user launch; a fork is a new
// top-level session and does count. The decision is per owning source, so a
// new remote session clears that host's decision only.
func ProjectsToUnarchive(decisions map[ArchiveKey]ArchiveDecision, sessions []SessionProject) []ArchiveKey {
	cleared := make(map[ArchiveKey]struct{})
	for _, session := range sessions {
		if session.Subagent || session.ProjectID == "" || session.CreatedAt.IsZero() {
			continue
		}
		key := ProjectDecisionKey(session.Source, session.ProjectID)
		decision, ok := decisions[key]
		if !ok || !decision.Archived || !session.CreatedAt.Truncate(time.Second).After(decision.DecidedAt) {
			continue
		}
		cleared[key] = struct{}{}
	}
	return slices.SortedFunc(maps.Keys(cleared), func(a, b ArchiveKey) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Source, b.Source))
	})
}
