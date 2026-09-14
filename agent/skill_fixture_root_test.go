package agent

import (
	"path/filepath"
	"testing"
)

// skillFixtureRoot is t.TempDir() with symlinks resolved, for fixtures whose
// expectations are built from the directory they hand the session.
//
// Skill discovery reports the paths it scanned, and it scans resolved
// directories: projectSkillDirs runs filepath.EvalSymlinks over the working
// directory, and plugin.SkillSources does the same for each plugin directory,
// because a symlinked plugin and its target have to collide under one name
// rather than load twice. So every skill source that comes back — an
// envelope's Source and BaseDirectory, a diagnostic's Source — is the
// resolved spelling.
//
// On macOS t.TempDir() hands back a path under /var/folders, which is a
// symlink to /private/var/folders, so a fixture comparing against the
// unresolved spelling compares two names for one file and fails on that
// machine alone. On Linux the two spellings are already identical and this is
// a no-op, which is why CI never saw it.
func skillFixtureRoot(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}
