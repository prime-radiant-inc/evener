package server

import (
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

	nextTurn       int
	nextItem       int
	reservedTurnID string
	activeTurnID   string
	assistantItem  string
	toolItemsByKey map[string]string
}

func NewAppEventProjector(threadID, ref string) *AppEventProjector {
	return &AppEventProjector{
		threadID:       threadID,
		ref:            ref,
		toolItemsByKey: map[string]string{},
	}
}

func (p *AppEventProjector) Project(event agent.SessionEvent) []AppNotification {
	if p.threadID == "" {
		p.threadID = event.SessionID
	}

	switch event.Kind {
	case agent.EventSessionStart:
		return []AppNotification{p.threadStatus(appwire.ThreadStatusIdle)}
	case agent.EventUserInput:
		turnID := p.startTurn()
		data := eventData[agent.UserInputData](event.Data)
		item := appwire.ThreadItem{
			Type:                 "user_message",
			ID:                   p.nextItemID("user"),
			TurnID:               turnID,
			TranscriptEntryIndex: data.Turn,
			Text:                 data.Text,
			Status:               "completed",
		}
		return []AppNotification{
			p.notification(appwire.NotifyTurnStarted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     appwire.Turn{ID: turnID, Status: appwire.TurnStatusRunning},
			}),
			p.notification(appwire.NotifyItemCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turnId":   turnID,
				"item":     item,
			}),
			p.threadStatus(appwire.ThreadStatusProcessing),
		}
	case agent.EventAssistantTextStart:
		p.ensureTurn()
		p.assistantItem = p.nextItemID("assistant")
		return []AppNotification{p.notification(appwire.NotifyItemStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item": appwire.ThreadItem{
				Type:   "agent_message",
				ID:     p.assistantItem,
				TurnID: p.activeTurnID,
				Status: appwire.TurnStatusRunning,
			},
		})}
	case agent.EventAssistantTextDelta:
		p.ensureAssistantItem()
		data := eventData[agent.AssistantTextDeltaData](event.Data)
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
		item := appwire.ThreadItem{
			Type:   "agent_message",
			ID:     p.assistantItem,
			TurnID: p.activeTurnID,
			Text:   data.Text,
			Status: "completed",
		}
		turnID := p.activeTurnID
		p.assistantItem = ""
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   turnID,
			"item":     item,
		})}
	case agent.EventToolCallStart:
		p.ensureTurn()
		data := eventData[agent.ToolCallStartData](event.Data)
		itemID := p.nextItemID("tool")
		p.toolItemsByKey[data.CallID] = itemID
		return []AppNotification{p.notification(appwire.NotifyItemStarted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   p.activeTurnID,
			"item": appwire.ThreadItem{
				Type:          "tool_call",
				ID:            itemID,
				TurnID:        p.activeTurnID,
				ToolName:      data.ToolName,
				CallID:        data.CallID,
				ArgumentsJSON: data.ArgumentsJSON,
				Status:        appwire.TurnStatusRunning,
			},
		})}
	case agent.EventToolCallOutputDelta:
		data := eventData[agent.ToolCallOutputDeltaData](event.Data)
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
		item := appwire.ThreadItem{
			Type:     "tool_call",
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
		p.ensureTurn()
		turnID := p.activeTurnID
		p.activeTurnID = ""
		p.assistantItem = ""
		return []AppNotification{
			p.notification(appwire.NotifyWarning, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"message":  message,
				"source":   string(info.Source),
				"title":    info.Title,
				"hint":     info.Hint,
				"warning": agent.WarningData{
					Message: message,
					Source:  string(info.Source),
					Title:   info.Title,
					Hint:    info.Hint,
				},
			}),
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
					},
				},
			}),
		}
	case agent.EventSteeringInjected:
		data := eventData[agent.SteeringInjectedData](event.Data)
		return []AppNotification{p.notification(appwire.NotifySerfSteeringInjected, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"text":     data.Text,
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
		if strings.EqualFold(data.State, "IDLE") {
			state = appwire.ThreadStatusIdle
		}
		out := []AppNotification{}
		if p.activeTurnID != "" {
			turnStatus := appwire.TurnStatusCompleted
			if state == appwire.ThreadStatusClosed {
				turnStatus = appwire.TurnStatusCanceled
			}
			turnID := p.activeTurnID
			p.activeTurnID = ""
			p.assistantItem = ""
			p.toolItemsByKey = map[string]string{}
			out = append(out, p.notification(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": p.threadID,
				"ref":      p.ref,
				"turn":     appwire.Turn{ID: turnID, Status: turnStatus},
			}))
		}
		return append(out, p.threadStatus(state))
	default:
		return nil
	}
}

func (p *AppEventProjector) notification(method string, params any) AppNotification {
	return AppNotification{ThreadID: p.threadID, Method: method, Params: params}
}

func (p *AppEventProjector) threadStatus(status string) AppNotification {
	return p.notification(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		Status:   appwire.ThreadStatus{Type: status},
	})
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
