package installid

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/identifier"
)

const CodexInstallationIDMetadataKey = "x-codex-installation-id"

const (
	installationIDLockAttempts = 100
	installationIDLockWait     = 5 * time.Millisecond
)

// installationIDContentionWait bounds lock contention to 500ms total (100
// attempts at 5ms each). Tests replace this seam to release a deterministic
// lock owner without relying on sleeps.
var installationIDContentionWait = func(int) {
	time.Sleep(installationIDLockWait)
}

var installationIDAtomicReplace = atomicReplaceInstallationID

func LoadOrCreateInstallationID(stateDir string) string {
	return loadOrCreateInstallationID(
		afero.NewOsFs(),
		stateDir,
		acquireInstallationIDFileLock,
		func(fs afero.Fs, tempPath, destinationPath string, destinationExists bool) error {
			return installationIDAtomicReplace(tempPath, destinationPath, destinationExists)
		},
	)
}

// LoadOrCreateInstallationIDWithFS loads or creates the installation ID using
// fs. Its lock-file seam preserves deterministic non-OS filesystem tests;
// LoadOrCreateInstallationID uses a crash-releasing host advisory lock.
func LoadOrCreateInstallationIDWithFS(fs afero.Fs, stateDir string) string {
	return loadOrCreateInstallationID(
		fs,
		stateDir,
		func(path string) (installationIDLock, bool, error) {
			return acquireInstallationIDSentinelLock(fs, path)
		},
		func(fs afero.Fs, tempPath, destinationPath string, _ bool) error {
			return fs.Rename(tempPath, destinationPath)
		},
	)
}

type installationIDLock interface {
	Release() error
}

type installationIDLockAcquirer func(path string) (lock installationIDLock, contended bool, err error)
type installationIDReplacer func(fs afero.Fs, tempPath, destinationPath string, destinationExists bool) error

func loadOrCreateInstallationID(fs afero.Fs, stateDir string, acquire installationIDLockAcquirer, replace installationIDReplacer) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	path := filepath.Join(stateDir, "installation_id")
	if id := readValidInstallationID(fs, path); id != "" {
		return id
	}
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		return ""
	}
	lockPath := path + ".lock"
	for attempt := 0; attempt < installationIDLockAttempts; attempt++ {
		lock, contended, err := acquire(lockPath)
		if err != nil {
			if winner := readValidInstallationID(fs, path); winner != "" {
				return winner
			}
			if contended {
				installationIDContentionWait(attempt)
				continue
			}
			return ""
		}
		result := createInstallationIDWhileLocked(fs, path, replace)
		if err := lock.Release(); err != nil {
			return ""
		}
		return result
	}
	return readValidInstallationID(fs, path)
}

type installationIDSentinelLock struct {
	fs   afero.Fs
	path string
}

func acquireInstallationIDSentinelLock(fs afero.Fs, path string) (installationIDLock, bool, error) {
	lock, err := fs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, os.IsExist(err), err
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		_ = fs.Remove(path)
		return nil, false, err
	}
	if err := lock.Close(); err != nil {
		_ = fs.Remove(path)
		return nil, false, err
	}
	return installationIDSentinelLock{fs: fs, path: path}, false, nil
}

func (lock installationIDSentinelLock) Release() error {
	return lock.fs.Remove(lock.path)
}

func readValidInstallationID(fs afero.Fs, path string) string {
	b, err := afero.ReadFile(fs, path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if identifier.ValidateInstallationID(id) != nil {
		return ""
	}
	return id
}

func createInstallationIDWhileLocked(fs afero.Fs, path string, replace installationIDReplacer) string {
	if winner := readValidInstallationID(fs, path); winner != "" {
		return winner
	}
	destinationExists := false
	if _, err := fs.Stat(path); err == nil {
		destinationExists = true
	} else if !os.IsNotExist(err) {
		return ""
	}
	id, err := identifier.NewInstallationID()
	if err != nil {
		return ""
	}
	tmp, err := afero.TempFile(fs, filepath.Dir(path), ".installation_id.*")
	if err == nil {
		tmpName := tmp.Name()
		defer func() { _ = fs.Remove(tmpName) }()
		if _, err = tmp.WriteString(id + "\n"); err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = fs.Chmod(tmpName, 0o600)
		}
		if err == nil {
			err = replace(fs, tmpName, path, destinationExists)
		}
		if err != nil {
			return readValidInstallationID(fs, path)
		}
	}
	if winner := readValidInstallationID(fs, path); winner != "" {
		return winner
	}
	return ""
}
