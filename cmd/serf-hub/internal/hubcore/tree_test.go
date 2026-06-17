package hubcore

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildTree_GroupsByProjectWithSubagentsAndForks(t *testing.T) {
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
		{ID: "01OTHER", UpdatedAt: now.Add(-15 * time.Minute), OriginalPrompt: "htmx swap",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ACTIVE", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB1", Status: appwire.ThreadStatusActive},
	}

	tree := BuildTree(metas, live)

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
	if len(proj.Sessions) != 2 {
		t.Fatalf("sessions: %d", len(proj.Sessions))
	}
	if proj.Sessions[0].ID != "01ACTIVE" {
		t.Errorf("[0]: %q", proj.Sessions[0].ID)
	}
	if proj.Sessions[1].ID != "01OTHER" {
		t.Errorf("[1]: %q", proj.Sessions[1].ID)
	}

	// Children of 01ACTIVE: subagent first, then the snapshotted original (fork).
	children := proj.Sessions[0].Children
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
	if len(proj.Sessions[1].Children) != 0 {
		t.Errorf("01OTHER should have no children, got %d", len(proj.Sessions[1].Children))
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

func TestBuildTree_UsesGeneratedNameForSessionTitle(t *testing.T) {
	tree := BuildTree([]schema.SessionMeta{{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := tree.Projects[0].Sessions[0].Title; got != "Launch Config Cheap Model" {
		t.Fatalf("session title = %q, want generated name", got)
	}
}

func TestBuildTree_UsesGeneratedNameForForkBaseTitle(t *testing.T) {
	tree := BuildTree([]schema.SessionMeta{{
		ID:             "01FORK",
		Name:           "Generated Base",
		OriginalPrompt: "original base",
		ForkLabel:      "before TDD",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := tree.Projects[0].Sessions[0].Title; got != "Generated Base · before TDD" {
		t.Fatalf("fork title = %q, want generated name plus label", got)
	}
}

func TestBuildTree_AttentionSortsLive(t *testing.T) {
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

	tree := BuildTree(metas, live)

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

func TestBuildTree_OrdersProjectSessionsByUpdatedCreatedTitleAndID(t *testing.T) {
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "02OLD", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01NEW", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "03TITLEB", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "04TITLEA", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}

	tree := BuildTree(metas, nil)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects=%d", len(tree.Projects))
	}
	got := make([]string, 0, len(tree.Projects[0].Sessions))
	for _, node := range tree.Projects[0].Sessions {
		got = append(got, node.ID)
	}
	want := []string{"01NEW", "02OLD", "04TITLEA", "03TITLEB"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func TestBuildTree_OrdersLiveRowsWithoutMetasByStartedAtAndID(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1, StartedAt: base.Add(-time.Hour)}, SessionID: "02OLD", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 2, StartedAt: base}, SessionID: "01NEW", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "04TIEA", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "03TIEB", Status: appwire.ThreadStatusIdle},
	}

	tree := BuildTree(nil, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"01NEW", "02OLD", "03TIEB", "04TIEA"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func TestBuildTree_OrdersMixedLiveRowsByMergedMetadata(t *testing.T) {
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

	tree := BuildTree(metas, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"01META", "02FRESH"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func TestBuildTree_NoProjectFallback(t *testing.T) {
	// A meta with empty WorkingDir — project name "(no project)".
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01NOPROJ", UpdatedAt: now, OriginalPrompt: "orphan task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: ""}},
	}
	live := []LiveEntry{}

	tree := BuildTree(metas, live)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	if tree.Projects[0].Name != "(no project)" {
		t.Errorf("project name: %q", tree.Projects[0].Name)
	}
	if len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("sessions: %d", len(tree.Projects[0].Sessions))
	}
	if tree.Projects[0].Sessions[0].ID != "01NOPROJ" {
		t.Errorf("session id: %q", tree.Projects[0].Sessions[0].ID)
	}
	// Not live => state = "ended"
	if tree.Projects[0].Sessions[0].State != "ended" {
		t.Errorf("state: %q", tree.Projects[0].Sessions[0].State)
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

// projectByName finds the TreeProject with the given name, or fails.
func projectByName(t *testing.T, tree Tree, name string) TreeProject {
	t.Helper()
	for _, p := range tree.Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("project %q not found in %v", name, projectNames(tree))
	return TreeProject{}
}

func projectNames(tree Tree) []string {
	names := make([]string, 0, len(tree.Projects))
	for _, p := range tree.Projects {
		names = append(names, p.Name)
	}
	return names
}

func TestBuildTree_TiersProjectsByRecencyAndLiveness(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		// A live project — has an awaiting live session => ACTIVE tier.
		{ID: "01LIVE", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "live work",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		// Touched 3h ago, not live => RECENT tier.
		{ID: "01RECENT", UpdatedAt: now.Add(-3 * time.Hour), OriginalPrompt: "recent work",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/bravo"}},
		// Touched 5 days ago, not live => OLDER tier.
		{ID: "01OLD", UpdatedAt: now.Add(-5 * 24 * time.Hour), OriginalPrompt: "old work",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/charlie"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting},
	}

	tree := BuildTree(metas, live)

	if got := projectByName(t, tree, "alpha").Tier; got != TierActive {
		t.Errorf("alpha tier = %q, want %q", got, TierActive)
	}
	if got := projectByName(t, tree, "bravo").Tier; got != TierRecent {
		t.Errorf("bravo tier = %q, want %q", got, TierRecent)
	}
	if got := projectByName(t, tree, "charlie").Tier; got != TierOlder {
		t.Errorf("charlie tier = %q, want %q", got, TierOlder)
	}
}

func TestBuildTree_BucketsE2eTestRunsIntoTestTier(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		// A real, freshly-touched project — should NOT be in the test tier
		// even though it sorts most-recent.
		{ID: "01REAL", UpdatedAt: now, OriginalPrompt: "real work",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	// Many serf-e2e-* throwaway projects, each a single session, some very recent.
	for i := 0; i < 25; i++ {
		metas = append(metas, schema.SessionMeta{
			ID:             "01E2E" + string(rune('A'+i)),
			UpdatedAt:      now.Add(-time.Duration(i) * time.Minute),
			OriginalPrompt: "e2e probe",
			EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-" + string(rune('a'+i))},
		})
	}

	tree := BuildTree(metas, nil)

	// Every serf-e2e-* project lands in the test tier.
	testCount := 0
	for _, p := range tree.Projects {
		if strings.HasPrefix(p.Name, "serf-e2e-") {
			if p.Tier != TierTest {
				t.Errorf("project %q tier = %q, want %q", p.Name, p.Tier, TierTest)
			}
			testCount++
		}
	}
	if testCount != 25 {
		t.Errorf("expected 25 serf-e2e projects, got %d", testCount)
	}

	// The real project is NOT in the test tier.
	if got := projectByName(t, tree, "serf").Tier; got != TierActive && got != TierRecent {
		t.Errorf("real project tier = %q, want active or recent (not test)", got)
	}
}

func TestBuildTree_RecentEvenWhenE2eSortsFirst(t *testing.T) {
	// A serf-e2e run touched *now* must not push the real project below it:
	// the real project must still be reachable (active/recent), and the e2e
	// run must be bucketed into the test tier regardless of recency.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01REAL", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "real",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01E2E", UpdatedAt: now, OriginalPrompt: "probe",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-zzz"}},
	}
	tree := BuildTree(metas, nil)
	if got := projectByName(t, tree, "serf-e2e-zzz").Tier; got != TierTest {
		t.Errorf("e2e tier = %q, want %q", got, TierTest)
	}
	if got := projectByName(t, tree, "serf").Tier; got != TierRecent {
		t.Errorf("real tier = %q, want %q", got, TierRecent)
	}
}

func TestTierGroups_OmitsEmptyAndAutoExpandsActive(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01OLD", UpdatedAt: now.Add(-9 * 24 * time.Hour), OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/charlie"}},
		{ID: "01E2E", UpdatedAt: now, OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-a"}},
	}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive}}

	groups := BuildTree(metas, live).TierGroups()

	// active, older, test — recent is omitted (no project qualifies).
	gotTiers := make([]string, 0, len(groups))
	for _, g := range groups {
		gotTiers = append(gotTiers, g.Tier)
	}
	want := []string{TierActive, TierOlder, TierTest}
	if strings.Join(gotTiers, ",") != strings.Join(want, ",") {
		t.Fatalf("tier groups = %v, want %v", gotTiers, want)
	}

	// Only the active tier auto-expands.
	for _, g := range groups {
		if g.Tier == TierActive && !g.Expanded {
			t.Errorf("active tier should be expanded")
		}
		if g.Tier != TierActive && g.Expanded {
			t.Errorf("%q tier should not be expanded", g.Tier)
		}
	}

	// Test tier label is "Test runs" (sentence-case sans, mockup #2).
	for _, g := range groups {
		if g.Tier == TierTest && g.Label != "Test runs" {
			t.Errorf("test tier label = %q, want %q", g.Label, "Test runs")
		}
	}
}

func TestTreeProject_AgeIsFormatted(t *testing.T) {
	now := time.Now()
	tree := BuildTree([]schema.SessionMeta{
		{ID: "01A", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil)
	if got := projectByName(t, tree, "alpha").Age; got != "2h" {
		t.Errorf("age = %q, want 2h", got)
	}
}

func TestTestRunsDateGroups_BucketsByRecency(t *testing.T) {
	// mockup #12 rec B: the Test runs bucket sub-groups its runs by date
	// (Today / Yesterday / Older) so "the one I ran this morning" is reachable
	// by structure, no typing.
	now := time.Date(2026, 6, 17, 15, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01TODAY", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "probe",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-today"}},
		{ID: "01YDAY", UpdatedAt: now.Add(-26 * time.Hour), OriginalPrompt: "probe",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-yesterday"}},
		{ID: "01OLD", UpdatedAt: now.Add(-5 * 24 * time.Hour), OriginalPrompt: "probe",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-old"}},
	}
	tree := BuildTree(metas, nil)
	var test *TierGroup
	for i := range tree.TierGroups() {
		g := tree.TierGroups()[i]
		if g.Tier == TierTest {
			test = &g
			break
		}
	}
	if test == nil {
		t.Fatal("no test tier")
	}
	groups := test.DateGroupsAt(now)
	if len(groups) != 3 {
		t.Fatalf("date groups = %d, want 3 (Today/Yesterday/Older)", len(groups))
	}
	wantLabels := []string{"Today", "Yesterday", "Older"}
	for i, g := range groups {
		if g.Label != wantLabels[i] {
			t.Errorf("group[%d] label = %q, want %q", i, g.Label, wantLabels[i])
		}
		if len(g.Projects) != 1 {
			t.Errorf("group[%d] %q has %d projects, want 1", i, g.Label, len(g.Projects))
		}
	}
}

func TestTestRunsDateGroups_OmitsEmptyBuckets(t *testing.T) {
	now := time.Date(2026, 6, 17, 15, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01OLD", UpdatedAt: now.Add(-9 * 24 * time.Hour), OriginalPrompt: "probe",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-old"}},
	}
	tree := BuildTree(metas, nil)
	for _, g := range tree.TierGroups() {
		if g.Tier != TierTest {
			continue
		}
		dg := g.DateGroupsAt(now)
		if len(dg) != 1 || dg[0].Label != "Older" {
			t.Fatalf("date groups = %v, want a single Older bucket", dg)
		}
	}
}

func TestBuildTree_RollupMagnitudeCountsLiveAndAttention(t *testing.T) {
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
		{ID: "01ZZZ", UpdatedAt: now.Add(-3 * time.Minute), OriginalPrompt: "idle one",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01WORK1", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01WORK2", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 4}, SessionID: "01ZZZ", Status: appwire.ThreadStatusIdle},
	}
	proj := projectByName(t, BuildTree(metas, live), "serf")
	// 2 working (active) sessions, 1 awaiting (needs-you). Idle does not count
	// toward either magnitude — it's the settled state.
	if proj.RollupLive != 2 {
		t.Errorf("RollupLive = %d, want 2", proj.RollupLive)
	}
	if proj.RollupAttn != 1 {
		t.Errorf("RollupAttn = %d, want 1", proj.RollupAttn)
	}
}

func TestBuildTree_NeedsYouAggregatesAwaitingAcrossProjects(t *testing.T) {
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
	tree := BuildTree(metas, live)
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

func TestBuildTree_NeedsYouEmptyWhenNothingAwaits(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
	}
	if got := len(BuildTree(metas, live).NeedsYou); got != 0 {
		t.Errorf("NeedsYou should be empty when nothing awaits, got %d", got)
	}
}

func TestBuildTree_ClustersRepeatedIdleTitles(t *testing.T) {
	// mockup #10/#C rec: a run of same-titled idle sessions collapses to one
	// cluster row; live/needs-you sessions stay un-clustered so signal isn't
	// hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := 0; i < 5; i++ {
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

	proj := projectByName(t, BuildTree(metas, nil), "serf-docs")
	var clusters, singles int
	for _, s := range proj.Sessions {
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

func TestBuildTree_DoesNotClusterLiveRepeatedTitles(t *testing.T) {
	// A live/needs-you member must keep all repeated-title sessions un-clustered
	// so live signal is never hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := 0; i < 4; i++ {
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
	proj := projectByName(t, BuildTree(metas, live), "serf-docs")
	for _, s := range proj.Sessions {
		if s.Kind == "cluster" {
			t.Fatalf("repeated titles must NOT cluster when a member is live: got cluster %q", s.Title)
		}
	}
	if len(proj.Sessions) != 4 {
		t.Errorf("expected 4 un-clustered sessions, got %d", len(proj.Sessions))
	}
}

func TestBuildTree_ClampsSubagentsOfDeadParent(t *testing.T) {
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
	proj := projectByName(t, BuildTree(metas, live), "serf")
	if len(proj.Sessions) != 1 || len(proj.Sessions[0].Children) != 1 {
		t.Fatalf("unexpected shape: %#v", proj.Sessions)
	}
	if got := proj.Sessions[0].Children[0].State; got != "ended" {
		t.Errorf("stale subagent state = %q, want ended (parent is dead)", got)
	}
}

func TestBuildTree_KeepsSubagentStateWhenParentLive(t *testing.T) {
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
	proj := projectByName(t, BuildTree(metas, live), "serf")
	if got := proj.Sessions[0].Children[0].State; got != "active" {
		t.Errorf("live subagent state = %q, want active (parent is live)", got)
	}
}

func TestBuildTree_OrdersProjectsByRecencyWithinResult(t *testing.T) {
	// Projects should be emitted in tier order (active, recent, older, test),
	// and within a tier most-recent first.
	now := time.Now()
	metas := []schema.SessionMeta{
		// older
		{ID: "01OLD", UpdatedAt: now.Add(-10 * 24 * time.Hour), OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/old-proj"}},
		// recent, touched 2h ago
		{ID: "01R2", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/recent-older"}},
		// recent, touched 10m ago (more recent than recent-older)
		{ID: "01R1", UpdatedAt: now.Add(-10 * time.Minute), OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/recent-newer"}},
		// e2e test
		{ID: "01E", UpdatedAt: now, OriginalPrompt: "x",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/serf-e2e-a"}},
	}
	tree := BuildTree(metas, nil)
	got := projectNames(tree)
	want := []string{"recent-newer", "recent-older", "old-proj", "serf-e2e-a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project order = %v, want %v", got, want)
	}
}
