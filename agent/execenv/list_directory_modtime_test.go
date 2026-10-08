package execenv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Both directory walks report each file's modification time, which the
// memory index uses to order pages that carry no updated stamp.
func TestListDirectoryReportsModTime(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	seed := func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "sub", "page.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, entries []DirEntry) {
		t.Helper()
		for _, entry := range entries {
			if entry.Name == filepath.Join("sub", "page.md") {
				if !entry.ModTime.Equal(stamp) {
					t.Fatalf("ModTime=%v want %v", entry.ModTime, stamp)
				}
				return
			}
		}
		t.Fatalf("page missing from %+v", entries)
	}
	t.Run("confined", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		env, err := NewConfinedFileEnvironment(root, "scope")
		if err != nil {
			t.Fatal(err)
		}
		defer env.Cleanup()
		seed(t, filepath.Join(root, "scope"))
		entries, err := env.ListDirectory(env.WorkingDirectory(), 4)
		if err != nil {
			t.Fatal(err)
		}
		check(t, entries)
	})
	t.Run("unsandboxed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		seed(t, dir)
		entries, err := NewLocalExecutionEnvironment(dir).ListDirectory(dir, 4)
		if err != nil {
			t.Fatal(err)
		}
		check(t, entries)
	})
}
