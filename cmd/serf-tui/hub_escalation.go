package main

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// hubEscalation is one pending M7 sandbox-exemption approval awaiting the human's
// decision. While any are pending the daemon's tool-exec goroutine(s) are BLOCKED;
// answering sends serf/sandbox/escalation/resolve, which unblocks that call.
type hubEscalation struct {
	id   string
	tool string
	path string
	mode string
	ref  string
}

// applySandboxEscalation ENQUEUES an escalation. Concurrent escalations from one
// session are supported (each is a distinct blocked daemon goroutine), so they are
// kept in a FIFO queue and answered in arrival order — never overwritten, which
// would strand the displaced one's goroutine until interrupt/close. It surfaces a
// HARNESS-framed prompt for the head (or a "queued" note when one is already shown).
func (m *hubModel) applySandboxEscalation(params appwire.SandboxEscalationRequested, ref string) {
	m.pendingEscalations = append(m.pendingEscalations, &hubEscalation{
		id:   params.EscalationID,
		tool: params.Tool,
		path: params.DeniedPath,
		mode: params.Mode,
		ref:  ref,
	})
	if len(m.pendingEscalations) == 1 {
		m.promptHeadEscalation()
	} else {
		m.addSessionSystem(fmt.Sprintf("Sandbox approval queued (%d now waiting).", len(m.pendingEscalations)))
	}
}

// promptHeadEscalation surfaces the approval prompt for the front-of-queue
// escalation, explicitly framed as a HARNESS request (not the agent) so a human is
// never socially-engineered into approving, and carrying the FULL path for informed
// consent. The consent chord is DELIBERATE (ctrl+y / ctrl+n) — never a bare key.
func (m *hubModel) promptHeadEscalation() {
	if len(m.pendingEscalations) == 0 {
		return
	}
	e := m.pendingEscalations[0]
	more := ""
	if n := len(m.pendingEscalations) - 1; n > 0 {
		more = fmt.Sprintf(" (%d more queued)", n)
	}
	m.addSessionSystem(fmt.Sprintf(
		"Sandbox approval (requested by serf, not the agent): %s wants to access %s [--sandbox %s]. Press ctrl+y to Allow once, ctrl+n to Deny.%s",
		e.tool, e.path, e.mode, more))
}

// handleEscalationKey answers the HEAD escalation with a DELIBERATE chord: a single
// unmodified keystroke must NEVER approve out-of-sandbox access (every message
// starts on an empty composer and the card arrives async, so a bare `y` would be a
// silent accident). ctrl+y allows, ctrl+n denies. Anything else is not handled.
func (m *hubModel) handleEscalationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if len(m.pendingEscalations) == 0 {
		return nil, false
	}
	switch msg.String() {
	case "ctrl+y":
		return m.resolveHeadEscalation(true), true
	case "ctrl+n":
		return m.resolveHeadEscalation(false), true
	}
	return nil, false
}

// resolveHeadEscalation answers the front escalation, pops it, echoes the decision,
// and surfaces the next queued one (if any). A deny (or a displacement) always sends
// approve:false — never a silent drop, which would strand the daemon.
func (m *hubModel) resolveHeadEscalation(approve bool) tea.Cmd {
	if len(m.pendingEscalations) == 0 {
		return nil
	}
	e := m.pendingEscalations[0]
	m.pendingEscalations = m.pendingEscalations[1:]
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

// clearSessionEscalations DENIES and clears every pending escalation. It runs when
// the VIEWED session changes: a consent must not remain answerable (via the chord)
// for a session whose card is no longer on screen, and a silent drop would strand
// the daemon — so the safe, documented fallback is to deny them.
func (m *hubModel) clearSessionEscalations() tea.Cmd {
	if len(m.pendingEscalations) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	if m.client != nil {
		for _, e := range m.pendingEscalations {
			cmds = append(cmds, sendHubEscalationResolve(m.client, e.ref, e.id, false))
		}
	}
	m.pendingEscalations = nil
	return tea.Batch(cmds...)
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
