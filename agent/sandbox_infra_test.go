package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/envvars"
)

// hermeticInfraEnv points the global MCP config layer at an empty temp dir so
// SessionInfraRoots never picks up the developer's real ~/.config/serf/mcp.json.
func hermeticInfraEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.XDGConfigHome.Name, t.TempDir())
}

func writeInfraFile(t *testing.T, path, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionInfraRootsComeFromTheSessionConfig is the anti-glob test: the
// hook/MCP read/exec surface is built from the plugin dirs and MCP servers THIS
// session is configured with, so a plugin cache the session does not load
// contributes nothing, and a plugin dir anywhere on disk contributes even though
// it is nowhere near ~/.claude/plugins.
func TestSessionInfraRootsComeFromTheSessionConfig(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)

	loaded := filepath.Join(root, "cache", "loaded-plugin")
	unloaded := filepath.Join(root, "cache", "unloaded-plugin")
	writeInfraFile(t, filepath.Join(loaded, "hooks", "session-start.sh"), "#!/bin/sh\n", 0o755)
	writeInfraFile(t, filepath.Join(unloaded, "hooks", "session-start.sh"), "#!/bin/sh\n", 0o755)

	server := writeInfraFile(t, filepath.Join(root, "servers", "mcp-server"), "#!/bin/sh\n", 0o755)
	script := writeInfraFile(t, filepath.Join(root, "scripts", "server.js"), "// mcp\n", 0o644)
	mcpJSON := writeInfraFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{
		"direct": {"command": "`+server+`"},
		"scripted": {"command": "node", "args": ["`+script+`"]},
		"remote": {"type": "http", "url": "https://example.invalid/mcp"}
	}}`, 0o644)

	cfg := SessionConfig{
		Sandbox:        "restricted",
		PluginDirs:     []string{loaded},
		MCPConfigFiles: []string{mcpJSON},
	}
	got := SessionInfraRoots(cfg, env)

	for _, want := range []string{loaded, filepath.Dir(server), filepath.Dir(script)} {
		if !slices.Contains(got, want) {
			t.Errorf("configured hook/MCP path %q missing from infra roots %v", want, got)
		}
	}
	if slices.Contains(got, unloaded) {
		t.Errorf("a plugin dir this session does NOT load must not be granted: %v", got)
	}
}

// TestSessionInfraRootsSkipUnsafeRoots pins the two paths that are deliberately
// NOT granted: a home-directory-level root (which would hand restricted mode the
// whole home) and a configured path that does not exist.
func TestSessionInfraRootsSkipUnsafeRoots(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{
		Sandbox:    "restricted",
		PluginDirs: []string{home, "/", filepath.Join(t.TempDir(), "does-not-exist")},
	}
	if got := SessionInfraRoots(cfg, env); len(got) != 0 {
		t.Errorf("home/root/missing paths must contribute no infra roots, got %v", got)
	}
}

// TestSessionInfraRootsAreFailSoft: an unreadable or malformed MCP config must
// not fail session start — the real MCP init reports it with proper diagnostics.
// The plugin dirs still contribute.
func TestSessionInfraRootsAreFailSoft(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)
	pluginDir := filepath.Join(root, "plug")
	writeInfraFile(t, filepath.Join(pluginDir, "plugin.yaml"), "name: p\n", 0o644)
	broken := writeInfraFile(t, filepath.Join(root, "broken.json"), "{not json", 0o644)

	cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{pluginDir}, MCPConfigFiles: []string{broken}}
	got := SessionInfraRoots(cfg, env)
	if !slices.Contains(got, pluginDir) {
		t.Errorf("a broken MCP config must not suppress the plugin roots, got %v", got)
	}
}
