package hub

import (
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/hubapi"
)

// normalizeNavigationResource converts an already bounded projector result into
// a resource-scoped normalized graph. The projector remains responsible for
// limits, ordering, and truncation; this pass only gives those values keys.
func normalizeNavigationResource(key navigationResourceKey, object any) (hubapi.NavigationSnapshot, error) {
	b := &navigationDocumentBuilder{key: key.View(), metadata: json.RawMessage(`{}`)}
	switch value := object.(type) {
	case hubapi.NavigationManifest:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(value)
		b.rootContainer("manifest")
	case hubapi.NavigationSectionResource:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID string `json:"generation_id"`
			Revision     uint64 `json:"revision"`
			Offset       uint32 `json:"offset"`
			Limit        uint32 `json:"limit"`
			Remaining    int    `json:"remaining"`
			Truncated    bool   `json:"truncated"`
		}{value.GenerationID, value.Revision, b.key.Offset, b.key.Limit, value.Remaining, value.Truncated})
		b.addSessions("root", "sessions", value.Sessions)
	case hubapi.NavigationPinSectionCatalog:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID string `json:"generation_id"`
			Revision     uint64 `json:"revision"`
			Offset       uint32 `json:"offset"`
			Limit        uint32 `json:"limit"`
			Remaining    int    `json:"remaining"`
		}{value.GenerationID, value.Revision, b.key.Offset, b.key.Limit, value.Remaining})
		children := make([]string, 0, len(value.PinSections))
		for _, section := range value.PinSections {
			entity := hubapi.NavigationEntityRecord{Key: navigationEntityKey(b.key, "pin_section", section.ID), Kind: "pin_section", Value: mustJSON(section)}
			b.entities = append(b.entities, entity)
			children = append(children, entity.Key)
		}
		b.container(navigationRootContainerKey(b.key, "pin_sections"), hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: "pin_sections"}, children)
	case hubapi.NavigationProjectCatalog:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID string `json:"generation_id"`
			Revision     uint64 `json:"revision"`
			Offset       uint32 `json:"offset"`
			Limit        uint32 `json:"limit"`
			Remaining    int    `json:"remaining"`
		}{value.GenerationID, value.Revision, b.key.Offset, b.key.Limit, value.Remaining})
		children := make([]string, 0, len(value.Projects))
		for _, project := range value.Projects {
			entity := hubapi.NavigationEntityRecord{Key: navigationEntityKey(b.key, "project", project.Key), Kind: "project", Value: mustJSON(project)}
			b.entities = append(b.entities, entity)
			children = append(children, entity.Key)
		}
		b.container(navigationRootContainerKey(b.key, "projects"), hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: "projects"}, children)
	case hubapi.NavigationProjectResource:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID      string `json:"generation_id"`
			Revision          uint64 `json:"revision"`
			Key               string `json:"key"`
			CurrentRemaining  int    `json:"current_remaining"`
			RecentRemaining   int    `json:"recent_remaining"`
			ArchivedRemaining int    `json:"archived_remaining"`
			Truncated         bool   `json:"truncated"`
		}{value.GenerationID, value.Revision, value.Key, value.Current.Remaining, value.Recent.Remaining, value.Archived.Remaining, value.Truncated})
		projectEntityKey := navigationEntityKey(b.key, "project", value.Key)
		p := hubapi.NavigationEntityRecord{Key: projectEntityKey, Kind: "project", Value: mustJSON(navigationProjectAnchor{Key: value.Key})}
		b.entities = append(b.entities, p)
		b.addSessions(projectEntityKey, "current", value.Current.Sessions)
		b.addSessions(projectEntityKey, "recent", value.Recent.Sessions)
		b.addSessions(projectEntityKey, "archived", value.Archived.Sessions)
	case hubapi.NavigationProjectPage:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID string `json:"generation_id"`
			Revision     uint64 `json:"revision"`
			Key          string `json:"key"`
			Tier         string `json:"tier"`
			Offset       uint32 `json:"offset"`
			Limit        uint32 `json:"limit"`
			Remaining    int    `json:"remaining"`
			Truncated    bool   `json:"truncated"`
		}{value.GenerationID, value.Revision, value.Key, value.Tier, b.key.Offset, b.key.Limit, value.Remaining, value.Truncated})
		b.addSessions("root", "sessions", value.Sessions)
	case hubapi.NavigationSessionLocation:
		b.generation, b.revision = value.GenerationID, value.Revision
		b.metadata, _ = json.Marshal(struct {
			GenerationID string `json:"generation_id"`
			Revision     uint64 `json:"revision"`
			Ref          string `json:"ref"`
			TopLevelRef  string `json:"top_level_ref"`
			ProjectKey   string `json:"project_key,omitempty"`
			TopLevel     bool   `json:"top_level"`
			Tier         string `json:"tier,omitempty"`
			PinSectionID string `json:"pin_section_id,omitempty"`
		}{value.GenerationID, value.Revision, value.Ref, value.TopLevelRef, value.ProjectKey, value.TopLevel, value.Tier, value.PinSectionID})
		if value.Session != nil {
			b.addSessions("root", "session", hubapi.NavigationArray[hubapi.NavigationSessionSummary]{*value.Session})
		} else {
			b.rootContainer("session")
		}
	default:
		return hubapi.NavigationSnapshot{}, fmt.Errorf("unsupported navigation object %T", object)
	}
	return b.finish()
}

type navigationDocumentBuilder struct {
	key        navigationResourceKey
	generation string
	revision   uint64
	metadata   json.RawMessage
	entities   []hubapi.NavigationEntityRecord
	containers []hubapi.NavigationOrderContainer
}

func (b *navigationDocumentBuilder) rootContainer(slot string) {
	b.container(navigationRootContainerKey(b.key, slot), hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: slot}, nil)
}
func (b *navigationDocumentBuilder) container(key string, owner hubapi.NavigationContainerOwner, children []string) {
	b.containers = append(b.containers, hubapi.NavigationOrderContainer{Key: key, Owner: owner, Children: append([]string{}, children...)})
}
func (b *navigationDocumentBuilder) addSessions(owner, slot string, sessions hubapi.NavigationArray[hubapi.NavigationSessionSummary]) {
	children := make([]string, 0, len(sessions))
	for _, session := range sessions {
		key := navigationEntityKey(b.key, "session", session.Ref)
		shallow := session
		shallow.Children = hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}
		entity := hubapi.NavigationEntityRecord{Key: key, Kind: "session", Value: mustJSON(shallow)}
		b.entities = append(b.entities, entity)
		children = append(children, key)
		b.addSessions(key, "children", session.Children)
	}
	ownerValue := hubapi.NavigationContainerOwner{Kind: "resource_root", Slot: slot}
	containerKey := navigationRootContainerKey(b.key, slot)
	if owner != "root" {
		ownerValue = hubapi.NavigationContainerOwner{Kind: "entity", EntityKey: owner, Slot: slot}
		containerKey = navigationOwnedContainerKey(owner, slot)
	}
	b.container(containerKey, ownerValue, children)
}
func (b *navigationDocumentBuilder) finish() (hubapi.NavigationSnapshot, error) {
	snapshot := hubapi.NavigationSnapshot{Metadata: b.metadata, Entities: b.entities, Containers: b.containers}
	hubapi.SortNavigationSnapshot(&snapshot)
	if err := validateNavigationResourceSnapshot(b.key, b.generation, b.revision, snapshot); err != nil {
		return hubapi.NavigationSnapshot{}, err
	}
	return snapshot, nil
}
func mustJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func cloneNavigationSnapshot(snapshot hubapi.NavigationSnapshot) hubapi.NavigationSnapshot {
	clone := hubapi.NavigationSnapshot{Metadata: append(json.RawMessage(nil), snapshot.Metadata...), Entities: make([]hubapi.NavigationEntityRecord, len(snapshot.Entities)), Containers: make([]hubapi.NavigationOrderContainer, len(snapshot.Containers))}
	copy(clone.Entities, snapshot.Entities)
	copy(clone.Containers, snapshot.Containers)
	for i := range clone.Entities {
		clone.Entities[i].Value = append(json.RawMessage(nil), snapshot.Entities[i].Value...)
	}
	for i := range clone.Containers {
		clone.Containers[i].Children = append([]string(nil), snapshot.Containers[i].Children...)
	}
	return clone
}
func navEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
