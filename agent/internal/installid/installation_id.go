package installid

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
)

const CodexInstallationIDMetadataKey = "x-codex-installation-id"

func LoadOrCreateInstallationID(stateDir string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	path := filepath.Join(stateDir, "installation_id")
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b))
	}
	id := ulid.Make().String()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		if b, readErr := os.ReadFile(path); readErr == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	return id
}
