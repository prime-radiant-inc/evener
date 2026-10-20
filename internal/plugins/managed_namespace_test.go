package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A failed marker write must leave a clean store's content unchanged, rather
// than publishing a Git-named namespace under the earlier clean marker.
func TestGitNamespacePublicationNeedsWritableCleanMarker(t *testing.T) {
	t.Parallel()
	m := NewManager(t.TempDir())
	var warnings bytes.Buffer
	m.Stderr = &warnings
	if err := m.writeManagedContentState(managedContentClean); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "staging")
	writeTestFile(t, filepath.Join(staging, "companion.bin"), "preserved")
	if err := os.Chmod(m.Root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(m.Root, 0o755); err != nil {
			t.Error(err)
		}
	})
	probe := filepath.Join(m.Root, "permission-probe")
	if err := os.WriteFile(probe, nil, 0o600); err == nil {
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
		t.Skip("filesystem permits writes to a read-only directory")
	} else if !os.IsPermission(err) {
		t.Fatal(err)
	}
	final, err := m.commitStaged("acme", ".git", staging, "revision")
	if err == nil || final != "" {
		t.Errorf("publication with unwritable marker returned path %q, error %v", final, err)
	}
	if _, err := os.Lstat(filepath.Join(m.cacheDir(), "acme", ".git")); !os.IsNotExist(err) {
		t.Errorf("publication introduced a namespace under the old clean marker: %v", err)
	}
	if !m.managedContentClean() {
		t.Error("failed publication changed the previous clean state")
	}
}
