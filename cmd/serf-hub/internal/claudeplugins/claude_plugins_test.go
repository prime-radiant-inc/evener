package claudeplugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnabledClaudePluginDirsResolvesEnabledCacheEntries(t *testing.T) {
	home := t.TempDir()
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{
  "enabledPlugins": {
    "alpha@market": true,
    "beta@market": false,
    "gamma@market": {"version":"2.0.0"},
    "delta@market": "1.0.0",
    "epsilon@market": true,
    "bad-key": true
  }
}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	writePluginManifest(t, home, "market", "alpha", "1.0.0", ".claude-plugin")
	writePluginManifest(t, home, "market", "alpha", "2.0.0", ".codex-plugin")
	writePluginManifest(t, home, "market", "beta", "1.0.0", ".claude-plugin")
	writePluginManifest(t, home, "market", "gamma", "1.0.0", ".claude-plugin")
	writePluginManifest(t, home, "market", "gamma", "2.0.0", ".claude-plugin")
	writePluginManifest(t, home, "market", "delta", "1.0.0", ".claude-plugin")
	writePluginManifestWithoutHooks(t, home, "market", "epsilon", "1.0.0")

	got := EnabledClaudePluginDirs(home)
	want := []string{
		filepath.Join(home, ".claude", "plugins", "cache", "market", "alpha", "2.0.0"),
		filepath.Join(home, ".claude", "plugins", "cache", "market", "delta", "1.0.0"),
		filepath.Join(home, ".claude", "plugins", "cache", "market", "gamma", "2.0.0"),
	}
	if len(got) != len(want) {
		t.Fatalf("dirs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirs[%d] = %q, want %q; all dirs=%#v", i, got[i], want[i], got)
		}
	}
}

func writePluginManifestWithoutHooks(t *testing.T, home, market, name, version string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "plugins", "cache", market, name, version, ".claude-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePluginManifest(t *testing.T, home, market, name, version, manifestDir string) {
	t.Helper()
	pluginDir := filepath.Join(home, ".claude", "plugins", "cache", market, name, version)
	manifestPath := filepath.Join(pluginDir, manifestDir)
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestPath, "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(pluginDir, "hooks")
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo started"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksPath, "hooks.json"), []byte(hooks), 0o600); err != nil {
		t.Fatal(err)
	}
}
