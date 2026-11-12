package plugin

import (
	"testing"

	"primeradiant.com/evener/internal/testfs"
)

// fixtureRoot is t.TempDir() with symlinks resolved, for a fixture whose
// expectations are built from a plugin directory this package reports back.
//
// SkillSources resolves and absolutizes every directory it is given, because
// a symlinked plugin and its target have to collide under one name rather
// than load twice, so the source and the diagnostics it returns carry the
// resolved spelling. On macOS t.TempDir() hands back a path under
// /var/folders, which is a symlink to /private/var/folders, so a fixture that
// compares against the unresolved spelling compares two names for one file
// and fails on that machine alone. On Linux the two spellings are already
// identical and this is a no-op, which is why CI never saw it.
//
// The step itself is internal/testfs.CanonicalTempDir; this name only keeps
// the package's existing fixtures readable.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	return testfs.CanonicalTempDir(t)
}
