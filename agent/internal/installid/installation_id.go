package installid

import (
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"primeradiant.com/serf/identifier"
)

const CodexInstallationIDMetadataKey = "x-codex-installation-id"

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
	if b, err := afero.ReadFile(fs, path); err == nil {
		if id := strings.TrimSpace(string(b)); identifier.ValidateInstallationID(id) == nil {
			return id
		}
	}
	id, err := identifier.NewInstallationID()
	if err != nil {
		return ""
	}
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		return ""
	}
	tmp, err := afero.TempFile(fs, stateDir, ".installation_id.*")
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
	if err != nil {
		if b, readErr := afero.ReadFile(fs, path); readErr == nil {
			if winner := strings.TrimSpace(string(b)); identifier.ValidateInstallationID(winner) == nil {
				return winner
			}
		}
		return ""
	}
	if b, readErr := afero.ReadFile(fs, path); readErr == nil {
		if winner := strings.TrimSpace(string(b)); identifier.ValidateInstallationID(winner) == nil {
			return winner
		}
	}
	return id
}
