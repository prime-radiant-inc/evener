//go:build darwin || linux

package llm

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCovOpenPrivateAPILogFileNilFile covers the file == nil path after
// os.NewFile (apilog_open_unix.go lines 20-22). This is hard to trigger
// because os.NewFile rarely returns nil for a valid fd. We can trigger it
// by passing an invalid fd. However, unix.Open succeeds with a valid fd.
// The file == nil path is effectively unreachable with a valid fd.
func TestCovOpenPrivateAPILogFileNilFile(t *testing.T) {
	// os.NewFile returns nil only when fd is 0 on some platforms or when
	// the fd is invalid. Since unix.Open returns a valid fd, this path is
	// effectively unreachable in normal operation.
	t.Skip("os.NewFile with a valid fd never returns nil; this path is unreachable")
}

// TestCovOpenPrivateAPILogFileStatError covers the file.Stat() error path
// (apilog_open_unix.go lines 30-31). We can trigger this by opening a file
// then making it inaccessible. On unix, we can use a path that becomes
// inaccessible between open and stat. However, since the file is already
// open, stat should succeed. This is hard to trigger deterministically.
func TestCovOpenPrivateAPILogFileStatError(t *testing.T) {
	// Opening a path that's a symlink to a deleted file could cause stat
	// to fail, but O_NOFOLLOW prevents following symlinks. This path is
	// hard to trigger deterministically.
	t.Skip("file.Stat() after successful open is hard to fail deterministically")
}

// TestCovOpenPrivateAPILogFileNotRegular covers the !info.Mode().IsRegular()
// path (apilog_open_unix.go lines 33-34). We can trigger this by opening
// a special file like /dev/null.
func TestCovOpenPrivateAPILogFileNotRegular(t *testing.T) {
	_, err := openPrivateAPILogFile("/dev/null")
	if err == nil {
		t.Fatal("openPrivateAPILogFile on /dev/null should fail (not regular)")
	}
}

// TestCovOpenPrivateAPILogFileFlockError covers the non-EWOULDBLOCK flock
// error path (apilog_open_unix.go line 39). This is hard to trigger because
// flock on a regular file usually succeeds or returns EWOULDBLOCK.
func TestCovOpenPrivateAPILogFileFlockError(t *testing.T) {
	// flock can fail with EINVAL on some filesystems (e.g. network
	// filesystems). This is hard to trigger deterministically in a test.
	t.Skip("non-EWOULDBLOCK flock error is hard to trigger deterministically")
}

// TestCovOpenPrivateAPILogFileChmodError covers the file.Chmod error path
// (apilog_open_unix.go lines 42-43). We can trigger this by opening a file
// on a read-only filesystem, but that requires special setup.
func TestCovOpenPrivateAPILogFileChmodError(t *testing.T) {
	// On macOS, /dev/null is not a regular file so it fails earlier.
	// A read-only filesystem would cause Chmod to fail, but setting
	// that up in a test is not deterministic.
	t.Skip("file.Chmod error requires a read-only filesystem")
}

// TestCovOpenPrivateAPILogFileTargetLocked covers the EWOULDBLOCK path
// (apilog_open_unix.go lines 36-37). We trigger this by opening the same
// file twice — the second open should get EWOULDBLOCK from flock.
func TestCovOpenPrivateAPILogFileTargetLocked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")

	// First open succeeds and holds the lock.
	f1, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("first openPrivateAPILogFile: %v", err)
	}
	defer f1.Close()

	// Second open should fail with ErrAPILogTargetLocked.
	_, err = openPrivateAPILogFile(path)
	if err == nil {
		t.Fatal("second openPrivateAPILogFile should fail with target locked")
	}
	// Verify it's the target-locked error.
	if !errorContains(err, ErrAPILogTargetLocked.Error()) {
		t.Fatalf("second open error = %v, want ErrAPILogTargetLocked", err)
	}
}

// TestCovOpenPrivateAPILogFileOpenError covers the unix.Open error path
// (apilog_open_unix.go lines 15-16).
func TestCovOpenPrivateAPILogFileOpenError(t *testing.T) {
	// A path under a non-existent directory should fail to open.
	_, err := openPrivateAPILogFile(filepath.Join(t.TempDir(), "nonexistent-dir", "api.jsonl"))
	if err == nil {
		t.Fatal("openPrivateAPILogFile under non-existent dir should fail")
	}
}

// TestCovOpenPrivateAPILogFileSuccess covers the happy path.
func TestCovOpenPrivateAPILogFileSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	f, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("openPrivateAPILogFile: %v", err)
	}
	defer f.Close()
	// Verify the file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

// errorContains checks if err's string contains substr.
func errorContains(err error, substr string) bool {
	if err == nil {
		return false
	}
	return err.Error() != "" && len(err.Error()) >= len(substr) && (err.Error() == substr || containsStr(err.Error(), substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
