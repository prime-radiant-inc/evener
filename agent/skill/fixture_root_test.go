package skill

import (
	"path/filepath"
	"testing"
)

// fixtureRoot is t.TempDir() with symlinks resolved, for a fixture whose
// expectations are built from a directory discovery reports back.
//
// projectSkillDirs resolves the working directory before it scans it, so a
// skill found under the project root comes back with the resolved spelling.
// On macOS t.TempDir() hands back a path under /var/folders, which is a
// symlink to /private/var/folders, so a fixture that compares against the
// unresolved spelling compares two names for one file and fails on that
// machine alone. On Linux the two spellings are already identical and this is
// a no-op, which is why CI never saw it.
//
// The directories discovery takes as given -- a home, a user skills
// directory, an extra directory, a plugin's own directory -- are reported
// back as they were passed, and fixtures for those keep using t.TempDir().
// Package agent has the same helper under the name skillFixtureRoot: a test
// helper cannot be shared across packages.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}
