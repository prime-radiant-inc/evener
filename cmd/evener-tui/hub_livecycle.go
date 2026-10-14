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

// issuePickedSessionRead issues a session read for a navigation pick — the
// dashboard list, the command palette, a fork or spawn landing — as the
// newest navigation intent: the bump supersedes any read still in flight,
// and the tag lets Update judge a late response against the generation it
// was issued under, so a superseded pick's success cannot yank the user
// into abandoned navigation and its rebuilding rejection cannot arm a retry
// for it. The cycling read's issuance below carries the same discipline for
// its own flavor.
func (m *hubModel) issuePickedSessionRead(ref appwire.Ref) tea.Cmd {
	m.liveNavSeq++
	// The pick supersedes any cycle still in flight: its pending target is
	// retired here, at the supersession — waiting for the pick's landing
	// would leave the dead target as the stepping base for the next press
	// and as a nonempty pending ref a stale cycling response defers
	// reconciliation on forever, stranding the displayed session's
	// subscription (round-15 medium 1).
	m.liveNavPendingRef = ""
	seq := m.liveNavSeq
	m.liveNavEntryPendingSeq = seq
	read := fetchHubSession(m.frames, m.client, ref)
	return func() tea.Msg {
		msg := read()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.liveNavSeq = seq
			sessionMsg.liveNavEntry = true
			return sessionMsg
		}
		return msg
	}
}

// discardSupersededSessionRead drops a response whose read already
// re-pointed the connection's subscriptions server-side. When a session is
// displayed and no newer navigation read is pending, its subscription is
// re-established, since nothing else will. While a newer read IS pending —
// a cycling read's target, a picked read whose generation is still the
// current one, or the armed entry-repair retry's own running re-read — the
// newer read owns the server-side subscription while it is in flight:
// reconciling now would race it, this read's replacement landing after the
// newer read's and stealing the subscription back, so the newer response
// would apply with the subscription pointing at the older session
// (round-16 medium 2, round-17 mediums 1 and 3). A picked generation that
// has been superseded does not count: its navigation is gone, and deferring
// on it would strand the obligation with nothing left to settle it. The
// obligation is recorded instead, and the newer navigation's landing
// settles it through the tagged reconcile (both settle points — the entry
// branch and the same-ref apply — already consume it). On a non-session
// surface there is nothing displayed to repair.
func (m hubModel) discardSupersededSessionRead() (hubModel, tea.Cmd) {
	if m.mode != hubModeSession {
		return m, nil
	}
	pendingCycle := m.liveNavPendingRef != ""
	pendingPick := m.liveNavEntryPendingSeq != 0 && m.liveNavEntryPendingSeq == m.liveNavSeq
	if pendingCycle || pendingPick || m.entryRepairReadInFlight {
		m.liveNavNeedsReconcile = true
		return m, nil
	}
	return m.reestablishDisplayedSubscription()
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
	// An active fork draft is the same class of hold even with the prefilled
	// text deleted: the switch replaces m.session and the entry path clears
	// forkDraft, silently discarding the fork target and original text
	// (roborev PR #1044 round-14 low 1).
	if m.forkDraft != nil {
		m.addSessionSystem("Fork draft kept. Enter to fork, or esc to cancel it before switching live sessions.")
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
	// The cycling press is an explicit navigation (#4343's "entering
	// another session"): the pending entry-repair retry retires with it
	// now, not lazily at its next tick — a reconnect between the two must
	// find nothing armed to revive.
	m.entryRepairRetire()
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

// tagLiveNavRefresh guards a session read issued against the ref displayed
// at issue time (roborev PR #1044 round-18 medium): the /details panel, a
// clear's re-read, the resync re-read. A navigation that moves the view
// before the response lands supersedes it, so the tag lets Update drop the
// stale response instead of processing it as an ordinary session entry that
// reverts the display and clobbers the newer navigation state.
//
// replace marks the replacing flavor (/details, the clear re-read), whose
// ThreadRead replaces the connection's server-side subscriptions: it is
// tagged with the live-nav sequence, and a superseded drop must hand the
// displayed session's subscription back (the round-8 medium 2 / round-15
// patterns in Update). The additive flavor (the resync re-read) culls
// nothing, so it carries no sequence — staleness is judged by the
// displayed ref alone, and its response keeps the ordinary same-ref paths
// (no cycling machinery, no child re-arm).
func (m hubModel) tagLiveNavRefresh(read tea.Cmd, ref string, replace bool) tea.Cmd {
	if ref != m.detail.Ref {
		// The read targets a ref other than the one displayed — a switch
		// the caller drives deliberately (a fork's or spawn's entry read).
		// Intentional-switch semantics apply (roborev PR #1044 round-2
		// medium 3): leave it untagged.
		return read
	}
	seq := 0
	if replace {
		seq = m.liveNavSeq
	}
	return func() tea.Msg {
		msg := read()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.liveNavSeq = seq
			sessionMsg.liveNavRefresh = true
			sessionMsg.liveNavRefreshReplace = replace
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
	return m.reestablishDisplayedSubscriptionRead(true)
}

// settleDeferredReconcile consumes the superseded read's recorded
// reconciliation obligation and re-establishes the displayed session's
// subscription now: the flight the obligation rode has ended, and no
// later landing is guaranteed soon — a retryable failure may pace its
// retries for minutes. errShown rides onto the reconciled model, so the
// settle never displaces the status or error the concluding flight
// surfaces.
func (m hubModel) settleDeferredReconcile(errShown error) (hubModel, tea.Cmd) {
	m.liveNavNeedsReconcile = false
	retried, reconcile := m.reestablishDisplayedSubscription()
	retried.err = errShown
	return retried, reconcile
}

// replacedAwayFromDisplayed reports whether this errored read's fetch
// already re-pointed the connection's server-side subscription at the
// failed ref — a replacing read's thread/read leg succeeded before the
// older pages failed — while the model still displays a different
// session: that displayed session is left with neither its main nor its
// child subscription, and no superseded landing ever existed to record
// the reconciliation obligation. A failure whose ref is the displayed
// session's own leaves the replacement pointing where it already pointed.
func (m hubModel) replacedAwayFromDisplayed(msg hubSessionMsg) bool {
	return msg.subscriptionReplaced && m.detail.Ref != "" && m.detail.Ref != msg.ref
}

// replacedDisplayedChildren reports whether this errored read's fetch
// already replaced the connection's server-side subscription AT the
// displayed session — the same replacement that re-points the main
// subscription culls the children — so a same-ref failure is the last
// flight that knows they are gone: watchedChildRefs still marks them
// watched, so no later landing re-arms them either, and the rail's live
// activity would stay silent until the user navigates away and back.
func (m hubModel) replacedDisplayedChildren(msg hubSessionMsg) bool {
	return msg.subscriptionReplaced && m.detail.Ref != "" && m.detail.Ref == msg.ref
}

// concludeRecoveryFlight clears the in-flight recovery fence when this
// landing is the flight's own: a completion tagged with an older flight's
// id — a dead connection's late response after a reconnect — must not
// unfence a newer flight. Every path a recovery response can take calls
// this: the non-session early return and the recovery handler itself.
func (m *hubModel) concludeRecoveryFlight(msg hubSessionMsg) {
	if msg.liveNavRecovery && m.liveNavRecoveryInFlight &&
		msg.liveNavRecoveryFlight == m.liveNavRecoveryFlight {
		m.liveNavRecoveryInFlight = false
	}
}

// resubscribeDisplayedSessionAfterReconnect is the reconnect flavor: the
// replacement connection carries no subscriptions (the dead connection's died
// with it), so the read stays ADDITIVE — a replacing one has nothing to cull,
// and additive keeps the reconnect path's long-standing contract. It still
// carries the recovery tag, so a late response drops when the user
// live-navigates while it is in flight (roborev PR #1044 round-13 medium 1),
// and its response re-arms the children sequenced after the subscribe.
func (m hubModel) resubscribeDisplayedSessionAfterReconnect() (hubModel, tea.Cmd) {
	return m.reestablishDisplayedSubscriptionRead(false)
}

func (m hubModel) reestablishDisplayedSubscriptionRead(replace bool) (hubModel, tea.Cmd) {
	ref, ok := m.currentRef()
	if !ok || m.frames == nil {
		return m, nil
	}
	if m.liveNavRecoveryInFlight {
		current := m.liveNavRecoveryRef == ref.String() && m.liveNavRecoverySeq == m.liveNavSeq
		if current {
			// A recovery read is already in flight for this display at this
			// navigation generation: its landing is the restore this caller
			// is owed, so the demand is recorded for that landing to settle
			// with a follow-up: a superseded read's replacement can re-point
			// the server-side subscription while this flight is still
			// collecting its older pages, and the restore would otherwise
			// be silently lost. A second concurrent read would race its
			// server-side replacement — each fetch re-points the
			// connection's subscriptions and culls the children, so the
			// first's success re-arms children the second's fetch may cull
			// after it, and the second's landing then skips re-subscribing
			// them because watchedChildRefs still marks them watched.
			m.liveNavNeedsReconcile = true
			return m, nil
		}
		// The in-flight flight belongs to an abandoned display or generation:
		// the user navigated while it was still collecting. Waiting for its
		// landing would park the current display's restore behind a read
		// with no deadline, so the current display replaces it — the new
		// flight's id fences the superseded completion out, and its own
		// landing drops as stale anyway.
	}
	m.liveNavRecoveryInFlight = true
	m.liveNavRecoveryFlight++
	flight := m.liveNavRecoveryFlight
	m.liveNavRecoveryRef = ref.String()
	m.liveNavRecoverySeq = m.liveNavSeq
	seq := m.liveNavSeq
	m.watchedChildRefs = nil // the recovery response's child re-arm re-fills it
	m.liveNavRecoveryRetries = 0
	m.liveNavNeedsReconcile = false // this read is the reconcile
	resub := fetchHubSessionRead(m.frames, m.client, ref, "", 0, true, replace)
	return m, func() tea.Msg {
		msg := resub()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.liveNavSeq = seq
			sessionMsg.liveNavRecovery = true
			sessionMsg.liveNavRecoveryFlight = flight
			return sessionMsg
		}
		return msg
	}
}
