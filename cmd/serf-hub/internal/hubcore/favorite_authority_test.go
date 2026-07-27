package hubcore

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubtest"
)

var favoriteAuthorityTestTime = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestClassifyFavoriteDecisions_EndedLocalTopLevelSessionIsValid(t *testing.T) {
	sessionID := hubtest.SessionID(t)
	key := ArchiveKey{Kind: "session", ID: sessionID}
	authority := FavoriteAuthority{
		Sessions: favoriteSessionAuthoritiesFromMetas(t, []schema.SessionMeta{{
			ID:        sessionID,
			CreatedAt: favoriteAuthorityTestTime.Add(-time.Hour),
			UpdatedAt: favoriteAuthorityTestTime,
		}}, true),
	}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{key: true}, authority)
	assertFavoriteClassification(t, got, key, FavoriteDecisionValid)
	if !got.Presentation[key] {
		t.Fatalf("ended local top-level session is not in presentation: %#v", got.Presentation)
	}
}

func TestClassifyFavoriteDecisions_FullAuthorityIncludesCappedAwayTopLevelSession(t *testing.T) {
	metas := make([]schema.SessionMeta, 0, 51)
	var target string
	for i := range 51 {
		id := hubtest.SessionID(t)
		if i == 50 {
			target = id
		}
		metas = append(metas, schema.SessionMeta{
			ID:        id,
			CreatedAt: favoriteAuthorityTestTime.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: favoriteAuthorityTestTime.Add(-time.Duration(i) * time.Minute),
		})
	}
	key := ArchiveKey{Kind: "session", ID: target}
	authority := FavoriteAuthority{Sessions: favoriteSessionAuthoritiesFromMetas(t, metas, true)}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{key: true}, authority)
	assertFavoriteClassification(t, got, key, FavoriteDecisionValid)
	if !got.Presentation[key] {
		t.Fatalf("capped-away top-level session is not in presentation: %#v", got.Presentation)
	}
}

func TestClassifyFavoriteDecisions_NestedSubagentAndForkSupersededParentAreConfirmedInvalid(t *testing.T) {
	parentID := hubtest.SessionID(t)
	subagentID := hubtest.SessionID(t)
	forkID := hubtest.SessionID(t)
	branchID := hubtest.SessionID(t)
	metas := []schema.SessionMeta{
		{ID: parentID},
		{ID: subagentID, ParentSessionID: parentID, IsSubagent: true},
		{ID: forkID, ForkLabel: "before edit"},
		{ID: branchID, ParentSessionID: forkID},
	}
	authority := FavoriteAuthority{Sessions: favoriteSessionAuthoritiesFromMetas(t, metas, true)}
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: subagentID}: true,
		{Kind: "session", ID: forkID}:     true,
	}

	got := ClassifyFavoriteDecisions(decisions, authority)
	for _, key := range []ArchiveKey{{Kind: "session", ID: subagentID}, {Kind: "session", ID: forkID}} {
		assertFavoriteClassification(t, got, key, FavoriteDecisionConfirmedInvalid)
		if got.Presentation[key] {
			t.Errorf("confirmed-invalid decision entered presentation: %v", key)
		}
	}
}

func TestClassifyFavoriteDecisions_OrphanForkIsValid(t *testing.T) {
	forkID := hubtest.SessionID(t)
	authority := FavoriteAuthority{Sessions: favoriteSessionAuthoritiesFromMetas(t, []schema.SessionMeta{{
		ID:        forkID,
		ForkLabel: "orphan",
	}}, true)}
	key := ArchiveKey{Kind: "session", ID: forkID}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{key: true}, authority)
	assertFavoriteClassification(t, got, key, FavoriteDecisionValid)
	if !got.Presentation[key] {
		t.Fatalf("orphan fork is not in presentation: %#v", got.Presentation)
	}
}

func TestClassifyFavoriteDecisions_UnverifiableEvidenceIsDormant(t *testing.T) {
	remoteID := "remote-test:" + hubtest.SessionID(t)
	missingID := hubtest.SessionID(t)
	aliasID := hubtest.SessionID(t)
	otherAliasID := hubtest.SessionID(t)
	malformedID := hubtest.SessionID(t)
	collisionID := "cluster:" + hubtest.SessionID(t)
	alias := "local:" + hubtest.SessionID(t)
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: remoteID}:    true,
		{Kind: "session", ID: missingID}:   true,
		{Kind: "session", ID: alias}:       true,
		{Kind: "session", ID: malformedID}: true,
		{Kind: "session", ID: collisionID}: true,
	}
	authority := FavoriteAuthority{
		Sessions: []FavoriteSessionAuthority{
			{ID: remoteID, Aliases: []string{remoteID}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityIncomplete},
			{ID: aliasID, Aliases: []string{alias}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
			{ID: otherAliasID, Aliases: []string{alias}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
			{ID: malformedID, Aliases: []string{malformedID}, TopLevel: false, Lineage: FavoriteAuthorityIncomplete, Source: FavoriteAuthorityComplete},
			{ID: collisionID, Aliases: []string{collisionID}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
		},
		Nodes: []FavoriteNodeAuthority{{ID: collisionID, Kind: FavoriteNodeCluster, Quality: FavoriteAuthorityComplete}},
	}

	got := ClassifyFavoriteDecisions(decisions, authority)
	for key := range decisions {
		assertFavoriteClassification(t, got, key, FavoriteDecisionDormant)
		if got.Presentation[key] {
			t.Errorf("dormant decision entered presentation: %v", key)
		}
	}
}

func TestClassifyFavoriteDecisions_ClusterInvalidityUsesCurrentNodeKind(t *testing.T) {
	clusterID := "cluster:deadbeef"
	clusterKey := ArchiveKey{Kind: "session", ID: clusterID}
	clusterAuthority := FavoriteAuthority{
		Nodes: []FavoriteNodeAuthority{{ID: clusterID, Kind: FavoriteNodeCluster, Quality: FavoriteAuthorityComplete}},
	}
	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{clusterKey: true}, clusterAuthority)
	assertFavoriteClassification(t, got, clusterKey, FavoriteDecisionConfirmedInvalid)

	legitimateID := "cluster:" + hubtest.SessionID(t)
	legitimateKey := ArchiveKey{Kind: "session", ID: legitimateID}
	legitimateAuthority := FavoriteAuthority{
		Sessions: []FavoriteSessionAuthority{{
			ID: legitimateID, Aliases: []string{legitimateID}, TopLevel: true,
			Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete,
		}},
	}
	got = ClassifyFavoriteDecisions(map[ArchiveKey]bool{legitimateKey: true}, legitimateAuthority)
	assertFavoriteClassification(t, got, legitimateKey, FavoriteDecisionValid)
}

func TestClassifyFavoriteDecisions_LocalRefAliasMustResolveOneToOne(t *testing.T) {
	canonicalID := hubtest.SessionID(t)
	aliasKey := ArchiveKey{Kind: "session", ID: "local:" + canonicalID}
	authority := FavoriteAuthority{Sessions: []FavoriteSessionAuthority{{
		ID: canonicalID, Aliases: []string{aliasKey.ID}, TopLevel: true,
		Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete,
	}}}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{aliasKey: true}, authority)
	assertFavoriteClassification(t, got, aliasKey, FavoriteDecisionValid)
	canonicalKey := ArchiveKey{Kind: "session", ID: canonicalID}
	if !got.Presentation[canonicalKey] {
		t.Fatalf("alias did not project under canonical key: %#v", got.Presentation)
	}

	firstID := hubtest.SessionID(t)
	secondID := hubtest.SessionID(t)
	ambiguousKey := ArchiveKey{Kind: "session", ID: "local:ambiguous"}
	got = ClassifyFavoriteDecisions(map[ArchiveKey]bool{ambiguousKey: true}, FavoriteAuthority{Sessions: []FavoriteSessionAuthority{
		{ID: firstID, Aliases: []string{ambiguousKey.ID}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
		{ID: secondID, Aliases: []string{ambiguousKey.ID}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
	}})
	assertFavoriteClassification(t, got, ambiguousKey, FavoriteDecisionDormant)
}

func TestClassifyFavoriteDecisions_ProjectUsesCanonicalIdentity(t *testing.T) {
	projectID := filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "alpha"))
	canonicalKey := ArchiveKey{Kind: "project", ID: projectID}
	authority := FavoriteAuthority{Projects: []FavoriteProjectAuthority{{ID: projectID, Quality: FavoriteAuthorityComplete}}}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{canonicalKey: true}, authority)
	assertFavoriteClassification(t, got, canonicalKey, FavoriteDecisionValid)

	legacyKey := ArchiveKey{Kind: "project", ID: "alpha"}
	got = ClassifyFavoriteDecisions(map[ArchiveKey]bool{legacyKey: true}, authority)
	assertFavoriteClassification(t, got, legacyKey, FavoriteDecisionDormant)
}

func TestClassifyFavoriteDecisions_FalseDecisionsAndPersistenceRemainUntouched(t *testing.T) {
	falseID := hubtest.SessionID(t)
	trueID := hubtest.SessionID(t)
	falseKey := ArchiveKey{Kind: "session", ID: falseID}
	trueKey := ArchiveKey{Kind: "session", ID: trueID}
	decisions := map[ArchiveKey]bool{falseKey: false, trueKey: true}
	before := map[ArchiveKey]bool{falseKey: false, trueKey: true}
	authority := FavoriteAuthority{Sessions: []FavoriteSessionAuthority{{
		ID: trueID, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete,
	}}}

	got := ClassifyFavoriteDecisions(decisions, authority)
	if got.Presentation[falseKey] {
		t.Fatal("false decision entered presentation")
	}
	if !got.Presentation[trueKey] {
		t.Fatal("true valid decision did not enter presentation")
	}
	if !reflect.DeepEqual(decisions, before) {
		t.Fatalf("classifier mutated stored decision input: got %#v, want %#v", decisions, before)
	}

	store := NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := store.Set(trueKey.Kind, trueKey.ID, true, favoriteAuthorityTestTime); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(falseKey.Kind, falseKey.ID, false, favoriteAuthorityTestTime); err != nil {
		t.Fatal(err)
	}
	storedBefore, err := store.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	_ = ClassifyFavoriteDecisions(storedBefore, authority)
	storedAfter, err := store.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedBefore, storedAfter) {
		t.Fatalf("classifier changed FavoriteStore contents: before %#v, after %#v", storedBefore, storedAfter)
	}
}

func favoriteSessionAuthoritiesFromMetas(t *testing.T, metas []schema.SessionMeta, complete bool) []FavoriteSessionAuthority {
	t.Helper()
	topLevel := TopLevelSessionIDs(metas)
	out := make([]FavoriteSessionAuthority, 0, len(metas))
	for _, meta := range metas {
		aliases := []string{meta.ID}
		if meta.ID != "" {
			aliases = append(aliases, "local:"+meta.ID)
		}
		quality := FavoriteAuthorityIncomplete
		if complete {
			quality = FavoriteAuthorityComplete
		}
		_, isTopLevel := topLevel[meta.ID]
		out = append(out, FavoriteSessionAuthority{
			ID:       meta.ID,
			Aliases:  aliases,
			TopLevel: isTopLevel,
			Lineage:  quality,
			Source:   quality,
		})
	}
	return out
}

func assertFavoriteClassification(t *testing.T, got FavoriteRevalidation, key ArchiveKey, want FavoriteDecisionState) {
	t.Helper()
	classification, ok := got.Classifications[key]
	if !ok {
		t.Fatalf("classification missing for %v: %#v", key, got.Classifications)
	}
	if classification.State != want {
		t.Fatalf("classification for %v = %q, want %q", key, classification.State, want)
	}
}
