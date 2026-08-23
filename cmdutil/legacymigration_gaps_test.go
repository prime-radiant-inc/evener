package cmdutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckLegacyDataDirsAlreadyMigrated covers the branch where the
// legacy directory exists AND the target directory also exists (line
// 96-97: already migrated -> continue), so checkLegacyDataDirs returns
// nil instead of erroring.
func TestCheckLegacyDataDirsAlreadyMigrated(t *testing.T) {
	_, configHome, stateHome := setEveneryTestEnv(t)

	// Create both legacy and target in config root.
	legacyConfig := filepath.Join(configHome, "serf")
	targetConfig := filepath.Join(configHome, "evener")
	if err := os.MkdirAll(legacyConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetConfig, 0o700); err != nil {
		t.Fatal(err)
	}

	// Create both legacy and target in state root.
	legacyState := filepath.Join(stateHome, "serf")
	targetState := filepath.Join(stateHome, "evener")
	if err := os.MkdirAll(legacyState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetState, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := checkLegacyDataDirs(); err != nil {
		t.Fatalf("checkLegacyDataDirs should return nil when already migrated: %v", err)
	}
}
