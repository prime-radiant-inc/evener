package installid

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// failingSyncFile wraps an afero.File to make Sync fail.
type failingSyncFile struct {
	afero.File
}

func (f *failingSyncFile) Sync() error {
	return errors.New("injected sync failure")
}

// failingSyncFs wraps an afero.Fs to return files whose Sync fails.
type failingSyncFs struct {
	afero.Fs
}

func (fs *failingSyncFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &failingSyncFile{File: f}, nil
}

// TestAcquireInstallationIDSentinelLock_SyncError covers the Sync error path
// (installation_id.go:107-110): a filesystem whose file Sync fails returns an
// error and cleans up the lock file.
func TestAcquireInstallationIDSentinelLock_SyncError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "installation_id.lock")
	fs := &failingSyncFs{Fs: afero.NewMemMapFs()}
	// Pre-create the directory.
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := acquireInstallationIDSentinelLock(fs, lockPath)
	if err == nil {
		t.Fatal("expected sync error")
	}
	// Lock file should have been cleaned up.
	if _, err := fs.Stat(lockPath); err == nil {
		t.Fatal("lock file should have been removed after sync failure")
	}
}

// failingCloseFile wraps an afero.File to make Close fail.
type failingCloseFile struct {
	afero.File
	synced bool
}

func (f *failingCloseFile) Sync() error {
	f.synced = true
	return nil
}

func (f *failingCloseFile) Close() error {
	return errors.New("injected close failure")
}

// failingCloseFs wraps an afero.Fs to return files whose Close fails.
type failingCloseFs struct {
	afero.Fs
}

func (fs *failingCloseFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &failingCloseFile{File: f}, nil
}

// TestAcquireInstallationIDSentinelLock_CloseError covers the Close error path
// (installation_id.go:112-114): a filesystem whose file Close fails after a
// successful Sync returns an error and cleans up the lock file.
func TestAcquireInstallationIDSentinelLock_CloseError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "installation_id.lock")
	fs := &failingCloseFs{Fs: afero.NewMemMapFs()}
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := acquireInstallationIDSentinelLock(fs, lockPath)
	if err == nil {
		t.Fatal("expected close error")
	}
	// Lock file should have been cleaned up.
	if _, err := fs.Stat(lockPath); err == nil {
		t.Fatal("lock file should have been removed after close failure")
	}
}

// erroringStatFs wraps an afero.Fs to make Stat return a non-NotExist error.
type erroringStatFs struct {
	afero.Fs
}

func (fs *erroringStatFs) Stat(name string) (os.FileInfo, error) {
	return nil, errors.New("injected stat failure")
}

// TestCreateInstallationIDWhileLocked_StatError covers the stat error path
// (installation_id.go:142-143): a Stat error that is not IsNotExist causes
// the function to return "".
func TestCreateInstallationIDWhileLocked_StatError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "installation_id")
	fs := &erroringStatFs{Fs: afero.NewMemMapFs()}
	// The function first calls readValidInstallationID (which uses ReadFile,
	// not Stat), so it returns "" and falls through to createInstallationIDWhileLocked.
	got := createInstallationIDWhileLocked(fs, path, func(afero.Fs, string, string, bool) error {
		return nil
	})
	if got != "" {
		t.Fatalf("expected empty result for stat error, got %q", got)
	}
}

// TestLoadOrCreateInstallationID_LockReleaseError covers the lock.Release error
// path (installation_id.go:89-90): when lock.Release fails, the function
// returns "".
func TestLoadOrCreateInstallationID_LockReleaseError(t *testing.T) {
	dir := t.TempDir()
	fs := afero.NewMemMapFs()
	got := loadOrCreateInstallationID(
		fs,
		dir,
		func(path string) (installationIDLock, bool, error) {
			return &failingReleaseLock{}, false, nil
		},
		func(fs afero.Fs, tempPath, destinationPath string, destinationExists bool) error {
			return fs.Rename(tempPath, destinationPath)
		},
	)
	if got != "" {
		t.Fatalf("expected empty result for lock release error, got %q", got)
	}
}

type failingReleaseLock struct{}

func (l *failingReleaseLock) Release() error {
	return errors.New("injected release failure")
}

// TestInstallationIDContentionWait_Default covers the default
// installationIDContentionWait function (installation_id.go:23-25).
// This exercises the time.Sleep call itself.
func TestInstallationIDContentionWait_Default(t *testing.T) {
	// The default function calls time.Sleep(5ms). We can verify it returns
	// without panicking. We don't assert timing — just that it doesn't hang.
	installationIDContentionWait(0)
}
