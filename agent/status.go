package agent

import "sort"

// ToolInfo describes a registered tool and its source.
type ToolInfo struct {
	Name   string `json:"name"`   // e.g. "shell", "linear__search"
	Source string `json:"source"` // "core", "mcp:<server>", "custom"
}

// MCPServerInfo describes a connected MCP server and its tools.
type MCPServerInfo struct {
	Name  string   `json:"name"`
	Tools []string `json:"tools"` // namespaced tool names
}

// PluginInfo summarizes a loaded plugin.
type PluginInfo struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	SkillCount int    `json:"skill_count"`
	AgentCount int    `json:"agent_count"`
	HookCount  int    `json:"hook_count"`
	MCPCount   int    `json:"mcp_count"`
}

// SubagentInfo describes an active sub-agent.
type SubagentInfo struct {
	ID        string         `json:"id"`
	Status    SubAgentStatus `json:"status"`
	TurnsUsed int            `json:"turns_used"`
}

// DetailedStatus captures the full session configuration for /status display.
type DetailedStatus struct {
	Tools     []ToolInfo        `json:"tools,omitempty"`
	MCP       []MCPServerInfo   `json:"mcp,omitempty"`
	Skills    []SkillMeta       `json:"skills,omitempty"`
	Plugins   []PluginInfo      `json:"plugins,omitempty"`
	Hooks     map[HookEvent]int `json:"hooks,omitempty"`
	Subagents []SubagentInfo    `json:"subagents,omitempty"`
	Agents    []string          `json:"agents,omitempty"` // plugin agent names
}

// DetailedStatus builds a snapshot of the session's loaded tools, MCP servers,
// skills, plugins, hooks, subagents, and plugin agents.
func (s *Session) DetailedStatus() DetailedStatus {
	var ds DetailedStatus

	// Build MCP tool → server name map for tool categorization.
	mcpToolServer := map[string]string{}
	if s.mcpMgr != nil {
		servers := s.mcpMgr.Servers()
		ds.MCP = servers
		for _, srv := range servers {
			for _, toolName := range srv.Tools {
				mcpToolServer[toolName] = srv.Name
			}
		}
	}

	// Categorize registered tools.
	for _, name := range s.reg.Names() {
		source := "custom"
		if s.coreToolNames[name] {
			source = "core"
		} else if srv, ok := mcpToolServer[name]; ok {
			source = "mcp:" + srv
		}
		ds.Tools = append(ds.Tools, ToolInfo{Name: name, Source: source})
	}

	// Skills (sorted by name).
	for _, meta := range s.skills {
		ds.Skills = append(ds.Skills, meta)
	}
	sort.Slice(ds.Skills, func(i, j int) bool {
		return ds.Skills[i].Name < ds.Skills[j].Name
	})

	// Plugins.
	for _, p := range s.plugins {
		hookCount := 0
		for _, hooks := range p.Hooks {
			hookCount += len(hooks)
		}
		ds.Plugins = append(ds.Plugins, PluginInfo{
			Name:       p.Manifest.Name,
			Version:    p.Manifest.Version,
			SkillCount: len(p.Skills),
			AgentCount: len(p.Agents),
			HookCount:  hookCount,
			MCPCount:   len(p.MCPConfigs),
		})
	}

	// Hooks.
	if s.hookRunner != nil {
		summary := s.hookRunner.Summary()
		if len(summary) > 0 {
			ds.Hooks = summary
		}
	}

	// Subagents.
	s.mu.Lock()
	for id, sub := range s.subagents {
		sub.mu.Lock()
		ds.Subagents = append(ds.Subagents, SubagentInfo{
			ID:        id,
			Status:    sub.status,
			TurnsUsed: sub.turnsUsed,
		})
		sub.mu.Unlock()
	}
	s.mu.Unlock()

	// Plugin agent names (sorted).
	for name := range s.pluginAgents {
		ds.Agents = append(ds.Agents, name)
	}
	sort.Strings(ds.Agents)

	return ds
}
