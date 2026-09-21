//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// OpenRegularNoFollow is the dedupe-read primitive for content-addressed
// storage: one descriptor, validated before any read.

// TestOpenRegularNoFollowRefusesSymlinkAtLeaf pins the primary guarantee: a
// symlink at the leaf is refused without its target ever being opened, even
// when the target holds the same bytes a dedupe compare would accept.
func TestOpenRegularNoFollowRefusesSymlinkAtLeaf(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	f, err := OpenRegularNoFollow(link)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularNoFollow returned a file for a symlinked leaf")
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("OpenRegularNoFollow(symlink) err = %v, want a symlink refusal", err)
	}
}

// TestOpenRegularNoFollowRefusesNonRegularWithoutBlocking pins the FIFO
// hazard: the open must never block on a read-only FIFO, and the entry must
// be refused rather than read.
func TestOpenRegularNoFollowRefusesNonRegularWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	f, err := OpenRegularNoFollow(fifo)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularNoFollow returned a file for a FIFO")
	}
	if err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("OpenRegularNoFollow(fifo) err = %v, want a not-regular refusal", err)
	}
}

// TestOpenRegularNoFollowOpensRegularFile pins the happy path: a regular
// file opens and reads back its exact bytes.
func TestOpenRegularNoFollowOpensRegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := OpenRegularNoFollow(path)
	if err != nil {
		t.Fatalf("OpenRegularNoFollow(regular): %v", err)
	}
	defer f.Close()
	got := make([]byte, len("payload"))
	if _, err := f.Read(got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("read bytes = %q, want payload", got)
	}
}
