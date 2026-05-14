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

func TestHubModelBrowseSelectedToolRendersFocusedAndToggles(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{
		{Kind: msgAssistant, Text: "before tool"},
		{
			Kind: msgTool,
			Tool: &toolCallInfo{
				Name:        "shell",
				Description: "run test",
				Output:      "ok",
				Done:        true,
				Expanded:    false,
			},
		},
	}
	m.session.scrollMode = true
	m.session.focusedToolIdx = -1
	m.browseSelected = 1

	if got := m.sessionView(); !strings.Contains(got, "▶") {
		t.Fatalf("selected tool group should render focused:\n%s", got)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := updated.(hubModel)
	if !got.session.messages[1].Tool.Expanded {
		t.Fatalf("tab should expand selected tool: %+v", got.session.messages[1].Tool)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(hubModel)
	if got.session.messages[1].Tool.Expanded {
		t.Fatalf("enter should collapse selected tool: %+v", got.session.messages[1].Tool)
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

func TestDetailsDrawerShowsHubDiagnostics(t *testing.T) {
	m := newSessionHubModel(nil)
	m.hubURL = "http://127.0.0.1:9180"
	m.detail = hubSessionDetail{
		Ref:             "local:01SEND",
		SessionID:       "01SEND",
		SourceLabel:     "serf",
		Model:           "gpt-5",
		Profile:         "openai",
		WorkingDir:      "/tmp/project",
		Branch:          "wip/tui",
		TurnCount:       3,
		ContextPressure: 0.37,
		RecentErrors:    []string{"turn_2: provider quota exceeded"},
		Diagnostics: &appwire.SerfDiagnostics{
			Tools: []appwire.SerfToolInfo{
				{Name: "shell", Source: "core"},
				{Name: "linear__search", Source: "mcp:linear"},
			},
			MCP:       []appwire.SerfMCPServerInfo{{Name: "linear", Tools: []string{"search"}}},
			Skills:    []appwire.SerfSkillInfo{{Name: "superpowers:systematic-debugging"}},
			Plugins:   []appwire.SerfPluginInfo{{Name: "superpowers", Version: "4.3.0", SkillCount: 12, AgentCount: 2, HookCount: 4}},
			Hooks:     map[string]int{"PreToolUse": 3},
			Subagents: []appwire.SerfSubagentInfo{{ID: "sub-1", Status: "completed", TurnsUsed: 2}},
			Agents:    []string{"explorer"},
		},
	}

	got := m.renderSessionDetails()
	for _, want := range []string{
		"Hub ref:  local:01SEND",
		"Web:      http://127.0.0.1:9180/s/local:01SEND",
		"Events:   http://127.0.0.1:9180/api/sessions/local:01SEND/events?mode=live",
		"Context:  37% used",
		"Branch:   wip/tui",
		"Tools (2):",
		"MCP [linear]: linear__search",
		"MCP Servers (1):",
		"Skills (1):",
		"Plugins (1):",
		"Hooks (1):",
		"Subagents (1):",
		"Agents (1):",
		"Recent errors:",
		"turn_2: provider quota exceeded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("details drawer missing %q:\n%s", want, got)
		}
	}
}

func TestDetailsDrawerShowsMissingDiagnostics(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail = hubSessionDetail{
		Ref:         "codex:th_1",
		SessionID:   "th_1",
		SourceLabel: "codex",
		Model:       "gpt-5.1",
	}

	got := m.renderSessionDetails()
	for _, want := range []string{"Source:   codex", "Diagnostics: not reported by source"} {
		if !strings.Contains(got, want) {
			t.Fatalf("details drawer missing %q:\n%s", want, got)
		}
	}
}
