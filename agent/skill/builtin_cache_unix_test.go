//go:build unix

package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateCacheDir_CreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := ensurePrivateCacheDir(dir); err != nil {
		t.Fatalf("ensurePrivateCacheDir: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat cache dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %o, want 700", info.Mode().Perm())
	}
	if err := ensurePrivateCacheDir(dir); err != nil {
		t.Fatalf("ensurePrivateCacheDir (again): %v", err)
	}
}

func TestEnsurePrivateCacheDir_RefusesUnsafeDirectories(t *testing.T) {
	root := t.TempDir()

	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ensurePrivateCacheDir(link); err == nil {
		t.Fatal("accepted a symlinked cache dir")
	}

	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatalf("create open dir: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot make the directory
	// private and hide the case under test.
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatalf("open up dir: %v", err)
	}
	if err := ensurePrivateCacheDir(open); err == nil {
		t.Fatal("accepted a group/other-accessible cache dir")
	}

	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatalf("create locked dir: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot leave it usable.
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("lock dir: %v", err)
	}
	if err := ensurePrivateCacheDir(locked); err == nil {
		t.Fatal("accepted a cache dir its owner cannot write")
	}
}

// A predictable per-user path another local user has already taken must not
// cost the session its bundled skills.
func TestDefaultEmbeddedSkillsBaseDir_FallsBackWhenPredictableNameIsUnusable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	squatted := filepath.Join(tmp, embeddedSkillsPrefix+processOwnerTag())
	if err := os.Mkdir(squatted, 0o700); err != nil {
		t.Fatalf("create squatted path: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot leave it private.
	if err := os.Chmod(squatted, 0o755); err != nil {
		t.Fatalf("open up squatted path: %v", err)
	}

	dir, err := defaultEmbeddedSkillsBaseDir()
	if err != nil {
		t.Fatalf("defaultEmbeddedSkillsBaseDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if dir == squatted {
		t.Fatalf("used the squatted path %q", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("fallback base dir = %q, %v, %v", dir, info, err)
	}
}
