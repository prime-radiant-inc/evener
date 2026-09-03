package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

func TestNavigationHistoryExactTupleMissesWrongETag(t *testing.T) {
	history := newNavigationHistory(4, 1<<20)
	view := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	snapshot, err := normalizeNavigationResource(view, hubapi.NavigationSectionResource{GenerationID: "g", Revision: 1, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{navigationSchemaSession("local:s1", "s1")}})
	if err != nil {
		t.Fatal(err)
	}
	base := appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"}
	if err := history.Remember(view, base, &snapshot, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := history.Lookup(view, appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "wrong"}); ok {
		t.Fatal("wrong etag matched")
	}
	if _, ok := history.Lookup(view, base); !ok {
		t.Fatal("exact tuple missed")
	}
}

func TestNavigationHistoryKeysByCompleteViewWithoutVersion(t *testing.T) {
	history := newNavigationHistory(4, 1<<20)
	versioned := navigationResourceKey{
		Kind:       navigationResourceProjectPage,
		ProjectKey: "project",
		Tier:       "current",
		Offset:     2,
		Limit:      2,
		Generation: "generation-a",
		Revision:   9,
	}
	snapshot, err := normalizeNavigationResource(versioned, hubapi.NavigationProjectPage{GenerationID: "generation-a", Revision: 9, Key: "project", Tier: "current", Offset: 2, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{navigationSchemaSession("local:s1", "s1")}})
	if err != nil {
		t.Fatal(err)
	}
	base := appwire.NavigationReadBase{GenerationID: "generation-a", Revision: 9, ETag: "tag-9"}
	if err := history.Remember(versioned, base, &snapshot, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := history.Lookup(versioned.View(), base); !ok {
		t.Fatal("version fields changed complete-view history identity")
	}
	otherPage := versioned.View()
	otherPage.Offset++
	if _, ok := history.Lookup(otherPage, base); ok {
		t.Fatal("different page selector matched retained complete view")
	}
}
