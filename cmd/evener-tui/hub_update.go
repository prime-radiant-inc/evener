package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/evener/cmd/evener-tui/internal/pending"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
)

func (m hubModel) updateImpl(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
	case hubEntryRepairTickMsg:
		return m.updateHubEntryRepairTick(msg)
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.session.width = msg.Width
		m.session.height = msg.Height
		m.session.viewport.Width = msg.Width
		m.session.viewport.Height = m.session.vpHeight()
		m.session.refreshViewport()
		m.dashboardFilter.Width = max(1, msg.Width-8)
		return m, nil
	case hubTreeMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.tree = msg.tree
		m.rows = buildDashboardRows(msg.tree)
		if m.mode == hubModeDashboard && !m.dashboardSelectedOnce {
			if m.selected == 0 && len(m.dashboardRows()) > 1 {
				m.selected = 1
			}
			m.dashboardSelectedOnce = true
		}
		m.clampSelection()
		return m, nil
	case hubSessionMsg:
		// The read's response is an exact cut on the wire, and this snapshot
		// replaces the transcript. Release the frames the connection delivered
		// AFTER it so they are folded on top of the snapshot rather than under
		// it: a release only enqueues, so bubbletea cannot hand them back
		// before this Update returns. The frames delivered ahead of the
		// response go under it, here — the snapshot carries their transcript
		// records, but not a further resync, an escalation, or a hub-wide
		// panel refresh (kata 0vk2).
		//
		// Only a read that replaces the transcript takes a capture, so the
		// early returns below are all reached with nothing held.
		msg.capture.Release()
		// A read re-issued by a pending entry-repair retry (#4343) carries
		// the retry's generation. One whose retry the model no longer holds
		// — a fresh pick armed another, or a landing retired this one — is
		// superseded: discard it whole. Its success must not enter the
		// abandoned navigation (the different-ref branch below would apply
		// it as a user-driven switch, clearing the newer surface's pending
		// attachments and composer with it), and its rejection must not
		// rearm it or surface on a session the user moved to. A landing
		// whose armed pair matches but whose generation has moved — a
		// cycling press, any newer navigation that bumped it while this read
		// was in flight — proves THIS retry's intent departed: the read is
		// discarded and the retry retires with it, so its status does not
		// stand on the surface the user moved to. Either way, the read's
		// subscribe+replace still re-pointed the connection's subscription
		// server-side, so when a session is displayed its subscription is
		// re-established — the same obligation the live-nav guards carry.
		if msg.entryRetrySeq > 0 {
			// This landing concludes the armed generation's own flight — an
			// older generation's response (a dead connection's, a
			// superseded round's) leaves the current flight standing
			// (#4343 round-17 medium 3).
			if m.entryRepairSeq == msg.entryRetrySeq {
				m.entryRepairReadInFlight = false
			}
			if m.entryRepairRef.String() != msg.ref || m.entryRepairSeq != msg.entryRetrySeq {
				return m.discardSupersededSessionRead()
			}
			if m.entryRepairSeq != m.liveNavSeq {
				m.entryRepairRetire()
				return m.discardSupersededSessionRead()
			}
			// The landed re-read's retry is current, but a SUCCESS
			// completes the pick automatically — the user has not
			// confirmed the switch since pressing it. The displayed
			// session's unsent work and open overlays outrank the
			// completion: the draft hold the cycling switch applies
			// (switchToAdjacentLiveSession), the focus trap's overlay
			// surfaces, and an open transcript viewer all hold here. Retire
			// the retry, restore the
			// displayed session's subscription (this read re-pointed it
			// at the retry's target), and drop the success — the
			// different-ref entry below would otherwise clear the draft,
			// delete its pending attachment files, and dismiss the panel
			// or picker the user is interacting with. The user re-picks
			// when they are ready. A rejected re-read keeps the retry
			// armed: the ordinary-transient contract below stands.
			if msg.err == nil && m.mode == hubModeSession && m.detail.Ref != msg.ref &&
				(m.session.input.Value() != "" || len(m.pendingAttachments) > 0 || m.forkDraft != nil ||
					m.transcriptView != nil || topmostOverlayName(m) != "") {
				m.entryRepairRetire()
				return m.discardSupersededSessionRead()
			}
			// An overlay open on a non-session surface holds the same way:
			// the switch would leave a dashboard panel or a spawn-surface
			// picker registered with the focus trap but invisible on the
			// session view, silently consuming keys. Retire the retry and
			// drop the success; the user re-picks when ready (#4343
			// round-33 medium 1).
			if msg.err == nil && m.mode != hubModeSession && topmostOverlayName(m) != "" {
				m.entryRepairRetire()
				return m.discardSupersededSessionRead()
			}
			// The spawn form stands through its entry read's retry window,
			// and it is an editable surface: the submitted prompt is a draft
			// the user may keep typing into. A success whose form has been
			// edited since submission holds the same way the displayed
			// session's composer does — entering would replace the form
			// (input included) and silently discard the newer draft. Retire
			// the retry and drop the success; the user enters when ready.
			if msg.err == nil && m.mode == hubModeSpawn &&
				strings.TrimSpace(m.session.input.Value()) != m.spawnSubmittedPrompt {
				m.entryRepairRetire()
				return m.discardSupersededSessionRead()
			}
		}
		// A refresh read (the /details panel, a clear's re-read, the resync
		// re-read — tagLiveNavRefresh) targets the session displayed at
		// issue time and never switches sessions: a current response applies
		// below as an ordinary same-ref resync. Anything that moved the view
		// while it was in flight supersedes it — an untagged stale response
		// would reach the different-session branch below, replace the
		// displayed session with stale data for the abandoned ref, and
		// clobber the newer navigation state (bumping liveNavSeq, clearing
		// the pending target a subsequent press steps from) (roborev PR
		// #1044 round-18 medium).
		if msg.liveNavRefresh {
			if m.mode != hubModeSession {
				// Session mode exited while the read was in flight:
				// applying it would yank the dashboard back into the re-read
				// session (the round-3 medium 2 drop, which the liveNavSeq
				// gate below cannot reach for an unsequenced read). No
				// repair: re-entering a session issues a fresh replacing
				// read.
				return m, nil
			}
			// Stale when the displayed ref has moved (a wrap back onto the
			// same seq leaves the sequence equal, so the ref must be judged
			// directly; the additive flavor carries no sequence at all), or
			// when the replacing flavor's sequence has moved (a press
			// supersedes it even while the display still shows the old
			// session).
			if m.detail.Ref != msg.ref || (msg.liveNavRefreshReplace && msg.liveNavSeq != m.liveNavSeq) {
				if !msg.liveNavRefreshReplace || m.liveNavPendingRef != "" {
					// Superseded. The additive flavor (the resync re-read)
					// culled nothing server-side, and while a newer read is
					// pending it owns the server-side subscription when it
					// lands (round-10 medium): either way, drop without
					// repair.
					return m, nil
				}
				// The superseded replacing flavor's ThreadRead still
				// replaced the connection's subscriptions server-side, and
				// no newer read is in flight: hand the displayed session's
				// back — through the tagged helper, so a yet-newer press
				// cannot be hijacked by the re-read's response (round-13
				// medium 1) — deferring instead when a newer navigation
				// read IS in flight (round-17 medium 2).
				return m.discardSupersededSessionRead()
			}
			// Current: fall through to the ordinary same-ref apply. The
			// cycling guards below do not apply to it (it never switches
			// sessions, so it clears no draft, overlay, or fork target), and
			// the pending target is not its concern: a navigation in flight
			// when it issued keeps its stepping base.
		}
		// A pick's read (the dashboard list, the palette, a fork or spawn
		// landing) carries the generation it was issued under. One that
		// lands after a newer navigation — a second pick, a cycling press, a
		// reconnect — is superseded: its success must not yank the user into
		// abandoned navigation, and its rebuilding rejection must not arm a
		// retry for it (round-12 high). Its ThreadRead already replaced the
		// connection's subscriptions server-side, so a displayed session's
		// subscription is re-established — the same obligation the live-nav
		// guards carry.
		if msg.liveNavEntry && msg.liveNavSeq != m.liveNavSeq {
			return m.discardSupersededSessionRead()
		}
		// A live-session cycling read a newer press has superseded is dropped
		// here, after Release: the superseding read's capture already adopted
		// the frames this one's was holding (hub_frames.go's takeLocked), and
		// Release still enqueues its own after-cut frames into the stream. Its
		// beforeCut fold and transcript replace are the stale halves that must
		// not apply. Refresh reads never enter: a current one falls straight
		// through to the same-ref apply below (it never switches sessions,
		// so the draft/overlay guards and the pending-ref semantics are not
		// its concern — clearing the pending target here would break a
		// concurrent navigation's stepping base), and a stale one was
		// already dropped above.
		//
		// A picked entry read never enters either: its landing guards ran
		// above, and a current one falls through to enter the picked
		// session even from a dashboard — the cycling block's session-mode
		// expectations are not its.
		if msg.liveNavSeq > 0 && !msg.liveNavRefresh && !msg.liveNavEntry {
			// Also dropped when session mode was exited (ctrl+o) while the read
			// was in flight: applying it would yank the dashboard back into
			// the fetched session (roborev PR #1044 round-3 medium 2).
			// The pending target is cleared with it: nothing on a dashboard
			// round-trip clears it, and the next press must step from the
			// session actually displayed, not the abandoned target (roborev
			// PR #1044 round-7 medium 1).
			//
			// A SEQUENCE mismatch in session mode is different: a newer
			// press is in flight and owns the pending target as its stepping
			// base. The stale read drops WITHOUT clearing, so the newer
			// intent keeps its base (roborev PR #1044 round-8 medium 1).
			// Its subscription replacement still took effect server-side,
			// so re-issue the read for the session now displayed to
			// re-establish it (round-8 medium 2). A read from before a
			// reconnect also lands here - but its seq cannot match, because
			// applyHubReconnect bumps liveNavSeq (round-8 medium 4).
			//
			// A recovery response landing on this non-session surface still
			// concludes its flight: a stranded fence would coalesce every
			// later subscription recovery into nothing until a reconnect
			// happened to clear it (#4343 round-29 medium 2).
			if m.mode != hubModeSession {
				m.concludeRecoveryFlight(msg)
				m.liveNavPendingRef = ""
				return m, nil
			}
			// A recovery read (reestablishDisplayedSubscription) re-enters
			// the session already displayed: no hijack to guard, no pending
			// target to step from. A stale one drops; a current one applies
			// below as an ordinary same-ref resync whose success re-arms
			// the children sequenced after the replacement. The draft and
			// overlay guards do not apply: the read was issued BECAUSE a
			// draft or overlay dropped the cycling read, and re-entering
			// those guards would loop recovery forever (roborev PR #1044
			// round-12 medium 2).
			if msg.liveNavRecovery {
				// This landing concludes the in-flight recovery flight, whatever
				// it says — but only this flight's own landing, judged by the
				// flight id: a stale completion cannot unfence a newer flight.
				// Clear before branching, so a stale drop or a failure retry
				// can issue the next flight (its own issuance sets the fence
				// again).
				m.concludeRecoveryFlight(msg)
				if msg.liveNavSeq != m.liveNavSeq || m.detail.Ref != msg.ref {
					// A stale recovery response's ThreadRead still replaced
					// the server-side subscription, and concurrent RPCs have
					// no ordering guarantee: it can have landed AFTER the
					// newer navigation's read, leaving the subscription on
					// the older session. When no newer read is in flight,
					// reconcile by re-establishing the DISPLAYED session's
					// subscription - through the tagged helper, so this
					// reconcile cannot itself hijack (roborev PR #1044
					// round-14 medium 1).
					//
					// While a newer read is still PENDING, the reconcile
					// defers: the newer read owns the subscription while it
					// is in flight, and its own response performs the
					// replacement. But the stale replacement may have landed
					// after the newer read's own request completed, so the
					// settle point must reconcile once the pending state
					// clears (round-15 medium 1). The shared discard helper
					// owns the deferral now: any newer navigation read in
					// flight — cycling, picked, or the entry retry's own —
					// records the obligation instead (round-17 medium 2).
					return m.discardSupersededSessionRead()
				}
				if msg.err != nil {
					// A failed recovery read left the connection's
					// subscriptions culled server-side; retry so a transient
					// failure does not strand them — but bound the retries: a
					// persistent RPC failure must surface as the model's
					// error rather than spin a tight command loop (roborev
					// PR #1044 round-13 medium 2).
					if m.liveNavRecoveryRetries >= hubLiveNavRecoveryMaxRetries {
						m.err = msg.err
						m.liveNavRecoveryRetries = 0
						return m, nil
					}
					m.liveNavRecoveryRetries++
					retried, retry := m.reestablishDisplayedSubscription()
					retried.liveNavRecoveryRetries = m.liveNavRecoveryRetries
					return retried, retry
				}
				// The read's response is an exact cut: fold the frames the
				// connection delivered AHEAD of it (escalations, resync
				// requests, other hub updates) before applying the snapshot,
				// the same order the ordinary entry path uses (roborev PR
				// #1044 round-13 medium 3).
				var recoveryPreCut []tea.Cmd
				for _, notification := range msg.beforeCut {
					recoveryPreCut = append(recoveryPreCut, m.applyHubNotification(notification))
				}
				m.mode = hubModeSession
				m.detail = msg.detail
				m.replaceSessionTranscript(msg.messages)
				m.surfaceEscalationsOnEntry()
				m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
				m.session.refreshViewport()
				m.liveNavRecoveryRetries = 0
				// The children re-arm AFTER the replacement completed
				// server-side (the response just applied); batched with the
				// read they would race it (round-12 medium 1). Evaluate the
				// mutation before the return: subscribeNewChildren fills
				// watchedChildRefs on the model the caller keeps.
				recoveryChildren := m.subscribeNewChildren()
				// A demand that arrived while this flight ran — the fence
				// coalesced it onto this flight — settles now: the flight is
				// done, and a superseded read's replacement may have
				// re-pointed the server-side subscription after this flight's
				// own read leg. The follow-up flight carries the restore.
				var followUp tea.Cmd
				if m.liveNavNeedsReconcile {
					m, followUp = m.reestablishDisplayedSubscription()
				}
				return m, tea.Batch(append(recoveryPreCut, recoveryChildren, followUp)...)
			}
			if msg.liveNavSeq != m.liveNavSeq {
				// A newer cycling read is still in flight: IT owns the
				// server-side subscription when it lands, and a resub here
				// would target the OLDER displayed session - its own
				// ThreadRead re-points the subscription at it, and its
				// untagged response then arrives after the newer read
				// applied and is processed as an ordinary session entry,
				// reverting the UI to the older session (roborev PR #1044
				// round-10 medium). Only the settled-display case (below,
				// once the pending target has been consumed) re-establishes.
				// The settled-display resub goes through the tagged recovery
				// path for the same reason as the newer-read drop above: an
				// untagged resub response can land after a yet-newer press
				// applied and revert the UI (roborev PR #1044 round-13
				// medium 1). The shared discard helper owns the deferral
				// when a newer navigation read is in flight — recording the
				// obligation for its landing to settle (round-17 medium 2).
				return m.discardSupersededSessionRead()
			}
			// Composing began while the read was in flight: the draft is newer
			// intent than the navigation. The press-time guard cannot cover
			// this - the content did not exist yet (round-5 medium 2). A fork
			// draft created mid-flight is the same class of newer intent:
			// applying the read would clear it via the session-entry path,
			// silently discarding the fork target (roborev PR #1044
			// round-15 medium 2).
			if m.session.input.Value() != "" || len(m.pendingAttachments) > 0 || m.forkDraft != nil {
				m.liveNavPendingRef = ""
				// The dropped read's ThreadRead replaced the connection's
				// subscriptions server-side (main AND children), so the
				// displayed session must be re-subscribed or it stops receiving
				// live updates (roborev PR #1044 round-11 medium 2). Safe here:
				// the pending ref was just cleared, so no newer cycling read
				// is in flight to be reverted by it (round-10 medium).
				return m.reestablishDisplayedSubscription()
			}
			// An overlay opened while the read was in flight is the same
			// class of newer intent: applying the read would swap the session
			// under the still-open overlay, leaving it against stale session
			// context (roborev PR #1044 round-6 low). Palette-issued live-nav
			// commands are unaffected: the palette closes itself before the
			// command runs (updateCommandPaletteKey), and the focus trap
			// swallows the chord itself while any overlay is open.
			if topmostOverlayName(m) != "" {
				m.liveNavPendingRef = ""
				// Same as the draft drop above: re-subscribe the displayed
				// session (roborev PR #1044 round-11 medium 2).
				return m.reestablishDisplayedSubscription()
			}
			m.liveNavPendingRef = ""
		}
		var preCut []tea.Cmd
		for _, notification := range msg.beforeCut {
			preCut = append(preCut, m.applyHubNotification(notification))
		}
		if msg.err != nil {
			if msg.expectedState != "" && (m.mode != hubModeSession || msg.ref == "" || m.detail.Ref != msg.ref || msg.expectedRefreshToken != m.statusRefreshToken) {
				return m, nil
			}
			// An errored cycling read reached here with its pending ref
			// already cleared above, but its ThreadRead may still have
			// replaced the connection's subscriptions server-side: re-establish
			// the displayed session's (roborev PR #1044 round-11 medium 2).
			// A refresh read takes the ordinary error path instead — an
			// errored /details or clear re-read must surface as the model's
			// error, not silently trigger a repair (roborev PR #1044
			// round-18 medium). A picked entry read also passes through: a
			// current pick's rebuilding rejection must arm the paced retry
			// below, not stop at a re-establish meant for cycling reads.
			if msg.liveNavSeq > 0 && !msg.liveNavRefresh && !msg.liveNavEntry {
				return m.reestablishDisplayedSubscription()
			}
			m.sessionDetailsRequested = false
			// The pick's response concluded its flight — whether the
			// arming below continues its intent or the error surfaces, the
			// pendingness itself is done.
			if msg.liveNavEntry {
				m.liveNavEntryPendingSeq = 0
			}
			// #4343: a session entry whose read answered one of the hub's
			// typed retryable codes — the rebuilding answer (a registered
			// repair) or the lock-contention answer (ordinary contention,
			// nothing registered) — arms the paced retry instead of
			// stranding, and the error line carries the repair status
			// while the retry stands. A terminal answer — the budget's
			// history-failed, an upgrade-required refusal, a target
			// deletion — ends the paced retry and the error shows. An
			// ordinary transient while the retry stands (mid-repair lock
			// contention with no repair in flight) keeps it armed: the
			// status stands and the transient rides after it.
			// The plain entry path arms — the tagged pick whose guard above
			// proved it current, or the retry's own re-read (whose landing
			// guard did) — and the already-open surfaces above keep their
			// existing re-entry points.
			if msg.expectedState == "" && !msg.liveNavRefresh && (msg.liveNavSeq == 0 || msg.liveNavEntry) {
				ref, refErr := appwire.ParseRef(msg.ref)
				armedFor := refErr == nil && m.entryRepairRef == ref
				switch {
				case refErr == nil && isHubTranscriptEntryRetryable(msg.err):
					if m.entryRepairRef != ref {
						// The failed entry is a navigation event exactly
						// as its success would have been (the plain-entry
						// bump below): the retry now owns the pick's
						// intent, so it takes the generation. This also
						// keeps the armed generation nonzero, without
						// which the tagged re-read's landing guard could
						// not tell its own tagged response from an
						// untagged one.
						m.liveNavSeq++
						m.entryRepairRef = ref
						m.entryRepairDelay = hubEntryRepairBaseDelay
						m.entryRepairSeq = m.liveNavSeq
					}
					m.err = nil
					if m.mode == hubModeSession && (((msg.liveNavEntry || msg.entryRetrySeq > 0) && m.liveNavNeedsReconcile) || m.replacedAwayFromDisplayed(msg) || m.replacedDisplayedChildren(msg)) {
						// The failed flight carried the superseded read's
						// deferred reconciliation, or its own read leg
						// already re-pointed the subscription at this
						// failed ref: settle either alongside the retry
						// this failure schedules, or the displayed
						// session stays unsubscribed through every paced
						// retry the repair runs.
						retried, reconcile := m.settleDeferredReconcile(nil)
						return retried, tea.Batch(reconcile, hubEntryRepairRetryCmd(m.entryRepairDelay, ref, m.entryRepairSeq))
					}
					return m, hubEntryRepairRetryCmd(m.entryRepairDelay, ref, m.entryRepairSeq)
				case armedFor && !isHubTranscriptEntryTerminal(msg.err):
					if m.mode == hubModeSession && (((msg.liveNavEntry || msg.entryRetrySeq > 0) && m.liveNavNeedsReconcile) || m.replacedAwayFromDisplayed(msg) || m.replacedDisplayedChildren(msg)) {
						// Same settle, armed-failure flavor: the standing
						// status rides after the reconcile.
						retried, reconcile := m.settleDeferredReconcile(msg.err)
						return retried, tea.Batch(reconcile, hubEntryRepairRetryCmd(m.entryRepairDelay, ref, m.entryRepairSeq))
					}
					m.err = msg.err
					return m, hubEntryRepairRetryCmd(m.entryRepairDelay, ref, m.entryRepairSeq)
				case armedFor:
					m.entryRepairRetire()
				}
				// A navigation error that concludes the newest tracked
				// flight is the error-side twin of the success landing's
				// settle point: the superseded read that deferred onto this
				// flight left the displayed session's server-side
				// subscription where its ThreadRead pointed it, and no later
				// landing exists to settle the obligation — consume it here
				// and reconcile, with the error still surfacing (roborev PR
				// #1044 round-18 medium 1). The obligation only ever rides
				// a tracked flight (the discard helper defers onto tracked
				// pending reads), so only a tracked conclusion can carry
				// one: a pick's landing (liveNavEntry) or the retry's own
				// re-read (entryRetrySeq). The retryable cases above settle
				// theirs at the failed flight's own end — the armed flight
				// they schedule may not land for minutes — so only the
				// non-retryable conclusions reach this second settle point.
				// A replaced read settles here too, whatever its error
				// class and whichever session it targeted: away from the
				// display, the subscription follows the failed ref; at the
				// display, the replacement still culled the children, and
				// nothing else will restore either.
				if m.mode == hubModeSession && (((msg.liveNavEntry || msg.entryRetrySeq > 0) && m.liveNavNeedsReconcile) || m.replacedAwayFromDisplayed(msg) || m.replacedDisplayedChildren(msg)) {
					retried, reconcile := m.settleDeferredReconcile(msg.err)
					return retried, reconcile
				}
			}
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.spawnSubmitting = false
		if msg.expectedState != "" {
			if m.mode != hubModeSession || msg.ref == "" || m.detail.Ref != msg.ref || msg.expectedRefreshToken != m.statusRefreshToken {
				return m, nil
			}
			if !statusRefreshStatesMatchExpected(m.detail.State, msg.detail.State, msg.expectedState) {
				return m, nil
			}
		}
		if m.mode == hubModeSession && m.detail.Ref == msg.detail.Ref {
			// A pending entry-repair retry's successful re-read (#4343)
			// converges the very session it was armed for: its purpose is
			// done, so the owner retires here with its status — the
			// different-ref branch's retire below never runs for it. The
			// re-read is a replacing read all the same: the children re-arm
			// with it, not skipped as an ordinary same-ref resync.
			if msg.entryRetrySeq > 0 {
				m.entryRepairRetire()
			}
			if msg.liveNavEntry {
				m.liveNavEntryPendingSeq = 0
			}
			m.detail = msg.detail
			if msg.expectedState == "" {
				m.replaceSessionTranscript(msg.messages)
				// A same-session resync wipes the transcript (including the pending
				// sandbox-approval prompt); re-surface it so a still-pending escalation
				// stays visible and answerable after a reload.
				m.surfaceEscalationsOnEntry()
				// Refresh queue preview from the authoritative read response
				// (kata r80p) so reloads resync state.
				m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
			} else {
				// A status refresh's snapshot may predate a queueChanged already
				// folded; the queue revision never moves backwards from it.
				m.applyQueueRefresh(msg.detail.Ref, msg.detail.Queue)
			}
			if m.sessionDetailsRequested {
				panel := hubSessionPanel{Body: m.renderSessionDetails()}
				m.sessionPanel = &panel
			}
			m.sessionDetailsRequested = false
			m.session.refreshViewport()
			// A live-nav read landing on the same session (a rapid wrap,
			// A -> B -> A) replaced the server-side subscriptions, culling
			// the children; watchedChildRefs still marks them, so the set
			// must be cleared before a re-arm can issue. A replacing
			// refresh read (the /details panel, a clear's re-read) issued
			// before any navigation carries seq 0 yet culls the children
			// just the same, so it re-arms identically (roborev PR #1044
			// round-18 medium). An entry-repair retry's re-read carries no
			// liveNavSeq but replaced the subscriptions too (#4343), so it
			// re-arms identically. The deferred reconcile from a stale
			// recovery dropped while this read was pending is also settled
			// here (round-16 medium 1). When the reconcile issues, the
			// recovery path owns the re-arm: a batched subscribeNewChildren
			// would race the replacing read, which can cull the child
			// subscriptions after they were created (round-12 race
			// reintroduced; round-17 medium 1).
			if msg.liveNavSeq > 0 || msg.liveNavRefreshReplace || msg.entryRetrySeq > 0 {
				m.watchedChildRefs = nil
				if m.liveNavNeedsReconcile {
					m.liveNavNeedsReconcile = false
					retried, reconcile := m.reestablishDisplayedSubscription()
					// preCut rides along: the notifications' follow-up reads
					// (thread resyncs, status refreshes) are non-subscribing,
					// so they cannot race the reconcile's replacement
					// (roborev PR #1044 round-17 medium 2).
					return retried, tea.Batch(append(preCut, reconcile)...)
				}
				reenableChildren := m.subscribeNewChildren()
				return m, tea.Batch(append(preCut, reenableChildren)...)
			}
			return m, tea.Batch(preCut...)
		}
		m.clearNoticesByCategory("action-unavailable")
		m.clearPendingAttachments(true)
		// Reaching here with a different detail.Ref is a session switch the
		// user drove by some path. A read that was not itself a live-nav read
		// is newer intent than any cycling read still in flight (roborev
		// PR #1044 round-2 medium 3): it takes the generation it did not
		// take at issue time — the tagged flavors already did — and any
		// pending cycling target it superseded, or resolved, is gone.
		// Status refreshes and same-ref resyncs took the early returns above
		// and never invalidate.
		if msg.liveNavSeq == 0 {
			m.liveNavSeq++
		}
		m.liveNavPendingRef = ""
		// The pick concluded its flight: its pending generation goes with
		// the landing, whatever the flavor's outcome below.
		if msg.liveNavEntry {
			m.liveNavEntryPendingSeq = 0
		}
		// Entering any session is the newer intent: a pending entry-repair
		// retry — whose own successful re-read lands here too — retires
		// with it, and its status with it.
		m.entryRepairRetire()
		m.mode = hubModeSession
		m.detail = msg.detail
		m.session = newModel(nil)
		m.session.width = m.width
		m.session.height = m.height
		m.session.sessionID = msg.detail.SessionID
		m.session.sessionModel = msg.detail.Model
		m.session.sessionProfile = msg.detail.Profile
		m.session.processing = appwire.IsActiveThreadStatus(msg.detail.State)
		m.session.messages = msg.messages
		m.session.viewport.Width = m.width
		m.session.viewport.Height = m.session.vpHeight()
		m.session.refreshViewport()
		m.browseSelected = -1
		m.watchedChildRefs = nil // fresh per session; re-subscribed below
		m.forkDraft = nil
		m.sessionThemePicker = nil
		m.sessionModelPicker = nil
		m.sessionVisionModelPicker = nil
		m.sessionTranscriptPicker = nil
		m.sessionPanel = nil
		m.sessionDetailsRequested = false
		m.transcriptTargets = nil
		m.transcriptView = nil
		// Reset queue scoping when (re)entering a session, then seed from
		// the wire snapshot so the composer chrome reflects authoritative
		// state immediately (kata r80p).
		m.clearSessionQueue()
		m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
		// Re-surface any escalation already queued for the entered session (kept
		// across a glance-away — NEVER auto-denied on switch, which would surprise
		// the user; a never-answered one is denied by the daemon's turn-interrupt/
		// close path instead).
		m.surfaceEscalationsOnEntry()
		// Subscribe to any already-running subagent children so the rail shows
		// their live activity on session entry, not just for new spawns.
		subscribeChildren := m.subscribeNewChildren()
		// The settle point of a live-nav navigation: a stale recovery dropped
		// while this read was pending may have left the server-side
		// subscription on the older session (its replacement can land after
		// the newer read's). Issue the deferred tagged reconcile now that the
		// navigation settled and the pending state cleared (roborev PR
		// #1044 round-15 medium 1). When the reconcile issues, the recovery
		// path owns the re-arm: a batched subscribeNewChildren would race the
		// replacing read, which can cull the child subscriptions after they
		// were created (round-17 medium 1).
		if m.liveNavNeedsReconcile {
			m.liveNavNeedsReconcile = false
			retried, reconcile := m.reestablishDisplayedSubscription()
			return retried, tea.Batch(append(preCut, reconcile)...)
		}
		preCut = append(preCut, subscribeChildren)
		return m, tea.Batch(preCut...)
	case hubNotificationMsg:
		if !msg.ok {
			// Evaluated before the return: the call mutates m, and the model
			// operand's read is not ordered against it.
			lost := m.hubConnectionLost()
			return m, lost
		}
		cmd := m.applyHubNotification(msg.notification)
		return m, tea.Batch(cmd, waitHubNotification(m.frames))
	case modelRetryTickMsg:
		cmd := m.applyModelRetryTick()
		return m, cmd
	case hubReconnectMsg:
		reconnected := m.applyHubReconnect(msg)
		return m, reconnected
	case pendingpkg.PendingRegisteredMsg:
		if msg.Entry.Ref != "" && !m.matchesAsyncSessionRef(msg.Entry.Ref) {
			return m, nil
		}
		switch msg.Entry.Method {
		case appwire.MethodTurnSteer:
			m.session.messages = append(m.session.messages, transcript.ChatMessage{
				Kind:      transcript.MsgSteering,
				Text:      msg.Entry.Text,
				Pending:   true,
				PendingID: msg.Entry.ID,
			})
		case appwire.MethodTurnStart:
			m.session.messages = append(m.session.messages, transcript.ChatMessage{
				Kind:      transcript.MsgUser,
				Text:      msg.Entry.Text,
				Pending:   true,
				PendingID: msg.Entry.ID,
			})
		case appwire.MethodTurnDrainAsSteer:
			m.session.messages = append(m.session.messages, transcript.ChatMessage{
				Kind:      transcript.MsgSteering,
				Text:      fmt.Sprintf("draining %d → steering", len(m.sessionQueue)),
				Pending:   true,
				PendingID: msg.Entry.ID,
			})
		case appwire.MethodTurnQueue:
			// Queue entries surface in the queue-preview chrome, not
			// the transcript pane. No transcript-side placeholder.
		}
		m.session.refreshViewport()
		return m, nil
	case pendingpkg.PendingFailedMsg:
		if msg.Entry.Ref != "" && !m.matchesAsyncSessionRef(msg.Entry.Ref) {
			return m, nil
		}
		m.markPendingFailedByID(msg.Entry.ID, msg.Reason)
		m.session.refreshViewport()
		return m, nil
	case pendingpkg.PendingConfirmedMsg:
		if msg.Entry.Ref != "" && !m.matchesAsyncSessionRef(msg.Entry.Ref) {
			return m, nil
		}
		m.removePendingByID(msg.Entry.ID)
		m.session.refreshViewport()
		return m, nil
	case hubSendMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			// Preserve pendingAttachments on error so the user can retry
			// without re-pasting (kata re91).
			reducer := m.sessionTranscriptReducer()
			reducer.RemoveUserMessageEcho(msg.text)
			m.applySessionTranscriptReducer(reducer)
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Send", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Send failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Send failed: " + msg.err.Error())
		} else {
			m.clearNoticesByCategory("appwire")
			m.clearSessionError()
			m.setActiveTurnID(msg.turnID)
			m.clearSubmittedAttachments(msg.submittedAttachments, true)
		}
		return m, nil
	case hubQueueMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			// Restore the failed payload only if the composer is still at
			// the post-submit blank state; newer user edits win.
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Queue", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Queue failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Queue failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("appwire")
		m.clearSessionError()
		m.clearSubmittedAttachments(msg.submittedAttachments, true)
		// The daemon emits a thread/queueChanged notification with the
		// new state; applyHubNotification picks it up. No local mirror
		// (kata r80p).
		return m, nil
	case hubDrainAsSteerMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil || msg.queued || isQueuedDrainPartial(msg.err) {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			if msg.queued || isQueuedDrainPartial(msg.err) {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
				preview := strings.TrimSpace(msg.text)
				if preview == "" && msg.hadAttachment {
					preview = "[image]"
				}
				if preview != "" && len(m.sessionQueue) <= msg.preQueueDepth {
					// The optimistic row is one more queued message until the
					// wire's queueChanged replaces both: the depth is what drain
					// availability and the next drain's preQueueDepth read.
					m.sessionQueue = append(m.sessionQueue, preview)
					m.detail.Queue.Depth++
					m.queueRevisionAtDrain = m.detail.Queue.Revision
					m.queueRevisionStale = true
					m.session.refreshViewport()
				}
				m.addHubErrorNotice("Force-steer failed after queueing", "appwire", msg.err, "The composer payload was queued already. Retry force-steer without resubmitting the same draft.")
				m.recordSessionError("Force-steer failed after queueing: " + msg.err.Error())
				return m, nil
			}
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Force-steer", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Force-steer failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Force-steer failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("appwire")
		m.clearSessionError()
		m.clearSubmittedAttachments(msg.submittedAttachments, true)
		// The daemon emits thread/queueChanged with depth=0 after a
		// successful drain; applyHubNotification clears sessionQueue when
		// it lands. We don't preemptively wipe here so the preview
		// reflects authoritative state at all times (kata r80p).
		m.addSessionSystem("Force-steer sent.")
		return m, nil
	case hubTasksMsg:
		if msg.err != nil {
			m.addHubErrorNotice("Tasks failed", "appwire", msg.err, "Check the hub connection and retry /tasks.")
			m.recordSessionError("Tasks error: " + msg.err.Error())
		} else {
			m.clearNoticesByCategory("appwire")
			m.clearSessionError()
			m.addSessionSystem(renderTasks(msg.tasks, m.width))
		}
		return m, nil
	case hubStatusMsg:
		if msg.err != nil {
			m.recordSessionError("Status error: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		m.detail = msg.detail
		panel := hubSessionPanel{Body: renderHubSessionStatus(msg.detail, msg.tasks, msg.auth, msg.taskErr, msg.authErr, tuiprim.PopupPaneContentWidth(m.width))}
		m.sessionPanel = &panel
		m.session.refreshViewport()
		return m, nil
	case hubEscalationResolvedMsg:
		m.handleEscalationResolved(msg)
		return m, nil
	case hubActionMsg:
		if msg.err != nil {
			m.addHubErrorNotice("Action failed", "action", msg.err, "Open /help to see source-supported actions.")
			m.recordSessionError(fmt.Sprintf("%s failed: %s", msg.action, msg.err))
			return m, nil
		}
		m.clearNoticesByCategory("action")
		m.clearSessionError()
		switch msg.action {
		case "interrupt":
			m.addSessionSystem("Interrupt sent.")
		case "compact":
			m.addSessionSystem("Context compacted.")
		case "shutdown":
			m.addSessionSystem("Stop requested.")
		case "model":
			m.addSessionSystem("Model updated.")
		case "vision-model":
			m.addSessionSystem("Vision model updated.")
		case "effort":
			m.addSessionSystem("Reasoning effort updated.")
		case "steer":
			m.addSessionSystem("Steering sent.")
		}
		return m, nil
	case hubUpgradeMsg:
		if msg.err != nil {
			if m.mode == hubModeSession {
				m.addHubErrorNotice("Upgrade failed", "upgrade", msg.err, "Check the hub connection and retry /upgrade.")
				m.recordSessionError("Upgrade failed: " + msg.err.Error())
			} else {
				m.err = fmt.Errorf("upgrade failed: %w", msg.err)
			}
			return m, nil
		}
		m.err = nil
		m.clearNoticesByCategory("upgrade")
		m.clearSessionError()
		text := formatHubUpgradeResult(msg.resp)
		if m.mode == hubModeSession {
			m.addSessionSystem(text)
		} else {
			m.addNotice(noticePanel{
				Title:      "Upgrade complete",
				Category:   "upgrade",
				Summary:    strings.SplitN(text, "\n", 2)[0],
				Source:     "hub",
				Reason:     strings.TrimSpace(msg.resp.Archive),
				NextAction: strings.TrimSpace(msg.resp.RestartMessage),
			})
		}
		return m, nil
	case hubClearMsg:
		if msg.err != nil {
			m.recordSessionError("Clear failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Clear returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		// The re-read refreshes the session the clear acted on (the
		// displayed one): a navigation that applies while it is in flight
		// supersedes it (roborev PR #1044 round-18 medium).
		return m, m.tagLiveNavRefresh(fetchHubSession(m.frames, m.client, ref), ref.String(), true)
	case hubGoalMsg:
		if msg.err != nil {
			m.recordSessionError("Goal failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		switch {
		case msg.cleared:
			m.addSessionSystem("Goal cleared.")
		case msg.started:
			m.addSessionSystem("Goal set; pursuing now.")
		default:
			m.addSessionSystem("Goal set; starts after the current turn.")
		}
		return m, nil
	case hubNotesMsg:
		if msg.err != nil {
			m.recordSessionError("Note failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		if msg.cleared {
			m.addSessionSystem("Note cleared.")
		} else {
			m.addSessionSystem("Note saved.")
		}
		return m, nil
	case hubURLRemoveMsg:
		if msg.err != nil {
			m.recordSessionError("URL removal failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		m.addSessionSystem("URL removed.")
		return m, nil
	case hubForkMsg:
		if msg.err != nil {
			if m.forkDraft != nil {
				m.forkDraft.Submitting = false
			}
			if msg.aside {
				m.recordSessionError("Aside failed: " + msg.err.Error())
			} else {
				m.recordSessionError("Fork failed: " + msg.err.Error())
			}
			return m, nil
		}
		m.clearSessionError()
		m.forkDraft = nil
		m.session.resetInput()
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Fork returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		// The fork's landing is the newer navigation intent: retire any
		// pending entry-repair retry so its re-read cannot race it, and
		// issue the read tagged with the generation this entry takes.
		m.entryRepairRetire()
		read := m.issuePickedSessionRead(ref)
		return m, read
	case hubSpawnMsg:
		m.spawnSubmitting = false
		if msg.err != nil {
			m.addNotice(noticePanel{
				Title:      "Start failed",
				Category:   "launch",
				Summary:    "Hub session start failed.",
				Source:     m.sourceLabelForNotice(),
				Reason:     msg.err.Error(),
				NextAction: "Check Hub launch diagnostics, auth status, and launch options.",
			})
			return m, nil
		}
		m.clearNoticesByCategory("launch")
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.err = fmt.Errorf("start returned invalid ref: %s", msg.resp.Ref)
			return m, nil
		}
		m.spawnLaunchOverrides = nil // one-shot overrides are consumed only after success
		read := m.issuePickedSessionRead(ref)
		return m, read
	case hubSessionModelsMsg:
		if msg.err != nil {
			m.removeTrailingSessionSystem("Fetching available models...")
			m.addHubErrorNotice("Provider unavailable", "provider", msg.err, "Check provider auth and model availability.")
			return m, nil
		}
		if len(msg.models) == 0 {
			m.removeTrailingSessionSystem("Fetching available models...")
			m.addSessionSystem("No models available from provider.")
			return m, nil
		}
		picker := tuipick.NewModelPicker(msg.models, m.detail.Model, m.width)
		m.sessionModelPicker = &picker
		m.removeTrailingSessionSystem("Fetching available models...")
		return m, nil
	case hubVisionModelsMsg:
		if msg.err != nil {
			m.removeTrailingSessionSystem("Fetching available models...")
			m.addHubErrorNotice("Provider unavailable", "provider", msg.err, "Check provider auth and model availability.")
			return m, nil
		}
		picker := tuipick.NewModelPicker(msg.models, visionModelPickerActiveID(m.detail.VisionModel), m.width)
		picker.SetTitle("Select vision model")
		m.sessionVisionModelPicker = &picker
		m.removeTrailingSessionSystem("Fetching available models...")
		return m, nil
	case hubTranscriptTargetsMsg:
		if msg.err != nil {
			m.addSessionSystem("Could not fetch session transcripts: " + msg.err.Error())
			return m, nil
		}
		items := hubTranscriptPickerItems(msg.targets)
		if len(items) == 0 {
			m.addSessionSystem("No session transcripts are available yet.")
			return m, nil
		}
		activeRef := m.detail.Ref
		if m.transcriptView != nil {
			activeRef = m.transcriptView.Ref
		}
		picker := tuipick.NewTranscriptPicker(items, activeRef, m.width)
		m.transcriptTargets = append([]appwire.ThreadTranscriptTarget(nil), msg.targets...)
		m.sessionTranscriptPicker = &picker
		return m, nil
	case hubTranscriptMsg:
		if msg.err != nil {
			m.addSessionSystem("Could not read transcript: " + hubErrorReason(msg.err))
			return m, nil
		}
		if m.transcriptView == nil {
			// Selecting another transcript is a departure from a pending
			// entry navigation, the same class as leaving the hub surface: a
			// standing retry retires with its status, and the generation
			// advances so its in-flight or paced re-read lands superseded
			// instead of entering the abandoned navigation or clearing the
			// viewer (#4343 round-30 medium 2). The selection itself already
			// departed (#4343 round-31); this landing covers what armed in
			// between, and clears a cycling target issued in the window so
			// its superseded landing reconciles the displayed session
			// instead of deferring onto the abandoned target. Only the open
			// retires: an in-place refresh of an already-open viewer stays
			// where it is.
			m.entryRepairRetire()
			m.liveNavSeq++
			m.liveNavEntryPendingSeq = 0
			m.liveNavPendingRef = ""
		}
		m.transcriptView = &hubTranscriptViewState{
			Ref:      msg.target.Ref,
			Title:    msg.target.Title,
			Source:   transcriptTargetSourceLabel(msg.target),
			Messages: msg.messages,
		}
		m.session.scrollMode = true
		m.session.focusedToolIdx = -1
		m.browseSelected = -1
		m.session.input.Blur()
		m.session.refreshViewport()
		return m, nil
	case hubSpawnOptionsMsg:
		if msg.err != nil {
			if m.mode == hubModeSpawn {
				m.err = fmt.Errorf("launch options failed: %w", msg.err)
			}
			return m, nil
		}
		m.spawnHarnesses = msg.harnesses
		if len(m.spawnHarnesses) == 0 {
			m.spawnHarnesses = []string{"evener"}
		}
		m.spawnHarnessKinds = msg.harnessKinds
		if m.spawnHarnessKinds == nil {
			m.spawnHarnessKinds = map[string]string{}
		}
		m.spawnEmptyTaskReasons = msg.emptyTaskUnsupportedReasons
		if m.spawnEmptyTaskReasons == nil {
			m.spawnEmptyTaskReasons = map[string]string{}
		}
		m.spawnEmptyTaskNext = msg.emptyTaskUnsupportedNext
		if m.spawnEmptyTaskNext == nil {
			m.spawnEmptyTaskNext = map[string]string{}
		}
		for _, harness := range m.spawnHarnesses {
			if m.spawnHarnessKinds[harness] == "" {
				m.spawnHarnessKinds[harness] = "evener"
			}
		}
		if !stringInSlice(m.spawnHarness, m.spawnHarnesses) {
			m.spawnHarness = m.spawnHarnesses[0]
		}
		m.spawnModels = msg.models
		m.spawnRecentDirs = msg.recentDirs
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
			if msg.modelErr != nil {
				m.err = fmt.Errorf("models failed: %w", msg.modelErr)
			}
			cmd := m.requestSpawnPluginPreview()
			return m, cmd
		}
		return m, nil
	case hubAuthStatusMsg:
		return m.handleAuthStatus(msg)
	case hubAuthLoginStartMsg:
		return m.handleAuthLoginStart(msg)
	case hubAuthLoginCompleteMsg:
		return m.handleAuthLoginComplete(msg)
	case hubAuthLogoutMsg:
		return m.handleAuthLogout(msg)
	case launchconfig.AuthListResultMsg:
		// launchconfig.AuthListResultMsg is no longer used; msgs are dropped.
		return m, nil
	case launchconfig.InstanceListResultMsg:
		return m.handleInstanceList(msg)
	case launchconfig.InstanceMutateResultMsg:
		return m.handleInstanceMutateResult(msg)
	case launchconfig.InstanceSetDefaultMsg:
		return m.handleInstanceSetDefault(msg)
	case launchconfig.InstanceRemoveMsg:
		return m.handleInstanceRemove(msg)
	case launchconfig.InstanceCreateSubmitMsg:
		return m.handleInstanceCreateSubmit(msg)
	case launchconfig.InstanceEditSubmitMsg:
		return m.handleInstanceEditSubmit(msg)
	case launchconfig.CredentialsActionMsg:
		return m.handleCredentialsAction(msg)
	case launchconfig.LaunchOverridesOpenMsg:
		return m.handleLaunchOverridesOpen(msg)
	case launchconfig.LaunchOverridesResultMsg:
		return m.handleLaunchOverridesResult(msg)
	case launchconfig.PluginPreviewRequestMsg:
		if m.client != nil {
			return m, launchconfig.CmdPluginPreview(m.client, msg.Params, msg.Key)
		}
		return m, nil
	case launchconfig.PluginPreviewResultMsg:
		return m.handlePluginPreviewResult(msg)
	case launchconfig.PluginsForLaunchResultMsg:
		return m.handlePluginsForLaunchResult(msg)
	case launchconfig.LaunchSettingsEditRequestMsg:
		return m.handleLaunchSettingsEditRequest(msg)
	case tuipick.TextInputResultMsg:
		return m.handleTextInputResult(msg)
	case launchconfig.AuthApiKeySetResultMsg:
		return m.handleAuthApiKeySetResult(msg)
	case launchconfig.AuthLoginStartResultMsg:
		return m.handleAuthLoginStartResult(msg)
	case launchconfig.AuthTestResultMsg:
		return m.handleAuthTestResult(msg)
	case launchconfig.AuthLoginCompleteResultMsg:
		return m.handleAuthLoginCompleteResult(msg)
	case launchconfig.LaunchSetLayerResultMsg:
		return m.handleLaunchSetLayerResult(msg)
	case launchconfig.LaunchLayerResultMsg, launchconfig.LaunchResolveResultMsg, launchconfig.LaunchTrustResultMsg, launchconfig.LaunchSchemaResultMsg:
		return m.handleLaunchResult(msg)
	case launchconfig.MarketplaceListResultMsg:
		return m.handleMarketplaceListResult(msg)
	case launchconfig.MarketplaceMutateResultMsg:
		return m.handleMarketplaceMutateResult(msg)
	case launchconfig.MarketplaceBrowseResultMsg:
		return m.handleMarketplaceBrowseResult(msg)
	case launchconfig.MarketplaceAddSubmitMsg:
		return m.handleMarketplaceAddSubmit(msg)
	case launchconfig.MarketplaceRemoveMsg:
		return m.handleMarketplaceRemove(msg)
	case launchconfig.MarketplaceRefreshMsg:
		return m.handleMarketplaceRefresh(msg)
	case launchconfig.MarketplaceBrowseRequestMsg:
		return m.handleMarketplaceBrowseRequest(msg)
	case launchconfig.PluginListResultMsg:
		return m.handlePluginListResult(msg)
	case launchconfig.PluginMutateResultMsg:
		return m.handlePluginMutateResult(msg)
	case launchconfig.PluginActionMsg:
		return m.handlePluginAction(msg)
	case launchconfig.PluginSetAutoUpgradeMsg:
		return m.handlePluginSetAutoUpgrade(msg)
	}
	return m, nil
}

// visionModelPickerActiveID canonicalizes only the bare off sentinel for the
// picker display. Other refs stay unchanged so active-model matching does not
// reinterpret provider-qualified refs such as "off/model".
func visionModelPickerActiveID(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if strings.EqualFold(trimmed, "off") {
		return "off"
	}
	return ref
}

func statusRefreshStatesMatchExpected(currentState, payloadState, expectedState string) bool {
	return currentState == expectedState && payloadState == expectedState
}
