package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzMCPConfigLoad drives LoadFile — the package's real .mcp.json decode seam
// (json.Unmarshal of the config file → ParseServerMap → serverJSONToConfig with
// ${VAR} expansion). Input is the raw config file bytes written to a temp file.
// Beyond no-panic it asserts the post-decode invariant ParseServerMap
// guarantees: every returned config has a non-empty Type (defaulted to "stdio").
// (Name is intentionally not asserted non-empty: the loader copies the map key
// verbatim and does not reject an empty server name — see the empty-name
// passthrough finding reported for this target.)
func FuzzMCPConfigLoad(f *testing.F) {
	seeds := []string{
		`{"mcpServers":{"a":{"command":"echo","args":["hi"]}}}`,
		`{"mcpServers":{"web":{"type":"http","url":"http://x","headers":{"k":"v"}}}}`,
		`{"mcpServers":{"e":{"command":"x","env":{"K":"${HOME:-/tmp}"}}}}`,
		`{"mcpServers":{}}`,
		`{}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		configs, err := LoadFile(path)
		if err != nil {
			return // malformed config or unset ${VAR}: no-panic floor proven, stop
		}

		for _, cfg := range configs {
			if cfg.Type == "" {
				t.Fatalf("server %q has empty Type after parse", cfg.Name)
			}
		}
	})
}
