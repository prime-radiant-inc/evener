//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestListDirEntryMetadataResolvesBeneathFd: a confined ListDirectory must fill
// each entry's Size/IsExec from the entry beneath the listing directory fd, never
// from a same-named path at the host root. readDirEntries wraps a dup of the
// directory fd as os.NewFile(fd, ""), whose DirEntry.Info() lstat's "/"+name, so
// before the fix an entry named after a host root path inherited that path's
// size/mode and any other name silently lost its size.
func TestListDirEntryMetadataResolvesBeneathFd(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)

	content := []byte("abc")
	// "etc" exists at the host root (a 0755 directory on Linux/macOS); the listed
	// entry is a 3-byte 0644 regular file, so host-root resolution reports the
	// wrong size and sets the exec bit.
	if err := os.WriteFile(filepath.Join(worktree, "etc"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	// A name absent from the host root: host-root resolution drops size entirely.
	if err := os.WriteFile(filepath.Join(worktree, "canary-no-such"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	ents, err := s.listDir("list_dir", worktree, 1)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	byName := map[string]DirEntry{}
	for _, e := range ents {
		byName[e.Name] = e
	}

	etc, ok := byName["etc"]
	if !ok {
		t.Fatalf("listing omitted etc: %+v", ents)
	}
	if etc.Size != int64(len(content)) {
		t.Errorf("etc Size = %d, want %d (host-root metadata leaked)", etc.Size, len(content))
	}
	if etc.IsExec {
		t.Errorf("etc IsExec = true, want false (mode 0644)")
	}

	canary, ok := byName["canary-no-such"]
	if !ok {
		t.Fatalf("listing omitted canary-no-such: %+v", ents)
	}
	if canary.Size != int64(len(content)) {
		t.Errorf("canary-no-such Size = %d, want %d (metadata dropped)", canary.Size, len(content))
	}
}
