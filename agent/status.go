package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
)

var detailedStatusMCPServers = func(s *Session) []mcpconfig.ServerInfo { return s.mcpMgr.Servers() }

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
	JobID            string `json:"job_id"`
	JobType          string `json:"job_type"`
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
	Resumable        *bool  `json:"resumable,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	OutputBytes      int64  `json:"output_bytes"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Command          string `json:"command,omitempty"`
	Intent           string `json:"intent,omitempty"`
	Task             string `json:"task,omitempty"`
}

// DelegateStatusInfo is the stable delegate read model exposed by
// DetailedStatus. AppWire maps these values into EvenerDelegateInfo without
// deriving them from Jobs.
type DelegateStatusInfo struct {
	DelegateID          string                       `json:"delegate_id"`
	OwnerSessionID      string                       `json:"owner_session_id"`
	RootSessionID       string                       `json:"root_session_id"`
	ChildSessionID      string                       `json:"child_session_id"`
	TranscriptRef       string                       `json:"transcript_ref"`
	ParentDelegateID    string                       `json:"parent_delegate_id,omitempty"`
	Type                string                       `json:"type"`
	Lifecycle           string                       `json:"lifecycle"`
	Phase               string                       `json:"phase"`
	Status              string                       `json:"status"`
	Outcome             string                       `json:"outcome,omitempty"`
	Reason              string                       `json:"reason,omitempty"`
	Terminal            bool                         `json:"terminal,omitempty"`
	Resumable           bool                         `json:"resumable"`
	NeedsAttention      bool                         `json:"needs_attention"`
	NotResumableReason  string                       `json:"not_resumable_reason,omitempty"`
	ProjectionRevision  uint64                       `json:"projection_revision"`
	Task                string                       `json:"task,omitempty"`
	Description         string                       `json:"description,omitempty"`
	AgentType           string                       `json:"agent_type,omitempty"`
	RequestedModel      string                       `json:"requested_model,omitempty"`
	ResolvedProfileID   string                       `json:"resolved_profile_id,omitempty"`
	ResolvedModel       string                       `json:"resolved_model,omitempty"`
	Model               string                       `json:"model,omitempty"`
	ReasoningEffort     string                       `json:"reasoning_effort,omitempty"`
	OriginTurnID        string                       `json:"origin_turn_id,omitempty"`
	OriginToolCallID    string                       `json:"origin_tool_call_id,omitempty"`
	OriginItemID        string                       `json:"origin_item_id,omitempty"`
	RunStartedAt        string                       `json:"run_started_at,omitempty"`
	RunEndedAt          string                       `json:"run_ended_at,omitempty"`
	LatestActivityAt    string                       `json:"latest_activity_at,omitempty"`
	RunningForMS        *int64                       `json:"running_for_ms,omitempty"`
	QuietForMS          *int64                       `json:"quiet_for_ms,omitempty"`
	DurationMS          *int64                       `json:"duration_ms,omitempty"`
	PacketKind          string                       `json:"packet_kind,omitempty"`
	Message             json.RawMessage              `json:"message,omitempty"`
	StructuredResult    json.RawMessage              `json:"structured_result,omitempty"`
	StructuredValid     *bool                        `json:"structured_result_valid,omitempty"`
	StructuredReason    string                       `json:"structured_result_reason,omitempty"`
	Warnings            []string                     `json:"warnings,omitempty"`
	Diagnostics         []string                     `json:"diagnostics,omitempty"`
	ExhaustionBudget    string                       `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit     int                          `json:"exhaustion_limit,omitempty"`
	ExhaustionResumable *bool                        `json:"exhaustion_resumable,omitempty"`
	DelegationAllowance int                          `json:"delegation_allowance,omitempty"`
	ParentWatchGranted  bool                         `json:"parent_watch_granted,omitempty"`
	Usage               *appwire.EvenerUsage         `json:"usage,omitempty"`
	Worktree            *appwire.JobActivityWorktree `json:"worktree,omitempty"`
}

// HookEventStatus describes a single hook event's registration state and
// compatibility tier for typed diagnostics.
// Tier: Supported=true events are "claude-compatible-subset";
// Supported=false events are "reserved-placeholder" (recognized by evener but
// not yet fired). The Tier field carries the exact label from plugin.EventTier.
type HookEventStatus struct {
	Event     plugin.HookEvent `json:"event"`
	Count     int              `json:"count"`
	Tier      string           `json:"tier,omitempty"`
	Supported bool             `json:"supported"`
}

// DetailedStatus captures the full session configuration for typed diagnostics.
type DetailedStatus struct {
	Tools   []ToolInfo             `json:"tools,omitempty"`   // every registered tool and its source
	MCP     []mcpconfig.ServerInfo `json:"mcp,omitempty"`     // connected MCP servers
	Skills  []skill.SkillMeta      `json:"skills,omitempty"`  // discovered skills, sorted by name
	Plugins []PluginInfo           `json:"plugins,omitempty"` // loaded plugins
	// HookEvents lists all registered hook events (supported) plus any
	// recognized-but-unsupported events declared by loaded plugins.
	HookEvents []HookEventStatus    `json:"hook_events,omitempty"`
	Jobs       []JobStatusInfo      `json:"jobs,omitempty"` // active and recent jobs
	Delegates  []DelegateStatusInfo `json:"delegates,omitempty"`
	Agents     []string             `json:"agents,omitempty"` // public agent names
	// TurnSlots reports tree-counter occupancy while any delegate-turn slot is
	// held; nil when idle.
	TurnSlots *turnSlotOccupancy `json:"turn_slots,omitempty"`
}

const detailedStatusTerminalJobsLimit = 50

// DetailedStatus builds a snapshot of the session's loaded tools, MCP servers,
// skills, plugins, hooks, jobs, and public agent names.
func (s *Session) DetailedStatus() DetailedStatus {
	ds := DetailedStatus{Plugins: make([]PluginInfo, 0)}
	now := s.sclock().Now().UTC()

	// Build MCP tool → server name map for tool categorization.
	mcpToolServer := map[string]string{}
	if s.mcpMgr != nil {
		servers := detailedStatusMCPServers(s)
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

	// Skills are projected through the canonical, path-free catalog helper.
	ds.Skills = skill.CatalogEntries(s.skills)

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

	// HookEvents supported entries count only hooks that can ACTUALLY run:
	// hooks with an unsupported handler type or an invalid matcher are
	// dispatch-time dead and surface as load warnings, not as active hooks.
	if s.hookRunner != nil {
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
	// fired by evener). These have Count=0 and Tier="reserved-placeholder".
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
		ds.Jobs = projectJobStatusInfos(detailedStatusJobRecords(s.jobManager.list(listFilter{})))
	}
	if s.delegateController != nil {
		rootID := s.delegateController.rootSessionID
		for _, row := range s.delegateController.Snapshot().rows {
			if row.descriptor.OwnerSessionID == s.ID() {
				ds.Delegates = append(ds.Delegates, delegateStatusInfoFromSnapshot(now, rootID, row))
			}
		}
	}

	// Plugin agent names (sorted).
	for name := range s.pluginAgents {
		ds.Agents = append(ds.Agents, name)
	}
	sort.Strings(ds.Agents)

	ds.TurnSlots = turnSlotOccupancyOf(s)

	return ds
}

// SessionOwnsDelegate reads durable ownership without projecting transcript
// attention. A sibling's transcript cannot determine whether a daemon owns a child.
func SessionOwnsDelegate(ctx context.Context, stateDir, ownerSessionID, childSessionID string) (bool, error) {
	if err := schema.ValidateSessionID(ownerSessionID); err != nil {
		return false, err
	}
	if err := schema.ValidateSessionID(childSessionID); err != nil {
		return false, err
	}
	meta, err := schema.LoadSessionMeta(stateDir, ownerSessionID)
	if err != nil {
		return false, err
	}
	rootID := activityRootIDFromMeta(ownerSessionID, meta)
	if err := schema.ValidateSessionID(rootID); err != nil {
		return false, err
	}
	path := filepath.Join(jobsDir(stateDir, rootID), "delegates.jsonl")
	result, err := historicalDelegateFoldCache.Get(ctx, path, extendHistoricalDelegateFold)
	if err != nil {
		return false, err
	}
	for _, aggregate := range result.Value.state {
		if aggregate != nil && aggregate.Descriptor.OwnerSessionID == ownerSessionID && aggregate.Descriptor.ChildSessionID == childSessionID {
			return true, nil
		}
	}
	if result.Value.tornTail {
		return false, fmt.Errorf("delegate ownership journal has an incomplete trailing batch: %s", path)
	}
	return false, nil
}

// LoadSessionDelegateStatus projects a cold session's stable delegate rows
// from the root journal without constructing a Session or opening an
// append-capable store. ctx is checked between decoded delegate-journal
// records the same way the job-activity tree loader's scans are; pass
// context.Background() when no request-scoped context is available.
func LoadSessionDelegateStatus(ctx context.Context, stateDir, sessionID string) ([]DelegateStatusInfo, []string, error) {
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		return nil, nil, err
	}
	rootID := activityRootIDFromMeta(sessionID, meta)
	rows, diagnostics, err := loadHistoricalStableActivityWithAttention(ctx, stateDir, rootID, sessionID)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]DelegateStatusInfo, 0, len(ids))
	now := time.Now().UTC()
	for _, id := range ids {
		out = append(out, delegateStatusInfoFromSnapshot(now, rootID, rows[id]))
	}
	return out, diagnostics, nil
}

func delegateStatusInfoFromSnapshot(now time.Time, rootID string, row delegateSnapshot) DelegateStatusInfo {
	descriptor := row.descriptor
	timing := projectStableDelegateStatus(now, row)
	out := DelegateStatusInfo{
		DelegateID:          row.id,
		OwnerSessionID:      descriptor.OwnerSessionID,
		RootSessionID:       rootID,
		ChildSessionID:      descriptor.ChildSessionID,
		TranscriptRef:       descriptor.TranscriptRef,
		ParentDelegateID:    descriptor.ParentDelegateID,
		Type:                "delegate",
		Lifecycle:           string(row.lifecycle),
		Phase:               string(row.phase),
		Status:              string(row.lifecycle),
		Resumable:           row.resumable,
		NeedsAttention:      row.needsAttention,
		NotResumableReason:  row.notResumableReason,
		ProjectionRevision:  row.revision,
		Task:                descriptor.Task,
		Description:         descriptor.Description,
		AgentType:           descriptor.AgentType,
		RequestedModel:      descriptor.RequestedModel,
		ResolvedProfileID:   descriptor.ResolvedProfileID,
		ResolvedModel:       descriptor.ResolvedModel,
		Model:               descriptor.ResolvedModel,
		ReasoningEffort:     descriptor.Config.ReasoningEffort,
		OriginTurnID:        descriptor.OriginTurnID,
		OriginToolCallID:    descriptor.OriginToolCallID,
		OriginItemID:        descriptor.OriginItemID,
		RunStartedAt:        timing.RunStartedAt,
		LatestActivityAt:    timing.LatestActivityAt,
		RunningForMS:        cloneInt64(timing.RunningForMS),
		QuietForMS:          cloneInt64(timing.QuietForMS),
		DurationMS:          cloneInt64(timing.DurationMS),
		DelegationAllowance: descriptor.DelegationAllowance,
		ParentWatchGranted:  descriptor.ParentWatchGranted,
	}
	if row.lastOutcome != nil {
		out.Outcome = string(row.lastOutcome.Status)
		out.Reason = row.lastOutcome.Reason
		out.Terminal = !row.currentRunOpen
		if !row.lastOutcome.EndedAt.IsZero() {
			out.RunEndedAt = row.lastOutcome.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		out.ExhaustionBudget = string(row.lastOutcome.ExhaustionBudget)
		out.ExhaustionLimit = row.lastOutcome.ExhaustionLimit
		out.ExhaustionResumable = cloneBool(row.lastOutcome.Resumable)
	}
	if packet := row.latestPacket; packet != nil {
		out.PacketKind = string(packet.Kind)
		out.Message = append(json.RawMessage(nil), packet.Message...)
		out.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
		out.StructuredValid = cloneBool(packet.StructuredResultValid)
		out.StructuredReason = packet.StructuredResultReason
		out.Warnings = append([]string(nil), packet.Warnings...)
		if len(packet.Metadata) != 0 {
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
				out.Diagnostics = append(out.Diagnostics, "delegate terminal metadata is invalid")
			} else {
				out.Usage = activityUsageFromCumulative(metadata.CumulativeUsage)
				if metadata.Worktree != nil {
					out.Worktree = &appwire.JobActivityWorktree{
						Path: metadata.Worktree.Path, Branch: metadata.Worktree.Branch,
						HeadSHA: metadata.Worktree.HeadSHA, Ahead: metadata.Worktree.Ahead, Dirty: metadata.Worktree.Dirty,
					}
				}
			}
		}
	}
	return out
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func delegateUpdatedDataFromStatus(info DelegateStatusInfo) events.DelegateUpdatedData {
	out := events.DelegateUpdatedData{
		DelegateID: info.DelegateID, OwnerSessionID: info.OwnerSessionID, RootSessionID: info.RootSessionID,
		ChildSessionID: info.ChildSessionID, TranscriptRef: info.TranscriptRef, ParentDelegateID: info.ParentDelegateID,
		Type: info.Type, Lifecycle: info.Lifecycle, Phase: info.Phase, Status: info.Status, Outcome: info.Outcome,
		Reason: info.Reason, Terminal: info.Terminal, Resumable: info.Resumable, NeedsAttention: info.NeedsAttention, NotResumableReason: info.NotResumableReason,
		ProjectionRevision: info.ProjectionRevision, Task: info.Task, Description: info.Description, AgentType: info.AgentType,
		RequestedModel: info.RequestedModel, ResolvedProfileID: info.ResolvedProfileID, ResolvedModel: info.ResolvedModel,
		Model: info.Model, ReasoningEffort: info.ReasoningEffort, OriginTurnID: info.OriginTurnID,
		OriginToolCallID: info.OriginToolCallID, OriginItemID: info.OriginItemID, RunStartedAt: info.RunStartedAt,
		RunEndedAt: info.RunEndedAt, LatestActivityAt: info.LatestActivityAt, RunningForMS: cloneInt64(info.RunningForMS),
		QuietForMS: cloneInt64(info.QuietForMS), DurationMS: cloneInt64(info.DurationMS), PacketKind: info.PacketKind,
		Message: append(json.RawMessage(nil), info.Message...), StructuredResult: append(json.RawMessage(nil), info.StructuredResult...),
		StructuredValid: cloneBool(info.StructuredValid), StructuredReason: info.StructuredReason,
		Warnings: append([]string(nil), info.Warnings...), Diagnostics: append([]string(nil), info.Diagnostics...),
		ExhaustionBudget: info.ExhaustionBudget, ExhaustionLimit: info.ExhaustionLimit,
		ExhaustionResumable: cloneBool(info.ExhaustionResumable), DelegationAllowance: info.DelegationAllowance,
		ParentWatchGranted: info.ParentWatchGranted,
	}
	if info.Usage != nil {
		out.Usage = &events.DelegateUsageData{
			InputTokens: info.Usage.InputTokens, OutputTokens: info.Usage.OutputTokens,
			CacheReadTokens: info.Usage.CacheReadTokens, TotalTokens: info.Usage.TotalTokens,
		}
	}
	if info.Worktree != nil {
		out.Worktree = &events.DelegateWorktreeData{
			Path: info.Worktree.Path, Branch: info.Worktree.Branch, HeadSHA: info.Worktree.HeadSHA,
			Ahead: info.Worktree.Ahead, Dirty: info.Worktree.Dirty,
		}
	}
	return out
}

func detailedStatusJobRecords(records []*jobstore.JobRecord) []*jobstore.JobRecord {
	jobs := make([]*jobstore.JobRecord, 0, len(records))
	terminal := 0
	for _, rec := range records {
		if rec == nil || rec.Type != jobstore.JobShell {
			continue
		}
		if rec.Status.IsTerminal() {
			if terminal >= detailedStatusTerminalJobsLimit {
				continue
			}
			terminal++
		}
		jobs = append(jobs, rec)
	}
	return jobs
}

func projectJobStatusInfos(records []*jobstore.JobRecord) []JobStatusInfo {
	jobs := make([]JobStatusInfo, 0, len(records))
	for _, rec := range records {
		jobs = append(jobs, JobStatusInfo{
			JobID:            rec.JobID,
			JobType:          string(rec.Type),
			Status:           string(rec.Status),
			Reason:           rec.Reason,
			ExhaustionBudget: rec.ExhaustionBudget,
			ExhaustionLimit:  rec.ExhaustionLimit,
			TranscriptRef:    jobTranscriptRef(rec),
			OutputBytes:      rec.OutputBytes,
			ExitCode:         rec.ExitCode,
			Command:          rec.Command,
			Intent:           rec.Intent,
			Task:             rec.Task,
		})
	}
	return jobs
}
