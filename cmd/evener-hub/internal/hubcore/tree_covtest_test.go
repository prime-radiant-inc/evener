package hubcore

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
)

var errCovBoom = errors.New("boom")

// --- tree.go: Page ---

// TestCovTreePageInvalidOffsetLimit covers the invalid offset/limit guard
// (tree.go:254-255) and the default tier branch (tree.go:274-275).
func TestCovTreePageInvalidOffsetLimit(t *testing.T) {
	p := TreeProject{Current: []TreeNode{{ID: "s1"}}}
	if rows, rem, ok := p.Page("current", -1, 10); ok || rows != nil || rem != 0 {
		t.Fatalf("negative offset should fail: rows=%v rem=%d ok=%v", rows, rem, ok)
	}
	if rows, rem, ok := p.Page("current", 0, 0); ok || rows != nil || rem != 0 {
		t.Fatalf("zero limit should fail: rows=%v rem=%d ok=%v", rows, rem, ok)
	}
	if rows, rem, ok := p.Page("bogus", 0, 10); ok || rows != nil || rem != 0 {
		t.Fatalf("unknown tier should fail: rows=%v rem=%d ok=%v", rows, rem, ok)
	}
}

// TestCovTreePageOffsetBeyondEnd covers offset >= len(rows) (tree.go:277-278).
func TestCovTreePageOffsetBeyondEnd(t *testing.T) {
	p := TreeProject{Current: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("current", 5, 10)
	if !ok || rows == nil || len(rows) != 0 || rem != 0 {
		t.Fatalf("offset beyond end: rows=%v rem=%d ok=%v", rows, rem, ok)
	}
}

// TestCovTreePageRecentTier covers the recent tier path (tree.go:265-267).
func TestCovTreePageRecentTier(t *testing.T) {
	p := TreeProject{
		allRecent: []TreeNode{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}},
	}
	rows, rem, ok := p.Page("recent", 0, 2)
	want := []TreeNode{{ID: "s1"}, {ID: "s2"}}
	if !ok || !reflect.DeepEqual(rows, want) || rem != 1 {
		t.Fatalf("recent page: rows=%v rem=%d ok=%v, want rows=%v rem=1 ok=true", rows, rem, ok, want)
	}
	rows2, rem2, ok2 := p.Page("recent", 2, 2)
	want2 := []TreeNode{{ID: "s3"}}
	if !ok2 || !reflect.DeepEqual(rows2, want2) || rem2 != 0 {
		t.Fatalf("recent page 2: rows=%v rem=%d ok=%v, want rows=%v rem=0 ok=true", rows2, rem2, ok2, want2)
	}
}

// TestCovTreePageArchivedTier covers the archived tier (tree.go:269-271).
func TestCovTreePageArchivedTier(t *testing.T) {
	p := TreeProject{
		Archived: []TreeNode{{ID: "s1"}, {ID: "s2"}},
	}
	rows, rem, ok := p.Page("archived", 0, 1)
	want := []TreeNode{{ID: "s1"}}
	if !ok || !reflect.DeepEqual(rows, want) || rem != 1 {
		t.Fatalf("archived page: rows=%v rem=%d ok=%v, want rows=%v rem=1 ok=true", rows, rem, ok, want)
	}
}

// TestCovTreePageCurrentFallback covers the nil allCurrent → Current fallback
// (tree.go:261-262).
func TestCovTreePageCurrentFallback(t *testing.T) {
	p := TreeProject{Current: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("current", 0, 10)
	want := []TreeNode{{ID: "s1"}}
	if !ok || !reflect.DeepEqual(rows, want) || rem != 0 {
		t.Fatalf("current fallback: rows=%v rem=%d ok=%v, want rows=%v rem=0 ok=true", rows, rem, ok, want)
	}
}

// TestCovTreePageRecentFallback covers the nil allRecent → Recent fallback
// (tree.go:266-267).
func TestCovTreePageRecentFallback(t *testing.T) {
	p := TreeProject{Recent: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("recent", 0, 10)
	want := []TreeNode{{ID: "s1"}}
	if !ok || !reflect.DeepEqual(rows, want) || rem != 0 {
		t.Fatalf("recent fallback: rows=%v rem=%d ok=%v, want rows=%v rem=0 ok=true", rows, rem, ok, want)
	}
}

// TestCovTreePageArchivedFallback covers the nil allArchived → Archived
// fallback (tree.go:271-272).
func TestCovTreePageArchivedFallback(t *testing.T) {
	p := TreeProject{Archived: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("archived", 0, 10)
	want := []TreeNode{{ID: "s1"}}
	if !ok || !reflect.DeepEqual(rows, want) || rem != 0 {
		t.Fatalf("archived fallback: rows=%v rem=%d ok=%v, want rows=%v rem=0 ok=true", rows, rem, ok, want)
	}
}

// --- tree.go: resolveProjectMap ---

// TestCovResolveProjectMapStrictError covers the strict error branch
// (tree.go:582-583) — a meta with a path that fails project resolution
// produces an error.
func TestCovResolveProjectMapStrictError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing", "project")
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", EnvInfo: schema.EnvironmentInfo{WorkingDir: missingPath}},
	}
	projects, err := ResolveProjectMapStrict(metas, nil)
	if err == nil || !strings.Contains(err.Error(), `resolve project "`+missingPath+`"`) {
		t.Fatalf("ResolveProjectMapStrict error = %v, want wrapped project path", err)
	}
	if projects != nil {
		t.Fatalf("ResolveProjectMapStrict projects = %v, want nil on error", projects)
	}
}

// TestCovResolveProjectMapStrictLiveError covers the strict error branch for
// live entries (tree.go:602-603).
func TestCovResolveProjectMapStrictLiveError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing", "project")
	live := []LiveEntry{
		{SessionID: "02wMz5Txv1C3Hut0M8GCeB", WorkingDir: missingPath},
	}
	projects, err := ResolveProjectMapStrict(nil, live)
	if err == nil || !strings.Contains(err.Error(), `resolve project "`+missingPath+`"`) {
		t.Fatalf("ResolveProjectMapStrict error = %v, want wrapped live project path", err)
	}
	if projects != nil {
		t.Fatalf("ResolveProjectMapStrict projects = %v, want nil on error", projects)
	}
}

// TestCovResolveProjectMapNonStrictSkipsErrors covers the non-strict
// continue path (tree.go:584-585) for metas and (tree.go:604-605) for live.
func TestCovResolveProjectMapNonStrictSkipsErrors(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing", "project")
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", EnvInfo: schema.EnvironmentInfo{WorkingDir: missingPath}},
		{ID: "02wMz5Txv2enqVTitaig6F", EnvInfo: schema.EnvironmentInfo{WorkingDir: ""}},
	}
	live := []LiveEntry{
		{SessionID: "02wMz5Txv3enqVTitaig6F", WorkingDir: missingPath},
		{SessionID: "02wMz5Txv4enqVTitaig6F", WorkingDir: ""},
	}
	projects := ResolveProjectMap(metas, live)
	want := map[string]identifier.Project{}
	if !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects = %#v, want empty map", projects)
	}
}

// TestCovResolveProjectMapLiveWithProjectID covers the live entry path where
// le.Project.ID is already set (tree.go:597-598).
func TestCovResolveProjectMapLiveWithProjectID(t *testing.T) {
	proj := identifier.Project{ID: "test-project", CanonicalPath: "/tmp/test"}
	live := []LiveEntry{
		{SessionID: "02wMz5Txv1C3Hut0M8GCeB", WorkingDir: "/tmp/test", Project: proj},
	}
	projects := ResolveProjectMap(nil, live)
	want := map[string]identifier.Project{"/tmp/test": proj}
	if !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects = %#v, want %#v", projects, want)
	}
}

// --- tree.go: FavoriteCandidates ---

// TestCovFavoriteCandidatesCoversAllKinds exercises FavoriteCandidates with
// session, fork, cluster, and empty-ID nodes to cover the switch branches
// (tree.go:62-71) and the empty-ID guard (tree.go:48-49).
func TestCovFavoriteCandidatesCoversAllKinds(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "live-1", Kind: "session", Title: "live"},
		},
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{ID: "", Kind: "session"},       // empty ID skipped
				{ID: "live-1", Kind: "session"}, // duplicate skipped
				{ID: "fork-1", Kind: "fork", Title: "fork"},
				{Kind: "cluster", Children: []TreeNode{
					{ID: "child-1", Kind: "session", Title: "child session"},
					{ID: "child-2", Kind: "fork", Title: "child fork"},
					{ID: "child-3", Kind: "subagent"}, // skipped
				}},
				{ID: "other-1", Kind: "other"}, // skipped
			}},
			{allRecent: []TreeNode{
				{ID: "recent-1", Kind: "session", Title: "recent"},
			}},
		},
	}
	got := tree.FavoriteCandidates()
	want := []TreeNode{
		{ID: "live-1", Kind: "session", Title: "live"},
		{ID: "fork-1", Kind: "fork", Title: "fork"},
		{ID: "child-1", Kind: "session", Title: "child session"},
		{ID: "child-2", Kind: "fork", Title: "child fork"},
		{ID: "recent-1", Kind: "session", Title: "recent"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FavoriteCandidates = %#v, want exact ordered nodes %#v", got, want)
	}
}

// --- past.go ---

// TestCovPastRebuildEmptyGlob covers Rebuild with an empty stateGlob
// (past.go:134-135).
func TestCovPastRebuildEmptyGlob(t *testing.T) {
	idx := NewPastIndex("")
	changed, err := idx.Rebuild()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatal("Rebuild with empty glob should return false")
	}
}

// TestCovPastRebuildGlobError covers Rebuild with a malformed glob pattern
// (past.go:137-138).
func TestCovPastRebuildGlobError(t *testing.T) {
	idx := NewPastIndex("[").SetFs(afero.NewMemMapFs())
	changed, err := idx.Rebuild()
	if !errors.Is(err, filepath.ErrBadPattern) {
		t.Fatalf("Rebuild malformed-glob error = %v, want filepath.ErrBadPattern", err)
	}
	if changed {
		t.Fatal("Rebuild with malformed glob reported a change")
	}
}

// TestCovPastFindInvalidID covers Find with an invalid session ID
// (past.go:713-714).
func TestCovPastFindInvalidID(t *testing.T) {
	idx := NewPastIndex("")
	// Seed the cache with the invalid key. If validation moves behind the cache
	// lookup, this fixture makes the test fail instead of reaching the same
	// false result through an empty index.
	idx.byID["not-a-valid-ulid"] = PastEntry{Meta: schema.SessionMeta{ID: "not-a-valid-ulid"}}
	if _, ok := idx.Find("not-a-valid-ulid"); ok {
		t.Fatal("Find admitted an invalid ID already present in the cache")
	}
}

// TestCovPastFindEmptyStateGlob covers Find with a valid ID but empty
// stateGlob (past.go:719-720).
func TestCovPastFindEmptyStateGlob(t *testing.T) {
	idx := NewPastIndex("")
	if _, ok := idx.Find(hubtest.SessionID(t)); ok {
		t.Fatal("Find with empty stateGlob should return false")
	}
}

// TestCovReportUnlistedMetas covers reportUnlistedMetas with an empty
// sessions directory (past.go:236-260).
func TestCovReportUnlistedMetas(t *testing.T) {
	fs := afero.NewMemMapFs()
	projectDir := "/project"
	_ = fs.MkdirAll(projectDir, 0o755)
	// No sessions directory — should be a no-op.
	indexed := map[string]bool{}
	skipped := map[string]string{}
	reportUnlistedMetas(fs, projectDir, indexed, skipped)
	if len(skipped) != 0 {
		t.Fatalf("expected 0 skipped, got %d", len(skipped))
	}
}

// --- remotecache.go ---

// TestCovInferRemoteSourcesEmptySourceID covers inferRemoteSources with a
// thread whose Source is empty and Evener.Ref also yields an empty sourceID
// (remotecache.go:76-82).
func TestCovInferRemoteSourcesEmptySourceID(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "", Evener: appwire.EvenerThread{Ref: ""}},
	}
	sources := inferRemoteSources(threads, true)
	if len(sources) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(sources))
	}
}

// TestCovInferRemoteSourcesFromRef covers the fallback where Source is empty
// but the Evener.Ref parses to a sourceID (remotecache.go:77-79).
func TestCovInferRemoteSourcesFromRef(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "t1", Source: "", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
	}
	wantInput := []appwire.Thread{
		{ID: "t1", Source: "", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
	}
	want := map[string]RemoteSourceSnapshot{
		"remote1": {
			Threads: []appwire.Thread{
				{ID: "t1", Source: "", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
			},
			Complete: true,
		},
	}
	sources := inferRemoteSources(threads, true)
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %#v, want %#v", sources, want)
	}
	if !reflect.DeepEqual(threads, wantInput) {
		t.Fatalf("input threads mutated: got %#v, want %#v", threads, wantInput)
	}
}

// --- favorite_authority.go ---

// TestCovClassifyFavoriteProjectNoAuthorities covers classifyFavoriteProject
// with zero authorities (favorite_authority.go:327).
func TestCovClassifyFavoriteProjectNoAuthorities(t *testing.T) {
	projects := favoriteProjectIndex{
		byID: map[string][]FavoriteProjectAuthority{},
	}
	result := classifyFavoriteProject(ArchiveKey{Kind: "project", ID: "/some/path"}, projects)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteProjectAmbiguous covers classifyFavoriteProject with
// ambiguous IDs (favorite_authority.go:327).
func TestCovClassifyFavoriteProjectAmbiguous(t *testing.T) {
	projects := favoriteProjectIndex{
		byID: map[string][]FavoriteProjectAuthority{
			"/some/path": {{ID: "/some/path", Quality: FavoriteAuthorityComplete}},
		},
		ambiguousIDs: map[string]bool{"/some/path": true},
	}
	result := classifyFavoriteProject(ArchiveKey{Kind: "project", ID: "/some/path"}, projects)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteProjectIncompleteQuality covers the incomplete
// quality branch (favorite_authority.go:330-331).
func TestCovClassifyFavoriteProjectIncompleteQuality(t *testing.T) {
	projects := favoriteProjectIndex{
		byID: map[string][]FavoriteProjectAuthority{
			"/some/path": {{ID: "/some/path", Quality: FavoriteAuthorityIncomplete}},
		},
	}
	result := classifyFavoriteProject(ArchiveKey{Kind: "project", ID: "/some/path"}, projects)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant for incomplete, got %v", result.State)
	}
}

// TestCovClassifyFavoriteProjectValid covers the valid path
// (favorite_authority.go:333).
func TestCovClassifyFavoriteProjectValid(t *testing.T) {
	projects := favoriteProjectIndex{
		byID: map[string][]FavoriteProjectAuthority{
			"/some/path": {{ID: "/some/path", Quality: FavoriteAuthorityComplete}},
		},
	}
	result := classifyFavoriteProject(ArchiveKey{Kind: "project", ID: "/some/path"}, projects)
	if result.State != FavoriteDecisionValid {
		t.Fatalf("expected valid, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionAmbiguousNode covers the ambiguousIDs branch
// (favorite_authority.go:280-281).
func TestCovClassifyFavoriteSessionAmbiguousNode(t *testing.T) {
	sessions := favoriteSessionIndex{}
	nodes := favoriteNodeIndex{ambiguousIDs: map[string]bool{"s1": true}}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionClusterNode covers the clusterIDs branch
// (favorite_authority.go:283-287).
func TestCovClassifyFavoriteSessionClusterNode(t *testing.T) {
	sessions := favoriteSessionIndex{}
	nodes := favoriteNodeIndex{clusterIDs: map[string]bool{"s1": true}}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionConfirmedInvalid {
		t.Fatalf("expected confirmed-invalid, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionAmbiguousAlias covers the ambiguousAlias branch
// (favorite_authority.go:290-291).
func TestCovClassifyFavoriteSessionAmbiguousAlias(t *testing.T) {
	sessions := favoriteSessionIndex{
		ambiguousAlias: map[string]bool{"s1": true},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionNoIDsNoRef covers the zero-ids, unparseable ref
// path (favorite_authority.go:294-302).
func TestCovClassifyFavoriteSessionNoIDsNoRef(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "not-a-ref"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionLocalRefDormant covers the local-ref parse
// path where the session ID validates (favorite_authority.go:295-300).
func TestCovClassifyFavoriteSessionLocalRefDormant(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "local:02wMz5Txv1C3Hut0M8GCeB"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
	if result.CanonicalKey.ID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("expected canonical key to be the session ID, got %q", result.CanonicalKey.ID)
	}
}

// TestCovClassifyFavoriteSessionMultipleIDs covers the len(ids) != 1 branch
// (favorite_authority.go:304-305).
func TestCovClassifyFavoriteSessionMultipleIDs(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a", "b"}},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionAmbiguousID covers the ambiguousIDs branch
// (favorite_authority.go:304-305).
func TestCovClassifyFavoriteSessionAmbiguousID(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias:      map[string][]string{"s1": {"a"}},
		ambiguousIDs: map[string]bool{"a": true},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionMultipleAuthorities covers the
// len(authorities) != 1 branch (favorite_authority.go:308-309).
func TestCovClassifyFavoriteSessionMultipleAuthorities(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {
				{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete, TopLevel: true},
				{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete, TopLevel: true},
			},
		},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionAuthorityAmbiguousNode covers the
// authority ambiguousIDs branch (favorite_authority.go:312-313).
func TestCovClassifyFavoriteSessionAuthorityAmbiguousNode(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete, TopLevel: true}},
		},
	}
	nodes := favoriteNodeIndex{ambiguousIDs: map[string]bool{"a": true}}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionIncompleteLineage covers the incomplete
// lineage branch (favorite_authority.go:316-317).
func TestCovClassifyFavoriteSessionIncompleteLineage(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {{ID: "a", Lineage: FavoriteAuthorityIncomplete, Source: FavoriteAuthorityComplete, TopLevel: true}},
		},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
	if result.CanonicalKey.ID != "a" {
		t.Fatalf("expected canonical key 'a', got %q", result.CanonicalKey.ID)
	}
}

// TestCovClassifyFavoriteSessionIncompleteSource covers the incomplete
// source branch (favorite_authority.go:316-317).
func TestCovClassifyFavoriteSessionIncompleteSource(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityIncomplete, TopLevel: true}},
		},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("expected dormant, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionNotTopLevel covers the not-top-level branch
// (favorite_authority.go:319-320).
func TestCovClassifyFavoriteSessionNotTopLevel(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete, TopLevel: false}},
		},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionConfirmedInvalid {
		t.Fatalf("expected confirmed-invalid, got %v", result.State)
	}
}

// TestCovClassifyFavoriteSessionValid covers the valid branch
// (favorite_authority.go:322).
func TestCovClassifyFavoriteSessionValid(t *testing.T) {
	sessions := favoriteSessionIndex{
		byAlias: map[string][]string{"s1": {"a"}},
		byID: map[string][]FavoriteSessionAuthority{
			"a": {{ID: "a", Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete, TopLevel: true}},
		},
	}
	nodes := favoriteNodeIndex{}
	result := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: "s1"}, sessions, nodes)
	if result.State != FavoriteDecisionValid {
		t.Fatalf("expected valid, got %v", result.State)
	}
}

// --- roster.go: SubagentState ---

// TestCovRosterSubagentStateNotFound covers Roster.SubagentState for a
// session that is not in the roster (roster.go:434-445).
func TestCovRosterSubagentStateNotFound(t *testing.T) {
	r := NewRoster("", nil)
	if state, ok := r.SubagentState("nonexistent"); ok || state != "" {
		t.Fatalf("expected empty state, not found; got %q, %v", state, ok)
	}
}

// TestCovRosterSubagentStateEmptyID covers the empty sessionID guard
// (roster.go:435-436).
func TestCovRosterSubagentStateEmptyID(t *testing.T) {
	r := NewRoster("", nil)
	if state, ok := r.SubagentState(""); ok || state != "" {
		t.Fatalf("expected empty state, not found; got %q, %v", state, ok)
	}
}

// --- deletion_store.go ---

// TestCovSaveDeletionSnapshotFSError covers saveDeletionSnapshotFS with a
// failing filesystem (deletion_store.go:304-305).
func TestCovSaveDeletionSnapshotFSError(t *testing.T) {
	fs := failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errCovBoom}
	_, err := saveDeletionSnapshotFS(fs, "/nonexistent", deletionSnapshot{Version: deletionSnapshotVersion}, deletionStoreFaults{})
	if err == nil {
		t.Fatal("saveDeletionSnapshotFS should error with failing fs")
	}
}

// TestCovSaveDeletionSnapshotFSEmptyRoot covers the empty stateRoot branch
// (deletion_store.go:292-293).
func TestCovSaveDeletionSnapshotFSEmptyRoot(t *testing.T) {
	ok, err := saveDeletionSnapshotFS(afero.NewMemMapFs(), "", deletionSnapshot{}, deletionStoreFaults{})
	if err != nil || !ok {
		t.Fatalf("expected ok=true, nil err; got ok=%v err=%v", ok, err)
	}
}

// TestCovLoadDeletionSnapshotFSDecodeError covers loadDeletionSnapshotFS with
// invalid JSON content (deletion_store.go:270-271).
func TestCovLoadDeletionSnapshotFSDecodeError(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateRoot := "/state"
	_ = fs.MkdirAll(filepath.Join(stateRoot, deletionStateSubdir), 0o755)
	_ = afero.WriteFile(fs, deletionStatePath(stateRoot), []byte("invalid json"), 0o644)
	_, err := loadDeletionSnapshotFS(fs, stateRoot)
	if err == nil {
		t.Fatal("loadDeletionSnapshotFS should error with invalid JSON")
	}
}

// TestCovLoadDeletionSnapshotFSEmptyRoot covers the empty stateRoot branch
// (deletion_store.go:257-258).
func TestCovLoadDeletionSnapshotFSEmptyRoot(t *testing.T) {
	state, err := loadDeletionSnapshotFS(afero.NewMemMapFs(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Version != deletionSnapshotVersion {
		t.Fatalf("expected version %d, got %d", deletionSnapshotVersion, state.Version)
	}
}

// TestCovNormalizeDeletionTargetsDedup covers normalizeDeletionTargets with
// duplicate thread IDs (deletion_store.go:215-240).
func TestCovNormalizeDeletionTargetsDedup(t *testing.T) {
	firstID := hubtest.SessionID(t)
	secondID := hubtest.SessionID(t)
	targets := []DeletionTarget{
		{ThreadID: secondID, Ref: "local:" + secondID},
		{ThreadID: firstID, Ref: "local:" + firstID},
		{ThreadID: secondID, Ref: "local:" + secondID},
	}
	normalized, err := normalizeDeletionTargets(targets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []DeletionTarget{
		{ThreadID: firstID, Ref: "local:" + firstID},
		{ThreadID: secondID, Ref: "local:" + secondID},
	}
	if strings.Compare(want[0].Ref, want[1].Ref) > 0 {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized = %#v, want sorted unique targets %#v", normalized, want)
	}
}

// TestCovMarkDeletedNotFound covers MarkDeleted with a non-existent generation
// (deletion_store.go:155).
func TestCovMarkDeletedNotFound(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	want := "deletion generation nonexistent-0123456789/1 not found"
	if err := store.MarkDeleted("nonexistent-0123456789", 1); err == nil || err.Error() != want {
		t.Fatalf("MarkDeleted error = %v, want %q", err, want)
	}
}

// TestCovMarkDeletedAlreadyDeleted covers MarkDeleted when the record is
// already deleted (deletion_store.go:149-150).
func TestCovMarkDeletedAlreadyDeleted(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	// Begin a deletion so there's a record to mark deleted.
	record, err := store.Begin("project-delete-0123456789", []DeletionTarget{
		{ThreadID: "02wMz5Txv1C3Hut0M8GCeB", Ref: "local:02wMz5Txv1C3Hut0M8GCeB"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.MarkDeleted("project-delete-0123456789", record.Generation); err != nil {
		t.Fatalf("first MarkDeleted: %v", err)
	}
	// Second call with same generation should be idempotent and leave no
	// in-progress record behind.
	if err := store.MarkDeleted("project-delete-0123456789", record.Generation); err != nil {
		t.Fatalf("second MarkDeleted: %v", err)
	}
	if got := store.Deleting(); got != nil {
		t.Fatalf("Deleting after idempotent MarkDeleted = %#v, want nil", got)
	}
}

// --- pin_section.go: openWithImmediateTransaction ---

// TestCovPinSectionOpenWithImmediateTransactionMkdirError covers the
// MkdirAll error branch (pin_section.go:112-113).
func TestCovPinSectionOpenWithImmediateTransactionMkdirError(t *testing.T) {
	store := NewPinSectionStore("/nonexistent-root/index.db")
	store.fs = failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errCovBoom}
	_, err := store.openWithImmediateTransaction(false)
	if !errors.Is(err, errCovBoom) {
		t.Fatalf("expected errCovBoom, got %v", err)
	}
}
