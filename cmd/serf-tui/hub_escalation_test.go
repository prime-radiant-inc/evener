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
	if len(got.pendingEscalations) != 1 || got.pendingEscalations[0].id != "esc_1" {
		t.Fatalf("escalation not enqueued: %+v", got.pendingEscalations)
	}
	// The prompt must read as a HARNESS message (not the agent), carry the full
	// path, and require a deliberate ctrl+y/ctrl+n chord.
	found := false
	for _, msg := range got.session.messages {
		if strings.Contains(msg.Text, "/etc/hosts") && strings.Contains(msg.Text, "requested by serf") && strings.Contains(msg.Text, "ctrl+y") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no harness-framed chord prompt with the full path: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_BareKeyDoesNotApprove(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.pendingEscalations = []*hubEscalation{{id: "esc_1", ref: "local:th_1"}}

	// A single unmodified 'y' (or 'n') must NOT be intercepted — a filesystem-access
	// consent must never be a one-keystroke accident.
	for _, k := range []string{"y", "Y", "n", "N"} {
		if _, handled := m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}); handled {
			t.Fatalf("bare %q must not answer an escalation", k)
		}
	}
	if len(m.pendingEscalations) != 1 {
		t.Fatal("the escalation must remain pending after bare keys")
	}

	// The deliberate chord DOES answer it.
	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	got := updated.(hubModel)
	if len(got.pendingEscalations) != 0 {
		t.Fatal("ctrl+y must resolve the head escalation")
	}
	if !containsMsg(got, "Allowed once") {
		t.Fatalf("ctrl+y must echo the allow decision: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_ChordDenies(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.pendingEscalations = []*hubEscalation{{id: "esc_1", ref: "local:th_1"}}

	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlN})
	got := updated.(hubModel)
	if len(got.pendingEscalations) != 0 {
		t.Fatal("ctrl+n must resolve the head escalation")
	}
	if !containsMsg(got, "Denied") {
		t.Fatalf("ctrl+n must echo the deny decision: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_ConcurrentQueueBothAnswerable(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc_1", Tool: "read_file", DeniedPath: "/a"}, "local:th_1")
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc_2", Tool: "write_file", DeniedPath: "/b"}, "local:th_1")
	if len(m.pendingEscalations) != 2 {
		t.Fatalf("both concurrent escalations must be queued, got %d", len(m.pendingEscalations))
	}

	// Answer them in order — neither is stranded.
	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	m1 := updated.(hubModel)
	if len(m1.pendingEscalations) != 1 || m1.pendingEscalations[0].id != "esc_2" {
		t.Fatalf("head answered → esc_2 must remain, got %+v", m1.pendingEscalations)
	}
	updated2, _ := m1.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlN})
	m2 := updated2.(hubModel)
	if len(m2.pendingEscalations) != 0 {
		t.Fatalf("both escalations must be answered, got %+v", m2.pendingEscalations)
	}
}

func TestHubModelSandboxEscalation_SessionSwitchClears(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.pendingEscalations = []*hubEscalation{{id: "esc_1", ref: "local:th_1"}}

	// Switching to another session denies+clears the stale escalation (it is no
	// longer on screen, so the chord must not answer it — and it must not strand).
	updated, _ := m.Update(hubSessionMsg{
		detail:   hubSessionDetail{Ref: "local:th_2", SessionID: "sess_2", State: appwire.ThreadStatusIdle},
		messages: nil,
		ref:      "local:th_2",
	})
	got := updated.(hubModel)
	if len(got.pendingEscalations) != 0 {
		t.Fatalf("a session switch must clear pending escalations, got %+v", got.pendingEscalations)
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
