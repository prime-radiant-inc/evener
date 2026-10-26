//go:build unix

package fsatomic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// When the parent sync fails and the new directory cannot be taken back out
// either, the next call must sync it before accepting it.
func TestMkdirAllSyncsAStrandedDirectoryOnTheNextCall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any directory")
	}
	removeFails := errors.New("rmdir failed")
	var removeCalls []string
	removeDir = func(p string) error {
		removeCalls = append(removeCalls, p)
		return removeFails
	}
	t.Cleanup(func() { removeDir = os.Remove })

	base := t.TempDir()
	if err := os.Chmod(base, 0o300); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(base, 0o700) }()
	dir := filepath.Join(base, "a")

	if err := MkdirAll(dir, 0o700); !errors.Is(err, removeFails) {
		t.Fatalf("err = %v, want the rollback failure reported", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{dir, base + "//a", alias} {
		if err := MkdirAll(spelling, 0o700); err == nil {
			t.Fatalf("retry through %q accepted a directory whose parent sync never succeeded", spelling)
		}
	}
	if len(removeCalls) != 1 {
		t.Fatalf("rollback attempted on %v, want only the one directory the first call created", removeCalls)
	}
	// A child of the stranded directory does not settle it either.
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(child, 0o700); err == nil {
		t.Fatal("retry through a child accepted a stranded ancestor")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(child, 0o700); err != nil {
		t.Fatalf("retry once the parent can be synced: %v", err)
	}
	if stranded[dirKey(dir)] {
		t.Fatal("directory still marked stranded after a successful sync")
	}
}
