//go:build unix

package fsatomic_test

import (
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
