package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// TestPastThreadSkillCatalogHonorsSessionMemoryAvailability proves the hub's
// real past-thread/composer catalog applies the same session-scoped skill
// availability activation enforces. A session that cannot save memory must not
// be offered gardening-memory, while a memory-capable root still sees it and
// every unrelated skill keeps its place.
func TestPastThreadSkillCatalogHonorsSessionMemoryAvailability(t *testing.T) {
	_, base := seedPastSessionWithSkillFixtures(t)
	for _, tc := range []struct {
		name    string
		mutate  func(*schema.SessionMeta)
		offered bool
	}{
		{"memory-capable-root", nil, true},
		{"delegate", func(meta *schema.SessionMeta) { meta.IsSubagent = true }, false},
		{"memory-disabled", func(meta *schema.SessionMeta) { meta.Config.DisableMemory = true }, false},
		{"save-tool-denied-root", func(meta *schema.SessionMeta) { meta.MemorySaveUnavailable = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := base
			if tc.mutate != nil {
				tc.mutate(&entry.Meta)
			}
			catalog := discoverPastThreadSkills(entry)
			if got := hasSkill(catalog.Skills, "gardening-memory"); got != tc.offered {
				t.Fatalf("gardening-memory offered = %t, want %t; catalog = %+v", got, tc.offered, catalog.Skills)
			}
			if !hasSkill(catalog.Skills, "project-skill") {
				t.Fatalf("unrelated project skill dropped from the catalog: %+v", catalog.Skills)
			}
		})
	}

	// The attachment thread/read uses must apply the same filter, not only the
	// raw catalog function.
	denied := base
	denied.Meta.MemorySaveUnavailable = true
	thread := attachPastThreadSkillCatalog(denied, appwire.Thread{ID: denied.Meta.ID})
	if thread.Evener.Diagnostics == nil || hasSkill(thread.Evener.Diagnostics.Skills, "gardening-memory") {
		t.Fatalf("attached past-thread diagnostics still offer gardening-memory: %+v", thread.Evener.Diagnostics)
	}
}
