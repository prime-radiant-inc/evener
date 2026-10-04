package main

import (
	"path/filepath"
	"testing"
)

// resolvedTempDir returns a fresh t.TempDir with symlinks resolved, for a test
// that compares against a path evener canonicalizes: on macOS /var is a symlink
// to /private/var, and the two spellings never match (#2497).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(t.TempDir()): %v", err)
	}
	return dir
}
