package agent

import (
	"fmt"
	"io/fs"
	"strings"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/internal/bundled"
)

// builtinAgents parses and returns the core agents embedded directly into the
// binary. These are keyed by their public name (no plugin prefix).
func builtinAgents() (map[string]plugin.Agent, error) {
	agentsFS := bundled.Agents()
	entries, err := fs.ReadDir(agentsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading embedded agents dir: %w", err)
	}
	agents := make(map[string]plugin.Agent, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := fs.ReadFile(agentsFS, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading embedded agent %s: %w", entry.Name(), err)
		}
		agent, err := plugin.ParseAgent(data, "builtin")
		if err != nil {
			return nil, fmt.Errorf("parsing embedded agent %s: %w", entry.Name(), err)
		}
		agents[agent.Name] = agent
	}
	return agents, nil
}
