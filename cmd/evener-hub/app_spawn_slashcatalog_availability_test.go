package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
// behind freshSpawnSkillAvailability, which the memory_enabled launch control
// now exercises: the helper derives from the resolved launch layer, and the
// control rides argv only — env stays clean of it.
func TestFreshSpawnSkillAvailabilityDerivesFromLaunchMemory(t *testing.T) {
	for _, tc := range []struct {
		name string
		eff  launchconfig.Layer
		want bool
	}{
		{"unset (builtin default on)", launchconfig.Layer{}, true},
		{"explicit on", launchconfig.Layer{MemoryEnabled: new(true)}, true},
		{"off", launchconfig.Layer{MemoryEnabled: new(false)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := freshSpawnSkillAvailability(tc.eff); got.MemorySaveAvailable != tc.want {
				t.Fatalf("freshSpawnSkillAvailability = %+v, want MemorySaveAvailable %t", got, tc.want)
			}
		})
	}
	// The control rides argv (the --disable-memory token), never env.
	args := launchconfig.ToArgs(launchconfig.Resolved{Effective: launchconfig.Layer{MemoryEnabled: new(false)}})
	if !slices.ContainsFunc(args, func(arg string) bool { return arg == "--disable-memory=true" }) {
		t.Fatalf("launch argv %v, want the --disable-memory=true token", args)
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		RunDir:   "/run",
		StateDir: "/state",
		HubToken: "token",
	})
	for _, entry := range env {
		if strings.Contains(strings.ToLower(entry), "memory") {
			t.Fatalf("launch env gained memory control %q; the control rides argv", entry)
		}
	}
}

// TestHubSpawnSlashCatalogHidesGardeningMemoryWhenLaunchDisables: a
// or the composer would offer a skill the started session cannot load.
func TestHubSpawnSlashCatalogHidesGardeningMemoryWhenLaunchDisables(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cwd := t.TempDir()
	writeSlashCatalogSkill(t, filepath.Join(cwd, "skills"), "project-skill", "project-skill", "Project skill")
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             cwd,
		LaunchOverrides: &appwire.LaunchConfigLayer{MemoryEnabled: new(false)},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	for _, entry := range resp.Skills {
		if entry.Name == "gardening-memory" {
			t.Fatalf("memory-disabled launch still offers gardening-memory: %+v", resp.Skills)
		}
	}
	if _, ok := skillEntryByName(resp.Skills, "project-skill"); !ok {
		t.Fatalf("memory-disabled launch dropped the project skill: %+v", resp.Skills)
	}
}

// TestHubSpawnSlashCatalogHidesGardeningMemoryUnderGlobalDefault: a saved
// global memory_enabled = false layer hides the skill too, without any
// per-launch override. The file's key spelling is pinned by writing the TOML
// bytes directly.
func TestHubSpawnSlashCatalogHidesGardeningMemoryUnderGlobalDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	launchRoot := t.TempDir()
	cwd := t.TempDir()
	if err := os.MkdirAll(launchRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchRoot, "launch.toml"), []byte("memory_enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSlashCatalogSkill(t, filepath.Join(cwd, "skills"), "project-skill", "project-skill", "Project skill")
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{LaunchConfigRoot: launchRoot}, appwire.SpawnSlashCatalogParams{CWD: cwd})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	for _, entry := range resp.Skills {
		if entry.Name == "gardening-memory" {
			t.Fatalf("globally memory-disabled catalog still offers gardening-memory: %+v", resp.Skills)
		}
	}
	if _, ok := skillEntryByName(resp.Skills, "project-skill"); !ok {
		t.Fatalf("globally memory-disabled catalog dropped the project skill: %+v", resp.Skills)
	}
}

func skillEntryByName(skills []appwire.EvenerSkillInfo, name string) (appwire.EvenerSkillInfo, bool) {
	for _, entry := range skills {
		if entry.Name == name {
			return entry, true
		}
	}
	return appwire.EvenerSkillInfo{}, false
}
