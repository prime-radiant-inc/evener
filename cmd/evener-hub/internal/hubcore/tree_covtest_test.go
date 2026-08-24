package hubcore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

var covErrBoom = errors.New("boom")

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
	if !ok || len(rows) != 0 || rem != 0 {
		t.Fatalf("offset beyond end: rows=%v rem=%d ok=%v", rows, rem, ok)
	}
}

// TestCovTreePageRecentTier covers the recent tier path (tree.go:265-267).
func TestCovTreePageRecentTier(t *testing.T) {
	p := TreeProject{
		allRecent: []TreeNode{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}},
	}
	rows, rem, ok := p.Page("recent", 0, 2)
	if !ok || len(rows) != 2 || rem != 1 {
		t.Fatalf("recent page: rows=%d rem=%d ok=%v", len(rows), rem, ok)
	}
	rows2, rem2, ok2 := p.Page("recent", 2, 2)
	if !ok2 || len(rows2) != 1 || rem2 != 0 {
		t.Fatalf("recent page 2: rows=%d rem=%d ok=%v", len(rows2), rem2, ok2)
	}
}

// TestCovTreePageArchivedTier covers the archived tier (tree.go:269-271).
func TestCovTreePageArchivedTier(t *testing.T) {
	p := TreeProject{
		Archived: []TreeNode{{ID: "s1"}, {ID: "s2"}},
	}
	rows, rem, ok := p.Page("archived", 0, 1)
	if !ok || len(rows) != 1 || rem != 1 {
		t.Fatalf("archived page: rows=%d rem=%d ok=%v", len(rows), rem, ok)
	}
}

// TestCovTreePageCurrentFallback covers the nil allCurrent → Current fallback
// (tree.go:261-262).
func TestCovTreePageCurrentFallback(t *testing.T) {
	p := TreeProject{Current: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("current", 0, 10)
	if !ok || len(rows) != 1 || rem != 0 {
		t.Fatalf("current fallback: rows=%d rem=%d ok=%v", len(rows), rem, ok)
	}
}

// TestCovTreePageRecentFallback covers the nil allRecent → Recent fallback
// (tree.go:266-267).
func TestCovTreePageRecentFallback(t *testing.T) {
	p := TreeProject{Recent: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("recent", 0, 10)
	if !ok || len(rows) != 1 || rem != 0 {
		t.Fatalf("recent fallback: rows=%d rem=%d ok=%v", len(rows), rem, ok)
	}
}

// TestCovTreePageArchivedFallback covers the nil allArchived → Archived
// fallback (tree.go:271-272).
func TestCovTreePageArchivedFallback(t *testing.T) {
	p := TreeProject{Archived: []TreeNode{{ID: "s1"}}}
	rows, rem, ok := p.Page("archived", 0, 10)
	if !ok || len(rows) != 1 || rem != 0 {
		t.Fatalf("archived fallback: rows=%d rem=%d ok=%v", len(rows), rem, ok)
	}
}

// --- tree.go: resolveProjectMap ---

// TestCovResolveProjectMapStrictError covers the strict error branch
// (tree.go:582-583) — a meta with a path that fails project resolution
// produces an error.
func TestCovResolveProjectMapStrictError(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/nonexistent/path/that/does/not/exist"}},
	}
	_, err := ResolveProjectMapStrict(metas, nil)
	if err == nil {
		t.Fatal("ResolveProjectMapStrict should error for unresolvable path")
	}
}

// TestCovResolveProjectMapStrictLiveError covers the strict error branch for
// live entries (tree.go:602-603).
func TestCovResolveProjectMapStrictLiveError(t *testing.T) {
	live := []LiveEntry{
		{SessionID: "02wMz5Txv1C3Hut0M8GCeB", WorkingDir: "/nonexistent/path/that/does/not/exist"},
	}
	_, err := ResolveProjectMapStrict(nil, live)
	if err == nil {
		t.Fatal("ResolveProjectMapStrict should error for unresolvable live path")
	}
}

// TestCovResolveProjectMapNonStrictSkipsErrors covers the non-strict
// continue path (tree.go:584-585) for metas and (tree.go:604-605) for live.
func TestCovResolveProjectMapNonStrictSkipsErrors(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/nonexistent/path/that/does/not/exist"}},
		{ID: "02wMz5Txv2enqVTitaig6F", EnvInfo: schema.EnvironmentInfo{WorkingDir: ""}},
	}
	live := []LiveEntry{
		{SessionID: "02wMz5Txv3enqVTitaig6F", WorkingDir: "/nonexistent/path/that/does/not/exist"},
		{SessionID: "02wMz5Txv4enqVTitaig6F", WorkingDir: ""},
	}
	projects := ResolveProjectMap(metas, live)
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects (all skipped), got %d", len(projects))
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
	if len(projects) == 0 {
		t.Fatal("expected at least 1 project")
	}
}

// --- tree.go: FavoriteCandidates ---

// TestCovFavoriteCandidatesCoversAllKinds exercises FavoriteCandidates with
// session, fork, cluster, and empty-ID nodes to cover the switch branches
// (tree.go:62-71) and the empty-ID guard (tree.go:48-49).
func TestCovFavoriteCandidatesCoversAllKinds(t *testing.T) {
	tree := Tree{
		favoriteLive: []TreeNode{
			{ID: "live-1", Kind: "session"},
		},
		Projects: []TreeProject{
			{allCurrent: []TreeNode{
				{ID: "", Kind: "session"},       // empty ID skipped
				{ID: "live-1", Kind: "session"}, // duplicate skipped
				{ID: "fork-1", Kind: "fork"},
				{Kind: "cluster", Children: []TreeNode{
					{ID: "child-1", Kind: "session"},
					{ID: "child-2", Kind: "fork"},
					{ID: "child-3", Kind: "subagent"}, // skipped
				}},
				{ID: "other-1", Kind: "other"}, // skipped
			}},
			{allRecent: []TreeNode{
				{ID: "recent-1", Kind: "session"},
			}},
		},
	}
	got := tree.FavoriteCandidates()
	ids := make(map[string]bool)
	for _, n := range got {
		ids[n.ID] = true
	}
	for _, want := range []string{"live-1", "fork-1", "child-1", "child-2", "recent-1"} {
		if !ids[want] {
			t.Errorf("FavoriteCandidates missing %q; got %v", want, ids)
		}
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
	_, err := idx.Rebuild()
	if err == nil {
		t.Fatal("Rebuild with malformed glob should error")
	}
}

// TestCovPastFindInvalidID covers Find with an invalid session ID
// (past.go:713-714).
func TestCovPastFindInvalidID(t *testing.T) {
	idx := NewPastIndex("")
	if _, ok := idx.Find("not-a-valid-ulid"); ok {
		t.Fatal("Find with invalid ID should return false")
	}
}

// TestCovPastFindEmptyStateGlob covers Find with a valid ID but empty
// stateGlob (past.go:719-720).
func TestCovPastFindEmptyStateGlob(t *testing.T) {
	idx := NewPastIndex("")
	if _, ok := idx.Find("01ABCDEFGHJKMNPQRSTVWX0"); ok {
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
	sources := inferRemoteSources(threads, true)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if _, ok := sources["remote1"]; !ok {
		t.Fatal("expected remote1 source")
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
	fs := failingFs{Fs: afero.NewMemMapFs(), mkdirErr: covErrBoom}
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
	targets := []DeletionTarget{
		{ThreadID: "02wMz5Txv1C3Hut0M8GCeB", Ref: "local:02wMz5Txv1C3Hut0M8GCeB"},
		{ThreadID: "02wMz5Txv1C3Hut0M8GCeB", Ref: "local:02wMz5Txv1C3Hut0M8GCeB"},
		{ThreadID: "02wMz5Txv2enqVTitaig6F", Ref: "local:02wMz5Txv2enqVTitaig6F"},
	}
	normalized, err := normalizeDeletionTargets(targets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("expected 2 unique targets, got %d", len(normalized))
	}
}

// TestCovMarkDeletedNotFound covers MarkDeleted with a non-existent generation
// (deletion_store.go:155).
func TestCovMarkDeletedNotFound(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	if err := store.MarkDeleted("nonexistent-0123456789", 1); err == nil {
		t.Fatal("MarkDeleted should error for non-existent generation")
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
	// Second call with same generation should be idempotent.
	if err := store.MarkDeleted("project-delete-0123456789", record.Generation); err != nil {
		t.Fatalf("second MarkDeleted: %v", err)
	}
}

// --- pin_section.go: openWithImmediateTransaction ---

// TestCovPinSectionOpenWithImmediateTransactionImmediate covers the
// immediate=true branch of openWithImmediateTransaction (pin_section.go:116-117).
func TestCovPinSectionOpenWithImmediateTransactionImmediate(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	store.openDB = func(_, dsn string) (*sql.DB, error) {
		if !strings.Contains(dsn, "_txlock=immediate") {
			t.Fatalf("expected immediate txlock in DSN, got %q", dsn)
		}
		return sql.Open("sqlite", strings.TrimSuffix(dsn, "&_txlock=immediate"))
	}
	db, err := store.openWithImmediateTransaction(true)
	if err != nil {
		t.Fatalf("openWithImmediateTransaction(immediate): %v", err)
	}
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	defer func() { _ = db.Close() }()
}

// TestCovPinSectionOpenWithImmediateTransactionMkdirError covers the
// MkdirAll error branch (pin_section.go:112-113).
func TestCovPinSectionOpenWithImmediateTransactionMkdirError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join("/nonexistent-root", "index.db"))
	store.fs = failingFs{Fs: afero.NewMemMapFs(), mkdirErr: covErrBoom}
	_, err := store.openWithImmediateTransaction(false)
	if !errors.Is(err, covErrBoom) {
		t.Fatalf("expected covErrBoom, got %v", err)
	}
}
