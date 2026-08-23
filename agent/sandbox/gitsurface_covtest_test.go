package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateFileIfAbsent_Success covers the happy path of createFileIfAbsent.
func TestCreateFileIfAbsent_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	if err := createFileIfAbsent(path, "content\n"); err != nil {
		t.Fatalf("createFileIfAbsent: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "content\n" {
		t.Fatalf("content = %q, want %q", got, "content\n")
	}
}

// TestCreateFileIfAbsent_AlreadyExists covers the EEXIST path (line 116-118):
// linking onto an existing file returns nil (success).
func TestCreateFileIfAbsent_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := createFileIfAbsent(path, "newcontent"); err != nil {
		t.Fatalf("createFileIfAbsent on existing file should succeed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q, want original unchanged", got)
	}
}

// TestCreateFileIfAbsent_TempCreateError covers the error when the
// temp directory does not exist (line 99-100).
func TestCreateFileIfAbsent_TempCreateError(t *testing.T) {
	// Parent directory does not exist → CreateTemp fails.
	path := filepath.Join(t.TempDir(), "nonexistent_dir", "testfile")
	if err := createFileIfAbsent(path, "content"); err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

// TestAcquireScratchLease_Success covers the happy path.
func TestAcquireScratchLease_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lease.lock")
	lease, contended, err := acquireScratchLease(path)
	if err != nil {
		t.Fatalf("acquireScratchLease: %v", err)
	}
	if contended {
		t.Fatal("expected contended=false")
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestAcquireScratchLease_OpenError covers the open-error path
// (line 19-20): a path whose parent does not exist.
func TestAcquireScratchLease_OpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no_dir", "lease.lock")
	_, _, err := acquireScratchLease(path)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

// TestAcquireScratchLease_NotRegular covers the non-regular-file path
// (line 33-34): opening a directory returns an error.
func TestAcquireScratchLease_NotRegular(t *testing.T) {
	dir := t.TempDir()
	// Create the path as a directory so open succeeds but Stat reports
	// it is not a regular file.
	dirPath := filepath.Join(dir, "mydir.lock")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := acquireScratchLease(dirPath)
	if err == nil {
		t.Fatal("expected error for non-regular file")
	}
}

// TestAcquireScratchLease_Symlink covers the ELOOP path (line 19-20):
// O_NOFOLLOW refuses a symlink, returning an open error.
func TestAcquireScratchLease_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := acquireScratchLease(link)
	if err == nil {
		t.Fatal("expected error for symlink")
	}
}

// TestUnixScratchLease_Release covers Release on a valid lease.
func TestUnixScratchLease_Release(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.lock")
	lease, _, err := acquireScratchLease(path)
	if err != nil {
		t.Fatalf("acquireScratchLease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
