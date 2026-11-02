package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAtomicWriteFile_WritesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")

	if err := atomicWriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatalf("content = %q, want %q", b, "one")
	}
	if err := atomicWriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("content = %q, want %q", b, "two")
	}

	// No leftover temp files.
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1 (leftover temp?)", len(entries))
	}
}

// A directory that can be searched and written but not read lets the rename
// land and then fails the directory sync; the file is in place, so the write
// is not a failure.
func TestAtomicWriteFile_DirectorySyncFailureAfterTheRenameIsNotAFailedWrite(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs a directory the process cannot open for reading")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := atomicWriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatalf("content = %q, want %q", b, "one")
	}
}
