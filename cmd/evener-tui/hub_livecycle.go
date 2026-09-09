package tui

// Live-session cycling for the session view's alt+shift+right /
// alt+shift+left bindings and the /next-live-session /previous-live-session
// palette commands: switch the viewed session to the next/previous LIVE
// session in dashboard order, wrapping at both ends. Same chords and same
// semantics as the web shell's session.liveNext/session.livePrevious
// keybinding actions (frontend src/shell/rail/liveSessionCycle.ts).

import (
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// liveSessionRefsInOrder is the cycling domain: live session rows in
// dashboard display order (buildDashboardRows' attention/recency sort).
func liveSessionRefsInOrder(tree hubTreeResponse) []appwire.Ref {
	var refs []appwire.Ref
	for _, row := range buildDashboardRows(tree) {
		if row.kind == hubRowSession && row.live {
			refs = append(refs, row.ref)
		}
	}
	return refs
}

// adjacentLiveRef steps from current through refs, wrapping at both ends. A
// current ref outside the list (viewing a recent or orphan session) lands
// next on the FIRST live session and previous on the LAST — the web shell's
// adjacentLiveSessionRef rule. Cycling the one live session onto itself is
// motion without movement: not ok.
func adjacentLiveRef(refs []appwire.Ref, current string, step int) (appwire.Ref, bool) {
	if len(refs) == 0 {
		return appwire.Ref{}, false
	}
	for i, ref := range refs {
		if ref.String() == current {
			if len(refs) == 1 {
				return appwire.Ref{}, false
			}
			return refs[(i+step+len(refs))%len(refs)], true
		}
	}
	if step >= 0 {
		return refs[0], true
	}
	return refs[len(refs)-1], true
}

// switchToAdjacentLiveSession switches the session view to the adjacent live
// session through the ordinary entry path (fetchHubSession's thread/read
// cut), so the switch lands with the same transcript hydration and
// subscription a dashboard enter would open.
//
// The read is asynchronous: until its hubSessionMsg lands, detail.Ref still
// names the OLD session. While a cycling read is in flight, further presses
// step from liveNavPendingRef instead, and the read is tagged with a
// per-press sequence so Update drops a response a newer press has superseded
// (roborev PR #1044 finding 2).
func (m hubModel) switchToAdjacentLiveSession(step int) (hubModel, tea.Cmd) {
	if m.client == nil {
		return m, nil
	}
	// The composer is a text surface, and a session switch replaces m.session
	// (input included) and clears pendingAttachments: firing the chord
	// mid-draft would discard unsent content. Hold while a draft - text or
	// attachment-only - is present: the web side's allowInEditable:false
	// policy mapped to the TUI (roborev PR #1044 round-4 medium 3, round-5
	// low).
	if m.session.input.Value() != "" || len(m.pendingAttachments) > 0 {
		m.addSessionSystem("Draft kept. Send or clear it before switching live sessions.")
		return m, nil
	}
	base := m.detail.Ref
	if m.liveNavPendingRef != "" {
		base = m.liveNavPendingRef
	}
	target, ok := adjacentLiveRef(liveSessionRefsInOrder(m.tree), base, step)
	if !ok {
		return m, nil
	}
	m.liveNavSeq++
	m.liveNavPendingRef = target.String()
	seq := m.liveNavSeq
	read := fetchHubSession(m.frames, m.client, target)
	return m, func() tea.Msg {
		msg := read()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.liveNavSeq = seq
			return sessionMsg
		}
		return msg
	}
}

// reestablishDisplayedSubscription re-issues the thread/read for the session
// currently displayed after a dropped or failed cycling read whose request
// already replaced the connection's subscriptions server-side. Replace: true
// culls the child-transcript subscriptions too (subscriptions.go's
// removeConnectionLocked), so the read is tagged with the current live-nav
// sequence and a recovery flag: Update drops its response when a newer
// navigation intent has since applied (it re-enters the SAME session, so an
// untagged response would be processed as an ordinary entry and could revert
// a newer switch), and its success path re-arms the still-running children
// AFTER the replacement completed server-side — tea.Batch would give the
// child subscriptions no ordering guarantee against the replacing read, and
// one landing after it would be culled with watchedChildRefs already marked,
// leaving the child's live activity dead (roborev PR #1044 round-12 mediums 1
// and 2).
func (m hubModel) reestablishDisplayedSubscription() (hubModel, tea.Cmd) {
	ref, ok := m.currentRef()
	if !ok || m.frames == nil {
		return m, nil
	}
	seq := m.liveNavSeq
	m.watchedChildRefs = nil // the recovery response's child re-arm re-fills it
	resub := fetchHubSession(m.frames, m.client, ref)
	return m, func() tea.Msg {
		msg := resub()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.liveNavSeq = seq
			sessionMsg.liveNavRecovery = true
			return sessionMsg
		}
		return msg
	}
}
