package appwire

import (
	"encoding/json"
	"testing"
)

func TestCodexAppServerCoreFixtureCompatibility(t *testing.T) {
	const threadRead = `{
		"id": 19,
		"result": {
			"thread": {
				"id": "thr_123",
				"sessionId": "thr_123",
				"name": "Bug bash notes",
				"ephemeral": false,
				"status": { "type": "active", "activeFlags": ["waitingOnApproval"] },
				"turns": [{
					"id": "turn_456",
					"status": "inProgress",
					"items": [
						{ "type": "userMessage", "id": "item_user", "turnId": "turn_456", "text": "Summarize this repo.", "status": "completed" },
						{ "type": "agentMessage", "id": "item_agent", "turnId": "turn_456", "text": "Working...", "status": "inProgress" },
						{ "type": "commandExecution", "id": "item_cmd", "turnId": "turn_456", "command": "git status", "cwd": "/work", "status": "inProgress", "aggregatedOutput": "" }
					]
				}]
			}
		}
	}`
	var msg Message
	if err := json.Unmarshal([]byte(threadRead), &msg); err != nil {
		t.Fatalf("unmarshal thread/read response: %v", err)
	}
	var body struct {
		Thread Thread `json:"thread"`
	}
	data, err := json.Marshal(msg.Response.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	if body.Thread.Status.Type != ThreadStatusActive || body.Thread.Status.ActiveFlags[0] != "waitingOnApproval" {
		t.Fatalf("status=%+v", body.Thread.Status)
	}
	if got := body.Thread.Turns[0].Status; got != TurnStatusInProgress {
		t.Fatalf("turn status=%q", got)
	}
	items := body.Thread.Turns[0].Items
	if items[0].Type != "userMessage" || items[1].Type != "agentMessage" || items[2].Type != "commandExecution" {
		t.Fatalf("items=%+v", items)
	}

	const turnStart = `{
		"id": 30,
		"result": {
			"turn": { "id": "turn_456", "status": "inProgress", "items": [], "error": null }
		}
	}`
	if err := json.Unmarshal([]byte(turnStart), &msg); err != nil {
		t.Fatalf("unmarshal turn/start response: %v", err)
	}
	data, err = json.Marshal(msg.Response.Result)
	if err != nil {
		t.Fatalf("marshal turn/start result: %v", err)
	}
	var turnResp TurnStartResponse
	if err := json.Unmarshal(data, &turnResp); err != nil {
		t.Fatalf("decode TurnStartResponse: %v", err)
	}
	if turnResp.Turn.ID != "turn_456" {
		t.Fatalf("TurnStartResponse.Turn.ID=%q, want %q", turnResp.Turn.ID, "turn_456")
	}
	if turnResp.Turn.Status != TurnStatusInProgress {
		t.Fatalf("TurnStartResponse.Turn.Status=%q, want %q", turnResp.Turn.Status, TurnStatusInProgress)
	}

	const itemStarted = `{
		"method": "item/started",
		"params": {
			"threadId": "thr_123",
			"turnId": "turn_456",
			"item": { "type": "dynamicToolCall", "id": "item_tool", "tool": "web_search", "status": "inProgress", "arguments": {"query":"docs"} }
		}
	}`
	if err := json.Unmarshal([]byte(itemStarted), &msg); err != nil {
		t.Fatalf("unmarshal item/started notification: %v", err)
	}
	if msg.Kind() != MessageNotification {
		t.Fatalf("kind=%v, want notification", msg.Kind())
	}
	var params struct {
		ThreadID string     `json:"threadId"`
		TurnID   string     `json:"turnId"`
		Item     ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(msg.Notification.Params, &params); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if params.ThreadID != "thr_123" || params.TurnID != "turn_456" || params.Item.Type != "dynamicToolCall" || params.Item.Status != TurnStatusInProgress {
		t.Fatalf("params=%+v", params)
	}
}
