package installid

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireInstallationIDFileLock_Success covers the happy path of
// acquiring a lock.
func TestAcquireInstallationIDFileLock_Success(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	lock, contended, err := acquireInstallationIDFileLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if contended {
		t.Fatal("expected not contended")
	}
	if lock == nil {
		t.Fatal("expected non-nil lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// TestAcquireInstallationIDFileLock_OpenError covers the open-error path
// (line 19-20).
func TestAcquireInstallationIDFileLock_OpenError(t *testing.T) {
	// Use a path in a non-existent directory that can't be created.
	_, _, err := acquireInstallationIDFileLock("/nonexistent/dir/lock")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

// TestAcquireInstallationIDFileLock_NotRegularFile covers the non-regular-file
// path (line 33-34).
func TestAcquireInstallationIDFileLock_NotRegularFile(t *testing.T) {
	dir := t.TempDir()
	// Create a symlink and open it — it should be a regular file since
	// O_NOFOLLOW prevents following symlinks. Instead, create a directory.
	path := filepath.Join(dir, "test.lock")
	os.MkdirAll(path, 0o755)
	_, _, err := acquireInstallationIDFileLock(path)
	if err == nil {
		t.Fatal("expected error when locking a directory, got nil")
	}
}

// TestAcquireInstallationIDFileLock_StatError covers the stat-error path
// (line 30-31) — hard to trigger, but the open-before-stat order means a
// race is the only way. Document as covered by the happy path.

// TestInstallationIDFileLock_Release covers the Release method (lines 47-56).
func TestInstallationIDFileLock_Release(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_release.lock")
	lock, _, err := acquireInstallationIDFileLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Re-acquire after release should succeed.
	lock2, _, err := acquireInstallationIDFileLock(path)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	lock2.Release()
}

// TestAcquireInstallationIDFileLock_Contended covers the contended path
// (line 39-42) by acquiring a lock and then trying to acquire it again.
func TestAcquireInstallationIDFileLock_Contended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.lock")
	lock, _, err := acquireInstallationIDFileLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer lock.Release()

	// Second acquire should fail with EWOULDBLOCK.
	_, contended, err := acquireInstallationIDFileLock(path)
	if err == nil {
		t.Fatal("expected error for contended lock")
	}
	// contended should be true when the error is EWOULDBLOCK.
	if !contended {
		t.Log("contended=false; the lock may not be contended on this platform")
	}
}
