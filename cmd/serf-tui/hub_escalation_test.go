package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

func TestHubModelSandboxEscalation_NotificationSurfacesPrompt(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifySerfSandboxEscalationRequested, map[string]any{
			"ref":          "local:th_1",
			"escalationId": "esc_1",
			"mode":         "read-only",
			"tool":         "read_file",
			"kind":         "file_tool",
			"deniedPath":   "/etc/hosts",
		}).Notification,
	})
	got := updated.(hubModel)
	if got.pendingEscalation == nil || got.pendingEscalation.id != "esc_1" {
		t.Fatalf("escalation not recorded: %+v", got.pendingEscalation)
	}
	// The prompt must read as a HARNESS message (not the agent) and carry the full path.
	found := false
	for _, msg := range got.session.messages {
		if strings.Contains(msg.Text, "/etc/hosts") && strings.Contains(msg.Text, "requested by serf") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no harness-framed approval prompt with the full path: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_KeyResolves(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.pendingEscalation = &hubEscalation{id: "esc_1", tool: "read_file", path: "/etc/hosts", mode: "read-only", ref: "local:th_1"}

	// 'y' on an empty composer allows and clears the prompt.
	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	got := updated.(hubModel)
	if got.pendingEscalation != nil {
		t.Fatal("y must clear the pending escalation")
	}
	if !containsMsg(got, "Allowed once") {
		t.Fatalf("y must echo the allow decision: %+v", got.session.messages)
	}

	// 'n' denies.
	m.pendingEscalation = &hubEscalation{id: "esc_2", ref: "local:th_1"}
	updated2, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got2 := updated2.(hubModel)
	if got2.pendingEscalation != nil {
		t.Fatal("n must clear the pending escalation")
	}
	if !containsMsg(got2, "Denied") {
		t.Fatalf("n must echo the deny decision: %+v", got2.session.messages)
	}
}

func TestHubModelSandboxEscalation_NonEmptyComposerNotIntercepted(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.pendingEscalation = &hubEscalation{id: "esc_1", ref: "local:th_1"}
	m.session.input.SetValue("typing a message")

	// With text in the composer, y/n must NOT be swallowed by the escalation.
	if _, handled := m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}); handled {
		t.Fatal("y must not be intercepted while the composer has text")
	}
	if m.pendingEscalation == nil {
		t.Fatal("the escalation must remain pending when a keystroke is meant for the composer")
	}
}

func containsMsg(m hubModel, sub string) bool {
	for _, msg := range m.session.messages {
		if strings.Contains(msg.Text, sub) {
			return true
		}
	}
	return false
}
