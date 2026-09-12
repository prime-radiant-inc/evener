package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"primeradiant.com/evener/hubapi"
)

type navigationPagedMetadata struct {
	GenerationID string `json:"generation_id"`
	Revision     uint64 `json:"revision"`
	Offset       uint32 `json:"offset"`
	Limit        uint32 `json:"limit"`
	Remaining    int64  `json:"remaining"`
	Truncated    bool   `json:"truncated"`
}

type navigationCatalogMetadata struct {
	GenerationID string `json:"generation_id"`
	Revision     uint64 `json:"revision"`
	Offset       uint32 `json:"offset"`
	Limit        uint32 `json:"limit"`
	Remaining    int64  `json:"remaining"`
}

type navigationProjectMetadata struct {
	GenerationID      string `json:"generation_id"`
	Revision          uint64 `json:"revision"`
	Key               string `json:"key"`
	CurrentRemaining  int64  `json:"current_remaining"`
	RecentRemaining   int64  `json:"recent_remaining"`
	ArchivedRemaining int64  `json:"archived_remaining"`
	Truncated         bool   `json:"truncated"`
}

type navigationProjectPageMetadata struct {
	GenerationID string `json:"generation_id"`
	Revision     uint64 `json:"revision"`
	Key          string `json:"key"`
	Tier         string `json:"tier"`
	Offset       uint32 `json:"offset"`
	Limit        uint32 `json:"limit"`
	Remaining    int64  `json:"remaining"`
	Truncated    bool   `json:"truncated"`
}

type navigationLocationMetadata struct {
	GenerationID string `json:"generation_id"`
	Revision     uint64 `json:"revision"`
	Ref          string `json:"ref"`
	TopLevelRef  string `json:"top_level_ref"`
	ProjectKey   string `json:"project_key,omitempty"`
	TopLevel     bool   `json:"top_level"`
	Tier         string `json:"tier,omitempty"`
	PinSectionID string `json:"pin_section_id,omitempty"`
}

type navigationProjectAnchor struct {
	Key string `json:"key"`
}

func navigationSchemaError(category string) error {
	return fmt.Errorf("navigation schema: %s", category)
}

func validateNavigationResourceSnapshot(
	key navigationResourceKey,
	generation string,
	revision uint64,
	snapshot hubapi.NavigationSnapshot,
) error {
	key = key.View()
	if err := snapshot.Validate(string(key.Kind)); err != nil {
		return navigationSchemaError("graph")
	}
	metadata, err := validateNavigationMetadata(key, generation, revision, snapshot.Metadata)
	if err != nil {
		return err
	}

	identities := make(map[string]bool, len(snapshot.Entities))
	entityKinds := make(map[string]string, len(snapshot.Entities))
	projectAnchor := ""
	for _, entity := range snapshot.Entities {
		identity, anchor, validateErr := validateNavigationEntity(key, metadata, entity)
		if validateErr != nil {
			return validateErr
		}
		logical := entity.Kind + "\x00" + identity
		if identities[logical] {
			return navigationSchemaError("identity")
		}
		identities[logical] = true
		if entity.Key != navigationEntityKey(key, entity.Kind, identity) {
			return navigationSchemaError("scope")
		}
		entityKinds[entity.Key] = entity.Kind
		if anchor {
			if projectAnchor != "" {
				return navigationSchemaError("graph")
			}
			projectAnchor = entity.Key
		}
	}

	expectedRoot := navigationExpectedRootSlot(key.Kind)
	rootCount := 0
	parentCount := make(map[string]int, len(snapshot.Entities))
	ownedSlots := make(map[string]map[string]bool, len(snapshot.Entities))
	for _, container := range snapshot.Containers {
		if container.Owner.Kind == "resource_root" {
			rootCount++
			if expectedRoot == "" || container.Owner.Slot != expectedRoot || container.Key != navigationRootContainerKey(key, expectedRoot) {
				return navigationSchemaError("graph")
			}
			if !navigationRootChildrenWithinBounds(key, len(container.Children)) {
				return navigationSchemaError("graph")
			}
		} else {
			kind, exists := entityKinds[container.Owner.EntityKey]
			if !exists || container.Key != navigationOwnedContainerKey(container.Owner.EntityKey, container.Owner.Slot) {
				return navigationSchemaError("graph")
			}
			if !navigationOwnedSlotAllowed(key.Kind, kind, container.Owner.EntityKey == projectAnchor, container.Owner.Slot) {
				return navigationSchemaError("graph")
			}
			if kind == "session" && len(container.Children) > maxNavigationChildren || kind == "project" && len(container.Children) > maxNavigationSectionRows {
				return navigationSchemaError("graph")
			}
			if ownedSlots[container.Owner.EntityKey] == nil {
				ownedSlots[container.Owner.EntityKey] = map[string]bool{}
			}
			ownedSlots[container.Owner.EntityKey][container.Owner.Slot] = true
		}
		for _, child := range container.Children {
			parentCount[child]++
		}
	}
	if expectedRoot == "" {
		if rootCount != 0 {
			return navigationSchemaError("graph")
		}
	} else if rootCount != 1 {
		return navigationSchemaError("graph")
	}

	for entityKey, kind := range entityKinds {
		anchor := entityKey == projectAnchor
		if anchor {
			if parentCount[entityKey] != 0 || !navigationHasExactSlots(ownedSlots[entityKey], "current", "recent", "archived") {
				return navigationSchemaError("graph")
			}
			continue
		}
		if parentCount[entityKey] != 1 {
			return navigationSchemaError("graph")
		}
		if kind == "session" {
			if !navigationHasExactSlots(ownedSlots[entityKey], "children") {
				return navigationSchemaError("graph")
			}
		} else if len(ownedSlots[entityKey]) != 0 {
			return navigationSchemaError("graph")
		}
	}
	if key.Kind == navigationResourceProject && projectAnchor == "" {
		return navigationSchemaError("graph")
	}
	if key.Kind == navigationResourceLocation && len(snapshot.Entities) > 1 {
		return navigationSchemaError("graph")
	}
	return nil
}

func validateNavigationMetadata(key navigationResourceKey, generation string, revision uint64, raw json.RawMessage) (any, error) {
	if !navigationSchemaIdentity(generation, false) || revision == 0 || revision > maxNavigationSafeInteger {
		return nil, navigationSchemaError("metadata")
	}
	validVersion := func(gotGeneration string, gotRevision uint64) bool {
		return gotGeneration == generation && gotRevision == revision
	}
	switch key.Kind {
	case navigationResourceManifest:
		var metadata hubapi.NavigationManifest
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "sources", "attentionSummary", "sections", "catalogs"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || !validateNavigationManifestRaw(raw) ||
			!navigationManifestValuesValid(metadata) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	case navigationResourceLive, navigationResourceNeedsYou, navigationResourcePinSection:
		var metadata navigationPagedMetadata
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "offset", "limit", "remaining", "truncated"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || metadata.Offset != key.Offset || metadata.Limit != key.Limit || !navigationCount(metadata.Remaining) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	case navigationResourcePinCatalog, navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		var metadata navigationCatalogMetadata
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "offset", "limit", "remaining"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || metadata.Offset != key.Offset || metadata.Limit != key.Limit || !navigationCount(metadata.Remaining) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	case navigationResourceProject:
		var metadata navigationProjectMetadata
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "key", "current_remaining", "recent_remaining", "archived_remaining", "truncated"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || metadata.Key != key.ProjectKey ||
			!navigationSchemaIdentity(metadata.Key, false) || !navigationCount(metadata.CurrentRemaining) ||
			!navigationCount(metadata.RecentRemaining) || !navigationCount(metadata.ArchivedRemaining) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	case navigationResourceProjectPage:
		var metadata navigationProjectPageMetadata
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "key", "tier", "offset", "limit", "remaining", "truncated"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || metadata.Key != key.ProjectKey || metadata.Tier != key.Tier ||
			metadata.Offset != key.Offset || metadata.Limit != key.Limit || !navigationCount(metadata.Remaining) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	case navigationResourceLocation:
		var metadata navigationLocationMetadata
		if strictNavigationDecode(raw, []string{"generation_id", "revision", "ref", "top_level_ref", "top_level"}, &metadata) != nil ||
			!validVersion(metadata.GenerationID, metadata.Revision) || metadata.Ref != key.ID ||
			!navigationSchemaIdentity(metadata.Ref, false) || !navigationSchemaIdentity(metadata.TopLevelRef, false) ||
			!navigationSchemaIdentity(metadata.ProjectKey, true) || !navigationSchemaIdentity(metadata.Tier, true) ||
			!navigationSchemaIdentity(metadata.PinSectionID, true) {
			return nil, navigationSchemaError("metadata")
		}
		return metadata, nil
	default:
		return nil, navigationSchemaError("metadata")
	}
}

func validateNavigationEntity(key navigationResourceKey, metadata any, entity hubapi.NavigationEntityRecord) (string, bool, error) {
	if !navigationEntityKeySyntax(key, entity.Key) {
		return "", false, navigationSchemaError("scope")
	}
	switch key.Kind {
	case navigationResourceManifest:
		return "", false, navigationSchemaError("entity")
	case navigationResourcePinCatalog:
		if entity.Kind != "pin_section" {
			return "", false, navigationSchemaError("entity")
		}
		var value hubapi.NavigationPinSectionDescriptor
		if strictNavigationDecode(entity.Value, []string{"id", "name", "count"}, &value) != nil || !navigationSchemaIdentity(value.ID, false) || !navigationIntCount(value.Count) || utf8.RuneCountInString(value.Name) > maxNavigationLabelRunes {
			return "", false, navigationSchemaError("entity")
		}
		return value.ID, false, nil
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		if entity.Kind != "project" {
			return "", false, navigationSchemaError("entity")
		}
		var value hubapi.NavigationProjectSummary
		if strictNavigationDecode(entity.Value, []string{"key", "name", "session_count"}, &value) != nil || !navigationProjectSummaryValid(value) {
			return "", false, navigationSchemaError("entity")
		}
		return value.Key, false, nil
	case navigationResourceProject:
		if entity.Kind == "project" {
			var value navigationProjectAnchor
			projectMetadata, ok := metadata.(navigationProjectMetadata)
			if !ok || strictNavigationDecode(entity.Value, []string{"key"}, &value) != nil || value.Key != projectMetadata.Key {
				return "", false, navigationSchemaError("entity")
			}
			return value.Key, true, nil
		}
		if entity.Kind != "session" {
			return "", false, navigationSchemaError("entity")
		}
	default:
		if entity.Kind != "session" {
			return "", false, navigationSchemaError("entity")
		}
	}
	var value hubapi.NavigationSessionSummary
	if strictNavigationDecode(entity.Value, []string{"ref", "host_id", "session_id", "title", "project", "state", "kind", "live", "children"}, &value) != nil || !navigationSessionValueValid(value) {
		return "", false, navigationSchemaError("entity")
	}
	return value.Ref, false, nil
}

func strictNavigationDecode(raw json.RawMessage, required []string, destination any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return navigationSchemaError("decode")
	}
	if navigationJSONContainsNull(fields) {
		return navigationSchemaError("decode")
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return navigationSchemaError("decode")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return navigationSchemaError("decode")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return navigationSchemaError("decode")
	}
	return nil
}

func navigationJSONContainsNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(value, &decoded) != nil {
			return true
		}
		return navigationJSONContainsNull(decoded)
	case map[string]json.RawMessage:
		for _, field := range value {
			if navigationJSONContainsNull(field) {
				return true
			}
		}
	case map[string]any:
		for _, field := range value {
			if navigationJSONContainsNull(field) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(value, navigationJSONContainsNull)
	}
	return false
}

func validateNavigationManifestRaw(raw json.RawMessage) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return false
	}
	if !navigationRawObjectExact(top["attentionSummary"], []string{"needsYou", "error", "working"}) ||
		!navigationRawObjectExact(top["sections"], []string{"live", "needs_you", "pin_sections"}) ||
		!navigationRawObjectExact(top["catalogs"], []string{"projects", "archived_projects", "test_runs"}) {
		return false
	}
	var sections, catalogs map[string]json.RawMessage
	if json.Unmarshal(top["sections"], &sections) != nil || json.Unmarshal(top["catalogs"], &catalogs) != nil {
		return false
	}
	for _, value := range sections {
		if !navigationRawObjectExact(value, []string{"count"}) {
			return false
		}
	}
	for _, value := range catalogs {
		if !navigationRawObjectExact(value, []string{"count"}) {
			return false
		}
	}
	var sources []json.RawMessage
	if json.Unmarshal(top["sources"], &sources) != nil || len(sources) > 64 {
		return false
	}
	for _, source := range sources {
		if !navigationRawObjectExact(source, []string{"id", "label", "kind", "online"}) {
			return false
		}
	}
	return true
}

func navigationRawObjectExact(raw json.RawMessage, fields []string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return false
		}
	}
	return true
}

func navigationManifestValuesValid(metadata hubapi.NavigationManifest) bool {
	for _, source := range metadata.Sources {
		if !navigationSchemaIdentity(source.ID, false) || !navigationSchemaIdentity(source.Kind, false) || utf8.RuneCountInString(source.Label) > maxNavigationLabelRunes {
			return false
		}
	}
	counts := []int{
		metadata.AttentionSummary.NeedsYou, metadata.AttentionSummary.Error, metadata.AttentionSummary.Working,
		metadata.Sections.Live.Count, metadata.Sections.NeedsYou.Count, metadata.Sections.PinSections.Count,
		metadata.Catalogs.Projects.Count, metadata.Catalogs.ArchivedProjects.Count, metadata.Catalogs.TestRuns.Count,
	}
	for _, count := range counts {
		if !navigationIntCount(count) {
			return false
		}
	}
	return true
}

func navigationSessionValueValid(value hubapi.NavigationSessionSummary) bool {
	if !navigationSchemaIdentity(value.Ref, false) || !navigationSchemaIdentity(value.HostID, false) ||
		!navigationSchemaIdentity(value.SessionID, false) || !navigationSchemaIdentity(value.State, false) ||
		!navigationSchemaIdentity(value.Kind, false) || len(value.Project) > maxNavigationIdentityBytes ||
		!utf8.ValidString(value.Project) || utf8.RuneCountInString(value.Title) > maxNavigationTitleRunes ||
		utf8.RuneCountInString(value.Branch) > maxNavigationLabelRunes || len(value.Children) != 0 ||
		!navigationIntCount(value.ClusterCount) || !navigationIntCount(value.MoreSubagents) ||
		!navigationIntCount(value.OmittedDescendants) {
		return false
	}
	for _, jobs := range []hubapi.NavigationArray[hubapi.NavigationJobSummary]{value.RunningJobs, value.CompletedJobs} {
		for _, job := range jobs {
			if !navigationSchemaIdentity(job.JobID, false) || !navigationSchemaIdentity(job.JobType, false) ||
				!navigationSchemaIdentity(job.Status, false) || utf8.RuneCountInString(job.Command) > maxNavigationLabelRunes ||
				utf8.RuneCountInString(job.Task) > maxNavigationLabelRunes || utf8.RuneCountInString(job.Reason) > maxNavigationLabelRunes ||
				utf8.RuneCountInString(job.Intent) > maxNavigationLabelRunes || utf8.RuneCountInString(job.FullCommand) > maxNavigationFullCommandRunes {
				return false
			}
		}
	}
	return true
}

func navigationProjectSummaryValid(value hubapi.NavigationProjectSummary) bool {
	counts := []int{value.RollupLive, value.RollupAttn, value.MoreCurrent, value.MoreRecent, value.MoreArchived, value.Worktrees, value.SessionCount}
	if !navigationSchemaIdentity(value.Key, false) || utf8.RuneCountInString(value.Name) > maxNavigationLabelRunes ||
		len(value.WorkingDir) > maxNavigationWorkingDirBytes || !utf8.ValidString(value.WorkingDir) ||
		utf8.RuneCountInString(value.RollupState) > maxNavigationLabelRunes {
		return false
	}
	for _, count := range counts {
		if !navigationIntCount(count) {
			return false
		}
	}
	return true
}

func navigationEntityKeySyntax(key navigationResourceKey, entityKey string) bool {
	prefix := navigationViewScope(key) + "/entity/"
	if len(entityKey) != len(prefix)+64 || entityKey[:len(prefix)] != prefix {
		return false
	}
	for _, value := range entityKey[len(prefix):] {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return false
		}
	}
	return true
}

func navigationSchemaIdentity(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return utf8.ValidString(value) && len(value) <= maxNavigationIdentityBytes
}

func navigationCount(value int64) bool {
	return value >= 0 && uint64(value) <= maxNavigationSafeInteger
}

func navigationIntCount(value int) bool {
	return value >= 0 && uint64(value) <= maxNavigationSafeInteger
}

func navigationExpectedRootSlot(kind navigationResourceKind) string {
	switch kind {
	case navigationResourceManifest:
		return "manifest"
	case navigationResourceLive, navigationResourceNeedsYou, navigationResourcePinSection, navigationResourceProjectPage:
		return "sessions"
	case navigationResourcePinCatalog:
		return "pin_sections"
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		return "projects"
	case navigationResourceLocation:
		return "session"
	default:
		return ""
	}
}

func navigationOwnedSlotAllowed(kind navigationResourceKind, entityKind string, anchor bool, slot string) bool {
	if entityKind == "session" {
		return slot == "children"
	}
	return kind == navigationResourceProject && anchor && (slot == "current" || slot == "recent" || slot == "archived")
}

func navigationHasExactSlots(actual map[string]bool, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, slot := range expected {
		if !actual[slot] {
			return false
		}
	}
	return true
}

func navigationRootChildrenWithinBounds(key navigationResourceKey, count int) bool {
	switch key.Kind {
	case navigationResourceLocation:
		return count <= 1
	case navigationResourceLive, navigationResourceNeedsYou, navigationResourcePinSection, navigationResourceProjectPage,
		navigationResourcePinCatalog, navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		return uint64(count) <= uint64(key.Limit)
	default:
		return count == 0
	}
}
