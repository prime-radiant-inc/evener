package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/hubapi"
)

func TestNormalizeNavigationSectionCreatesCompleteContainers(t *testing.T) {
	object := hubapi.NavigationSectionResource{GenerationID: "g", Revision: 1, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{{Ref: "local:parent", HostID: "local", SessionID: "parent", Title: "parent", Project: "p", State: "active", Kind: "session", Live: true, RunningJobs: hubapi.NavigationArray[hubapi.NavigationJobSummary]{{JobID: "j1", JobType: "shell", Status: "running"}}, Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}}}
	key := navigationResourceKey{Kind: navigationResourceLive}
	got, err := normalizeNavigationResource(key, object)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate("live"); err != nil {
		t.Fatal(err)
	}
	var found bool
	entityKey := navigationEntityKey(key, "session", "local:parent")
	for _, c := range got.Containers {
		if c.Key == navigationOwnedContainerKey(entityKey, "children") {
			found = true
			if len(c.Children) != 0 {
				t.Fatalf("children=%v", c.Children)
			}
		}
	}
	if !found {
		t.Fatal("missing complete child container")
	}
	for _, e := range got.Entities {
		if e.Key == entityKey && string(e.Value) != "" {
			return
		}
	}
	t.Fatal("missing shallow session entity")
}

func TestNormalizeNavigationResourcePreservesSessionNodeLimit(t *testing.T) {
	sessions := navigationSessionForest(maxNavigationNodes)
	for _, test := range []struct {
		name           string
		key            navigationResourceKey
		object         any
		wantEntities   int
		wantContainers int
	}{
		{
			name: "section",
			key:  navigationResourceKey{Kind: navigationResourceLive, Limit: maxNavigationSectionRows},
			object: hubapi.NavigationSectionResource{
				GenerationID: "g",
				Revision:     1,
				Sessions:     sessions,
			},
			wantEntities:   maxNavigationNodes,
			wantContainers: maxNavigationNodes + 1,
		},
		{
			name: "project",
			key:  navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "project"},
			object: hubapi.NavigationProjectResource{
				GenerationID: "g",
				Revision:     1,
				Key:          "project",
				Current:      hubapi.NavigationTier{Sessions: sessions},
			},
			wantEntities:   maxNavigationNodes + 1,
			wantContainers: maxNavigationNodes + 3,
		},
		{
			name: "project page",
			key:  navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "project", Tier: "current", Limit: maxNavigationSectionRows},
			object: hubapi.NavigationProjectPage{
				GenerationID: "g",
				Revision:     1,
				Key:          "project",
				Tier:         "current",
				Sessions:     sessions,
			},
			wantEntities:   maxNavigationNodes,
			wantContainers: maxNavigationNodes + 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := normalizeNavigationResource(test.key, test.object)
			if err != nil {
				t.Fatalf("normalize maximum-size %s: %v", test.name, err)
			}
			if len(snapshot.Entities) != test.wantEntities || len(snapshot.Containers) != test.wantContainers {
				t.Fatalf("maximum-size %s graph = %d entities/%d containers, want %d/%d", test.name, len(snapshot.Entities), len(snapshot.Containers), test.wantEntities, test.wantContainers)
			}
		})
	}

	overLimit := navigationSessionForest(maxNavigationNodes + 1)
	for _, test := range []struct {
		name   string
		key    navigationResourceKey
		object any
	}{
		{
			name:   "section session count",
			key:    navigationResourceKey{Kind: navigationResourceLive, Limit: maxNavigationSectionRows},
			object: hubapi.NavigationSectionResource{GenerationID: "g", Revision: 1, Sessions: overLimit},
		},
		{
			name: "project aggregate graph",
			key:  navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "project"},
			object: hubapi.NavigationProjectResource{
				GenerationID: "g",
				Revision:     1,
				Key:          "project",
				Current:      hubapi.NavigationTier{Sessions: overLimit},
			},
		},
	} {
		t.Run(test.name+" over limit", func(t *testing.T) {
			if _, err := normalizeNavigationResource(test.key, test.object); err == nil {
				t.Fatalf("normalize accepted %d session nodes, limit is %d", maxNavigationNodes+1, maxNavigationNodes)
			}
		})
	}
}

func navigationSessionForest(count int) hubapi.NavigationArray[hubapi.NavigationSessionSummary] {
	summary := func(index int) hubapi.NavigationSessionSummary {
		id := fmt.Sprintf("session-%04d", index)
		return hubapi.NavigationSessionSummary{
			Ref:       "local:" + id,
			HostID:    "local",
			SessionID: id,
			Title:     id,
			Project:   "project",
			State:     "idle",
			Kind:      "session",
			Children:  hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
		}
	}

	forest := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], 0, (count+maxNavigationChildren)/(maxNavigationChildren+1))
	for next := 0; next < count; {
		root := summary(next)
		next++
		for len(root.Children) < maxNavigationChildren && next < count {
			root.Children = append(root.Children, summary(next))
			next++
		}
		forest = append(forest, root)
	}
	return forest
}

func TestNavigationViewScopeParityVectors(t *testing.T) {
	tests := []struct {
		key  navigationResourceKey
		want string
	}{
		{
			key:  navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "项目/a|b", Tier: "recent", Offset: 2, Limit: 7},
			want: "nav2/project_page///6aG555uuL2F8Yg/cmVjZW50/2/7",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourceLocation, ID: "源/α:β|?"},
			want: "nav2/location/5rqQL86xOs6yfD8////0/0",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourcePinSection, SectionID: "pins/研发|?", Offset: 3, Limit: 11},
			want: "nav2/pin_section//cGlucy_noJTlj5F8Pw///3/11",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourceLive, Offset: 4, Limit: 0},
			want: "nav2/live/////4/50",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "project", Tier: "current", Offset: 5, Limit: maxNavigationSectionRows + 1},
			want: "nav2/project_page///cHJvamVjdA/Y3VycmVudA/5/50",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourcePinCatalog, Offset: 6, Limit: 0},
			want: "nav2/pin_catalog/////6/100",
		},
		{
			key:  navigationResourceKey{Kind: navigationResourceProjects, Offset: 7, Limit: maxNavigationCatalogRows + 1},
			want: "nav2/projects/////7/100",
		},
	}
	for _, test := range tests {
		if got := navigationViewScope(test.key); got != test.want {
			t.Errorf("navigationViewScope(%+v) = %q, want %q", test.key, got, test.want)
		}
	}
}

func TestNormalizeNavigationCatalogRootsIncludeCompleteViewScope(t *testing.T) {
	tests := []struct {
		name   string
		kind   navigationResourceKind
		slot   string
		object any
	}{
		{
			name: "pin catalog",
			kind: navigationResourcePinCatalog,
			slot: "pin_sections",
			object: hubapi.NavigationPinSectionCatalog{GenerationID: "g", Revision: 1, PinSections: hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor]{
				{ID: "pins", Name: "Pins", Count: 1},
			}},
		},
		{
			name: "project catalog",
			kind: navigationResourceProjects,
			slot: "projects",
			object: hubapi.NavigationProjectCatalog{GenerationID: "g", Revision: 1, Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{
				{Key: "project", Name: "Project", SessionCount: 1},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstKey := navigationResourceKey{Kind: test.kind, Offset: 2, Limit: 7}
			secondKey := navigationResourceKey{Kind: test.kind, Offset: 3, Limit: 7}
			first, err := normalizeNavigationResource(firstKey, test.object)
			if err != nil {
				t.Fatal(err)
			}
			second, err := normalizeNavigationResource(secondKey, test.object)
			if err != nil {
				t.Fatal(err)
			}
			firstRoot := navigationRootContainerKey(firstKey, test.slot)
			secondRoot := navigationRootContainerKey(secondKey, test.slot)
			if firstRoot == secondRoot {
				t.Fatal("distinct catalog pages have the same expected root")
			}
			if len(first.Containers) != 1 || first.Containers[0].Key != firstRoot {
				t.Fatalf("first root = %+v, want %q", first.Containers, firstRoot)
			}
			if len(second.Containers) != 1 || second.Containers[0].Key != secondRoot {
				t.Fatalf("second root = %+v, want %q", second.Containers, secondRoot)
			}
			if first.Containers[0].Key == second.Containers[0].Key {
				t.Fatal("catalog roots collide across offset views")
			}
		})
	}
}

func TestNormalizeNavigationRecordsAreStateless(t *testing.T) {
	object := hubapi.NavigationSectionResource{
		GenerationID: "g",
		Revision:     1,
		Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
			{Ref: "local:stateless", HostID: "local", SessionID: "stateless", Title: "stateless", Project: "p", State: "idle", Kind: "session", Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}},
		},
	}
	snapshot, err := normalizeNavigationResource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50}, object)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range snapshot.Entities {
		wire, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), `"revision"`) {
			t.Fatalf("entity record emitted resource revision: %s", wire)
		}
	}
	for _, record := range snapshot.Containers {
		wire, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), `"revision"`) {
			t.Fatalf("container record emitted resource revision: %s", wire)
		}
	}
}

func TestNormalizeNavigationKeysIncludeCompleteViewScope(t *testing.T) {
	ref := strings.Repeat("r", maxNavigationIdentityBytes)
	sectionID := strings.Repeat("s", maxNavigationIdentityBytes)
	object := hubapi.NavigationSectionResource{
		GenerationID: "generation",
		Revision:     7,
		Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{{
			Ref:       ref,
			HostID:    "local",
			SessionID: "session",
			Title:     "session",
			Project:   "project",
			State:     "idle",
			Kind:      "session",
			Children:  hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
		}},
	}
	firstKey := navigationResourceKey{Kind: navigationResourcePinSection, SectionID: sectionID, Offset: 2, Limit: 7, Generation: "generation-a", Revision: 1}
	secondKey := firstKey
	secondKey.Offset++

	first, err := normalizeNavigationResource(firstKey, object)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeNavigationResource(secondKey, object)
	if err != nil {
		t.Fatal(err)
	}
	firstScope, secondScope := navigationViewScope(firstKey), navigationViewScope(secondKey)
	if firstScope == secondScope {
		t.Fatalf("selector scopes collide: %q", firstScope)
	}
	for name, snapshot := range map[string]hubapi.NavigationSnapshot{"first": first, "second": second} {
		scope := map[string]string{"first": firstScope, "second": secondScope}[name]
		for _, entity := range snapshot.Entities {
			if !strings.HasPrefix(entity.Key, scope+"/") {
				t.Errorf("%s entity key %q does not start with complete view scope", name, entity.Key)
			}
			if len(entity.Key) > 2_048 {
				t.Errorf("%s entity key has %d bytes, max 2048", name, len(entity.Key))
			}
		}
		for _, container := range snapshot.Containers {
			if !strings.HasPrefix(container.Key, scope+"/") {
				t.Errorf("%s container key %q does not start with complete view scope", name, container.Key)
			}
			if len(container.Key) > 2_048 {
				t.Errorf("%s container key has %d bytes, max 2048", name, len(container.Key))
			}
		}
	}
	if first.Entities[0].Key == second.Entities[0].Key || first.Containers[0].Key == second.Containers[0].Key {
		t.Fatal("entity or container keys collide across selector views")
	}
}
