package hubcore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildTreeCanonicalProjectAggregation(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "shared")
	initHubTestRepo(t, main)
	linked := filepath.Join(root, "linked")
	runHubTestGit(t, main, "worktree", "add", "-q", linked, "-b", "feature")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(linked, alias); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "other", "shared")
	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		t.Fatal(err)
	}
	runHubTestGit(t, root, "clone", "-q", main, clone)
	now := time.Unix(1_700_000_000, 0)
	paths := []string{main, linked, alias, clone}
	metas := make([]schema.SessionMeta, 0, len(paths))
	for i, path := range paths {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("canonical-%d", i), CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: path},
		})
	}
	tree := BuildTreeAt(metas, nil, nil, now)
	if len(tree.Projects) != 2 {
		t.Fatalf("projects=%d, want shared main/worktree/symlink plus distinct clone: %+v", len(tree.Projects), tree)
	}
	for _, project := range tree.Projects {
		if err := identifier.ValidateProjectID(project.Key); err != nil {
			t.Fatalf("project key %q is not canonical: %v", project.Key, err)
		}
		if project.WorkingDir == main && len(allSessions(project)) != 3 {
			t.Fatalf("shared project sessions=%d, want 3: %+v", len(allSessions(project)), project)
		}
	}
}

func initHubTestRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runHubTestGit(t, filepath.Dir(dir), "init", "-q", filepath.Base(dir))
	runHubTestGit(t, dir, "add", ".")
	runHubTestGit(t, dir, "-c", "user.name=serf-test", "-c", "user.email=serf-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
}

func runHubTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func fuzzScenarioBuildTree_GroupsByProjectWithSubagentsAndForks(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		// Active branch — newer, holds the original session's name. Top-level.
		{ID: "01ACTIVE", UpdatedAt: now, OriginalPrompt: "fix replay bug",
			EnvInfo:         schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ParentSessionID: "01OLDORIG", DivergenceTurn: 7},
		// Subagent of the active branch.
		{ID: "01SUB1", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "verify",
			EnvInfo:         schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ParentSessionID: "01ACTIVE", IsSubagent: true},
		// Snapshotted original — older transcript preserved. Has ForkLabel.
		// Becomes a dim child of 01ACTIVE (the active branch references it).
		{ID: "01OLDORIG", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "fix replay bug",
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ForkLabel: "before TDD"},
		// Unrelated session in same project.
		{ID: "01OTHER", UpdatedAt: now.Add(-15 * time.Minute), OriginalPrompt: "rename column",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ACTIVE", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB1", Status: appwire.ThreadStatusActive},
	}

	tree := buildTree(metas, live)

	// One project
	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	proj := tree.Projects[0]
	if proj.Name != "serf-hub" {
		t.Errorf("name: %q", proj.Name)
	}

	// Top-level sessions: 01ACTIVE then 01OTHER (by recency).
	// 01OLDORIG is NOT top-level — it's the snapshotted original under 01ACTIVE.
	sessions := allSessions(proj)
	if len(sessions) != 2 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	if sessions[0].ID != "01ACTIVE" {
		t.Errorf("[0]: %q", sessions[0].ID)
	}
	if sessions[1].ID != "01OTHER" {
		t.Errorf("[1]: %q", sessions[1].ID)
	}

	// Children of 01ACTIVE: subagent first, then the snapshotted original (fork).
	children := sessions[0].Children
	if len(children) != 2 {
		t.Fatalf("children: %d", len(children))
	}
	if children[0].ID != "01SUB1" || children[0].Kind != "subagent" {
		t.Errorf("[0]: %s/%s", children[0].ID, children[0].Kind)
	}
	if children[1].ID != "01OLDORIG" || children[1].Kind != "fork" {
		t.Errorf("[1]: %s/%s", children[1].ID, children[1].Kind)
	}
	// Fork title includes the label.
	if !strings.Contains(children[1].Title, "before TDD") {
		t.Errorf("fork title missing label: %q", children[1].Title)
	}

	// 01OTHER has no children
	if len(sessions[1].Children) != 0 {
		t.Errorf("01OTHER should have no children, got %d", len(sessions[1].Children))
	}

	// Live: 2 entries, both active.
	if len(tree.Live) != 2 {
		t.Fatalf("live: %d", len(tree.Live))
	}

	// Rollup state for the project: active (the most-attention live state).
	if proj.RollupState != "active" {
		t.Errorf("rollup: %q", proj.RollupState)
	}
}

func fuzzScenarioBuildTree_ProjectsRunningSubagentOnChild(t *testing.T) {
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01PARENT", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01CHILD", CreatedAt: now, UpdatedAt: now, ParentSessionID: "01PARENT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{{
		Entry:              rendezvous.Entry{PID: 1},
		SessionID:          "01PARENT",
		Status:             appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"01CHILD"},
	}}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(tree.Projects))
	}
	project := tree.Projects[0]
	sessions := allSessions(project)
	if len(sessions) != 1 || len(sessions[0].Children) != 1 {
		t.Fatalf("sessions = %+v, want one parent with one child", sessions)
	}
	if sessions[0].State != "idle" {
		t.Fatalf("parent state = %q, want idle", sessions[0].State)
	}
	if sessions[0].Children[0].State != "active" {
		t.Fatalf("child state = %q, want active", sessions[0].Children[0].State)
	}
	if project.RollupState != "active" || project.RollupLive != 1 || !project.Expanded {
		t.Fatalf("project rollup = state %q live %d expanded %v, want active/1/true", project.RollupState, project.RollupLive, project.Expanded)
	}

	tree = BuildTreeAt(metas, []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01PARENT", Status: appwire.ThreadStatusIdle}}, nil, now)
	project = tree.Projects[0]
	sessions = allSessions(project)
	if sessions[0].Children[0].State != "ended" || project.RollupState != "idle" || project.RollupLive != 0 || project.Expanded {
		t.Fatalf("stopped child projection = child %q rollup %q/%d expanded %v", sessions[0].Children[0].State, project.RollupState, project.RollupLive, project.Expanded)
	}
}

func fuzzScenarioBuildTree_UsesGeneratedNameForSessionTitle(t *testing.T) {
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(allSessions(tree.Projects[0])) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := allSessions(tree.Projects[0])[0].Title; got != "Launch Config Cheap Model" {
		t.Fatalf("session title = %q, want generated name", got)
	}
}

func fuzzScenarioBuildTree_TruncatesLongOriginalPromptTitles(t *testing.T) {
	long := strings.Repeat("é", 5000) // multi-byte runes: truncation must be rune-safe
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01LONG",
		OriginalPrompt: long,
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(allSessions(tree.Projects[0])) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	got := allSessions(tree.Projects[0])[0].Title
	if want := strings.Repeat("é", 200) + "…"; got != want {
		t.Fatalf("title = %d runes (%.30q...), want 200 runes + ellipsis", len([]rune(got)), got)
	}
}

func fuzzScenarioBuildTree_TruncatesLongForkBaseTitleKeepingLabel(t *testing.T) {
	long := strings.Repeat("x", 300)
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01LONGFORK",
		OriginalPrompt: long,
		ForkLabel:      "before TDD",
		UpdatedAt:      time.Now(),
	}}, nil)
	got := allSessions(tree.Projects[0])[0].Title
	if want := strings.Repeat("x", 200) + "… · before TDD"; got != want {
		t.Fatalf("fork title = %q, want truncated base plus label", got)
	}
}

func fuzzScenarioBuildTree_ShortTitleNotTruncated(t *testing.T) {
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01SHORT",
		OriginalPrompt: strings.Repeat("a", 200),
		UpdatedAt:      time.Now(),
	}}, nil)
	if got := allSessions(tree.Projects[0])[0].Title; got != strings.Repeat("a", 200) {
		t.Fatalf("title = %q, want untouched 200-rune title", got)
	}
}

func fuzzScenarioBuildTree_UsesGeneratedNameForForkBaseTitle(t *testing.T) {
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01FORK",
		Name:           "Generated Base",
		OriginalPrompt: "original base",
		ForkLabel:      "before TDD",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(allSessions(tree.Projects[0])) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := allSessions(tree.Projects[0])[0].Title; got != "Generated Base · before TDD" {
		t.Fatalf("fork title = %q, want generated name plus label", got)
	}
}

func fuzzScenarioBuildTree_AttentionSortsLive(t *testing.T) {
	// Three live sessions: idle, awaiting, processing.
	// Live should sort: awaiting, processing, idle.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01IDLE", UpdatedAt: now.Add(-3 * time.Minute), OriginalPrompt: "idle task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01AWAIT", UpdatedAt: now.Add(-2 * time.Minute), OriginalPrompt: "awaiting task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01PROC", UpdatedAt: now.Add(-1 * time.Minute), OriginalPrompt: "processing task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01IDLE", Status: "idle"},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01AWAIT", Status: "awaiting"},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01PROC", Status: appwire.ThreadStatusActive},
	}

	tree := buildTree(metas, live)

	if len(tree.Live) != 3 {
		t.Fatalf("live count: %d", len(tree.Live))
	}
	if tree.Live[0].ID != "01AWAIT" {
		t.Errorf("live[0] should be awaiting, got %q (state=%q)", tree.Live[0].ID, tree.Live[0].State)
	}
	if tree.Live[1].ID != "01PROC" {
		t.Errorf("live[1] should be processing, got %q (state=%q)", tree.Live[1].ID, tree.Live[1].State)
	}
	if tree.Live[2].ID != "01IDLE" {
		t.Errorf("live[2] should be idle, got %q (state=%q)", tree.Live[2].ID, tree.Live[2].State)
	}

	// All live nodes should have Kind="session"
	for _, node := range tree.Live {
		if node.Kind != "session" {
			t.Errorf("live node %q has kind %q, want session", node.ID, node.Kind)
		}
	}
}

func fuzzScenarioBuildTree_OrdersProjectSessionsByUpdatedCreatedTitleAndID(t *testing.T) {
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv2enqVTitaig6F", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "02wMz5Txv1C3Hut0M8GCeB", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "02wMz5Txv47YP64RR3B9YJ", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "02wMz5Txv5aIxgf9yVdd0N", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}

	// Classify against a clock right after the sessions so all four land in the
	// same (Current) tier; the test is about within-project ordering, not tiering.
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, updated.Add(time.Hour))
	if len(tree.Projects) != 1 {
		t.Fatalf("projects=%d", len(tree.Projects))
	}
	sessions := allSessions(tree.Projects[0])
	got := make([]string, 0, len(sessions))
	for _, node := range sessions {
		got = append(got, node.ID)
	}
	want := []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv2enqVTitaig6F", "02wMz5Txv5aIxgf9yVdd0N", "02wMz5Txv47YP64RR3B9YJ"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func fuzzScenarioBuildTree_OrdersLiveRowsWithoutMetasByStartedAtAndID(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1, StartedAt: base.Add(-time.Hour)}, SessionID: "02wMz5Txv2enqVTitaig6F", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 2, StartedAt: base}, SessionID: "02wMz5Txv1C3Hut0M8GCeB", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "02wMz5Txv8Vo4rqb3QYZuV", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "02wMz5Txv733WHFsVy66SR", Status: appwire.ThreadStatusIdle},
	}

	tree := buildTree(nil, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv2enqVTitaig6F", "02wMz5Txv733WHFsVy66SR", "02wMz5Txv8Vo4rqb3QYZuV"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func fuzzScenarioBuildTree_OrdersMixedLiveRowsByMergedMetadata(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{
		ID:             "01META",
		UpdatedAt:      base.Add(time.Hour),
		CreatedAt:      base.Add(-time.Hour),
		OriginalPrompt: "meta-backed live row",
	}}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "01META", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 2, StartedAt: base}, SessionID: "02FRESH", Status: appwire.ThreadStatusIdle},
	}

	tree := buildTree(metas, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"01META", "02FRESH"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func fuzzScenarioBuildTree_NoProjectFallback(t *testing.T) {
	// A meta with empty WorkingDir — project name "(no project)".
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01NOPROJ", UpdatedAt: now, OriginalPrompt: "orphan task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: ""}},
	}
	live := []LiveEntry{}

	tree := buildTree(metas, live)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	if tree.Projects[0].Name != "(no project)" {
		t.Errorf("project name: %q", tree.Projects[0].Name)
	}
	sessions := allSessions(tree.Projects[0])
	if len(sessions) != 1 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	if sessions[0].ID != "01NOPROJ" {
		t.Errorf("session id: %q", sessions[0].ID)
	}
	// Not live => state = "ended"
	if sessions[0].State != "ended" {
		t.Errorf("state: %q", sessions[0].State)
	}
	// No live sessions => RollupState = ""
	if tree.Projects[0].RollupState != "" {
		t.Errorf("rollup should be empty, got %q", tree.Projects[0].RollupState)
	}
	// Live slice is empty
	if len(tree.Live) != 0 {
		t.Fatalf("live: %d", len(tree.Live))
	}
}

// TestBuildTree_GroupsByRestoreRootWhenWorktreeActive proves the native
// worktree tools spec §7 "Hub consumers" migration: a session actively
// inside a managed worktree must group (and prefill the spawn form, via
// TreeProject.WorkingDir) under its restore root, not the worktree path —
// else it lands under a phantom project named after the worktree leaf
// (e.g. "dlg_01H...").
func fuzzScenarioBuildTree_GroupsByRestoreRootWhenWorktreeActive(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	restoreRoot := filepath.Join(root, "serf-hub")
	worktree := filepath.Join(root, "state", "worktrees", "serf-hub", "dlg_01H")
	if err := os.MkdirAll(restoreRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalRestoreRoot, err := identifier.ResolveProject(restoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01WT", UpdatedAt: now, OriginalPrompt: "work in lane",
			EnvInfo:             schema.EnvironmentInfo{WorkingDir: worktree},
			WorktreePath:        worktree,
			WorktreeManaged:     true,
			WorktreeRestoreRoot: restoreRoot},
	}

	tree := buildTree(metas, nil)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	proj := tree.Projects[0]
	if proj.Name != "serf-hub" {
		t.Errorf("name: %q, want restore-root basename %q", proj.Name, "serf-hub")
	}
	if proj.WorkingDir != canonicalRestoreRoot.CanonicalPath {
		t.Errorf("workingDir: %q, want canonical restore root %q", proj.WorkingDir, canonicalRestoreRoot.CanonicalPath)
	}
}

// TestBuildTree_PathEnteredNonManagedWorktreeAlsoGroupsByRestoreRoot proves
// the non-managed by-path case migrates too — spec §7 is explicit that both
// switch modes swap the env and so must both use the restore root.
func fuzzScenarioBuildTree_PathEnteredNonManagedWorktreeAlsoGroupsByRestoreRoot(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	restoreRoot := filepath.Join(root, "serf-hub")
	worktree := filepath.Join(root, "other-checkout")
	if err := os.MkdirAll(restoreRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01PATHWT", UpdatedAt: now, OriginalPrompt: "poke around a sibling checkout",
			EnvInfo:             schema.EnvironmentInfo{WorkingDir: worktree},
			WorktreePath:        worktree,
			WorktreeManaged:     false,
			WorktreeRestoreRoot: restoreRoot},
	}

	tree := buildTree(metas, nil)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	if tree.Projects[0].Name != "serf-hub" {
		t.Errorf("name: %q, want restore-root basename %q", tree.Projects[0].Name, "serf-hub")
	}
}

// buildTree is the test convenience wrapper around BuildTree for the cases that
// don't exercise archive decisions: it passes an empty decision map.
func buildTree(metas []schema.SessionMeta, live []LiveEntry) Tree {
	return BuildTree(metas, live, map[ArchiveKey]bool{})
}

// allSessions returns a project's top-level session rows across all tiers,
// Current then Recent then Archived — the flat list most session-shape
// assertions want now that sessions are tier-split.
func allSessions(p TreeProject) []TreeNode {
	out := make([]TreeNode, 0, len(p.Current)+len(p.Recent)+len(p.Archived))
	out = append(out, p.Current...)
	out = append(out, p.Recent...)
	out = append(out, p.Archived...)
	return out
}

// projectByName finds the TreeProject with the given name in the active list, or
// fails.
func projectByName(t *testing.T, tree Tree, name string) TreeProject {
	t.Helper()
	for _, p := range tree.Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("project %q not found in %v", name, projectNames(tree.Projects))
	return TreeProject{}
}

func projectNames(ps []TreeProject) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

func fuzzScenarioBuildTreeSessionTiersAndProjectOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, createdAgo, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-createdAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("a-cur", "alpha", 2*time.Hour, 1*time.Hour),         // alpha: current
		mk("a-old", "alpha", 40*24*time.Hour, 30*24*time.Hour), // alpha: auto-archived
		mk("b-rec", "beta", 50*time.Hour, 48*time.Hour),        // beta: recent only
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)

	// alpha has a current session and a >14d session -> alpha is an active project
	// with one Current and one Archived session.
	var alpha *TreeProject
	for i := range tree.Projects {
		if tree.Projects[i].Name == "alpha" {
			alpha = &tree.Projects[i]
		}
	}
	if alpha == nil {
		t.Fatalf("alpha should be an active project")
		return
	}
	if len(alpha.Current) != 1 || len(alpha.Archived) != 1 || len(alpha.Recent) != 0 {
		t.Fatalf("alpha tiers: current=%d recent=%d archived=%d", len(alpha.Current), len(alpha.Recent), len(alpha.Archived))
	}
	// Projects ordered by last-activity desc: alpha's most recent touch was 1h
	// ago, beta's was 48h ago -> alpha first.
	if len(tree.Projects) < 2 || tree.Projects[0].Name != "alpha" {
		t.Fatalf("project order wrong: %+v", projectNames(tree.Projects))
	}
}

func fuzzScenarioBuildTreeArchivedProjectPlacement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	// gamma: every session auto-archived -> gamma goes to ArchivedProjects.
	metas := []schema.SessionMeta{mk("g1", "gamma", 30*24*time.Hour)}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 0 {
		t.Fatalf("gamma should not be active, got %v", projectNames(tree.Projects))
	}
	if len(tree.ArchivedProjects) != 1 || tree.ArchivedProjects[0].Name != "gamma" {
		t.Fatalf("gamma should be an archived project")
	}
	if !tree.ArchivedProjects[0].IsArchived {
		t.Fatalf("gamma should be flagged IsArchived")
	}

	// manual project archive forces delta to ArchivedProjects even though it's fresh.
	deltaDir := filepath.Join(t.TempDir(), "delta")
	if err := os.MkdirAll(deltaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	delta, err := identifier.ResolveProject(deltaDir)
	if err != nil {
		t.Fatal(err)
	}
	metas2 := []schema.SessionMeta{{ID: "d1", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: deltaDir}}}
	tree2 := BuildTreeAt(metas2, nil, map[ArchiveKey]bool{{Kind: "project", ID: delta.ID}: true}, now)
	if len(tree2.Projects) != 0 || len(tree2.ArchivedProjects) != 1 {
		t.Fatalf("delta manual-archived should be in ArchivedProjects")
	}
}

func fuzzScenarioBuildTreeManualSessionArchiveAndUnarchive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}}
	}
	metas := []schema.SessionMeta{
		mk("fresh", 1*time.Hour),     // current by age
		mk("stale", 30*24*time.Hour), // auto-archived by age
	}
	// Manual archive overrides the fresh session; manual unarchive rescues the stale one.
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "fresh"}: true,
		{Kind: "session", ID: "stale"}: false,
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, decisions, now), "serf")
	if len(proj.Archived) != 1 || proj.Archived[0].ID != "fresh" {
		t.Fatalf("manual-archived fresh session should be Archived; archived=%v", proj.Archived)
	}
	// An unarchived stale session is visible (Recent), not Current — it is old.
	if len(proj.Recent) != 1 || proj.Recent[0].ID != "stale" {
		t.Fatalf("manual-unarchived stale session should be Recent; recent=%v", proj.Recent)
	}
}

func fuzzScenarioBuildTreeE2eProjectsClassifyByRecency(t *testing.T) {
	// The serf-e2e-* prefix bucket is gone: a fresh e2e project flows through the
	// normal model (active, ordered by recency), and a stale one auto-archives.
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("e-fresh", "serf-e2e-fresh", 1*time.Hour),
		mk("e-stale", "serf-e2e-stale", 30*24*time.Hour),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if got := projectNames(tree.Projects); len(got) != 1 || got[0] != "serf-e2e-fresh" {
		t.Fatalf("fresh e2e project should be active, got %v", got)
	}
	if got := projectNames(tree.ArchivedProjects); len(got) != 1 || got[0] != "serf-e2e-stale" {
		t.Fatalf("stale e2e project should be archived, got %v", got)
	}
}

func fuzzScenarioBuildTree_RollupMagnitudeCountsLiveAndAttention(t *testing.T) {
	// mockup #10 spine: a project header carries a *magnitude* rollup
	// (how many are live vs. how many need you), not a single ambiguous dot.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01WORK1", UpdatedAt: now, OriginalPrompt: "work a",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01WORK2", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "work b",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01ASK", UpdatedAt: now.Add(-2 * time.Minute), OriginalPrompt: "blocked",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01WARN", UpdatedAt: now.Add(-3 * time.Minute), OriginalPrompt: "has a warning",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01ZZZ", UpdatedAt: now.Add(-4 * time.Minute), OriginalPrompt: "idle one",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01WORK1", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01WORK2", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 4}, SessionID: "01WARN", Status: appwire.ThreadStatusWarning},
		{Entry: rendezvous.Entry{PID: 5}, SessionID: "01ZZZ", Status: appwire.ThreadStatusIdle},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	// 2 working (active) sessions. Idle does not count toward either magnitude.
	if proj.RollupLive != 2 {
		t.Errorf("RollupLive = %d, want 2", proj.RollupLive)
	}
	// 1 awaiting + 1 warning = 2 attention-needed. Both states count toward
	// RollupAttn; pinning both legs of the switch case.
	if proj.RollupAttn != 2 {
		t.Errorf("RollupAttn = %d, want 2 (awaiting + warning)", proj.RollupAttn)
	}
}

func fuzzScenarioBuildTree_NeedsYouAggregatesAwaitingAcrossProjects(t *testing.T) {
	// mockup #11 rec: a single "Needs you" tier that aggregates every awaiting
	// session across all projects, oldest-first, hidden when nothing awaits.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A_NEW", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "newer ask",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01A_OLD", UpdatedAt: now.Add(-5 * time.Minute), OriginalPrompt: "older ask",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/prime-radiant"}},
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "just working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A_NEW", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01A_OLD", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 2 {
		t.Fatalf("NeedsYou count = %d, want 2 (only awaiting sessions)", len(tree.NeedsYou))
	}
	// Oldest-blocked first so the triage queue works top-down.
	if tree.NeedsYou[0].ID != "01A_OLD" {
		t.Errorf("NeedsYou[0] = %q, want 01A_OLD (oldest awaiting first)", tree.NeedsYou[0].ID)
	}
	// Each needs-you node carries its project for the cross-project meta line.
	if tree.NeedsYou[0].Project != "prime-radiant" {
		t.Errorf("NeedsYou[0].Project = %q, want prime-radiant", tree.NeedsYou[0].Project)
	}
	// A working (non-awaiting) session never appears in the triage tier.
	for _, n := range tree.NeedsYou {
		if n.ID == "01LIVE" {
			t.Errorf("working session leaked into NeedsYou: %q", n.ID)
		}
	}
}

func fuzzScenarioBuildTree_NeedsYouEmptyWhenNothingAwaits(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
	}
	if got := len(buildTree(metas, live).NeedsYou); got != 0 {
		t.Errorf("NeedsYou should be empty when nothing awaits, got %d", got)
	}
}

func fuzzScenarioBuildTree_ClustersRepeatedIdleTitles(t *testing.T) {
	// mockup #10/#C rec: a run of same-titled idle sessions collapses to one
	// cluster row; live/needs-you sessions stay un-clustered so signal isn't
	// hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := range 5 {
		metas = append(metas, schema.SessionMeta{
			ID:        "01IMG" + string(rune('A'+i)),
			Name:      "describe this image",
			UpdatedAt: now.Add(-time.Duration(i) * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"},
		})
	}
	// A distinct singleton in the same project must not be swept into a cluster.
	metas = append(metas, schema.SessionMeta{
		ID: "01HAIKU", Name: "write a haiku", UpdatedAt: now.Add(-30 * time.Minute),
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"}})

	proj := projectByName(t, buildTree(metas, nil), "serf-docs")
	var clusters, singles int
	for _, s := range allSessions(proj) {
		if s.Kind == "cluster" {
			clusters++
			if s.ClusterCount != 5 {
				t.Errorf("cluster count = %d, want 5", s.ClusterCount)
			}
			if len(s.Children) != 5 {
				t.Errorf("cluster should hold its 5 members as children, got %d", len(s.Children))
			}
			if !strings.Contains(s.Title, "describe this image") {
				t.Errorf("cluster title = %q, want the shared title", s.Title)
			}
		} else {
			singles++
		}
	}
	if clusters != 1 {
		t.Errorf("clusters = %d, want 1", clusters)
	}
	if singles != 1 {
		t.Errorf("singleton sessions = %d, want 1 (the haiku)", singles)
	}
}

func fuzzScenarioBuildTree_DoesNotClusterLiveRepeatedTitles(t *testing.T) {
	// A live/needs-you member must keep all repeated-title sessions un-clustered
	// so live signal is never hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := range 4 {
		metas = append(metas, schema.SessionMeta{
			ID:        "01DUP" + string(rune('A'+i)),
			Name:      "describe this image",
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"},
		})
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01DUPA", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf-docs")
	sessions := allSessions(proj)
	for _, s := range sessions {
		if s.Kind == "cluster" {
			t.Fatalf("repeated titles must NOT cluster when a member is live: got cluster %q", s.Title)
		}
	}
	if len(sessions) != 4 {
		t.Errorf("expected 4 un-clustered sessions, got %d", len(sessions))
	}
}

func fuzzScenarioBuildTree_ClampsSubagentsOfDeadParent(t *testing.T) {
	// A subagent that still reports "active" in the live map but whose parent
	// session has ended must not keep spinning ⟳ forever — its state is clamped
	// to "ended" so the dead session's children read as terminal.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01DEADP", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "parent",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01STALESUB", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "sub",
			IsSubagent: true, ParentSessionID: "01DEADP",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	// Parent is NOT live (ended); the subagent lingers as "active" in the map.
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 9}, SessionID: "01STALESUB", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	sessions := allSessions(proj)
	if len(sessions) != 1 || len(sessions[0].Children) != 1 {
		t.Fatalf("unexpected shape: %#v", sessions)
	}
	if got := sessions[0].Children[0].State; got != "ended" {
		t.Errorf("stale subagent state = %q, want ended (parent is dead)", got)
	}
}

func fuzzScenarioBuildTree_KeepsSubagentStateWhenParentLive(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVEP", UpdatedAt: now, OriginalPrompt: "parent",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01RUNSUB", UpdatedAt: now, OriginalPrompt: "sub",
			IsSubagent: true, ParentSessionID: "01LIVEP",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVEP", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01RUNSUB", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	if got := allSessions(proj)[0].Children[0].State; got != "active" {
		t.Errorf("live subagent state = %q, want active (parent is live)", got)
	}
}

func TestBuildTree_ExcludesNestedForkFromNeedsYou(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "branch", ParentSessionID: "fork", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "fork", ForkLabel: "before edit", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "branch", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "fork", Status: appwire.ThreadStatusAwaiting},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("nested fork leaked into NeedsYou: %#v", tree.NeedsYou)
	}
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("tree = %#v, want one top-level branch", tree)
	}
	children := tree.Projects[0].Current[0].Children
	if len(children) != 1 || children[0].ID != "fork" || children[0].Kind != "fork" {
		t.Fatalf("branch children = %#v, want nested fork", children)
	}
}

func TestBuildTree_CanonicalizesDuplicateMetadataIDs(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "root-a", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "root-b", UpdatedAt: now.Add(-time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		// The newer duplicate is canonically retained, so dup remains a direct
		// child of root-a and is not emitted again under root-b or top-level.
		{ID: "dup", ParentSessionID: "root-a", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "dup", ParentSessionID: "root-b", UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}

	tree := BuildTreeAt(metas, nil, nil, now)
	var count func([]TreeNode, string) int
	count = func(nodes []TreeNode, id string) int {
		n := 0
		for _, node := range nodes {
			if node.ID == id {
				n++
			}
			n += count(node.Children, id)
		}
		return n
	}
	var top []TreeNode
	for _, project := range append(append([]TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		top = append(top, allSessions(project)...)
	}
	if got := count(top, "dup"); got != 1 {
		t.Fatalf("duplicate metadata ID emitted %d times, want once: %#v", got, tree)
	}
	if len(top) == 0 || len(top[0].Children) != 1 || top[0].Children[0].ID != "dup" {
		t.Fatalf("canonical duplicate parentage = %#v, want root-a child", top)
	}
}

func TestBuildTree_GuardsMalformedSubagentLineage(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "root", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "a", ParentSessionID: "root", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "b", ParentSessionID: "a", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		// Duplicate a closes a malformed a -> b -> a cycle if the builder
		// follows lineage blindly. It must not emit a twice.
		{ID: "a", ParentSessionID: "b", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		// An orphan remains absent rather than being hoisted to the project root.
		{ID: "orphan", ParentSessionID: "missing", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}

	tree := BuildTreeAt(metas, nil, nil, now)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("tree = %#v, want one current root", tree)
	}
	root := tree.Projects[0].Current[0]
	if len(root.Children) != 1 || root.Children[0].ID != "a" {
		t.Fatalf("root children = %#v, want one direct a", root.Children)
	}
	if len(root.Children[0].Children) != 1 || root.Children[0].Children[0].ID != "b" {
		t.Fatalf("a children = %#v, want one direct b", root.Children[0].Children)
	}
	if got := root.Children[0].Children[0].Children; len(got) != 0 {
		t.Fatalf("cycle should terminate at b, got children %#v", got)
	}

	var count func(TreeNode, string) int
	count = func(node TreeNode, id string) int {
		n := 0
		if node.ID == id {
			n++
		}
		for _, child := range node.Children {
			n += count(child, id)
		}
		return n
	}
	if got := count(root, "a"); got != 1 {
		t.Fatalf("malformed cycle duplicated a %d times", got)
	}
	if got := count(root, "orphan"); got != 0 {
		t.Fatalf("orphan was hoisted into tree %d times", got)
	}
}

func fuzzScenarioBuildTree_OrdersProjectsByLastActivity(t *testing.T) {
	// Active projects are emitted newest-first by their most recent
	// last-activity (max last-activity across the project's sessions), not by
	// when a session merely started.
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, createdAgo, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-createdAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		// touched-older's sessions were both CREATED before touched-newer's only
		// session, so max-CreatedAt ordering would rank touched-newer first. But
		// one of touched-older's sessions was TOUCHED more recently than
		// anything in touched-newer, which should lift touched-older to the top.
		mk("01R2", "touched-older", 3*time.Hour, 3*time.Hour),
		mk("01R3", "touched-older", 2*time.Hour, 1*time.Minute),
		mk("01R1", "touched-newer", 10*time.Minute, 10*time.Minute),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	got := projectNames(tree.Projects)
	want := []string{"touched-older", "touched-newer"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project order = %v, want %v", got, want)
	}
}

func fuzzScenarioBuildTree_OrdersProjectsByLastActivityNotCreatedAt(t *testing.T) {
	// A project's ordering key is last ACTIVITY, not session start: a project
	// whose only session was created long ago but touched moments ago must
	// outrank a project whose only session was created recently but has sat
	// stale (untouched) since. Sorting by max CreatedAt (the pre-change
	// behavior) gets this backwards — it ranks the freshly-started-but-stale
	// project first.
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, createdAgo, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-createdAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		// old-but-touched: session started 30 days ago, touched 1 minute ago.
		mk("01OLD", "old-but-touched", 30*24*time.Hour, 1*time.Minute),
		// new-but-stale: session started 10 minutes ago, never touched since.
		mk("02wMz5Txv1C3Hut0M8GCeB", "new-but-stale", 10*time.Minute, 10*time.Minute),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	got := projectNames(tree.Projects)
	want := []string{"old-but-touched", "new-but-stale"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project order = %v, want %v (must order by last activity, not session start)", got, want)
	}
}

func fuzzScenarioBuildTree_ExpandedOnlyForLiveProjects(t *testing.T) {
	// A project auto-expands only when it has a live (working) or awaiting
	// (needs-you) session — RollupLive > 0 || RollupAttn > 0. Idle-only and
	// archived projects stay collapsed.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/live-proj"}},
		{ID: "01IDLE", UpdatedAt: now, OriginalPrompt: "idle",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/idle-proj"}},
		{ID: "01OLD", UpdatedAt: now.Add(-30 * 24 * time.Hour), OriginalPrompt: "stale",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/archived-proj"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01IDLE", Status: appwire.ThreadStatusIdle},
	}
	tree := buildTree(metas, live)

	liveProj := projectByName(t, tree, "live-proj")
	if !liveProj.Expanded {
		t.Errorf("live-proj should be Expanded (RollupLive=%d RollupAttn=%d)", liveProj.RollupLive, liveProj.RollupAttn)
	}
	idleProj := projectByName(t, tree, "idle-proj")
	if idleProj.Expanded {
		t.Errorf("idle-proj should NOT be Expanded (RollupLive=%d RollupAttn=%d)", idleProj.RollupLive, idleProj.RollupAttn)
	}
	var archivedProj *TreeProject
	for i := range tree.ArchivedProjects {
		if tree.ArchivedProjects[i].Name == "archived-proj" {
			archivedProj = &tree.ArchivedProjects[i]
		}
	}
	if archivedProj == nil {
		t.Fatalf("archived-proj should be an archived project")
		return
	}
	if archivedProj.Expanded {
		t.Errorf("archived-proj should NOT be Expanded")
	}
}

func fuzzScenarioBuildTree_ExpandedForAwaitingProject(t *testing.T) {
	// An awaiting (needs-you) session counts toward RollupAttn, which auto-expands.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01ASK", UpdatedAt: now, OriginalPrompt: "blocked",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/asking"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting},
	}
	proj := projectByName(t, buildTree(metas, live), "asking")
	if !proj.Expanded {
		t.Errorf("awaiting project should be Expanded (RollupAttn=%d)", proj.RollupAttn)
	}
}

func fuzzScenarioBuildTree_CapsSessionsPerTierWithOverflowCounts(t *testing.T) {
	// Each tier is capped at maxSidebarSessionsPerTier; the overflow count is
	// recorded so the sidebar can show a "+N older" note instead of every row.
	now := time.Unix(1_700_000_000, 0)
	total := maxSidebarSessionsPerTier + 7
	metas := make([]schema.SessionMeta, 0, total)
	for i := range total {
		// Spread updated times so they don't cluster-fold (distinct titles too).
		metas = append(metas, schema.SessionMeta{
			ID:             fmt.Sprintf("01CUR%03d", i),
			OriginalPrompt: fmt.Sprintf("current task %d", i),
			CreatedAt:      now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt:      now.Add(-time.Duration(i) * time.Minute),
			EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/big"},
		})
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now), "big")
	if len(proj.Current) != maxSidebarSessionsPerTier {
		t.Errorf("Current capped len = %d, want %d", len(proj.Current), maxSidebarSessionsPerTier)
	}
	if proj.MoreCurrent != 7 {
		t.Errorf("MoreCurrent = %d, want 7", proj.MoreCurrent)
	}
	// The kept rows are the most-recent N: the very first (newest) survives.
	if proj.Current[0].ID != "01CUR000" {
		t.Errorf("Current[0] = %q, want most-recent 01CUR000", proj.Current[0].ID)
	}
}

func fuzzScenarioBuildTree_NoOverflowWhenUnderCap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "01A", OriginalPrompt: "a", CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/small"}},
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now), "small")
	if proj.MoreCurrent != 0 || proj.MoreRecent != 0 || proj.MoreArchived != 0 {
		t.Errorf("under-cap project should have zero overflow: more=%d/%d/%d",
			proj.MoreCurrent, proj.MoreRecent, proj.MoreArchived)
	}
}

func fuzzScenarioBuildProjectTree_ReturnsSingleProjectTiers(t *testing.T) {
	// The lazy-load helper rebuilds one canonical project's tiers from the full meta
	// set, ignoring sessions from other projects.
	now := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	wantedDir := filepath.Join(root, "wanted")
	otherDir := filepath.Join(root, "other")
	if err := os.MkdirAll(wantedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wanted, err := identifier.ResolveProject(wantedDir)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string, dir string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, OriginalPrompt: id,
			CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}}
	}
	metas := []schema.SessionMeta{
		mk("wanted-cur", wantedDir, 1*time.Hour),
		mk("wanted-rec", wantedDir, 48*time.Hour),
		mk("other-cur", otherDir, 1*time.Hour),
	}
	proj, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, wanted.ID)
	if !ok {
		t.Fatalf("BuildProjectTreeAt should find project %q", wanted.ID)
	}
	if proj.Name != "wanted" {
		t.Fatalf("got project %q, want 'wanted'", proj.Name)
	}
	if len(proj.Current) != 1 || proj.Current[0].ID != "wanted-cur" {
		t.Errorf("wanted Current = %v, want [wanted-cur]", proj.Current)
	}
	if len(proj.Recent) != 1 || proj.Recent[0].ID != "wanted-rec" {
		t.Errorf("wanted Recent = %v, want [wanted-rec]", proj.Recent)
	}
	// No leakage from "other".
	for _, n := range append(append([]TreeNode{}, proj.Current...), proj.Recent...) {
		if n.Project == "other" {
			t.Errorf("project tree leaked a session from 'other': %+v", n)
		}
	}
}

func fuzzScenarioBuildProjectTree_FindsArchivedProject(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	gammaDir := filepath.Join(t.TempDir(), "gamma")
	if err := os.MkdirAll(gammaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gamma, err := identifier.ResolveProject(gammaDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "g1", OriginalPrompt: "g1", CreatedAt: now.Add(-30 * 24 * time.Hour),
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: gammaDir}},
	}
	proj, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, gamma.ID)
	if !ok {
		t.Fatalf("BuildProjectTreeAt should find archived project %q", gamma.ID)
	}
	if len(proj.Archived) != 1 || proj.Archived[0].ID != "g1" {
		t.Errorf("gamma Archived = %v, want [g1]", proj.Archived)
	}
}

func fuzzScenarioBuildProjectTree_UsesCanonicalIDForSameBasename(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	olderDir := filepath.Join(root, "older", "shared")
	archivedDir := filepath.Join(root, "archived", "shared")
	newestDir := filepath.Join(root, "newest", "shared")
	for _, dir := range []string{olderDir, archivedDir, newestDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newest, err := identifier.ResolveProject(newestDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "active-older", OriginalPrompt: "active older", CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: olderDir}},
		{ID: "archived", OriginalPrompt: "archived", CreatedAt: now.Add(-30 * 24 * time.Hour),
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: archivedDir}},
		{ID: "active-newest", OriginalPrompt: "active newest", CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now.Add(-time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: newestDir}},
	}

	proj, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, newest.ID)
	if !ok {
		t.Fatalf("BuildProjectTreeAt should find project %q", newest.ID)
	}
	if proj.WorkingDir != newest.CanonicalPath {
		t.Fatalf("WorkingDir = %q, want canonical path %q", proj.WorkingDir, newest.CanonicalPath)
	}
}

func fuzzScenarioBuildProjectTree_UnknownProjectReturnsFalse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "a", OriginalPrompt: "a", CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/known"}},
	}
	if _, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, "nope"); ok {
		t.Errorf("BuildProjectTreeAt should return ok=false for unknown project")
	}
}

func fuzzScenarioClassifySession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	yes, no := true, false
	cases := []struct {
		name     string
		decision *bool
		age      time.Duration
		want     string
	}{
		{"fresh -> current", nil, 1 * time.Hour, "current"},
		{"yesterday -> recent", nil, 36 * time.Hour, "recent"},
		{"3 weeks -> archived (auto)", nil, 21 * 24 * time.Hour, "archived"},
		{"manual archive overrides fresh", &yes, 1 * time.Hour, "archived"},
		{"manual unarchive overrides old", &no, 30 * 24 * time.Hour, "recent"},
		{"boundary 24h -> current", nil, 24 * time.Hour, "current"},
		{"boundary 14d -> recent", nil, 14 * 24 * time.Hour, "recent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySession(c.decision, now.Add(-c.age), now)
			if got != c.want {
				t.Fatalf("classifySession=%q want %q", got, c.want)
			}
		})
	}
}

func fuzzScenarioNormalizeState(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "idle"},
		{"awaiting", "awaiting"},
		{"active", "active"},
		{"systemError", "errored"},
		{"warning", "warning"},
		{"idle", "idle"},
		{"closed", "ended"},
		{"notLoaded", "notLoaded"},
		{"ended", "ended"},
		{"unknown", "notLoaded"},
	}
	for _, c := range cases {
		if got := NormalizeState(c.in); got != c.want {
			t.Errorf("NormalizeState(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func fuzzScenarioAttentionRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"awaiting", 4}, {"active", 3}, {"warning", 2}, {"idle", 1}, {"ended", 0}, {"unknown", 0},
	}
	for _, c := range cases {
		if got := hubapi.AttentionRank(c.in); got != c.want {
			t.Errorf("hubapi.AttentionRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func fuzzScenarioRollupRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"awaiting", 4}, {"warning", 3}, {"active", 2}, {"idle", 1}, {"ended", 0}, {"unknown", 0},
	}
	for _, c := range cases {
		if got := hubapi.RollupRank(c.in); got != c.want {
			t.Errorf("hubapi.RollupRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func fuzzScenarioAgeString(t *testing.T) {
	// AgeString calls time.Since internally so we keep inputs well within each
	// bucket (≥10s of margin). The 59s boundary cannot be reliably tested
	// without clock injection — use 45s (15s margin) to stay in "now".
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-45 * time.Second), "now"}, // was 59s (1s margin → flaky); 45s gives 15s margin
		{now.Add(-75 * time.Second), "1m"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-59 * time.Minute), "59m"},
		{now.Add(-61 * time.Minute), "1h"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-23 * time.Hour), "23h"},
		{now.Add(-25 * time.Hour), "1d"},
		{now.Add(-2 * 24 * time.Hour), "2d"},
	}
	for _, c := range cases {
		if got := AgeString(c.t); got != c.want {
			t.Errorf("AgeString(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func fuzzScenarioShortID(t *testing.T) {
	if got := ShortID("01A"); got != "01A" {
		t.Errorf("ShortID(01A) = %q, want 01A", got)
	}
	// 14 chars is the boundary: still passes through unchanged.
	if got := ShortID("0123456789ABCD"); got != "0123456789ABCD" {
		t.Errorf("ShortID(14-char) = %q, want 0123456789ABCD", got)
	}
	// 15 chars is one past the boundary: shortened to "session "+last6.
	if got := ShortID("0123456789ABCDE"); got != "session 9ABCDE" {
		t.Errorf("ShortID(15-char) = %q, want session 9ABCDE", got)
	}
	if got := ShortID("01ABCDEFGHIJKLMNOPQRSTUVWXYZ"); got != "session UVWXYZ" {
		t.Errorf("ShortID(long) = %q, want session UVWXYZ", got)
	}
}

func fuzzScenarioNodeTitle_EmptyBaseWithForkLabel(t *testing.T) {
	m := schema.SessionMeta{ID: "", Name: "", OriginalPrompt: ""}
	got := nodeTitle(m, "fork")
	if got != "" {
		t.Errorf("nodeTitle(empty base, fork) = %q, want \"\"", got)
	}
	m.ForkLabel = "before"
	got = nodeTitle(m, "fork")
	if got != " · before" {
		t.Errorf("nodeTitle(empty base with label, fork) = %q, want \" · before\"", got)
	}

	// Realistic named session with a fork label renders "<name> · <label>".
	named := schema.SessionMeta{Name: "deploy", ForkLabel: "before"}
	if got := nodeTitle(named, "fork"); got != "deploy · before" {
		t.Errorf("nodeTitle(named, fork) = %q, want \"deploy · before\"", got)
	}
	// A non-fork node omits the fork label even when one is present.
	if got := nodeTitle(named, "session"); got != "deploy" {
		t.Errorf("nodeTitle(named, session) = %q, want \"deploy\"", got)
	}
}

// ShortID exists so an unnamed session does not spell its whole 22-character
// base62 payload across a one-line rail row, and nodeTitle's own doc comment
// says so. It never reached a session that has a meta.
//
// SessionDisplayName's last resort is the bare ID, which is non-empty, so
// nodeTitle's `base == ""` guard could only fire for a session with no ID at
// all. The visible consequence was a title that CHANGED for no reason a reader
// could see: the live tier renders ShortID while the past index is still
// catching up (tree.go's `node.Title = ShortID(le.SessionID)`), so a freshly
// spawned session read "session ..." for a few seconds and then swapped itself
// for the raw payload the moment its meta landed. Dormant spawn (kata ytpa)
// made unnamed sessions ordinary rather than legacy-only, so that swap is now
// something a user watches happen.
//
// The fix belongs here and not in SessionDisplayName: eight other callers
// (agent tools, transcript render, appwire preview) rely on its bare-ID last
// resort, and the rail is the only surface with a one-line budget.
func fuzzScenarioNodeTitle_UnnamedSessionKeepsTheShortForm(t *testing.T) {
	const id = "033vq9Kif27AzZgnbjr55t" // a real 22-char UUIDv7 base62 payload

	unnamed := schema.SessionMeta{ID: id}
	if got, want := nodeTitle(unnamed, "session"), ShortID(id); got != want {
		t.Errorf("nodeTitle(unnamed) = %q, want %q", got, want)
	}
	if got := nodeTitle(unnamed, "session"); got == id {
		t.Errorf("nodeTitle(unnamed) = %q — the raw payload, which is what ShortID exists to avoid", got)
	}

	// The short form is a base like any other, so a fork label still composes.
	forked := schema.SessionMeta{ID: id, ForkLabel: "before"}
	if got, want := nodeTitle(forked, "fork"), ShortID(id)+" · before"; got != want {
		t.Errorf("nodeTitle(unnamed fork) = %q, want %q", got, want)
	}

	// A session that HAS something to say still says it: the ID fallback must
	// stay strictly last, behind both the generated name and the prompt.
	named := schema.SessionMeta{ID: id, Name: "Deploy the hub"}
	if got := nodeTitle(named, "session"); got != "Deploy the hub" {
		t.Errorf("nodeTitle(named) = %q, want the name", got)
	}
	prompted := schema.SessionMeta{ID: id, OriginalPrompt: "fix the flaky test"}
	if got := nodeTitle(prompted, "session"); got != "fix the flaky test" {
		t.Errorf("nodeTitle(prompted) = %q, want the prompt", got)
	}

	// A short ID is already legible, so ShortID returns it whole - and
	// nodeTitle must not mistake that for "no fallback applied" and hand back
	// the raw value by a different route. Same result either way here; the
	// assertion pins that the two agree.
	shortIDMeta := schema.SessionMeta{ID: "01ABC"}
	if got := nodeTitle(shortIDMeta, "session"); got != "01ABC" {
		t.Errorf("nodeTitle(short id) = %q, want 01ABC", got)
	}
}

// The end-to-end half: one unnamed session, listed in both tiers, must carry
// the same title in both. This is the assertion that would have caught the
// reported symptom, which nodeTitle's unit test alone cannot see - the live
// tier reaches ShortID by a completely separate line of code.
func fuzzScenarioBuildTree_UnnamedSessionTitleSurvivesItsMetaLanding(t *testing.T) {
	const id = "033vq9Kif27AzZgnbjr55t"
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	live := []LiveEntry{{
		Entry:     rendezvous.Entry{PID: 1},
		SessionID: id,
		Status:    appwire.ThreadStatusIdle,
	}}

	// Before the past index catches up there is no meta at all, and the live
	// tier renders the short form on its own.
	beforeTree := buildTree(nil, live)
	if len(beforeTree.Live) != 1 {
		t.Fatalf("Live tier has %d rows, want 1", len(beforeTree.Live))
	}
	if got := beforeTree.Live[0].Title; got != ShortID(id) {
		t.Fatalf("no-meta Live title = %q, want %q", got, ShortID(id))
	}

	// Once the meta lands, both listings must still read the same thing. A
	// title that changes here is a row rewriting itself under the reader.
	afterTree := buildTree([]schema.SessionMeta{{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
	}}, live)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(afterTree, id)
	if !inLive || !inProject {
		t.Fatalf("session missing: live=%v project=%v", inLive, inProject)
	}
	if liveRow.Title != beforeTree.Live[0].Title {
		t.Errorf("Live title changed when the meta landed: %q -> %q", beforeTree.Live[0].Title, liveRow.Title)
	}
	if projectRow.Title != liveRow.Title {
		t.Errorf("project row title %q, Live row title %q — the two listings of one session disagree",
			projectRow.Title, liveRow.Title)
	}
}

func fuzzScenarioCompareOrderText(t *testing.T) {
	if got := compareOrderText("alpha", "beta"); got != -1 {
		t.Errorf("compareOrderText(alpha, beta) = %d, want -1", got)
	}
	if got := compareOrderText("beta", "alpha"); got != 1 {
		t.Errorf("compareOrderText(beta, alpha) = %d, want 1", got)
	}
	if got := compareOrderText("Alpha", "alpha"); got != -1 {
		t.Errorf("compareOrderText(Alpha, alpha) = %d, want -1", got)
	}
	if got := compareOrderText("  alpha  ", "alpha"); got != 0 {
		t.Errorf("compareOrderText( alpha , alpha) = %d, want -1", got)
	}
}

func fuzzScenarioOrderUpdatedAt(t *testing.T) {
	now := time.Now()
	if got := OrderUpdatedAt(now.Add(time.Hour), now); got != now.Add(time.Hour) {
		t.Errorf("OrderUpdatedAt(updated, created) = %v, want updated", got)
	}
	if got := OrderUpdatedAt(time.Time{}, now); got != now {
		t.Errorf("OrderUpdatedAt(zero, created) = %v, want created", got)
	}
}

func fuzzScenarioOrderCreatedAt(t *testing.T) {
	now := time.Now()
	if got := OrderCreatedAt(now.Add(time.Hour), now); got != now.Add(time.Hour) {
		t.Errorf("OrderCreatedAt(created, updated) = %v, want created", got)
	}
	if got := OrderCreatedAt(time.Time{}, now); got != now {
		t.Errorf("OrderCreatedAt(zero, updated) = %v, want updated", got)
	}
}

func fuzzScenarioAttentionRanks_Errored(t *testing.T) {
	if hubapi.AttentionRank("errored") <= hubapi.AttentionRank("awaiting") {
		t.Fatal("errored must outrank awaiting")
	}
	if hubapi.RollupRank("errored") <= hubapi.RollupRank("awaiting") {
		t.Fatal("RollupRank: errored must outrank awaiting")
	}
}

func fuzzScenarioNeedsYou_AdmitsErroredAndWarning_RanksErroredFirst(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01AWAIT", UpdatedAt: now.Add(-1 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01WARN", UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01AWAIT", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01WARN", Status: appwire.ThreadStatusWarning},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 3 {
		t.Fatalf("NeedsYou len = %d, want 3", len(tree.NeedsYou))
	}
	if tree.NeedsYou[0].ID != "01ERR" || tree.NeedsYou[0].State != "errored" {
		t.Fatalf("[0] = %s/%s, want 01ERR/errored (errors first, real state on node)", tree.NeedsYou[0].ID, tree.NeedsYou[0].State)
	}
	// Then oldest-first among the amber family: WARN (-2h) before AWAIT (-1h).
	if tree.NeedsYou[1].ID != "01WARN" || tree.NeedsYou[2].ID != "01AWAIT" {
		t.Fatalf("amber order = %s,%s want 01WARN,01AWAIT", tree.NeedsYou[1].ID, tree.NeedsYou[2].ID)
	}
}

func fuzzScenarioNeedsYou_ArchivedLiveAwaitingExcluded(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ARCH", Status: appwire.ThreadStatusAwaiting}}
	tree := BuildTree(metas, live, map[ArchiveKey]bool{{Kind: "session", ID: "01ARCH"}: true})
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("archived live awaiting session must not appear in NeedsYou; got %d", len(tree.NeedsYou))
	}
}

func fuzzScenarioCoBasenameProjectsAreDistinctNodes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	aFoo := filepath.Join(root, "a", "foo")
	bFoo := filepath.Join(root, "b", "foo")
	if err := os.MkdirAll(aFoo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bFoo, 0o755); err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: aFoo}},
		{ID: "01B", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: bFoo}},
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 2 {
		t.Fatalf("want 2 distinct projects for co-basename dirs, got %d: %+v", len(tree.Projects), tree.Projects)
	}
	keys := map[string]string{}
	for _, p := range tree.Projects {
		if p.Name != "foo" {
			t.Fatalf("both projects display basename foo, got %q", p.Name)
		}
		keys[p.Key] = p.WorkingDir
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 distinct Keys, got %v", keys)
	}
	for key := range keys {
		if err := identifier.ValidateProjectID(key); err != nil {
			t.Fatalf("project key %q is not canonical: %v", key, err)
		}
	}
}

func fuzzScenarioNoProjectKeyIsStable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{{ID: "01A", CreatedAt: now, UpdatedAt: now}}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 1 || tree.Projects[0].Key != "no-project" || tree.Projects[0].Name != "(no project)" {
		t.Fatalf("want a single (no project)/no-project node, got %+v", tree.Projects)
	}
}

func fuzzScenarioProjectArchiveDecisionUsesCanonicalID(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	aFoo := filepath.Join(root, "a", "foo")
	bFoo := filepath.Join(root, "b", "foo")
	if err := os.MkdirAll(aFoo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bFoo, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(id, wd string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
	}
	projects := ResolveProjectMap([]schema.SessionMeta{mk("01A", aFoo), mk("01B", bFoo)}, nil)
	aID := projects[aFoo].ID
	legacy := map[ArchiveKey]bool{{Kind: "project", ID: aID}: true}
	tree := BuildTreeAtWithProjects([]schema.SessionMeta{mk("01A", aFoo), mk("01B", bFoo)}, nil, legacy, now, projects)
	if len(tree.Projects) != 1 || len(tree.ArchivedProjects) != 1 {
		t.Fatalf("canonical row should archive only one clone; got projects=%d archived=%d", len(tree.Projects), len(tree.ArchivedProjects))
	}
	precedence := map[ArchiveKey]bool{{Kind: "project", ID: aID}: false}
	tree = BuildTreeAtWithProjects([]schema.SessionMeta{mk("01A", aFoo), mk("01B", bFoo)}, nil, precedence, now, projects)
	if len(tree.Projects) != 2 {
		t.Fatalf("canonical unarchive should leave both active; got %+v", tree)
	}
}

func fuzzScenarioTwoClustersInOneProjectGetDistinctIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := now.Add(-30 * 24 * time.Hour) // ended, clusterable
	mk := func(id, title string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, Name: title, CreatedAt: old, UpdatedAt: old, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"}}
	}
	metas := []schema.SessionMeta{
		mk("01A", "alpha"), mk("01B", "alpha"), mk("01C", "alpha"),
		mk("01D", "beta"), mk("01E", "beta"), mk("01F", "beta"),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	var clusters []TreeNode
	for _, p := range tree.ArchivedProjects {
		for _, n := range append(append([]TreeNode{}, p.Archived...), p.Recent...) {
			if n.Kind == "cluster" {
				clusters = append(clusters, n)
			}
		}
	}
	if len(clusters) != 2 {
		t.Fatalf("want 2 clusters, got %d", len(clusters))
	}
	if clusters[0].ID == "" || clusters[1].ID == "" || clusters[0].ID == clusters[1].ID {
		t.Fatalf("clusters need distinct non-empty IDs: %q vs %q", clusters[0].ID, clusters[1].ID)
	}
	for _, c := range clusters {
		if !strings.HasPrefix(c.ID, "cluster:") {
			t.Fatalf("cluster ID must be cluster:<hex>, got %q", c.ID)
		}
	}
}

func fuzzScenarioSubagentChildrenCappedPerTier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	parent := schema.SessionMeta{ID: "01P", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"}}
	metas := []schema.SessionMeta{parent}
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01S%02d", i), IsSubagent: true, ParentSessionID: "01P",
			CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"},
		})
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	var subs int
	for _, p := range tree.Projects {
		for _, n := range p.Current {
			if n.ID == "01P" {
				subs = len(n.Children)
			}
		}
	}
	if subs != maxSidebarSessionsPerTier {
		t.Fatalf("subagent children should cap at %d, got %d", maxSidebarSessionsPerTier, subs)
	}
	if got := tree.Projects[0].Current[0].MoreSubagents; got != 10 {
		t.Fatalf("subagent overage = %d, want 10", got)
	}
}

func TestSubagentChildrenCarryOverage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{{ID: "01P", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"}}}
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01S%02d", i), IsSubagent: true, ParentSessionID: "01P",
			CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"},
		})
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	parent := tree.Projects[0].Current[0]
	if len(parent.Children) != maxSidebarSessionsPerTier {
		t.Fatalf("children = %d, want %d", len(parent.Children), maxSidebarSessionsPerTier)
	}
	if got := parent.MoreSubagents; got != 10 {
		t.Fatalf("subagent overage = %d, want 10", got)
	}
}

func TestTreeProjectPageReturnsCappedAwayTierRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := make([]schema.SessionMeta, 0, 60)
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01PAGE%02d", i), CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/page"},
		})
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(tree.Projects))
	}
	project := tree.Projects[0]
	if len(project.Current) != maxSidebarSessionsPerTier || project.MoreCurrent != 10 {
		t.Fatalf("capped project = %d rows + %d more, want 50 + 10", len(project.Current), project.MoreCurrent)
	}
	page, remaining, ok := project.Page("current", maxSidebarSessionsPerTier, maxSidebarSessionsPerTier)
	if !ok {
		t.Fatal("current tier page was rejected")
	}
	if len(page) != 10 || remaining != 0 {
		t.Fatalf("page = %d rows + %d remaining, want 10 + 0", len(page), remaining)
	}
	if page[0].ID == project.Current[0].ID {
		t.Fatalf("page repeated a retained row %q", page[0].ID)
	}
}

func fuzzScenarioAllTestSessionsClassifyAsTestRun(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, origin string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, Origin: origin, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/tp"}}
	}
	tree := BuildTreeAt([]schema.SessionMeta{mk("01A", "test"), mk("01B", "test")}, nil, map[ArchiveKey]bool{}, now)
	var testRun *TreeProject
	for i := range tree.Projects {
		if tree.Projects[i].IsTestRun {
			testRun = &tree.Projects[i]
		}
	}
	if testRun == nil {
		t.Fatalf("all-test project should be flagged IsTestRun; projects=%+v", tree.Projects)
	}
	// One unmarked session reclassifies the project (hiding real work is worse).
	tree = BuildTreeAt([]schema.SessionMeta{mk("01A", "test"), mk("01B", "")}, nil, map[ArchiveKey]bool{}, now)
	for _, p := range tree.Projects {
		if p.IsTestRun {
			t.Fatalf("a mixed project must not be IsTestRun")
		}
	}
}

func fuzzScenarioNeedsYou_CarriesAskPendingFromLiveEntry(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 1 || !tree.NeedsYou[0].AskPending {
		t.Fatalf("NeedsYou node must carry AskPending=true, got %+v", tree.NeedsYou)
	}
}

// TestLiveTier_CarriesAskPendingFromLiveEntry guards against the live-tier
// TreeNode builder silently dropping AskPending: a session that's ask-pending
// in NeedsYou must show the same marker in the flat Live rail, or the sidebar
// disagrees with itself about the same session.
func fuzzScenarioLiveTier_CarriesAskPendingFromLiveEntry(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}}
	tree := buildTree(metas, live)
	if len(tree.Live) != 1 || !tree.Live[0].AskPending {
		t.Fatalf("Live node must carry AskPending=true, got %+v", tree.Live)
	}
}

// TestProjectTier_CarriesAskPendingFromLiveEntry guards against the
// per-project TreeNode builder silently dropping AskPending: the same
// ask-pending session rendered under its project (Current tier) must carry
// the marker too, or the project row disagrees with its NeedsYou tile.
func fuzzScenarioProjectTier_CarriesAskPendingFromLiveEntry(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}}
	tree := buildTree(metas, live)
	if len(tree.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d: %+v", len(tree.Projects), tree.Projects)
	}
	sessions := allSessions(tree.Projects[0])
	if len(sessions) != 1 || !sessions[0].AskPending {
		t.Fatalf("project session node must carry AskPending=true, got %+v", sessions)
	}
}

func fuzzScenarioNeedsYou_AskPendingBandsBetweenErroredAndYourMove(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01OLD_YOURMOVE", UpdatedAt: now.Add(-3 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ASK", UpdatedAt: now.Add(-1 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01OLD_YOURMOVE", Status: appwire.ThreadStatusAwaiting, PendingAsk: false},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting, PendingAsk: true},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 3 {
		t.Fatalf("NeedsYou len = %d, want 3", len(tree.NeedsYou))
	}
	// errored first, then ask-pending (even though it is newer than the
	// your-move row), then your-move last — despite 01OLD_YOURMOVE being the
	// oldest-updated of all three.
	got := []string{tree.NeedsYou[0].ID, tree.NeedsYou[1].ID, tree.NeedsYou[2].ID}
	want := []string{"01ERR", "01ASK", "01OLD_YOURMOVE"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("band order = %v, want %v", got, want)
		}
	}
}

// fuzzScenarioNeedsYou_PendingEscalationUnifiesWithAttentionSummary is the
// wave-6 wire-honesty regression: attention.go's AttentionSummary already
// promoted a live, top-level, active session with a pending sandbox-
// exemption escalation (M7) into needs_you, but BuildTree's needs-you tier
// had no matching promotion — the escalation lit the web title/favicon
// (summary-driven) but never appeared in needs_you[], so the notifications
// engine never edge-fired OS/sound for it. Both now derive inclusion from
// the same promotedAttentionLevel call (attention.go), so the tier and the
// summary can't drift apart again. 01SUB (subagent) and 01ARCH (manually
// archived) prove the promotion is additive, not a widening of who's
// tier-eligible in the first place.
func fuzzScenarioNeedsYou_PendingEscalationUnifiesWithAttentionSummary(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01SUB", UpdatedAt: now, ParentSessionID: "01A", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusActive, PendingEscalation: true},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB", Status: appwire.ThreadStatusActive, PendingEscalation: true},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ARCH", Status: appwire.ThreadStatusActive, PendingEscalation: true},
	}
	decisions := map[ArchiveKey]bool{{Kind: "session", ID: "01ARCH"}: true}

	tree := BuildTree(metas, live, decisions)
	if len(tree.NeedsYou) != 1 || tree.NeedsYou[0].ID != "01A" {
		t.Fatalf("NeedsYou = %+v, want exactly [01A] (active session promoted by its own pending escalation)", tree.NeedsYou)
	}
	if tree.NeedsYou[0].State != "active" {
		t.Fatalf("NeedsYou[0].State = %q, want the real underlying state (active) — promotion changes membership, not the node's reported state", tree.NeedsYou[0].State)
	}

	_, sum := DeriveAttention(metas, live, decisions)
	if sum.NeedsYou != 1 || sum.Error != 0 {
		t.Fatalf("summary = %+v, want NeedsYou:1 Error:0 (01SUB excluded as a subagent, 01ARCH excluded as archived)", sum)
	}
	if got, want := len(tree.NeedsYou), sum.NeedsYou+sum.Error; got != want {
		t.Fatalf("tree.NeedsYou has %d entries but the summary counts %d (needsYou+error) for the same inputs — the tier and the badge disagree", got, want)
	}
}

func TestNeedsYou_PendingEscalationUnifiesWithAttentionSummary(t *testing.T) {
	fuzzScenarioNeedsYou_PendingEscalationUnifiesWithAttentionSummary(t)
}

// fuzzScenarioNeedsYou_ForkSupersededParentUnifiesWithAttentionSummary is the
// round-4-review regression: the escalation-promotion unification (above)
// shared *which states count*, but left *which sessions are even eligible*
// as two independent copies. DeriveAttention (attention.go) excluded only
// IsSubagent; BuildTree's needs-you tier also excludes a fork-superseded
// parent — the snapshotted original of an edited message (ForkLabel set),
// nested under the active branch that superseded it (see
// TestBuildTree_ExcludesNestedForkFromNeedsYou). So a live, awaiting,
// non-subagent, ForkLabel'd parent with a live active continuation was
// counted by AttentionSummary (badge) but absent from needs_you[] (list) —
// same bug class, different axis. Both now derive eligibility from the same
// nestedSessionIDs + tierEligible calls, so a session can't be top-level to
// one and nested to the other.
func fuzzScenarioNeedsYou_ForkSupersededParentUnifiesWithAttentionSummary(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		// "branch" is the active continuation: it forked from "fork" (edited a
		// message), so it carries ParentSessionID but no ForkLabel of its own.
		{ID: "branch", ParentSessionID: "fork", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		// "fork" is the snapshotted original — superseded, renders nested under
		// "branch" — but it's still live and awaiting.
		{ID: "fork", ForkLabel: "before edit", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "branch", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "fork", Status: appwire.ThreadStatusAwaiting},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("nested fork leaked into NeedsYou: %#v", tree.NeedsYou)
	}

	_, sum := DeriveAttention(metas, live, nil)
	if sum.NeedsYou != 0 {
		t.Fatalf("summary = %+v, want NeedsYou:0 (the fork-superseded parent is nested, not tier-eligible — same exclusion as the tree)", sum)
	}
	if got, want := len(tree.NeedsYou), sum.NeedsYou+sum.Error; got != want {
		t.Fatalf("tree.NeedsYou has %d entries but the summary counts %d (needsYou+error) for the same inputs — the tier and the badge disagree", got, want)
	}
}

func TestNeedsYou_ForkSupersededParentUnifiesWithAttentionSummary(t *testing.T) {
	fuzzScenarioNeedsYou_ForkSupersededParentUnifiesWithAttentionSummary(t)
}
