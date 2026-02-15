package agent

import (
	"fmt"
	"sort"
	"strings"
)

// FormatPluginAgentsPrompt formats a system prompt section listing available
// plugin agents. Returns empty string if no agents are available.
func FormatPluginAgentsPrompt(agents map[string]PluginAgent) string {
	if len(agents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n<plugin_agents>\n")
	b.WriteString("The following plugin-defined agent types are available for spawn_agent with agent_type parameter:\n")

	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		agent := agents[name]
		b.WriteString(fmt.Sprintf("- %s: %s\n", name, agent.Description))
	}
	b.WriteString("</plugin_agents>\n")
	return b.String()
}
