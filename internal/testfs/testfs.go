// Package testfs holds filesystem fixtures shared by command tests.
package testfs

import (
	"path/filepath"
	"testing"
)

// CanonicalTempDir returns a fresh t.TempDir by its canonical path: symlinks
// resolved, so a path the test builds from it matches the path production
// canonicalizes. On macOS /var is a symlink to /private/var and the two
// spellings never match (#2497).
//
// This is the one implementation of that fixture step. Command tests used to
// carry three spellings of it (#4426); a local copy is drift by construction,
// so `make lint`'s canonical-temp-dir-one-helper check fails one.
func CanonicalTempDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}
