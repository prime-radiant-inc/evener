//go:build unix

package fsatomic_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/execsupport/fsatomic"
)

func TestWriteIgnoresUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "state.json")
	if err := fsatomic.Write(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %v, want 0644 despite umask", info.Mode().Perm())
	}
}

// A directory that can be searched and written but not opened for reading lets
// the rename land and then fails the directory sync's open.
func TestWriteReportsPublishedWhenDirectorySyncFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	err := fsatomic.Write(path, []byte("new"), 0o600)
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("err = %v, want ErrPublished", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("contents = %q, want the published new contents", got)
	}
}

// The deletion is only durable once the directory entry is synced, so a failed
// directory sync is reported even though the file is already gone.
func TestRemoveReportsFailedDirectorySync(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if err := fsatomic.Remove(path); err == nil {
		t.Fatal("expected the unreadable directory to fail its sync")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	if err := fsatomic.Remove(path); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestWriteSetsPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := fsatomic.Write(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteFailureBeforeRenameKeepsExistingFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into any directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	err := fsatomic.Write(path, []byte("new"), 0o600)
	if err == nil || errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("contents = %q, want the old file untouched", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("directory entries = %v, want no temp file left", entries)
	}
}
