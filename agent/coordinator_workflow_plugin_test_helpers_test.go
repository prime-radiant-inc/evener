package agent

import (
	"os"
	"path/filepath"
	"testing"
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

func coordinatorWorkflowPublicAgentsForTest(t *testing.T) map[string]PluginAgent {
	t.Helper()
	lp, err := LoadPlugin(coordinatorWorkflowPluginDirForTest(t))
	if err != nil {
		t.Fatalf("LoadPlugin(coordinator-workflow): %v", err)
	}
	agents := make(map[string]PluginAgent, len(lp.Agents))
	for _, agent := range lp.Agents {
		agents[agent.Name] = agent
	}
	return agents
}

func coordinatorWorkflowAgentForTest(t *testing.T, name string) PluginAgent {
	t.Helper()
	agents := coordinatorWorkflowPublicAgentsForTest(t)
	agent, ok := agents[name]
	if !ok {
		t.Fatalf("missing coordinator-workflow agent %q", name)
	}
	return agent
}
