//go:build unix

package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestInstallLockReadOnlyDirReportsSentinel proves a managed directory that
// cannot host a lock file is reported as ErrInstallDirReadOnly — the one
// failure the restart path may continue past, with its digest re-verify.
func TestInstallLockReadOnlyDirReportsSentinel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 directory")
	}
	dir := filepath.Join(t.TempDir(), "share")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := acquireInstallLockCtx(context.Background(), dir)
	if !errors.Is(err, ErrInstallDirReadOnly) {
		t.Fatalf("err = %v, want ErrInstallDirReadOnly", err)
	}
}

// TestInstallLockUnreadableExistingFileIsNotReadOnly proves an existing but
// unreadable lock is an ordinary failure: another installer may be using it,
// so the restart must not bypass it.
func TestInstallLockUnreadableExistingFileIsNotReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 file")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".install.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })

	_, err := acquireInstallLockCtx(context.Background(), dir)
	if err == nil {
		t.Fatal("acquired an unreadable existing lock")
	}
	if errors.Is(err, ErrInstallDirReadOnly) {
		t.Fatalf("unreadable lock reported as a read-only install: %v", err)
	}
}

// TestInstallLockReadOnlyFilesystemUsesExistingLock proves a read-only
// filesystem still serializes through a lock file that already exists.
func TestInstallLockReadOnlyFilesystemUsesExistingLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".install.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeReadOnlyCreate(t)

	release, err := acquireInstallLockCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("acquire existing lock on a read-only filesystem: %v", err)
	}
	release()
}

// TestInstallLockReadOnlyFilesystemWithoutLockReportsSentinel proves a
// read-only filesystem with no lock file is the one no-lock case.
func TestInstallLockReadOnlyFilesystemWithoutLockReportsSentinel(t *testing.T) {
	dir := t.TempDir()
	fakeReadOnlyCreate(t)

	_, err := acquireInstallLockCtx(context.Background(), dir)
	if !errors.Is(err, ErrInstallDirReadOnly) {
		t.Fatalf("err = %v, want ErrInstallDirReadOnly", err)
	}
}

// fakeReadOnlyCreate makes the lock file's create open fail with EROFS while
// leaving read-only opens on the real filesystem.
func fakeReadOnlyCreate(t *testing.T) {
	t.Helper()
	previous := openInstallLockFile
	t.Cleanup(func() { openInstallLockFile = previous })
	openInstallLockFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if flag&os.O_RDWR != 0 {
			return nil, &os.PathError{Op: "open", Path: name, Err: unix.EROFS}
		}
		return os.OpenFile(name, flag, perm)
	}
}
