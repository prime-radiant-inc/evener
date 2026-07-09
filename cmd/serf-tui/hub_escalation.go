package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// hubEscalation is a pending M7 sandbox-exemption approval awaiting the human's
// y/n decision. While one is pending the daemon's tool-exec goroutine is BLOCKED;
// answering it sends serf/sandbox/escalation/resolve, which unblocks that call.
type hubEscalation struct {
	id   string
	tool string
	path string
	mode string
	ref  string
}

// applySandboxEscalation records a pending escalation from its notification and
// surfaces a HARNESS-framed approval prompt in the transcript — explicitly "requested
// by serf, not the agent" so a human cannot be socially-engineered by model text
// into approving. The card carries the FULL denied path for informed consent.
func (m *hubModel) applySandboxEscalation(params appwire.SandboxEscalationRequested, ref string) {
	m.pendingEscalation = &hubEscalation{
		id:   params.EscalationID,
		tool: params.Tool,
		path: params.DeniedPath,
		mode: params.Mode,
		ref:  ref,
	}
	m.addSessionSystem(fmt.Sprintf(
		"Sandbox approval (requested by serf, not the agent): %s wants to access %s [--sandbox %s]. Press y to Allow once, n to Deny.",
		params.Tool, params.DeniedPath, params.Mode))
}

// handleEscalationKey answers a pending escalation with y (allow) / n or esc
// (deny). It only intercepts when the composer is empty, so a keystroke meant for
// a message is never swallowed. Reports handled=false when there is nothing to
// answer or the key is not a decision.
func (m *hubModel) handleEscalationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.pendingEscalation == nil {
		return nil, false
	}
	if strings.TrimSpace(m.session.input.Value()) != "" {
		return nil, false
	}
	switch msg.String() {
	case "y", "Y":
		return m.resolvePendingEscalation(true), true
	case "n", "N", "esc":
		return m.resolvePendingEscalation(false), true
	}
	return nil, false
}

// resolvePendingEscalation sends the decision, echoes it, and clears the prompt.
// A deny (or a dismiss) always sends approve:false — never a silent drop, which
// would leave the daemon blocked forever.
func (m *hubModel) resolvePendingEscalation(approve bool) tea.Cmd {
	esc := m.pendingEscalation
	m.pendingEscalation = nil
	if esc == nil {
		return nil
	}
	if approve {
		m.addSessionSystem("Allowed once.")
	} else {
		m.addSessionSystem("Denied.")
	}
	if m.client == nil {
		return nil
	}
	return sendHubEscalationResolve(m.client, esc.ref, esc.id, approve)
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
