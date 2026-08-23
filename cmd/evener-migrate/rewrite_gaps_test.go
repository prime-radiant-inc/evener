package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRewriteLegacyPathsWalkError covers the walk error path (line 52-53)
// by passing a non-existent root directory.
func TestRewriteLegacyPathsWalkError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	var stdout bytes.Buffer
	err := rewriteLegacyPaths(missing, "/old", "/new", &stdout)
	if err == nil {
		t.Fatalf("rewriteLegacyPaths on non-existent dir should error")
	}
}

// TestRewriteLegacyPathsWriteError covers the WriteFile error path (line
// 85-86) by making the target file read-only so the rewrite fails.
func TestRewriteLegacyPathsWriteError(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	target := filepath.Join(dst, "readonly.json")
	body := `{"path":"` + old + `/data"}`
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the file read-only so WriteFile fails.
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o644) })

	var stdout bytes.Buffer
	err := rewriteLegacyPaths(dst, old, nw, &stdout)
	if err == nil {
		t.Fatalf("rewriteLegacyPaths on read-only file should error")
	}
}

// TestRewriteLegacyPathsSkipsSymlink covers the symlink skip path (line 60-62)
// by creating a symlink that points outside the tree.
func TestRewriteLegacyPathsSkipsSymlink(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	// Create a symlink to an external file containing the old path.
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte(old+"/path"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dst, "link.txt")
	if err := os.Symlink(external, link); err != nil {
		t.Skip("symlink not supported on this platform")
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths with symlink should not error: %v", err)
	}
	// The symlink target should be unchanged.
	got, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != old+"/path" {
		t.Fatalf("symlink target was modified: got %q, want %q", got, old+"/path")
	}
}

// TestLooksBinaryInvalidUTF8 covers the !utf8.Valid branch in looksBinary.
func TestLooksBinaryInvalidUTF8(t *testing.T) {
	// Invalid UTF-8 sequence (0xfe is never valid in UTF-8).
	data := []byte{0xfe, 0xff}
	if !looksBinary(data) {
		t.Fatalf("looksBinary with invalid UTF-8 should return true")
	}
}

// TestLooksBinaryLargeFileSniffWindow covers the len(sniff) > binarySniffWindow
// branch by passing data larger than the sniff window.
func TestLooksBinaryLargeFileSniffWindow(t *testing.T) {
	// A large file with no NUL bytes and valid UTF-8 should not be binary.
	data := bytes.Repeat([]byte("a"), binarySniffWindow+100)
	if looksBinary(data) {
		t.Fatalf("looksBinary with large valid UTF-8 should return false")
	}
	// A large file with a NUL byte just past the sniff window should not be binary.
	data2 := bytes.Repeat([]byte("a"), binarySniffWindow+100)
	data2[binarySniffWindow] = 0
	if looksBinary(data2) {
		t.Fatalf("looksBinary with NUL past sniff window should return false")
	}
}
