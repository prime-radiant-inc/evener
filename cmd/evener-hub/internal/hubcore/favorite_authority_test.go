package hubcore

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
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
	alias := "local:" + hubtest.SessionID(t)
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: remoteID}:    true,
		{Kind: "session", ID: missingID}:   true,
		{Kind: "session", ID: alias}:       true,
		{Kind: "session", ID: malformedID}: true,
	}
	authority := FavoriteAuthority{
		Sessions: []FavoriteSessionAuthority{
			{ID: remoteID, Aliases: []string{remoteID}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityIncomplete},
			{ID: aliasID, Aliases: []string{alias}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
			{ID: otherAliasID, Aliases: []string{alias}, TopLevel: true, Lineage: FavoriteAuthorityComplete, Source: FavoriteAuthorityComplete},
			{ID: malformedID, Aliases: []string{malformedID}, TopLevel: false, Lineage: FavoriteAuthorityIncomplete, Source: FavoriteAuthorityComplete},
		},
	}

	got := ClassifyFavoriteDecisions(decisions, authority)
	for key := range decisions {
		assertFavoriteClassification(t, got, key, FavoriteDecisionDormant)
		if got.Presentation[key] {
			t.Errorf("dormant decision entered presentation: %v", key)
		}
	}
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

// Two hosts' projects may share an ID. A favorite decision is stored under
// (source, project ID), so classification must match each host's own authority
// instead of collapsing both onto the bare ID and marking the ID ambiguous.
func TestClassifyFavoriteDecisions_ProjectDistinguishesSources(t *testing.T) {
	const projectID = "shared-project"
	decision := ArchiveKey{Kind: "project", ID: projectID, Source: "host-a"}
	authority := FavoriteAuthority{Projects: []FavoriteProjectAuthority{
		{ID: projectID, Source: "host-a", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/a\x00host-a"},
		{ID: projectID, Source: "host-b", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/b\x00host-b"},
	}}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{decision: true}, authority)
	assertFavoriteClassification(t, got, decision, FavoriteDecisionValid)
	if !got.Presentation[decision] {
		t.Fatalf("source-qualified favorite was not presented: %#v", got.Presentation)
	}

	// A source with no authority for that ID stays dormant rather than borrowing
	// another host's authority.
	unknown := ArchiveKey{Kind: "project", ID: projectID, Source: "host-c"}
	got = ClassifyFavoriteDecisions(map[ArchiveKey]bool{unknown: true}, authority)
	assertFavoriteClassification(t, got, unknown, FavoriteDecisionDormant)
	if got.Presentation[unknown] {
		t.Fatalf("unknown source favorite was presented: %#v", got.Presentation)
	}
}

// The rail's project favorite carries no source, so its decision is stored under
// the controller key even when the project is owned by a remote host. When
// exactly one source claims that project ID, the bare decision must resolve to
// it (and present under the host-qualified key) or a successful favorite would
// never show a star. Two sources claiming the ID stay dormant: the bare decision
// cannot choose a host.
func TestClassifyFavoriteDecisions_EmptySourceResolvesSingleAuthority(t *testing.T) {
	const projectID = "shared-project"
	bare := ArchiveKey{Kind: "project", ID: projectID}

	remoteOnly := FavoriteAuthority{Projects: []FavoriteProjectAuthority{
		{ID: projectID, Source: "host-a", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/a\x00host-a"},
	}}
	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{bare: true}, remoteOnly)
	assertFavoriteClassification(t, got, bare, FavoriteDecisionValid)
	canonical := ArchiveKey{Kind: "project", ID: projectID, Source: "host-a"}
	if !got.Presentation[canonical] {
		t.Fatalf("bare decision did not present on the remote authority: %#v", got.Presentation)
	}

	both := FavoriteAuthority{Projects: []FavoriteProjectAuthority{
		{ID: projectID, Source: "host-a", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/a\x00host-a"},
		{ID: projectID, Source: "host-b", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/b\x00host-b"},
	}}
	got = ClassifyFavoriteDecisions(map[ArchiveKey]bool{bare: true}, both)
	assertFavoriteClassification(t, got, bare, FavoriteDecisionDormant)
	if len(got.Presentation) != 0 {
		t.Fatalf("ambiguous bare decision presented something: %#v", got.Presentation)
	}
}

// An empty-source decision still addresses the controller's own project when one
// exists, even while a remote host claims the same ID: exact matches win before
// the cross-source fallback.
func TestClassifyFavoriteDecisions_EmptySourcePrefersControllerAuthority(t *testing.T) {
	const projectID = "shared-project"
	bare := ArchiveKey{Kind: "project", ID: projectID}
	authority := FavoriteAuthority{Projects: []FavoriteProjectAuthority{
		{ID: projectID, Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/local"},
		{ID: projectID, Source: "host-a", Quality: FavoriteAuthorityComplete, ClaimKey: "/srv/a\x00host-a"},
	}}

	got := ClassifyFavoriteDecisions(map[ArchiveKey]bool{bare: true}, authority)
	assertFavoriteClassification(t, got, bare, FavoriteDecisionValid)
	if !got.Presentation[bare] {
		t.Fatalf("controller project favorite not presented on the bare key: %#v", got.Presentation)
	}
}

func TestLocalSessionDecisionAliasesUsesAuthorityAndExcludesRemoteRefs(t *testing.T) {
	const sessionID = "canonical-session"
	authority := FavoriteAuthority{Sessions: []FavoriteSessionAuthority{{
		ID:      sessionID,
		Aliases: []string{sessionID, "local:" + sessionID, "remote:" + sessionID, "local:other"},
	}}}
	got := LocalSessionDecisionAliases(sessionID, authority)
	want := []string{sessionID, "local:" + sessionID}
	if !slices.Equal(got, want) {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
}

func TestLocalSessionDecisionAliasesIncludesCanonicalLocalRefWithoutAuthority(t *testing.T) {
	const sessionID = "canonical-session"
	got := LocalSessionDecisionAliases(sessionID, FavoriteAuthority{})
	want := []string{sessionID, "local:" + sessionID}
	if !slices.Equal(got, want) {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
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
	if err := store.Set("", trueKey.Kind, trueKey.ID, true, favoriteAuthorityTestTime); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("", falseKey.Kind, falseKey.ID, false, favoriteAuthorityTestTime); err != nil {
		t.Fatal(err)
	}
	storedBefore, err := store.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	beforeFlag, beforeDecidedAt := favoriteStoreRow(t, store, falseKey)
	_ = ClassifyFavoriteDecisions(storedBefore, authority)
	storedAfter, err := store.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedBefore, storedAfter) {
		t.Fatalf("classifier changed FavoriteStore contents: before %#v, after %#v", storedBefore, storedAfter)
	}
	afterFlag, afterDecidedAt := favoriteStoreRow(t, store, falseKey)
	if beforeFlag != afterFlag || beforeDecidedAt != afterDecidedAt {
		t.Fatalf("classifier changed false FavoriteStore row: before=(%d,%d), after=(%d,%d)", beforeFlag, beforeDecidedAt, afterFlag, afterDecidedAt)
	}
}

func favoriteStoreRow(t *testing.T, store *FavoriteStore, key ArchiveKey) (int, int64) {
	t.Helper()
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var flag int
	var decidedAt int64
	if err := db.QueryRow(`SELECT favorited, decided_at FROM favorite WHERE kind = ? AND id = ?`, key.Kind, key.ID).Scan(&flag, &decidedAt); err != nil {
		t.Fatal(err)
	}
	return flag, decidedAt
}

func favoriteSessionAuthoritiesFromMetas(t *testing.T, metas []schema.SessionMeta, complete bool) []FavoriteSessionAuthority {
	t.Helper()
	roots := NewRootIndex(metas)
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
		out = append(out, FavoriteSessionAuthority{
			ID:       meta.ID,
			Aliases:  aliases,
			TopLevel: roots.TopLevel(meta.ID),
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

// TestClassifyFavoriteSession pins how one session favorite resolves against
// the authority index: only a single, unambiguous, fully known top-level
// session is valid; a known non-top-level session is confirmed invalid; every
// other shape stays dormant, keeping its canonical key when one is known.
func TestClassifyFavoriteSession(t *testing.T) {
	complete := FavoriteAuthorityComplete
	incomplete := FavoriteAuthorityIncomplete
	single := func(authority FavoriteSessionAuthority) favoriteSessionIndex {
		return favoriteSessionIndex{
			byAlias: map[string][]string{"s1": {"a"}},
			byID:    map[string][]FavoriteSessionAuthority{"a": {authority}},
		}
	}
	// validExcept starts from an index under which s1 is valid, so each
	// ambiguity below is the only reason it stays dormant.
	validExcept := func(ambiguity func(*favoriteSessionIndex)) favoriteSessionIndex {
		index := single(FavoriteSessionAuthority{ID: "a", Lineage: complete, Source: complete, TopLevel: true})
		ambiguity(&index)
		return index
	}
	for _, tc := range []struct {
		name          string
		keyID         string
		sessions      favoriteSessionIndex
		wantState     FavoriteDecisionState
		wantCanonical string
	}{
		{name: "ambiguous alias", keyID: "s1", sessions: validExcept(func(index *favoriteSessionIndex) {
			index.ambiguousAlias = map[string]bool{"s1": true}
		}), wantState: FavoriteDecisionDormant},
		{name: "unknown alias that is not a ref", keyID: "not-a-ref", sessions: favoriteSessionIndex{byAlias: map[string][]string{}}, wantState: FavoriteDecisionDormant},
		{name: "unknown local ref canonicalizes to its session", keyID: "local:02wMz5Txv1C3Hut0M8GCeB", sessions: favoriteSessionIndex{byAlias: map[string][]string{}}, wantState: FavoriteDecisionDormant, wantCanonical: "02wMz5Txv1C3Hut0M8GCeB"},
		{name: "alias naming several sessions", keyID: "s1", sessions: validExcept(func(index *favoriteSessionIndex) {
			index.byAlias["s1"] = []string{"a", "b"}
		}), wantState: FavoriteDecisionDormant},
		{name: "ambiguous session ID", keyID: "s1", sessions: validExcept(func(index *favoriteSessionIndex) {
			index.ambiguousIDs = map[string]bool{"a": true}
		}), wantState: FavoriteDecisionDormant},
		{name: "several authorities for the session", keyID: "s1", sessions: favoriteSessionIndex{
			byAlias: map[string][]string{"s1": {"a"}},
			byID: map[string][]FavoriteSessionAuthority{"a": {
				{ID: "a", Lineage: complete, Source: complete, TopLevel: true},
				{ID: "a", Lineage: complete, Source: complete, TopLevel: true},
			}},
		}, wantState: FavoriteDecisionDormant},
		{name: "incomplete lineage", keyID: "s1", sessions: single(FavoriteSessionAuthority{ID: "a", Lineage: incomplete, Source: complete, TopLevel: true}), wantState: FavoriteDecisionDormant, wantCanonical: "a"},
		{name: "incomplete source", keyID: "s1", sessions: single(FavoriteSessionAuthority{ID: "a", Lineage: complete, Source: incomplete, TopLevel: true}), wantState: FavoriteDecisionDormant, wantCanonical: "a"},
		{name: "not top level", keyID: "s1", sessions: single(FavoriteSessionAuthority{ID: "a", Lineage: complete, Source: complete}), wantState: FavoriteDecisionConfirmedInvalid, wantCanonical: "a"},
		{name: "valid", keyID: "s1", sessions: single(FavoriteSessionAuthority{ID: "a", Lineage: complete, Source: complete, TopLevel: true}), wantState: FavoriteDecisionValid, wantCanonical: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFavoriteSession(ArchiveKey{Kind: "session", ID: tc.keyID}, tc.sessions)
			if got.State != tc.wantState || got.CanonicalKey.ID != tc.wantCanonical {
				t.Fatalf("classifyFavoriteSession(%q) = %+v, want state %v canonical %q", tc.keyID, got, tc.wantState, tc.wantCanonical)
			}
		})
	}
}

// TestClassifyFavoriteProject pins how one project favorite resolves: only a
// single, unambiguous, completely known project authority is valid; anything
// else stays dormant.
func TestClassifyFavoriteProject(t *testing.T) {
	key := projectDecisionKey{id: "/some/path"}
	authority := func(quality FavoriteAuthorityQuality) []FavoriteProjectAuthority {
		return []FavoriteProjectAuthority{{ID: "/some/path", Quality: quality}}
	}
	for _, tc := range []struct {
		name      string
		projects  favoriteProjectIndex
		wantState FavoriteDecisionState
	}{
		{name: "no authority", projects: favoriteProjectIndex{byKey: map[projectDecisionKey][]FavoriteProjectAuthority{}}, wantState: FavoriteDecisionDormant},
		{name: "ambiguous authority", projects: favoriteProjectIndex{
			byKey:         map[projectDecisionKey][]FavoriteProjectAuthority{key: authority(FavoriteAuthorityComplete)},
			ambiguousKeys: map[projectDecisionKey]bool{key: true},
		}, wantState: FavoriteDecisionDormant},
		{name: "incomplete authority", projects: favoriteProjectIndex{byKey: map[projectDecisionKey][]FavoriteProjectAuthority{key: authority(FavoriteAuthorityIncomplete)}}, wantState: FavoriteDecisionDormant},
		{name: "valid", projects: favoriteProjectIndex{byKey: map[projectDecisionKey][]FavoriteProjectAuthority{key: authority(FavoriteAuthorityComplete)}}, wantState: FavoriteDecisionValid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFavoriteProject(ArchiveKey{Kind: "project", ID: "/some/path"}, tc.projects)
			if got.State != tc.wantState {
				t.Fatalf("classifyFavoriteProject = %+v, want state %v", got, tc.wantState)
			}
		})
	}
}

func TestClassifyFavoriteDecisionOfAnUnknownKindIsDormant(t *testing.T) {
	result := classifyFavoriteDecision(ArchiveKey{Kind: "unknown", ID: "x"}, favoriteSessionIndex{}, favoriteProjectIndex{})
	if result.State != FavoriteDecisionDormant {
		t.Fatalf("state = %v, want dormant", result.State)
	}
}

// TestIndexFavoriteSessions pins the session authority index: authorities
// with no ID and empty aliases are left out, and an ID claimed by more than
// one authority is ambiguous.
func TestIndexFavoriteSessions(t *testing.T) {
	index := indexFavoriteSessions([]FavoriteSessionAuthority{
		{ID: ""},
		{ID: "session-1", Aliases: []string{"", "alias-1"}},
		{ID: "session-2"},
		{ID: "session-2"},
	})
	if got := slices.Sorted(maps.Keys(index.byID)); !slices.Equal(got, []string{"session-1", "session-2"}) {
		t.Fatalf("byID keys = %v, want session-1 and session-2", got)
	}
	if got := slices.Sorted(maps.Keys(index.byAlias)); !slices.Equal(got, []string{"alias-1", "session-1", "session-2"}) {
		t.Fatalf("byAlias keys = %v, want alias-1, session-1 and session-2", got)
	}
	if !index.ambiguousIDs["session-2"] || index.ambiguousIDs["session-1"] {
		t.Fatalf("ambiguousIDs = %v, want only session-2", index.ambiguousIDs)
	}
}

// TestIndexFavoriteProjects pins the project authority index: authorities with
// no ID are left out, and several authorities for one key are ambiguous when
// their claim keys differ or any of them carries none.
func TestIndexFavoriteProjects(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authorities   []FavoriteProjectAuthority
		wantKeys      int
		wantAmbiguous bool
	}{
		{name: "empty ID left out", authorities: []FavoriteProjectAuthority{{ID: ""}, {ID: "project-1"}}, wantKeys: 1},
		{name: "different claim keys", authorities: []FavoriteProjectAuthority{{ID: "project-1", ClaimKey: "a"}, {ID: "project-1", ClaimKey: "b"}}, wantKeys: 1, wantAmbiguous: true},
		{name: "an empty claim key", authorities: []FavoriteProjectAuthority{{ID: "project-1", ClaimKey: ""}, {ID: "project-1", ClaimKey: "a"}}, wantKeys: 1, wantAmbiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := indexFavoriteProjects(tc.authorities)
			if len(index.byKey) != tc.wantKeys {
				t.Fatalf("byKey = %v, want %d keys", index.byKey, tc.wantKeys)
			}
			if got := index.ambiguousKeys[projectDecisionKey{id: "project-1"}]; got != tc.wantAmbiguous {
				t.Fatalf("project-1 ambiguous = %t, want %t", got, tc.wantAmbiguous)
			}
		})
	}
}
