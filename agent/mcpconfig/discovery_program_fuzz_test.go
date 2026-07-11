//go:build serffuzz

package mcpconfig

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
)

// FuzzMCPConfigDiscoveryProgram drives Discover through each real configuration
// layer using temporary files: global, project, explicit CLI files, and inline
// specs. The fake execution environment only answers the git-root probe, so the
// program never invokes Git, a shell, an MCP server, or a network client.
//
// Every replay exercises the stable discovery decisions that are outside the
// raw JSON and inline-parser fuzzers: layer precedence, optional-layer warning
// handling, fatal explicit-input handling, absent project roots, and both XDG
// and HOME global-config path selection. Fuzz bytes affect fixture values only;
// they never become a path, command, or environment-variable name.
func FuzzMCPConfigDiscoveryProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		[]byte("layered-config"),
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		token := mcpConfigDiscoveryToken(raw)
		t.Setenv("MCP_CONFIG_DISCOVERY_VALUE", token)

		fuzzMCPConfigDiscoverLayers(t, token)
		fuzzMCPConfigDiscoverOptionalFailures(t, token)
		fuzzMCPConfigDiscoverMissingLayers(t)
		fuzzMCPConfigDiscoverNoProjectRoot(t, token)
		fuzzMCPConfigDiscoverFatalInputs(t)
		fuzzMCPConfigDiscoverHomeFallback(t, token)
	})
}

func fuzzMCPConfigDiscoverLayers(t *testing.T, token string) {
	t.Helper()

	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	projectRoot := t.TempDir()
	cliPath := filepath.Join(t.TempDir(), "cli-mcp.json")

	fuzzMCPConfigWrite(t, filepath.Join(globalDir, "serf", "mcp.json"), map[string]map[string]any{
		"shared":      {"command": "global-" + token},
		"global-only": {"command": "${MCP_CONFIG_DISCOVERY_VALUE}"},
	})
	fuzzMCPConfigWrite(t, filepath.Join(projectRoot, ".serf", "mcp.json"), map[string]map[string]any{
		"shared":       {"command": "project-" + token},
		"project-only": {"command": "project-only"},
	})
	fuzzMCPConfigWrite(t, cliPath, map[string]map[string]any{
		"shared":   {"command": "cli-" + token},
		"cli-only": {"command": "cli-only"},
	})

	configs, warnings, err := Discover(
		&agenttest.FakeEnv{WorkDir: projectRoot, GitRoot: projectRoot},
		[]string{cliPath},
		[]string{"shared:inline-" + token + " --inline", "inline-only:inline-only --flag"},
	)
	if err != nil {
		t.Fatalf("Discover(layers): %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("Discover(layers) warnings = %v, want none", warnings)
	}
	if len(configs) != 5 {
		t.Fatalf("Discover(layers) returned %d configs, want 5: %#v", len(configs), configs)
	}

	fuzzMCPConfigAssertCommand(t, configs, "shared", "inline-"+token)
	fuzzMCPConfigAssertCommand(t, configs, "global-only", token)
	fuzzMCPConfigAssertCommand(t, configs, "project-only", "project-only")
	fuzzMCPConfigAssertCommand(t, configs, "cli-only", "cli-only")
	inline := fuzzMCPConfigByName(t, configs, "inline-only")
	if inline.Command != "inline-only" || len(inline.Args) != 1 || inline.Args[0] != "--flag" {
		t.Fatalf("inline-only config = %#v, want inline-only --flag", inline)
	}
}

func fuzzMCPConfigDiscoverOptionalFailures(t *testing.T, token string) {
	t.Helper()

	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	globalPath := filepath.Join(globalDir, "serf", "mcp.json")
	projectRoot := t.TempDir()
	projectPath := filepath.Join(projectRoot, ".serf", "mcp.json")

	fuzzMCPConfigWriteRaw(t, globalPath, []byte(`{"mcpServers":`))
	fuzzMCPConfigWriteRaw(t, projectPath, []byte(`{"mcpServers":{"broken":{"command":"`+token+`"}`))

	configs, warnings, err := Discover(&agenttest.FakeEnv{WorkDir: projectRoot, GitRoot: projectRoot}, nil, nil)
	if err != nil {
		t.Fatalf("Discover(optional malformed layers): %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("Discover(optional malformed layers) configs = %#v, want none", configs)
	}
	if len(warnings) != 2 {
		t.Fatalf("Discover(optional malformed layers) warnings = %v, want two", warnings)
	}
	if !fuzzMCPConfigWarningMentions(warnings, globalPath) || !fuzzMCPConfigWarningMentions(warnings, projectPath) {
		t.Fatalf("Discover(optional malformed layers) warnings = %v, want paths %q and %q", warnings, globalPath, projectPath)
	}
}

func fuzzMCPConfigDiscoverMissingLayers(t *testing.T) {
	t.Helper()

	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "nested"), 0o755); err != nil {
		t.Fatalf("make project nested directory: %v", err)
	}

	configs, warnings, err := Discover(&agenttest.FakeEnv{
		WorkDir: filepath.Join(projectRoot, "nested"),
		GitRoot: projectRoot,
	}, nil, nil)
	if err != nil {
		t.Fatalf("Discover(missing optional layers): %v", err)
	}
	if len(configs) != 0 || len(warnings) != 0 {
		t.Fatalf("Discover(missing optional layers) = configs:%#v warnings:%#v, want none", configs, warnings)
	}

	configs, warnings, err = Discover(nil, nil, nil)
	if err != nil || len(configs) != 0 || len(warnings) != 0 {
		t.Fatalf("Discover(nil) = configs:%#v warnings:%#v err:%v, want empty success", configs, warnings, err)
	}
}

func fuzzMCPConfigDiscoverNoProjectRoot(t *testing.T, token string) {
	t.Helper()

	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	fuzzMCPConfigWrite(t, filepath.Join(globalDir, "serf", "mcp.json"), map[string]map[string]any{
		"global-only": {"command": "global-" + token},
	})

	configs, warnings, err := Discover(&agenttest.FakeEnv{WorkDir: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("Discover(no project root): %v", err)
	}
	if len(warnings) != 0 || len(configs) != 1 {
		t.Fatalf("Discover(no project root) = configs:%#v warnings:%#v, want one global config", configs, warnings)
	}
	fuzzMCPConfigAssertCommand(t, configs, "global-only", "global-"+token)
}

func fuzzMCPConfigDiscoverFatalInputs(t *testing.T) {
	t.Helper()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	missingPath := filepath.Join(t.TempDir(), "missing-mcp.json")
	configs, warnings, err := Discover(nil, []string{missingPath}, nil)
	if err == nil || !strings.Contains(err.Error(), "--mcp-config") || configs != nil || warnings != nil {
		t.Fatalf("Discover(missing CLI file) = configs:%#v warnings:%#v err:%v, want fatal --mcp-config error", configs, warnings, err)
	}

	configs, warnings, err = Discover(nil, nil, []string{"missing-colon"})
	if err == nil || !strings.Contains(err.Error(), "--mcp") || configs != nil || warnings != nil {
		t.Fatalf("Discover(malformed inline spec) = configs:%#v warnings:%#v err:%v, want fatal --mcp error", configs, warnings, err)
	}
}

func fuzzMCPConfigDiscoverHomeFallback(t *testing.T, token string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	fuzzMCPConfigWrite(t, filepath.Join(home, ".config", "serf", "mcp.json"), map[string]map[string]any{
		"home-only": {"command": "home-" + token},
	})

	configs, warnings, err := Discover(nil, nil, nil)
	if err != nil {
		t.Fatalf("Discover(HOME fallback): %v", err)
	}
	if len(warnings) != 0 || len(configs) != 1 {
		t.Fatalf("Discover(HOME fallback) = configs:%#v warnings:%#v, want one config", configs, warnings)
	}
	fuzzMCPConfigAssertCommand(t, configs, "home-only", "home-"+token)
}

func fuzzMCPConfigWrite(t *testing.T, path string, servers map[string]map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("marshal MCP fixture: %v", err)
	}
	fuzzMCPConfigWriteRaw(t, path, body)
}

func fuzzMCPConfigWriteRaw(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make MCP fixture directory: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write MCP fixture: %v", err)
	}
}

func fuzzMCPConfigByName(t *testing.T, configs []ServerConfig, name string) ServerConfig {
	t.Helper()
	for _, cfg := range configs {
		if cfg.Name == name {
			return cfg
		}
	}
	t.Fatalf("config %q not found in %#v", name, configs)
	return ServerConfig{}
}

func fuzzMCPConfigAssertCommand(t *testing.T, configs []ServerConfig, name, want string) {
	t.Helper()
	if got := fuzzMCPConfigByName(t, configs, name).Command; got != want {
		t.Fatalf("config %q Command = %q, want %q", name, got, want)
	}
}

func fuzzMCPConfigWarningMentions(warnings []string, path string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, path) {
			return true
		}
	}
	return false
}

func mcpConfigDiscoveryToken(raw []byte) string {
	if len(raw) > 24 {
		raw = raw[:24]
	}
	if len(raw) == 0 {
		return "empty"
	}
	return "f" + hex.EncodeToString(raw)
}
