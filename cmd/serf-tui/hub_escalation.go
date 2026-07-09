package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

// hubEscalationResolvedMsg is the daemon's ACK for a resolve request. The resolve
// is NON-OPTIMISTIC: the escalation stays queued until this arrives, so a transport
// failure never drops a still-pending escalation.
type hubEscalationResolvedMsg struct {
	ref     string
	id      string
	approve bool
	err     error
}

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
	// resolving marks a resolve in flight: the escalation is kept at the head of
	// its queue until the daemon ACKs, and a second chord is swallowed (no
	// double-submit). Cleared on a transport failure so the user can retry.
	resolving bool
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

// surfaceEscalationsOnEntry merges the entered session's thread/read snapshot of
// pending escalations (so a fresh / other-client-raised / reconnecting escalation is
// picked up) and re-surfaces the head prompt. It NEVER denies — re-entering must not
// auto-resolve; the escalation simply becomes visible and answerable again.
func (m *hubModel) surfaceEscalationsOnEntry() {
	m.mergeSnapshotEscalations(m.detail)
	if m.headEscalation() != nil {
		m.promptHeadEscalation()
	}
}

// mergeSnapshotEscalations folds the thread/read snapshot's pending escalations into
// escalationsByRef, DE-DUPED BY ID against any already tracked live — so a client
// that saw an escalation live and then re-enters (or a resync) never double-surfaces
// it; only escalations it has not seen (fresh entry, reconnect, another client
// raised it) are added.
func (m *hubModel) mergeSnapshotEscalations(detail hubSessionDetail) {
	ref := strings.TrimSpace(detail.Ref)
	if ref == "" || len(detail.PendingEscalations) == 0 {
		return
	}
	if m.escalationsByRef == nil {
		m.escalationsByRef = map[string][]*hubEscalation{}
	}
	seen := map[string]bool{}
	for _, e := range m.escalationsByRef[ref] {
		seen[e.id] = true
	}
	for _, p := range detail.PendingEscalations {
		if p.EscalationID == "" || seen[p.EscalationID] {
			continue
		}
		seen[p.EscalationID] = true
		m.escalationsByRef[ref] = append(m.escalationsByRef[ref], &hubEscalation{
			id: p.EscalationID, tool: p.Tool, path: p.DeniedPath, mode: p.Mode, ref: ref,
		})
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

// resolveHeadEscalation SENDS the decision for the viewed session's head escalation
// but does NOT pop it: the escalation stays queued until the daemon ACKs
// (handleEscalationResolved), so a transport failure never drops a still-pending
// escalation. A second chord while a resolve is in flight is swallowed.
func (m *hubModel) resolveHeadEscalation(approve bool) tea.Cmd {
	e := m.headEscalation()
	if e == nil || e.resolving {
		return nil
	}
	e.resolving = true
	m.addSessionSystem("Resolving sandbox approval…")
	if m.client == nil {
		return nil
	}
	return sendHubEscalationResolve(m.client, e.ref, e.id, approve)
}

// handleEscalationResolved applies the daemon's ACK. On success the escalation is
// popped and the decision echoed; a CONFLICT (already resolved elsewhere / not
// pending) is terminal and pops it; any other error (transport / daemon-unavailable)
// keeps it queued and retryable — never dropped while its tool-exec goroutine blocks.
func (m *hubModel) handleEscalationResolved(msg hubEscalationResolvedMsg) {
	q := m.escalationsByRef[msg.ref]
	idx := -1
	for i, e := range q {
		if e.id == msg.id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	viewing := strings.TrimSpace(m.detail.Ref) == strings.TrimSpace(msg.ref)
	echo := func(s string) {
		if viewing {
			m.addSessionSystem(s)
		}
	}
	var we appwire.WireError
	switch {
	case msg.err == nil:
		m.removeEscalationAt(msg.ref, idx)
		if msg.approve {
			echo("Allowed once.")
		} else {
			echo("Denied.")
		}
	case errors.As(msg.err, &we) && we.Code == appwire.CodeConflict:
		m.removeEscalationAt(msg.ref, idx)
		echo("Sandbox approval already resolved.")
	default:
		q[idx].resolving = false
		echo("Couldn't reach the agent — retry with ctrl+y (Allow) / ctrl+g (Deny).")
		return
	}
	if viewing {
		m.promptHeadEscalation()
	}
}

// removeEscalationAt drops the escalation at idx from ref's queue.
func (m *hubModel) removeEscalationAt(ref string, idx int) {
	q := m.escalationsByRef[ref]
	if idx < 0 || idx >= len(q) {
		return
	}
	q = append(q[:idx], q[idx+1:]...)
	if len(q) == 0 {
		delete(m.escalationsByRef, ref)
	} else {
		m.escalationsByRef[ref] = q
	}
}

func sendHubEscalationResolve(client *appwire.Client, ref, escalationID string, approve bool) tea.Cmd {
	return func() tea.Msg {
		err := client.Request(context.Background(), appwire.MethodSerfSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{
			Ref:          ref,
			EscalationID: escalationID,
			Approve:      approve,
		}, nil)
		return hubEscalationResolvedMsg{ref: ref, id: escalationID, approve: approve, err: err}
	}
}
