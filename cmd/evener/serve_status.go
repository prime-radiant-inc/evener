package main

import (
	"slices"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

// evenerUsageFromLLM maps a session's cumulative llm.Usage to the wire
// appwire.EvenerUsage shown in thread/read. Returns nil when every
// total (including CacheReadTokens) is zero — a fresh session, an old daemon
// that never seeded usage, or a session without recorded usage — so the status row hides the
// usage cluster rather than rendering ↑0 ↓0 (WS2 A7).
func evenerUsageFromLLM(u llm.Usage) *appwire.EvenerUsage {
	return appwire.EvenerUsageFromLLM(u)
}

func agentToServerDetailedStatus(ds agent.DetailedStatus) server.DetailedStatus {
	var out server.DetailedStatus
	out.Commands = slices.Clone(ds.Commands)
	if ds.Plugins != nil {
		out.Plugins = make([]server.PluginStatusInfo, 0, len(ds.Plugins))
	}

	for _, t := range ds.Tools {
		out.Tools = append(out.Tools, server.ToolInfo{Name: t.Name, Source: t.Source})
	}
	for _, m := range ds.MCP {
		out.MCP = append(out.MCP, server.MCPServerInfo{Name: m.Name, Tools: m.Tools, Status: m.Status, Error: m.Error})
	}
	// Real invocation controls and availability come from the inspection
	// catalog; Skills is its user-advertisement view, so each listed skill is
	// matched back to its descriptor. Values are Stage 1 copies, never
	// re-derived here.
	descriptors := make(map[string]skill.Descriptor, len(ds.SkillCatalog))
	for _, descriptor := range ds.SkillCatalog {
		descriptors[descriptor.CatalogName] = descriptor
	}
	for _, s := range ds.Skills {
		info := server.SkillInfo{Name: s.Name, Description: s.Description}
		if descriptor, ok := descriptors[s.Name]; ok {
			info.DisableModelInvocation = descriptor.Controls.DisableModelInvocation
			info.UserInvocable = descriptor.Controls.UserInvocable
			info.Available = !descriptor.Unavailable
			info.AllowedTools = append([]string(nil), s.AllowedTools...)
		}
		out.Skills = append(out.Skills, info)
	}
	// Stage 1 discovery diagnostics copy verbatim alongside the catalog: the
	// path-free Skills view stays the completion source, SkillDiagnostics is
	// the explicit source-detail view.
	for _, d := range ds.SkillDiagnostics {
		out.SkillDiagnostics = append(out.SkillDiagnostics, server.SkillDiagnosticInfo{
			Category:    d.Category,
			Name:        d.Name,
			Source:      d.Source,
			OtherSource: d.OtherSource,
			Field:       d.Field,
			Message:     d.Message,
		})
	}
	for _, p := range ds.Plugins {
		out.Plugins = append(out.Plugins, server.PluginStatusInfo{
			Name:       p.Name,
			Version:    p.Version,
			SkillCount: p.SkillCount,
			AgentCount: p.AgentCount,
			HookCount:  p.HookCount,
			MCPCount:   p.MCPCount,
		})
	}
	for _, he := range ds.HookEvents {
		if out.HookEvents == nil {
			out.HookEvents = make([]server.HookEventStatus, 0, len(ds.HookEvents))
		}
		out.HookEvents = append(out.HookEvents, server.HookEventStatus{
			Event:     string(he.Event),
			Count:     he.Count,
			Tier:      he.Tier,
			Supported: he.Supported,
		})
	}
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, server.JobStatusInfo{
			JobID:            job.JobID,
			JobType:          job.JobType,
			Status:           job.Status,
			Reason:           job.Reason,
			ExhaustionBudget: job.ExhaustionBudget,
			ExhaustionLimit:  job.ExhaustionLimit,
			Resumable:        job.Resumable,
			ExitCode:         job.ExitCode,
			OutputBytes:      job.OutputBytes,
			TranscriptRef:    job.TranscriptRef,
			Command:          job.Command,
			Intent:           job.Intent,
			Task:             job.Task,
		})
	}
	out.Delegates = slices.Clone(ds.Delegates)
	out.Watches = cloneServeWatches(ds.Watches)
	if ds.TurnSlots != nil {
		out.TurnSlots = &server.TurnSlotStatus{
			InUse: ds.TurnSlots.InUse, Cap: ds.TurnSlots.Cap, Jobs: ds.TurnSlots.Jobs, Drives: ds.TurnSlots.Drives,
		}
	}
	out.Agents = ds.Agents

	return out
}

// cloneServeWatches returns a defensive copy of the watch diagnostics in a
// DetailedStatus. Cadence, event, and delivery-time slices are copied so a
// consumer mutating its copy cannot reach the shared status value, matching
// appwire.CloneEvenerWatches and appWatchFromDetailedStatus.
func cloneServeWatches(watches []agent.WatchStatusInfo) []agent.WatchStatusInfo {
	if watches == nil {
		return nil
	}
	out := make([]agent.WatchStatusInfo, len(watches))
	for i := range watches {
		out[i] = watches[i]
		out[i].Cadence = append([]agent.WatchCadenceInfo(nil), watches[i].Cadence...)
		out[i].Events = append([]string(nil), watches[i].Events...)
		out[i].DeliveryTimes = append([]string(nil), watches[i].DeliveryTimes...)
	}
	return out
}

// liveThreadEnvelopeSource is the daemon's one sampling window onto live session
// state for the materialized root envelope. The server uses it for the
// pre-bridge seed and retained checkpoint/legacy facets, never on a read. Typed
// task and goal carriers bypass this adapter and patch cached root/descendant
// state inside CommitProjection. Every method here can take the session's mutex,
// the task store's, or read jobs.jsonl, and a read path that could reach them
// would hold the projection gate across that work.
//
// It resolves the session per call rather than capturing one, so it follows
// thread/clear onto the replacement session exactly as the sixteen closures it
// replaced did.
//
// The session is reached as agent.EnvelopeSampling rather than as *agent.Session
// ON PURPOSE, and the narrowing is a deadlock guard rather than tidiness. These
// methods run on the bridge's drain goroutine, which is the session's
// AUTHORITATIVE event consumer: a sample that blocks on a lock an emitter holds
// stops the drain, fills the event buffer, blocks the emitter inside that same
// critical section, and leaves the session unclosable. Which locks those are is a
// fact about package agent that is invisible from here, so the interface is where
// a new facet has to go to ask — read its doc comment before adding a method to
// it, and note that agent/session_envelope_sampling_test.go proves the answer for
// every method it declares.
type liveThreadEnvelopeSource struct {
	session func() agent.EnvelopeSampling
}

func (l liveThreadEnvelopeSource) ContextStatus() (server.ContextMetrics, float64) {
	metrics, pressure := l.session().ContextStatus()
	return server.ContextMetrics{Used: metrics.Used, Window: metrics.Window, Remaining: metrics.Remaining, Assumed: metrics.Assumed}, pressure
}

func (l liveThreadEnvelopeSource) DetailedStatus() server.DetailedStatus {
	return agentToServerDetailedStatus(l.session().DetailedStatus())
}

func (l liveThreadEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return l.session().ClientMutationProjection()
}

// TaskAggregate reports nil on a persisted-store failure. That is unavailable
// task state, not an authoritative empty list, and the wire distinguishes them.
func (l liveThreadEnvelopeSource) TaskAggregate() *appwire.TaskAggregate {
	tasks, err := l.session().TasksWithError()
	if err != nil {
		return nil
	}
	summary := taskpkg.Summarize(tasks)
	aggregate := &appwire.TaskAggregate{
		Total:     summary.Total,
		Done:      summary.Done,
		Cancelled: summary.Cancelled,
		Remaining: summary.Remaining,
	}
	if summary.Current != nil {
		aggregate.Current = &appwire.TaskSummary{ID: summary.Current.ID, Description: summary.Current.Description}
	}
	return aggregate
}

func (l liveThreadEnvelopeSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
	sess := l.session()
	return sess.WorkMillisSnapshot(), evenerUsageFromLLM(sess.CumulativeUsageSnapshot()), sess.ActiveTurnStartedAtMillis()
}

func (l liveThreadEnvelopeSource) FailedToolCalls() (int, bool) {
	return l.session().FailedToolCallsSnapshot()
}

func (l liveThreadEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	return l.session().PendingQuestion()
}

func (l liveThreadEnvelopeSource) RestingFailure() *appwire.ThreadFailure {
	return l.session().RestingFailure()
}

func (l liveThreadEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return mapServePendingEscalations(l.session().PendingEscalations())
}

func (l liveThreadEnvelopeSource) ReasoningInfo() (string, []string, bool) {
	sess := l.session()
	p := sess.Profile()
	return sess.ReasoningEffort(), p.ReasoningEffortLevels(), p.SupportsReasoning()
}

func (l liveThreadEnvelopeSource) VisionModel() string {
	return l.session().VisionModel()
}

func (l liveThreadEnvelopeSource) SessionMeta() schema.SessionMeta {
	return l.session().Meta()
}
