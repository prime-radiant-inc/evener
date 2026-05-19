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
