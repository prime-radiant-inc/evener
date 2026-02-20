package agent

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed agents/*.md
var embeddedAgents embed.FS

// builtinAgents parses and returns agents from the embedded agents/ directory.
// They are keyed by their name (no plugin prefix), e.g. "explorer".
func builtinAgents() (map[string]PluginAgent, error) {
	entries, err := embeddedAgents.ReadDir("agents")
	if err != nil {
		return nil, fmt.Errorf("reading embedded agents dir: %w", err)
	}
	agents := make(map[string]PluginAgent, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := embeddedAgents.ReadFile("agents/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading embedded agent %s: %w", entry.Name(), err)
		}
		agent, err := parsePluginAgent(data, "builtin")
		if err != nil {
			return nil, fmt.Errorf("parsing embedded agent %s: %w", entry.Name(), err)
		}
		agents[agent.Name] = agent
	}
	return agents, nil
}
