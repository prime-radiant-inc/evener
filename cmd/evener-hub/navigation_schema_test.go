package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/hubapi"
)

const navigationSchemaGeneration = "schema-generation"
const navigationSchemaRevision uint64 = 7

func TestValidateNavigationResourceSnapshotRejectsWrongSchema(t *testing.T) {
	fixtures := navigationSchemaFixtures(t)
	for name, fixture := range fixtures {
		t.Run("valid_"+name, func(t *testing.T) {
			if err := validateNavigationResourceSnapshot(fixture.key, navigationSchemaGeneration, navigationSchemaRevision, fixture.snapshot); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
	}

	tests := []struct {
		name     string
		resource string
		category string
		mutate   func(*hubapi.NavigationSnapshot, navigationResourceKey)
	}{
		{
			name: "metadata generation or revision mismatch", resource: "live", category: "metadata",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Metadata = replaceNavigationSchemaJSONField(t, snapshot.Metadata, "generation_id", "private-generation")
			},
		},
		{
			name: "paged metadata selector mismatch", resource: "live", category: "metadata",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Metadata = replaceNavigationSchemaJSONField(t, snapshot.Metadata, "offset", float64(99))
			},
		},
		{
			name: "wrong or unknown entity kind", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Entities[0].Kind = "unknown"
			},
		},
		{
			name: "missing required value field", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Entities[0].Value = deleteNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "host_id")
			},
		},
		{
			name: "unknown value field", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "unknown", "private-body-value")
			},
		},
		{
			name: "non-empty session value children", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "children", []any{map[string]any{"ref": "private-child"}})
			},
		},
		{
			name: "wrong root slot", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				snapshot.Containers[0].Owner.Slot = "projects"
				snapshot.Containers[0].Key = navigationRootContainerKey(key, "projects")
			},
		},
		{
			name: "extra root", resource: "manifest", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{Key: navigationRootContainerKey(key, "sessions"), Owner: hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: "sessions"}, Children: []string{}})
			},
		},
		{
			name: "entity-owned slot not children/current/recent/archived as applicable", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				for i := range snapshot.Containers {
					if snapshot.Containers[i].Owner.Kind == "entity" {
						snapshot.Containers[i].Owner.Slot = "recent"
						snapshot.Containers[i].Key = navigationOwnedContainerKey(snapshot.Containers[i].Owner.EntityKey, "recent")
						return
					}
				}
			},
		},
		{
			name: "owner missing", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				missing := navigationEntityKey(key, "session", "private-missing-owner")
				for i := range snapshot.Containers {
					if snapshot.Containers[i].Owner.Kind == "entity" {
						snapshot.Containers[i].Owner.EntityKey = missing
						snapshot.Containers[i].Key = navigationOwnedContainerKey(missing, "children")
						return
					}
				}
			},
		},
		{
			name: "orphan represented entity", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				for i := range snapshot.Containers {
					if snapshot.Containers[i].Owner.Kind == "resource_root" {
						snapshot.Containers[i].Children = []string{}
						return
					}
				}
			},
		},
		{
			name: "duplicate logical identity", resource: "live", category: "identity",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				duplicate := snapshot.Entities[0]
				duplicate.Key = navigationEntityKey(key, "session", "private-second-key")
				snapshot.Entities = append(snapshot.Entities, duplicate)
				for i := range snapshot.Containers {
					if snapshot.Containers[i].Owner.Kind == "resource_root" {
						snapshot.Containers[i].Children = append(snapshot.Containers[i].Children, duplicate.Key)
					}
				}
				snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{Key: navigationOwnedContainerKey(duplicate.Key, "children"), Owner: hubapi.NavigationContainerOwner{Kind: "entity", EntityKey: duplicate.Key, Slot: "children"}, Children: []string{}})
			},
		},
		{
			name: "wrong view scope", resource: "live", category: "scope",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				oldKey := snapshot.Entities[0].Key
				other := key
				other.Offset++
				newKey := navigationEntityKey(other, "session", "local:schema-session")
				snapshot.Entities[0].Key = newKey
				for i := range snapshot.Containers {
					for child := range snapshot.Containers[i].Children {
						if snapshot.Containers[i].Children[child] == oldKey {
							snapshot.Containers[i].Children[child] = newKey
						}
					}
					if snapshot.Containers[i].Owner.EntityKey == oldKey {
						snapshot.Containers[i].Owner.EntityKey = newKey
						snapshot.Containers[i].Key = navigationOwnedContainerKey(newKey, snapshot.Containers[i].Owner.Slot)
					}
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := fixtures[test.resource]
			snapshot := cloneNavigationSnapshot(fixture.snapshot)
			test.mutate(&snapshot, fixture.key)
			err := validateNavigationResourceSnapshot(fixture.key, navigationSchemaGeneration, navigationSchemaRevision, snapshot)
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if !strings.Contains(err.Error(), "navigation schema: "+test.category) {
				t.Fatalf("error category = %q, want %q", err, test.category)
			}
			for _, private := range []string{"private-generation", "private-body-value", "private-child", "private-missing-owner", "private-second-key"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("validation error contains private content: %q", err)
				}
			}
		})
	}
}

func TestValidateNavigationResourceSnapshotRejectsNullAndWrongWireTypes(t *testing.T) {
	fixtures := navigationSchemaFixtures(t)
	tests := []struct {
		name     string
		resource string
		category string
		mutate   func(*hubapi.NavigationSnapshot)
	}{
		{
			name: "required metadata null", resource: "live", category: "metadata",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Metadata = replaceNavigationSchemaJSONField(t, snapshot.Metadata, "truncated", nil)
			},
		},
		{
			name: "nested metadata null", resource: "manifest", category: "metadata",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Metadata = replaceNavigationSchemaJSONField(t, snapshot.Metadata, "sources", []any{
					map[string]any{"id": "source", "label": "Source", "kind": "local", "online": nil},
				})
			},
		},
		{
			name: "required entity null", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "live", nil)
			},
		},
		{
			name: "optional entity null", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "running_jobs", nil)
			},
		},
		{
			name: "nested entity null", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "running_jobs", []any{
					map[string]any{"job_id": "job", "job_type": "shell", "status": nil},
				})
			},
		},
		{
			name: "required metadata wrong type", resource: "live", category: "metadata",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Metadata = replaceNavigationSchemaJSONField(t, snapshot.Metadata, "truncated", "false")
			},
		},
		{
			name: "required entity wrong type", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "live", "false")
			},
		},
		{
			name: "optional entity wrong type", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "running_jobs", map[string]any{})
			},
		},
		{
			name: "nested entity wrong type", resource: "live", category: "entity",
			mutate: func(snapshot *hubapi.NavigationSnapshot) {
				snapshot.Entities[0].Value = replaceNavigationSchemaJSONField(t, snapshot.Entities[0].Value, "running_jobs", []any{
					map[string]any{"job_id": "job", "job_type": "shell", "status": 1},
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := fixtures[test.resource]
			snapshot := cloneNavigationSnapshot(fixture.snapshot)
			test.mutate(&snapshot)
			err := validateNavigationResourceSnapshot(fixture.key, navigationSchemaGeneration, navigationSchemaRevision, snapshot)
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if got, want := err.Error(), "navigation schema: "+test.category; got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestValidateNavigationResourceSnapshotRejectsChildrenOnLists(t *testing.T) {
	fixtures := navigationSchemaFixtures(t)
	for _, resource := range []string{"live", "needs_you", "pin_section", "project", "project_page", "location"} {
		t.Run(resource, func(t *testing.T) {
			fixture := fixtures[resource]
			// Depth 2 puts the second entity under a children slot: a
			// non-empty children link, which lists must reject.
			snapshot := navigationSchemaChainSnapshot(t, fixture, 2)
			err := validateNavigationResourceSnapshot(fixture.key, navigationSchemaGeneration, navigationSchemaRevision, snapshot)
			if err == nil {
				t.Fatalf("a children slot on %s was accepted", resource)
			} else if err.Error() != "navigation schema: graph" {
				t.Fatalf("error = %q, want graph category", err)
			}
		})
	}

}

func TestNavigationTimestampParityFixturesMatchGoTime(t *testing.T) {
	raw, err := os.ReadFile("testdata/navigation/timestamps.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Value string `json:"value"`
		Valid bool   `json:"valid"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Value, func(t *testing.T) {
			encoded, err := json.Marshal(fixture.Value)
			if err != nil {
				t.Fatal(err)
			}
			var value time.Time
			err = json.Unmarshal(encoded, &value)
			// The codec (and validNavigationTimestamp) deliberately reject an
			// offset whose hour or minute exceeds 23:59, while Go's time.Time
			// accepts it. Those fixtures carry valid:false to document the
			// codec's stricter grammar, so they are the one place where the
			// fixture and time.Time are expected to disagree.
			want := fixture.Valid || fixtureZoneExceedsCodecBound(fixture.Value)
			if got := err == nil; got != want {
				t.Fatalf("time.Time accepted = %v, want %v (error %v)", got, want, err)
			}
		})
	}
}

// fixtureZoneExceedsCodecBound reports whether value's +/-HH:MM offset is
// outside the range the web codec accepts (hour <= 23 and minute <= 59).
func fixtureZoneExceedsCodecBound(value string) bool {
	if len(value) < 6 {
		return false
	}
	zone := value[len(value)-6:]
	if zone[0] != '+' && zone[0] != '-' {
		return false
	}
	hour := int(zone[1]-'0')*10 + int(zone[2]-'0')
	minute := int(zone[4]-'0')*10 + int(zone[5]-'0')
	return hour > 23 || minute > 59
}

func TestNavigationSessionValueValidatesApprovalDetail(t *testing.T) {
	withApproval := func(tool, target string) hubapi.NavigationSessionSummary {
		session := navigationSchemaSession("local:schema-session", "schema-session")
		session.ApprovalPending = true
		session.ApprovalTool = tool
		session.ApprovalTarget = target
		return session
	}
	for name, session := range map[string]hubapi.NavigationSessionSummary{
		"a tool and its path":          withApproval("write_file", "/home/me/sites/docs/index.md"),
		"a target at the rune bound":   withApproval("write_file", strings.Repeat("😀", maxNavigationLabelRunes)),
		"a tool at the identity bound": withApproval(strings.Repeat("t", maxNavigationIdentityBytes), "/srv/docs"),
		"no approval detail":           withApproval("", ""),
	} {
		if !navigationSessionValueValid(session) {
			t.Errorf("%s rejected: tool %d bytes, target %d runes", name, len(session.ApprovalTool), utf8.RuneCountInString(session.ApprovalTarget))
		}
	}
	for name, session := range map[string]hubapi.NavigationSessionSummary{
		"an over-long target":      withApproval("write_file", strings.Repeat("t", maxNavigationLabelRunes+1)),
		"an over-long tool":        withApproval(strings.Repeat("t", maxNavigationIdentityBytes+1), "/srv/docs"),
		"a tool that is not UTF-8": withApproval("write\xff", "/srv/docs"),
	} {
		if navigationSessionValueValid(session) {
			t.Errorf("%s accepted: tool %d bytes, target %d runes", name, len(session.ApprovalTool), utf8.RuneCountInString(session.ApprovalTarget))
		}
	}
}

// Task progress is counts and a label on the wire, bounded as the codec's
// tasksValue bounds them: safe non-negative counts, no more tasks done and
// cancelled than exist, and a current task within the label bound.
func TestNavigationSessionValueValidatesTaskProgress(t *testing.T) {
	withTasks := func(tasks hubapi.NavigationTaskProgress) hubapi.NavigationSessionSummary {
		session := navigationSchemaSession("local:schema-session", "schema-session")
		session.Tasks = &tasks
		return session
	}
	for name, tasks := range map[string]hubapi.NavigationTaskProgress{
		"a list in progress":          {Total: 7, Done: 2, Cancelled: 1, CurrentID: 4, Current: "Fix the settle/drain race"},
		"a finished list":             {Total: 7, Done: 6, Cancelled: 1},
		"a current task at the bound": {Total: 1, CurrentID: 1, Current: strings.Repeat("😀", maxNavigationLabelRunes)},
		"counts at the safe bound":    {Total: int(maxNavigationSafeInteger), Done: int(maxNavigationSafeInteger), CurrentID: int(maxNavigationSafeInteger)},
	} {
		if !navigationSessionValueValid(withTasks(tasks)) {
			t.Errorf("%s rejected: %+v", name, tasks)
		}
	}
	for name, tasks := range map[string]hubapi.NavigationTaskProgress{
		"a negative total":              {Total: -1},
		"a negative done count":         {Total: 1, Done: -1},
		"a negative cancelled count":    {Total: 1, Cancelled: -1},
		"a negative current id":         {Total: 1, CurrentID: -1},
		"more done than exist":          {Total: 2, Done: 3},
		"more settled than exist":       {Total: 2, Done: 1, Cancelled: 2},
		"a total beyond the safe range": {Total: int(maxNavigationSafeInteger) + 1},
		"an over-long current task":     {Total: 1, CurrentID: 1, Current: strings.Repeat("t", maxNavigationLabelRunes+1)},
	} {
		if navigationSessionValueValid(withTasks(tasks)) {
			t.Errorf("%s accepted: %+v", name, tasks)
		}
	}
}

func navigationSchemaChainSnapshot(t *testing.T, fixture navigationSchemaFixture, depth int) hubapi.NavigationSnapshot {
	t.Helper()
	snapshot := cloneNavigationSnapshot(fixture.snapshot)
	snapshot.Entities = nil
	snapshot.Containers = nil
	parentContainer := -1
	if fixture.key.Kind == navigationResourceProject {
		for _, entity := range fixture.snapshot.Entities {
			if entity.Kind == "project" {
				snapshot.Entities = append(snapshot.Entities, entity)
				break
			}
		}
		for _, container := range fixture.snapshot.Containers {
			if container.Owner.Kind == "entity" && len(snapshot.Entities) == 1 && container.Owner.EntityKey == snapshot.Entities[0].Key {
				container.Children = nil
				snapshot.Containers = append(snapshot.Containers, container)
				if container.Owner.Slot == "current" {
					parentContainer = len(snapshot.Containers) - 1
				}
			}
		}
	} else {
		for _, container := range fixture.snapshot.Containers {
			if container.Owner.Kind == "resource_root" {
				container.Children = nil
				snapshot.Containers = append(snapshot.Containers, container)
				parentContainer = 0
				break
			}
		}
	}
	if parentContainer < 0 {
		t.Fatal("missing chain root container")
	}
	for index := range depth {
		ref := fmt.Sprintf("local:depth-%d", index)
		key := navigationEntityKey(fixture.key, "session", ref)
		value, err := json.Marshal(navigationSchemaSession(ref, fmt.Sprintf("depth-%d", index)))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Entities = append(snapshot.Entities, hubapi.NavigationEntityRecord{Key: key, Kind: "session", Value: value})
		snapshot.Containers[parentContainer].Children = []string{key}
		snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{
			Key:      navigationOwnedContainerKey(key, "children"),
			Owner:    hubapi.NavigationContainerOwner{Kind: "entity", EntityKey: key, Slot: "children"},
			Children: []string{},
		})
		parentContainer = len(snapshot.Containers) - 1
	}
	return snapshot
}

type navigationSchemaFixture struct {
	key      navigationResourceKey
	snapshot hubapi.NavigationSnapshot
}

func navigationSchemaFixtures(t *testing.T) map[string]navigationSchemaFixture {
	t.Helper()
	session := navigationSchemaSession("local:schema-session", "schema-session")
	section := func() hubapi.NavigationSectionResource {
		return hubapi.NavigationSectionResource{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{session}}
	}
	fixtures := map[string]struct {
		key    navigationResourceKey
		object any
	}{
		"manifest":          {navigationResourceKey{Kind: navigationResourceManifest}, hubapi.NavigationManifest{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Sources: hubapi.NavigationArray[hubapi.Source]{}, Sections: hubapi.NavigationSections{}, Catalogs: hubapi.NavigationCatalogs{}}},
		"live":              {navigationResourceKey{Kind: navigationResourceLive, Offset: 0, Limit: 50}, section()},
		"needs_you":         {navigationResourceKey{Kind: navigationResourceNeedsYou, Offset: 0, Limit: 50}, section()},
		"pin_section":       {navigationResourceKey{Kind: navigationResourcePinSection, SectionID: "schema-pins", Offset: 0, Limit: 50}, section()},
		"pin_catalog":       {navigationResourceKey{Kind: navigationResourcePinCatalog, Offset: 0, Limit: 100}, hubapi.NavigationPinSectionCatalog{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, PinSections: hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor]{{ID: "schema-pins", Name: "Schema Pins", Count: 1}}}},
		"projects":          {navigationResourceKey{Kind: navigationResourceProjects, Offset: 0, Limit: 100}, hubapi.NavigationProjectCatalog{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{{Key: "schema-project", Name: "Schema Project", SessionCount: 1}}}},
		"archived_projects": {navigationResourceKey{Kind: navigationResourceArchivedProjects, Offset: 0, Limit: 100}, hubapi.NavigationProjectCatalog{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{{Key: "schema-project", Name: "Schema Project", SessionCount: 1}}}},
		"test_runs":         {navigationResourceKey{Kind: navigationResourceTestRuns, Offset: 0, Limit: 100}, hubapi.NavigationProjectCatalog{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{{Key: "schema-project", Name: "Schema Project", SessionCount: 1}}}},
		"project":           {navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, hubapi.NavigationProjectResource{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Key: "schema-project", Current: hubapi.NavigationTier{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{session}}, Recent: hubapi.NavigationTier{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}, Archived: hubapi.NavigationTier{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}}},
		"project_page":      {navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "schema-project", Tier: "current", Offset: 0, Limit: 50}, hubapi.NavigationProjectPage{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Key: "schema-project", Tier: "current", Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{session}}},
		"location":          {navigationResourceKey{Kind: navigationResourceLocation, ID: "local:schema-session"}, hubapi.NavigationSessionLocation{GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision, Ref: "local:schema-session", TopLevelRef: "local:schema-session", TopLevel: true, Session: &session}},
	}
	out := make(map[string]navigationSchemaFixture, len(fixtures))
	for name, fixture := range fixtures {
		snapshot, err := normalizeNavigationResource(fixture.key, fixture.object)
		if err != nil {
			t.Fatalf("normalize valid %s fixture: %v", name, err)
		}
		out[name] = navigationSchemaFixture{key: fixture.key, snapshot: snapshot}
	}
	return out
}

func navigationSchemaSession(ref, sessionID string) hubapi.NavigationSessionSummary {
	return hubapi.NavigationSessionSummary{Ref: ref, HostID: "local", SessionID: sessionID, Title: "Schema session", Project: "schema-project", State: "idle", Kind: "session", Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}
}

func replaceNavigationSchemaJSONField(t *testing.T, raw json.RawMessage, name string, value any) json.RawMessage {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object[name] = value
	result, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func deleteNavigationSchemaJSONField(t *testing.T, raw json.RawMessage, name string) json.RawMessage {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, name)
	result, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNavigationSessionValueValidatesCompactActivityCounts(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.RunningJobCount = 4
	session.WatchCount = 7
	session.ArmedWatchCount = 3
	if !navigationSessionValueValid(session) {
		t.Fatal("valid own-session counts rejected")
	}
	for _, mutate := range []func(*hubapi.NavigationSessionSummary){
		func(s *hubapi.NavigationSessionSummary) { s.RunningJobCount = -1 },
		func(s *hubapi.NavigationSessionSummary) { s.WatchCount = -1 },
		func(s *hubapi.NavigationSessionSummary) { s.ArmedWatchCount = 8 },
		func(s *hubapi.NavigationSessionSummary) { s.ArmedWatchCount = -1 },
		func(s *hubapi.NavigationSessionSummary) {
			s.RunningJobCommand = strings.Repeat("x", maxNavigationLabelRunes+1)
		},
	} {
		bad := session
		mutate(&bad)
		if navigationSessionValueValid(bad) {
			t.Fatalf("invalid counts accepted:%+v", bad)
		}
	}
}
