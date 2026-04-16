package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/frontmatter"
)

// TaskTemplate defines a default task in an agent's workflow.
type TaskTemplate struct {
	Title           string `json:"title"`
	Prompt          string `json:"prompt"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Type            string `json:"type,omitempty"`
	Insert          string `json:"insert,omitempty"`
}

// PluginAgent represents a subagent defined by a plugin.
type PluginAgent struct {
	Name         string
	Description  string
	Model        string // "inherit", "sonnet", "opus", "haiku"
	Color        string
	AllTools     bool
	Tools        []string       // serf canonical names (mapped at load time)
	Skills       []string       // skill names to auto-inject at dispatch time
	Tasks        []TaskTemplate // default workflow tasks from YAML
	SystemPrompt string         // markdown body
	PluginName   string         // owning plugin
}

// parsePluginAgent parses a markdown file with YAML frontmatter into a PluginAgent.
// Required frontmatter fields: name, description.
// Optional: model, color, tools (mapped to serf canonical names, or the scalar string "all"), skills (plain strings).
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

	// Model and color are optional; default to "inherit" and "blue".
	model := "inherit"
	if v, ok := doc.Meta["model"].(string); ok && v != "" {
		model = v
	}
	color := "blue"
	if v, ok := doc.Meta["color"].(string); ok && v != "" {
		color = v
	}

	var (
		allTools bool
		tools    []string
	)
	if raw, ok := doc.Meta["tools"]; ok {
		switch v := raw.(type) {
		case string:
			switch strings.TrimSpace(strings.ToLower(v)) {
			case "all":
				allTools = true
			case "*":
				return PluginAgent{}, fmt.Errorf("agent field \"tools\" uses the scalar form \"all\" for unrestricted access; use `tools: all`")
			default:
				return PluginAgent{}, fmt.Errorf("agent field \"tools\" must be a list of strings or the string \"all\"")
			}
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return PluginAgent{}, fmt.Errorf("agent tool name must be a string, got %T", item)
				}
				switch strings.TrimSpace(strings.ToLower(s)) {
				case "all", "*":
					return PluginAgent{}, fmt.Errorf("agent field \"tools\" uses the scalar form \"all\" for unrestricted access; use `tools: all`")
				default:
					tools = append(tools, MapClaudeToolName(s))
				}
			}
		default:
			return PluginAgent{}, fmt.Errorf("agent field \"tools\" must be a list of strings or the string \"all\"")
		}
	}

	var skills []string
	if raw, ok := doc.Meta["skills"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return PluginAgent{}, fmt.Errorf("agent field \"skills\" must be a list of strings")
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return PluginAgent{}, fmt.Errorf("agent skill name must be a string, got %T", item)
			}
			skills = append(skills, s)
		}
	}

	var tasks []TaskTemplate
	if raw, ok := doc.Meta["tasks"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return PluginAgent{}, fmt.Errorf("agent field \"tasks\" must be a list")
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return PluginAgent{}, fmt.Errorf("each task must be an object with title and prompt")
			}
			tt := TaskTemplate{}
			if v, ok := m["title"].(string); ok {
				tt.Title = v
			}
			if v, ok := m["prompt"].(string); ok {
				tt.Prompt = v
			}
			if v, ok := m["reasoning_effort"].(string); ok {
				tt.ReasoningEffort = v
			}
			if v, ok := m["type"].(string); ok {
				tt.Type = v
			}
			if v, ok := m["insert"].(string); ok {
				tt.Insert = v
			}
			tasks = append(tasks, tt)
		}
	}

	return PluginAgent{
		Name:         name,
		Description:  description,
		Model:        model,
		Color:        color,
		AllTools:     allTools,
		Tools:        tools,
		Skills:       skills,
		Tasks:        tasks,
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
