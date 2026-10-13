package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
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
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
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

// A row's own archived flag (#4199): the Board's rows carry whether the rail
// files the session as archived, so a pinned, still-running archived session
// gets no unread dot. The session's own decision, its project's decision, and
// the two-week age rule all set it; a row it does not touch stays false.
func TestBuildTreeRowsCarryTheArchivedDecision(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * 24 * time.Hour) // beyond archiveWindow: auto-archived

	env := schema.EnvironmentInfo{WorkingDir: "/w"}
	unarchivedEnv := schema.EnvironmentInfo{WorkingDir: "/w2"}
	meta := func(id string, activity time.Time) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: activity, UpdatedAt: activity, EnvInfo: env}
	}
	entry := func(id string) LiveEntry {
		return LiveEntry{SessionID: id, Status: "active", Entry: rendezvous.Entry{SessionID: id, StartedAt: now}}
	}

	keep := meta("keep", now)
	keep.EnvInfo = unarchivedEnv
	metas := []schema.SessionMeta{
		keep,
		meta("manual", now),
		meta("auto", stale),
		meta("project-archived", now),
	}
	live := []LiveEntry{entry("keep"), entry("manual"), entry("auto"), entry("project-archived")}
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "manual"}: true,
		{Kind: "project", ID: "proj-x"}: true,
	}
	// Pin the project keys so the project decision resolves without a real
	// working-directory resolver. "keep" sits in a project with no decision.
	tree := BuildTreeAtWithProjects(metas, live, decisions, now, map[string]identifier.Project{"/w": {ID: "proj-x"}, "/w2": {ID: "other"}})

	rows := map[string]bool{}
	var walk func([]TreeNode)
	walk = func(nodes []TreeNode) {
		for _, node := range nodes {
			if _, seen := rows[node.ID]; !seen {
				rows[node.ID] = node.Archived
			}
			walk(node.Children)
		}
	}
	for _, project := range append(append([]TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		walk(project.Current)
		walk(project.Recent)
		walk(project.Archived)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"keep", false},
		{"manual", true},           // its own decision
		{"auto", true},             // the age rule
		{"project-archived", true}, // its project's decision
	} {
		got, present := rows[tc.id]
		if !present {
			t.Errorf("row %s missing from the tree", tc.id)
			continue
		}
		if got != tc.want {
			t.Errorf("row %s Archived = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// An age-archived live session reaches Needs you (tierEligible clears only an
// explicit decision), so its row carries the archived fact too: otherwise the
// Board would keep its unread dot (#4199). A project decision archives a Needs
// you row the same way, and an unarchived row stays false.
func TestBuildTreeNeedsYouRowCarriesTheArchivedDecision(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * 24 * time.Hour) // beyond archiveWindow: auto-archived
	meta := func(id string, activity time.Time) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: activity, UpdatedAt: activity, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	}
	entry := func(id string) LiveEntry {
		return LiveEntry{SessionID: id, Status: "awaiting", Entry: rendezvous.Entry{SessionID: id, StartedAt: now, WorkingDir: "/w"}}
	}
	for _, tc := range []struct {
		name      string
		metas     []schema.SessionMeta
		decisions map[ArchiveKey]bool
		want      bool
	}{
		{"age-archived", []schema.SessionMeta{meta("awaiting", stale)}, nil, true},
		{"project-archived", []schema.SessionMeta{meta("awaiting", now)}, map[ArchiveKey]bool{{Kind: "project", ID: "p"}: true}, true},
		{"unarchived", []schema.SessionMeta{meta("awaiting", now)}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := BuildTreeAtWithProjects(tc.metas, []LiveEntry{entry("awaiting")}, tc.decisions, now, map[string]identifier.Project{"/w": {ID: "p"}})
			if len(tree.NeedsYou) != 1 {
				t.Fatalf("NeedsYou tier holds %d rows, want 1", len(tree.NeedsYou))
			}
			if got := tree.NeedsYou[0].Archived; got != tc.want {
				t.Errorf("Needs you row Archived = %v, want %v", got, tc.want)
			}
		})
	}
}

// A nested fork original carries the archived fact too (#4199): a location
// summary and the archived list serve a nested row in its own right, so a
// consumer must be able to see that it is archived.
func TestBuildTreeNestedRowCarriesTheArchivedDecision(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	env := schema.EnvironmentInfo{WorkingDir: "/w"}
	metas := []schema.SessionMeta{
		{ID: "orig", ForkLabel: "before edit", CreatedAt: now, UpdatedAt: now, EnvInfo: env},
		{ID: "cont", ParentSessionID: "orig", CreatedAt: now, UpdatedAt: now, EnvInfo: env},
	}
	decisions := map[ArchiveKey]bool{{Kind: "session", ID: "orig"}: true}
	tree := BuildTreeAtWithProjects(metas, nil, decisions, now, map[string]identifier.Project{"/w": {ID: "p"}})

	rows := map[string]bool{}
	var walk func([]TreeNode)
	walk = func(nodes []TreeNode) {
		for _, node := range nodes {
			if _, seen := rows[node.ID]; !seen {
				rows[node.ID] = node.Archived
			}
			walk(node.Children)
		}
	}
	for _, project := range append(append([]TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		walk(project.Current)
		walk(project.Recent)
		walk(project.Archived)
	}
	if _, present := rows["orig"]; !present {
		t.Fatal("the nested fork original has no row")
	}
	if !rows["orig"] {
		t.Error("a nested fork original archived by its own decision reads Archived false")
	}
	if rows["cont"] {
		t.Error("the continuation of an archived fork original reads Archived true")
	}
}
