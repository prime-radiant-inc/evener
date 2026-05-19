package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type streamEvent struct {
	Event string
	Data  string
}

type streamEventMsg streamEvent

type streamConnectedMsg struct{}

type streamErrorMsg struct{ err error }

func streamAppwire(ctx context.Context, addr string, send func(tea.Msg)) {
	transport, err := appwire.DialWebSocket(ctx, "ws://"+addr+"/rpc", http.DefaultClient)
	if err != nil {
		send(streamErrorMsg{err})
		return
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serf-tui", Version: "tui"},
	}); err != nil {
		send(streamErrorMsg{err})
		return
	}
	send(streamConnectedMsg{})

	translator := newAppwireStreamTranslator()
	resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{IncludeTurns: true, ItemsView: "full", Subscribe: true})
	if err != nil {
		send(streamErrorMsg{err})
		return
	}
	for _, ev := range translator.eventsFromThread(resp.Thread) {
		send(streamEventMsg(ev))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-client.Notifications():
			if !ok {
				send(streamErrorMsg{fmt.Errorf("appwire stream closed")})
				return
			}
			for _, ev := range translator.eventsFromNotification(notification) {
				send(streamEventMsg(ev))
			}
		}
	}
}

func streamEventsFromThread(thread appwire.Thread) []streamEvent {
	return newAppwireStreamTranslator().eventsFromThread(thread)
}

func streamEventsFromNotification(notification appwire.Notification) []streamEvent {
	return newAppwireStreamTranslator().eventsFromNotification(notification)
}

type appwireStreamTranslator struct {
	activeToolCalls map[string]bool
	completedItems  map[string]bool
}

func newAppwireStreamTranslator() *appwireStreamTranslator {
	return &appwireStreamTranslator{
		activeToolCalls: make(map[string]bool),
		completedItems:  make(map[string]bool),
	}
}

func (t *appwireStreamTranslator) eventsFromThread(thread appwire.Thread) []streamEvent {
	events := []streamEvent{newStreamEvent("SESSION_START", map[string]any{
		"session_id": firstNonEmptyString(thread.SessionID, thread.ID),
		"profile":    thread.Serf.Profile,
		"model":      thread.ModelProvider,
		"restored":   true,
	})}
	for _, turn := range thread.Turns {
		events = append(events, t.eventsFromItems(turn.ID, turn.Status, turn.Items)...)
		events = append(events, eventsFromFailedTurn(turn)...)
	}
	return events
}

func (t *appwireStreamTranslator) eventsFromNotification(notification appwire.Notification) []streamEvent {
	switch notification.Method {
	case appwire.NotifyThreadStarted:
		var params struct {
			Thread appwire.Thread `json:"thread"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("SESSION_START", map[string]any{
				"session_id": firstNonEmptyString(params.Thread.SessionID, params.Thread.ID),
				"profile":    params.Thread.Serf.Profile,
				"model":      params.Thread.ModelProvider,
			})}
		}
	case appwire.NotifyThreadClosed:
		var params struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(notification.Params, &params)
		return []streamEvent{newStreamEvent("SESSION_END", map[string]any{"reason": params.Reason})}
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("THREAD_STATUS_CHANGED", map[string]any{"status": params.Status.Type})}
		}
	case appwire.NotifyTurnStarted:
		var params struct {
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("TURN_STARTED", map[string]any{"turnId": params.Turn.ID})}
		}
	case appwire.NotifyTurnCompleted:
		var params struct {
			// serf:naming-ignore: AppWire envelope field
			TurnID string       `json:"turnId"`
			Turn   appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			turnID := firstNonEmptyString(params.TurnID, params.Turn.ID)
			events := t.eventsFromTurnCompletedItems(turnID, params.Turn.Items)
			events = append(events, eventsFromFailedTurn(params.Turn)...)
			events = append(events, newStreamEvent("TURN_COMPLETED", map[string]any{"turnId": turnID}))
			return events
		}
	case appwire.NotifyItemStarted:
		var params struct {
			// serf:naming-ignore: AppWire envelope field
			TurnID string             `json:"turnId"`
			Item   appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			if params.Item.TurnID == "" {
				params.Item.TurnID = params.TurnID
			}
			return t.eventsFromItem(params.Item, false)
		}
	case appwire.NotifyItemCompleted:
		var params struct {
			// serf:naming-ignore: AppWire envelope field
			TurnID string             `json:"turnId"`
			Item   appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			if params.Item.TurnID == "" {
				params.Item.TurnID = params.TurnID
			}
			return t.eventsFromItem(params.Item, true)
		}
	case appwire.NotifyAgentMessageDelta:
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("ASSISTANT_TEXT_DELTA", map[string]any{"delta": params.Delta})}
		}
	case appwire.NotifyToolOutputDelta:
		var params appwire.ToolOutputDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("TOOL_CALL_OUTPUT_DELTA", map[string]any{
				"call_id": firstNonEmptyString(params.CallID, params.ItemID),
				"delta":   params.Delta,
			})}
		}
	case appwire.NotifySerfSteeringInjected:
		var params struct {
			Text   string              `json:"text"`
			Images []appwire.InputItem `json:"images"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			payload := map[string]any{"text": params.Text}
			if len(params.Images) > 0 {
				payload["images"] = params.Images
			}
			return []streamEvent{newStreamEvent("STEERING_INJECTED", payload)}
		}
	case appwire.NotifySerfSubagentStarted:
		var params struct {
			Subagent struct {
				AgentID string `json:"agent_id"`
			} `json:"subagent"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.Subagent.AgentID != "" {
			return []streamEvent{newStreamEvent("SUBAGENT_START", map[string]any{"agent_id": params.Subagent.AgentID})}
		}
	case appwire.NotifySerfSubagentEnded:
		var params struct {
			Subagent struct {
				AgentID   string `json:"agent_id"`
				Status    string `json:"status"`
				TurnsUsed int    `json:"turns_used"`
			} `json:"subagent"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.Subagent.AgentID != "" {
			return []streamEvent{newStreamEvent("SUBAGENT_END", map[string]any{
				"agent_id":   params.Subagent.AgentID,
				"status":     params.Subagent.Status,
				"turns_used": params.Subagent.TurnsUsed,
			})}
		}
	}
	return nil
}

func eventsFromFailedTurn(turn appwire.Turn) []streamEvent {
	if turn.Status != appwire.TurnStatusFailed {
		return nil
	}
	message := "turn failed"
	payload := map[string]any{"error": message}
	if turn.Error != nil && turn.Error.Message != "" {
		message = turn.Error.Message
		payload["error"] = message
		if turn.Error.Source != "" {
			payload["source"] = turn.Error.Source
		}
		if turn.Error.Title != "" {
			payload["title"] = turn.Error.Title
		}
		if turn.Error.Hint != "" {
			payload["hint"] = turn.Error.Hint
		}
		if turn.Error.Cause != nil {
			payload["cause"] = turn.Error.Cause
		}
	}
	return []streamEvent{newStreamEvent("ERROR", payload)}
}

func (t *appwireStreamTranslator) eventsFromItems(turnID, turnStatus string, items []appwire.ThreadItem) []streamEvent {
	var events []streamEvent
	turnCompleted := turnStatus == appwire.TurnStatusCompleted
	for _, item := range items {
		if item.TurnID == "" {
			item.TurnID = turnID
		}
		events = append(events, t.eventsFromHydratedItem(item, turnCompleted)...)
	}
	return events
}

func (t *appwireStreamTranslator) eventsFromTurnCompletedItems(turnID string, items []appwire.ThreadItem) []streamEvent {
	var events []streamEvent
	for _, item := range items {
		if item.TurnID == "" {
			item.TurnID = turnID
		}
		if t.itemAlreadyCompleted(item) {
			continue
		}
		events = append(events, t.eventsFromHydratedItem(item, true)...)
	}
	return events
}

func (t *appwireStreamTranslator) eventsFromHydratedItem(item appwire.ThreadItem, turnCompleted bool) []streamEvent {
	if item.Type == "commandExecution" {
		callID := firstNonEmptyString(item.CallID, item.ID)
		if toolItemTerminal(item) || (turnCompleted && !toolItemRunning(item)) {
			if t.activeToolCalls[callID] {
				delete(t.activeToolCalls, callID)
				return t.eventsFromItem(item, true)
			}
			events := t.eventsFromItem(item, false)
			events = append(events, t.eventsFromItem(item, true)...)
			return events
		}
		t.activeToolCalls[callID] = true
		return t.eventsFromItem(item, false)
	}
	return t.eventsFromItem(item, turnCompleted || item.Status == appwire.TurnStatusCompleted)
}

func (t *appwireStreamTranslator) eventsFromItem(item appwire.ThreadItem, completed bool) []streamEvent {
	switch item.Type {
	case "userMessage":
		t.markItemCompleted(item)
		payload := map[string]any{"text": userMessageItemText(item), "turn": item.TranscriptEntryIndex}
		if len(item.Images) > 0 {
			payload["images"] = item.Images
		}
		return []streamEvent{newStreamEvent("USER_INPUT", payload)}
	case "steering":
		t.markItemCompleted(item)
		text := item.Text
		if text == "" && len(item.Images) > 0 {
			text = imageItemsPlaceholder(item.Images)
		}
		payload := map[string]any{"text": text}
		if len(item.Images) > 0 {
			payload["images"] = item.Images
		}
		return []streamEvent{newStreamEvent("STEERING_INJECTED", payload)}
	case "agentMessage":
		if completed {
			t.markItemCompleted(item)
			return []streamEvent{newStreamEvent("ASSISTANT_TEXT_END", map[string]any{"text": item.Text})}
		}
		events := []streamEvent{newStreamEvent("ASSISTANT_TEXT_START", map[string]any{})}
		if item.Text != "" {
			events = append(events, newStreamEvent("ASSISTANT_TEXT_DELTA", map[string]any{"delta": item.Text}))
		}
		return events
	case "commandExecution":
		callID := firstNonEmptyString(item.CallID, item.ID)
		if completed {
			delete(t.activeToolCalls, callID)
			t.markItemCompleted(item)
			return []streamEvent{newStreamEvent("TOOL_CALL_END", map[string]any{
				"call_id":        callID,
				"tool_name":      item.ToolName,
				"arguments_json": item.ArgumentsJSON,
				"output":         item.Output,
				"error":          item.Error,
			})}
		}
		t.activeToolCalls[callID] = true
		events := []streamEvent{newStreamEvent("TOOL_CALL_START", map[string]any{
			"call_id":        callID,
			"tool_name":      item.ToolName,
			"arguments_json": item.ArgumentsJSON,
		})}
		if item.Output != "" {
			events = append(events, newStreamEvent("TOOL_CALL_OUTPUT_DELTA", map[string]any{
				"call_id": callID,
				"delta":   item.Output,
			}))
		}
		return events
	}
	return nil
}

func (t *appwireStreamTranslator) markItemCompleted(item appwire.ThreadItem) {
	for _, key := range itemCompletionKeys(item) {
		t.completedItems[key] = true
	}
}

func (t *appwireStreamTranslator) itemAlreadyCompleted(item appwire.ThreadItem) bool {
	for _, key := range itemCompletionKeys(item) {
		if t.completedItems[key] {
			return true
		}
	}
	return false
}

func itemCompletionKeys(item appwire.ThreadItem) []string {
	keys := make([]string, 0, 2)
	scope := "turn:" + item.TurnID + ":"
	if item.TurnID == "" {
		scope = ""
	}
	if item.ID != "" {
		keys = append(keys, scope+"item:"+item.ID)
	}
	if item.Type == "commandExecution" && item.CallID != "" {
		keys = append(keys, scope+"call:"+item.CallID)
	}
	return keys
}

func toolItemTerminal(item appwire.ThreadItem) bool {
	if appwire.IsTerminalItemStatus(item.Status) {
		return true
	}
	if appwire.IsActiveItemStatus(item.Status) {
		return false
	}
	return item.Output != "" || item.Error != ""
}

func toolItemRunning(item appwire.ThreadItem) bool {
	return appwire.IsActiveItemStatus(item.Status)
}

func newStreamEvent(event string, data any) streamEvent {
	raw, _ := json.Marshal(data)
	return streamEvent{Event: event, Data: string(raw)}
}
