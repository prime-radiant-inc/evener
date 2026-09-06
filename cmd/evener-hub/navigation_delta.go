package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

func diffNavigationSnapshots(key navigationResourceKey, baseVersion, currentVersion appwire.NavigationReadBase, base, current hubapi.NavigationSnapshot) (hubapi.NavigationDelta, error) {
	if err := validateNavigationResourceSnapshot(key, baseVersion.GenerationID, baseVersion.Revision, base); err != nil {
		return hubapi.NavigationDelta{}, fmt.Errorf("invalid base snapshot: %w", err)
	}
	if err := validateNavigationResourceSnapshot(key, currentVersion.GenerationID, currentVersion.Revision, current); err != nil {
		return hubapi.NavigationDelta{}, fmt.Errorf("invalid current snapshot: %w", err)
	}
	delta := hubapi.NavigationDelta{UpsertedEntities: []hubapi.NavigationEntityRecord{}, RemovedEntityKeys: []string{}, UpsertedContainers: []hubapi.NavigationOrderContainer{}, RemovedContainerKeys: []string{}}
	oldEntities, newEntities := map[string]hubapi.NavigationEntityRecord{}, map[string]hubapi.NavigationEntityRecord{}
	for _, entity := range base.Entities {
		oldEntities[entity.Key] = entity
	}
	for _, entity := range current.Entities {
		newEntities[entity.Key] = entity
	}
	for key, entity := range newEntities {
		old, ok := oldEntities[key]
		if !ok || old.Kind != entity.Kind || string(old.Value) != string(entity.Value) {
			delta.UpsertedEntities = append(delta.UpsertedEntities, entity)
		}
	}
	for key := range oldEntities {
		if _, ok := newEntities[key]; !ok {
			delta.RemovedEntityKeys = append(delta.RemovedEntityKeys, key)
		}
	}
	oldContainers, newContainers := map[string]hubapi.NavigationOrderContainer{}, map[string]hubapi.NavigationOrderContainer{}
	for _, container := range base.Containers {
		oldContainers[container.Key] = container
	}
	for _, container := range current.Containers {
		newContainers[container.Key] = container
	}
	for key, container := range newContainers {
		old, ok := oldContainers[key]
		if !ok || old.Owner != container.Owner || !slices.Equal(old.Children, container.Children) {
			delta.UpsertedContainers = append(delta.UpsertedContainers, container)
		}
	}
	for key := range oldContainers {
		if _, ok := newContainers[key]; !ok {
			delta.RemovedContainerKeys = append(delta.RemovedContainerKeys, key)
		}
	}
	if string(base.Metadata) != string(current.Metadata) {
		delta.Metadata = append(json.RawMessage(nil), current.Metadata...)
	}
	sort.Slice(delta.UpsertedEntities, func(i, j int) bool { return delta.UpsertedEntities[i].Key < delta.UpsertedEntities[j].Key })
	sort.Strings(delta.RemovedEntityKeys)
	sort.Slice(delta.UpsertedContainers, func(i, j int) bool { return delta.UpsertedContainers[i].Key < delta.UpsertedContainers[j].Key })
	sort.Strings(delta.RemovedContainerKeys)
	if err := delta.Validate(string(key.Kind)); err != nil {
		return hubapi.NavigationDelta{}, err
	}
	applied, err := applyNavigationDelta(base, delta)
	if err != nil {
		return hubapi.NavigationDelta{}, fmt.Errorf("invalid applied navigation delta: %w", err)
	}
	if err := validateNavigationResourceSnapshot(key, currentVersion.GenerationID, currentVersion.Revision, applied); err != nil {
		return hubapi.NavigationDelta{}, fmt.Errorf("invalid applied navigation delta: %w", err)
	}
	expected := current
	hubapi.SortNavigationSnapshot(&expected)
	if !reflect.DeepEqual(applied, expected) {
		return hubapi.NavigationDelta{}, errors.New("navigation delta does not reconstruct current snapshot")
	}
	return delta, nil
}

func applyNavigationDelta(base hubapi.NavigationSnapshot, delta hubapi.NavigationDelta) (hubapi.NavigationSnapshot, error) {
	entities := make(map[string]hubapi.NavigationEntityRecord, len(base.Entities)+len(delta.UpsertedEntities))
	for _, entity := range base.Entities {
		entities[entity.Key] = entity
	}
	for _, key := range delta.RemovedEntityKeys {
		delete(entities, key)
	}
	for _, entity := range delta.UpsertedEntities {
		entities[entity.Key] = entity
	}
	containers := make(map[string]hubapi.NavigationOrderContainer, len(base.Containers)+len(delta.UpsertedContainers))
	for _, container := range base.Containers {
		containers[container.Key] = container
	}
	for _, key := range delta.RemovedContainerKeys {
		delete(containers, key)
	}
	for _, container := range delta.UpsertedContainers {
		containers[container.Key] = container
	}
	applied := hubapi.NavigationSnapshot{Metadata: base.Metadata, Entities: make([]hubapi.NavigationEntityRecord, 0, len(entities)), Containers: make([]hubapi.NavigationOrderContainer, 0, len(containers))}
	if delta.Metadata != nil {
		applied.Metadata = delta.Metadata
	}
	for _, entity := range entities {
		applied.Entities = append(applied.Entities, entity)
	}
	for _, container := range containers {
		applied.Containers = append(applied.Containers, container)
	}
	hubapi.SortNavigationSnapshot(&applied)
	return applied, nil
}
