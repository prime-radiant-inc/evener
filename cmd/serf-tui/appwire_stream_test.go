package main

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
)

func TestStreamEventsFromThreadHydratesCompletedToolWithStartAndEnd(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID: "turn_1",
			Items: []appwire.ThreadItem{{
				Type:          "tool_call",
				ID:            "item_tool",
				CallID:        "call_tool",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"printf ok"}`,
				Output:        "ok",
				Status:        appwire.TurnStatusCompleted,
			}},
		}},
	})

	var sawStart bool
	var sawEnd bool
	for _, ev := range events {
		switch ev.Event {
		case "TOOL_CALL_START":
			sawStart = true
			var data struct {
				CallID        string `json:"call_id"`
				ToolName      string `json:"tool_name"`
				ArgumentsJSON string `json:"arguments_json"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatal(err)
			}
			if data.CallID != "call_tool" || data.ToolName != "shell" || data.ArgumentsJSON == "" {
				t.Fatalf("start data=%+v", data)
			}
		case "TOOL_CALL_END":
			sawEnd = true
			var data struct {
				CallID string `json:"call_id"`
				Output string `json:"output"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatal(err)
			}
			if data.CallID != "call_tool" || data.Output != "ok" {
				t.Fatalf("end data=%+v", data)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("events=%+v, want completed hydrated tool start and end", events)
	}
}

func TestStreamEventsFromThreadHydratesSplitToolAsSingleStartEndPair(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID: "turn_1",
			Items: []appwire.ThreadItem{
				{
					Type:          "tool_call",
					ID:            "item_tool_start",
					CallID:        "call_tool",
					ToolName:      "shell",
					ArgumentsJSON: `{"command":"printf ok"}`,
					Status:        "running",
				},
				{
					Type:     "tool_call",
					ID:       "item_tool_result",
					CallID:   "call_tool",
					ToolName: "shell",
					Output:   "ok",
					Status:   appwire.TurnStatusCompleted,
				},
			},
		}},
	})

	var starts, ends int
	for _, ev := range events {
		switch ev.Event {
		case "TOOL_CALL_START":
			starts++
		case "TOOL_CALL_END":
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("tool starts=%d ends=%d events=%+v", starts, ends, events)
	}
}

func TestStreamEventsFromThreadHydratesFailedToolAsTerminal(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID: "turn_1",
			Items: []appwire.ThreadItem{{
				Type:          "tool_call",
				ID:            "item_tool",
				CallID:        "call_tool",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"false"}`,
				Error:         "exit status 1",
				Status:        appwire.TurnStatusFailed,
			}},
		}},
	})

	var sawEnd bool
	for _, ev := range events {
		if ev.Event != "TOOL_CALL_END" {
			continue
		}
		sawEnd = true
		var data struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
			t.Fatal(err)
		}
		if data.Error != "exit status 1" {
			t.Fatalf("error=%q", data.Error)
		}
	}
	if !sawEnd {
		t.Fatalf("events=%+v, want failed tool end", events)
	}
}

func TestStreamEventsFromTurnCompletedIncludesFinalItems(t *testing.T) {
	notification := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turn": appwire.Turn{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:   "agent_message",
				ID:     "item_agent",
				TurnID: "turn_1",
				Text:   "final answer",
				Status: appwire.TurnStatusCompleted,
			}},
		},
	})

	events := streamEventsFromNotification(*notification.Notification)
	if len(events) < 2 {
		t.Fatalf("events=%+v", events)
	}
	if events[0].Event != "ASSISTANT_TEXT_END" || events[len(events)-1].Event != "TURN_COMPLETED" {
		t.Fatalf("events=%+v, want final item before TURN_COMPLETED", events)
	}
}

func TestStreamEventsFromTurnCompletedSkipsAlreadyCompletedItems(t *testing.T) {
	translator := newAppwireStreamTranslator()
	itemCompleted := appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"item": appwire.ThreadItem{
			Type:   "agent_message",
			ID:     "item_agent",
			TurnID: "turn_1",
			Text:   "final answer",
			Status: appwire.TurnStatusCompleted,
		},
	})
	itemEvents := translator.eventsFromNotification(*itemCompleted.Notification)
	if len(itemEvents) != 1 || itemEvents[0].Event != "ASSISTANT_TEXT_END" {
		t.Fatalf("item events=%+v", itemEvents)
	}

	turnCompleted := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turn": appwire.Turn{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:   "agent_message",
				ID:     "item_agent",
				TurnID: "turn_1",
				Text:   "final answer",
				Status: appwire.TurnStatusCompleted,
			}},
		},
	})
	turnEvents := translator.eventsFromNotification(*turnCompleted.Notification)
	for _, ev := range turnEvents {
		if ev.Event == "ASSISTANT_TEXT_END" {
			t.Fatalf("turn completed replayed already completed item: %+v", turnEvents)
		}
	}
	if len(turnEvents) != 1 || turnEvents[0].Event != "TURN_COMPLETED" {
		t.Fatalf("turn events=%+v, want only TURN_COMPLETED", turnEvents)
	}
}

func TestStreamEventsFromTurnCompletedSkipsAlreadyRenderedUserInput(t *testing.T) {
	translator := newAppwireStreamTranslator()
	itemStarted := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"item": appwire.ThreadItem{
			Type:   "user_message",
			ID:     "item_user",
			TurnID: "turn_1",
			Text:   "hello",
			Status: "running",
		},
	})
	startEvents := translator.eventsFromNotification(*itemStarted.Notification)
	if len(startEvents) != 1 || startEvents[0].Event != "USER_INPUT" {
		t.Fatalf("start events=%+v", startEvents)
	}

	turnCompleted := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turn": appwire.Turn{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:   "user_message",
				ID:     "item_user",
				TurnID: "turn_1",
				Text:   "hello",
				Status: appwire.TurnStatusCompleted,
			}},
		},
	})
	turnEvents := translator.eventsFromNotification(*turnCompleted.Notification)
	for _, ev := range turnEvents {
		if ev.Event == "USER_INPUT" {
			t.Fatalf("turn completed replayed already rendered user input: %+v", turnEvents)
		}
	}
}

func TestStreamEventsDedupKeysAreTurnScoped(t *testing.T) {
	translator := newAppwireStreamTranslator()
	first := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"item": appwire.ThreadItem{
			Type:   "user_message",
			ID:     "reused_item",
			TurnID: "turn_1",
			Text:   "first",
		},
	})
	_ = translator.eventsFromNotification(*first.Notification)

	secondTurn := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turn": appwire.Turn{
			ID:     "turn_2",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:   "user_message",
				ID:     "reused_item",
				TurnID: "turn_2",
				Text:   "second",
				Status: appwire.TurnStatusCompleted,
			}},
		},
	})
	events := translator.eventsFromNotification(*secondTurn.Notification)
	var sawSecond bool
	for _, ev := range events {
		if ev.Event != "USER_INPUT" {
			continue
		}
		var data struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
			t.Fatal(err)
		}
		if data.Text == "second" {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Fatalf("events=%+v, want second turn user input despite reused item id", events)
	}
}

func TestStreamEventsDedupKeysUseEnvelopeTurnID(t *testing.T) {
	translator := newAppwireStreamTranslator()
	first := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turnId":   "turn_1",
		"item": appwire.ThreadItem{
			Type: "user_message",
			ID:   "reused_item",
			Text: "first",
		},
	})
	_ = translator.eventsFromNotification(*first.Notification)

	secondTurn := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turnId":   "turn_2",
		"turn": appwire.Turn{
			ID:     "provider_local_turn_2",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:   "user_message",
				ID:     "reused_item",
				Text:   "second",
				Status: appwire.TurnStatusCompleted,
			}},
		},
	})
	events := translator.eventsFromNotification(*secondTurn.Notification)
	var sawSecond bool
	for _, ev := range events {
		if ev.Event != "USER_INPUT" {
			continue
		}
		var data struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
			t.Fatal(err)
		}
		if data.Text == "second" {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Fatalf("events=%+v, want second turn user input from envelope turn ids", events)
	}
}

func TestStreamEventsFromNotificationMapsSubagentLifecycle(t *testing.T) {
	start := appwire.NotificationMessage(appwire.NotifySerfSubagentStarted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"subagent": agent.SubagentStartData{AgentID: "sub_1", Task: "inspect"},
	})
	startEvents := streamEventsFromNotification(*start.Notification)
	if len(startEvents) != 1 || startEvents[0].Event != "SUBAGENT_START" {
		t.Fatalf("start events=%+v", startEvents)
	}
	var startData struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(startEvents[0].Data), &startData); err != nil {
		t.Fatal(err)
	}
	if startData.AgentID != "sub_1" {
		t.Fatalf("start agent_id=%q", startData.AgentID)
	}

	end := appwire.NotificationMessage(appwire.NotifySerfSubagentEnded, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"subagent": agent.SubagentEndData{AgentID: "sub_1", Status: "completed", TurnsUsed: 3},
	})
	endEvents := streamEventsFromNotification(*end.Notification)
	if len(endEvents) != 1 || endEvents[0].Event != "SUBAGENT_END" {
		t.Fatalf("end events=%+v", endEvents)
	}
	var endData struct {
		AgentID   string `json:"agent_id"`
		Status    string `json:"status"`
		TurnsUsed int    `json:"turns_used"`
	}
	if err := json.Unmarshal([]byte(endEvents[0].Data), &endData); err != nil {
		t.Fatal(err)
	}
	if endData.AgentID != "sub_1" || endData.Status != "completed" || endData.TurnsUsed != 3 {
		t.Fatalf("end data=%+v", endData)
	}
}
