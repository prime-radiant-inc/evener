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
