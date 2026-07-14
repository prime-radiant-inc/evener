package installid

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"primeradiant.com/serf/identifier"
)

const CodexInstallationIDMetadataKey = "x-codex-installation-id"

const installationIDLockAttempts = 32

func LoadOrCreateInstallationID(stateDir string) string {
	return LoadOrCreateInstallationIDWithFS(afero.NewOsFs(), stateDir)
}

// LoadOrCreateInstallationIDWithFS loads or creates the installation ID using
// fs. LoadOrCreateInstallationID delegates here with the host filesystem.
func LoadOrCreateInstallationIDWithFS(fs afero.Fs, stateDir string) string {
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
		lock, err := fs.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if winner := readValidInstallationID(fs, path); winner != "" {
				return winner
			}
			if os.IsExist(err) {
				continue
			}
			return ""
		}
		lockErr := lock.Sync()
		if closeErr := lock.Close(); lockErr == nil {
			lockErr = closeErr
		}
		if lockErr != nil {
			_ = fs.Remove(lockPath)
			if winner := readValidInstallationID(fs, path); winner != "" {
				return winner
			}
			return ""
		}
		result := createInstallationIDWhileLocked(fs, path)
		_ = fs.Remove(lockPath)
		return result
	}
	return readValidInstallationID(fs, path)
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

func createInstallationIDWhileLocked(fs afero.Fs, path string) string {
	if winner := readValidInstallationID(fs, path); winner != "" {
		return winner
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
			err = fs.Rename(tmpName, path)
		}
	}
	if winner := readValidInstallationID(fs, path); winner != "" {
		return winner
	}
	return ""
}
