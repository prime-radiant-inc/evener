package hubcore

import (
	"reflect"
	"testing"
	"time"
)

// TestTreeProjectPage pins rail pagination over one tier: a page is a window
// of the tier's authoritative rows (the uncapped slice when the tree retained
// one, the emitted rows otherwise) plus the count of rows after it, and an
// invalid window or tier is refused.
func TestTreeProjectPage(t *testing.T) {
	three := []TreeNode{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}}
	one := []TreeNode{{ID: "s1"}}
	for _, tc := range []struct {
		name          string
		project       TreeProject
		tier          string
		offset, limit int
		wantRows      []TreeNode
		wantRemaining int
		wantOK        bool
	}{
		{name: "negative offset", project: TreeProject{allCurrent: one}, tier: "current", offset: -1, limit: 10},
		{name: "zero limit", project: TreeProject{allCurrent: one}, tier: "current", offset: 0, limit: 0},
		{name: "unknown tier", project: TreeProject{allCurrent: one}, tier: "bogus", offset: 0, limit: 10},
		{name: "offset past the last row", project: TreeProject{allCurrent: one}, tier: "current", offset: 5, limit: 10, wantRows: []TreeNode{}, wantOK: true},
		{name: "first current page", project: TreeProject{allCurrent: three}, tier: "current", offset: 0, limit: 2, wantRows: three[:2], wantRemaining: 1, wantOK: true},
		{name: "first recent page", project: TreeProject{allRecent: three}, tier: "recent", offset: 0, limit: 2, wantRows: three[:2], wantRemaining: 1, wantOK: true},
		{name: "last recent page", project: TreeProject{allRecent: three}, tier: "recent", offset: 2, limit: 2, wantRows: three[2:], wantOK: true},
		{name: "first archived page", project: TreeProject{allArchived: three[:2]}, tier: "archived", offset: 0, limit: 1, wantRows: three[:1], wantRemaining: 1, wantOK: true},
		{name: "current falls back to emitted rows", project: TreeProject{Current: one}, tier: "current", offset: 0, limit: 10, wantRows: one, wantOK: true},
		{name: "recent falls back to emitted rows", project: TreeProject{Recent: one}, tier: "recent", offset: 0, limit: 10, wantRows: one, wantOK: true},
		{name: "archived falls back to emitted rows", project: TreeProject{Archived: three[:2]}, tier: "archived", offset: 0, limit: 1, wantRows: three[:1], wantRemaining: 1, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, remaining, ok := tc.project.Page(tc.tier, tc.offset, tc.limit)
			if ok != tc.wantOK || remaining != tc.wantRemaining || !reflect.DeepEqual(rows, tc.wantRows) {
				t.Fatalf("Page(%q, %d, %d) = %v, %d, %t; want %v, %d, %t",
					tc.tier, tc.offset, tc.limit, rows, remaining, ok, tc.wantRows, tc.wantRemaining, tc.wantOK)
			}
		})
	}
}

// TestTreePinCandidates pins which rows a pin can target: every session row the
// tree retains, live favorites first, then each project's uncapped tiers and
// archived projects, once per session ID; fork rows are never candidates.
func TestTreePinCandidates(t *testing.T) {
	session := func(id string) TreeNode { return TreeNode{ID: id, Kind: "session"} }
	fork := TreeNode{ID: "fork-1", Kind: "fork"}
	for _, tc := range []struct {
		name    string
		tree    Tree
		wantIDs []string
	}{
		{name: "empty tree"},
		{name: "live favorites", tree: Tree{favoriteLive: []TreeNode{session("session-1"), session("session-2")}}, wantIDs: []string{"session-1", "session-2"}},
		{name: "fork favorite skipped", tree: Tree{favoriteLive: []TreeNode{fork, session("session-1")}}, wantIDs: []string{"session-1"}},
		{name: "fork project row skipped", tree: Tree{Projects: []TreeProject{{allCurrent: []TreeNode{session("session-1"), fork}}}}, wantIDs: []string{"session-1"}},
		{name: "session listed once", tree: Tree{
			favoriteLive: []TreeNode{session("session-1")},
			Projects:     []TreeProject{{allCurrent: []TreeNode{session("session-1")}}},
		}, wantIDs: []string{"session-1"}},
		{name: "archived project rows", tree: Tree{ArchivedProjects: []TreeProject{{allArchived: []TreeNode{session("session-1")}}}}, wantIDs: []string{"session-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotIDs []string
			for _, node := range tc.tree.PinCandidates() {
				gotIDs = append(gotIDs, node.ID)
			}
			if !reflect.DeepEqual(gotIDs, tc.wantIDs) {
				t.Fatalf("PinCandidates IDs = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

// TestTreeProjectTotalSessionCount pins that a project's session count is
// taken from its uncapped tiers when the tree retained them, and from the
// emitted tiers otherwise.
func TestTreeProjectTotalSessionCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project TreeProject
	}{
		{name: "uncapped tiers", project: TreeProject{
			allCurrent:  []TreeNode{{ID: "s1"}},
			allRecent:   []TreeNode{{ID: "s2"}, {ID: "s3"}},
			allArchived: []TreeNode{{ID: "s4"}},
		}},
		{name: "emitted tiers", project: TreeProject{
			Current:  []TreeNode{{ID: "s1"}},
			Recent:   []TreeNode{{ID: "s2"}},
			Archived: []TreeNode{{ID: "s3"}, {ID: "s4"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.project.TotalSessionCount(); got != 4 {
				t.Fatalf("TotalSessionCount = %d, want 4", got)
			}
		})
	}
}

// A live entry with no session ID cannot be shown or opened, so it never
// reaches the needs-you tier however its status reads.
func TestBuildTreeNeedsYouSkipsALiveEntryWithoutSession(t *testing.T) {
	tree := BuildTreeAt(nil, []LiveEntry{{SessionID: "", Status: "awaiting"}}, nil, time.Now())
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("NeedsYou = %+v, want no row for a live entry without a session", tree.NeedsYou)
	}
}

func TestDecisionForAnEmptyIDIsNil(t *testing.T) {
	if got := decisionFor(map[ArchiveKey]bool{{Kind: "session", ID: ""}: true}, ""); got != nil {
		t.Fatalf("decisionFor(\"\") = %v, want nil: an empty ID never has a decision", *got)
	}
}
