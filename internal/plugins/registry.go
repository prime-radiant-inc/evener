package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

var marshalRegistry = json.MarshalIndent

// Registry is installed_plugins.json: the set of installed plugins keyed by
// "<plugin>@<marketplace>". The value is an array (Claude Code's shape); v1
// writes exactly one entry per key.
type Registry struct {
	Version int                       `json:"version"`
	Plugins map[string][]InstallEntry `json:"plugins"`
}

// InstallEntry is one installed plugin.
type InstallEntry struct {
	InstallPath  string    `json:"installPath"` //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	Version      string    `json:"version"`
	GitCommitSha string    `json:"gitCommitSha,omitempty"` //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	InstalledAt  time.Time `json:"installedAt"`            //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	LastUpdated  time.Time `json:"lastUpdated"`            //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	Enabled      bool      `json:"enabled"`
	AutoUpgrade  bool      `json:"autoUpgrade"` //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	Source       Source    `json:"source"`
	// Note is an install-time message worth showing the user — currently
	// only why an MCP-server-shaped, manifest-less plugin was NOT
	// auto-wired (see ensureManifestFallback). Empty for a clean install.
	Note string `json:"note,omitempty"`
}

// LoadRegistry reads installed_plugins.json. An absent file is not an error: it
// returns an empty v2 registry.
func LoadRegistry(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{Version: 2, Plugins: map[string][]InstallEntry{}}, nil
		}
		return Registry{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if r.Version != 1 && r.Version != 2 {
		return Registry{}, fmt.Errorf("parsing %s: unsupported installed_plugins.json schema version %d", path, r.Version)
	}
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	return r, nil
}

// SaveRegistry writes r to path atomically, always at schema version 2.
func SaveRegistry(path string, r Registry) error {
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	r.Version = 2
	body, err := marshalRegistry(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling registry: %w", err)
	}
	body = append(body, '\n')
	return atomicWriteFile(path, body, 0o644)
}
