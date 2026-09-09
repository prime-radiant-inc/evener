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
