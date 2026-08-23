package hubcore

import (
	"testing"

	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
)

// TestIndexFavoriteSessionsEmptyID covers the empty-ID skip path.
func TestIndexFavoriteSessionsEmptyID(t *testing.T) {
	authorities := []FavoriteSessionAuthority{
		{ID: ""},
		{ID: "session-1"},
	}
	index := indexFavoriteSessions(authorities)
	if len(index.byID) != 1 {
		t.Fatalf("byID = %v, want 1 entry", index.byID)
	}
}

// TestIndexFavoriteSessionsEmptyAlias covers the empty-alias skip path.
func TestIndexFavoriteSessionsEmptyAlias(t *testing.T) {
	authorities := []FavoriteSessionAuthority{
		{ID: "session-1", Aliases: []string{"", "alias-1"}},
	}
	index := indexFavoriteSessions(authorities)
	// The ID "session-1" is always indexed as an alias, plus "alias-1".
	// The empty string "" should NOT be in the alias map.
	if _, ok := index.byAlias[""]; ok {
		t.Fatalf("empty string should not be in byAlias")
	}
	if _, ok := index.byAlias["alias-1"]; !ok {
		t.Fatalf("alias-1 should be in byAlias")
	}
}

// TestIndexFavoriteSessionsAmbiguous covers the ambiguous detection.
func TestIndexFavoriteSessionsAmbiguous(t *testing.T) {
	authorities := []FavoriteSessionAuthority{
		{ID: "session-1"},
		{ID: "session-1"},
	}
	index := indexFavoriteSessions(authorities)
	if !index.ambiguousIDs["session-1"] {
		t.Fatalf("session-1 should be ambiguous")
	}
}

// TestIndexFavoriteProjectsEmptyID covers the empty-ID skip path.
func TestIndexFavoriteProjectsEmptyID(t *testing.T) {
	authorities := []FavoriteProjectAuthority{
		{ID: ""},
		{ID: "project-1"},
	}
	index := indexFavoriteProjects(authorities)
	if len(index.byID) != 1 {
		t.Fatalf("byID = %v, want 1 entry", index.byID)
	}
}

// TestIndexFavoriteProjectsAmbiguous covers the ambiguous detection with
// different claim keys.
func TestIndexFavoriteProjectsAmbiguous(t *testing.T) {
	authorities := []FavoriteProjectAuthority{
		{ID: "project-1", ClaimKey: "a"},
		{ID: "project-1", ClaimKey: "b"},
	}
	index := indexFavoriteProjects(authorities)
	if !index.ambiguousIDs["project-1"] {
		t.Fatalf("project-1 should be ambiguous with different claim keys")
	}
}

// TestIndexFavoriteProjectsAmbiguousEmptyClaim covers the ambiguous detection
// when one authority has an empty claim key.
func TestIndexFavoriteProjectsAmbiguousEmptyClaim(t *testing.T) {
	authorities := []FavoriteProjectAuthority{
		{ID: "project-1", ClaimKey: ""},
		{ID: "project-1", ClaimKey: "a"},
	}
	index := indexFavoriteProjects(authorities)
	if !index.ambiguousIDs["project-1"] {
		t.Fatalf("project-1 should be ambiguous with mixed empty/non-empty claim keys")
	}
}

// TestIndexFavoriteNodesEmptyID covers the empty-ID skip path.
func TestIndexFavoriteNodesEmptyID(t *testing.T) {
	authorities := []FavoriteNodeAuthority{
		{ID: ""},
		{ID: "node-1", Quality: FavoriteAuthorityComplete, Kind: FavoriteNodeSession},
	}
	sessions := indexFavoriteSessions(nil)
	index := indexFavoriteNodes(authorities, sessions)
	if len(index.byID) != 1 {
		t.Fatalf("byID = %v, want 1 entry", index.byID)
	}
}

// TestIndexFavoriteNodesAmbiguous covers the ambiguous detection for
// incomplete authority.
func TestIndexFavoriteNodesAmbiguous(t *testing.T) {
	authorities := []FavoriteNodeAuthority{
		{ID: "node-1", Quality: FavoriteAuthorityIncomplete, Kind: FavoriteNodeSession},
	}
	sessions := indexFavoriteSessions(nil)
	index := indexFavoriteNodes(authorities, sessions)
	if !index.ambiguousIDs["node-1"] {
		t.Fatalf("node-1 should be ambiguous with partial quality")
	}
}

// TestIndexFavoriteNodesUnknownKind covers the unknown-kind path.
func TestIndexFavoriteNodesUnknownKind(t *testing.T) {
	authorities := []FavoriteNodeAuthority{
		{ID: "node-1", Quality: FavoriteAuthorityComplete, Kind: "unknown"},
	}
	sessions := indexFavoriteSessions(nil)
	index := indexFavoriteNodes(authorities, sessions)
	if !index.ambiguousIDs["node-1"] {
		t.Fatalf("node-1 should be ambiguous with unknown kind")
	}
}

// TestIndexFavoriteNodesCluster covers the cluster node path.
func TestIndexFavoriteNodesCluster(t *testing.T) {
	authorities := []FavoriteNodeAuthority{
		{ID: "cluster-1", Quality: FavoriteAuthorityComplete, Kind: FavoriteNodeCluster},
	}
	sessions := indexFavoriteSessions(nil)
	index := indexFavoriteNodes(authorities, sessions)
	if !index.clusterIDs["cluster-1"] {
		t.Fatalf("cluster-1 should be in clusterIDs")
	}
}

// TestIndexFavoriteNodesClusterAmbiguous covers the cluster-collision path.
func TestIndexFavoriteNodesClusterAmbiguous(t *testing.T) {
	authorities := []FavoriteNodeAuthority{
		{ID: "session-1", Quality: FavoriteAuthorityComplete, Kind: FavoriteNodeCluster},
	}
	sessions := indexFavoriteSessions([]FavoriteSessionAuthority{
		{ID: "session-1"},
	})
	index := indexFavoriteNodes(authorities, sessions)
	if !index.ambiguousIDs["session-1"] {
		t.Fatalf("session-1 should be ambiguous (cluster + session collision)")
	}
}

// TestClassifyFavoriteDecisionUnknown covers the default (dormant) path.
func TestClassifyFavoriteDecisionUnknown(t *testing.T) {
	key := ArchiveKey{Kind: "unknown", ID: "x"}
	result := classifyFavoriteDecision(key, favoriteSessionIndex{}, favoriteProjectIndex{}, favoriteNodeIndex{})
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("state = %v, want dormant", result.State)
	}
}

// TestClassifyFavoriteSessionAliasToCanonicalRef covers the alias-to-canonical
// ref path where the key ID parses as a local ref.
func TestClassifyFavoriteSessionAliasToCanonicalRef(t *testing.T) {
	// A session ID that looks like a local ref but isn't in the index.
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := hubapi.Ref{HostID: "local", SessionID: sessionID}
	if err := identifier.ValidateSessionID(sessionID); err != nil {
		t.Skip("session ID doesn't validate on this platform")
	}
	key := ArchiveKey{Kind: "session", ID: ref.String()}
	sessions := indexFavoriteSessions(nil)
	nodes := indexFavoriteNodes(nil, sessions)
	result := classifyFavoriteSession(key, sessions, nodes)
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("state = %v, want dormant", result.State)
	}
	if result.CanonicalKey.ID != sessionID {
		t.Fatalf("canonical key ID = %q, want %q", result.CanonicalKey.ID, sessionID)
	}
}
