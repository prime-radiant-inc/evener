package hub

import (
	"fmt"
	"sort"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

func testNavigationSnapshot(t *testing.T, key navigationResourceKey, revision uint64, children map[string][]string) hubapi.NavigationSnapshot {
	t.Helper()
	metadata := navigationPagedMetadata{GenerationID: "g", Revision: revision, Offset: key.Offset, Limit: key.View().Limit}
	snapshot := hubapi.NavigationSnapshot{Metadata: mustJSON(metadata), Entities: []hubapi.NavigationEntityRecord{}, Containers: []hubapi.NavigationOrderContainer{}}
	seen := map[string]bool{}
	addEntity := func(identity string) string {
		entityKey := navigationEntityKey(key, "session", "local:"+identity)
		if !seen[identity] {
			seen[identity] = true
			snapshot.Entities = append(snapshot.Entities, hubapi.NavigationEntityRecord{Key: entityKey, Kind: "session", Value: mustJSON(navigationSchemaSession("local:"+identity, identity))})
		}
		return entityKey
	}
	top := make([]string, 0, len(children))
	for owner := range children {
		top = append(top, addEntity(owner))
		for _, child := range children[owner] {
			addEntity(child)
		}
	}
	sort.Strings(top)
	snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{Key: navigationRootContainerKey(key, "sessions"), Owner: hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: "sessions"}, Children: top})
	for identity := range seen {
		ownedChildren := make([]string, 0, len(children[identity]))
		for _, child := range children[identity] {
			ownedChildren = append(ownedChildren, navigationEntityKey(key, "session", "local:"+child))
		}
		ownerKey := navigationEntityKey(key, "session", "local:"+identity)
		snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{Key: navigationOwnedContainerKey(ownerKey, "children"), Owner: hubapi.NavigationContainerOwner{Kind: "entity", EntityKey: ownerKey, Slot: "children"}, Children: ownedChildren})
	}
	hubapi.SortNavigationSnapshot(&snapshot)
	return snapshot
}

func TestNavigationDeltaReparentReplacesBothContainers(t *testing.T) {
	key := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	base := testNavigationSnapshot(t, key, 1, map[string][]string{"left": {"s1", "s2"}, "right": {"s3"}})
	current := testNavigationSnapshot(t, key, 2, map[string][]string{"left": {"s2"}, "right": {"s1", "s3"}})
	delta, err := diffNavigationSnapshots(
		key,
		appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"},
		appwire.NavigationReadBase{GenerationID: "g", Revision: 2, ETag: "tag-2"},
		base,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.UpsertedContainers) != 2 {
		t.Fatalf("upserted containers=%d, want 2", len(delta.UpsertedContainers))
	}
}

func TestNavigationDeltaEqualRecordsWithoutCountersProduceNoUpsert(t *testing.T) {
	key := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	base := testNavigationSnapshot(t, key, 1, map[string][]string{"same": {"child"}})
	current := testNavigationSnapshot(t, key, 2, map[string][]string{"same": {"child"}})
	delta, err := diffNavigationSnapshots(
		key,
		appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"},
		appwire.NavigationReadBase{GenerationID: "g", Revision: 2, ETag: "tag-2"},
		base,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.UpsertedEntities) != 0 || len(delta.UpsertedContainers) != 0 {
		t.Fatalf("equal semantic records produced upserts: entities=%+v containers=%+v", delta.UpsertedEntities, delta.UpsertedContainers)
	}
}

func TestNavigationHistoryEvictsOldestGlobally(t *testing.T) {
	history := newNavigationHistory(2, 1<<20)
	view := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	for revision := uint64(1); revision <= 3; revision++ {
		object := hubapi.NavigationSectionResource{
			GenerationID: "g",
			Revision:     revision,
			Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
				navigationSchemaSession(fmt.Sprintf("local:s%d", revision), fmt.Sprintf("s%d", revision)),
			},
		}
		snapshot, err := normalizeNavigationResource(view, object)
		if err != nil {
			t.Fatal(err)
		}
		version := appwire.NavigationReadBase{GenerationID: "g", Revision: revision, ETag: fmt.Sprintf("tag-%d", revision)}
		if err := history.Remember(view, version, &snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := history.Lookup(view, appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"}); ok {
		t.Fatal("oldest version remained retained")
	}
}

func TestNavigationDeltaReconstructsResourcesWithoutEntities(t *testing.T) {
	sectionKey := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	cases := []struct {
		name    string
		key     navigationResourceKey
		base    any
		current any
	}{
		{
			name:    "manifest",
			key:     navigationResourceKey{Kind: navigationResourceManifest},
			base:    hubapi.NavigationManifest{GenerationID: "g", Revision: 1},
			current: hubapi.NavigationManifest{GenerationID: "g", Revision: 2, Sections: hubapi.NavigationSections{Live: hubapi.NavigationResourceDescriptor{Count: 1}}},
		},
		{
			name: "section emptied",
			key:  sectionKey,
			base: hubapi.NavigationSectionResource{GenerationID: "g", Revision: 1, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
				navigationSchemaSession("local:s1", "s1"),
			}},
			current: hubapi.NavigationSectionResource{GenerationID: "g", Revision: 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := normalizeNavigationResource(tc.key, tc.base)
			if err != nil {
				t.Fatal(err)
			}
			current, err := normalizeNavigationResource(tc.key, tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Entities) != 0 {
				t.Fatalf("test setup: current snapshot has %d entities, want an entity-less resource", len(current.Entities))
			}
			baseVersion := appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"}
			currentVersion := appwire.NavigationReadBase{GenerationID: "g", Revision: 2, ETag: "tag-2"}
			history := newNavigationHistory(4, 1<<20)
			if err := history.Remember(tc.key, baseVersion, &base); err != nil {
				t.Fatal(err)
			}
			retained, ok := history.Lookup(tc.key, baseVersion)
			if !ok {
				t.Fatal("base snapshot was not retained")
			}
			delta, err := diffNavigationSnapshots(tc.key, baseVersion, currentVersion, retained, current)
			if err != nil {
				t.Fatalf("diff entity-less current snapshot: %v", err)
			}
			if len(delta.UpsertedEntities) != 0 || len(delta.RemovedEntityKeys) != len(base.Entities) {
				t.Fatalf("delta = %+v, want removals only for %d base entities", delta, len(base.Entities))
			}
		})
	}
}
