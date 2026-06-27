package main

import (
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubdiagnostics"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/serf/cmd/serf-tui/internal/pending"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func (m *hubModel) applyHubNotification(notification appwire.Notification) tea.Cmd {
	// Panel-refresh notifications fire regardless of current mode.
	switch notification.Method {
	case appwire.NotifySerfAuthUpdated:
		if m.credentialsPanel != nil && m.client != nil {
			return launchconfig.CmdInstanceList(m.client)
		}
		return nil
	case appwire.NotifySerfLaunchUpdated:
		if m.launchSettingsPanel != nil {
			return m.launchSettingsPanel.InitialCmd()
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
	var cmd tea.Cmd
	switch notification.Method {
	case appwire.NotifyTurnStarted:
		var params struct {
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.setActiveTurnID(params.Turn.ID)
		}
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			previous := m.detail.State
			m.detail.State = params.Status.Type
			m.session.processing = params.Status.Type == appwire.ThreadStatusActive
			// Refresh on any transition so capabilities (interrupt, steer, send, etc.)
			// reflect the source's current view. Without this, the cached idle snapshot
			// keeps Interrupt=false for the entire turn (kata 4yvd).
			if previous != params.Status.Type && m.client != nil {
				if ref, ok := m.currentRef(); ok {
					m.statusRefreshToken++
					cmd = fetchHubSessionExpectingStateToken(m.client, ref, params.Status.Type, m.statusRefreshToken)
				}
			}
		}
	case appwire.NotifyItemStarted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyThreadItem(params.Item, false)
		}
	case appwire.NotifyItemCompleted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyThreadItem(params.Item, true)
		}
	case appwire.NotifyAgentMessageDelta:
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyAgentMessageDelta(params.TurnID, params.ItemID, params.Delta)
		}
	case appwire.NotifyReasoningSummaryDelta:
		var params appwire.ReasoningSummaryDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyReasoningSummaryDelta(params.TurnID, params.ItemID, params.Delta)
		}
	case appwire.NotifyAgentMessageReset:
		var params appwire.AgentMessageResetParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ResetAgentMessage(params.TurnID, params.ItemID)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyToolOutputDelta:
		var params appwire.ToolOutputDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplyToolOutputDelta(params.ItemID, params.Delta)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifySerfJobStarted, appwire.NotifySerfJobFinished:
		var params struct {
			Job appwire.SerfJobInfo `json:"job"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.ApplySerfJob(params.Job)
			m.applySessionTranscriptReducer(reducer)
			// Subscribe to any newly-running child so its activity pushes live.
			cmd = m.subscribeNewChildren()
		}
	case appwire.NotifyTurnCompleted:
		var params appwire.TurnCompletedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			turnID := firstNonEmptyString(params.TurnID, params.Turn.ID)
			for _, item := range params.Turn.Items {
				if item.TurnID == "" {
					item.TurnID = turnID
				}
				m.applyThreadItem(item, true)
			}
			// A completed turn never leaves a thought streaming open.
			reducer := m.sessionTranscriptReducer()
			reducer.FinalizeReasoning()
			m.applySessionTranscriptReducer(reducer)
			if turnID != "" && turnID == m.detail.ActiveTurnID {
				m.detail.ActiveTurnID = ""
			}
			if params.Turn.Status == appwire.TurnStatusFailed {
				m.addSessionSystemOnce(hubdiagnostics.FormatHubTurnError(params.Turn.Error, "Session error"))
			}
			// Queue head pop is now driven by thread/queueChanged from
			// the daemon (kata r80p); we no longer mirror locally on turn
			// completion.
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
	case appwire.NotifySerfSteeringInjected:
		var params struct {
			Text   string              `json:"text"`
			Images []appwire.InputItem `json:"images"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			text := strings.TrimSpace(params.Text)
			if text == "" {
				text = transcript.ImageItemsPlaceholder(params.Images)
			}
			if text != "" {
				m.session.messages = append(m.session.messages, transcript.ChatMessage{Kind: transcript.MsgSteering, Text: text})
			}
		}
	case appwire.NotifyWarning:
		// Cause is decoded as a pointer so its absence (legacy payloads)
		// stays distinguishable from kind=="" (kata 5q3p). When present,
		// classifyWarningCategory uses the typed Cause; otherwise it falls
		// back to the message-substring path so legacy NotifyWarning
		// payloads still classify correctly.
		var params struct {
			Message string                   `json:"message"`
			Source  string                   `json:"source"`
			Title   string                   `json:"title"`
			Cause   *appwire.DiagnosticCause `json:"cause"`
			Warning struct {
				Message string `json:"message"`
			} `json:"warning"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			message := params.Message
			if strings.TrimSpace(message) == "" {
				message = params.Warning.Message
			}
			title := params.Title
			source := params.Source
			if strings.TrimSpace(title) == "" && strings.TrimSpace(source) == "" && classifyWarningCategory(message, params.Cause) == "provider" {
				source = "provider"
			}
			m.addSessionSystemOnce(hubdiagnostics.FormatHubDiagnosticWithCause(title, source, message, "Session warning", params.Cause))
		}
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

// reconcilePendingFromNotification translates an inbound daemon
// notification into the wire-method name(s) the pending coordinator
// registered under, then calls TryReconcile. Some notifications
// match multiple methods (serf/steering/injected reconciles both
// turn/steer with matching text AND any in-flight turn/drainAsSteer).
//
// Drain-special: turn/drainAsSteer matches first-come-first-served
// regardless of text, because the daemon collapses queued entries
// into one STEERING and the placeholder doesn't know the joined text.
func reconcilePendingFromNotification(pending *pendingpkg.PendingCoordinator, n appwire.Notification) {
	ref := notificationPendingRef(n)
	switch n.Method {
	case appwire.NotifySerfSteeringInjected:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(n.Params, &p)
		pending.TryReconcile(appwire.MethodTurnSteer, p.Text, ref)
		pending.TryReconcile(appwire.MethodTurnDrainAsSteer, "", ref)
	case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
		// userMessage item carries the user's text. Match against
		// any turn/start pending entry.
		var p struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return
		}
		if p.Item.Type == "userMessage" && (p.Item.Text != "" || len(p.Item.Images) > 0) {
			text := p.Item.Text
			if text == "" {
				text = transcript.ImageItemsPlaceholder(p.Item.Images)
			}
			pending.TryReconcile(appwire.MethodTurnStart, text, ref)
		}
	case appwire.NotifyTurnCompleted:
		var p struct {
			Turn appwire.Turn `json:"turn"`
		}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return
		}
		for _, item := range p.Turn.Items {
			if item.Type != "userMessage" || (item.Text == "" && len(item.Images) == 0) {
				continue
			}
			text := item.Text
			if text == "" {
				text = transcript.ImageItemsPlaceholder(item.Images)
			}
			pending.TryReconcile(appwire.MethodTurnStart, text, ref)
		}
	}
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
// wire-sourced snapshot (kata r80p). Called from ReadThread responses and
// from thread/queueChanged notifications. Scoped to the current session
// ref so a notification routed to a different session can't leak into
// this view.
func (m *hubModel) applyQueueState(ref string, queue appwire.QueueState) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	m.sessionQueueRef = ref
	if queue.Depth == 0 && len(queue.Preview) == 0 {
		m.sessionQueue = nil
		return
	}
	m.sessionQueue = append([]string(nil), queue.Preview...)
}

// clearSessionQueue empties the local queue preview. Called when
// navigating away from a session so a stale preview never bleeds across
// views; new state arrives via the next ReadThread / queueChanged.
func (m *hubModel) clearSessionQueue() {
	m.sessionQueue = nil
	m.sessionQueueRef = ""
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
	if notification.Method != appwire.NotifyItemStarted && notification.Method != appwire.NotifyItemCompleted {
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
	var params struct {
		Item appwire.ThreadItem `json:"item"`
	}
	if json.Unmarshal(notification.Params, &params) != nil {
		return nil, true
	}
	if activity := childActivityFromItem(params.Item); activity != "" {
		reducer := m.sessionTranscriptReducer()
		if reducer.ApplyChildActivity(childRef, activity) {
			m.applySessionTranscriptReducer(reducer)
		}
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
		if ref == "" || !runStillRunning(msg.Tool.Subagent.Status) {
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
	case "completed", "done", "failed", "cancelled", "stopped", "succeeded":
		return false
	}
	return true
}

func (m *hubModel) applyAgentMessageDelta(turnID, itemID, delta string) {
	reducer := m.sessionTranscriptReducer()
	reducer.ApplyAgentMessageDelta(turnID, itemID, delta)
	m.applySessionTranscriptReducer(reducer)
}

func (m *hubModel) applyThreadItem(item appwire.ThreadItem, completed bool) {
	reducer := m.sessionTranscriptReducer()
	reducer.ApplyThreadItem(item, transcript.TurnIndexFromID(item.TurnID), completed)
	m.applySessionTranscriptReducer(reducer)
}

func (m *hubModel) applyReasoningSummaryDelta(turnID, itemID, delta string) {
	reducer := m.sessionTranscriptReducer()
	reducer.ApplyReasoningSummaryDelta(turnID, itemID, delta)
	m.applySessionTranscriptReducer(reducer)
}

func (m *hubModel) sessionTranscriptReducer() transcript.TranscriptReducer {
	return transcript.NewTranscriptReducer(m.session.messages, m.session.activeTools, m.session.activeMessages)
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
