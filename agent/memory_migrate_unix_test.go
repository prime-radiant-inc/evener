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
	page := filepath.Join(scope, "locked.md")
	if err := os.WriteFile(index, []byte("- [locked](locked.md) — described by the index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("body\n"), 0o444); err != nil {
		t.Fatal(err)
	}

	if err := migrateMemoryScope(env); err == nil {
		t.Fatal("migration of an unwritable page returned nil")
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("MEMORY.md should stay for the next run: %v", err)
	}

	if err := os.Chmod(page, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(page)
	if err != nil || string(raw) != "---\ndescription: described by the index\n---\nbody\n" {
		t.Fatalf("locked.md=%q, %v", raw, err)
	}
	if _, err := os.Stat(index); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MEMORY.md still present: %v", err)
	}
}
