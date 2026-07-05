package appprojector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/invariant"
)

type AppNotification struct {
	ThreadID string
	Method   string
	Params   any
}

type skillActivationCandidate struct {
	turnID         string
	itemID         string
	callID         string
	skill          string
	valid          bool
	activationName string
}

type AppEventProjector struct {
	threadID string
	ref      string

	nextTurn        int
	nextItem        int
	reservedTurnID  string
	activeTurnID    string
	assistantItem   string
	assistantText   string
	reasoningItem   string
	toolItemsByKey  map[string]string
	toolArgsByKey   map[string]string
	suppressedTools map[string]struct{}
	skillCandidate  skillActivationCandidate

	lastAssistantTurnID string
	lastAssistantText   string

	// pendingTurnID/pendingCompletedAtUnix/pendingDurationMS record the most
	// recent EventTurnEnded's timing until the turn it names is actually
	// completed by one of the existing completion sites (EventUserInput,
	// EventGoalContinuation, EventError, EventSessionEnd). EventTurnEnded fires
	// before those sites on some paths (interrupt/close) and after on others
	// (a failed turn), so this is a stash, not a completion — see
	// applyPendingTiming.
	pendingTurnID          string
	pendingCompletedAtUnix int64
	pendingDurationMS      int64
}

func NewAppEventProjector(threadID, ref string) *AppEventProjector {
	return &AppEventProjector{
		threadID:        threadID,
		ref:             ref,
		toolItemsByKey:  map[string]string{},
		toolArgsByKey:   map[string]string{},
		suppressedTools: map[string]struct{}{},
	}
}

func (p *AppEventProjector) clearSkillCandidate() {
	p.skillCandidate = skillActivationCandidate{}
}

func (p *AppEventProjector) Project(event events.SessionEvent) []AppNotification {
	if p.threadID == "" {
		p.threadID = event.SessionID
	}

	switch event.Kind {
	case events.EventSessionStart:
		data := eventData[events.SessionStartData](event.Data)
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
		return []AppNotification{
			p.notification(appwire.NotifyThreadStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"thread": appwire.Thread{
					ID:            p.threadID,
					SessionID:     p.threadID,
					Source:        "local",
					ModelProvider: data.Model,
					Status:        appwire.ThreadStatus{Type: status},
					Serf: appwire.SerfThread{
						Ref:     p.ref,
						Profile: data.Profile,
					},
				},
			}),
			p.threadStatus(status),
		}
	case events.EventUserInput:
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted}
			p.applyPendingTiming(turnID, &turn)
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		turnID := p.startTurn()
		data := eventData[events.UserInputData](event.Data)
		item := appwire.ThreadItem{
			Type:                 "userMessage",
			ID:                   p.nextItemID("user"),
			TurnID:               turnID,
			TranscriptEntryIndex: data.Turn,
			Text:                 data.Text,
			Images:               projectUserInputImages(data.Images),
			Status:               "completed",
		}
		out = append(out,
			p.notification(appwire.NotifyTurnStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     startedTurn(turnID, event.Timestamp),
			}),
			p.notification(appwire.NotifyItemCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turnId":   turnID,
				"item":     item,
			}),
			p.threadStatus(appwire.ThreadStatusActive),
		)
		return out
	case events.EventGoalContinuation:
		p.clearSkillCandidate()
		// A goal continuation opens a fresh turn just like a user input
		// (close the prior turn, start a new one), but renders its prompt as
		// a systemMessage rather than a userMessage so continuations don't
		// look like the user spoke.
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted}
			p.applyPendingTiming(turnID, &turn)
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		turnID := p.startTurn()
		data := eventData[events.GoalContinuationData](event.Data)
		item := appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          p.nextItemID("goal_continuation"),
			TurnID:      turnID,
			Description: "Goal",
			Text:        data.Text,
			Status:      appwire.TurnStatusCompleted,
		}
		out = append(out,
			p.notification(appwire.NotifyTurnStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     startedTurn(turnID, event.Timestamp),
			}),
			p.notification(appwire.NotifyItemCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turnId":   turnID,
				"item":     item,
			}),
			p.threadStatus(appwire.ThreadStatusActive),
		)
		return out
	case events.EventAssistantTextStart:
		p.skillCandidate = skillActivationCandidate{}
		p.ensureTurn()
		p.assistantItem = p.nextItemID("assistant")
		p.assistantText = ""
		p.reasoningItem = ""
		return []AppNotification{p.notification(appwire.NotifyItemStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item": appwire.ThreadItem{
				Type:   "agentMessage",
				ID:     p.assistantItem,
				TurnID: p.activeTurnID,
				Status: appwire.TurnStatusInProgress,
			},
		})}
	case events.EventAssistantTextDelta:
		p.ensureAssistantItem()
		data := eventData[events.AssistantTextDeltaData](event.Data)
		p.assistantText += data.Delta
		return []AppNotification{p.notification(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			ItemID:   p.assistantItem,
			Delta:    data.Delta,
		})}
	case events.EventReasoningSummaryDelta:
		data := eventData[events.ReasoningSummaryDeltaData](event.Data)
		created := p.ensureReasoningItem()
		var out []AppNotification
		if created {
			out = append(out, p.notification(appwire.NotifyItemStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turnId":   p.activeTurnID,
				"item": appwire.ThreadItem{
					Type:   "reasoning",
					ID:     p.reasoningItem,
					TurnID: p.activeTurnID,
					Status: appwire.TurnStatusInProgress,
				},
			}))
		}
		out = append(out, p.notification(appwire.NotifyReasoningSummaryDelta, appwire.ReasoningSummaryDeltaParams{
			ThreadID:     p.threadID,
			Ref:          p.ref,
			TurnID:       p.activeTurnID,
			ItemID:       p.reasoningItem,
			SummaryIndex: data.SummaryIndex,
			Delta:        data.Delta,
		}))
		return out
	case events.EventAssistantTextEnd:
		p.skillCandidate = skillActivationCandidate{}
		p.ensureAssistantItem()
		data := eventData[events.AssistantTextEndData](event.Data)
		text := data.Text
		if text == "" {
			text = p.assistantText
		}
		item := appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     p.assistantItem,
			TurnID: p.activeTurnID,
			Text:   text,
			Status: "completed",
		}
		turnID := p.activeTurnID
		p.recordAssistantMessage(turnID, text)
		p.assistantItem = ""
		p.assistantText = ""
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   turnID,
			"item":     item,
		})}
	case events.EventAssistantTextReset:
		// A retry after partial output: discard the in-progress assistant item
		// so the retry's stream replaces it rather than appending. No-op when
		// nothing was streamed yet (no item to reset).
		if p.assistantItem == "" {
			return nil
		}
		itemID := p.assistantItem
		turnID := p.activeTurnID
		p.assistantItem = ""
		p.assistantText = ""
		return []AppNotification{p.notification(appwire.NotifyAgentMessageReset, appwire.AgentMessageResetParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   turnID,
			ItemID:   itemID,
		})}
	case events.EventCommunicate:
		p.skillCandidate = skillActivationCandidate{}
		data := eventData[events.CommunicateData](event.Data)
		text := strings.TrimSpace(data.Message)
		if text == "" {
			return nil
		}
		p.ensureTurn()
		if p.matchesLastAssistantMessage(p.activeTurnID, text) {
			return nil
		}
		item := appwire.ThreadItem{
			Type:   "agentMessage",
			ID:     p.nextItemID("assistant"),
			TurnID: p.activeTurnID,
			Text:   text,
			Status: appwire.TurnStatusCompleted,
		}
		p.recordAssistantMessage(p.activeTurnID, text)
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item":     item,
		})}
	case events.EventToolCallStart:
		p.ensureTurn()
		data := eventData[events.ToolCallStartData](event.Data)
		if data.ToolName != "use_skill" {
			p.skillCandidate = skillActivationCandidate{}
		}
		if data.ToolName == "communicate" {
			p.suppressedTools[data.CallID] = struct{}{}
			return nil
		}
		itemID := p.nextItemID("tool")
		p.toolItemsByKey[data.CallID] = itemID
		p.toolArgsByKey[data.CallID] = data.ArgumentsJSON
		if data.ToolName == "use_skill" {
			skill := useSkillNameFromArgs(data.ArgumentsJSON)
			p.skillCandidate = skillActivationCandidate{
				turnID: p.activeTurnID,
				itemID: itemID,
				callID: data.CallID,
				skill:  skill,
				valid:  skill != "",
			}
		}
		return []AppNotification{p.notification(appwire.NotifyItemStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item": appwire.ThreadItem{
				Type:          "commandExecution",
				ID:            itemID,
				TurnID:        p.activeTurnID,
				ToolName:      data.ToolName,
				CallID:        data.CallID,
				ArgumentsJSON: data.ArgumentsJSON,
				Description:   data.Description,
				Status:        appwire.TurnStatusInProgress,
			},
		})}
	case events.EventToolCallOutputDelta:
		data := eventData[events.ToolCallOutputDeltaData](event.Data)
		if _, ok := p.suppressedTools[data.CallID]; ok {
			return nil
		}
		return []AppNotification{p.notification(appwire.NotifyToolOutputDelta, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"itemId":   p.toolItemID(data.CallID),
			"callId":   data.CallID,
			"delta":    data.Delta,
		})}
	case events.EventToolCallEnd:
		data := eventData[events.ToolCallEndData](event.Data)
		if _, ok := p.suppressedTools[data.CallID]; ok {
			delete(p.suppressedTools, data.CallID)
			return nil
		}
		raw := data.ToolState
		if p.skillCandidate.valid && p.skillCandidate.callID == data.CallID && p.skillCandidate.activationName != "" {
			raw = skillActivationRaw(p.skillCandidate.activationName)
		}
		item := appwire.ThreadItem{
			Type:     "commandExecution",
			ID:       p.toolItemID(data.CallID),
			TurnID:   p.activeTurnID,
			ToolName: data.ToolName,
			CallID:   data.CallID,
			Output:   data.Output,
			Error:    data.Error,
			Status:   "completed",
			Raw:      raw,
		}
		argsJSON := p.toolArgsByKey[data.CallID]
		if data.ToolName == "use_skill" && data.Error == "" {
			skill := useSkillNameFromArgs(argsJSON)
			activationName := ""
			if p.skillCandidate.callID == data.CallID {
				activationName = p.skillCandidate.activationName
			}
			p.skillCandidate = skillActivationCandidate{
				turnID:         p.activeTurnID,
				itemID:         item.ID,
				callID:         data.CallID,
				skill:          skill,
				valid:          skill != "",
				activationName: activationName,
			}
		} else {
			p.skillCandidate = skillActivationCandidate{}
		}
		delete(p.toolItemsByKey, data.CallID)
		delete(p.toolArgsByKey, data.CallID)
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item":     item,
		})}
	case events.EventToolCallRepaired:
		// This fires before EventToolCallStart creates the CallID-keyed tool
		// item (repair runs before PreToolUse hooks, which run before the
		// start event), so there is no item yet to annotate. Render it as a
		// standalone system announcement instead, the same way other
		// out-of-band, no-item-state events (hook end, plugin loaded, ...)
		// are surfaced.
		data := eventData[events.ToolCallRepairedData](event.Data)
		return p.systemAnnouncement("tool_repair", "Tool call repaired", toolCallRepairedAnnouncement(data))
	case events.EventWarning:
		p.clearSkillCandidate()
		data := eventData[events.WarningData](event.Data)
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Message)
		return []AppNotification{p.notification(appwire.NotifyWarning, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"message":  data.Message,
			"source":   string(info.Source),
			"title":    info.Title,
			"hint":     info.Hint,
			"warning":  event.Data,
		})}
	case events.EventError:
		p.clearSkillCandidate()
		data := eventData[events.ErrorData](event.Data)
		message := strings.TrimSpace(data.Error)
		if message == "" {
			message = "session error"
		}
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, message)
		cause := projectErrorCause(data.Cause)

		// A user-cancelled turn is not a failure: surface it as a warning and
		// let the interrupted SessionEnd own the turn's terminal state (do NOT
		// complete the turn as failed here).
		if isContextCanceledError(message) {
			return []AppNotification{p.notification(appwire.NotifyWarning, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"message":  message,
				"source":   string(info.Source),
				"title":    info.Title,
				"hint":     info.Hint,
				"cause":    cause,
				"warning": events.WarningData{
					Message: message,
					Source:  string(info.Source),
					Title:   info.Title,
					Hint:    info.Hint,
				},
			})}
		}

		// A genuine turn failure is surfaced exactly once, as a failed turn. The
		// TurnError carries the full diagnostic (message/source/title/hint/cause);
		// emitting a separate NotifyWarning too would make the same error render
		// twice in clients that show both the warning channel and turn errors.
		p.ensureTurn()
		turnID := p.activeTurnID
		p.activeTurnID = ""
		p.assistantItem = ""
		p.assistantText = ""
		p.toolItemsByKey = map[string]string{}
		p.suppressedTools = map[string]struct{}{}
		turn := appwire.Turn{
			ID:     turnID,
			Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{
				Message: message,
				Source:  string(info.Source),
				Title:   info.Title,
				Hint:    info.Hint,
				Cause:   cause,
			},
		}
		// EventTurnEnded runs after EventError on the failure path (see
		// handleModelError), so no pending timing has been recorded yet here;
		// this call is a no-op today but keeps the timing path uniform across
		// all four completion sites.
		p.applyPendingTiming(turnID, &turn)
		return []AppNotification{
			p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}),
		}
	case events.EventSteeringInjected:
		p.clearSkillCandidate()
		data := eventData[events.SteeringInjectedData](event.Data)
		images := projectUserInputImages(data.Images)
		text := data.Text
		if strings.TrimSpace(text) == "" {
			text = apptranscript.ImagePlaceholder(len(images))
		}
		return []AppNotification{p.notification(appwire.NotifySerfSteeringInjected, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"text":     text,
			"images":   images,
		})}
	case events.EventCompactionTurn:
		p.clearSkillCandidate()
		data := eventData[events.CompactionTurnData](event.Data)
		return p.systemAnnouncement("compaction", apptranscript.CompactionDescription(data.Kind), data.Text)
	case events.EventTurnLimit:
		p.clearSkillCandidate()
		data := eventData[events.TurnLimitData](event.Data)
		return p.systemAnnouncement("turn_limit", "Turn limit", turnLimitAnnouncement(data))
	case events.EventLoopDetection:
		p.clearSkillCandidate()
		data := eventData[events.LoopDetectionData](event.Data)
		return p.systemAnnouncement("loop_detection", "Loop detection", data.Message)
	case events.EventGoalEnded:
		p.clearSkillCandidate()
		data := eventData[events.GoalEndedData](event.Data)
		return p.systemAnnouncement("goal", "Goal", goalEndText(data))
	case events.EventSkillActivated:
		data := eventData[events.SkillActivatedData](event.Data)
		name := strings.TrimSpace(data.Name)
		if p.skillCandidate.valid && p.skillCandidate.turnID == p.activeTurnID && p.skillCandidate.skill == name {
			candidate := p.skillCandidate
			if _, inFlight := p.toolItemsByKey[candidate.callID]; inFlight {
				p.skillCandidate.activationName = name
				return nil
			}
			p.skillCandidate = skillActivationCandidate{}
			item := appwire.ThreadItem{
				Type:     "commandExecution",
				ID:       candidate.itemID,
				TurnID:   candidate.turnID,
				ToolName: "use_skill",
				CallID:   candidate.callID,
				Status:   "completed",
				Raw:      skillActivationRaw(name),
			}
			return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turnId":   candidate.turnID,
				"item":     item,
			})}
		}
		p.skillCandidate = skillActivationCandidate{}
		return p.systemAnnouncement("skill", "Skill activated", "Activated skill: "+data.Name)
	case events.EventContextCompaction:
		p.clearSkillCandidate()
		data := eventData[events.ContextCompactionData](event.Data)
		return p.systemAnnouncementWithRaw("context_compaction", "Context compaction", contextCompactionAnnouncement(data), contextCompactionRaw(data))
	case events.EventPluginLoaded:
		p.clearSkillCandidate()
		data := eventData[events.PluginLoadedData](event.Data)
		return p.systemAnnouncement("plugin", "Plugin loaded", pluginLoadedAnnouncement(data))
	case events.EventHookStart:
		return nil
	case events.EventHookEnd:
		p.clearSkillCandidate()
		data := eventData[events.HookEndData](event.Data)
		return p.systemAnnouncement("hook", "Hook", hookEndAnnouncement(data))
	case events.EventForkSummary:
		p.clearSkillCandidate()
		data := eventData[events.ForkSummaryData](event.Data)
		return p.systemAnnouncement("fork_summary", "Fork summary", forkSummaryAnnouncement(data))
	case events.EventPromptLoaded:
		p.clearSkillCandidate()
		data := eventData[events.PromptLoadedData](event.Data)
		return p.systemAnnouncement("prompt", "Prompt loaded", promptLoadedAnnouncement(data))
	case events.EventRoundTimings:
		p.clearSkillCandidate()
		data := eventData[events.RoundTimings](event.Data)
		return p.systemAnnouncement("round_timings", "Round timings", roundTimingsAnnouncement(data))
	case events.EventQueueChanged:
		p.clearSkillCandidate()
		data := eventData[events.QueueChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Queue:    appwire.QueueState{Depth: data.Depth, Preview: append([]string(nil), data.Preview...)},
		})}
	case events.EventJobStarted:
		p.clearSkillCandidate()
		data := eventData[events.JobStartedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfJobStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"job": appwire.SerfJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				TranscriptRef:    data.TranscriptRef,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
	case events.EventJobFinished:
		p.clearSkillCandidate()
		data := eventData[events.JobFinishedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfJobFinished, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"job": appwire.SerfJobInfo{
				JobID:            data.JobID,
				JobType:          data.JobType,
				Status:           data.Status,
				Reason:           data.Reason,
				ExitCode:         data.ExitCode,
				OutputBytes:      data.OutputBytes,
				TranscriptRef:    data.TranscriptRef,
				FromWatch:        data.FromWatch,
				Background:       data.Background,
				Command:          data.Command,
				DelegateID:       data.DelegateID,
				Task:             data.Task,
				OriginTurnID:     data.OriginTurnID,
				OriginToolCallID: data.OriginToolCallID,
				OriginItemID:     data.OriginItemID,
			},
		})}
	case events.EventTurnEnded:
		if p.activeTurnID == "" {
			return nil // turn already completed (e.g. failed via EventError)
		}
		data := eventData[events.TurnEndedData](event.Data)
		p.pendingTurnID = p.activeTurnID
		p.pendingCompletedAtUnix = event.Timestamp.Unix()
		p.pendingDurationMS = data.TurnDurationMS
		return nil
	case events.EventSessionEnd:
		p.clearSkillCandidate()
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
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnStatus := appwire.TurnStatusCompleted
			if state == appwire.ThreadStatusClosed || data.Interrupted {
				turnStatus = appwire.TurnStatusInterrupted
			}
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.reasoningItem = ""
			p.toolItemsByKey = map[string]string{}
			p.toolArgsByKey = map[string]string{}
			p.suppressedTools = map[string]struct{}{}
			turn := appwire.Turn{ID: turnID, Status: turnStatus}
			p.applyPendingTiming(turnID, &turn)
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     turn,
			}))
		}
		out = append(out, p.threadStatus(state))
		if state == appwire.ThreadStatusClosed {
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

func useSkillNameFromArgs(raw string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	for _, key := range []string{"skill_name", "name"} {
		if v, ok := args[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func skillActivationRaw(name string) json.RawMessage {
	payload := struct {
		SkillActivation struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skillActivation"`
	}{}
	payload.SkillActivation.Name = name
	payload.SkillActivation.Text = "Activated skill: " + name
	raw, _ := json.Marshal(payload)
	return raw
}

func (p *AppEventProjector) notification(method string, params any) AppNotification {
	// Every notification carries an AppWire method name the hub routes on. The
	// callers all pass a non-empty appwire.Notify* constant; an empty method
	// would produce an unroutable wire frame.
	invariant.Hold(method != "", "appprojector: notification with empty method (threadID=%q)", p.threadID)
	return AppNotification{ThreadID: p.threadID, Method: method, Params: params}
}

// startedTurn builds an in-progress turn carrying its start time so the web UI
// can report how long the active turn has been running. A zero timestamp leaves
// StartedAt unset rather than reporting the Unix epoch.
func startedTurn(id string, startedAt time.Time) appwire.Turn {
	turn := appwire.Turn{ID: id, Status: appwire.TurnStatusInProgress}
	if !startedAt.IsZero() {
		unix := startedAt.Unix()
		turn.StartedAt = &unix
	}
	return turn
}

// applyPendingTiming stamps CompletedAt/DurationMS from the most recently
// recorded EventTurnEnded onto turn, but only when that recorded turn is the
// one now completing (turnID matches). A mismatch — or no pending timing at
// all, as at the EventError site where EventTurnEnded has not run yet — leaves
// turn untouched.
func (p *AppEventProjector) applyPendingTiming(turnID string, turn *appwire.Turn) {
	if p.pendingTurnID == "" || p.pendingTurnID != turnID {
		return
	}
	c := p.pendingCompletedAtUnix
	d := p.pendingDurationMS
	turn.CompletedAt = &c
	turn.DurationMS = &d
	p.pendingTurnID = ""
	p.pendingCompletedAtUnix = 0
	p.pendingDurationMS = 0
}

func (p *AppEventProjector) systemAnnouncement(prefix, description, text string) []AppNotification {
	return p.systemAnnouncementWithRaw(prefix, description, text, nil)
}

// systemAnnouncementWithRaw renders a lifecycle system one-liner like
// systemAnnouncement, additionally attaching structured detail to the item's
// Raw field. The web can then surface that detail (e.g. a compaction
// before→after expand, mockup #17 Alt A) from real numbers instead of
// re-parsing the prose text.
func (p *AppEventProjector) systemAnnouncementWithRaw(prefix, description, text string, raw json.RawMessage) []AppNotification {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	turnID := p.activeTurnID
	if turnID == "" {
		p.nextTurn++
		turnID = fmt.Sprintf("turn_%d", p.nextTurn)
	}
	item := appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          p.nextItemID(prefix),
		TurnID:      turnID,
		Description: strings.TrimSpace(description),
		Text:        text,
		Status:      appwire.TurnStatusCompleted,
		Raw:         raw,
	}
	if p.activeTurnID == "" {
		return []AppNotification{p.notification(appwire.NotifyTurnCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turn": appwire.Turn{
				ID:        turnID,
				Items:     []appwire.ThreadItem{item},
				ItemsView: "full",
				Status:    appwire.TurnStatusCompleted,
			},
		})}
	}
	return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
		"threadId": p.threadID,
		"ref":      p.ref,
		"turnId":   turnID,
		"item":     item,
	})}
}

func isContextCanceledError(message string) bool {
	return strings.TrimSpace(message) == context.Canceled.Error()
}

func (p *AppEventProjector) threadStatus(status string) AppNotification {
	return p.notification(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		Status:   appwire.ThreadStatus{Type: status},
	})
}

func projectUserInputImages(images []events.UserInputImage) []appwire.InputItem {
	if len(images) == 0 {
		return nil
	}
	out := make([]appwire.InputItem, 0, len(images))
	for _, img := range images {
		out = append(out, appwire.InputItem{
			Type:      "image",
			MediaType: img.MediaType,
			Data:      append([]byte(nil), img.Data...),
			Name:      img.Name,
		})
	}
	return out
}

// goalEndText renders the terminal /goal report line from a GoalEnded payload.
// A completed goal reads "✓ Goal achieved"; a blocked goal "⊘ Goal blocked"
// (with the reason appended when present); any other terminal status falls back
// to "⊘ Goal stopped".
func goalEndText(data events.GoalEndedData) string {
	switch data.Status {
	case "complete":
		return "✓ Goal achieved"
	case "blocked":
		if reason := strings.TrimSpace(data.Reason); reason != "" {
			return "⊘ Goal blocked: " + reason
		}
		return "⊘ Goal blocked"
	default:
		return "⊘ Goal stopped"
	}
}

func turnLimitAnnouncement(data events.TurnLimitData) string {
	var lines []string
	if data.MaxTurns > 0 {
		lines = append(lines, fmt.Sprintf("Maximum turns reached: %d", data.MaxTurns))
	}
	if data.MaxToolRoundsPerInput > 0 {
		lines = append(lines, fmt.Sprintf("Maximum tool rounds per input reached: %d", data.MaxToolRoundsPerInput))
	}
	if len(lines) == 0 {
		return "Turn limit reached"
	}
	return strings.Join(lines, "\n")
}

// contextCompactionRaw marshals the structured compaction numbers under a
// "compaction" key on the system item's Raw field. The web reads these to draw
// an honest before→after expand (mockup #17 Alt A) from real numbers. Returns
// nil when there is nothing to carry so the item stays clean.
func contextCompactionRaw(data events.ContextCompactionData) json.RawMessage {
	if data.Layer == "" && data.TurnsBefore == 0 && data.TurnsAfter == 0 &&
		data.EstTokensBefore == 0 && data.EstTokensAfter == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"compaction": data})
	if err != nil {
		return nil
	}
	return raw
}

func contextCompactionAnnouncement(data events.ContextCompactionData) string {
	var lines []string
	if strings.TrimSpace(data.Layer) != "" {
		lines = append(lines, "Layer: "+strings.TrimSpace(data.Layer))
	}
	if data.TurnsBefore > 0 || data.TurnsAfter > 0 {
		lines = append(lines, fmt.Sprintf("Turns: %d -> %d", data.TurnsBefore, data.TurnsAfter))
	}
	if data.EstTokensBefore > 0 || data.EstTokensAfter > 0 {
		lines = append(lines, fmt.Sprintf("Estimated tokens: %d -> %d", data.EstTokensBefore, data.EstTokensAfter))
	}
	if len(lines) == 0 {
		return "Context compaction ran"
	}
	return strings.Join(lines, "\n")
}

func pluginLoadedAnnouncement(data events.PluginLoadedData) string {
	name := strings.TrimSpace(data.Name)
	if name == "" {
		name = "plugin"
	}
	return fmt.Sprintf("Loaded plugin %s (%d skills, %d agents, %d MCP servers)", name, data.SkillCount, data.AgentCount, data.MCPCount)
}

func hookEndAnnouncement(data events.HookEndData) string {
	parts := []string{fallbackLabel(data.Event, "hook") + " hook"}
	if pluginName := strings.TrimSpace(data.PluginName); pluginName != "" {
		parts = append(parts, pluginName)
	}
	if matcher := strings.TrimSpace(data.Matcher); matcher != "" {
		parts = append(parts, matcher)
	}
	if hookType := strings.TrimSpace(data.HookType); hookType != "" {
		parts = append(parts, hookType)
	}
	parts = append(parts, fmt.Sprintf("exit %d", data.ExitCode))
	return strings.Join(parts, " ")
}

func toolCallRepairedAnnouncement(data events.ToolCallRepairedData) string {
	name := fallbackLabel(data.ToolName, "tool call")
	if len(data.Changes) == 0 {
		return fmt.Sprintf("Repaired %s", name)
	}
	return fmt.Sprintf("Repaired %s: %s", name, strings.Join(data.Changes, ", "))
}

func forkSummaryAnnouncement(data events.ForkSummaryData) string {
	if data.Turn > 0 {
		return fmt.Sprintf("Fork summary captured at transcript turn %d", data.Turn)
	}
	return "Fork summary captured"
}

func promptLoadedAnnouncement(data events.PromptLoadedData) string {
	label := fallbackLabel(data.Label, "prompt")
	if data.Size > 0 {
		return fmt.Sprintf("Loaded prompt %s (%d B)", label, data.Size)
	}
	return "Loaded prompt " + label
}

func roundTimingsAnnouncement(data events.RoundTimings) string {
	parts := []string{
		fmt.Sprintf("Round %d", data.Round),
		"total=" + data.TotalRound.String(),
		"llm=" + data.LLMCall.String(),
		"context=" + data.ContextMgmt.String(),
		"tools=" + data.ToolExec.String(),
		"prompt=" + data.SystemPrompt.String(),
		"history=" + data.HistoryExpand.String(),
		"tool_defs=" + data.ToolDefs.String(),
		"persistence=" + data.Persistence.String(),
		"after_action=" + data.AfterAction.String(),
		"overhead=" + data.LoopOverhead.String(),
	}
	return strings.Join(parts, " ")
}

func fallbackLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func (p *AppEventProjector) startTurn() string {
	if p.reservedTurnID != "" {
		p.activeTurnID = p.reservedTurnID
		p.reservedTurnID = ""
	} else {
		p.nextTurn++
		p.activeTurnID = fmt.Sprintf("turn_%d", p.nextTurn)
	}
	p.assistantItem = ""
	p.assistantText = ""
	// startTurn always yields a usable turn id (a promoted reservation or a
	// freshly minted turn_N); the item-emitting paths rely on activeTurnID being
	// non-empty after this returns.
	invariant.Hold(p.activeTurnID != "", "appprojector: startTurn left activeTurnID empty")
	return p.activeTurnID
}

func (p *AppEventProjector) ReserveTurnID() string {
	if p.reservedTurnID != "" {
		return p.reservedTurnID
	}
	p.nextTurn++
	p.reservedTurnID = fmt.Sprintf("turn_%d", p.nextTurn)
	return p.reservedTurnID
}

func (p *AppEventProjector) ReleaseReservedTurnID(turnID string) {
	if p.reservedTurnID == turnID {
		p.reservedTurnID = ""
	}
}

func (p *AppEventProjector) ActiveTurnID() string {
	if p.activeTurnID != "" {
		return p.activeTurnID
	}
	return p.reservedTurnID
}

func (p *AppEventProjector) ensureTurn() {
	if p.activeTurnID == "" {
		p.startTurn()
	}
}

func (p *AppEventProjector) ensureAssistantItem() {
	p.ensureTurn()
	if p.assistantItem == "" {
		p.assistantItem = p.nextItemID("assistant")
	}
}

// ensureReasoningItem makes sure an in-progress reasoning item exists for the
// active turn, returning true when it had to create one (so the caller emits a
// single item/started before the first delta).
func (p *AppEventProjector) ensureReasoningItem() bool {
	p.ensureTurn()
	if p.reasoningItem == "" {
		p.reasoningItem = p.nextItemID("reasoning")
		return true
	}
	return false
}

func (p *AppEventProjector) nextItemID(prefix string) string {
	p.nextItem++
	return fmt.Sprintf("item_%s_%d", prefix, p.nextItem)
}

func (p *AppEventProjector) toolItemID(callID string) string {
	if itemID := p.toolItemsByKey[callID]; itemID != "" {
		return itemID
	}
	itemID := p.nextItemID("tool")
	p.toolItemsByKey[callID] = itemID
	return itemID
}

func (p *AppEventProjector) recordAssistantMessage(turnID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.lastAssistantTurnID = turnID
	p.lastAssistantText = text
}

func (p *AppEventProjector) matchesLastAssistantMessage(turnID, text string) bool {
	return turnID != "" &&
		turnID == p.lastAssistantTurnID &&
		strings.TrimSpace(text) == p.lastAssistantText
}

// projectErrorCause maps the agent-side structured cause attached to
// EventError (kata ts0x) to its wire-level appwire shape (kata cmfz).
// Returns nil when the caller did not attach a cause so the warning
// envelope's "cause" field stays omitempty-eligible on the wire.
func projectErrorCause(cause *events.ErrorCause) *appwire.DiagnosticCause {
	if cause == nil {
		return nil
	}
	return &appwire.DiagnosticCause{
		Kind:     cause.Kind,
		Provider: cause.Provider,
		Model:    cause.Model,
		Status:   cause.Status,
	}
}

// eventData returns the concrete payload carried by a SessionEvent.Data value.
// Data is now the sealed events.EventData interface holding the exact payload
// the emit site constructed, so a direct type assertion is authoritative and
// the former JSON marshal/unmarshal round-trip is gone. A mismatched T (a
// projector bug) yields the zero value rather than panicking.
func eventData[T events.EventData](data events.EventData) T {
	typed, _ := data.(T)
	return typed
}
