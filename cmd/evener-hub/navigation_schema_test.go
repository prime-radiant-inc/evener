package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
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
		{
			name: "root container removed", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				for index := range snapshot.Containers {
					if snapshot.Containers[index].Owner.Kind == "resource_root" {
						snapshot.Containers = append(snapshot.Containers[:index], snapshot.Containers[index+1:]...)
						return
					}
				}
			},
		},
		{
			// The anchor's current-tier rows move into its recent tier so every
			// session stays parented exactly once; only the anchor's own slot set
			// is then wrong (current is missing), which is the graph rule under
			// test.
			name: "anchor tier slot removed", resource: "project", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				current, recent := -1, -1
				for index, container := range snapshot.Containers {
					if container.Owner.Kind != "entity" {
						continue
					}
					switch container.Owner.Slot {
					case "current":
						current = index
					case "recent":
						recent = index
					}
				}
				if current < 0 || recent < 0 {
					panic("project fixture lacks tier containers")
				}
				snapshot.Containers[recent].Children = append(snapshot.Containers[recent].Children, snapshot.Containers[current].Children...)
				snapshot.Containers = append(snapshot.Containers[:current], snapshot.Containers[current+1:]...)
			},
		},
		{
			name: "session children slot removed", resource: "live", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				for index := range snapshot.Containers {
					if snapshot.Containers[index].Owner.Kind == "entity" {
						snapshot.Containers = append(snapshot.Containers[:index], snapshot.Containers[index+1:]...)
						return
					}
				}
			},
		},
		{
			// A project detail has no root container of its own, so any
			// resource_root container is a foreign graft.
			name: "root container on the project detail", resource: "project", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{
					Key:      navigationRootContainerKey(key, "sessions"),
					Owner:    hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: "sessions"},
					Children: []string{},
				})
			},
		},
		{
			name: "project detail without its anchor entity", resource: "project", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, _ navigationResourceKey) {
				snapshot.Entities = nil
				snapshot.Containers = nil
			},
		},
		{
			name: "entity key outside its identity", resource: "live", category: "scope",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				// Well-formed key syntax naming some other identity: the scope
				// rule, not the syntax rule, must refuse it.
				oldKey := snapshot.Entities[0].Key
				newKey := navigationViewScope(key) + "/entity/" + strings.Repeat("0", 64)
				snapshot.Entities[0].Key = newKey
				for index := range snapshot.Containers {
					for child := range snapshot.Containers[index].Children {
						if snapshot.Containers[index].Children[child] == oldKey {
							snapshot.Containers[index].Children[child] = newKey
						}
					}
					if snapshot.Containers[index].Owner.EntityKey == oldKey {
						snapshot.Containers[index].Owner.EntityKey = newKey
						snapshot.Containers[index].Key = navigationOwnedContainerKey(newKey, snapshot.Containers[index].Owner.Slot)
					}
				}
			},
		},
		{
			// A location is one session; a second entity is refused by the
			// graph rules whichever invariant fires first.
			name: "second location entity", resource: "location", category: "graph",
			mutate: func(snapshot *hubapi.NavigationSnapshot, key navigationResourceKey) {
				second := navigationSchemaSession("local:schema-second", "schema-second")
				secondKey := navigationEntityKey(key, "session", second.Ref)
				snapshot.Entities = append(snapshot.Entities, hubapi.NavigationEntityRecord{Key: secondKey, Kind: "session", Value: mustJSON(second)})
				snapshot.Containers = append(snapshot.Containers, hubapi.NavigationOrderContainer{
					Key:      navigationOwnedContainerKey(secondKey, "children"),
					Owner:    hubapi.NavigationContainerOwner{Kind: "entity", EntityKey: secondKey, Slot: "children"},
					Children: []string{},
				})
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
			if violation, ok := errors.AsType[navigationSchemaViolationError](err); !ok || violation.category != test.category {
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

// Metadata is the read's own selector echoed on the wire: each kind's
// metadata must name the offset, limit, key, tier or ref the read asked with,
// and the generation and revision must themselves be well-formed. A valid
// control precedes each mismatch so the refusal is pinned to the selector,
// not to a malformed raw value.
func TestValidateNavigationMetadataRejectsSelectorMismatches(t *testing.T) {
	t.Parallel()
	generation, revision := navigationSchemaGeneration, navigationSchemaRevision
	paged := `{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"offset":0,"limit":50,"remaining":0,"truncated":false}`
	catalog := `{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"offset":0,"limit":100,"remaining":0}`
	project := `{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"key":"schema-project","current_remaining":0,"recent_remaining":0,"archived_remaining":0,"truncated":false}`
	location := `{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"ref":"local:schema-session","top_level_ref":"local:schema-session","top_level":true}`
	tests := []struct {
		name string
		key  navigationResourceKey
		raw  string
	}{
		{"catalog offset mismatch", navigationResourceKey{Kind: navigationResourceProjects, Offset: 0, Limit: 100},
			`{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"offset":1,"limit":100,"remaining":0}`},
		{"project key mismatch", navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"},
			`{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"key":"other","current_remaining":0,"recent_remaining":0,"archived_remaining":0,"truncated":false}`},
		{"location ref mismatch", navigationResourceKey{Kind: navigationResourceLocation, ID: "local:schema-session"},
			`{"generation_id":"` + generation + `","revision":` + strconv.FormatUint(revision, 10) + `,"ref":"local:other","top_level_ref":"local:schema-session","top_level":true}`},
		{"unknown kind", navigationResourceKey{Kind: navigationResourceKind("bogus")}, `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateNavigationMetadata(test.key, generation, revision, []byte(test.raw)); err == nil {
				t.Fatal("mismatched metadata accepted")
			}
		})
	}
	// The same raw values pass with a matching key and generation, so the
	// refusals above name the selector mismatch.
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceLive, Offset: 0, Limit: 50}, generation, revision, []byte(paged)); err != nil {
		t.Fatalf("valid paged metadata rejected: %v", err)
	}
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceProjects, Offset: 0, Limit: 100}, generation, revision, []byte(catalog)); err != nil {
		t.Fatalf("valid catalog metadata rejected: %v", err)
	}
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, generation, revision, []byte(project)); err != nil {
		t.Fatalf("valid project metadata rejected: %v", err)
	}
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceLocation, ID: "local:schema-session"}, generation, revision, []byte(location)); err != nil {
		t.Fatalf("valid location metadata rejected: %v", err)
	}
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceLive, Offset: 0, Limit: 50}, "", revision, []byte(paged)); err == nil {
		t.Fatal("empty generation accepted")
	}
	if _, err := validateNavigationMetadata(navigationResourceKey{Kind: navigationResourceLive, Offset: 0, Limit: 50}, generation, 0, []byte(paged)); err == nil {
		t.Fatal("zero revision accepted")
	}
}

// Every resource kind admits only the entity kinds and value shapes it can
// render; anything else is refused as an entity violation before the graph is
// even walked.
func TestValidateNavigationEntityRejectsWrongKindsAndValues(t *testing.T) {
	t.Parallel()
	session := mustJSON(navigationSchemaSession("local:schema-session", "schema-session"))
	tests := []struct {
		name     string
		key      navigationResourceKey
		metadata any
		entity   hubapi.NavigationEntityRecord
	}{
		{
			name:   "manifest admits no entity",
			key:    navigationResourceKey{Kind: navigationResourceManifest},
			entity: hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceManifest}, "session", "local:schema-session"), Kind: "session", Value: session},
		},
		{
			name:   "pin catalog holds only pin sections",
			key:    navigationResourceKey{Kind: navigationResourcePinCatalog},
			entity: hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourcePinCatalog}, "session", "local:schema-session"), Kind: "session", Value: session},
		},
		{
			name:   "pin section value with a negative count",
			key:    navigationResourceKey{Kind: navigationResourcePinCatalog},
			entity: hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourcePinCatalog}, "pin_section", "pins"), Kind: "pin_section", Value: []byte(`{"id":"pins","name":"Pins","count":-1}`)},
		},
		{
			name:   "catalog holds only projects",
			key:    navigationResourceKey{Kind: navigationResourceProjects},
			entity: hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceProjects}, "session", "local:schema-session"), Kind: "session", Value: session},
		},
		{
			name:   "project summary without an identity",
			key:    navigationResourceKey{Kind: navigationResourceProjects},
			entity: hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceProjects}, "project", "schema-project"), Kind: "project", Value: []byte(`{"key":"","name":"Project","session_count":1}`)},
		},
		{
			name:     "project anchor naming another key",
			key:      navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"},
			metadata: navigationProjectMetadata{Key: "schema-project"},
			entity:   hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, "project", "other"), Kind: "project", Value: []byte(`{"key":"other"}`)},
		},
		{
			name:     "project detail holding an unknown kind",
			key:      navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"},
			metadata: navigationProjectMetadata{Key: "schema-project"},
			entity:   hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, "pin_section", "pins"), Kind: "pin_section", Value: []byte(`{"id":"pins","name":"Pins","count":1}`)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := validateNavigationEntity(test.key, test.metadata, test.entity)
			if violation, ok := errors.AsType[navigationSchemaViolationError](err); !ok || violation.category != "entity" {
				t.Fatalf("error = %v, want an entity violation", err)
			}
		})
	}
	// The project kind still accepts its anchor when the key matches.
	anchor := hubapi.NavigationEntityRecord{Key: navigationEntityKey(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, "project", "schema-project"), Kind: "project", Value: []byte(`{"key":"schema-project"}`)}
	identity, isAnchor, err := validateNavigationEntity(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "schema-project"}, navigationProjectMetadata{Key: "schema-project"}, anchor)
	if err != nil || identity != "schema-project" || !isAnchor {
		t.Fatalf("anchor validation = %q %v %v, want the project's key marked as anchor", identity, isAnchor, err)
	}
}

// The strict decoder and the null scan are the wire's outer guard: a value
// that is not a JSON object, or a null masquerading as a field, never reaches
// the typed decoders.
func TestStrictNavigationDecodeRefusesNonObjectsAndNulls(t *testing.T) {
	t.Parallel()
	var destination map[string]any
	if err := strictNavigationDecode([]byte("[]"), nil, &destination); err == nil {
		t.Error("a non-object value was decoded")
	}
	if err := strictNavigationDecode([]byte("null"), nil, &destination); err == nil {
		t.Error("a null literal was decoded")
	}
	if err := strictNavigationDecode([]byte(`{"generation_id":"g"}`), []string{"generation_id", "revision"}, &destination); err == nil {
		t.Error("a missing required field was decoded")
	}
	if !navigationJSONContainsNull(json.RawMessage("{malformed")) {
		t.Error("a malformed raw value passed the null scan")
	}
	if navigationJSONContainsNull([]byte(`{"a":1}`)) {
		t.Error("a well-formed object was treated as null-containing")
	}
}

// The manifest's raw shape is checked before its typed value: the attention,
// section and catalog objects are exact, sources are an array of at most 64
// exact objects, and the top itself must be an object.
func TestValidateNavigationManifestRawRejectsCorruptedShapes(t *testing.T) {
	t.Parallel()
	manifest := hubapi.NavigationManifest{
		GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision,
		Sources:  hubapi.NavigationArray[hubapi.Source]{{ID: "source", Label: "Source", Kind: "local", Online: true}},
		Sections: hubapi.NavigationSections{Live: hubapi.NavigationResourceDescriptor{Count: 1}},
		Catalogs: hubapi.NavigationCatalogs{Projects: hubapi.NavigationResourceDescriptor{Count: 1}},
	}
	valid := mustJSON(manifest)
	if !validateNavigationManifestRaw(valid) {
		t.Fatal("a well-formed manifest raw was refused")
	}
	if validateNavigationManifestRaw([]byte("[]")) {
		t.Error("a non-object manifest raw was accepted")
	}
	corrupt := func(name string, value any) json.RawMessage {
		return replaceNavigationSchemaJSONField(t, valid, name, value)
	}
	if validateNavigationManifestRaw(corrupt("attentionSummary", map[string]any{"needsYou": 1})) {
		t.Error("an inexact attention summary was accepted")
	}
	if validateNavigationManifestRaw(corrupt("sections", map[string]any{"live": map[string]any{"count": 1, "extra": 0}, "needs_you": map[string]any{"count": 0}, "pin_sections": map[string]any{"count": 0}})) {
		t.Error("a section object with extra fields was accepted")
	}
	if validateNavigationManifestRaw(corrupt("catalogs", map[string]any{"projects": map[string]any{"size": 1}, "archived_projects": map[string]any{"count": 0}, "test_runs": map[string]any{"count": 0}})) {
		t.Error("a catalog object with the wrong fields was accepted")
	}
	if validateNavigationManifestRaw(corrupt("sources", map[string]any{})) {
		t.Error("a non-array sources field was accepted")
	}
	many := make([]map[string]any, 65)
	for index := range many {
		many[index] = map[string]any{"id": fmt.Sprintf("source-%02d", index), "label": "Source", "kind": "local", "online": true}
	}
	if validateNavigationManifestRaw(corrupt("sources", many)) {
		t.Error("more than 64 sources were accepted")
	}
	if validateNavigationManifestRaw(corrupt("sources", []any{map[string]any{"id": "source", "label": "Source", "kind": "local"}})) {
		t.Error("a source object with a missing field was accepted")
	}
}

// The manifest's typed values and the project summary are bounded the way
// the wire bounds them: identities are identities, counts are safe
// non-negative integers, and the source list fits the sources the inputs
// admit.
func TestNavigationManifestValuesAndProjectSummaryBounds(t *testing.T) {
	t.Parallel()
	manifest := hubapi.NavigationManifest{
		GenerationID: navigationSchemaGeneration, Revision: navigationSchemaRevision,
		Sources:  hubapi.NavigationArray[hubapi.Source]{{ID: "source", Label: "Source", Kind: "local"}},
		Sections: hubapi.NavigationSections{Live: hubapi.NavigationResourceDescriptor{Count: 1}},
	}
	if !navigationManifestValuesValid(manifest) {
		t.Fatal("a valid manifest value set was refused")
	}
	emptyID := manifest
	emptyID.Sources = hubapi.NavigationArray[hubapi.Source]{{ID: "", Label: "Source", Kind: "local"}}
	if navigationManifestValuesValid(emptyID) {
		t.Error("a source without an ID was accepted")
	}
	negative := manifest
	negative.Sections.Live.Count = -1
	if navigationManifestValuesValid(negative) {
		t.Error("a negative section count was accepted")
	}

	summary := hubapi.NavigationProjectSummary{Key: "schema-project", Name: "Schema Project", SessionCount: 1}
	if !navigationProjectSummaryValid(summary) {
		t.Fatal("a valid project summary was refused")
	}
	oversizeWorkingDir := summary
	oversizeWorkingDir.WorkingDir = strings.Repeat("/", maxNavigationWorkingDirBytes+1)
	if navigationProjectSummaryValid(oversizeWorkingDir) {
		t.Error("an over-long working directory was accepted")
	}
	tooManySources := summary
	tooManySources.Sources = make(hubapi.NavigationArray[string], maxNavigationProjectSources+1)
	if navigationProjectSummaryValid(tooManySources) {
		t.Error("more sources than the inputs admit were accepted")
	}
	emptySource := summary
	emptySource.Sources = hubapi.NavigationArray[string]{""}
	if navigationProjectSummaryValid(emptySource) {
		t.Error("an empty source name was accepted")
	}
	negativeCount := summary
	negativeCount.SessionCount = -1
	if navigationProjectSummaryValid(negativeCount) {
		t.Error("a negative session count was accepted")
	}
}

// Entity key syntax accepts only the view's scope prefix and lowercase hex,
// and the exact-slot helper refuses both a wrong count and a missing slot.
func TestNavigationEntityKeySyntaxAndExactSlotsRefuseCorruption(t *testing.T) {
	t.Parallel()
	key := navigationResourceKey{Kind: navigationResourceLive, Offset: 0, Limit: 50}
	valid := navigationEntityKey(key, "session", "local:schema-session")
	if !navigationEntityKeySyntax(key, valid) {
		t.Fatal("a minted entity key failed its own syntax check")
	}
	corrupted := valid[:len(valid)-1] + "g"
	if navigationEntityKeySyntax(key, corrupted) {
		t.Error("a non-hex entity key suffix was accepted")
	}
	if navigationEntityKeySyntax(key, valid+"0") {
		t.Error("an over-long entity key was accepted")
	}
	if navigationHasExactSlots(nil, "children") {
		t.Error("a missing slot set matched an exact slot list")
	}
	if navigationHasExactSlots(map[string]bool{"other": true}, "children") {
		t.Error("a wrong slot name matched an exact slot list")
	}
	if !navigationHasExactSlots(map[string]bool{"children": true}, "children") {
		t.Error("the exact slot set was refused")
	}
}
