package plugins

import (
	"os"
	"path/filepath"
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
