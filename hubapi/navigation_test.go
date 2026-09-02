package hubapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestNavigationResourcesJSON(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 25, 20, 0, 0, 0, time.UTC)
	manifest := NavigationManifest{
		GenerationID: "generation-a",
		Revision:     7,
		Sources:      []Source{},
		AttentionSummary: AttentionSummary{
			NeedsYou: 2,
		},
		Sections: NavigationSections{
			Live:        NavigationResourceDescriptor{Count: 1},
			NeedsYou:    NavigationResourceDescriptor{Count: 2},
			PinSections: NavigationResourceDescriptor{Count: 3},
		},
		Catalogs: NavigationCatalogs{
			Projects:         NavigationResourceDescriptor{Count: 4},
			ArchivedProjects: NavigationResourceDescriptor{Count: 5},
			TestRuns:         NavigationResourceDescriptor{Count: 6},
		},
	}
	got, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generation_id":"generation-a","revision":7,"sources":[],"attentionSummary":{"needsYou":2,"error":0,"working":0},"sections":{"live":{"count":1},"needs_you":{"count":2},"pin_sections":{"count":3}},"catalogs":{"projects":{"count":4},"archived_projects":{"count":5},"test_runs":{"count":6}}}`
	if string(got) != want {
		t.Fatalf("manifest JSON = %s, want %s", got, want)
	}

	section := NavigationSectionResource{
		GenerationID: "generation-a",
		Revision:     8,
		Sessions: []NavigationSessionSummary{{
			Ref:       "local:session-a",
			HostID:    "local",
			SessionID: "session-a",
			Title:     "Session A",
			Project:   "project-a",
			State:     "active",
			Kind:      "session",
			Live:      true,
			UpdatedAt: &updatedAt,
			Children:  []NavigationSessionSummary{},
		}},
		Remaining: 0,
	}
	got, err = json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	for _, wantFragment := range []string{
		`"sessions":[{`,
		`"host_id":"local"`,
		`"session_id":"session-a"`,
		`"updated_at":"2026-08-25T20:00:00Z"`,
		`"children":[]`,
	} {
		if !strings.Contains(string(got), wantFragment) {
			t.Fatalf("section JSON = %s, missing %s", got, wantFragment)
		}
	}
	if strings.Contains(string(got), `"branch"`) || strings.Contains(string(got), `"favorite"`) {
		t.Fatalf("section JSON includes absent optional fields: %s", got)
	}
}

func TestNavigationCollectionsEncodeEmptySlices(t *testing.T) {
	tests := []struct {
		name  string
		value any
		field string
	}{
		{"section", NavigationSectionResource{Sessions: []NavigationSessionSummary{}}, `"sessions":[]`},
		{"pin catalog", NavigationPinSectionCatalog{PinSections: []NavigationPinSectionDescriptor{}}, `"pin_sections":[]`},
		{"project catalog", NavigationProjectCatalog{Projects: []NavigationProjectSummary{}}, `"projects":[]`},
		{"project", NavigationProjectResource{Current: NavigationTier{Sessions: []NavigationSessionSummary{}}, Recent: NavigationTier{Sessions: []NavigationSessionSummary{}}, Archived: NavigationTier{Sessions: []NavigationSessionSummary{}}}, `"sessions":[]`},
		{"project page", NavigationProjectPage{Sessions: []NavigationSessionSummary{}}, `"sessions":[]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tc.field) {
				t.Fatalf("JSON = %s, want non-null %s", got, tc.field)
			}
		})
	}
}

func TestNavigationArraysEncodeNilAsEmptyArrays(t *testing.T) {
	tests := []struct {
		name  string
		value any
		field string
	}{
		{"manifest sources", NavigationManifest{}, `"sources":[]`},
		{"section sessions", NavigationSectionResource{}, `"sessions":[]`},
		{"pin catalog", NavigationPinSectionCatalog{}, `"pin_sections":[]`},
		{"project catalog", NavigationProjectCatalog{}, `"projects":[]`},
		{"project tiers", NavigationProjectResource{}, `"sessions":[]`},
		{"project page", NavigationProjectPage{}, `"sessions":[]`},
		{"session children", NavigationSessionSummary{}, `"children":[]`},
		{"mutation targets", NavigationMutation{}, `"targets":[]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tc.field) {
				t.Fatalf("JSON = %s, want nil array encoded as %s", got, tc.field)
			}
		})
	}
}

func TestNavigationMutationSharesAppWireTargetShape(t *testing.T) {
	mutation := NavigationMutation{
		GenerationID: "generation-a",
		Targets: []appwire.NavigationInvalidationTarget{{
			Kind:       appwire.NavigationTargetProject,
			ProjectKey: "project-key",
			Revision:   3,
		}},
	}
	got, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generation_id":"generation-a","targets":[{"kind":"project","projectKey":"project-key","revision":3}]}`
	if string(got) != want {
		t.Fatalf("mutation JSON = %s, want %s", got, want)
	}
}

func TestNavigationRecordWireShapeOmitsRevisionAndRejectsObsoleteRevision(t *testing.T) {
	snapshot := NavigationSnapshot{
		Metadata: []byte(`{"generation_id":"g","revision":1}`),
		Entities: []NavigationEntityRecord{{Key: "entity", Kind: "session", Value: []byte(`{}`)}},
		Containers: []NavigationOrderContainer{{
			Key:      "container",
			Owner:    NavigationContainerOwner{Kind: "resource_root", Slot: "sessions"},
			Children: []string{"entity"},
		}},
	}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Entities   []map[string]json.RawMessage `json:"entities"`
		Containers []map[string]json.RawMessage `json:"containers"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, record := range append(decoded.Entities, decoded.Containers...) {
		if _, ok := record["revision"]; ok {
			t.Fatalf("normalized record emitted obsolete revision: %s", wire)
		}
	}

	for name, obsolete := range map[string]string{
		"entity":    `{"key":"entity","revision":1,"kind":"session","value":{}}`,
		"container": `{"key":"container","revision":1,"owner":{"kind":"resource_root","slot":"sessions"},"children":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var destination any
			if name == "entity" {
				destination = new(NavigationEntityRecord)
			} else {
				destination = new(NavigationOrderContainer)
			}
			if err := json.Unmarshal([]byte(obsolete), destination); err == nil {
				t.Fatal("obsolete record revision was accepted")
			}
		})
	}
}
