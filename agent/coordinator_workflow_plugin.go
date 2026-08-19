package agent

import "primeradiant.com/evener/agent/plugin"

const coordinatorWorkflowPluginName = "coordinator-workflow"

func exposedAgentCatalogKey(lp plugin.Instance, rawKey string, agent plugin.Agent) string {
	if lp.Manifest.Name == coordinatorWorkflowPluginName {
		return agent.Name
	}
	return rawKey
}
