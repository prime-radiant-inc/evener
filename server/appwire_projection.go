package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
)

type AppNotification struct {
	ThreadID string
	Method   string
	Params   any
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
	toolItemsByKey  map[string]string
	suppressedTools map[string]struct{}

	lastAssistantTurnID string
	lastAssistantText   string
}

func NewAppEventProjector(threadID, ref string) *AppEventProjector {
	return &AppEventProjector{
		threadID:        threadID,
		ref:             ref,
		toolItemsByKey:  map[string]string{},
		suppressedTools: map[string]struct{}{},
	}
}

func (p *AppEventProjector) Project(event agent.SessionEvent) []AppNotification {
	if p.threadID == "" {
		p.threadID = event.SessionID
	}

	switch event.Kind {
	case agent.EventSessionStart:
		data := eventData[agent.SessionStartData](event.Data)
		return []AppNotification{
			p.notification(appwire.NotifyThreadStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"thread": appwire.Thread{
					ID:            p.threadID,
					SessionID:     p.threadID,
					Source:        "local",
					ModelProvider: data.Model,
					Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
					Serf: appwire.SerfThread{
						Ref:     p.ref,
						Profile: data.Profile,
					},
				},
			}),
			p.threadStatus(appwire.ThreadStatusIdle),
		}
	case agent.EventUserInput:
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.assistantText = ""
			p.toolItemsByKey = map[string]string{}
			p.suppressedTools = map[string]struct{}{}
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted},
			}))
		}
		turnID := p.startTurn()
		data := eventData[agent.UserInputData](event.Data)
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
				"turn":     appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress},
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
	case agent.EventAssistantTextStart:
		p.ensureTurn()
		p.assistantItem = p.nextItemID("assistant")
		p.assistantText = ""
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
	case agent.EventAssistantTextDelta:
		p.ensureAssistantItem()
		data := eventData[agent.AssistantTextDeltaData](event.Data)
		p.assistantText += data.Delta
		return []AppNotification{p.notification(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			TurnID:   p.activeTurnID,
			ItemID:   p.assistantItem,
			Delta:    data.Delta,
		})}
	case agent.EventAssistantTextEnd:
		p.ensureAssistantItem()
		data := eventData[agent.AssistantTextEndData](event.Data)
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
	case agent.EventCommunicate:
		data := eventData[agent.CommunicateData](event.Data)
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
	case agent.EventToolCallStart:
		p.ensureTurn()
		data := eventData[agent.ToolCallStartData](event.Data)
		if data.ToolName == "communicate" {
			p.suppressedTools[data.CallID] = struct{}{}
			return nil
		}
		itemID := p.nextItemID("tool")
		p.toolItemsByKey[data.CallID] = itemID
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
	case agent.EventToolCallOutputDelta:
		data := eventData[agent.ToolCallOutputDeltaData](event.Data)
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
	case agent.EventToolCallEnd:
		data := eventData[agent.ToolCallEndData](event.Data)
		if _, ok := p.suppressedTools[data.CallID]; ok {
			delete(p.suppressedTools, data.CallID)
			return nil
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
			Raw:      data.ToolState,
		}
		delete(p.toolItemsByKey, data.CallID)
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item":     item,
		})}
	case agent.EventWarning:
		data := eventData[agent.WarningData](event.Data)
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
	case agent.EventError:
		data := eventData[agent.ErrorData](event.Data)
		message := strings.TrimSpace(data.Error)
		if message == "" {
			message = "session error"
		}
		info := diagnostic.FromFields(data.Source, data.Title, data.Hint, message)
		cause := projectErrorCause(data.Cause)
		warning := p.notification(appwire.NotifyWarning, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"message":  message,
			"source":   string(info.Source),
			"title":    info.Title,
			"hint":     info.Hint,
			"cause":    cause,
			"warning": agent.WarningData{
				Message: message,
				Source:  string(info.Source),
				Title:   info.Title,
				Hint:    info.Hint,
			},
		})
		if isContextCanceledError(message) {
			return []AppNotification{warning}
		}
		p.ensureTurn()
		turnID := p.activeTurnID
		p.activeTurnID = ""
		p.assistantItem = ""
		p.assistantText = ""
		p.toolItemsByKey = map[string]string{}
		p.suppressedTools = map[string]struct{}{}
		return []AppNotification{
			warning,
			p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn": appwire.Turn{
					ID:     turnID,
					Status: appwire.TurnStatusFailed,
					Error: &appwire.TurnError{
						Message: message,
						Source:  string(info.Source),
						Title:   info.Title,
						Hint:    info.Hint,
						Cause:   cause,
					},
				},
			}),
		}
	case agent.EventSteeringInjected:
		data := eventData[agent.SteeringInjectedData](event.Data)
		images := projectUserInputImages(data.Images)
		text := data.Text
		if strings.TrimSpace(text) == "" {
			text = imagePreviewText(len(images))
		}
		return []AppNotification{p.notification(appwire.NotifySerfSteeringInjected, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"text":     text,
			"images":   images,
		})}
	case agent.EventQueueChanged:
		data := eventData[agent.QueueChangedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: p.threadID,
			Ref:      p.ref,
			Queue:    appwire.QueueState{Depth: data.Depth, Preview: append([]string(nil), data.Preview...)},
		})}
	case agent.EventSubagentStart:
		return []AppNotification{p.notification(appwire.NotifySerfSubagentStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"subagent": event.Data,
		})}
	case agent.EventSubagentEnd:
		return []AppNotification{p.notification(appwire.NotifySerfSubagentEnded, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"subagent": event.Data,
		})}
	case agent.EventSessionEnd:
		data := eventData[agent.SessionEndData](event.Data)
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
			p.toolItemsByKey = map[string]string{}
			p.suppressedTools = map[string]struct{}{}
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     appwire.Turn{ID: turnID, Status: turnStatus},
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

func (p *AppEventProjector) notification(method string, params any) AppNotification {
	return AppNotification{ThreadID: p.threadID, Method: method, Params: params}
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

func projectUserInputImages(images []agent.UserInputImage) []appwire.InputItem {
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

func imagePreviewText(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "[image]"
	default:
		return fmt.Sprintf("[%d images]", n)
	}
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
func projectErrorCause(cause *agent.ErrorCause) *appwire.DiagnosticCause {
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

func eventData[T any](data any) T {
	if typed, ok := data.(T); ok {
		return typed
	}
	var zero T
	raw, err := json.Marshal(data)
	if err != nil {
		return zero
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero
	}
	return zero
}
