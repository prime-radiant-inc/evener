//go:build unix

package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// A link squatting a fallback base name must never be followed: the reaper
// unlinks the entry itself and leaves whatever it points at alone.
func TestReapStaleFallbackBases_UnlinksALinkWithoutFollowingIt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	canary := filepath.Join(target, "keep")
	if err := os.WriteFile(canary, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write canary: %v", err)
	}
	link := filepath.Join(tmp, embeddedSkillsPrefix+processOwnerTag()+"-planted")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A regular file under the same prefix is not this package's to remove.
	stray := filepath.Join(tmp, embeddedSkillsPrefix+processOwnerTag()+"-stray")
	if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	reapStaleFallbackBases(os.TempDir(), time.Now(), "", "")

	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("planted link still present: %v", err)
	}
	if _, err := os.Lstat(stray); err != nil {
		t.Fatalf("reaper removed a regular file: %v", err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("reaper removed the link's target: %v", err)
	}
}

// A temp root another user could replace entries in must not be trusted with the
// per-user cache.
func TestDefaultEmbeddedSkillsBaseDir_RefusesAnUntrustedTempRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("open up temp root: %v", err)
	}
	if _, err := defaultEmbeddedSkillsBaseDir(); err == nil {
		t.Fatal("accepted a temp root without the sticky bit")
	}
	if err := os.Chmod(root, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make temp root sticky: %v", err)
	}
	t.Cleanup(func() {
		privateSkillsBaseMu.Lock()
		base := privateSkillsBase
		privateSkillsBase = ""
		privateSkillsBaseMu.Unlock()
		if base != "" {
			_ = os.RemoveAll(base)
		}
	})
	if _, err := defaultEmbeddedSkillsBaseDir(); err != nil {
		t.Fatalf("refused a sticky temp root: %v", err)
	}
}

// A predictable base another user has squatted must yield one private base for
// the process, not a fresh one per call: a retry that resolves a new base would
// publish another copy there and keep another lease.
func TestDefaultEmbeddedSkillsBaseDir_ReusesItsPrivateBase(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	squatted := filepath.Join(tmp, embeddedSkillsPrefix+processOwnerTag())
	if err := os.Mkdir(squatted, 0o700); err != nil {
		t.Fatalf("create squatted path: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot make it private.
	if err := os.Chmod(squatted, 0o755); err != nil {
		t.Fatalf("open up squatted path: %v", err)
	}
	t.Cleanup(func() {
		privateSkillsBaseMu.Lock()
		base := privateSkillsBase
		privateSkillsBase = ""
		privateSkillsBaseMu.Unlock()
		if base != "" {
			_ = os.RemoveAll(base)
		}
	})

	first, err := defaultEmbeddedSkillsBaseDir()
	if err != nil {
		t.Fatalf("defaultEmbeddedSkillsBaseDir: %v", err)
	}
	if first == squatted {
		t.Fatalf("used the squatted path %q", first)
	}
	second, err := defaultEmbeddedSkillsBaseDir()
	if err != nil {
		t.Fatalf("defaultEmbeddedSkillsBaseDir (again): %v", err)
	}
	if first != second {
		t.Fatalf("private base changed between calls: %q then %q", first, second)
	}
	if !cacheDirExists(second) {
		t.Fatalf("private base %q is not a directory", second)
	}
}
