package appprojector

import (
	"encoding/json"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/invariant"
)

type AppNotification struct {
	ThreadID                string
	Method                  string
	Params                  any
	TaskStoreOwnerSessionID string
	TaskPublicationEpoch    uint64
	TaskPublicationRevision uint64
}

// AppEventProjector maps one thread's session events to its thread-level
// AppWire notifications: thread/started, thread/status/changed, the queue,
// goal, notes, URLs, tasks, delegates, model, effort and vision changes, jobs,
// escalations, the name, model retries and thread/closed. History reaches
// clients from recorded entries (history/updated) and live unrecorded state
// from the overlay, so the projector holds no turn or item state and mints no
// ids.
type AppEventProjector struct {
	threadID string
	ref      string
	// taskStoreOwnerSessionID is routing metadata for the server's cached
	// descendant projection. It never enters an AppWire params shape.
	taskStoreOwnerSessionID string
	// taskPublicationRevision is the newest internal task publication observed
	// by this source projector. It never enters an AppWire params shape.
	taskPublicationEpoch    uint64
	taskPublicationRevision uint64

	// runningTurnID is the running execution's TurnID, from its
	// EXECUTION_STARTED to its EXECUTION_ENDED (or the session's end).
	runningTurnID string
	delegates     map[string]appwire.EvenerDelegateInfo
}

func NewAppEventProjector(threadID, ref string) *AppEventProjector {
	return &AppEventProjector{
		threadID:  threadID,
		ref:       ref,
		delegates: map[string]appwire.EvenerDelegateInfo{},
	}
}

// RunningTurnID is the running execution's TurnID, empty between executions.
func (p *AppEventProjector) RunningTurnID() string {
	return p.runningTurnID
}

// TaskStoreOwnerSessionID returns internal routing metadata learned from typed
// task carriers. It is not part of any public AppWire params shape.
func (p *AppEventProjector) TaskStoreOwnerSessionID() string {
	return p.taskStoreOwnerSessionID
}

// TaskPublicationEpoch returns the newest process-local TaskStore incarnation
// observed by this projector. It is not part of any public AppWire params shape.
func (p *AppEventProjector) TaskPublicationEpoch() uint64 {
	return p.taskPublicationEpoch
}

// TaskPublicationRevision returns the newest internal task publication learned
// from typed task carriers. It is not part of any public AppWire params shape.
func (p *AppEventProjector) TaskPublicationRevision() uint64 {
	return p.taskPublicationRevision
}

func (p *AppEventProjector) observeTaskPublication(epoch, revision uint64) {
	if epoch > p.taskPublicationEpoch || (epoch == p.taskPublicationEpoch && revision > p.taskPublicationRevision) {
		p.taskPublicationEpoch = epoch
		p.taskPublicationRevision = revision
	}
}

func (p *AppEventProjector) Project(event events.SessionEvent) []AppNotification {
	if p.threadID == "" {
		p.threadID = event.SessionID
	}

	switch event.Kind {
	case events.EventSessionStart:
		data := eventData[events.SessionStartData](event.Data)
		if data.TaskStoreOwnerSessionID != "" {
			p.taskStoreOwnerSessionID = data.TaskStoreOwnerSessionID
		}
		p.observeTaskPublication(data.TaskPublicationEpoch, data.TaskPublicationRevision)
		// A restored session carries its re-derived state on the event (spec
		// §5.4's "two touchpoints"); a fresh session's State is empty and
		// defaults to idle, same as an unrecognized value.
		status := appwire.ThreadStatusIdle
		switch data.State {
		case appwire.ThreadStatusAwaiting:
			status = appwire.ThreadStatusAwaiting
		case appwire.ThreadStatusIdle:
			status = appwire.ThreadStatusIdle
		}
		var tasks *appwire.TaskAggregate
		var goal *appwire.GoalState
		if data.CurrentWork != nil {
			tasks = taskAggregate(data.CurrentWork.Tasks)
			goal = goalState(data.CurrentWork.Goal)
		}
		out := []AppNotification{
			p.notification(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
				ThreadID: p.threadID,
				Ref:      p.ref,
				Thread: appwire.Thread{
					ID:            p.threadID,
					SessionID:     p.threadID,
					Source:        "local",
					ModelProvider: data.Model,
					Status:        appwire.ThreadStatus{Type: status},
					Evener: appwire.EvenerThread{
						Ref:     p.ref,
						Profile: data.Profile,
						Tasks:   tasks,
						Goal:    goal,
					},
				},
			}),
			p.threadStatus(status),
		}
		for i := range out {
			out[i].TaskStoreOwnerSessionID = data.TaskStoreOwnerSessionID
			out[i].TaskPublicationEpoch = data.TaskPublicationEpoch
			out[i].TaskPublicationRevision = data.TaskPublicationRevision
		}
		return out
	case events.EventExecutionStarted:
		p.runningTurnID = eventData[events.ExecutionStartedData](event.Data).TurnID
		return []AppNotification{p.threadStatus(appwire.ThreadStatusActive)}
	case events.EventExecutionEnded:
		if data := eventData[events.ExecutionEndedData](event.Data); data.TurnID != p.runningTurnID {
			// An end for an execution another one already replaced.
			return nil
		}
		p.runningTurnID = ""
		return []AppNotification{p.threadStatus(appwire.ThreadStatusIdle)}
	case events.EventGoalUpdated:
		data := eventData[events.GoalUpdatedData](event.Data)
		var state *appwire.GoalState
		if data.Goal != nil {
			state = &appwire.GoalState{
				Objective:  data.Goal.Objective,
				Status:     data.Goal.Status,
				Iterations: data.Goal.Iterations,
			}
		}
		return []AppNotification{p.notification(appwire.NotifyEvenerGoalUpdated, appwire.GoalUpdatedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Goal:     state,
		})}
	case events.EventNotesUpdated:
		data := eventData[events.NotesUpdatedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyEvenerNotesUpdated, appwire.NotesUpdatedParams{
			ThreadID:  p.threadID,
			Ref:       p.ref,
			HumanNote: data.HumanNote,
			AgentNote: data.AgentNote,
		})}
	case events.EventUrlsUpdated:
		data := eventData[events.UrlsUpdatedData](event.Data)
		urls := make([]appwire.SessionURL, 0, len(data.URLs))
		for _, u := range data.URLs {
			urls = append(urls, appwire.SessionURL{ID: u.ID, URL: u.URL, Label: u.Label, AddedBy: u.AddedBy, AddedAt: u.AddedAt})
		}
		return []AppNotification{p.notification(appwire.NotifyEvenerUrlsUpdated, appwire.UrlsUpdatedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			URLs:     urls,
		})}
	case events.EventModelRetry:
		// Thread-scoped, item-less: the retry is state about the wait in
		// progress, not a fact worth a transcript row (see
		// appwire.ThreadModelRetryParams on why 91 rows is the wrong answer).
		data := eventData[events.ModelRetryData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyEvenerThreadModelRetry, appwire.ThreadModelRetryParams{
			ThreadID:       p.threadID,
			Ref:            p.ref,
			TurnID:         p.runningTurnID,
			Attempt:        data.Attempt,
			MaxAttempts:    data.MaxAttempts,
			DelayMS:        data.DelayMS,
			ErrorClass:     data.ErrorClass,
			StatusCode:     data.StatusCode,
			Message:        data.Message,
			Model:          data.Model,
			GroupElapsedMS: data.GroupElapsedMS,
			AttemptCap:     data.AttemptCap,
		})}
	case events.EventQueueChanged:
		data := eventData[events.QueueChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Queue: appwire.QueueState{
				Depth:             data.Depth,
				Revision:          data.Revision,
				Preview:           append([]string(nil), data.Preview...),
				IDs:               append([]string(nil), data.IDs...),
				ClientMutationIDs: append([]string(nil), data.ClientMutationIDs...),
				Texts:             append([]string(nil), data.Texts...),
				SkillNames:        cloneSkillNames(data.SkillNames),
			},
			ConsumedClientMutationIDs: append([]string(nil), data.ConsumedClientMutationIDs...),
		})}
	case events.EventTaskUpdated:
		data := eventData[events.TaskUpdatedData](event.Data)
		if data.TaskStoreOwnerSessionID != "" {
			p.taskStoreOwnerSessionID = data.TaskStoreOwnerSessionID
		}
		p.observeTaskPublication(data.TaskPublicationEpoch, data.TaskPublicationRevision)
		notification := p.notification(appwire.NotifyEvenerTaskUpdated, appwire.TaskUpdatedParams{
			ThreadID:  p.threadID,
			Ref:       p.ref,
			Total:     data.Total,
			Done:      data.Done,
			Cancelled: data.Cancelled,
			Remaining: data.Remaining,
			Current:   taskSummary(data.Current),
		})
		notification.TaskStoreOwnerSessionID = data.TaskStoreOwnerSessionID
		notification.TaskPublicationEpoch = data.TaskPublicationEpoch
		notification.TaskPublicationRevision = data.TaskPublicationRevision
		return []AppNotification{notification}
	case events.EventSandboxEscalationRequested:
		// A harness-raised sandbox-exemption approval card (M7). It rides the event
		// stream ONLY — it is never appended to the transcript, so the model can
		// neither observe nor replay it. DeniedPath is the FULL path for informed
		// consent (only non-sensitive containment denials escalate; a sensitive path,
		// which never escalates, would degrade to "<denied>"); file contents never
		// appear. The shell fields are reserved and empty in v1.
		data := eventData[events.SandboxEscalationRequestedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyEvenerSandboxEscalationRequested, appwire.SandboxEscalationRequested{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			EscalationID: data.EscalationID,
			Mode:         data.Mode,
			Tool:         data.Tool,
			Kind:         data.Kind,
			DeniedPath:   data.DeniedPath,
			Command:      data.Command,
			OutputSoFar:  data.OutputSoFar,
			PartiallyRan: data.PartiallyRan,
		})}
	case events.EventSandboxEscalationResolved:
		// The pair to EventSandboxEscalationRequested above: a previously-raised
		// escalation left the pending set (resolved, turn-interrupted, or cleared by
		// session close — agent/session_escalation.go's escalateOnSandboxDenial emits
		// this exactly once per escalation from its convergence-point exit). Every
		// OTHER subscribed client uses it to clear its own stale copy of the card. It
		// carries no reason/approved (a review decision, additive later): the sole
		// consumer clears by id identically regardless of outcome, and the producer
		// cannot reliably distinguish close-cancel from interrupt anyway. Like
		// requested, it rides the event stream only and touches no turn/item state.
		data := eventData[events.SandboxEscalationResolvedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyEvenerSandboxEscalationResolved, appwire.SandboxEscalationResolved{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			EscalationID: data.EscalationID,
		})}
	case events.EventSessionNameChanged:
		data := eventData[events.SessionNameChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Name:     data.Name,
			Source:   data.Source,
		})}
	case events.EventModelChanged:
		data := eventData[events.ModelChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadModelChanged, appwire.ThreadModelChangedParams{
			ThreadID:              p.threadID,
			Ref:                   p.ref,
			ModelProvider:         data.NewProvider,
			Model:                 data.NewModel,
			ReasoningEffortLevels: data.ReasoningEffortLevels,
			SupportsReasoning:     data.SupportsReasoning,
		})}
	case events.EventReasoningEffortChanged:
		data := eventData[events.ReasoningEffortChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadReasoningEffortChanged, appwire.ThreadReasoningEffortChangedParams{
			ThreadID:        p.threadID,
			Ref:             p.ref,
			ReasoningEffort: data.ReasoningEffort,
		})}
	case events.EventVisionModelChanged:
		data := eventData[events.VisionModelChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadVisionModelChanged, appwire.ThreadVisionModelChangedParams{
			ThreadID:    p.threadID,
			Ref:         p.ref,
			VisionModel: data.NewVisionModel,
		})}
	// The job lifecycle pair below is the ONLY job push on the wire, and it
	// serves every consumer: the webui folds it into subagent rows and uses it
	// as the jobs panel's refetch trigger, and the TUI applies it to its
	// transcript reducer. Job.JobID/Job.Status carry what a refetch trigger
	// needs, so no lighter-weight second notification exists for the same
	// instants (kata j7y6).
	case events.EventJobStarted:
		data := eventData[events.JobStartedData](event.Data)
		if data.JobType != "shell" {
			return nil
		}
		out := []AppNotification{p.notification(appwire.NotifyEvenerJobStarted, appwire.EvenerJobParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Job: appwire.EvenerJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				Intent:           data.Intent,
				ParentDelegateID: data.ParentDelegateID,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				TranscriptRef:    data.TranscriptRef,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
		if data.RootSessionID != "" && data.TreeRevision > 0 {
			out = append(out, p.notification(appwire.NotifyEvenerJobsTreeUpdated, appwire.JobsTreeUpdatedParams{
				ThreadID: data.RootSessionID,
				Ref:      "local:" + data.RootSessionID,
				Revision: data.TreeRevision,
			}))
		}
		return out
	case events.EventJobFinished:
		data := eventData[events.JobFinishedData](event.Data)
		if data.JobType != "shell" {
			return nil
		}
		out := []AppNotification{p.notification(appwire.NotifyEvenerJobFinished, appwire.EvenerJobParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Job: appwire.EvenerJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				Reason:           data.Reason,
				ExhaustionBudget: data.ExhaustionBudget,
				ExhaustionLimit:  data.ExhaustionLimit,
				Resumable:        data.Resumable,
				ExitCode:         data.ExitCode,
				OutputBytes:      data.OutputBytes,
				TranscriptRef:    data.TranscriptRef,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				Intent:           data.Intent,
				ParentDelegateID: data.ParentDelegateID,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
		if data.RootSessionID != "" && data.TreeRevision > 0 {
			out = append(out, p.notification(appwire.NotifyEvenerJobsTreeUpdated, appwire.JobsTreeUpdatedParams{
				ThreadID: data.RootSessionID,
				Ref:      "local:" + data.RootSessionID,
				Revision: data.TreeRevision,
			}))
		}
		return out
	case events.EventDelegateUpdated:
		data := eventData[events.DelegateUpdatedData](event.Data)
		if data.OwnerSessionID != p.threadID || data.DelegateID == "" {
			return nil
		}
		incoming := appwireDelegateInfo(data)
		merged, changed := mergeAppwireDelegateInfo(p.delegates[data.DelegateID], incoming)
		if !changed {
			return nil
		}
		p.delegates[data.DelegateID] = cloneAppwireDelegateInfo(merged)
		return []AppNotification{p.notification(appwire.NotifyEvenerDelegateUpdated, appwire.EvenerDelegateParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Delegate: cloneAppwireDelegateInfo(merged),
		})}
	case events.EventSessionEnd:
		data := eventData[events.SessionEndData](event.Data)
		state := appwire.ThreadStatusClosed
		switch data.State {
		case appwire.ThreadStatusIdle:
			state = appwire.ThreadStatusIdle
		case appwire.ThreadStatusAwaiting:
			state = appwire.ThreadStatusAwaiting
		case appwire.ThreadStatusClosed:
			state = appwire.ThreadStatusClosed
		}
		// A session end outlives no execution: the ended one's own event may
		// never arrive (a closing session stops emitting).
		p.runningTurnID = ""
		out := []AppNotification{p.threadStatus(state)}
		if state == appwire.ThreadStatusClosed {
			// Still map[string]any, not appwire.ThreadClosedParams (kcb5):
			// data.Reason is empty whenever the source reported none (the type's
			// own doc comment), but this map always emits "reason" anyway;
			// Reason is tagged `omitempty` on the struct, so a typed literal
			// would drop the key when blank. Not provably byte-identical; left
			// as a map.
			out = append(out, p.notification(appwire.NotifyThreadClosed, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"reason":   data.Reason,
			}))
		}
		return out
	default:
		return nil
	}
}

func taskSummary(data *events.TaskSummaryData) *appwire.TaskSummary {
	if data == nil {
		return nil
	}
	return &appwire.TaskSummary{ID: data.ID, Description: data.Description}
}

func taskAggregate(data *events.TaskStateData) *appwire.TaskAggregate {
	if data == nil {
		return nil
	}
	return &appwire.TaskAggregate{
		Total:     data.Total,
		Done:      data.Done,
		Cancelled: data.Cancelled,
		Remaining: data.Remaining,
		Current:   taskSummary(data.Current),
	}
}

func goalState(data *events.GoalStateData) *appwire.GoalState {
	if data == nil {
		return nil
	}
	return &appwire.GoalState{Objective: data.Objective, Status: data.Status, Iterations: data.Iterations}
}

func appwireDelegateInfo(data events.DelegateUpdatedData) appwire.EvenerDelegateInfo {
	out := appwire.EvenerDelegateInfo{
		DelegateID: data.DelegateID, OwnerSessionID: data.OwnerSessionID, RootSessionID: data.RootSessionID,
		ChildSessionID: data.ChildSessionID, TranscriptRef: data.TranscriptRef, ParentDelegateID: data.ParentDelegateID,
		Type: data.Type, Lifecycle: data.Lifecycle, Phase: data.Phase, Status: data.Status, Outcome: data.Outcome,
		Reason: data.Reason, Terminal: data.Terminal, Resumable: data.Resumable, NeedsAttention: data.NeedsAttention, NotResumableReason: data.NotResumableReason,
		ProjectionRevision: data.ProjectionRevision, Task: data.Task, Description: data.Description, AgentType: data.AgentType,
		RequestedModel: data.RequestedModel, ResolvedProfileID: data.ResolvedProfileID, ResolvedModel: data.ResolvedModel,
		Model: data.Model, ReasoningEffort: data.ReasoningEffort, OriginTurnID: data.OriginTurnID,
		OriginToolCallID: data.OriginToolCallID, OriginItemID: data.OriginItemID, RunStartedAt: data.RunStartedAt,
		RunEndedAt: data.RunEndedAt, LatestActivityAt: data.LatestActivityAt, RunningForMS: cloneInt64Pointer(data.RunningForMS),
		QuietForMS: cloneInt64Pointer(data.QuietForMS), DurationMS: cloneInt64Pointer(data.DurationMS), PacketKind: data.PacketKind,
		Message: append(json.RawMessage(nil), data.Message...), StructuredResult: append(json.RawMessage(nil), data.StructuredResult...),
		StructuredValid: cloneBoolPointer(data.StructuredValid), StructuredReason: data.StructuredReason,
		Warnings: append([]string(nil), data.Warnings...), Diagnostics: append([]string(nil), data.Diagnostics...),
		ExhaustionBudget: data.ExhaustionBudget, ExhaustionLimit: data.ExhaustionLimit,
		ExhaustionResumable: cloneBoolPointer(data.ExhaustionResumable), DelegationAllowance: data.DelegationAllowance,
		ParentWatchGranted: data.ParentWatchGranted,
	}
	if data.Usage != nil {
		out.Usage = &appwire.EvenerUsage{
			InputTokens: data.Usage.InputTokens, OutputTokens: data.Usage.OutputTokens,
			CacheReadTokens: data.Usage.CacheReadTokens, TotalTokens: data.Usage.TotalTokens,
		}
	}
	if data.Worktree != nil {
		out.Worktree = &appwire.JobActivityWorktree{
			Path: data.Worktree.Path, Branch: data.Worktree.Branch, HeadSHA: data.Worktree.HeadSHA,
			Ahead: data.Worktree.Ahead, Dirty: data.Worktree.Dirty,
		}
	}
	return out
}

func mergeAppwireDelegateInfo(current, incoming appwire.EvenerDelegateInfo) (appwire.EvenerDelegateInfo, bool) {
	if current.DelegateID == "" || incoming.ProjectionRevision > current.ProjectionRevision {
		merged := cloneAppwireDelegateInfo(incoming)
		if delegateActivityAfter(current.LatestActivityAt, merged.LatestActivityAt) {
			merged.LatestActivityAt = current.LatestActivityAt
		}
		return merged, true
	}
	if delegateActivityAfter(incoming.LatestActivityAt, current.LatestActivityAt) {
		merged := cloneAppwireDelegateInfo(current)
		merged.LatestActivityAt = incoming.LatestActivityAt
		return merged, true
	}
	return current, false
}

func delegateActivityAfter(candidate, current string) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentAt, currentErr := time.Parse(time.RFC3339Nano, current)
	return candidateErr == nil && currentErr == nil && candidateAt.After(currentAt)
}

func cloneAppwireDelegateInfo(value appwire.EvenerDelegateInfo) appwire.EvenerDelegateInfo {
	value.Message = append(json.RawMessage(nil), value.Message...)
	value.StructuredResult = append(json.RawMessage(nil), value.StructuredResult...)
	value.StructuredValid = cloneBoolPointer(value.StructuredValid)
	value.ExhaustionResumable = cloneBoolPointer(value.ExhaustionResumable)
	value.RunningForMS = cloneInt64Pointer(value.RunningForMS)
	value.QuietForMS = cloneInt64Pointer(value.QuietForMS)
	value.DurationMS = cloneInt64Pointer(value.DurationMS)
	value.Warnings = append([]string(nil), value.Warnings...)
	value.Diagnostics = append([]string(nil), value.Diagnostics...)
	if value.Usage != nil {
		usage := *value.Usage
		value.Usage = &usage
	}
	if value.Worktree != nil {
		worktree := *value.Worktree
		value.Worktree = &worktree
	}
	return value
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (p *AppEventProjector) notification(method string, params any) AppNotification {
	// Every notification carries an AppWire method name the hub routes on. The
	// callers all pass a non-empty appwire.Notify* constant; an empty method
	// would produce an unroutable wire frame.
	invariant.Hold(method != "", "appprojector: notification with empty method (threadID=%q)", p.threadID)
	if invariant.Enabled {
		// The daemon restamps every notification's threadId/ref with its
		// authoritative fanout target (server.stampAppNotificationTarget), so
		// params must be either a struct implementing NotificationTargeted or
		// a map carrying the keys — anything else would ship untargeted.
		switch params.(type) {
		case appwire.NotificationTargeted, map[string]any:
		default:
			invariant.Hold(false, "appprojector: %s params %T cannot carry a notification target", method, params)
		}
	}
	return AppNotification{ThreadID: p.threadID, Method: method, Params: params}
}

// threadStatus is a thread/status/changed carrying the running execution's
// TurnID when the thread is active.
func (p *AppEventProjector) threadStatus(status string) AppNotification {
	params := appwire.ThreadStatusChangedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		Status:   appwire.ThreadStatus{Type: status},
	}
	if status == appwire.ThreadStatusActive {
		params.ActiveTurnID = p.runningTurnID
	}
	return p.notification(appwire.NotifyThreadStatusChanged, params)
}

func eventData[T events.EventData](data events.EventData) T {
	typed, _ := data.(T)
	return typed
}

// cloneSkillNames deep-copies the per-entry skill-name lists a
// QueueChangedData snapshot carries, so the wire projection never aliases
// the daemon's queue state.
func cloneSkillNames(names [][]string) [][]string {
	if names == nil {
		return nil
	}
	out := make([][]string, len(names))
	for i, entry := range names {
		out[i] = append([]string(nil), entry...)
	}
	return out
}
