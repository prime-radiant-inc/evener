package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/internal/frontmatter"
	"primeradiant.com/serf/agent/internal/toolname"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// Agent represents a subagent defined by a plugin.
type Agent struct {
	Name         string              // agent name (unqualified)
	Description  string              // when-to-use description shown to the model
	Model        string              // "inherit", "sonnet", "opus", "haiku"
	Color        string              // display color hint from frontmatter
	AllTools     bool                // when true, the agent may use every tool (Tools is ignored)
	Tools        []string            // serf canonical names (mapped at load time)
	Skills       []string            // skill names to auto-inject at dispatch time
	Tasks        []task.TaskTemplate // default workflow tasks from YAML
	SystemPrompt string              // markdown body
	PluginName   string              // owning plugin
}

// splitCommaList splits a comma-separated string into trimmed, non-empty
// elements. Claude Code agent frontmatter uses this form for tools/skills.
func splitCommaList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseAgent parses a markdown file with YAML frontmatter into an Agent.
// Required frontmatter fields: name, description.
// Optional: model, color, tools (mapped to serf canonical names, or the scalar string "all"), skills (plain strings).
func ParseAgent(data []byte, pluginName string) (Agent, error) {
	doc, err := frontmatter.Parse(string(data))
	if err != nil {
		return Agent{}, fmt.Errorf("parsing agent frontmatter: %w", err)
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
		return Agent{}, err
	}
	description, err := getString("description")
	if err != nil {
		return Agent{}, err
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
		var names []string
		switch v := raw.(type) {
		case string:
			switch strings.TrimSpace(strings.ToLower(v)) {
			case "all":
				allTools = true
			case "*":
				return Agent{}, errors.New("agent field \"tools\" uses the scalar form \"all\" for unrestricted access; use `tools: all`")
			default:
				// Claude Code frontmatter writes tools as a
				// comma-separated string (e.g. "Read, Grep, Glob").
				names = splitCommaList(v)
			}
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return Agent{}, fmt.Errorf("agent tool name must be a string, got %T", item)
				}
				names = append(names, s)
			}
		default:
			return Agent{}, errors.New("agent field \"tools\" must be a list of strings, a comma-separated string, or the string \"all\"")
		}
		for _, s := range names {
			switch strings.TrimSpace(strings.ToLower(s)) {
			case "all", "*":
				return Agent{}, errors.New("agent field \"tools\" uses the scalar form \"all\" for unrestricted access; use `tools: all`")
			case "":
				// tolerate empty list entries the same way as empty comma segments
			default:
				tools = append(tools, toolname.ClaudeToSerf(s))
			}
		}
	}

	var skills []string
	if raw, ok := doc.Meta["skills"]; ok {
		switch v := raw.(type) {
		case string:
			// Claude Code frontmatter writes skills as a plain
			// (possibly comma-separated) string.
			skills = splitCommaList(v)
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return Agent{}, fmt.Errorf("agent skill name must be a string, got %T", item)
				}
				if strings.TrimSpace(s) == "" {
					continue // tolerate empty list entries like empty comma segments
				}
				skills = append(skills, s)
			}
		default:
			return Agent{}, errors.New("agent field \"skills\" must be a list of strings or a comma-separated string")
		}
	}

	var tasks []task.TaskTemplate
	if raw, ok := doc.Meta["tasks"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return Agent{}, errors.New("agent field \"tasks\" must be a list")
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return Agent{}, errors.New("each task must be an object with title and prompt")
			}
			tt := task.TaskTemplate{}
			if v, ok := m["title"].(string); ok {
				tt.Title = v
			}
			if v, ok := m["prompt"].(string); ok {
				tt.Prompt = v
			}
			if v, ok := m["reasoning_effort"].(string); ok {
				tt.ReasoningEffort = v
				if err := llm.ValidateReasoningEffort(tt.ReasoningEffort); err != nil {
					return Agent{}, fmt.Errorf("agent task %q: %w", tt.Title, err)
				}
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

	return Agent{
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
func discoverPluginAgents(pluginDir string, agentsOverride json.RawMessage, pluginName string) (map[string]Agent, error) {
	var override any
	if len(agentsOverride) > 0 {
		if err := json.Unmarshal(agentsOverride, &override); err != nil {
			return nil, fmt.Errorf("parsing agents override: %w", err)
		}
	}

	files, err := componentMarkdownFiles(resolveComponentDirs(pluginDir, "agents", override))
	if err != nil {
		return nil, err
	}

	agents := map[string]Agent{}
	for _, file := range files {
		data, err := pluginReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading agent file %q: %w", filepath.Base(file), err)
		}
		agent, err := ParseAgent(data, pluginName)
		if err != nil {
			return nil, fmt.Errorf("parsing agent file %q: %w", filepath.Base(file), err)
		}
		key := pluginName + ":" + agent.Name
		agents[key] = agent
	}

	return agents, nil
}
