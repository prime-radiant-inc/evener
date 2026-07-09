package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// M7 in-UI sandbox-exemption escalation for the TUI hub. Escalations are held in a
// FIFO queue PER SESSION REF (escalationsByRef), independent of the viewed session,
// so one for a non-viewed session is enqueued (never dropped) and surfaced on entry.
// The answerable escalation is the head for the currently-viewed session. Consent is
// a DELIBERATE, composer-collision-free chord: ctrl+y allows, ctrl+g denies. Both are
// verified NOT bound by the bubbles v1.0.0 textarea (which binds ctrl+n=LineNext —
// the round-1 collision — but not ctrl+y/ctrl+g) nor by any hub session key, so
// intercepting them steals no composer function and a bare key never answers.

// escalationChordAllow / escalationChordDeny are the verified-free consent chords.
const (
	escalationChordAllow = "ctrl+y"
	escalationChordDeny  = "ctrl+g"
)

type hubEscalation struct {
	id   string
	tool string
	path string
	mode string
	ref  string
}

// applySandboxEscalation enqueues an escalation under its OWN session ref (from the
// notification), regardless of which session is currently viewed, then surfaces the
// prompt only if that session is the one on screen.
func (m *hubModel) applySandboxEscalation(params appwire.SandboxEscalationRequested, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	if m.escalationsByRef == nil {
		m.escalationsByRef = map[string][]*hubEscalation{}
	}
	m.escalationsByRef[ref] = append(m.escalationsByRef[ref], &hubEscalation{
		id:   params.EscalationID,
		tool: params.Tool,
		path: params.DeniedPath,
		mode: params.Mode,
		ref:  ref,
	})
	if ref == strings.TrimSpace(m.detail.Ref) && len(m.escalationsByRef[ref]) == 1 {
		m.promptHeadEscalation()
	} else if ref == strings.TrimSpace(m.detail.Ref) {
		m.addSessionSystem(fmt.Sprintf("Sandbox approval queued (%d now waiting).", len(m.escalationsByRef[ref])))
	}
}

// headEscalation returns the front-of-queue escalation for the CURRENTLY-VIEWED
// session, or nil when none is answerable here.
func (m *hubModel) headEscalation() *hubEscalation {
	q := m.escalationsByRef[strings.TrimSpace(m.detail.Ref)]
	if len(q) == 0 {
		return nil
	}
	return q[0]
}

// surfaceEscalationsOnEntry re-surfaces the viewed session's head escalation prompt
// when the user enters/re-enters that session. It NEVER denies — re-entering must
// not auto-resolve; the escalation simply becomes visible and answerable again.
func (m *hubModel) surfaceEscalationsOnEntry() {
	if m.headEscalation() != nil {
		m.promptHeadEscalation()
	}
}

// promptHeadEscalation surfaces the approval prompt for the viewed session's head
// escalation — explicitly a HARNESS request (not the agent), carrying the FULL path
// for informed consent, and naming the deliberate consent chords.
func (m *hubModel) promptHeadEscalation() {
	e := m.headEscalation()
	if e == nil {
		return
	}
	more := ""
	if n := len(m.escalationsByRef[e.ref]) - 1; n > 0 {
		more = fmt.Sprintf(" (%d more queued)", n)
	}
	m.addSessionSystem(fmt.Sprintf(
		"Sandbox approval (requested by serf, not the agent): %s wants to access %s [--sandbox %s]. Press ctrl+y to Allow once, ctrl+g to Deny.%s",
		e.tool, e.path, e.mode, more))
}

// handleEscalationKey answers the VIEWED session's head escalation with a DELIBERATE
// chord (ctrl+y allow / ctrl+g deny). A bare key never answers, and neither chord is
// bound by the composer textarea, so a multi-line draft is never stolen. Reports
// handled=false when there is nothing answerable or the key is not a consent chord.
func (m *hubModel) handleEscalationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.headEscalation() == nil {
		return nil, false
	}
	switch msg.String() {
	case escalationChordAllow:
		return m.resolveHeadEscalation(true), true
	case escalationChordDeny:
		return m.resolveHeadEscalation(false), true
	}
	return nil, false
}

// resolveHeadEscalation answers the viewed session's head escalation, pops it, echoes
// the decision, and surfaces the next queued one for this session (if any).
func (m *hubModel) resolveHeadEscalation(approve bool) tea.Cmd {
	ref := strings.TrimSpace(m.detail.Ref)
	q := m.escalationsByRef[ref]
	if len(q) == 0 {
		return nil
	}
	e := q[0]
	if len(q) == 1 {
		delete(m.escalationsByRef, ref)
	} else {
		m.escalationsByRef[ref] = q[1:]
	}
	if approve {
		m.addSessionSystem("Allowed once.")
	} else {
		m.addSessionSystem("Denied.")
	}
	var cmd tea.Cmd
	if m.client != nil {
		cmd = sendHubEscalationResolve(m.client, e.ref, e.id, approve)
	}
	m.promptHeadEscalation()
	return cmd
}

func sendHubEscalationResolve(client *appwire.Client, ref, escalationID string, approve bool) tea.Cmd {
	return func() tea.Msg {
		err := client.Request(context.Background(), appwire.MethodSerfSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{
			Ref:          ref,
			EscalationID: escalationID,
			Approve:      approve,
		}, nil)
		return hubActionMsg{action: "sandbox-escalation", err: err}
	}
}
