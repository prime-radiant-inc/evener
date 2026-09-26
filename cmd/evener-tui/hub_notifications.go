package tui

import (
	"encoding/json"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubdiagnostics"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/evener/cmd/evener-tui/internal/pending"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

func (m *hubModel) applyHubNotification(notification appwire.Notification) tea.Cmd {
	m.clearModelRetryOnProgress(notification)
	// Panel-refresh notifications fire regardless of current mode.
	switch notification.Method {
	case appwire.NotifyEvenerAuthUpdated:
		if m.credentialsPanel != nil && m.client != nil {
			return launchconfig.CmdInstanceList(m.client)
		}
		return nil
	case appwire.NotifyEvenerLaunchUpdated:
		if m.launchSettingsPanel != nil {
			return m.launchSettingsPanel.InitialCmd()
		}
		return nil
	case appwire.NotifyEvenerMarketplaceUpdated, appwire.NotifyEvenerPluginUpdated:
		if m.mode == hubModeSpawn && m.client != nil {
			return m.requestSpawnPluginPreview()
		}
		if m.pluginsPanel != nil && m.client != nil {
			return m.refreshPluginsPanel()
		}
		return nil
	case appwire.NotifyEvenerSandboxEscalationRequested:
		// Handled ABOVE the mode/session filters so an escalation for a NON-viewed
		// session (or one that arrives while on the dashboard) is enqueued by its own
		// ref, never silently dropped; it is surfaced when the user enters that
		// session. The daemon's tool-exec goroutine is blocked until it is answered.
		var params appwire.SandboxEscalationRequested
		if json.Unmarshal(notification.Params, &params) == nil && params.EscalationID != "" {
			m.applySandboxEscalation(params, notificationPendingRef(notification))
		}
		return nil
	case appwire.NotifyThreadNameChanged:
		// Handled ABOVE the mode/session filters so a rename that arrives while
		// the dashboard is showing still refreshes the cached row and tree node
		// (the dashboard has no periodic refresh — only `r` or a reconnect
		// re-fetches). The viewed session's detail follows when the frame is for
		// it; the Update wrapper emits the new terminal title from that. The name
		// is untrusted wire text, sanitized before it is stored or rendered, and
		// an absent, blank, or all-control name is not a name.
		var params appwire.ThreadNameChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			if name := sanitizeDisplayName(params.Name); strings.TrimSpace(name) != "" {
				m.updateDashboardRowTitle(params.Ref, name)
				// notificationMatchesCurrentSession treats an empty ref/threadId
				// as "matches" (correct for frames that carry no routing), so
				// require a real identity here: an unidentified rename must not
				// relabel whichever session happens to be open.
				identified := strings.TrimSpace(params.Ref) != "" || strings.TrimSpace(params.ThreadID) != ""
				if identified && m.notificationMatchesCurrentSession(notification) {
					m.detail.Title = name
				}
			}
		}
		return nil
	}

	if m.mode != hubModeSession {
		return nil
	}
	// A frame for a watched subagent child updates its rail row's live activity
	// and is NOT processed as a session-transcript frame.
	if cmd, handled := m.handleChildActivityFrame(notification); handled {
		return cmd
	}
	if !m.notificationMatchesCurrentSession(notification) {
		return nil
	}
	m.markModelRetryInProgress(notification)
	var cmd tea.Cmd
	switch notification.Method {
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			previous := m.detail.State
			m.detail.State = params.Status.Type
			m.session.processing = params.Status.Type == appwire.ThreadStatusActive
			// The set that goes with the announced status rides inline (kata
			// 06t8; the web reducer and the mobile store apply it the same way):
			// apply it now, so Send, Steer and Stop follow the frame instead of
			// the read below. Absent means "no update", never "nothing offered".
			if params.Capabilities != nil {
				m.detail.Capabilities = hubCapabilitiesFromWire(*params.Capabilities, m.detail.Capabilities.ResumeRequired)
			}
			// The waiting-question flag rides the same frame, for the same
			// reason and under the same rule (#1613): it is otherwise
			// snapshot-only, so without this the badge keeps saying "question
			// waiting" after the answer until the next read. Absent means "no
			// update", never "no question waiting".
			if params.AskPending != nil {
				m.detail.AskPending = *params.AskPending
			}
			// Refresh on any transition so the rest of the detail (and a set an
			// older daemon did not send inline) reflects the source's current
			// view. Without this, the cached idle snapshot keeps Interrupt=false
			// for the entire turn (kata 4yvd).
			if previous != params.Status.Type && m.client != nil {
				if ref, ok := m.currentRef(); ok {
					m.statusRefreshToken++
					cmd = fetchHubSessionExpectingStateToken(m.client, ref, params.Status.Type, m.statusRefreshToken)
				}
			}
			// The running-turn display rule (spec "Turn status"): an open
			// turn shows as running only while this names it, and as
			// interrupted otherwise — the sole source of truth for it under
			// the read model, never inferred from an item's or turn's own
			// recorded status.
			m.setActiveTurnID(params.ActiveTurnID)
		}
	case appwire.NotifyHistoryUpdated:
		var params appwire.HistoryUpdatedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyHistoryUpdated(params)
		}
	case appwire.NotifyOverlayUpserted:
		var params appwire.OverlayUpsertedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyOverlayItem(params.Item)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyOverlayDelta:
		var params appwire.OverlayDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyOverlayDelta(params.Key, params.Field, params.Delta)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyOverlayReset:
		var params appwire.OverlayResetParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyOverlayReset(params.StreamID)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyOverlayEnd:
		var params appwire.OverlayEndParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyOverlayEnd(params.RoundID)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyEvenerThreadResync:
		// The hub is saying the model this session holds belongs to a daemon
		// that has been replaced. A relaunched daemon seeds its live "turn_%d"
		// counter from the transcript, which can sit BELOW the dead
		// generation's high-water mark — a between-turns announcement mints an
		// id that never becomes a transcript entry, and a released turn
		// reservation burns one too — so the replacement's first live turn can
		// carry an id this transcript already has rows under. Re-read the
		// thread rather than folding the new generation's frames into the old
		// one's state (kata xx1p).
		if m.client != nil {
			if ref, ok := m.currentRef(); ok {
				// Additive re-read (no subscription replacement): staleness
				// is judged by the displayed ref alone — a stale response
				// lands on the ordinary same-ref guard and drops there
				// (roborev PR #1044 round-18 medium).
				cmd = m.tagLiveNavRefresh(resyncHubSession(m.frames, m.client, ref), ref.String(), false)
			}
		}
	case appwire.NotifyEvenerThreadModelRetry:
		var params appwire.ThreadModelRetryParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.modelRetry = &params
			m.modelRetryReceivedAt = time.Now()
			// A newly reported retry is a fresh wait, whatever the previous
			// attempt was doing when it failed.
			m.modelRetryInProgress = false
			// Arm the timer half of the in-progress OR: without this, a
			// session that gets no further deltas during the wait never
			// re-renders once DelayMS actually elapses (applyModelRetryTick's
			// own doc comment).
			cmd = scheduleModelRetryTick()
		}
	case appwire.NotifyEvenerJobStarted, appwire.NotifyEvenerJobFinished:
		var params appwire.EvenerJobParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyEvenerJob(params.Job)
			m.applySessionTranscriptReducer(reducer)
			// Subscribe to any newly-running child so its activity pushes live.
			cmd = m.subscribeNewChildren()
		}
	case appwire.NotifyEvenerDelegateUpdated:
		var params appwire.EvenerDelegateParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyEvenerDelegate(params.Delegate)
			m.applySessionTranscriptReducer(reducer)
			// Stable delegates carry their child transcript directly; subscribe
			// without waiting for any activation-job notification.
			cmd = m.subscribeNewChildren()
		}
	case appwire.NotifyThreadModelChanged:
		var params appwire.ThreadModelChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.detail.Model = params.Model
			m.detail.ReasoningEffortLevels = params.ReasoningEffortLevels
			m.detail.SupportsReasoning = params.SupportsReasoning
			m.updateDashboardRowModel(params.Ref, params.Model)
		}
	case appwire.NotifyThreadReasoningEffortChanged:
		var params appwire.ThreadReasoningEffortChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.detail.ReasoningEffort = params.ReasoningEffort
		}
	case appwire.NotifyThreadVisionModelChanged:
		var params appwire.ThreadVisionModelChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.detail.VisionModel = params.VisionModel
		}
	case appwire.NotifyThreadQueueChanged:
		var params appwire.ThreadQueueChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			ref := strings.TrimSpace(params.Ref)
			if ref == "" {
				ref = strings.TrimSpace(m.detail.Ref)
			}
			m.applyQueueState(ref, params.Queue)
		}
	case appwire.NotifyEvenerNotesUpdated:
		var params appwire.NotesUpdatedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.detail.HumanNote = params.HumanNote
			m.detail.AgentNote = params.AgentNote
		}
	case appwire.NotifyEvenerUrlsUpdated:
		var params appwire.UrlsUpdatedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.detail.SessionURLs = params.URLs
		}
	case appwire.NotifyWarning:
		// Cause is decoded as a pointer so its absence (legacy payloads)
		// stays distinguishable from kind=="" (kata 5q3p). When present,
		// classifyWarningCategory uses the typed Cause; otherwise it falls
		// back to the message-substring path so legacy NotifyWarning
		// payloads still classify correctly.
		params, message := appwire.DecodeWarningParams(notification.Params)
		title := params.Title
		source := params.Source
		if strings.TrimSpace(title) == "" && strings.TrimSpace(source) == "" && classifyWarningCategory(message, params.Cause) == "provider" {
			source = "provider"
		}
		line := hubdiagnostics.FormatHubDiagnosticWithCause(title, source, message, "Session warning", params.Cause)
		if hint := strings.TrimSpace(params.Hint); hint != "" {
			line += " (" + hint + ")"
		}
		m.addSessionSystemOnce(line)
	}
	m.session.refreshViewport()
	// After the authoritative reducer update has applied, reconcile
	// any matching pending optimistic placeholder. This is the SINGLE
	// reconciliation site on the TUI side per the spec.
	if m.pending != nil {
		reconcilePendingFromNotification(m.pending, notification)
	}
	return cmd
}

// refreshPluginsPanel re-fetches the plugins panel's marketplace and plugin
// lists — and, if a catalog is open, that marketplace's browse results — after
// a evener/marketplace/updated or evener/plugin/updated notification. Either list
// can affect the other's rendering (Browse's install badge only reflects
// reality when the Installed list is current, and an auto-upgrade daemon pass
// or another client's mutation can change either at any time).
func (m *hubModel) refreshPluginsPanel() tea.Cmd {
	cmds := []tea.Cmd{m.marketplaceListRead(), launchconfig.CmdPluginList(m.client)}
	if name := m.pluginsPanel.BrowseMarketplace(); name != "" {
		cmds = append(cmds, launchconfig.CmdMarketplaceBrowse(m.client, name))
	}
	return tea.Batch(cmds...)
}

// reconcilePendingFromNotification translates an inbound daemon
// notification into the wire-method name(s) the pending coordinator
// registered under, then reconciles. Some notifications match multiple
// methods (evener/steering/injected reconciles both turn/steer AND any
// in-flight turn/drainAsSteer).
//
// A steering or history userMessage item that carries the server's
// authoritative ClientMutationID reconciles by that identity
// (PendingCoordinator.Reconcile's preferred path) rather than by matching
// text: the daemon can substitute an image placeholder or, for a drain,
// join several queued texts into one, so a client-side text match is only
// ever a fallback for an older daemon that sends no mutation id.
//
// Drain-special: turn/drainAsSteer matches first-come-first-served
// regardless of text, because the daemon collapses queued entries
// into one STEERING and the placeholder doesn't know the joined text.
func reconcilePendingFromNotification(pending *pendingpkg.PendingCoordinator, n appwire.Notification) {
	ref := notificationPendingRef(n)
	switch n.Method {
	case appwire.NotifyHistoryUpdated:
		// history/updated carries the recorded form of every affected item,
		// including a steering echo (both a human's Ctrl+S and a
		// daemon-injected job/delegate result — evener/steering/injected's
		// replacement) or the turn-opening user message.
		var p appwire.HistoryUpdatedParams
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return
		}
		for _, item := range p.Items {
			if item.Type == "steering" {
				reconcileSteeringItem(pending, item, ref)
				continue
			}
			reconcileUserMessageItem(pending, item, ref)
		}
	}
}

// reconcileUserMessageItem reconciles a pending turn/start entry against one
// thread item, if it is a non-empty userMessage.
func reconcileUserMessageItem(pending *pendingpkg.PendingCoordinator, item appwire.ThreadItem, ref string) {
	if item.Type != "userMessage" || (item.Text == "" && len(item.Images) == 0) {
		return
	}
	text := item.Text
	if text == "" {
		text = transcript.ImageItemsPlaceholder(item.Images)
	}
	pending.Reconcile(appwire.MethodTurnStart, item.ClientMutationID, text, ref)
}

// reconcileSteeringItem reconciles a pending turn/steer entry (and, first-come-
// first-served regardless of text, any in-flight turn/drainAsSteer — the
// daemon collapses queued entries into one steering write and the placeholder
// doesn't know the joined text) against one recorded "steering" item.
func reconcileSteeringItem(pending *pendingpkg.PendingCoordinator, item appwire.ThreadItem, ref string) {
	if strings.TrimSpace(item.Text) == "" {
		return
	}
	pending.Reconcile(appwire.MethodTurnSteer, item.ClientMutationID, item.Text, ref)
	pending.TryReconcile(appwire.MethodTurnDrainAsSteer, "", ref)
}

func notificationPendingRef(n appwire.Notification) string {
	var p appwire.NotificationRef
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return ""
	}
	if strings.TrimSpace(p.Ref) != "" {
		return strings.TrimSpace(p.Ref)
	}
	return strings.TrimSpace(p.ThreadID)
}

// markPendingFailedByID flips the transcript.ChatMessage with the given PendingID
// from Pending → Failed and stamps the reason. ID-keyed so simultaneous
// placeholders of the same kind (e.g. a steer and a drain both rendered
// as transcript.MsgSteering) can't cross-fail each other.
func (m *hubModel) markPendingFailedByID(id int64, reason string) {
	for i := range m.session.messages {
		if m.session.messages[i].PendingID != id {
			continue
		}
		m.session.messages[i].Pending = false
		m.session.messages[i].Failed = true
		m.session.messages[i].Reason = reason
		return
	}
}

// removePendingByID drops the transcript.ChatMessage with the given PendingID
// after the authoritative event has rendered separately.
func (m *hubModel) removePendingByID(id int64) {
	for i := range m.session.messages {
		if m.session.messages[i].PendingID != id {
			continue
		}
		m.session.messages = append(m.session.messages[:i], m.session.messages[i+1:]...)
		return
	}
}

func (m *hubModel) setActiveTurnID(turnID string) {
	m.detail.ActiveTurnID = turnID
}

// applyQueueState replaces the local preview with the authoritative
// wire-sourced snapshot (kata r80p). Called from thread/queueChanged
// notifications and from the ReadThread responses that arrive in stream
// order (a session entry, a transcript-replacing read whose capture folded
// the frames delivered ahead of it), so the state is at least as new as the
// one held. Scoped to the current session ref so a notification routed to a
// different session can't leak into this view.
func (m *hubModel) applyQueueState(ref string, queue appwire.QueueState) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	m.sessionQueueRef = ref
	// The wire's queue state is stored whole, revision included: a drain swaps
	// against the revision the client last saw, and a queue another client
	// edited since hydrate would otherwise be refused as a conflict.
	m.detail.Queue = queue
	m.sessionQueueLatest = queue
	// The stale gate set by a partial drain lifts only for a revision newer
	// than the one that drain was sent against: a thread/read snapshot cut
	// before the daemon moved the revision would otherwise re-enable Ctrl+S
	// with the same stale revision.
	if m.queueRevisionStale && queue.Revision > m.queueRevisionAtDrain {
		m.queueRevisionStale = false
	}
	if queue.Depth == 0 && len(queue.Preview) == 0 {
		m.sessionQueue = nil
		return
	}
	m.sessionQueue = append([]string(nil), queue.Preview...)
}

// updateDashboardRowModel keeps the dashboard's session-row Model column
// live: thread/model/changed fires while the daemon still holds the row list
// (m.rows), which is otherwise only rebuilt from a fresh tree fetch.
//
// Deliberately lazy: this only fires for the currently-viewed session, because
// applyHubNotification gates every row-mutating notification behind
// notificationMatchesCurrentSession. A model switch on a background session is
// not reflected in its dashboard row until the next tree fetch rebuilds m.rows.
// This matches how all other per-row state (queue, activity) updates — we do
// not fan notifications out to non-viewed rows.
func (m *hubModel) updateDashboardRowModel(ref, model string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || model == "" {
		return
	}
	for i := range m.rows {
		if m.rows[i].ref.String() == ref {
			m.rows[i].model = model
			return
		}
	}
}

// updateDashboardRowTitle keeps the dashboard's session-row title live after an
// evener/thread/name/changed push, the same way updateDashboardRowModel keeps
// the Model column live. The rows (and the cached tree they were built from)
// are otherwise rebuilt only by a fresh tree fetch, so without this the
// dashboard would show the old name until the next refresh.
func (m *hubModel) updateDashboardRowTitle(ref, title string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || title == "" {
		return
	}
	for i := range m.rows {
		if m.rows[i].ref.String() == ref {
			m.rows[i].title = title
		}
	}
	updateTreeNodeTitles(m.tree.Live, ref, title)
	for i := range m.tree.Projects {
		updateTreeNodeTitles(m.tree.Projects[i].Sessions, ref, title)
	}
}

func updateTreeNodeTitles(nodes []hubTreeNode, ref, title string) {
	for i := range nodes {
		if nodes[i].Ref == ref {
			nodes[i].Title = title
		}
		updateTreeNodeTitles(nodes[i].Children, ref, title)
	}
}

// applyQueueRefresh folds a status-refresh read's queue state. That read takes
// no hold on the feed, so its snapshot can be a cut older than a queueChanged
// the model already folded; the queue revision is a high-water mark within a
// daemon generation, and an older snapshot is ignored (the held state is put
// back over the detail the read replaced). A Ctrl+S that carried the regressed
// revision would be refused as a conflict every time until the next
// queueChanged.
func (m *hubModel) applyQueueRefresh(ref string, queue appwire.QueueState) {
	if strings.TrimSpace(ref) == m.sessionQueueRef && queue.Revision < m.sessionQueueLatest.Revision {
		m.detail.Queue = m.sessionQueueLatest
		return
	}
	m.applyQueueState(ref, queue)
}

// clearSessionQueue empties the local queue preview. Called when
// navigating away from a session so a stale preview never bleeds across
// views; new state arrives via the next ReadThread / queueChanged.
func (m *hubModel) clearSessionQueue() {
	m.sessionQueue = nil
	m.sessionQueueRef = ""
	m.sessionQueueLatest = appwire.QueueState{}
	// The stale gate a partial drain set belongs to the session it happened
	// in; a session entered afterwards starts with its own queue state.
	m.queueRevisionStale = false
	m.queueRevisionAtDrain = 0
}

func (m hubModel) notificationMatchesCurrentSession(notification appwire.Notification) bool {
	var params appwire.NotificationRef
	if json.Unmarshal(notification.Params, &params) != nil {
		return true
	}

	detailRef := strings.TrimSpace(m.detail.Ref)
	if params.Ref != "" && detailRef != "" {
		return params.Ref == detailRef
	}

	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return true
	}
	if threadID == strings.TrimSpace(m.detail.SessionID) {
		return true
	}
	if ref, err := appwire.ParseRef(detailRef); err == nil && ref.ThreadID != "" {
		return threadID == ref.ThreadID
	}
	return false
}

// handleChildActivityFrame routes a frame belonging to a watched subagent child
// to its rail row's live activity (matched by ref), and reports handled=true so
// the caller does NOT render it in the parent transcript. A child ref is always
// a different thread than this session, so this never swallows our own frames.
func (m *hubModel) handleChildActivityFrame(notification appwire.Notification) (tea.Cmd, bool) {
	if len(m.watchedChildRefs) == 0 {
		return nil, false
	}
	if notification.Method != appwire.NotifyHistoryUpdated {
		return nil, false
	}
	var ref appwire.NotificationRef
	if json.Unmarshal(notification.Params, &ref) != nil {
		return nil, false
	}
	childRef := strings.TrimSpace(ref.Ref)
	if childRef == "" || !m.watchedChildRefs[childRef] {
		return nil, false
	}
	var params appwire.HistoryUpdatedParams
	if json.Unmarshal(notification.Params, &params) != nil {
		return nil, true
	}
	reducer := m.sessionTranscriptReducer()
	applied := false
	for _, item := range params.Items {
		if activity := childActivityFromItem(item); activity != "" && reducer.ApplyChildActivity(childRef, activity) {
			applied = true
		}
	}
	if applied {
		m.applySessionTranscriptReducer(reducer)
	}
	return nil, true
}

// subscribeNewChildren subscribes (additively, no turns) to any running subagent
// child thread not yet watched, so its frames push live to the rail.
func (m *hubModel) subscribeNewChildren() tea.Cmd {
	if m.client == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, msg := range m.session.messages {
		if !isSubagentRunMessage(msg) {
			continue
		}
		ref := strings.TrimSpace(msg.Tool.Subagent.TranscriptRef)
		if ref == "" || msg.Tool.Subagent.Terminal || !runStillRunning(msg.Tool.Subagent.Status) {
			continue
		}
		if m.watchedChildRefs == nil {
			m.watchedChildRefs = map[string]bool{}
		}
		if m.watchedChildRefs[ref] {
			continue
		}
		m.watchedChildRefs[ref] = true
		cmds = append(cmds, subscribeChildActivity(m.client, ref))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// childActivityFromItem distills a child notification item into one verb-led
// line — the current thing the subagent is doing.
func childActivityFromItem(item appwire.ThreadItem) string {
	if tool := strings.TrimSpace(item.ToolName); tool != "" {
		if detail := strings.TrimSpace(item.Description); detail != "" {
			return tool + ": " + detail
		}
		return tool
	}
	switch strings.TrimSpace(item.Type) {
	case "agentMessage", "assistantText", "reasoning":
		return "responding"
	}
	if t := strings.TrimSpace(item.Text); t != "" {
		return t
	}
	return strings.TrimSpace(item.Status)
}

func runStillRunning(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "failed", "cancelled", "stopped", "succeeded", "exhausted", "command_exited_nonzero", "command_killed":
		return false
	}
	return true
}

// applyHistoryUpdated folds one history/updated notification — the full
// current, recorded form and version of every item and turn a just-appended
// entry affected — into the transcript. Items merge by version through
// ApplyHistoryItem; turns carry no items (the spec: "the reducer merges them
// against items it already holds by key") and exist here only to surface a
// turn's failure, the same system line turn/completed used to append.
func (m *hubModel) applyHistoryUpdated(params appwire.HistoryUpdatedParams) {
	reducer := m.sessionTranscriptReducer()
	for _, item := range params.Items {
		reducer.ApplyHistoryItem(item, transcript.TurnIndexFromID(item.TurnID))
	}
	reducer.FinalizeReasoning()
	m.applySessionTranscriptReducer(reducer)
	for _, turn := range params.Turns {
		if turn.Status == appwire.TurnStatusFailed && turn.Error != nil {
			m.addSessionSystemOnce(hubdiagnostics.FormatHubTurnError(turn.Error, "Session error"))
		}
	}
}

func (m *hubModel) sessionTranscriptReducer() transcript.TranscriptReducer {
	reducer := transcript.NewTranscriptReducer(m.session.messages, m.session.activeTools, m.session.activeMessages)
	reducer.SetCwd(m.detail.WorkingDir)
	return reducer
}

func (m *hubModel) applySessionTranscriptReducer(reducer transcript.TranscriptReducer) {
	m.session.messages = reducer.Messages()
	m.session.activeTools = reducer.ActiveTools()
	m.session.activeMessages = reducer.ActiveMessages()
}

func (m *hubModel) replaceSessionTranscript(messages []transcript.ChatMessage) {
	m.session.messages = append([]transcript.ChatMessage(nil), messages...)
	m.session.activeTools = nil
	m.session.activeMessages = nil
	m.browseSelected = -1
	m.transcriptView = nil
	m.session.refreshViewport()
}

// markModelRetryInProgress records that the retried model call is producing
// output again. A delta is the one signal that the reported backoff has
// elapsed, so the chip stops counting down toward a wait that is over — while
// still standing (clearing it here is the vanishing-chip bug). This is the
// delta half of modelRetryInProgress's OR; a delta drives a re-render on its
// own, so this half needs no timer — but a session that gets no further
// deltas still needs the wait to resolve once DelayMS actually elapses, which
// is what the timer half (applyModelRetryTick) exists for.
//
// Its caller runs it below applyHubNotification's current-session filter: this
// is a positive claim about the viewed session's call, and any other session
// that happens to be streaming would otherwise keep the chip permanently at
// "in progress" — suppressing the countdown that is the whole point of it.
func (m *hubModel) markModelRetryInProgress(notification appwire.Notification) {
	if m.modelRetry == nil {
		return
	}
	// overlay/delta is the read model's single replacement for the three
	// legacy per-kind delta notifications (agentMessage, reasoning summary,
	// tool output): any of them producing output again ends the wait.
	if notification.Method == appwire.NotifyOverlayDelta {
		m.modelRetryInProgress = true
	}
}

// modelRetryTickMsg drives applyModelRetryTick — the timer half of
// modelRetryInProgress's OR (see that field's own doc comment for the delta
// half, markModelRetryInProgress). Without it, a session that receives no
// further deltas during a reported wait shows a stale "retrying in Ns" chip
// forever once Ns has actually elapsed, since nothing else re-renders the TUI
// on a timer. Carries no payload: applyModelRetryTick re-reads m.modelRetry
// itself when the tick lands, same as any other notification-driven Update
// case reads m at apply time.
type modelRetryTickMsg struct{}

// modelRetryTickInterval is how often applyModelRetryTick re-checks a pending
// retry's elapsed wait. One second matches the chip's own second-granularity
// countdown ("retrying in 45s") — ticking faster would re-render without ever
// changing what the reader sees.
const modelRetryTickInterval = time.Second

// scheduleModelRetryTick starts one leg of the tick loop that flips a pending
// retry to "in progress" once its reported delay elapses without a delta
// arriving. Each tea.Tick fires exactly once; applyModelRetryTick re-arms the
// loop by returning another one of these while the wait is still open, so
// there is only ever one timer in flight per retry, not one per tick.
func scheduleModelRetryTick() tea.Cmd {
	return tea.Tick(modelRetryTickInterval, func(time.Time) tea.Msg {
		return modelRetryTickMsg{}
	})
}

// applyModelRetryTick re-evaluates the pending retry against the current time
// and reschedules itself while the wait is still open — the timer half of
// modelRetryInProgress's OR (markModelRetryInProgress is the delta half).
// Returns nil once there is nothing left to watch (no pending retry, or
// already in progress) so the tick loop actually stops rather than ticking
// forever after the chip has resolved — View() stays a pure function of model
// state (docs/developing-evener/testing.md), so this time-based check lives here in the Update
// path, never in rendering.
func (m *hubModel) applyModelRetryTick() tea.Cmd {
	if m.modelRetry == nil || m.modelRetryInProgress {
		return nil
	}
	if time.Since(m.modelRetryReceivedAt) >= time.Duration(m.modelRetry.DelayMS)*time.Millisecond {
		m.modelRetryInProgress = true
		return nil
	}
	return scheduleModelRetryTick()
}

// clearModelRetryOnProgress drops a pending model-call retry only once the
// wait it describes has actually ended: a turn boundary, or the completion of
// the model-output item (assistant message, reasoning, tool call) the retried
// call was producing.
//
// Deltas do NOT clear it. A user watching a provider grind through retries
// still sees deltas arrive between attempts; clearing on the first one makes
// the chip flicker away mid-grind and reads as "stuck" recovering, not
// "still working" — the vanishing-chip bug this rule exists to fix. The same
// reasoning excludes systemMessage and user-input item completions: a user
// steering "are you stuck?" completes a systemMessage item mid-grind, and
// that is not evidence the retried call finished.
func (m *hubModel) clearModelRetryOnProgress(notification appwire.Notification) {
	if m.modelRetry == nil {
		return
	}
	if notification.Method != appwire.NotifyHistoryUpdated {
		return
	}
	// history/updated is the read model's replacement for item/completed
	// (and, since a turn boundary no longer has its own notification, the
	// shared appwire-client reducer's own port of this rule relies on the
	// same signal alone): the recorded completion of a model-output item is
	// what actually proves the retried call finished.
	var params appwire.HistoryUpdatedParams
	if json.Unmarshal(notification.Params, &params) != nil {
		return
	}
	for _, item := range params.Items {
		if isModelOutputItemType(item.Type) {
			m.modelRetry = nil
			return
		}
	}
}

// isModelOutputItemType reports whether a thread item's type is model output
// (as opposed to a systemMessage announcement or user input), matching the
// cases transcript.TranscriptReducer.ApplyThreadItem treats as the model
// speaking or acting.
func isModelOutputItemType(itemType string) bool {
	switch itemType {
	case "agentMessage", "reasoning", "commandExecution":
		return true
	}
	return false
}
