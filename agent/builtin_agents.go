package agent

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/internal/bundled"
)

var builtinAgentsCache struct {
	once   sync.Once
	agents map[string]plugin.Agent
	err    error
}

// builtinAgents parses and returns the core agents embedded directly into the
// binary. These are keyed by their public name (no plugin prefix).
func builtinAgents() (map[string]plugin.Agent, error) {
	builtinAgentsCache.once.Do(func() {
		builtinAgentsCache.agents, builtinAgentsCache.err = loadBuiltinAgents()
	})
	if builtinAgentsCache.err != nil {
		return nil, builtinAgentsCache.err
	}
	return cloneBuiltinAgents(builtinAgentsCache.agents), nil
}

func loadBuiltinAgents() (map[string]plugin.Agent, error) {
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

func cloneBuiltinAgents(in map[string]plugin.Agent) map[string]plugin.Agent {
	out := make(map[string]plugin.Agent, len(in))
	for name, agent := range in {
		agent.Tools = append([]string(nil), agent.Tools...)
		agent.Skills = append([]string(nil), agent.Skills...)
		agent.Tasks = append([]task.TaskTemplate(nil), agent.Tasks...)
		out[name] = agent
	}
	return out
}
