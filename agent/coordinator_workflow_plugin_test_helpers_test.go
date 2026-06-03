package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

func coordinatorWorkflowPluginDirForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	return filepath.Join(wd, "bundled_plugins", "coordinator-workflow")
}

func coordinatorWorkflowSessionConfig(t *testing.T, cfg SessionConfig) SessionConfig {
	t.Helper()
	cfg.PluginDirs = append([]string{coordinatorWorkflowPluginDirForTest(t)}, cfg.PluginDirs...)
	return cfg
}

func coordinatorWorkflowPublicAgentsForTest(t *testing.T) map[string]plugin.Agent {
	t.Helper()
	lp, err := plugin.Load(coordinatorWorkflowPluginDirForTest(t))
	if err != nil {
		t.Fatalf("plugin.Load(coordinator-workflow): %v", err)
	}
	agents := make(map[string]plugin.Agent, len(lp.Agents))
	for _, agent := range lp.Agents {
		agents[agent.Name] = agent
	}
	return agents
}

func coordinatorWorkflowAgentForTest(t *testing.T, name string) plugin.Agent {
	t.Helper()
	agents := coordinatorWorkflowPublicAgentsForTest(t)
	agent, ok := agents[name]
	if !ok {
		t.Fatalf("missing coordinator-workflow agent %q", name)
	}
	return agent
}
