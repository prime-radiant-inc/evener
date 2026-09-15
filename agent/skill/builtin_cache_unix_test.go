//go:build unix

package skill

import (
	"os"
	"path/filepath"
	"strings"
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
	if _, err := defaultEmbeddedSkillsBaseDir(); err != nil {
		t.Fatalf("refused a sticky temp root: %v", err)
	}
}

// The whole chain above the cache root decides whether another user can replace
// the per-user entry: a world-writable, non-sticky ancestor is refused, while a
// sticky or non-writable chain is accepted.
func TestEnsureTrustedRoot_ChecksTheAncestorChain(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatalf("create inner: %v", err)
	}
	if err := ensureTrustedRoot(inner); err != nil {
		t.Fatalf("refused a private chain: %v", err)
	}

	// Chmod rather than Mkdir, so the ambient umask cannot hide the case.
	if err := os.Chmod(outer, 0o777); err != nil {
		t.Fatalf("open up ancestor: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a world-writable ancestor")
	}
	if err := os.Chmod(outer, 0o770); err != nil {
		t.Fatalf("make ancestor group-writable: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a group-writable ancestor")
	}

	// The sticky bit stops another user replacing the entry named inside it.
	if err := os.Chmod(outer, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make ancestor sticky: %v", err)
	}
	if err := ensureTrustedRoot(inner); err != nil {
		t.Fatalf("refused a sticky ancestor: %v", err)
	}

	// A non-writable ancestor is accepted, and the final component still obeys
	// its own rule: the cache root itself must not be group- or other-accessible.
	if err := os.Chmod(outer, 0o700); err != nil {
		t.Fatalf("restore ancestor: %v", err)
	}
	if err := os.Chmod(inner, 0o770); err != nil {
		t.Fatalf("make root group-writable: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a group-accessible cache root")
	}
}

// The degraded extraction must not land in a temp root the shared cache refuses.
func TestEmbeddedSkillsDir_RefusesAnUntrustedTempRootForTheProcessCopy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("open up temp root: %v", err)
	}
	if _, err := EmbeddedSkillsDir(); err == nil {
		t.Fatal("extracted a process copy into an untrusted temp root")
	}

	if err := os.Chmod(root, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make temp root sticky: %v", err)
	}
	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("refused a sticky temp root: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(dir), embeddedSkillsPrefix+"process-") {
		t.Fatalf("degraded copy = %q, want a process-lifetime extraction", dir)
	}
}
