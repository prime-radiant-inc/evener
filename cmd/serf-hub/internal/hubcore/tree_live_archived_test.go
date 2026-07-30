package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

// The rail's Live tier must not list archived sessions: archive is a
// clearing verb, and an archived-but-still-running session is already
// reachable under its project's Archived tier. The exclusion rule is the
// same one the pinned (favorite) tier applies: an explicit session decision,
// age-based auto-archive, or a manually archived project all clear the row.
func TestBuildTreeLiveExcludesArchivedSessions(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * 24 * time.Hour) // beyond archiveWindow: auto-archived

	meta := func(id string, activity time.Time) schema.SessionMeta {
		return schema.SessionMeta{
			ID:        id,
			CreatedAt: activity,
			UpdatedAt: activity,
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
		}
	}
	entry := func(id string) LiveEntry {
		return LiveEntry{
			SessionID: id,
			Status:    "active",
			Entry:     rendezvous.Entry{SessionID: id, StartedAt: now},
		}
	}

	projectEntry := entry("proj-archived")
	projectEntry.Project = identifier.Project{ID: "proj-x"}

	metas := []schema.SessionMeta{
		meta("keep", now),
		meta("manual", now),
		meta("auto", stale),
		meta("proj-archived", now),
	}
	live := []LiveEntry{
		entry("keep"),
		entry("manual"),
		entry("auto"),
		projectEntry,
		entry("orphan"), // no meta on disk; explicitly archived
	}
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "manual"}: true,
		{Kind: "session", ID: "orphan"}: true,
		{Kind: "project", ID: "proj-x"}: true,
	}

	tree := BuildTreeAt(metas, live, decisions, now)

	got := map[string]bool{}
	for _, node := range tree.Live {
		got[node.ID] = true
	}
	for _, wantGone := range []string{"manual", "auto", "proj-archived", "orphan"} {
		if got[wantGone] {
			t.Errorf("archived live session %q still listed in the Live tier", wantGone)
		}
	}
	if !got["keep"] {
		t.Errorf("non-archived live session \"keep\" missing from the Live tier: %v", got)
	}

	// The archived sessions must not vanish entirely. The two archived by
	// session-level rules (manual decision, age) land in their project's
	// Archived tier.
	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d, want 1", len(tree.Projects))
	}
	archived := map[string]bool{}
	for _, node := range tree.Projects[0].Archived {
		archived[node.ID] = true
	}
	for _, id := range []string{"manual", "auto"} {
		if !archived[id] {
			t.Errorf("archived live session %q missing from its project's Archived tier", id)
		}
	}
	// The project-archived session was filtered by its live entry's
	// canonical project identity (production: the same ID its metas group
	// under; this fixture can only set it on the entry). It must still be
	// reachable in its project grouping, whatever tier.
	reachable := false
	for _, node := range allSessions(tree.Projects[0]) {
		if node.ID == "proj-archived" {
			reachable = true
		}
	}
	if !reachable {
		t.Errorf("project-archived live session vanished from its project grouping")
	}
}
