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

	resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{IncludeTurns: true, ItemsView: "full", Subscribe: true})
	if err != nil {
		send(streamErrorMsg{err})
		return
	}
	for _, ev := range streamEventsFromThread(resp.Thread) {
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
			for _, ev := range streamEventsFromNotification(notification) {
				send(streamEventMsg(ev))
			}
		}
	}
}

func streamEventsFromThread(thread appwire.Thread) []streamEvent {
	events := []streamEvent{newStreamEvent("SESSION_START", map[string]any{
		"session_id": firstNonEmptyString(thread.SessionID, thread.ID),
		"profile":    thread.Serf.Profile,
		"model":      thread.ModelProvider,
		"restored":   true,
	})}
	for _, turn := range thread.Turns {
		for _, item := range turn.Items {
			events = append(events, streamEventsFromItem(item, true)...)
		}
	}
	return events
}

func streamEventsFromNotification(notification appwire.Notification) []streamEvent {
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
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("TURN_COMPLETED", map[string]any{"turnId": params.Turn.ID})}
		}
	case appwire.NotifyItemStarted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return streamEventsFromItem(params.Item, false)
		}
	case appwire.NotifyItemCompleted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return streamEventsFromItem(params.Item, true)
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
			Text string `json:"text"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			return []streamEvent{newStreamEvent("STEERING_INJECTED", map[string]any{"text": params.Text})}
		}
	}
	return nil
}

func streamEventsFromItem(item appwire.ThreadItem, completed bool) []streamEvent {
	switch item.Type {
	case "user_message":
		return []streamEvent{newStreamEvent("USER_INPUT", map[string]any{"text": item.Text, "turn": item.TranscriptEntryIndex})}
	case "agent_message":
		if completed {
			return []streamEvent{newStreamEvent("ASSISTANT_TEXT_END", map[string]any{"text": item.Text})}
		}
		return []streamEvent{newStreamEvent("ASSISTANT_TEXT_START", map[string]any{})}
	case "tool_call":
		if completed {
			return []streamEvent{newStreamEvent("TOOL_CALL_END", map[string]any{
				"call_id":        firstNonEmptyString(item.CallID, item.ID),
				"tool_name":      item.ToolName,
				"arguments_json": item.ArgumentsJSON,
				"output":         item.Output,
				"error":          item.Error,
			})}
		}
		return []streamEvent{newStreamEvent("TOOL_CALL_START", map[string]any{
			"call_id":        firstNonEmptyString(item.CallID, item.ID),
			"tool_name":      item.ToolName,
			"arguments_json": item.ArgumentsJSON,
		})}
	}
	return nil
}

func newStreamEvent(event string, data any) streamEvent {
	raw, _ := json.Marshal(data)
	return streamEvent{Event: event, Data: string(raw)}
}
