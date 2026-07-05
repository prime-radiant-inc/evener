package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/internal/frontmatter"
)

// Command represents a slash command defined by a plugin. Invoking it expands
// Body against the user-supplied argument string (see the agent/command
// package): $ARGUMENTS, $1..$9, backtick command substitution, and @file
// inclusion.
type Command struct {
	Name         string   // command name, derived from the command's .md filename (not frontmatter — see ParseCommand)
	Description  string   // shown in command catalogs/autocomplete
	ArgumentHint string   // display hint for the arguments the command expects
	Model        string   // requested per-turn model override (parsed; not yet enforced — see design §14)
	AllowedTools []string // requested per-turn tool restriction, verbatim as declared (parsed; not yet enforced — see design §14)
	Body         string   // markdown template body
	PluginName   string   // owning plugin
}

// ParseCommand parses a markdown file with optional YAML frontmatter into a
// Command named name. Unlike agents (and matching real Claude Code plugin
// commands, which are commonly frontmatter-less or omit "name" — the filename
// is authoritative), a command has no required frontmatter fields: a bare
// markdown file with no leading "---" is a valid command whose entire content
// is the template body. Recognized (all optional) fields: description,
// argument-hint, allowed-tools (a list of strings; a malformed or
// non-list/non-string value degrades to no restriction rather than erroring,
// like skill.parseSkillFile's allowed-tools), model. The only error is
// genuinely malformed frontmatter YAML.
func ParseCommand(data []byte, name string, pluginName string) (Command, error) {
	doc, err := frontmatter.Parse(string(data))
	if err != nil {
		return Command{}, fmt.Errorf("parsing command frontmatter: %w", err)
	}

	description := ""
	if v, ok := doc.Meta["description"].(string); ok {
		description = v
	}
	argumentHint := ""
	if v, ok := doc.Meta["argument-hint"].(string); ok {
		argumentHint = v
	}
	model := ""
	if v, ok := doc.Meta["model"].(string); ok {
		model = v
	}

	var allowedTools []string
	if raw, ok := doc.Meta["allowed-tools"]; ok {
		if items, ok := raw.([]any); ok {
			for _, item := range items {
				if s, ok := item.(string); ok {
					allowedTools = append(allowedTools, s)
				}
			}
		}
	}

	return Command{
		Name:         name,
		Description:  description,
		ArgumentHint: argumentHint,
		Model:        model,
		AllowedTools: allowedTools,
		Body:         doc.Body,
		PluginName:   pluginName,
	}, nil
}

// discoverPluginCommands scans a plugin's commands directories for .md files
// and returns commands namespaced as "pluginName:commandName", where
// commandName is each file's basename with the .md extension stripped (a
// frontmatter "name" field, if a plugin author includes one, is not read —
// see ParseCommand).
func discoverPluginCommands(pluginDir string, commandsOverride json.RawMessage, pluginName string) (map[string]Command, error) {
	var override any
	if len(commandsOverride) > 0 {
		if err := json.Unmarshal(commandsOverride, &override); err != nil {
			return nil, fmt.Errorf("parsing commands override: %w", err)
		}
	}

	dirs := resolveComponentDirs(pluginDir, "commands", override)
	commands := map[string]Command{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading commands dir %q: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("reading command file %q: %w", entry.Name(), err)
			}
			name := strings.TrimSuffix(entry.Name(), ".md")
			command, err := ParseCommand(data, name, pluginName)
			if err != nil {
				return nil, fmt.Errorf("parsing command file %q: %w", entry.Name(), err)
			}
			key := pluginName + ":" + command.Name
			commands[key] = command
		}
	}

	return commands, nil
}

// ResolveCommand looks up a plugin command by name in a namespaced
// ("plugin:command") map, such as the union of every loaded plugin's Commands.
// It tries an exact match first (the fully-qualified "plugin:command" form the
// map is keyed by), then falls back to matching name against the unqualified
// suffix of each key — mirroring skill.ResolveSkillContent's bare-name lookup.
// As with skill resolution, an unqualified name matching more than one
// plugin's command resolves to whichever is found first in map iteration
// order; the fully-qualified form disambiguates.
func ResolveCommand(commands map[string]Command, name string) (Command, bool) {
	if cmd, ok := commands[name]; ok {
		return cmd, true
	}
	for key, cmd := range commands {
		if _, unqualified, found := strings.Cut(key, ":"); found && unqualified == name {
			return cmd, true
		}
	}
	return Command{}, false
}
