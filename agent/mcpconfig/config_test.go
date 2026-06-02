package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
)

func TestLoadMCPConfigFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"github": {
				"command": "gh-mcp",
				"args": ["--token", "abc"],
				"env": {"GH_TOKEN": "xyz"}
			},
			"db": {
				"type": "sse",
				"url": "http://localhost:8080/sse",
				"headers": {"Authorization": "Bearer tok"}
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	byName := map[string]ServerConfig{}
	for _, c := range configs {
		byName[c.Name] = c
	}

	gh := byName["github"]
	if gh.Command != "gh-mcp" {
		t.Errorf("github command = %q, want %q", gh.Command, "gh-mcp")
	}
	if len(gh.Args) != 2 || gh.Args[0] != "--token" || gh.Args[1] != "abc" {
		t.Errorf("github args = %v, want [--token abc]", gh.Args)
	}
	if gh.Env["GH_TOKEN"] != "xyz" {
		t.Errorf("github env GH_TOKEN = %q, want %q", gh.Env["GH_TOKEN"], "xyz")
	}
	if gh.Type != "stdio" {
		t.Errorf("github type = %q, want %q (default)", gh.Type, "stdio")
	}

	db := byName["db"]
	if db.Type != "sse" {
		t.Errorf("db type = %q, want %q", db.Type, "sse")
	}
	if db.URL != "http://localhost:8080/sse" {
		t.Errorf("db url = %q, want %q", db.URL, "http://localhost:8080/sse")
	}
	if db.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("db auth header = %q, want %q", db.Headers["Authorization"], "Bearer tok")
	}
}

func TestLoadMCPConfigFile_HTTPTransport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"api": {
				"type": "http",
				"url": "https://api.example.com/mcp"
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Type != "http" {
		t.Errorf("type = %q, want %q", configs[0].Type, "http")
	}
	if configs[0].URL != "https://api.example.com/mcp" {
		t.Errorf("url = %q", configs[0].URL)
	}
}

func TestLoadMCPConfigFile_MissingFile(t *testing.T) {
	_, err := LoadFile("/nonexistent/mcp.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadMCPConfigFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadMCPConfigFile_EmptyServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers": {}}`), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs, got %d", len(configs))
	}
}

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("TEST_MCP_VAR", "hello")

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"no vars here", "no vars here", false},
		{"${TEST_MCP_VAR}", "hello", false},
		{"prefix-${TEST_MCP_VAR}-suffix", "prefix-hello-suffix", false},
		{"${TEST_MCP_VAR:-fallback}", "hello", false},
		{"${UNSET_VAR_12345:-default}", "default", false},
		{"${UNSET_VAR_12345}", "", true}, // missing with no default
		{"${TEST_MCP_VAR:-}", "hello", false},
		{"${UNSET_VAR_12345:-}", "", false}, // empty default is valid
		{"multiple ${TEST_MCP_VAR} and ${TEST_MCP_VAR}", "multiple hello and hello", false},
	}

	for _, tt := range tests {
		got, err := expandEnvVars(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("expandEnvVars(%q): expected error, got %q", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("expandEnvVars(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseMCPInline(t *testing.T) {
	tests := []struct {
		spec    string
		name    string
		command string
		args    []string
		wantErr bool
	}{
		{"github:gh-mcp --token abc", "github", "gh-mcp", []string{"--token", "abc"}, false},
		{"simple:myserver", "simple", "myserver", nil, false},
		{"", "", "", nil, true},
		{"nocolon", "", "", nil, true},
		{":nocmd", "", "", nil, true},
		{"name:", "", "", nil, true},
	}

	for _, tt := range tests {
		cfg, err := ParseInline(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseInline(%q): expected error", tt.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseInline(%q): unexpected error: %v", tt.spec, err)
			continue
		}
		if cfg.Name != tt.name {
			t.Errorf("ParseInline(%q).Name = %q, want %q", tt.spec, cfg.Name, tt.name)
		}
		if cfg.Command != tt.command {
			t.Errorf("ParseInline(%q).Command = %q, want %q", tt.spec, cfg.Command, tt.command)
		}
		if len(cfg.Args) != len(tt.args) {
			t.Errorf("ParseInline(%q).Args = %v, want %v", tt.spec, cfg.Args, tt.args)
		}
		if cfg.Type != "stdio" {
			t.Errorf("ParseInline(%q).Type = %q, want stdio", tt.spec, cfg.Type)
		}
	}
}

func TestMergeMCPConfigs(t *testing.T) {
	layer1 := []ServerConfig{
		{Name: "a", Command: "cmd1"},
		{Name: "b", Command: "cmd2"},
	}
	layer2 := []ServerConfig{
		{Name: "b", Command: "cmd2-override"},
		{Name: "c", Command: "cmd3"},
	}

	merged := Merge(layer1, layer2)
	byName := map[string]ServerConfig{}
	for _, c := range merged {
		byName[c.Name] = c
	}

	if len(merged) != 3 {
		t.Fatalf("expected 3 configs, got %d", len(merged))
	}
	if byName["a"].Command != "cmd1" {
		t.Errorf("a.Command = %q, want cmd1", byName["a"].Command)
	}
	if byName["b"].Command != "cmd2-override" {
		t.Errorf("b.Command = %q, want cmd2-override (last wins)", byName["b"].Command)
	}
	if byName["c"].Command != "cmd3" {
		t.Errorf("c.Command = %q, want cmd3", byName["c"].Command)
	}
}

func TestMergeMCPConfigs_Empty(t *testing.T) {
	merged := Merge()
	if len(merged) != 0 {
		t.Errorf("expected 0 configs from empty merge, got %d", len(merged))
	}
}

func TestDiscoverMCPConfigs_GlobalAndProject(t *testing.T) {
	// Set up a fake global config dir.
	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)

	serfDir := filepath.Join(globalDir, "serf")
	if err := os.MkdirAll(serfDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serfDir, "mcp.json"), []byte(`{
		"mcpServers": {
			"global-tool": {"command": "gtool"}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set up a project directory with .serf/mcp.json.
	projDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projDir, ".serf"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ".serf", "mcp.json"), []byte(`{
		"mcpServers": {
			"project-tool": {"command": "ptool"},
			"global-tool": {"command": "gtool-override"}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a fake env that returns projDir as git root.
	env := &agenttest.FakeEnv{WorkDir: projDir, GitRoot: projDir}

	configs, err := Discover(env, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byName := map[string]ServerConfig{}
	for _, c := range configs {
		byName[c.Name] = c
	}

	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d: %v", len(configs), configs)
	}
	if byName["global-tool"].Command != "gtool-override" {
		t.Errorf("global-tool should be overridden by project config, got %q", byName["global-tool"].Command)
	}
	if byName["project-tool"].Command != "ptool" {
		t.Errorf("project-tool.Command = %q, want ptool", byName["project-tool"].Command)
	}
}

func TestDiscoverMCPConfigs_CLIOverrides(t *testing.T) {
	// No global or project configs.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	cliFile := filepath.Join(dir, "cli-mcp.json")
	if err := os.WriteFile(cliFile, []byte(`{
		"mcpServers": {
			"cli-tool": {"command": "ctool"}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := &agenttest.FakeEnv{WorkDir: dir, GitRoot: ""}

	configs, err := Discover(env, []string{cliFile}, []string{"inline-tool:itool --flag"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byName := map[string]ServerConfig{}
	for _, c := range configs {
		byName[c.Name] = c
	}

	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}
	if byName["cli-tool"].Command != "ctool" {
		t.Errorf("cli-tool.Command = %q, want ctool", byName["cli-tool"].Command)
	}
	if byName["inline-tool"].Command != "itool" {
		t.Errorf("inline-tool.Command = %q, want itool", byName["inline-tool"].Command)
	}
	if len(byName["inline-tool"].Args) != 1 || byName["inline-tool"].Args[0] != "--flag" {
		t.Errorf("inline-tool.Args = %v, want [--flag]", byName["inline-tool"].Args)
	}
}

func TestExpandEnvVars_InConfigLoading(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "secret123")

	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"test": {
				"command": "server",
				"env": {"TOKEN": "${MCP_TEST_TOKEN}"}
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configs[0].Env["TOKEN"] != "secret123" {
		t.Errorf("env TOKEN = %q, want secret123", configs[0].Env["TOKEN"])
	}
}

func TestExpandEnvVars_MissingVarInConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"test": {
				"command": "${DEFINITELY_UNSET_VAR_98765}"
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for missing env var without default")
	}
}

// agenttest.FakeEnv is a minimal execenv.ExecutionEnvironment for testing MCP config discovery.
// agenttest.FakeEnv now lives in agent/internal/agenttest as FakeEnv (shared with
// the agent and internal/mcp test suites).
