package hub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Navigation resource keys, canonicalization, view scoping, entity/container
// key derivation, and ETags are shared by the v2 read path, delta history,
// and normalization. They live here after the v1 representation cache was
// removed.

// canonical returns the representation identity after applying the same
// effective limits as the projector. Fields irrelevant to a resource kind are
// cleared so equivalent route forms share one entry.
func (key navigationResourceKey) canonical() navigationResourceKey {
	canonical := navigationResourceKey{
		Kind:       key.Kind,
		Generation: key.Generation,
		Revision:   key.Revision,
	}
	switch key.Kind {
	case navigationResourceManifest:
		return canonical
	case navigationResourceLive, navigationResourceNeedsYou:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourcePinCatalog:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationCatalogRows)
	case navigationResourcePinSection:
		canonical.SectionID = key.SectionID
		if canonical.SectionID == "" {
			canonical.SectionID = key.ID
		}
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationCatalogRows)
	case navigationResourceProject:
		canonical.ProjectKey = key.ProjectKey
	case navigationResourceProjectPage:
		canonical.ProjectKey = key.ProjectKey
		canonical.Tier = key.Tier
		canonical.Offset = key.Offset
		canonical.Limit = canonicalNavigationLimit(key.Limit, maxNavigationSectionRows)
	case navigationResourceLocation:
		canonical.ID = key.ID
	default:
		return key
	}
	return canonical
}

// View returns the complete selector identity for a normalized resource. It
// excludes only the publication generation and revision.
func (key navigationResourceKey) View() navigationResourceKey {
	view := key.canonical()
	view.Generation = ""
	view.Revision = 0
	return view
}

func navigationViewScope(key navigationResourceKey) string {
	view := key.View()
	encode := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf(
		"nav2/%s/%s/%s/%s/%s/%d/%d",
		view.Kind,
		encode([]byte(view.ID)),
		encode([]byte(view.SectionID)),
		encode([]byte(view.ProjectKey)),
		encode([]byte(view.Tier)),
		view.Offset,
		view.Limit,
	)
}

func navigationEntityKey(key navigationResourceKey, kind, identity string) string {
	localID := sha256.Sum256([]byte(kind + "\x00" + identity))
	return navigationViewScope(key) + "/entity/" + hex.EncodeToString(localID[:])
}

func navigationRootContainerKey(key navigationResourceKey, slot string) string {
	return navigationViewScope(key) + "/root/" + slot
}

func navigationOwnedContainerKey(entityKey, slot string) string {
	return entityKey + "/" + slot
}

func canonicalNavigationLimit(limit, maximum uint32) uint32 {
	if limit == 0 || limit > maximum {
		return maximum
	}
	return limit
}

func validNavigationETagGeneration(generation string) bool {
	if generation == "" {
		return false
	}
	for _, character := range []byte(generation) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

// String is a collision-safe canonical encoding. JSON supplies explicit field
// boundaries and deterministic struct-field ordering; raw IDs never become
// delimiters or metric labels.
func (key navigationResourceKey) String() string {
	key = key.canonical()
	identity := struct {
		Kind       navigationResourceKind `json:"kind"`
		ID         string                 `json:"id,omitempty"`
		SectionID  string                 `json:"section_id,omitempty"`
		ProjectKey string                 `json:"project_key,omitempty"`
		Tier       string                 `json:"tier,omitempty"`
		Offset     uint32                 `json:"offset,omitempty"`
		Limit      uint32                 `json:"limit,omitempty"`
		Generation string                 `json:"generation"`
		Revision   uint64                 `json:"revision"`
	}{
		Kind: key.Kind, ID: key.ID, SectionID: key.SectionID,
		ProjectKey: key.ProjectKey, Tier: key.Tier, Offset: key.Offset,
		Limit: key.Limit, Generation: key.Generation, Revision: key.Revision,
	}
	encoded, _ := json.Marshal(identity)
	return string(encoded)
}

func navigationETag(key navigationResourceKey, generation string, revision uint64) string {
	return fmt.Sprintf(`W/"nav-%s-%x-%d"`, generation, sha256.Sum256([]byte(key.String())), revision)
}
