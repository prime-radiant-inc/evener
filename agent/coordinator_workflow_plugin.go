package agent

const coordinatorWorkflowPluginName = "coordinator-workflow"

func exposedAgentCatalogKey(plugin LoadedPlugin, rawKey string, agent PluginAgent) string {
	if plugin.Manifest.Name == coordinatorWorkflowPluginName {
		return agent.Name
	}
	return rawKey
}
