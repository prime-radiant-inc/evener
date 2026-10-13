package hub

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// searchSubagentCase builds a past index over one project holding the given
// metas (saved in order), a roster over the given live entries, and answers
// query through hubSearch at now.
func searchSubagentCase(t *testing.T, metas []schema.SessionMeta, live []hubcore.LiveEntry, query string, now time.Time) appwire.SearchResponse {
	t.Helper()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	for _, meta := range metas {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	var roster *hubcore.Roster
	if live != nil {
		roster = hubcore.NewRosterWithEntries(live...)
	}
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: query}, now)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// An ended subagent match names its ended parent as the top-level session to
// group under: identity, title, state and age all say whose row anchors the
// group.
func TestHubSearchAnnotatesEndedSubagentWithItsParent(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parentID, childID := hubtest.SessionID(t), hubtest.SessionID(t)
	parentUpdated := now.Add(-2 * time.Hour)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: parentID, UpdatedAt: parentUpdated, Name: "Alpha root", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: parentID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{childID}) {
		t.Fatalf("past=%v, want only the matching subagent", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil {
		t.Fatalf("past[0].TopLevel=nil, want the parent anchor")
	}
	if tl.ID != parentID || tl.Ref != hubRefFromTreeNodeID(parentID).String() {
		t.Fatalf("topLevel id=%q ref=%q, want the parent %q", tl.ID, tl.Ref, parentID)
	}
	if tl.Title != "Alpha root" || tl.State != "ended" || tl.Age != hubcore.AgeString(parentUpdated) {
		t.Fatalf("topLevel=%+v, want the ended parent's title, state and age", tl)
	}
	if tl.AskPending || tl.ApprovalPending {
		t.Fatalf("topLevel=%+v, want no pending flags on an ended parent", tl)
	}
}

// A live parent anchors its ended subagent's hit with live data: the active
// state and "now" age of its own row, so the group's anchor shows the session
// is running.
func TestHubSearchAnnotatesSubagentOfALiveParent(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parentID, childID := hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: parentID, UpdatedAt: now, Name: "Alpha root", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: parentID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, []hubcore.LiveEntry{{PID: 1, SessionID: parentID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusActive}}, "frobnitz", now)

	if len(resp.Live) != 0 {
		t.Fatalf("live=%+v, want nothing: the parent itself does not match", resp.Live)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{childID}) {
		t.Fatalf("past=%v, want only the matching subagent", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil {
		t.Fatalf("past[0].TopLevel=nil, want the live parent anchor")
	}
	if tl.ID != parentID || tl.Title != "Alpha root" {
		t.Fatalf("topLevel=%+v, want the live parent's identity", tl)
	}
	if tl.State != "active" || tl.Age != "now" {
		t.Fatalf("topLevel=%+v, want the live state and now age", tl)
	}
}

// A live parent the past index has not caught up with still anchors the hit:
// the roster entry resolves it, with the short-id title fallback.
func TestHubSearchAnchorsSubagentToRosterOnlyLiveParent(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parentID, childID := hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: parentID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, []hubcore.LiveEntry{{PID: 1, SessionID: parentID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusActive}}, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{childID}) {
		t.Fatalf("past=%v, want only the matching subagent", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil {
		t.Fatalf("past[0].TopLevel=nil, want the roster-resolved parent anchor")
	}
	if tl.ID != parentID || tl.Ref != hubRefFromTreeNodeID(parentID).String() {
		t.Fatalf("topLevel id=%q ref=%q, want the live parent %q", tl.ID, tl.Ref, parentID)
	}
	if tl.Title != hubcore.ShortID(parentID) {
		t.Fatalf("topLevel title=%q, want the short-id fallback", tl.Title)
	}
	if tl.State != "active" || tl.Age != "now" {
		t.Fatalf("topLevel=%+v, want the live state and now age", tl)
	}
}

// A nested delegate chain anchors to the root, not the immediate parent: the
// top-level session is the row the tree shows.
func TestHubSearchAnnotatesNestedDelegatesWithTheRoot(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rootID, midID, leafID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: rootID, UpdatedAt: now, Name: "Alpha root", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: midID, UpdatedAt: now.Add(-30 * time.Minute), Name: "mid delegate", IsSubagent: true, ParentSessionID: rootID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: leafID, UpdatedAt: now.Add(-time.Minute), Name: "leaf frobnitz", IsSubagent: true, ParentSessionID: midID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{leafID}) {
		t.Fatalf("past=%v, want only the matching leaf", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil || tl.ID != rootID {
		t.Fatalf("topLevel=%+v, want the chain's root %q", tl, rootID)
	}
}

// A subagent of a fork original anchors to the continuation that superseded
// it — the same rule the navigation tree applies.
func TestHubSearchAnchorsSubagentOfForkOriginalToContinuation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	originalID, continuationID, subID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: originalID, UpdatedAt: now.Add(-3 * time.Hour), Name: "original", ForkLabel: "old branch", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: continuationID, UpdatedAt: now.Add(-2 * time.Hour), Name: "continuation", ParentSessionID: originalID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: subID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: originalID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{subID}) {
		t.Fatalf("past=%v, want only the matching subagent", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil || tl.ID != continuationID {
		t.Fatalf("topLevel=%+v, want the fork continuation %q", tl, continuationID)
	}
}

// Unresolvable ancestry loses the annotation, never the match: an orphan
// subagent still appears, flat, exactly as before the grouping wire.
func TestHubSearchLeavesOrphanSubagentUnannotated(t *testing.T) {
	t.Parallel()
	now := time.Now()
	childID := hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: "01GONE", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{childID}) {
		t.Fatalf("past=%v, want the orphan subagent still listed", got)
	}
	if resp.Past[0].TopLevel != nil {
		t.Fatalf("topLevel=%+v, want nil for unresolvable ancestry", resp.Past[0].TopLevel)
	}
}

// When the parent matches too it keeps its own plain row beside the child,
// and only the child carries the anchor: the parent's row already speaks for
// itself.
func TestHubSearchKeepsMatchingParentUnannotatedBesideAnnotatedChild(t *testing.T) {
	t.Parallel()
	now := time.Now()
	parentID, childID := hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: parentID, UpdatedAt: now.Add(-30 * time.Minute), Name: "frobnitz root", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate", IsSubagent: true, ParentSessionID: parentID, OriginalPrompt: "go fix the frobnitz drain", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, nil, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{parentID, childID}) {
		t.Fatalf("past=%v, want the newer parent row first, then the child", got)
	}
	if resp.Past[0].TopLevel != nil {
		t.Fatalf("parent row topLevel=%+v, want nil: it matched itself", resp.Past[0].TopLevel)
	}
	tl := resp.Past[1].TopLevel
	if tl == nil || tl.ID != parentID {
		t.Fatalf("child topLevel=%+v, want the parent %q", tl, parentID)
	}
}

// An indexed descendant of a live delegate whose meta the index has not
// folded in yet anchors through the roster's report to the session the
// delegate runs under: the delegate is a subagent with no row of its own,
// not its group's anchor.
func TestHubSearchAnchorsThroughRosterReportedLineage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rootID, delegateID, childID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	resp := searchSubagentCase(t, []schema.SessionMeta{
		{ID: childID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: delegateID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}, []hubcore.LiveEntry{
		{PID: 1, SessionID: rootID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusActive, RunningSubagentIDs: []string{delegateID}},
		{PID: 2, SessionID: delegateID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusActive},
	}, "frobnitz", now)

	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{childID}) {
		t.Fatalf("past=%v, want only the matching descendant", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil || tl.ID != rootID {
		t.Fatalf("topLevel=%+v, want the root the roster reports the delegate under", tl)
	}
}

// A live subagent whose meta is on disk but not yet folded into the index is
// indexed by the query's own probes (MatchIDs' and liveSearchResult's Find
func TestHubSearchExcludesLiveSubagentFoldedInMidQuery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	subID := hubtest.SessionID(t)
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: subID, UpdatedAt: now, Name: "live frobnitz", IsSubagent: true, ParentSessionID: "01GONE",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	// No Rebuild: the index starts stale, and the query's probes do the
	// folding — exactly the first query after a hub restart.
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: subID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusActive},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Live) != 0 {
		t.Fatalf("live=%v, want nothing: a live subagent is not a navigation row", searchIDs(resp.Live))
	}
	if len(resp.Past) != 0 {
		t.Fatalf("past=%v, want nothing: the live subagent is not an ended match", searchIDs(resp.Past))
	}
}

// A fork continuation folded in mid-query supersedes the original as a past
// child's anchor: the annotation's lineage view must be read after the
// query's own probes, or the group lands under the superseded original.
func TestHubSearchAppliesForkSupersessionFoldedInMidQuery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	origID, subID, contID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	for _, meta := range []schema.SessionMeta{
		{ID: origID, UpdatedAt: now.Add(-4 * time.Hour), Name: "original", ForkLabel: "old branch", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: subID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", IsSubagent: true, ParentSessionID: origID, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// The continuation exists only on disk: the query's probes fold it in.
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: contID, UpdatedAt: now.Add(-3 * time.Hour), Name: "quiet worker", ParentSessionID: origID,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: contID, WorkingDir: "/projects/alpha", Status: appwire.ThreadStatusIdle},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{subID}) {
		t.Fatalf("past=%v, want only the matching delegate", got)
	}
	tl := resp.Past[0].TopLevel
	if tl == nil || tl.ID != contID {
		t.Fatalf("topLevel=%+v, want the folded-in continuation", tl)
	}
}

// The real search rules and the tree's rollup agree on one corpus: the same
// metas, disk index and roster drive hubSearch's annotations and
// BuildTreeAt's attribution, so the two lineage views cannot drift apart.
// (hubcore's agreement test walks with injected predicates; this one runs
// the production searchTopLevelFor through hubSearch.)
func TestHubSearchGroupingAgreesWithTheTreeRollup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	chainDir, forkDir, brokenDir := t.TempDir(), t.TempDir(), t.TempDir()
	// Valid session ids throughout: the disk lister silently drops a
	// hand-seeded meta whose id is not a UUIDv7 payload, so plausible-looking
	// ids never reach the index.
	rootID, midID, leafID, grindID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	origID, contID, forkSubID, forkWorkID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	idleID, cycleXID, cycleYID, ghostID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	metas := []schema.SessionMeta{
		// A live chain root with an ended delegate chain under it, plus a
		// live working child whose activity the tree attributes to A.
		{ID: rootID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		{ID: midID, IsSubagent: true, ParentSessionID: rootID, UpdatedAt: now.Add(-30 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		{ID: leafID, IsSubagent: true, ParentSessionID: midID, UpdatedAt: now.Add(-time.Hour), Name: "delegate frobnitz", EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		{ID: grindID, IsSubagent: true, ParentSessionID: rootID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: chainDir}},
		// A live fork original superseded by an ended continuation: the
		// ended delegate groups under the continuation, and the live child's
		// activity attributes there too.
		{ID: origID, ForkLabel: "old branch", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		{ID: contID, ParentSessionID: origID, UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		{ID: forkSubID, IsSubagent: true, ParentSessionID: origID, UpdatedAt: now.Add(-2*time.Hour - time.Minute), Name: "delegate frobnitz", EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		{ID: forkWorkID, IsSubagent: true, ParentSessionID: origID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: forkDir}},
		// Unresolvable: a live parent cycle and a live orphan keep the
		// project idle (live work that attributes to nothing), and an ended
		// orphan stays a flat search row.
		{ID: idleID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: cycleXID, IsSubagent: true, ParentSessionID: cycleYID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: cycleYID, IsSubagent: true, ParentSessionID: cycleXID, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
		{ID: ghostID, IsSubagent: true, ParentSessionID: "01GONE", UpdatedAt: now.Add(-3 * time.Hour), Name: "ghost frobnitz", EnvInfo: schema.EnvironmentInfo{WorkingDir: brokenDir}},
	}
	live := []hubcore.LiveEntry{
		{PID: 1, SessionID: rootID, WorkingDir: chainDir, Status: appwire.ThreadStatusIdle},
		{PID: 2, SessionID: grindID, WorkingDir: chainDir, Status: appwire.ThreadStatusActive},
		{PID: 3, SessionID: origID, WorkingDir: forkDir, Status: appwire.ThreadStatusIdle},
		{PID: 4, SessionID: forkWorkID, WorkingDir: forkDir, Status: appwire.ThreadStatusActive},
		{PID: 5, SessionID: idleID, WorkingDir: brokenDir, Status: appwire.ThreadStatusIdle},
		{PID: 6, SessionID: cycleXID, WorkingDir: brokenDir, Status: appwire.ThreadStatusActive},
		{PID: 7, SessionID: cycleYID, WorkingDir: brokenDir, Status: appwire.ThreadStatusActive},
	}

	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	for _, meta := range metas {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(live...)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Search: the chain's leaf anchors to the live root, the fork's delegate
	// to the continuation, the ended orphan stays flat — and nothing else
	// matches.
	if len(resp.Live) != 0 {
		t.Fatalf("live=%+v, want nothing: no live session matches the query", resp.Live)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{leafID, forkSubID, ghostID}) {
		t.Fatalf("past=%v, want the chain leaf, the fork delegate and the ended orphan", got)
	}
	if tl := resp.Past[0].TopLevel; tl == nil || tl.ID != rootID {
		t.Fatalf("chain leaf topLevel=%+v, want the live root", tl)
	}
	if tl := resp.Past[1].TopLevel; tl == nil || tl.ID != contID {
		t.Fatalf("fork delegate topLevel=%+v, want the continuation", tl)
	}
	if resp.Past[2].TopLevel != nil {
		t.Fatalf("ended orphan topLevel=%+v, want nil for unresolvable ancestry", resp.Past[2].TopLevel)
	}

	// Tree over the same corpus: the working children attribute to the same
	// roots search resolved to, and the unresolvable project stays idle.
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	rollups := map[string]string{}
	for _, project := range tree.Projects {
		rollups[project.WorkingDir] = project.RollupState
	}
	if rollups[chainDir] != "active" || rollups[forkDir] != "active" {
		t.Fatalf("rollups=%v, want the chain's and the fork continuation's projects working", rollups)
	}
	if rollups[brokenDir] == "active" {
		t.Fatalf("rollups=%v, want the unresolvable project idle: its live work attributes to no row", rollups)
	}
	for _, row := range tree.Live {
		switch row.ID {
		case midID, leafID, grindID, forkSubID, forkWorkID, cycleXID, cycleYID, ghostID:
			t.Fatalf("subagent %q has a top-level row", row.ID)
		case rootID, contID, origID, idleID:
		}
	}
}
