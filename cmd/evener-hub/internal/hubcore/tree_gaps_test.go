package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// TestPinCandidatesEmpty covers PinCandidates with an empty tree.
func TestPinCandidatesEmpty(t *testing.T) {
	tree := Tree{}
	got := tree.PinCandidates()
	if len(got) != 0 {
		t.Fatalf("PinCandidates on empty tree = %d, want 0", len(got))
	}
}

// TestPinCandidatesFromFavoriteLive covers the favoriteLive path.
func TestPinCandidatesFromFavoriteLive(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "session-1", Kind: "session"},
			{ID: "session-2", Kind: "session"},
		},
	}
	got := tree.PinCandidates()
	if len(got) != 2 {
		t.Fatalf("PinCandidates = %d, want 2", len(got))
	}
}

// TestPinCandidatesSkipsNonSession covers the path where non-session nodes are skipped.
func TestPinCandidatesSkipsNonSession(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "fork-1", Kind: "fork"},
			{ID: "session-1", Kind: "session"},
		},
	}
	got := tree.PinCandidates()
	if len(got) != 1 {
		t.Fatalf("PinCandidates = %d, want 1 (non-session skipped)", len(got))
	}
	if got[0].ID != "session-1" {
		t.Fatalf("PinCandidates[0].ID = %q, want session-1", got[0].ID)
	}
}

// TestPinCandidatesDeduplicates covers the deduplication path.
func TestPinCandidatesDeduplicates(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "session-1", Kind: "session"},
		},
		Projects: []TreeProject{
			{allCurrent: []TreeNode{{ID: "session-1", Kind: "session"}}},
		},
	}
	got := tree.PinCandidates()
	if len(got) != 1 {
		t.Fatalf("PinCandidates = %d, want 1 (deduplicated)", len(got))
	}
}

// TestPinCandidatesFromClusterChildren covers the path where session children
// grouped under a cluster are included.
func TestPinCandidatesFromClusterChildren(t *testing.T) {
	tree := Tree{
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{Kind: "cluster", Children: []TreeNode{
					{ID: "session-1", Kind: "session"},
					{ID: "session-2", Kind: "session"},
				}},
			}},
		},
	}
	got := tree.PinCandidates()
	if len(got) != 2 {
		t.Fatalf("PinCandidates = %d, want 2 (from cluster children)", len(got))
	}
}

// TestPinCandidatesFromArchivedProjects covers the ArchivedProjects path.
func TestPinCandidatesFromArchivedProjects(t *testing.T) {
	tree := Tree{
		ArchivedProjects: []TreeProject{
			{allArchived: []TreeNode{
				{ID: "session-1", Kind: "session"},
			}},
		},
	}
	got := tree.PinCandidates()
	if len(got) != 1 {
		t.Fatalf("PinCandidates = %d, want 1 (from archived)", len(got))
	}
}

// TestFavoriteNodeAuthoritiesEmpty covers the empty path.
func TestFavoriteNodeAuthoritiesEmpty(t *testing.T) {
	tree := Tree{}
	got := tree.FavoriteNodeAuthorities()
	if len(got) != 0 {
		t.Fatalf("FavoriteNodeAuthorities on empty tree = %d, want 0", len(got))
	}
}

// TestFavoriteNodeAuthoritiesFromProjects covers the Projects path.
func TestFavoriteNodeAuthoritiesFromProjects(t *testing.T) {
	tree := Tree{
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{ID: "session-1", Kind: "session"},
				{ID: "", Kind: "session"}, // should be skipped
			}},
		},
	}
	got := tree.FavoriteNodeAuthorities()
	if len(got) != 1 {
		t.Fatalf("FavoriteNodeAuthorities = %d, want 1", len(got))
	}
	if got[0].ID != "session-1" {
		t.Fatalf("FavoriteNodeAuthorities[0].ID = %q, want session-1", got[0].ID)
	}
}

// TestFavoriteNodeAuthoritiesFromArchived covers the ArchivedProjects path.
func TestFavoriteNodeAuthoritiesFromArchived(t *testing.T) {
	tree := Tree{
		ArchivedProjects: []TreeProject{
			{allArchived: []TreeNode{
				{ID: "session-arch", Kind: "session"},
			}},
		},
	}
	got := tree.FavoriteNodeAuthorities()
	if len(got) != 1 {
		t.Fatalf("FavoriteNodeAuthorities = %d, want 1", len(got))
	}
	if got[0].ID != "session-arch" {
		t.Fatalf("FavoriteNodeAuthorities[0].ID = %q, want session-arch", got[0].ID)
	}
}

// TestBuildTreeWithProjects covers BuildTreeWithProjects with an empty input.
func TestBuildTreeWithProjectsEmpty(t *testing.T) {
	tree := BuildTreeWithProjects(nil, nil, nil, nil)
	if len(tree.Projects) != 0 {
		t.Fatalf("Projects = %d, want 0", len(tree.Projects))
	}
}

// TestResolveProjectMapStrictEmpty covers ResolveProjectMapStrict with empty input.
func TestResolveProjectMapStrictEmpty(t *testing.T) {
	projects, err := ResolveProjectMapStrict(nil, nil)
	if err != nil {
		t.Fatalf("ResolveProjectMapStrict: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("projects = %d, want 0", len(projects))
	}
}

// TestResolveProjectMapStrictWithPath covers the error path where a path
// cannot be resolved.
func TestResolveProjectMapStrictWithPath(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "s1", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/nonexistent/path/that/does/not/resolve"}},
	}
	_, err := ResolveProjectMapStrict(metas, nil)
	if err == nil {
		t.Fatalf("ResolveProjectMapStrict with unresolvable path should error")
	}
}

// TestPageInvalidOffset covers the invalid-offset path in Page.
func TestPageInvalidOffset(t *testing.T) {
	p := TreeProject{allCurrent: []TreeNode{{ID: "s1"}}}
	got, _, ok := p.Page("current", -1, 10)
	if ok {
		t.Fatalf("Page with negative offset should return ok=false")
	}
	if got != nil {
		t.Fatalf("Page with negative offset should return nil, got %v", got)
	}
}

// TestPageInvalidLimit covers the invalid-limit path.
func TestPageInvalidLimit(t *testing.T) {
	p := TreeProject{allCurrent: []TreeNode{{ID: "s1"}}}
	_, _, ok := p.Page("current", 0, 0)
	if ok {
		t.Fatalf("Page with zero limit should return ok=false")
	}
}

// TestPageInvalidTier covers the invalid-tier path.
func TestPageInvalidTier(t *testing.T) {
	p := TreeProject{allCurrent: []TreeNode{{ID: "s1"}}}
	_, _, ok := p.Page("unknown", 0, 10)
	if ok {
		t.Fatalf("Page with unknown tier should return ok=false")
	}
}

// TestPageOffsetBeyondRows covers the offset-beyond-rows path.
func TestPageOffsetBeyondRows(t *testing.T) {
	p := TreeProject{allCurrent: []TreeNode{{ID: "s1"}}}
	got, remaining, ok := p.Page("current", 10, 10)
	if !ok {
		t.Fatalf("Page with offset beyond rows should return ok=true")
	}
	if len(got) != 0 {
		t.Fatalf("Page with offset beyond rows should return empty, got %d", len(got))
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
}

// TestPageSuccess covers the happy path.
func TestPageSuccess(t *testing.T) {
	p := TreeProject{allCurrent: []TreeNode{
		{ID: "s1"}, {ID: "s2"}, {ID: "s3"},
	}}
	got, remaining, ok := p.Page("current", 0, 2)
	if !ok {
		t.Fatalf("Page should succeed")
	}
	if len(got) != 2 {
		t.Fatalf("got = %d, want 2", len(got))
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d, want 1", remaining)
	}
}

// TestPageWithFallbackRows covers the path where allCurrent is nil and the
// public Current field is used instead.
func TestPageWithFallbackRows(t *testing.T) {
	p := TreeProject{Current: []TreeNode{{ID: "s1"}, {ID: "s2"}}}
	got, _, ok := p.Page("current", 0, 10)
	if !ok {
		t.Fatalf("Page with fallback rows should succeed")
	}
	if len(got) != 2 {
		t.Fatalf("got = %d, want 2", len(got))
	}
}

// TestTotalSessionCountWithAllRows covers the path where all* fields are set.
func TestTotalSessionCountWithAllRows(t *testing.T) {
	p := TreeProject{
		allCurrent:  []TreeNode{{ID: "s1"}},
		allRecent:   []TreeNode{{ID: "s2"}, {ID: "s3"}},
		allArchived: []TreeNode{{ID: "s4"}},
	}
	if got := p.TotalSessionCount(); got != 4 {
		t.Fatalf("TotalSessionCount = %d, want 4", got)
	}
}

// TestTotalSessionCountWithPublicFields covers the path where all* fields are nil.
func TestTotalSessionCountWithPublicFields(t *testing.T) {
	p := TreeProject{
		Current:  []TreeNode{{ID: "s1"}},
		Recent:   []TreeNode{{ID: "s2"}},
		Archived: []TreeNode{{ID: "s3"}, {ID: "s4"}},
	}
	if got := p.TotalSessionCount(); got != 4 {
		t.Fatalf("TotalSessionCount = %d, want 4", got)
	}
}

// TestFavoriteCandidatesWithFork covers the fork-kind path in FavoriteCandidates.
func TestFavoriteCandidatesWithFork(t *testing.T) {
	tree := Tree{
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{ID: "fork-1", Kind: "fork"},
			}},
		},
	}
	got := tree.FavoriteCandidates()
	if len(got) != 1 {
		t.Fatalf("FavoriteCandidates = %d, want 1 (fork)", len(got))
	}
	if got[0].ID != "fork-1" {
		t.Fatalf("FavoriteCandidates[0].ID = %q, want fork-1", got[0].ID)
	}
}

// TestFavoriteCandidatesWithClusterChildren covers the cluster-children path.
func TestFavoriteCandidatesWithClusterChildren(t *testing.T) {
	tree := Tree{
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{Kind: "cluster", Children: []TreeNode{
					{ID: "session-1", Kind: "session"},
					{ID: "fork-1", Kind: "fork"},
					{ID: "cluster-2", Kind: "cluster"}, // should be skipped
				}},
			}},
		},
	}
	got := tree.FavoriteCandidates()
	if len(got) != 2 {
		t.Fatalf("FavoriteCandidates = %d, want 2 (session + fork, not nested cluster)", len(got))
	}
}

// TestFavoriteCandidatesFromRecent covers the allRecent path.
func TestFavoriteCandidatesFromRecent(t *testing.T) {
	tree := Tree{
		Projects: []TreeProject{
			{allRecent: []TreeNode{
				{ID: "session-recent", Kind: "session"},
			}},
		},
	}
	got := tree.FavoriteCandidates()
	if len(got) != 1 {
		t.Fatalf("FavoriteCandidates = %d, want 1", len(got))
	}
	if got[0].ID != "session-recent" {
		t.Fatalf("FavoriteCandidates[0].ID = %q, want session-recent", got[0].ID)
	}
}

// TestFavoriteCandidatesSkipsEmptyID covers the empty-ID skip path.
func TestFavoriteCandidatesSkipsEmptyID(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "", Kind: "session"}, // should be skipped
			{ID: "session-1", Kind: "session"},
		},
	}
	got := tree.FavoriteCandidates()
	if len(got) != 1 {
		t.Fatalf("FavoriteCandidates = %d, want 1", len(got))
	}
}

// Ensure identifier import is used.
var _ = identifier.ValidateProjectID
var _ = time.Now
