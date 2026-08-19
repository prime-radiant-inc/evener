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

func TestEnsureUserConfigDirsProceedsWhenNoLegacyDataExists(t *testing.T) {
	setEveneryTestEnv(t)

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs should proceed on a clean install: %v", err)
	}
}

// TestEnsureUserConfigDirsRefusesStrandedLegacyHomeRoot covers the legacy
// (~/.serf) home root: it pre-rename held providers.toml/credentials.toml/
// auth-token/hub.lock/index.db/run, none of which have a whole-directory
// destination to compare against any more (they split across the config and
// state roots) — see homeRootFiles / firstUnmigratedHomeRootFile.
func TestEnsureUserConfigDirsRefusesStrandedLegacyHomeRoot(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	legacy := filepath.Join(home, ".serf")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "index.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := EnsureUserConfigDirs()
	if err == nil {
		t.Fatal("expected an error when the legacy home root holds unmigrated data")
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("error %q does not name the legacy path %q", err, legacy)
	}
	if !strings.Contains(err.Error(), "evener-migrate") {
		t.Errorf("error %q does not mention evener-migrate", err)
	}
}

// TestEnsureUserConfigDirsRefusesStrandedInterimEvenerRoot covers Jesse's
// machine: the interim (~/.evener) home root, the post-rename/
// pre-consolidation location for the same files.
func TestEnsureUserConfigDirsRefusesStrandedInterimEvenerRoot(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	interim := filepath.Join(home, ".evener")
	if err := os.MkdirAll(interim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "auth-token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := EnsureUserConfigDirs()
	if err == nil {
		t.Fatal("expected an error when the interim ~/.evener root holds unmigrated data")
	}
	if !strings.Contains(err.Error(), interim) {
		t.Errorf("error %q does not name the interim path %q", err, interim)
	}
	if !strings.Contains(err.Error(), "evener-migrate") {
		t.Errorf("error %q does not mention evener-migrate", err)
	}
}

// TestEnsureUserConfigDirsProceedsWhenHomeRootsAreFullyMigrated covers the
// post-evener-migrate state: both home roots still exist (evener-migrate
// only removes them once truly empty; a stray unrelated file may remain) but
// hold none of the known homeRootFiles, so there is nothing left to strand.
func TestEnsureUserConfigDirsProceedsWhenHomeRootsAreFullyMigrated(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".serf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".evener"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs should proceed once both home roots are emptied of known files: %v", err)
	}
}

// TestEnsureUserConfigDirsIgnoresStateDirOverride locks in the retirement of
// EVENER_STATE_DIR as an override for this check: unlike the old ~/.evener
// home-root pair (which the previous guard skipped entirely when
// EVENER_STATE_DIR was set), the split config/state destinations are governed
// by XDG_CONFIG_HOME/XDG_STATE_HOME only, so an unrelated EVENER_STATE_DIR
// value must not bypass the guard.
func TestEnsureUserConfigDirsIgnoresStateDirOverride(t *testing.T) {
	home, _, _ := setEveneryTestEnv(t)
	interim := filepath.Join(home, ".evener")
	if err := os.MkdirAll(interim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interim, "index.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_STATE_DIR", filepath.Join(t.TempDir(), "custom-state"))

	if err := EnsureUserConfigDirs(); err == nil {
		t.Fatal("EVENER_STATE_DIR must not bypass the home-root guard")
	}
}
