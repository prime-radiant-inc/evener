package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/execenv"
)

// MCPServerConfig describes a single MCP server connection.
type MCPServerConfig struct {
	Name    string            // from the map key
	Type    string            // "stdio" (default), "sse", "http"
	Command string            // stdio: executable
	Args    []string          // stdio: arguments
	Env     map[string]string // extra env vars (merged with process env)
	URL     string            // sse/http: server URL
	Headers map[string]string // sse/http: extra headers
}

// mcpConfigFile is the JSON structure of an mcp.json file.
type mcpConfigFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// mcpServerJSON is the raw JSON form of a single server entry.
type mcpServerJSON struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// LoadMCPConfigFile parses one .mcp.json file and returns server configs
// with env vars expanded.
func LoadMCPConfigFile(path string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading MCP config %s: %w", path, err)
	}

	var cf mcpConfigFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	var configs []MCPServerConfig
	for name, raw := range cf.MCPServers {
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, path, err)
		}

		cfg, err := serverJSONToConfig(name, sj)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, path, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func serverJSONToConfig(name string, sj mcpServerJSON) (MCPServerConfig, error) {
	typ := strings.TrimSpace(sj.Type)
	if typ == "" {
		typ = "stdio"
	}

	command, err := expandEnvVars(sj.Command)
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding command: %w", err)
	}

	args := make([]string, len(sj.Args))
	for i, a := range sj.Args {
		args[i], err = expandEnvVars(a)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding arg[%d]: %w", i, err)
		}
	}
	if len(sj.Args) == 0 {
		args = nil
	}

	env := make(map[string]string, len(sj.Env))
	for k, v := range sj.Env {
		env[k], err = expandEnvVars(v)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding env %q: %w", k, err)
		}
	}
	if len(sj.Env) == 0 {
		env = nil
	}

	url, err := expandEnvVars(sj.URL)
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding url: %w", err)
	}

	headers := make(map[string]string, len(sj.Headers))
	for k, v := range sj.Headers {
		headers[k], err = expandEnvVars(v)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding header %q: %w", k, err)
		}
	}
	if len(sj.Headers) == 0 {
		headers = nil
	}

	return MCPServerConfig{
		Name:    name,
		Type:    typ,
		Command: command,
		Args:    args,
		Env:     env,
		URL:     url,
		Headers: headers,
	}, nil
}

// expandEnvVars expands ${VAR} and ${VAR:-default} in s.
// Missing ${VAR} with no default is an error.
func expandEnvVars(s string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		// Find next ${
		idx := strings.Index(s[i:], "${")
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		i += idx + 2 // skip past ${

		// Find closing }
		end := strings.Index(s[i:], "}")
		if end < 0 {
			// No closing brace — treat literally.
			b.WriteString("${")
			continue
		}

		expr := s[i : i+end]
		i += end + 1 // skip past }

		varName := expr
		defaultVal := ""
		hasDefault := false
		if di := strings.Index(expr, ":-"); di >= 0 {
			varName = expr[:di]
			defaultVal = expr[di+2:]
			hasDefault = true
		}

		val, ok := os.LookupEnv(varName)
		if !ok {
			if !hasDefault {
				return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
			}
			val = defaultVal
		}
		b.WriteString(val)
	}
	return b.String(), nil
}

// ParseMCPInline parses a "name:command args..." inline spec into an MCPServerConfig.
func ParseMCPInline(spec string) (MCPServerConfig, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return MCPServerConfig{}, errors.New("empty MCP inline spec")
	}

	colon := strings.Index(spec, ":")
	if colon < 0 {
		return MCPServerConfig{}, fmt.Errorf("MCP inline spec missing colon: %q (format: name:command args...)", spec)
	}

	name := strings.TrimSpace(spec[:colon])
	rest := strings.TrimSpace(spec[colon+1:])
	if name == "" {
		return MCPServerConfig{}, fmt.Errorf("MCP inline spec has empty name: %q", spec)
	}
	if rest == "" {
		return MCPServerConfig{}, fmt.Errorf("MCP inline spec has empty command: %q", spec)
	}

	parts := strings.Fields(rest)
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	return MCPServerConfig{
		Name:    name,
		Type:    "stdio",
		Command: parts[0],
		Args:    args,
	}, nil
}

// MergeMCPConfigs merges multiple layers of configs. Later layers shadow
// earlier layers by server name.
func MergeMCPConfigs(layers ...[]MCPServerConfig) []MCPServerConfig {
	seen := map[string]int{} // name -> index in result
	var result []MCPServerConfig

	for _, layer := range layers {
		for _, cfg := range layer {
			if idx, ok := seen[cfg.Name]; ok {
				result[idx] = cfg
			} else {
				seen[cfg.Name] = len(result)
				result = append(result, cfg)
			}
		}
	}
	return result
}

// DiscoverMCPConfigs loads MCP configs from all sources:
// global (~/.config/serf/mcp.json) -> project (.serf/mcp.json at git root)
// -> CLI files -> CLI inline specs. Later sources shadow earlier by name.
func DiscoverMCPConfigs(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]MCPServerConfig, error) {
	var layers [][]MCPServerConfig

	// Layer 1: Global config.
	globalPath := globalMCPConfigPath()
	if globalPath != "" {
		if configs, err := LoadMCPConfigFile(globalPath); err == nil {
			layers = append(layers, configs)
		}
		// Missing global file is not an error.
	}

	// Layer 2: Per-project config (.serf/mcp.json at git root).
	if env != nil {
		cwd := env.WorkingDirectory()
		root := execenv.GitRootOrEmpty(env, cwd)
		if root != "" {
			projPath := filepath.Join(root, ".serf", "mcp.json")
			if configs, err := LoadMCPConfigFile(projPath); err == nil {
				layers = append(layers, configs)
			}
		}
	}

	// Layer 3: CLI config files.
	for _, path := range extraFiles {
		configs, err := LoadMCPConfigFile(path)
		if err != nil {
			return nil, fmt.Errorf("--mcp-config %s: %w", path, err)
		}
		layers = append(layers, configs)
	}

	// Layer 4: CLI inline specs.
	for _, spec := range inlineSpecs {
		cfg, err := ParseMCPInline(spec)
		if err != nil {
			return nil, fmt.Errorf("--mcp %q: %w", spec, err)
		}
		layers = append(layers, []MCPServerConfig{cfg})
	}

	return MergeMCPConfigs(layers...), nil
}

// globalMCPConfigPath returns the path to the global MCP config file.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config.
func globalMCPConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "mcp.json")
}
