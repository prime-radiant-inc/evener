package hub

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
)

// TestHubSpawnSlashCatalogOffersGardeningMemoryToFreshRoot pins the pre-session
// side of the shared availability seam: a fresh hub root gets the default memory
// binding and every save tool, so its catalog still offers the bundled skill.
func TestHubSpawnSlashCatalogOffersGardeningMemoryToFreshRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cwd := t.TempDir()
	writeSlashCatalogSkill(t, filepath.Join(cwd, "skills"), "project-skill", "project-skill", "Project skill")
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{CWD: cwd})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	byName := make(map[string]appwire.EvenerSkillInfo, len(resp.Skills))
	for _, entry := range resp.Skills {
		byName[entry.Name] = entry
	}
	gardening, ok := byName["gardening-memory"]
	if !ok {
		t.Fatalf("fresh-root catalog does not offer gardening-memory: %+v", resp.Skills)
	}
	if !gardening.Available || !gardening.UserInvocable || gardening.DisableModelInvocation {
		t.Fatalf("gardening-memory flags = {Available:%t UserInvocable:%t DisableModelInvocation:%t}, want offered", gardening.Available, gardening.UserInvocable, gardening.DisableModelInvocation)
	}
	if _, ok := byName["project-skill"]; !ok {
		t.Fatalf("fresh-root catalog dropped the project skill: %+v", resp.Skills)
	}
}

// TestFreshSpawnSkillAvailabilityHasNoLaunchMemoryControl guards the invariant
// behind freshSpawnSkillAvailability. If launch configuration gains a memory
// control, the helper must derive from it instead of returning a fixed true.
func TestFreshSpawnSkillAvailabilityHasNoLaunchMemoryControl(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(launchconfig.Layer{}),
		reflect.TypeOf(appwire.LaunchConfigLayer{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			if name := typ.Field(i).Name; strings.Contains(name, "Memory") {
				t.Fatalf("%s gained memory control %q; freshSpawnSkillAvailability must derive from it", typ, name)
			}
		}
	}
	if got := freshSpawnSkillAvailability(); !got.MemorySaveAvailable {
		t.Fatalf("fresh spawn availability = %+v, want memory save available", got)
	}
}
