package agent

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/internal/bundled"
)

var builtinAgentsCache struct {
	once   sync.Once
	agents map[string]plugin.Agent
	err    error
}

var builtinAgentsFS = bundled.Agents

// runtimeOnlyAgentRoles are bundled roles that only the runtime starts. They
// are left out of the catalog a model can delegate to, so no model can ask
// for one (or for the capabilities its identity carries).
var runtimeOnlyAgentRoles = map[string]bool{memoryLibrarianRole: true}

// builtinAgents parses and returns the core agents embedded directly into the
// binary, without the runtime-only roles. These are keyed by their public
// name (no plugin prefix).
func builtinAgents() (map[string]plugin.Agent, error) {
	all, err := allBuiltinAgents()
	if err != nil {
		return nil, err
	}
	for name := range runtimeOnlyAgentRoles {
		delete(all, name)
	}
	return all, nil
}

// runtimeRoleAgent returns the bundled runtime-only role named name.
func runtimeRoleAgent(name string) (plugin.Agent, error) {
	if !runtimeOnlyAgentRoles[name] {
		return plugin.Agent{}, fmt.Errorf("%q is not a runtime role", name)
	}
	all, err := allBuiltinAgents()
	if err != nil {
		return plugin.Agent{}, err
	}
	agent, ok := all[name]
	if !ok {
		return plugin.Agent{}, fmt.Errorf("runtime role %q is not bundled", name)
	}
	return agent, nil
}

func allBuiltinAgents() (map[string]plugin.Agent, error) {
	builtinAgentsCache.once.Do(func() {
		builtinAgentsCache.agents, builtinAgentsCache.err = loadBuiltinAgents()
	})
	if builtinAgentsCache.err != nil {
		return nil, builtinAgentsCache.err
	}
	return cloneBuiltinAgents(builtinAgentsCache.agents), nil
}

func loadBuiltinAgents() (map[string]plugin.Agent, error) {
	agentsFS := builtinAgentsFS()
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
