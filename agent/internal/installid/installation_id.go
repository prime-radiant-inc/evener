package installid

import (
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/afero"
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
		return strings.TrimSpace(string(b))
	}
	id := ulid.Make().String()
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		return ""
	}
	if err := afero.WriteFile(fs, path, []byte(id+"\n"), 0o600); err != nil {
		if b, readErr := afero.ReadFile(fs, path); readErr == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	return id
}
