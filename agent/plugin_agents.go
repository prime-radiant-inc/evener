package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/frontmatter"
)

// PluginAgent represents a subagent defined by a plugin.
type PluginAgent struct {
	Name         string
	Description  string
	Model        string   // "inherit", "sonnet", "opus", "haiku"
	Color        string
	Tools        []string // serf canonical names (mapped at load time)
	SystemPrompt string   // markdown body
	PluginName   string   // owning plugin
}

// parsePluginAgent parses a markdown file with YAML frontmatter into a PluginAgent.
// Required frontmatter fields: name, description, model, color.
// Optional: tools (list of Claude Code tool names, mapped to serf canonical names).
func parsePluginAgent(data []byte, pluginName string) (PluginAgent, error) {
	doc, err := frontmatter.Parse(string(data))
	if err != nil {
		return PluginAgent{}, fmt.Errorf("parsing agent frontmatter: %w", err)
	}

	getString := func(key string) (string, error) {
		v, ok := doc.Meta[key]
		if !ok {
			return "", fmt.Errorf("agent missing required field %q", key)
		}
		s, ok := v.(string)
		if !ok || s == "" {
			return "", fmt.Errorf("agent field %q must be a non-empty string", key)
		}
		return s, nil
	}

	name, err := getString("name")
	if err != nil {
		return PluginAgent{}, err
	}
	description, err := getString("description")
	if err != nil {
		return PluginAgent{}, err
	}
	model, err := getString("model")
	if err != nil {
		return PluginAgent{}, err
	}
	color, err := getString("color")
	if err != nil {
		return PluginAgent{}, err
	}

	var tools []string
	if raw, ok := doc.Meta["tools"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return PluginAgent{}, fmt.Errorf("agent field \"tools\" must be a list of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return PluginAgent{}, fmt.Errorf("agent tool name must be a string, got %T", item)
			}
			tools = append(tools, MapClaudeToolName(s))
		}
	}

	return PluginAgent{
		Name:         name,
		Description:  description,
		Model:        model,
		Color:        color,
		Tools:        tools,
		SystemPrompt: doc.Body,
		PluginName:   pluginName,
	}, nil
}

// discoverPluginAgents scans a plugin's agents directories for .md files
// and returns agents namespaced as "pluginName:agentName".
func discoverPluginAgents(pluginDir string, agentsOverride json.RawMessage, pluginName string) (map[string]PluginAgent, error) {
	var override any
	if len(agentsOverride) > 0 {
		if err := json.Unmarshal(agentsOverride, &override); err != nil {
			return nil, fmt.Errorf("parsing agents override: %w", err)
		}
	}

	dirs := resolveComponentDirs(pluginDir, "agents", override)
	agents := map[string]PluginAgent{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading agents dir %q: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("reading agent file %q: %w", entry.Name(), err)
			}
			agent, err := parsePluginAgent(data, pluginName)
			if err != nil {
				return nil, fmt.Errorf("parsing agent file %q: %w", entry.Name(), err)
			}
			key := pluginName + ":" + agent.Name
			agents[key] = agent
		}
	}

	return agents, nil
}
