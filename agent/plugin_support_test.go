package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/internal/testfs"
)

// makePluginDir creates a temp dir holding a minimal .claude-plugin/plugin.json
// for the named plugin and returns its resolved absolute path.
func makePluginDir(t *testing.T, name string) string {
	t.Helper()
	dir := testfs.CanonicalTempDir(t)
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `{"name": "` + name + `"}`
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

// keys returns the keys of m in unspecified order.
func keys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
