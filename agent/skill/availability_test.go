package skill

import (
	"slices"
	"testing"
)

func TestSessionAvailability(t *testing.T) {
	t.Parallel()
	available := SessionAvailability{MemorySaveAvailable: true}
	unavailable := SessionAvailability{}
	if !available.SkillAvailable(GardeningMemorySkillName) {
		t.Fatal("memory-capable session must be offered gardening-memory")
	}
	if unavailable.SkillAvailable(GardeningMemorySkillName) {
		t.Fatal("session without save tools must not be offered gardening-memory")
	}
	for _, name := range []string{"plain", "doctoring-evener", "plugin:skill"} {
		if !unavailable.SkillAvailable(name) {
			t.Fatalf("session availability narrowed unrelated skill %q", name)
		}
	}
}

func TestUserEntriesForSession(t *testing.T) {
	t.Parallel()
	catalog := Catalog{Entries: map[string]Descriptor{
		"alpha":                  {CatalogName: "alpha", Controls: InvocationControls{UserInvocable: true}},
		GardeningMemorySkillName: {CatalogName: GardeningMemorySkillName, Controls: InvocationControls{UserInvocable: true}},
		"zeta":                   {CatalogName: "zeta", Controls: InvocationControls{UserInvocable: true}},
	}}
	names := func(entries []Descriptor) []string {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.CatalogName)
		}
		return out
	}
	if got := names(catalog.UserEntriesForSession(SessionAvailability{})); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Fatalf("without save tools = %v, want canonical order without gardening-memory", got)
	}
	if got := names(catalog.UserEntriesForSession(SessionAvailability{MemorySaveAvailable: true})); !slices.Equal(got, []string{"alpha", GardeningMemorySkillName, "zeta"}) {
		t.Fatalf("with save tools = %v, want every user entry in canonical order", got)
	}
}
