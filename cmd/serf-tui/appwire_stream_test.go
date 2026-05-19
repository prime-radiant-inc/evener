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

func TestStreamEventsFromThreadTreatsItemsInCompletedTurnAsCompleted(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type: "agent_message",
				ID:   "agent_1",
				Text: "done",
			}},
		}},
	})

	for _, ev := range events {
		if ev.Event == "ASSISTANT_TEXT_END" {
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatal(err)
			}
			if data.Text != "done" {
				t.Fatalf("text=%q, want done", data.Text)
			}
			return
		}
	}
	t.Fatalf("events=%+v, want ASSISTANT_TEXT_END", events)
}

func TestStreamEventsFromThreadTreatsCompletedTurnToolAsTerminal(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{{
				Type:          "tool_call",
				ID:            "item_tool",
				CallID:        "call_tool",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"true"}`,
			}},
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
		t.Fatalf("events=%+v, want one tool start and one tool end", events)
	}
}

func TestStreamEventsFromThreadReplaysRunningAssistantTextAsDelta(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusRunning,
			Items: []appwire.ThreadItem{{
				Type:   "agent_message",
				ID:     "item_agent",
				Text:   "partial answer",
				Status: appwire.TurnStatusRunning,
			}},
		}},
	})

	var sawStart bool
	for _, ev := range events {
		switch ev.Event {
		case "ASSISTANT_TEXT_START":
			sawStart = true
		case "ASSISTANT_TEXT_DELTA":
			var data struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatal(err)
			}
			if !sawStart || data.Delta != "partial answer" {
				t.Fatalf("events=%+v, delta=%+v, want start before partial delta", events, data)
			}
			return
		case "ASSISTANT_TEXT_END":
			t.Fatalf("events=%+v, running assistant should not be terminal", events)
		}
	}
	t.Fatalf("events=%+v, want assistant text delta", events)
}

func TestStreamEventsFromThreadKeepsRunningToolWithOutputActive(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusRunning,
			Items: []appwire.ThreadItem{{
				Type:          "tool_call",
				ID:            "item_tool",
				CallID:        "call_tool",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"printf partial"}`,
				Output:        "partial",
				Status:        appwire.TurnStatusRunning,
			}},
		}},
	})

	var sawStart, sawDelta bool
	for _, ev := range events {
		switch ev.Event {
		case "TOOL_CALL_START":
			sawStart = true
		case "TOOL_CALL_OUTPUT_DELTA":
			sawDelta = true
			var data struct {
				CallID string `json:"call_id"`
				Delta  string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatal(err)
			}
			if !sawStart || data.CallID != "call_tool" || data.Delta != "partial" {
				t.Fatalf("events=%+v, delta=%+v, want start before partial output", events, data)
			}
		case "TOOL_CALL_END":
			t.Fatalf("events=%+v, running tool should not be terminal", events)
		}
	}
	if !sawStart || !sawDelta {
		t.Fatalf("events=%+v, want tool start and output delta", events)
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

func TestStreamEventsFromTurnCompletedFailedPreservesDiagnosticFields(t *testing.T) {
	notification := appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"turn": appwire.Turn{
			ID:     "turn_1",
			Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{
				Message: "configuration failed",
				Source:  "serf",
				Title:   "Serf configuration error",
				Hint:    "Check launch config.",
				Cause:   &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Model: "gpt-5", Status: 503},
			},
		},
	})

	events := streamEventsFromNotification(*notification.Notification)
	var got struct {
		Error  string                   `json:"error"`
		Source string                   `json:"source"`
		Title  string                   `json:"title"`
		Hint   string                   `json:"hint"`
		Cause  *appwire.DiagnosticCause `json:"cause"`
	}
	for _, ev := range events {
		if ev.Event != "ERROR" {
			continue
		}
		if err := json.Unmarshal([]byte(ev.Data), &got); err != nil {
			t.Fatal(err)
		}
	}
	if got.Error != "configuration failed" || got.Source != "serf" || got.Title != "Serf configuration error" || got.Hint != "Check launch config." {
		t.Fatalf("error payload=%+v", got)
	}
	if got.Cause == nil || got.Cause.Kind != "provider" || got.Cause.Provider != "openai" || got.Cause.Model != "gpt-5" || got.Cause.Status != 503 {
		t.Fatalf("cause=%+v", got.Cause)
	}
}

func TestStreamEventsFromThreadFailedTurnPreservesDiagnosticFields(t *testing.T) {
	events := streamEventsFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{
				Message: "configuration failed",
				Source:  "serf",
				Title:   "Serf configuration error",
				Hint:    "Check launch config.",
				Cause:   &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Model: "gpt-5", Status: 503},
			},
			Items: []appwire.ThreadItem{{
				Type:   "agent_message",
				ID:     "item_agent",
				TurnID: "turn_1",
				Text:   "partial answer",
				Status: appwire.TurnStatusCompleted,
			}},
		}},
	})

	var sawFinal bool
	var got struct {
		Error  string                   `json:"error"`
		Source string                   `json:"source"`
		Title  string                   `json:"title"`
		Hint   string                   `json:"hint"`
		Cause  *appwire.DiagnosticCause `json:"cause"`
	}
	for _, ev := range events {
		switch ev.Event {
		case "ASSISTANT_TEXT_END":
			sawFinal = true
		case "ERROR":
			if err := json.Unmarshal([]byte(ev.Data), &got); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !sawFinal {
		t.Fatalf("events=%+v, want final item before failed turn error", events)
	}
	if got.Error != "configuration failed" || got.Source != "serf" || got.Title != "Serf configuration error" || got.Hint != "Check launch config." {
		t.Fatalf("error payload=%+v", got)
	}
	if got.Cause == nil || got.Cause.Kind != "provider" || got.Cause.Provider != "openai" || got.Cause.Model != "gpt-5" || got.Cause.Status != 503 {
		t.Fatalf("cause=%+v", got.Cause)
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

func TestStreamEventsFromImageOnlyUserMessageUsesPlaceholder(t *testing.T) {
	translator := newAppwireStreamTranslator()
	itemStarted := appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
		"threadId": "th_1",
		"ref":      "local:th_1",
		"item": appwire.ThreadItem{
			Type:   "user_message",
			ID:     "item_user",
			TurnID: "turn_1",
			Images: []appwire.InputItem{{
				Type:      "image",
				MediaType: "image/png",
				Data:      []byte("png"),
			}},
			Status: "running",
		},
	})
	events := translator.eventsFromNotification(*itemStarted.Notification)
	if len(events) != 1 || events[0].Event != "USER_INPUT" {
		t.Fatalf("events=%+v, want USER_INPUT", events)
	}
	var payload struct {
		Text   string              `json:"text"`
		Images []appwire.InputItem `json:"images"`
	}
	if err := json.Unmarshal([]byte(events[0].Data), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Text != "[image]" || len(payload.Images) != 1 {
		t.Fatalf("payload=%+v, want image placeholder and image metadata", payload)
	}
}

func TestHydratedSteeringItemCarriesImages(t *testing.T) {
	translator := newAppwireStreamTranslator()
	events := translator.eventsFromHydratedItem(appwire.ThreadItem{
		Type: "steering",
		Images: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Metadata:  map[string]string{"sha": "abc"},
		}},
		Status: appwire.TurnStatusCompleted,
	}, false)
	if len(events) != 1 || events[0].Event != "STEERING_INJECTED" {
		t.Fatalf("events=%+v, want STEERING_INJECTED", events)
	}
	var payload struct {
		Text   string              `json:"text"`
		Images []appwire.InputItem `json:"images"`
	}
	if err := json.Unmarshal([]byte(events[0].Data), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Text != "[image]" || len(payload.Images) != 1 {
		t.Fatalf("payload=%+v, want image placeholder and image metadata", payload)
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
