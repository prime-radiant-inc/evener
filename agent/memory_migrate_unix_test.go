//go:build unix

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// A page that exists but can't be written keeps its description only in the
// index, so the index stays until a later run can write it.
func TestMigrateMemoryScopeKeepsIndexWhenAPageWriteFails(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
	index := filepath.Join(scope, "MEMORY.md")
	dir := filepath.Join(scope, "locked")
	page := filepath.Join(dir, "p.md")
	if err := os.WriteFile(index, []byte("- [locked](locked/p.md) — described by the index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Writes replace the page through a temp file in its directory, so a
	// read-only directory is what makes the write fail.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := migrateMemoryScope(env); err == nil {
		t.Fatal("migration of an unwritable page returned nil")
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("MEMORY.md should stay for the next run: %v", err)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(page)
	if err != nil || string(raw) != "---\ndescription: described by the index\n---\nbody\n" {
		t.Fatalf("p.md=%q, %v", raw, err)
	}
	if _, err := os.Stat(index); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MEMORY.md still present: %v", err)
	}
}
