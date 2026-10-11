package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
)

// The read itself is confined to the session's folder, not only the check
// before it: a path that passed fspaths.ResolveInRoot and was then swapped for
// a symlink leading out must not be read. The test hands readDocFile the
// swapped path directly, which is the state a swap between the check and the
// open leaves behind.
func TestReadDocFile_OpenStaysInsideTheRootAfterASwap(t *testing.T) {
	root := docTestRoot(t)
	outside := t.TempDir()
	writeDocAt(t, filepath.Join(outside, "secret.txt"), []byte("secret"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	read, err := readDocFile(root, filepath.Join(root, "notes.txt"))
	if err == nil {
		t.Fatalf("read %q through a symlink leading out of the root, want a refusal", read.Data)
	}
}

// A symlink that stays inside the folder is still followed: ResolveInRoot
// resolves it, and the open reads the file it names.
func TestReadDocFile_FollowsASymlinkInsideTheRoot(t *testing.T) {
	root := docTestRoot(t)
	writeDocAt(t, filepath.Join(root, "docs", "plan.md"), []byte("# Plan"), time.UnixMilli(1_790_000_000_000))
	if err := os.Symlink(filepath.Join(root, "docs", "plan.md"), filepath.Join(root, "latest.md")); err != nil {
		t.Fatal(err)
	}
	abs, err := fspaths.ResolveInRoot(root, "latest.md")
	if err != nil {
		t.Fatal(err)
	}

	read, err := readDocFile(root, abs)
	if err != nil || string(read.Data) != "# Plan" {
		t.Fatalf("readDocFile = %q, %v; want the linked file", read.Data, err)
	}
}
