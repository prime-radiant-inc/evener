package hubapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

const (
	maxNavigationGraphDepth      = 32
	maxNavigationSessionEntities = 2_000
	maxNavigationGraphEntities   = maxNavigationSessionEntities + 1
	maxNavigationGraphContainers = maxNavigationSessionEntities + 3
)

// NavigationEntityRecord is a shallow, resource-scoped entity in a normalized
// navigation representation. Key identity is deliberately scoped by the
// complete resource key and is not a global entity identifier.
type NavigationEntityRecord struct {
	Key   string          `json:"key"`
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

type NavigationContainerOwner struct {
	Kind      string `json:"kind"`
	Slot      string `json:"slot,omitempty"`
	EntityKey string `json:"entityKey,omitempty"` //nolint:tagliatelle // Navigation v2 wire contract is camelCase
}

type NavigationOrderContainer struct {
	Key      string                   `json:"key"`
	Owner    NavigationContainerOwner `json:"owner"`
	Children []string                 `json:"children"`
}

func (record *NavigationEntityRecord) UnmarshalJSON(raw []byte) error {
	type plain NavigationEntityRecord
	var decoded plain
	if err := decodeStrictNavigationRecord(raw, &decoded); err != nil {
		return err
	}
	*record = NavigationEntityRecord(decoded)
	return nil
}

func (container *NavigationOrderContainer) UnmarshalJSON(raw []byte) error {
	type plain NavigationOrderContainer
	var decoded plain
	if err := decodeStrictNavigationRecord(raw, &decoded); err != nil {
		return err
	}
	*container = NavigationOrderContainer(decoded)
	return nil
}

func decodeStrictNavigationRecord(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing navigation record JSON")
		}
		return err
	}
	return nil
}

type NavigationSnapshot struct {
	Metadata   json.RawMessage            `json:"metadata"`
	Entities   []NavigationEntityRecord   `json:"entities"`
	Containers []NavigationOrderContainer `json:"containers"`
}

type NavigationDelta struct {
	Metadata             json.RawMessage            `json:"metadata,omitempty"`
	UpsertedEntities     []NavigationEntityRecord   `json:"upsertedEntities"`     //nolint:tagliatelle // Navigation v2 wire contract is camelCase
	RemovedEntityKeys    []string                   `json:"removedEntityKeys"`    //nolint:tagliatelle // Navigation v2 wire contract is camelCase
	UpsertedContainers   []NavigationOrderContainer `json:"upsertedContainers"`   //nolint:tagliatelle // Navigation v2 wire contract is camelCase
	RemovedContainerKeys []string                   `json:"removedContainerKeys"` //nolint:tagliatelle // Navigation v2 wire contract is camelCase
}

func (snapshot NavigationSnapshot) MarshalJSON() ([]byte, error) {
	if snapshot.Entities == nil {
		snapshot.Entities = []NavigationEntityRecord{}
	}
	if snapshot.Containers == nil {
		snapshot.Containers = []NavigationOrderContainer{}
	}
	if snapshot.Metadata == nil {
		snapshot.Metadata = json.RawMessage(`{}`)
	}
	type plain NavigationSnapshot
	return json.Marshal(plain(snapshot))
}

func (delta NavigationDelta) MarshalJSON() ([]byte, error) {
	if delta.UpsertedEntities == nil {
		delta.UpsertedEntities = []NavigationEntityRecord{}
	}
	if delta.RemovedEntityKeys == nil {
		delta.RemovedEntityKeys = []string{}
	}
	if delta.UpsertedContainers == nil {
		delta.UpsertedContainers = []NavigationOrderContainer{}
	}
	if delta.RemovedContainerKeys == nil {
		delta.RemovedContainerKeys = []string{}
	}
	type plain NavigationDelta
	return json.Marshal(plain(delta))
}

func (snapshot NavigationSnapshot) Validate(resource string) error {
	if resource == "" {
		return errors.New("navigation resource is required")
	}
	if len(snapshot.Entities) > maxNavigationGraphEntities || len(snapshot.Containers) > maxNavigationGraphContainers {
		return errors.New("navigation graph exceeds bounds")
	}
	if len(snapshot.Metadata) == 0 || !json.Valid(snapshot.Metadata) {
		return errors.New("invalid navigation metadata")
	}
	entities := make(map[string]NavigationEntityRecord, len(snapshot.Entities))
	sessionEntities := 0
	for _, entity := range snapshot.Entities {
		if !validNavigationKey(entity.Key) || entity.Kind == "" || len(entity.Value) == 0 || !json.Valid(entity.Value) {
			return errors.New("invalid navigation entity")
		}
		if _, exists := entities[entity.Key]; exists {
			return errors.New("duplicate navigation entity key")
		}
		entities[entity.Key] = entity
		if entity.Kind == "session" {
			sessionEntities++
			if sessionEntities > maxNavigationSessionEntities {
				return errors.New("navigation graph exceeds bounds")
			}
		}
	}
	containers := make(map[string]NavigationOrderContainer, len(snapshot.Containers))
	for _, container := range snapshot.Containers {
		if !validNavigationKey(container.Key) || container.Owner.Kind == "resource_root" && container.Owner.EntityKey != "" || container.Owner.Kind == "entity" && (container.Owner.EntityKey == "" || container.Owner.Slot == "") {
			return errors.New("invalid navigation container owner")
		}
		if container.Owner.Kind != "resource_root" && container.Owner.Kind != "entity" {
			return errors.New("invalid navigation container owner kind")
		}
		if container.Owner.Kind == "entity" {
			if _, ok := entities[container.Owner.EntityKey]; !ok {
				return errors.New("dangling navigation container owner")
			}
		}
		if _, exists := containers[container.Key]; exists {
			return errors.New("duplicate navigation container key")
		}
		containers[container.Key] = container
	}
	parents := make(map[string]string)
	rootChildren := make([]string, 0)
	childrenByOwner := make(map[string][]string)
	for _, container := range snapshot.Containers {
		if container.Owner.Kind == "resource_root" {
			rootChildren = append(rootChildren, container.Children...)
		} else {
			childrenByOwner[container.Owner.EntityKey] = append(childrenByOwner[container.Owner.EntityKey], container.Children...)
		}
		for _, child := range container.Children {
			if _, ok := entities[child]; !ok {
				return errors.New("dangling navigation child")
			}
			if _, exists := parents[child]; exists {
				return errors.New("navigation entity has multiple parents")
			}
			parents[child] = container.Key
		}
	}
	// Containers form an owner-to-container graph through entity references. A
	// DFS detects cycles and applies the projector's depth semantics without
	// imposing an ordering on wire arrays: resource-root children start at depth
	// one, while a parentless resource anchor starts at depth zero.
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string, int) error
	visit = func(entityKey string, depth int) error {
		if visiting[entityKey] {
			return errors.New("cyclic navigation graph")
		}
		if visited[entityKey] {
			return nil
		}
		if depth > maxNavigationGraphDepth {
			return errors.New("navigation graph exceeds depth bound")
		}
		visiting[entityKey] = true
		for _, child := range childrenByOwner[entityKey] {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		delete(visiting, entityKey)
		visited[entityKey] = true
		return nil
	}
	for _, key := range rootChildren {
		if err := visit(key, 1); err != nil {
			return err
		}
	}
	for key := range entities {
		if _, hasParent := parents[key]; !hasParent {
			if err := visit(key, 0); err != nil {
				return err
			}
		}
	}
	for key := range entities {
		if err := visit(key, 1); err != nil {
			return err
		}
	}
	return nil
}

func (delta NavigationDelta) Validate(resource string) error {
	if resource == "" {
		return errors.New("navigation resource is required")
	}
	if delta.Metadata != nil && !json.Valid(delta.Metadata) {
		return errors.New("invalid navigation metadata")
	}
	seenEntities, seenContainers := map[string]bool{}, map[string]bool{}
	for _, entity := range delta.UpsertedEntities {
		if !validNavigationKey(entity.Key) || entity.Kind == "" || !json.Valid(entity.Value) || seenEntities[entity.Key] {
			return errors.New("invalid upserted navigation entity")
		}
		seenEntities[entity.Key] = true
	}
	for _, key := range delta.RemovedEntityKeys {
		if !validNavigationKey(key) || seenEntities[key] {
			return errors.New("invalid removed navigation entity key")
		}
		seenEntities[key] = true
	}
	for _, container := range delta.UpsertedContainers {
		if !validNavigationKey(container.Key) || seenContainers[container.Key] {
			return errors.New("invalid upserted navigation container")
		}
		if container.Owner.Kind != "resource_root" && container.Owner.Kind != "entity" {
			return errors.New("invalid upserted navigation owner")
		}
		if container.Owner.Kind == "entity" && container.Owner.EntityKey == "" {
			return errors.New("missing navigation owner entity")
		}
		seenContainers[container.Key] = true
	}
	for _, key := range delta.RemovedContainerKeys {
		if !validNavigationKey(key) || seenContainers[key] {
			return errors.New("invalid removed navigation container key")
		}
		seenContainers[key] = true
	}
	return nil
}

func validNavigationKey(key string) bool { return key != "" && len(key) <= 2048 }

func SortNavigationSnapshot(snapshot *NavigationSnapshot) {
	sort.Slice(snapshot.Entities, func(i, j int) bool {
		if snapshot.Entities[i].Kind == snapshot.Entities[j].Kind {
			return snapshot.Entities[i].Key < snapshot.Entities[j].Key
		}
		return snapshot.Entities[i].Kind < snapshot.Entities[j].Kind
	})
	sort.Slice(snapshot.Containers, func(i, j int) bool { return snapshot.Containers[i].Key < snapshot.Containers[j].Key })
}
