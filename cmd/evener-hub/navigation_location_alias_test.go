package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
)

const (
	aliasRootID     = "01ROOT00000000000000000000"
	aliasOtherID    = "01OTHER0000000000000000000"
	aliasForkOrigID = "01FORKORIG0000000000000000"
	aliasContID     = "01CONTINUATION000000000000"
	aliasSubID      = "01SUBAGENT0000000000000000"
	aliasNestedID   = "01NESTEDSUB00000000000000"
)

// aliasParents is the test stand-in for the hub's meta lookup: child id to the
// id of the row it hangs under (a subagent's parent, or a fork original's
// continuation).
type aliasParents map[string]string

func (p aliasParents) parent(ref string) (string, string, bool) {
	ref = hubRefFromTreeNodeID(ref).SessionID
	next, ok := p[ref]
	kind := "subagent"
	if ref == aliasForkOrigID {
		kind = "fork"
	}
	return next, kind, ok
}

func aliasNode(id, title string, now time.Time, children ...hubcore.TreeNode) hubcore.TreeNode {
	return hubcore.TreeNode{ID: id, Title: title, Project: "p1", Kind: "session", State: "idle", UpdatedAt: now.Add(-time.Hour), Children: children}
}

func aliasSetRows(source *testNavigationSource, rows ...hubcore.TreeNode) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.inputs.Tree.Projects[0].Current = rows
	source.revision++
}

func aliasService(t *testing.T, parents aliasParents, rows ...hubcore.TreeNode) (*NavigationService, *testNavigationSource) {
	t.Helper()
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	aliasSetRows(source, rows...)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.SubagentParent = parents.parent
	})
	return service, source
}

func aliasRefresh(t *testing.T, service *NavigationService) hubapi.NavigationMutation {
	t.Helper()
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	return mutation
}

func aliasRead(t *testing.T, service *NavigationService, id string, base *appwire.NavigationReadBase) appwire.NavigationReadResponse {
	t.Helper()
	key := navigationResourceKey{Kind: navigationResourceLocation, ID: "local:" + id}
	result, err := service.readV2(t.Context(), key, base)
	if err != nil {
		t.Fatalf("read location %s: %v", id, err)
	}
	return result.Response
}

func aliasBase(response appwire.NavigationReadResponse) *appwire.NavigationReadBase {
	return &appwire.NavigationReadBase{GenerationID: response.GenerationID, Revision: response.Revision, ETag: response.ETag}
}

func aliasLocation(t *testing.T, service *NavigationService, id string) hubapi.NavigationSessionLocation {
	t.Helper()
	key := navigationResourceKey{Kind: navigationResourceLocation, ID: "local:" + id}
	_, versioned, projection, err := service.versionedCore(t.Context(), key)
	if err != nil {
		t.Fatalf("versionedCore %s: %v", id, err)
	}
	object, _, err := projection.Resource(versioned)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := object.(hubapi.NavigationSessionLocation)
	if !ok {
		t.Fatalf("resource is %T, want a location", object)
	}
	return location
}

func TestNavigationLocationAliasResolvesUnindexedSubagentToItsRoot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service, _ := aliasService(t, aliasParents{aliasSubID: aliasRootID}, aliasNode(aliasRootID, "root", now))

	location := aliasLocation(t, service, aliasSubID)
	root := aliasLocation(t, service, aliasRootID)
	if location.Ref != "local:"+aliasSubID || location.TopLevel || location.TopLevelRef != root.Ref {
		t.Fatalf("alias identity = %+v, want the child routed to root %s", location, root.Ref)
	}
	if location.ProjectKey != root.ProjectKey || location.Tier != root.Tier || location.PinSectionID != "" {
		t.Fatalf("alias placement = %+v, want root's project %q and tier %q with no pin section", location, root.ProjectKey, root.Tier)
	}
	if location.Session == nil || location.Session.Ref != location.Ref || location.Session.SessionID != aliasSubID || location.Session.Kind != "subagent" || location.Session.Live {
		t.Fatalf("alias session = %+v, want a subagent identity for the child", location.Session)
	}
	if response := aliasRead(t, service, aliasSubID, nil); response.Status != "ok" || response.Revision == 0 {
		t.Fatalf("alias read = %+v, want ok with a revision", response)
	}
}

// A subagent of a fork-superseded original routes to the continuation row that
// carries the original as a child, and copies that row's own top_level_ref.
func TestNavigationLocationAliasFollowsForkContinuationToTopLevelRow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	original := aliasNode(aliasForkOrigID, "original", now)
	original.Kind = "fork"
	continuation := aliasNode(aliasContID, "continuation", now, original)

	t.Run("original indexed as a nested row", func(t *testing.T) {
		service, _ := aliasService(t, aliasParents{aliasSubID: aliasForkOrigID}, continuation)
		location := aliasLocation(t, service, aliasSubID)
		if location.TopLevelRef != "local:"+aliasContID || location.TopLevel {
			t.Fatalf("alias = %+v, want the continuation as top-level row", location)
		}
	})
	t.Run("original not indexed", func(t *testing.T) {
		service, _ := aliasService(t, aliasParents{aliasSubID: aliasForkOrigID, aliasForkOrigID: aliasContID}, aliasNode(aliasContID, "continuation", now))
		location := aliasLocation(t, service, aliasSubID)
		if location.TopLevelRef != "local:"+aliasContID || location.TopLevel {
			t.Fatalf("alias = %+v, want the continuation as top-level row", location)
		}
	})
	t.Run("nested subagent chain", func(t *testing.T) {
		service, _ := aliasService(t, aliasParents{aliasNestedID: aliasSubID, aliasSubID: aliasRootID}, aliasNode(aliasRootID, "root", now))
		if location := aliasLocation(t, service, aliasNestedID); location.TopLevelRef != "local:"+aliasRootID {
			t.Fatalf("alias = %+v, want the root", location)
		}
	})
}

// Unknown, orphaned and unresolvable refs answer a real gone response, not a
// retryable "navigation unavailable" error.
func TestNavigationLocationAliasUnresolvableRefsAreGone(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := map[string]aliasParents{
		"unknown ref":              {},
		"orphan, parent unknown":   {aliasSubID: aliasOtherID},
		"parent cycle":             {aliasSubID: aliasNestedID, aliasNestedID: aliasSubID},
		"chain ends below no root": {aliasSubID: aliasNestedID, aliasNestedID: aliasOtherID},
	}
	for name, parents := range cases {
		t.Run(name, func(t *testing.T) {
			service, _ := aliasService(t, parents, aliasNode(aliasRootID, "root", now))
			gone := aliasRead(t, service, aliasSubID, nil)
			if gone.Status != "gone" || gone.Data != nil || gone.GenerationID == "" || gone.ETag == "" {
				t.Fatalf("response = %+v, want gone with generation and etag", gone)
			}
			if again := aliasRead(t, service, aliasSubID, aliasBase(gone)); again.Status != "not_modified" {
				t.Fatalf("gone re-read = %+v, want not_modified against the gone base", again)
			}
		})
	}
}

func TestNavigationLocationAliasRevisionStaysMonotonicAcrossIndexedPath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	parents := aliasParents{aliasSubID: aliasRootID}
	withChild := aliasNode(aliasRootID, "root", now, aliasNode(aliasSubID, "child", now))
	service, source := aliasService(t, parents, withChild)

	indexed := aliasRead(t, service, aliasSubID, nil)
	if indexed.Status != "ok" {
		t.Fatalf("indexed read = %+v", indexed)
	}
	// Give the root's location a few revisions so a naive alias revision (the
	// root's own) is far from the child's indexed one.
	previous := indexed.Revision
	for _, title := range []string{"one", "two", "three"} {
		aliasSetRows(source, aliasNode(aliasRootID, title, now, aliasNode(aliasSubID, "child "+title, now)))
		aliasRefresh(t, service)
		current := aliasRead(t, service, aliasSubID, nil)
		if current.Revision <= previous {
			t.Fatalf("indexed revision %d after %d", current.Revision, previous)
		}
		previous = current.Revision
	}

	// The child row is dropped: the key moves from the indexed path to the alias.
	aliasSetRows(source, aliasNode(aliasRootID, "dropped", now))
	aliasRefresh(t, service)
	alias := aliasRead(t, service, aliasSubID, nil)
	if alias.Status != "ok" || alias.Revision <= previous {
		t.Fatalf("alias revision %d after indexed %d, want it to move forward", alias.Revision, previous)
	}
	if base := aliasRead(t, service, aliasSubID, aliasBase(alias)); base.Status != "not_modified" {
		t.Fatalf("alias re-read = %+v, want not_modified", base)
	}

	// The root's location moves: the alias moves with it.
	aliasSetRows(source, aliasNode(aliasRootID, "renamed", now))
	aliasRefresh(t, service)
	moved := aliasRead(t, service, aliasSubID, aliasBase(alias))
	if moved.Status == "not_modified" || moved.Revision <= alias.Revision {
		t.Fatalf("after root change = %+v, want a later revision than %d", moved, alias.Revision)
	}

	// The child row returns: the indexed path continues past the alias revision.
	aliasSetRows(source, aliasNode(aliasRootID, "renamed", now, aliasNode(aliasSubID, "child", now)))
	aliasRefresh(t, service)
	back := aliasRead(t, service, aliasSubID, aliasBase(moved))
	if back.Status != "ok" || back.Revision <= moved.Revision {
		t.Fatalf("indexed again = %+v, want a revision past alias %d", back, moved.Revision)
	}
}

// A child first served by alias and indexed afterwards must not restart low.
func TestNavigationLocationAliasToFreshIndexedKeyIsMonotonic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service, source := aliasService(t, aliasParents{aliasSubID: aliasRootID}, aliasNode(aliasRootID, "root", now))
	for _, title := range []string{"one", "two", "three"} {
		aliasSetRows(source, aliasNode(aliasRootID, title, now))
		aliasRefresh(t, service)
	}
	alias := aliasRead(t, service, aliasSubID, nil)
	aliasSetRows(source, aliasNode(aliasRootID, "three", now, aliasNode(aliasSubID, "child", now)))
	aliasRefresh(t, service)
	indexed := aliasRead(t, service, aliasSubID, aliasBase(alias))
	if indexed.Status != "ok" || indexed.Revision <= alias.Revision {
		t.Fatalf("indexed = %+v, want a revision past alias %d", indexed, alias.Revision)
	}
}

// An open child location must learn when its root's location changes: the root
// change invalidates the target the child's alias shares, and both keys move in
// the same commit.
func TestNavigationLocationAliasMovesWithRootInTheSameInvalidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service, source := aliasService(t, aliasParents{aliasSubID: aliasRootID}, aliasNode(aliasRootID, "root", now), aliasNode(aliasOtherID, "other", now))
	before := aliasRead(t, service, aliasSubID, nil)

	// A change to an unrelated root's title leaves this root's location alone.
	aliasSetRows(source, aliasNode(aliasRootID, "root", now), aliasNode(aliasOtherID, "other renamed", now))
	aliasRefresh(t, service)
	if same := aliasRead(t, service, aliasSubID, aliasBase(before)); same.Status != "not_modified" {
		t.Fatalf("unrelated change = %+v, want not_modified", same)
	}

	aliasSetRows(source, aliasNode(aliasRootID, "root renamed", now), aliasNode(aliasOtherID, "other renamed", now))
	mutation := aliasRefresh(t, service)
	found := false
	for _, target := range mutation.Targets {
		if target.Kind == appwire.NavigationTargetProject && target.ProjectKey == "p1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("root change targets = %+v, want the root's project", mutation.Targets)
	}
	after := aliasRead(t, service, aliasSubID, aliasBase(before))
	root := aliasRead(t, service, aliasRootID, nil)
	if after.Status == "not_modified" || after.Revision <= before.Revision || root.Revision == 0 {
		t.Fatalf("after root change alias = %+v, want a later revision", after)
	}
}

func TestWebNavigationSubagentParentHops(t *testing.T) {
	root, sub, orig, cont, lone := identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID()
	live, dead, inproc, stale, unknown := identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID()
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: root, ProfileID: "local"},
		{ID: sub, ProfileID: "local", IsSubagent: true, ParentSessionID: root},
		{ID: orig, ProfileID: "local", ForkLabel: "before edit"},
		{ID: cont, ProfileID: "local", ParentSessionID: orig},
		{ID: lone, ProfileID: "local", ForkLabel: "no continuation"},
	})
	parent := liveActivityEntry(1, live, appwire.ThreadStatusActive, nil)
	parent.RunningSubagentIDs = []string{inproc}
	crashed := liveActivityEntry(2, dead, "errored", nil)
	crashed.Crashed = true
	crashed.RunningSubagentIDs = []string{stale}
	web := &WebServer{cfg: hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries(parent, crashed)}}

	cases := []struct {
		ref, want, kind string
		ok              bool
	}{
		{"local:" + sub, root, "subagent", true},
		{"local:" + orig, cont, "fork", true},
		{"local:" + inproc, live, "subagent", true},
		{"local:" + root, "", "", false},
		{"local:" + cont, "", "", false},
		{"local:" + lone, "", "", false},
		{"local:" + stale, "", "", false},
		{"local:" + unknown, "", "", false},
		{"remote:" + sub, "", "", false},
	}
	for _, tc := range cases {
		got, kind, ok := web.navigationSubagentParent(tc.ref)
		if got != tc.want || ok != tc.ok || (ok && kind != tc.kind) {
			t.Errorf("navigationSubagentParent(%q) = %q, %q, %v; want %q, %q, %v", tc.ref, got, kind, ok, tc.want, tc.kind, tc.ok)
		}
	}
}

// An alias that later stops resolving answers gone at a revision the client
// accepts (never below the alias it replaces), and stays put while gone.
func TestNavigationLocationAliasToGoneIsMonotonic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service, source := aliasService(t, aliasParents{aliasSubID: aliasRootID}, aliasNode(aliasRootID, "root", now))
	for _, title := range []string{"one", "two", "three"} {
		aliasSetRows(source, aliasNode(aliasRootID, title, now))
		aliasRefresh(t, service)
	}
	alias := aliasRead(t, service, aliasSubID, nil)
	aliasSetRows(source, aliasNode(aliasOtherID, "other", now))
	aliasRefresh(t, service)
	gone := aliasRead(t, service, aliasSubID, aliasBase(alias))
	if gone.Status != "gone" || gone.Revision <= alias.Revision {
		t.Fatalf("gone = %+v, want a revision past alias %d", gone, alias.Revision)
	}
	if again := aliasRead(t, service, aliasSubID, aliasBase(gone)); again.Status != "not_modified" {
		t.Fatalf("gone re-read = %+v, want not_modified", again)
	}
	aliasSetRows(source, aliasNode(aliasRootID, "back", now))
	aliasRefresh(t, service)
	back := aliasRead(t, service, aliasSubID, aliasBase(gone))
	if back.Status != "ok" || back.Revision <= gone.Revision {
		t.Fatalf("resolved again = %+v, want a revision past gone %d", back, gone.Revision)
	}
}

func TestNavigationLocationAliasKeepsForkKind(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service, _ := aliasService(t, aliasParents{aliasForkOrigID: aliasContID}, aliasNode(aliasContID, "continuation", now))
	location := aliasLocation(t, service, aliasForkOrigID)
	if location.Session == nil || location.Session.Kind != "fork" || location.TopLevelRef != "local:"+aliasContID {
		t.Fatalf("alias = %+v, want a fork routed to the continuation", location)
	}
}

// Forgetting alias history at the memory bound must not let a later answer
// fall below what was served before.
func TestNavigationLocationAliasRevisionSurvivesMemoryOverflow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	parents := aliasParents{aliasSubID: aliasRootID}
	others := make([]string, maxNavigationAliasServed)
	for index := range others {
		others[index] = fmt.Sprintf("01OVERFLOW%016d", index)
		parents[others[index]] = aliasRootID
	}
	service, source := aliasService(t, parents, aliasNode(aliasRootID, "root", now))
	for _, title := range []string{"one", "two", "three"} {
		aliasSetRows(source, aliasNode(aliasRootID, title, now))
		aliasRefresh(t, service)
	}
	alias := aliasRead(t, service, aliasSubID, nil)
	for _, id := range others {
		aliasRead(t, service, id, nil)
	}
	aliasSetRows(source, aliasNode(aliasOtherID, "other", now))
	aliasRefresh(t, service)
	gone := aliasRead(t, service, aliasSubID, aliasBase(alias))
	if gone.Status != "gone" || gone.Revision <= alias.Revision {
		t.Fatalf("gone after overflow = %+v, want a revision past alias %d", gone, alias.Revision)
	}
}
