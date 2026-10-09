//go:build unix

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A page that exists but can't be written keeps its description only in the
// index, so the index stays until a later run can write it. Pages after the
// failed one still get their descriptions.
func TestMigrateMemoryScopeKeepsIndexWhenAPageWriteFails(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	env, scope := newMemoryMigrateScope(t)
	index := filepath.Join(scope, "MEMORY.md")
	dir := filepath.Join(scope, "locked")
	page := filepath.Join(dir, "p.md")
	later := filepath.Join(scope, "z.md")
	if err := os.WriteFile(index, []byte("- [locked](locked/p.md) — described by the index\n- [later](z.md) — a later page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(later, []byte("body\n"), 0o600); err != nil {
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
	if raw, err := os.ReadFile(later); err != nil || string(raw) != "---\ndescription: a later page\n---\nbody\n" {
		t.Fatalf("a page after the failed one: z.md=%q, %v", raw, err)
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

// A FIFO named like a page, linked from the old index, must not block
// migration: only regular files are read.
func TestMigrateMemoryScopeSkipsAFIFOPage(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	index := filepath.Join(scope, "MEMORY.md")
	if err := os.WriteFile(index, []byte("- [pipe](x.md) — a fifo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(scope, "x.md"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- migrateMemoryScope(env) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	// TRIPWIRE: migration of a FIFO page returns in milliseconds; without a bound a regressed FIFO read would hang the whole test binary.
	case <-time.After(10 * time.Second):
		t.Fatal("migration blocked on a FIFO page")
	}
	if _, err := os.Stat(index); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MEMORY.md still present: %v", err)
	}
}

// A root index that is not a regular file (here a FIFO) is no index, as a
// page that is not a regular file is no page: migration neither blocks on it
// nor fails on it every run.
func TestMigrateMemoryScopeSkipsAFIFOIndex(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	if err := syscall.Mkfifo(filepath.Join(scope, "MEMORY.md"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- migrateMemoryScope(env) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	// TRIPWIRE: migration past a FIFO index returns in milliseconds; without a bound a regressed FIFO read would hang the whole test binary.
	case <-time.After(10 * time.Second):
		t.Fatal("migration blocked on a FIFO index")
	}
}

// Any entry at a backup name, a symlink included (even one whose target is
// gone), occupies it: migration moves on to the next free name and leaves the
// entry alone.
func TestMigrateMemoryScopeTreatsASymlinkBackupAsTaken(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	link := filepath.Join(scope, memoryLegacyIndexBackup)
	if err := os.Symlink("gone", link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte("- [a](a.md) — index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != "gone" {
		t.Fatalf("the symlink backup was replaced: %q, %v", target, err)
	}
	if raw, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup+".2")); err != nil || string(raw) != "- [a](a.md) — index\n" {
		t.Fatalf("%s.2=%q, %v", memoryLegacyIndexBackup, raw, err)
	}
}
