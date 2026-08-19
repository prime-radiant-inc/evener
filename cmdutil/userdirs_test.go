package cmdutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserConfigDirsUseXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	if got, want := DefaultConfigRoot(), filepath.Join(root, "evener"); got != want {
		t.Fatalf("DefaultConfigRoot = %q, want %q", got, want)
	}
	if got, want := DefaultSkillsDir(), filepath.Join(root, "evener", "skills"); got != want {
		t.Fatalf("DefaultSkillsDir = %q, want %q", got, want)
	}
	if got, want := DefaultPluginsRoot(), filepath.Join(root, "evener", "plugins"); got != want {
		t.Fatalf("DefaultPluginsRoot = %q, want %q", got, want)
	}
}

func TestEnsureUserConfigDirsCreatesExtensionRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	// checkLegacyDataDirs also compares the home state root and the XDG state
	// root; isolate both from the developer's real environment so this test
	// exercises a clean-install fixture rather than the host machine's
	// actual home directory (see TestEnsureUserConfigDirsRefusesStranded* in
	// legacymigration_test.go for the guard's own coverage).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("EVENER_STATE_DIR", "")

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(root, "evener"),
		filepath.Join(root, "evener", "skills"),
		filepath.Join(root, "evener", "plugins"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}
