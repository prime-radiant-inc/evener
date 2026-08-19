package cmdutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setEveneryTestEnv points HOME, XDG_CONFIG_HOME, and XDG_STATE_HOME at fresh,
// empty subdirectories of a t.TempDir() root, and clears EVENER_STATE_DIR.
// Every test below starts from this clean baseline and then seeds only the
// legacy/target directories it means to test, so a directory's mere absence
// is never accidentally sourced from the developer's real home.
func setEveneryTestEnv(t *testing.T) (home, configHome, stateHome string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	configHome = filepath.Join(root, "config")
	stateHome = filepath.Join(root, "state")
	for _, dir := range []string{home, configHome, stateHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("EVENER_STATE_DIR", "")
	return home, configHome, stateHome
}

func TestEnsureUserConfigDirsRefusesStrandedLegacyStateRoot(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	legacy := filepath.Join(home, ".serf")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	err := EnsureUserConfigDirs()
	if err == nil {
		t.Fatal("expected an error when the legacy state root exists and the new one does not")
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("error %q does not name the legacy path %q", err, legacy)
	}
	if !strings.Contains(err.Error(), "evener-migrate") {
		t.Errorf("error %q does not mention evener-migrate", err)
	}
}

func TestEnsureUserConfigDirsRefusesStrandedLegacyConfigRoot(t *testing.T) {
	_, configHome, _ := setEveneryTestEnv(t)
	legacy := filepath.Join(configHome, "serf")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	err := EnsureUserConfigDirs()
	if err == nil {
		t.Fatal("expected an error when the legacy config root exists and the new one does not")
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("error %q does not name the legacy path %q", err, legacy)
	}
	if !strings.Contains(err.Error(), "evener-migrate") {
		t.Errorf("error %q does not mention evener-migrate", err)
	}
}

func TestEnsureUserConfigDirsRefusesStrandedLegacyXDGStateRoot(t *testing.T) {
	_, _, stateHome := setEveneryTestEnv(t)
	legacy := filepath.Join(stateHome, "serf")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	err := EnsureUserConfigDirs()
	if err == nil {
		t.Fatal("expected an error when the legacy XDG state root exists and the new one does not")
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("error %q does not name the legacy path %q", err, legacy)
	}
	if !strings.Contains(err.Error(), "evener-migrate") {
		t.Errorf("error %q does not mention evener-migrate", err)
	}
}

func TestEnsureUserConfigDirsProceedsWhenBothLegacyAndNewStateRootExist(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".serf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".evener"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs should proceed once the new state root already exists: %v", err)
	}
}

func TestEnsureUserConfigDirsProceedsWhenNoLegacyDataExists(t *testing.T) {
	setEveneryTestEnv(t)

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs should proceed on a clean install: %v", err)
	}
}

func TestEnsureUserConfigDirsSkipsStateRootCheckWhenStateDirOverridden(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".serf"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_STATE_DIR", filepath.Join(t.TempDir(), "custom-state"))

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs should not gate the home state root when EVENER_STATE_DIR overrides it: %v", err)
	}
}
