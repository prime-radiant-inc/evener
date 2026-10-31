package server

import (
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
)

// appThread assembles the thread snapshot. It is a struct copy plus identity:
// every live-session value comes from the materialized envelope, which the
// bridge maintains at the moments those values change.
//
// It must stay that way. This runs under the response cut, which holds
// projectionMu AND deliveryMu, so anything reached from here blocks every other
// connection's capture, every projection commit, and the whole event bridge for
// as long as it takes. It used to pull sixteen live session callbacks from
// here; four of them could block behind a transcript fsync, a synchronous
// jobs.jsonl read, or the session's own mutex.
func (s *Server) appThread() appwire.Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appThreadLocked()
}

func (s *Server) appThreadLocked() appwire.Thread {
	return s.appThreadWithDiagnosticsLocked(appDiagnosticsFromDetailedStatus)
}

// appThreadWithDiagnosticsLocked assembles the thread snapshot with its
// diagnostics block projected from the envelope's detailed status by
// diagnostics, so a caller that needs only part of the block never builds the
// rest.
func (s *Server) appThreadWithDiagnosticsLocked(diagnostics func(DetailedStatus) *appwire.EvenerDiagnostics) appwire.Thread {
	status := s.status
	sourceID := s.appSourceID
	threadID := s.appThreadID
	ref := s.appRef
	processing := s.processing
	envelope := s.appEnvelope
	activeTurnID := s.appActiveTurnID

	if sourceID == "" {
		sourceID = "local"
	}
	if threadID == "" {
		threadID = status.SessionID
	}
	if ref == "" {
		ref = appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	}
	pressure := envelope.ContextPressure
	metrics := envelope.ContextMetrics
	var threadDiagnostics *appwire.EvenerDiagnostics
	if envelope.Detailed != nil {
		threadDiagnostics = diagnostics(*envelope.Detailed)
	}
	queue := envelope.Queue
	pendingMutations := envelope.PendingMutations
	goalState := envelope.Goal
	humanNote := envelope.HumanNote
	agentNote := envelope.AgentNote
	sessionURLs := envelope.SessionURLs
	taskAggregate := envelope.Tasks
	workMillis := envelope.WorkMillis
	usage := envelope.Usage
	activeTurnStartedAt := envelope.ActiveTurnStartedAt
	failedToolCalls := envelope.FailedToolCalls
	askPending := envelope.PendingQuestion != nil
	pendingQuestion := envelope.PendingQuestion
	pendingEscalations := envelope.PendingEscalations
	statusType := appStatus(status.State, processing)
	// A failure summary describes the failed turn the session rests on, so it
	// shows only while the status says so: the next turn moves the status
	// before any facet re-samples the summary (S1c).
	var failure *appwire.ThreadFailure
	if statusType == appwire.ThreadStatusSystemError {
		failure = envelope.Failure
	}
	reasoningEffort := envelope.ReasoningEffort
	reasoningEffortLevels := envelope.ReasoningEffortLevels
	supportsReasoning := envelope.SupportsReasoning
	visionModel := envelope.VisionModel
	manager := envelope.Manager
	lastTurnEndedAt := envelope.LastTurnEndedAt
	lastMessage := envelope.LastMessage
	access := cloneThreadAccess(envelope.Access)
	threadName := envelope.Name
	threadPreview := envelope.Preview
	workingDir := envelope.WorkingDir
	if workingDir == "" && envelope.cwdCarrierGeneration == 0 {
		workingDir = status.WorkingDir
	}
	if threadPreview == "" {
		threadPreview = status.SessionID
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     status.SessionID,
		Name:          threadName,
		Preview:       threadPreview,
		ModelProvider: status.Model,
		Status:        appwire.ThreadStatus{Type: statusType},
		CWD:           workingDir,
		Path:          filepath.Base(workingDir),
		Source:        sourceID,
		Evener: appwire.EvenerThread{
			Ref:                   ref,
			InstanceID:            threadID,
			Profile:               status.Profile,
			TurnCount:             status.Turns,
			ActiveTurnID:          activeTurnID,
			ContextPressure:       pressure,
			ContextUsed:           metrics.Used,
			ContextWindow:         metrics.Window,
			ContextRemaining:      metrics.Remaining,
			ContextAssumed:        metrics.Assumed,
			Capabilities:          s.appCapabilitiesLocked(status.State, processing),
			Diagnostics:           threadDiagnostics,
			Queue:                 queue,
			PendingMutations:      pendingMutations,
			Tasks:                 taskAggregate,
			Goal:                  goalState,
			HumanNote:             humanNote,
			AgentNote:             agentNote,
			SessionURLs:           sessionURLs,
			Usage:                 usage,
			Cost:                  appwire.EstimateCost(s.costFor(status.Profile+"/"+status.Model), usage),
			WorkMillis:            workMillis,
			ActiveTurnStartedAt:   activeTurnStartedAt,
			FailedToolCalls:       failedToolCalls,
			AskPending:            askPending,
			PendingQuestion:       pendingQuestion,
			Failure:               failure,
			PendingEscalations:    pendingEscalations,
			ReasoningEffort:       reasoningEffort,
			ReasoningEffortLevels: reasoningEffortLevels,
			SupportsReasoning:     supportsReasoning,
			VisionModel:           visionModel,
			Manager:               manager,
			LastTurnEndedAt:       lastTurnEndedAt,
			LastMessage:           lastMessage,
			Access:                access,
		},
	}
}

func appDiagnosticsFromDetailedStatus(ds DetailedStatus) *appwire.EvenerDiagnostics {
	out := &appwire.EvenerDiagnostics{}
	out.Commands = slices.Clone(ds.Commands)
	if ds.Plugins != nil {
		out.Plugins = make([]appwire.EvenerPluginInfo, 0, len(ds.Plugins))
	}
	for _, tool := range ds.Tools {
		out.Tools = append(out.Tools, appwire.EvenerToolInfo{Name: tool.Name, Source: tool.Source})
	}
	for _, srv := range ds.MCP {
		out.MCP = append(out.MCP, appwire.EvenerMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...), Status: srv.Status, Error: srv.Error})
	}
	for _, skill := range ds.Skills {
		out.Skills = append(out.Skills, appwire.EvenerSkillInfo{
			Name:                   skill.Name,
			Description:            skill.Description,
			DisableModelInvocation: skill.DisableModelInvocation,
			UserInvocable:          skill.UserInvocable,
			Available:              skill.Available,
			AllowedTools:           append([]string(nil), skill.AllowedTools...),
		})
	}
	for _, d := range ds.SkillDiagnostics {
		out.SkillDiagnostics = append(out.SkillDiagnostics, appwire.EvenerSkillDiagnostic{
			Category:    d.Category,
			Name:        d.Name,
			Source:      d.Source,
			OtherSource: d.OtherSource,
			Field:       d.Field,
			Message:     d.Message,
		})
	}
	for _, plugin := range ds.Plugins {
		out.Plugins = append(out.Plugins, appwire.EvenerPluginInfo{
			Name:       plugin.Name,
			Version:    plugin.Version,
			SkillCount: plugin.SkillCount,
			AgentCount: plugin.AgentCount,
			HookCount:  plugin.HookCount,
			MCPCount:   plugin.MCPCount,
		})
	}
	for _, he := range ds.HookEvents {
		if out.HookEvents == nil {
			out.HookEvents = make([]appwire.EvenerHookEventStatus, 0, len(ds.HookEvents))
		}
		out.HookEvents = append(out.HookEvents, appwire.EvenerHookEventStatus{
			Event:     he.Event,
			Count:     he.Count,
			Tier:      he.Tier,
			Supported: he.Supported,
		})
	}
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, appJobFromDetailedStatus(job))
	}
	for _, delegate := range ds.Delegates {
		out.Delegates = append(out.Delegates, appwire.SlimDelegateForRoster(delegate.AppwireInfo()))
	}
	for _, watch := range ds.Watches {
		out.Watches = append(out.Watches, appWatchFromDetailedStatus(watch))
	}
	if ds.TurnSlots != nil {
		out.TurnSlots = &appwire.EvenerTurnSlots{
			InUse: ds.TurnSlots.InUse, Cap: ds.TurnSlots.Cap, Jobs: ds.TurnSlots.Jobs, Drives: ds.TurnSlots.Drives,
		}
	}
	out.Agents = append(out.Agents, ds.Agents...)
	return out
}

// appStatusDiagnosticsFromDetailedStatus projects only what a liveness probe
// reads (a statusOnly thread/list): jobs, watches, and each delegate's identity
// and lifecycle. It skips the tool, skill and plugin catalogs and every
// delegate's task, final message and structured result, which make the full
// block megabytes on a session with many delegates.
func appStatusDiagnosticsFromDetailedStatus(ds DetailedStatus) *appwire.EvenerDiagnostics {
	out := &appwire.EvenerDiagnostics{}
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, appJobFromDetailedStatus(job))
	}
	for _, delegate := range ds.Delegates {
		out.Delegates = append(out.Delegates, appwire.EvenerDelegateInfo{
			RunGeneration: delegate.RunGeneration, DelegateID: delegate.DelegateID, ChildSessionID: delegate.ChildSessionID, Lifecycle: delegate.Lifecycle,
		})
	}
	for _, watch := range ds.Watches {
		out.Watches = append(out.Watches, appWatchFromDetailedStatus(watch))
	}
	return out
}

func appJobFromDetailedStatus(job JobStatusInfo) appwire.EvenerJobInfo {
	return appwire.EvenerJobInfo{
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
	}
}

func appWatchFromDetailedStatus(watch agent.WatchStatusInfo) appwire.EvenerWatchInfo {
	out := appwire.EvenerWatchInfo{
		ID:             watch.ID,
		Source:         watch.Source,
		Target:         watch.Target,
		SendTo:         watch.SendTo,
		Note:           watch.Note,
		OutputMatch:    watch.OutputMatch,
		Events:         append([]string(nil), watch.Events...),
		WildcardEvents: watch.WildcardEvents,
		Deliveries:     watch.Deliveries,
		DeliveryTimes:  append([]string(nil), watch.DeliveryTimes...),
		CreatedAt:      watch.CreatedAt,
		Active:         watch.Active,
		EndReason:      watch.EndReason,
	}
	if watch.Cadence != nil {
		out.Cadence = make([]appwire.EvenerWatchCadence, 0, len(watch.Cadence))
		for _, cadence := range watch.Cadence {
			out.Cadence = append(out.Cadence, appwire.EvenerWatchCadence{
				Kind:              cadence.Kind,
				Seconds:           cadence.Seconds,
				DerivedNextFireAt: cadence.DerivedNextFireAt,
				Every:             cadence.Every,
				Filter:            cadence.Filter,
			})
		}
	}
	return out
}

// attachLiveWatches merges the live watch rows that belong on each returned list
// row into it, for the root and for a descendant alike, in one resolver call for
// the whole page.
//
// A descendant thread begins carrying a diagnostics block it never carried
// before: the event-shaped projection writes no Evener.Diagnostics, so a child
// row had no watches even when the child session held some. The root's row does
// carry one, but the diagnostics facet is only re-sampled on a few events and a
// turn boundary, so it can lag a watch armed or cleared in between. The live
// sample supersedes both. The block is attached to the returned copy only; the
// cached projection is never mutated.
//
// The sample happens on a LIST read, not from a propagation event and not on the
// single-thread read path (which runs under the subscription cut and must not
// touch session state; see appThreadForID). Watch state changes without a
// per-thread app event on this daemon, so the hub picks a new watch up on its next
// roster probe rather than instantly: a row can lag a new watch by one probe
// interval.
//
// An ID the resolver does not answer for keeps its cached projection. A present
// entry with an empty non-nil slice is a real answer -- the row has no watches --
// and replaces the cached watches, which is how a cleared root watch leaves the
// row.
func (s *Server) attachLiveWatches(data []appwire.Thread) {
	s.mu.RLock()
	fn := s.appDescendantLiveWatchesFunc
	s.mu.RUnlock()
	if fn == nil || len(data) == 0 {
		return
	}
	ids := make([]string, 0, len(data))
	for i := range data {
		ids = append(ids, data[i].ID)
	}
	rows := fn(ids)
	for i := range data {
		statuses, ok := rows[data[i].ID]
		if !ok {
			continue
		}
		data[i] = appThreadWithWatches(data[i], statuses)
	}
}

// attachSubagentTally stamps the root row with its tree's subagent
// tally. A nested delegate's lifecycle change is emitted on its owner's stream
// and never samples the root's envelope, so the tally is read when the row is
// listed rather than cached. A tree with no subagent carries none.
func (s *Server) attachSubagentTally(root *appwire.Thread) {
	s.mu.RLock()
	fn := s.appSubagentTallyFunc
	s.mu.RUnlock()
	if fn == nil {
		return
	}
	tally, ok := fn()
	if !ok || tally == (appwire.SubagentTally{}) {
		return
	}
	root.Evener.Subagents = &tally
}

// appThreadWithWatches returns thread with statuses as its diagnostics watch
// rows.
//
// The returned rows are rebuilt from the agent rows and never alias them: each
// EvenerWatchInfo copies its event, cadence, and delivery-time slices, so a
// caller mutating the response cannot reach the agent state. The whole
// diagnostics block is deep-copied through appwire.CloneEvenerDiagnostics, so
// every other slice of the cached projection is out of reach too, not just
// Watches.
//
// A row with no diagnostics block and no watches to attach keeps its nil block.
// The live resolver answers every known session, so a watchless child reaches
// this function with an empty sample; materializing a block for it would change
// the wire shape of every watchless row and read as "has diagnostics" to a
// consumer of the field. The block is materialized only when there is a watch to
// carry, and it is kept when the row already had one, which is what lets a
// cleared watch leave the row.
func appThreadWithWatches(thread appwire.Thread, statuses []agent.WatchStatusInfo) appwire.Thread {
	if thread.Evener.Diagnostics == nil && len(statuses) == 0 {
		return thread
	}
	watches := make([]appwire.EvenerWatchInfo, 0, len(statuses))
	for _, status := range statuses {
		watches = append(watches, appWatchFromDetailedStatus(status))
	}
	diagnostics := appwire.CloneEvenerDiagnostics(thread.Evener.Diagnostics)
	if diagnostics == nil {
		diagnostics = &appwire.EvenerDiagnostics{}
	}
	diagnostics.Watches = watches
	thread.Evener.Diagnostics = diagnostics
	return thread
}

func (s *Server) appCapabilities(state string, processing bool) appwire.ThreadCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appCapabilitiesLocked(state, processing)
}

// skillInputSupportedLocked computes ThreadCapabilities.SkillInput's
// advertisement: true exactly when all four input-bearing retry-safe seams are
// wired. Each handler's own ValidateSkillInputSupport gate enforces its own
// seam's wiring, so no endpoint ever accepts a selection it cannot consume;
// the advertisement stays the thread-level conjunction because a selection
// can enter through any of the four mutations. Caller holds s.mu.
func (s *Server) skillInputSupportedLocked() bool {
	return s.retrySafeTurns.Start != nil && s.retrySafeTurns.Steer != nil &&
		s.retrySafeTurns.Queue != nil && s.retrySafeTurns.Drain != nil
}

func (s *Server) appCapabilitiesLocked(state string, processing bool) appwire.ThreadCapabilities {
	// Status-dependent capabilities derive from the status this thread PUBLISHES,
	// not assembled again from the fields behind it. Clear additionally requires
	// its handler and state gate to report that the action is currently safe.
	// A client reading a status and the capabilities beside it is reading one
	// decision, so no window can open where the two describe different threads.
	status := appStatus(state, processing)
	active := status == appwire.ThreadStatusActive
	closed := status == appwire.ThreadStatusClosed
	// steerAvailable reads the retry-safe callback because that is the seam
	// handleAppTurnSteer dispatches through.
	steerAvailable := s.retrySafeTurns.Steer != nil
	clearAvailable := s.clearFunc != nil && !active && !closed && s.clearBlockedReasonLocked() == ""
	return appwire.ThreadCapabilities{
		Send: !active && !closed,
		// Steer answers "can this harness steer", not "is there a turn to steer
		// right now": clients apply the status themselves (the web's canSteer
		// requires active for turn/steer), and the daemon accepts
		// turn/drainAsSteer and turn/promoteQueuedAsSteer with no turn in
		// flight -- they are two of the runs that release a queue a Stop parked
		// (agent/session_client_mutation_queue.go). Folding `active` in here
		// left an idle client unable to tell a harness that cannot steer from
		// one that can, so a parked queue had no affordance to run it (#1363).
		Steer: steerAvailable && !closed,
		// Interrupt answers "can this harness stop a turn", not "is a turn
		// running to stop right now": clients apply the status themselves (the
		// web's showStop is busy && interrupt, the SDK's `stop` requires an
		// active status), the same uniform rule Steer and Queue follow (#1375).
		// It reads the retry-safe handler because that is the seam
		// handleAppTurnInterrupt dispatches through: a server whose only
		// interrupt wiring is the ambient cancel answers the RPC with
		// Unavailable, so advertising Stop would offer a control that can only
		// fail. It is deliberately NOT the ambient cancelFunc.
		//
		// That field is also per-turn -- armed and cleared once per turn by the
		// session loop, on a different goroutine and a different clock from the
		// processing flag the status reads, and cmd/evener/serve.go's drain path
		// published processing before arming it. Reading it here let this set
		// say steer=true interrupt=false: a turn is running and cannot be
		// stopped. The set is PUSHED on thread/status/changed and a client keeps
		// it until the status changes again, so a frame stamped inside that
		// window takes Stop away for the whole turn that follows (kata 5gdv).
		// The startup-installed handler has no such window: it is durable from
		// before the first turn, which is also why a fresh idle daemon
		// advertises Stop instead of understating itself until its first turn.
		// Whether a cancel is armed at the instant the request arrives stays the
		// business of the typed interrupt handler; InterruptClientMutation has
		// its own quiescence precondition (kata vewa).
		Interrupt:         s.retrySafeTurns.Interrupt != nil && !closed,
		Compact:           s.compactFunc != nil && !closed,
		Clear:             clearAvailable,
		ForkFromTurn:      false,
		Shutdown:          s.shutdownFunc != nil,
		ChangeModel:       s.modelFunc != nil && !closed,
		ChangeVisionModel: s.visionModelFunc != nil && !closed,
		Rename:            s.nameFunc != nil && !closed,
		// Queue answers "can this harness queue work", not "is a turn running
		// to queue behind": the client applies the status (turn/queue is
		// meaningful only mid-turn), the uniform rule Steer and Interrupt
		// follow (#1375). It was the "active turn" gate of kata 111a.
		//
		// Queue reads the retry-safe callback for the same reason steerAvailable
		// does: handleAppTurnQueue dispatches through retrySafeTurns.Queue.
		Queue: s.retrySafeTurns.Queue != nil && !closed,
		// Goal is available whenever the engine is wired and the session is
		// open. It is intentionally NOT gated on !active: a goal may be set
		// mid-turn (it arms for the next continuation), unlike Send.
		Goal: s.goalFunc != nil && !closed,
		// SharedNotes is available whenever both notes verbs are wired and
		// the session is open. Like Goal it is NOT gated on !active: a human
		// save may land mid-turn (it steers the running turn), unlike Send.
		SharedNotes: s.notesHumanSetFunc != nil && s.urlsRemoveFunc != nil && !closed,
		// StopSubagent is available whenever the stop is wired and the
		// session is open. Like Interrupt, whether a run is there to stop is
		// the handler's answer (S6).
		StopSubagent: s.delegateStopFunc != nil && !closed,
		// SkillInput advertises that this thread's input-bearing turn mutations
		// (turn/start, turn/steer, turn/queue, turn/drainAsSteer) consume skill
		// selections at the input's actual claim. It is true exactly when every
		// one of those retry-safe seams is wired: a server that wired none or
		// only part of them has no consumable selection surface and must not
		// advertise one. Cold/discovered threads and older daemons keep their
		// own answers; this is the live daemon-sourced projection only. Every
		// handler's skill-input gate enforces its own seam's wiring, so an
		// advertised true means every endpoint really consumes selections.
		SkillInput:   s.skillInputSupportedLocked(),
		CommandInput: s.skillInputSupportedLocked(),
	}
}

func (s *Server) ensureAppProjectorLocked(threadID string) {
	if s.appProjector != nil {
		return
	}
	if threadID == "" {
		threadID = s.appThreadID
	}
	ref := s.appRef
	if ref == "" {
		ref = appwire.Ref{SourceID: s.appSourceID, ThreadID: threadID}.String()
	}
	s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
}

// appStatus is the daemon's ONE answer to "what is this thread doing". The
// status the wire publishes and the capability set published beside it are both
// derived from it, so the two cannot disagree about whether a turn is running.
//
// The daemon holds `processing` across the whole of an input, pre-turn work
// included (a slash command's inline shell span can run for seconds), while the
// SESSION has not entered SessionProcessing yet and reports itself settled;
// processing therefore wins over a resting state.
func appStatus(state string, processing bool) string {
	// Closed wins over everything. A thread whose session has gone is not
	// working, whatever flag was left set behind it.
	state = strings.TrimSpace(state)
	if state == appwire.ThreadStatusClosed {
		return appwire.ThreadStatusClosed
	}
	if processing {
		return appwire.ThreadStatusActive
	}
	switch state {
	case appwire.ThreadStatusIdle, appwire.ThreadStatusActive, appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusWarning, appwire.ThreadStatusSystemError, appwire.ThreadStatusNotLoaded:
		return state
	default:
		return appwire.ThreadStatusIdle
	}
}
