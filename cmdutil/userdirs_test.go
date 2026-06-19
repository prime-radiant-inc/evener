package cmdutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserConfigDirsUseXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	if got, want := DefaultConfigRoot(), filepath.Join(root, "serf"); got != want {
		t.Fatalf("DefaultConfigRoot = %q, want %q", got, want)
	}
	if got, want := DefaultSkillsDir(), filepath.Join(root, "serf", "skills"); got != want {
		t.Fatalf("DefaultSkillsDir = %q, want %q", got, want)
	}
	if got, want := DefaultPluginsRoot(), filepath.Join(root, "serf", "plugins"); got != want {
		t.Fatalf("DefaultPluginsRoot = %q, want %q", got, want)
	}
}

func TestEnsureUserConfigDirsCreatesExtensionRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(root, "serf"),
		filepath.Join(root, "serf", "skills"),
		filepath.Join(root, "serf", "plugins"),
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
