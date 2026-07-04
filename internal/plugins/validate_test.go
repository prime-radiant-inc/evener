package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, dir, name string, extra map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o644)
	for rel, content := range extra {
		full := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(content), 0o644)
	}
}

func TestValidatePluginDir(t *testing.T) {
	good := filepath.Join(t.TempDir(), "good")
	writePlugin(t, good, "widget", nil)
	if err := validatePluginDir(good); err != nil {
		t.Fatalf("valid plugin rejected: %v", err)
	}
	if v := pluginManifestVersion(good); v != "1.0.0" {
		t.Fatalf("manifest version = %q, want 1.0.0", v)
	}

	// A broken agents/*.md (missing frontmatter name) must fail validation,
	// because agent/plugin.Load parses every component.
	bad := filepath.Join(t.TempDir(), "bad")
	writePlugin(t, bad, "widget", map[string]string{
		"agents/broken.md": "no frontmatter here",
	})
	if err := validatePluginDir(bad); err == nil {
		t.Fatal("broken plugin passed validation")
	}
}
