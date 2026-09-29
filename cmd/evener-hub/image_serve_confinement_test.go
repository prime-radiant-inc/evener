package hub

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The image read is confined to the session's folder, not only the check before
// it: a path that passed fspaths.ResolveInRoot and was then swapped for a
// symlink leading out must not be read. The test hands the reader the swapped
// path directly, which is the state a swap between the check and the open
// leaves behind.
func TestReadOutputImageInRoot_RefusesASymlinkLeadingOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(root, "shot.png")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	if data, _, ok := readOutputImageInRoot(root, filepath.Join(root, "shot.png")); ok {
		t.Fatalf("read %x through a symlink leading out of the root, want a refusal", data)
	}
}

// A symlink that stays inside the folder is still followed, so the confinement
// refuses only what leaves the root.
func TestReadOutputImageInRoot_FollowsASymlinkInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.MkdirAll(filepath.Join(root, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shots", "real.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	// A relative symlink, the form a swap can leave that still stays inside.
	if err := os.Symlink(filepath.Join("shots", "real.png"), filepath.Join(root, "shot.png")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	data, _, ok := readOutputImageInRoot(root, filepath.Join(root, "shot.png"))
	if !ok || !bytes.Equal(data, png) {
		t.Fatalf("readOutputImageInRoot = %x, %v; want the linked file inside the root", data, ok)
	}
}

// The root itself may be reached through a symlink: on macOS t.TempDir sits
// under /var, a symlink to /private/var, so the root the test builds and the abs
// it joins onto it are unresolved. This builds that same shape on any platform
// (a symlink whose target is the real root) and pins that the in-root symlink is
// still followed, so the read is not refused by the resolved-root against
// unresolved-abs mismatch.
func TestReadOutputImageInRoot_FollowsASymlinkInsideASymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	root := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realRoot, root); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.MkdirAll(filepath.Join(realRoot, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "shots", "real.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("shots", "real.png"), filepath.Join(realRoot, "shot.png")); err != nil {
		t.Fatal(err)
	}

	data, _, ok := readOutputImageInRoot(root, filepath.Join(root, "shot.png"))
	if !ok || !bytes.Equal(data, png) {
		t.Fatalf("readOutputImageInRoot = %x, %v; want the linked file inside the symlinked root", data, ok)
	}
}
