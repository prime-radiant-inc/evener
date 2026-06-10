package agent

import (
	"sort"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/skill"
)

// ToolInfo describes a registered tool and its source.
type ToolInfo struct {
	Name   string `json:"name"`   // e.g. "shell", "linear__search"
	Source string `json:"source"` // "core", "mcp:<server>", "custom"
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

// JobStatusInfo describes an active or recent job.
type JobStatusInfo struct {
	JobID         string `json:"job_id"`
	JobType       string `json:"job_type"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	TranscriptRef string `json:"transcript_ref,omitempty"`
	OutputBytes   int64  `json:"output_bytes"`
	ExitCode      *int   `json:"exit_code,omitempty"`
}

// HookEventStatus describes a single hook event's registration state and
// compatibility tier for the /status endpoint.
// Tier: Supported=true events are "claude-compatible-subset";
// Supported=false events are "reserved-placeholder" (recognized by serf but
// not yet fired). The Tier field carries the exact label from plugin.EventTier.
type HookEventStatus struct {
	Event     plugin.HookEvent `json:"event"`
	Count     int              `json:"count"`
	Tier      string           `json:"tier"`
	Supported bool             `json:"supported"`
}

// DetailedStatus captures the full session configuration for /status display.
type DetailedStatus struct {
	Tools   []ToolInfo             `json:"tools,omitempty"`   // every registered tool and its source
	MCP     []mcpconfig.ServerInfo `json:"mcp,omitempty"`     // connected MCP servers
	Skills  []skill.SkillMeta      `json:"skills,omitempty"`  // discovered skills, sorted by name
	Plugins []PluginInfo           `json:"plugins,omitempty"` // loaded plugins
	// Hooks maps each hook event to the number of registered hooks for it.
	// Retained for backward compatibility; HookEvents carries richer per-event data.
	Hooks map[plugin.HookEvent]int `json:"hooks,omitempty"`
	// HookEvents lists all registered hook events (supported) plus any
	// recognized-but-unsupported events declared by loaded plugins.
	HookEvents []HookEventStatus `json:"hook_events,omitempty"`
	Jobs       []JobStatusInfo   `json:"jobs,omitempty"`   // active and recent jobs
	Agents     []string          `json:"agents,omitempty"` // public agent names
}

// DetailedStatus builds a snapshot of the session's loaded tools, MCP servers,
// skills, plugins, hooks, jobs, and public agent names.
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

	// Hooks: populate the backward-compatible map and the richer HookEvents slice.
	if s.hookRunner != nil {
		// Legacy map: every registered hook per event (registered, not necessarily
		// runnable). Retained for backward compatibility.
		if summary := s.hookRunner.Summary(); len(summary) > 0 {
			ds.Hooks = summary
		}
		// HookEvents supported entries count only hooks that can ACTUALLY run:
		// hooks with an unsupported handler type or an invalid matcher are
		// dispatch-time dead and surface as load warnings, not as active hooks.
		for event, count := range s.hookRunner.SupportedSummary() {
			ds.HookEvents = append(ds.HookEvents, HookEventStatus{
				Event:     event,
				Count:     count,
				Tier:      plugin.EventTier(event),
				Supported: true,
			})
		}
	}
	// Append recognized-but-unsupported events (declared by plugins but not
	// fired by serf). These have Count=0 and Tier="reserved-placeholder".
	for event := range s.unsupportedPluginHookEvents {
		ds.HookEvents = append(ds.HookEvents, HookEventStatus{
			Event:     event,
			Count:     0,
			Tier:      plugin.EventTier(event),
			Supported: false,
		})
	}
	// Sort HookEvents by event name for deterministic output.
	sort.Slice(ds.HookEvents, func(i, j int) bool {
		return ds.HookEvents[i].Event < ds.HookEvents[j].Event
	})

	// Jobs.
	if s.jobManager != nil {
		ds.Jobs = projectJobStatusInfos(s.jobManager.list(listFilter{}))
	}

	// Plugin agent names (sorted).
	for name := range s.pluginAgents {
		ds.Agents = append(ds.Agents, name)
	}
	sort.Strings(ds.Agents)

	return ds
}

func projectJobStatusInfos(records []*jobstore.JobRecord) []JobStatusInfo {
	jobs := make([]JobStatusInfo, 0, len(records))
	for _, rec := range records {
		jobs = append(jobs, JobStatusInfo{
			JobID:         rec.JobID,
			JobType:       string(rec.Type),
			Status:        string(rec.Status),
			Reason:        rec.Reason,
			TranscriptRef: rec.TranscriptRef,
			OutputBytes:   rec.OutputBytes,
			ExitCode:      rec.ExitCode,
		})
	}
	return jobs
}
