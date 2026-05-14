package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelLiveAgentCompletionUpdatesDeltaWithoutDuplicate(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "th_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: "th_1",
			Ref:      "local:th_1",
			TurnID:   "turn_1",
			ItemID:   "agent_1",
			Delta:    "partial **markdown",
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:          "tool_call",
				ID:            "tool_1",
				CallID:        "call_1",
				TurnID:        "turn_1",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"pwd"}`,
				Status:        "running",
			},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:   "agent_message",
				ID:     "agent_1",
				TurnID: "turn_1",
				Text:   "partial **markdown** final",
				Status: "completed",
			},
		}).Notification,
	})

	got := updated.(hubModel)
	assistantCount := 0
	for _, msg := range got.session.messages {
		if msg.Kind == msgAssistant {
			assistantCount++
			if msg.Text != "partial **markdown** final" {
				t.Fatalf("assistant text=%q, want final markdown text", msg.Text)
			}
		}
	}
	if assistantCount != 1 {
		t.Fatalf("assistant messages=%d in %+v", assistantCount, got.session.messages)
	}
}

func TestToolGroupRendersErrorResult(t *testing.T) {
	got := renderToolCall(toolCallInfo{
		Name:        "read_file",
		Description: "read missing file",
		Error:       "open missing.go: no such file or directory",
		Done:        true,
		Expanded:    true,
	}, 100, false)

	for _, want := range []string{"read_file", "error:", "open missing.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool group missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelActionUnavailableNoticeIncludesSourceAndReason(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Ref = "codex-local:th_1"
	m.detail.SourceLabel = "codex-local"
	m.detail.Capabilities.Clear = false
	m.session.setInputValue("/clear")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unavailable clear should not call hub")
	}
	got := updated.(hubModel).View()
	for _, want := range []string{"Action unavailable", "Clear is not available for this session.", "source: codex-local", "reason: source does not advertise clear"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice missing %q:\n%s", want, got)
		}
	}
}

func TestDetailsDrawerShowsCapabilities(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "codex-local"
	m.detail.Capabilities = hubSessionCapabilities{
		Send:    true,
		Steer:   true,
		Compact: true,
	}

	got := m.renderSessionDetails()
	for _, want := range []string{"details", "Source:   codex-local", "Capabilities: send, steer, compact"} {
		if !strings.Contains(got, want) {
			t.Fatalf("details drawer missing %q:\n%s", want, got)
		}
	}
}
