// Package hubtest provides fixture helpers shared by tests across
// cmd/serf-hub and its internal packages.
//
// It exists because the identifiers a hub fixture has to spell out are
// encodings, not free-form names, and the failure mode when one is wrong is
// silence: PastIndex.Rebuild refuses to index a project directory or session
// meta whose id fails validation, so a fixture seeded with a plausible-looking
// placeholder is invisible to every reader rather than rejected out loud. A
// session id is a 22-character base62 UUIDv7 payload; a project directory is
// <readable>-<10 base62>. Mint them here instead of writing them by hand.
//
// Both helpers DELEGATE to package identifier rather than reproducing its
// encodings. That is a rule, not a preference: identifier_audit_test.go fails
// the build on a second implementation of project-id construction anywhere in
// the tracked tree, and a fixture that spelled the format out itself would be
// free to drift from the format the hub actually enforces - which is the exact
// class of bug this package exists to stop.
package hubtest

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/identifier"
)

// SessionID returns a fresh session id that passes
// identifier.ValidateSessionID, for naming a seeded session meta.
func SessionID(t *testing.T) string {
	t.Helper()
	id, err := identifier.NewSessionID()
	if err != nil {
		t.Fatalf("hubtest.SessionID: mint session id: %v", err)
	}
	return id
}

// ProjectDir creates a project state directory under projectsRoot and returns
// its path. The directory's name is a real project id, obtained by resolving a
// stand-in checkout named readable - the same call the hub itself makes - so a
// fixture never has to know the id's shape.
//
// A checkout is involved because a project id is DERIVED from a canonical path
// rather than chosen: identifier.ResolveProject requires the directory to
// exist and takes the readable portion from its name. The stand-in lives in
// the test's own temp dir, so distinct readable names yield distinct ids and
// two fixtures cannot collide.
func ProjectDir(t *testing.T, projectsRoot, readable string) string {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), readable)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatalf("hubtest.ProjectDir: create stand-in checkout %s: %v", checkout, err)
	}
	project, err := identifier.ResolveProject(checkout)
	if err != nil {
		t.Fatalf("hubtest.ProjectDir: resolve %s: %v", checkout, err)
	}
	if err := identifier.ValidateProjectID(project.ID); err != nil {
		t.Fatalf("hubtest.ProjectDir(%q) resolved invalid id %q: %v", readable, project.ID, err)
	}
	dir := filepath.Join(projectsRoot, project.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("hubtest.ProjectDir: create %s: %v", dir, err)
	}
	return dir
}
