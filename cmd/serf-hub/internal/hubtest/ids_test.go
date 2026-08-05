package hubtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

// The point of these helpers is that their output survives the validation
// PastIndex.Rebuild applies, so each case asserts against the real validator
// rather than against a hand-copied description of the encoding. Nothing here
// pins the id's SHAPE - that belongs to package identifier, and a fixture
// asserting it would be the second implementation identifier_audit_test.go
// exists to forbid.

func TestSessionIDIsValidAndUnique(t *testing.T) {
	first := SessionID(t)
	if err := identifier.ValidateSessionID(first); err != nil {
		t.Fatalf("SessionID() = %q, ValidateSessionID: %v", first, err)
	}
	if second := SessionID(t); second == first {
		t.Fatalf("SessionID() returned %q twice; ids must be distinct", first)
	}
}

func TestProjectDirIsNamedByAValidProjectID(t *testing.T) {
	projects := t.TempDir()
	dir := ProjectDir(t, projects, "alpha")

	if got := filepath.Dir(dir); got != projects {
		t.Fatalf("ProjectDir() = %q, want a child of %q", dir, projects)
	}
	if err := identifier.ValidateProjectID(filepath.Base(dir)); err != nil {
		t.Fatalf("ProjectDir() = %q, ValidateProjectID(%q): %v", dir, filepath.Base(dir), err)
	}
	// Rebuild walks the projects root, so the directory has to be on disk -
	// returning a path it never created would seed an invisible fixture, which
	// is the failure this package exists to prevent.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("ProjectDir() = %q, not created: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("ProjectDir() = %q, want a directory", dir)
	}
}

func TestProjectDirKeepsTheReadableNameFindable(t *testing.T) {
	// Not an assertion about the encoding - only that a human reading a failure
	// can tell which fixture a directory belongs to.
	dir := ProjectDir(t, t.TempDir(), "checkout-alpha")
	if !strings.Contains(filepath.Base(dir), "checkout-alpha") {
		t.Fatalf("ProjectDir(..., %q) = %q, want the readable name to appear in it", "checkout-alpha", filepath.Base(dir))
	}
}

func TestProjectDirGivesDistinctNamesDistinctIDs(t *testing.T) {
	projects := t.TempDir()
	first := ProjectDir(t, projects, "one")
	second := ProjectDir(t, projects, "two")
	if first == second {
		t.Fatalf("ProjectDir returned %q for two different names; fixtures would collide", first)
	}
}

// A readable name longer than the id's 80-byte ceiling must still yield a valid
// id. The truncation is package identifier's to own; this only pins that the
// helper does not hand back something Rebuild would silently skip.
func TestProjectDirSurvivesAnOverlongReadableName(t *testing.T) {
	dir := ProjectDir(t, t.TempDir(), strings.Repeat("x", 200))
	id := filepath.Base(dir)
	if err := identifier.ValidateProjectID(id); err != nil {
		t.Fatalf("ProjectDir(200 bytes) = %q, ValidateProjectID: %v", id, err)
	}
}
