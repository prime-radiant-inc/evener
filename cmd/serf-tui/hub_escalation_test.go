package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

func escalationNotif(ref, id, tool, path string) hubNotificationMsg {
	return hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifySerfSandboxEscalationRequested, map[string]any{
			"ref":          ref,
			"threadId":     strings.TrimPrefix(ref, "local:"),
			"escalationId": id,
			"mode":         "read-only",
			"tool":         tool,
			"kind":         "file_tool",
			"deniedPath":   path,
		}).Notification,
	}
}

func TestHubModelSandboxEscalation_NotificationSurfacesPromptForViewed(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	updated, _ := m.Update(escalationNotif("local:th_1", "esc_1", "read_file", "/etc/hosts"))
	got := updated.(hubModel)
	if len(got.escalationsByRef["local:th_1"]) != 1 {
		t.Fatalf("escalation not enqueued under its ref: %+v", got.escalationsByRef)
	}
	found := false
	for _, msg := range got.session.messages {
		if strings.Contains(msg.Text, "/etc/hosts") && strings.Contains(msg.Text, "requested by serf") &&
			strings.Contains(msg.Text, "ctrl+y") && strings.Contains(msg.Text, "ctrl+g") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no harness-framed chord prompt with the full path: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_BareAndComposerKeysDoNotAnswer(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1"}}}

	// A bare y/n must NOT answer; ctrl+n (the composer's LineNext binding) must NOT
	// answer either — a security consent must be un-accidentable and collision-free.
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("y")},
		{Type: tea.KeyRunes, Runes: []rune("n")},
		{Type: tea.KeyCtrlN},
	} {
		if _, handled := m.handleEscalationKey(k); handled {
			t.Fatalf("key %q must not answer an escalation", k.String())
		}
	}
	if len(m.escalationsByRef["local:th_1"]) != 1 {
		t.Fatal("the escalation must remain pending after bare/composer keys")
	}

	// The deliberate ctrl+y chord SENDS but does not pop optimistically: the
	// escalation stays queued (marked resolving) until the daemon ACKs.
	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	got := updated.(hubModel)
	if len(got.escalationsByRef["local:th_1"]) != 1 || !got.escalationsByRef["local:th_1"][0].resolving {
		t.Fatal("ctrl+y must send non-optimistically: the escalation stays queued+resolving until ACK")
	}
	// The daemon ACK (success) pops it and echoes the decision.
	updated, _ = got.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: true})
	got = updated.(hubModel)
	if len(got.escalationsByRef["local:th_1"]) != 0 {
		t.Fatal("a successful ACK must pop the escalation")
	}
	if !containsMsg(got, "Allowed once") {
		t.Fatalf("a successful allow ACK must echo the decision: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_ChordDeniesOnAck(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1"}}}

	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	got := updated.(hubModel)
	updated, _ = got.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: false})
	got = updated.(hubModel)
	if len(got.escalationsByRef["local:th_1"]) != 0 {
		t.Fatal("a deny ACK must pop the escalation")
	}
	if !containsMsg(got, "Denied") {
		t.Fatalf("a deny ACK must echo the decision: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_TransportFailureReSurfaces(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1", resolving: true}}}

	// A transport/daemon-unavailable ACK must NOT drop the escalation — its
	// tool-exec goroutine is still blocked; keep it queued and retryable.
	updated, _ := m.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: true, err: appwire.WireError{Code: appwire.CodeInternalError}})
	got := updated.(hubModel)
	q := got.escalationsByRef["local:th_1"]
	if len(q) != 1 {
		t.Fatal("a transport failure must NOT drop the still-pending escalation")
	}
	if q[0].resolving {
		t.Fatal("a transport failure must clear resolving so the user can retry")
	}
	if !containsMsg(got, "Couldn't reach the agent") {
		t.Fatalf("a transport failure must prompt a retry: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_ConflictIsTerminal(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1", resolving: true}}}

	// A genuine conflict (already resolved elsewhere / not pending) is terminal.
	updated, _ := m.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: true, err: appwire.Conflict("not pending")})
	got := updated.(hubModel)
	if len(got.escalationsByRef["local:th_1"]) != 0 {
		t.Fatal("a conflict ACK must terminally remove the escalation")
	}
	if !containsMsg(got, "already resolved") {
		t.Fatalf("a conflict ACK must say the approval was already resolved: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_SameSessionResyncReSurfaces(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1", tool: "read_file", path: "/x"}}}

	// A same-session resync replaces the transcript (wiping the prompt); the pending
	// escalation must be re-surfaced so it stays visible.
	updated, _ := m.Update(hubSessionMsg{detail: hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1", State: appwire.ThreadStatusActive}, ref: "local:th_1"})
	got := updated.(hubModel)
	if !containsMsg(got, "/x") || !containsMsg(got, "requested by serf") {
		t.Fatalf("a same-session resync must re-surface the pending escalation prompt: %+v", got.session.messages)
	}
}

func TestHubModelSandboxEscalation_NonViewedEnqueuedAndSurfacedOnEntry(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	// An escalation for a DIFFERENT (non-viewed) session must be enqueued by its
	// own ref, never dropped, and NOT surfaced in the current session.
	updated, _ := m.Update(escalationNotif("local:th_2", "esc_2", "write_file", "/b"))
	m1 := updated.(hubModel)
	if len(m1.escalationsByRef["local:th_2"]) != 1 {
		t.Fatalf("non-viewed escalation must still be enqueued, got %+v", m1.escalationsByRef)
	}
	if containsMsg(m1, "/b") {
		t.Fatal("a non-viewed escalation must not be surfaced in the current session")
	}

	// Entering that session surfaces it and makes it answerable.
	updated2, _ := m1.Update(hubSessionMsg{detail: hubSessionDetail{Ref: "local:th_2", SessionID: "sess_2", State: appwire.ThreadStatusIdle}, ref: "local:th_2"})
	m2 := updated2.(hubModel)
	if !containsMsg(m2, "/b") || !containsMsg(m2, "requested by serf") {
		t.Fatalf("entering the session must surface its queued escalation: %+v", m2.session.messages)
	}
	if m2.headEscalation() == nil {
		t.Fatal("the entered session's escalation must be answerable")
	}
}

func TestHubModelSandboxEscalation_ReEntryDoesNotAutoDeny(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1"}}}

	// Switch away to another session — the escalation for th_1 must NOT be denied
	// or cleared (no auto-deny on a glance-away).
	updated, _ := m.Update(hubSessionMsg{detail: hubSessionDetail{Ref: "local:th_2", SessionID: "sess_2", State: appwire.ThreadStatusIdle}, ref: "local:th_2"})
	m1 := updated.(hubModel)
	if len(m1.escalationsByRef["local:th_1"]) != 1 {
		t.Fatalf("switching away must NOT clear/deny the escalation, got %+v", m1.escalationsByRef)
	}

	// Return to th_1 — still pending and re-surfaced.
	updated2, _ := m1.Update(hubSessionMsg{detail: hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1", State: appwire.ThreadStatusIdle}, ref: "local:th_1"})
	m2 := updated2.(hubModel)
	if m2.headEscalation() == nil {
		t.Fatal("re-entering must find the escalation still pending (not auto-denied)")
	}
}

func TestHubModelSandboxEscalation_ConcurrentQueueBothAnswerable(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc_1", Tool: "read_file", DeniedPath: "/a"}, "local:th_1")
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc_2", Tool: "write_file", DeniedPath: "/b"}, "local:th_1")
	if len(m.escalationsByRef["local:th_1"]) != 2 {
		t.Fatalf("both concurrent escalations must be queued, got %d", len(m.escalationsByRef["local:th_1"]))
	}

	updated, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	m1 := updated.(hubModel)
	updated, _ = m1.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: true})
	m1 = updated.(hubModel)
	if len(m1.escalationsByRef["local:th_1"]) != 1 || m1.escalationsByRef["local:th_1"][0].id != "esc_2" {
		t.Fatalf("head answered → esc_2 must remain, got %+v", m1.escalationsByRef["local:th_1"])
	}
	updated2, _ := m1.updateSessionKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m2 := updated2.(hubModel)
	updated2, _ = m2.Update(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_2", approve: false})
	m2 = updated2.(hubModel)
	if len(m2.escalationsByRef["local:th_1"]) != 0 {
		t.Fatalf("both escalations must be answered, got %+v", m2.escalationsByRef["local:th_1"])
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
