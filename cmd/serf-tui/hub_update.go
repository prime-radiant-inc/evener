package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/serf/cmd/serf-tui/internal/pending"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/internal/appwire"
)

func (m hubModel) updateImpl(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
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
		if msg.err != nil {
			if msg.expectedState != "" && (m.mode != hubModeSession || msg.ref == "" || m.detail.Ref != msg.ref || msg.expectedRefreshToken != m.statusRefreshToken) {
				return m, nil
			}
			m.sessionDetailsRequested = false
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
			m.detail = msg.detail
			if msg.expectedState == "" {
				m.replaceSessionTranscript(msg.messages)
			}
			// Refresh queue preview from the authoritative read response
			// (kata r80p) so reloads / status refreshes resync state.
			m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
			if m.sessionDetailsRequested {
				panel := hubSessionPanel{Body: m.renderSessionDetails()}
				m.sessionPanel = &panel
			}
			m.sessionDetailsRequested = false
			m.session.refreshViewport()
			return m, nil
		}
		m.clearNoticesByCategory("action-unavailable")
		m.clearPendingAttachments(true)
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
		m.forkDraft = nil
		m.sessionThemePicker = nil
		m.sessionModelPicker = nil
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
		return m, nil
	case hubNotificationMsg:
		if !msg.ok {
			return m, nil
		}
		cmd := m.applyHubNotification(msg.notification)
		return m, tea.Batch(cmd, waitHubNotification(m.client))
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
					m.sessionQueue = append(m.sessionQueue, preview)
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
		case "steer":
			m.addSessionSystem("Steering sent.")
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
		return m, fetchHubSession(m.client, ref)
	case hubForkMsg:
		if msg.err != nil {
			if m.forkDraft != nil {
				m.forkDraft.Submitting = false
			}
			m.recordSessionError("Fork failed: " + msg.err.Error())
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
		return m, fetchHubSession(m.client, ref)
	case hubSpawnMsg:
		m.spawnSubmitting = false
		if msg.err != nil {
			m.addNotice(noticePanel{
				Title:      "Spawn failed",
				Category:   "launch",
				Summary:    "Hub spawn failed.",
				Source:     m.sourceLabelForNotice(),
				Reason:     msg.err.Error(),
				NextAction: "Check Hub launch diagnostics, auth status, and spawn options.",
			})
			return m, nil
		}
		m.clearNoticesByCategory("launch")
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.err = fmt.Errorf("spawn returned invalid ref: %s", msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubModelsMsg:
		if msg.err != nil {
			if m.mode == hubModeSpawn {
				if msg.harness != "" && !m.spawnHarnessUsesSerfModels() {
					m.err = fmt.Errorf("%s models unavailable; using harness default: %w", msg.harness, msg.err)
				} else {
					m.err = fmt.Errorf("models failed: %w", msg.err)
				}
			}
			return m, nil
		}
		if msg.harness != "" {
			if m.spawnHarnessModels == nil {
				m.spawnHarnessModels = map[string][]modelPickerItem{}
			}
			m.spawnHarnessModels[msg.harness] = msg.models
			if m.mode == hubModeSpawn && m.spawnHarness == msg.harness {
				if len(msg.models) == 0 {
					m.err = fmt.Errorf("no %s models available; using harness default", msg.harness)
					return m, nil
				}
				m.openSpawnModelPicker(msg.models)
			}
			return m, nil
		}
		m.spawnModels = msg.models
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
		}
		return m, nil
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
		picker := newModelPicker(msg.models, m.detail.Model, m.width)
		m.sessionModelPicker = &picker
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
		picker := newTranscriptPicker(items, activeRef, m.width)
		m.transcriptTargets = append([]appwire.ThreadTranscriptTarget(nil), msg.targets...)
		m.sessionTranscriptPicker = &picker
		return m, nil
	case hubTranscriptMsg:
		if msg.err != nil {
			m.addSessionSystem("Could not read transcript: " + hubErrorReason(msg.err))
			return m, nil
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
				m.err = fmt.Errorf("spawn options failed: %w", msg.err)
			}
			return m, nil
		}
		m.spawnHarnesses = msg.harnesses
		if len(m.spawnHarnesses) == 0 {
			m.spawnHarnesses = []string{"serf"}
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
				m.spawnHarnessKinds[harness] = "serf"
			}
		}
		if !stringInSlice(m.spawnHarness, m.spawnHarnesses) {
			m.spawnHarness = m.spawnHarnesses[0]
		}
		m.spawnModels = msg.models
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
			if msg.modelErr != nil && m.spawnHarnessUsesSerfModels() {
				m.err = fmt.Errorf("models failed: %w", msg.modelErr)
			}
		}
		return m, nil
	case hubAuthStatusMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Auth status failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authStatus = authStatusFromAppWire(msg.status)
		m.authStatusSeen = true
		m.clearSessionError()
		m.addSessionSystem(formatAuthStatusSummary(m.authStatus))
		return m, nil
	case hubAuthLoginStartMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			return m, nil
		}
		m.authLoginProvider = strings.TrimSpace(msg.resp.Provider)
		if m.authLoginProvider == "" {
			m.authLoginProvider = "openai"
		}
		m.authLoginFlowID = msg.resp.FlowID
		m.addSessionSystem("OpenAI sign-in URL:\n" + msg.resp.URL + "\nPaste the full OpenAI redirect URL and press enter.")
		return m, nil
	case hubAuthLoginCompleteMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Login failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authLoginProvider = ""
		m.authLoginFlowID = ""
		m.authStatus = authStatusFromAppWire(msg.resp.Status)
		m.authStatusSeen = true
		m.clearSessionError()
		m.addSessionSystem("OpenAI login complete. " + formatAuthStatusSummary(m.authStatus))
		return m, nil
	case hubAuthLogoutMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Logout failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authStatus = authStatusFromAppWire(msg.resp.Status)
		m.authStatusSeen = true
		m.clearSessionError()
		if msg.resp.Removed {
			m.addSessionSystem("OpenAI sign-out complete. " + formatAuthStatusSummary(m.authStatus))
		} else {
			m.addSessionSystem("OpenAI auth was already signed out. " + formatAuthStatusSummary(m.authStatus))
		}
		return m, nil
	case launchconfig.AuthListResultMsg:
		// launchconfig.AuthListResultMsg is no longer used; msgs are dropped.
		return m, nil
	case launchconfig.InstanceListResultMsg:
		if m.credentialsPanel != nil {
			updated, cmd := m.credentialsPanel.Update(msg)
			panel := updated.(launchconfig.CredentialsPanel)
			m.credentialsPanel = &panel
			return m, cmd
		}
		return m, nil
	case launchconfig.InstanceMutateResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		// Refresh the panel with the updated list returned by the mutation.
		if m.credentialsPanel != nil {
			updated, cmd := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: msg.List})
			panel := updated.(launchconfig.CredentialsPanel)
			m.credentialsPanel = &panel
			return m, cmd
		}
		return m, nil
	case launchconfig.InstanceSetDefaultMsg:
		if m.client != nil {
			return m, launchconfig.CmdInstanceSetDefault(m.client, msg.Name)
		}
		return m, nil
	case launchconfig.InstanceRemoveMsg:
		if m.client != nil {
			return m, launchconfig.CmdInstanceRemove(m.client, msg.Name)
		}
		return m, nil
	case launchconfig.InstanceCreateSubmitMsg:
		if m.client != nil {
			return m, launchconfig.CmdInstanceCreate(m.client, msg.Params)
		}
		return m, nil
	case launchconfig.InstanceEditSubmitMsg:
		if m.client != nil {
			return m, launchconfig.CmdInstanceEdit(m.client, msg.Params)
		}
		return m, nil
	case launchconfig.CredentialsActionMsg:
		switch msg.Action {
		case "set":
			modal := newTextInputModalMasked(fmt.Sprintf("API key for %s:", msg.Instance), "credential-set:"+msg.Instance)
			m.followupModal = &modal
			return m, nil
		case "logout":
			if m.client != nil {
				return m, launchconfig.CmdAuthLogout(m.client, msg.Instance)
			}
			return m, nil
		case "oauth":
			if m.client != nil {
				return m, launchconfig.CmdAuthLoginStart(m.client, msg.Instance)
			}
			return m, nil
		}
		return m, nil
	case launchconfig.LaunchOverridesOpenMsg:
		var modal launchconfig.LaunchOverridesModal
		if msg.Initial != nil {
			modal = launchconfig.NewLaunchOverridesModalWith(*msg.Initial)
		} else {
			modal = launchconfig.NewLaunchOverridesModal()
		}
		m.launchOverridesModal = &modal
		if m.client != nil {
			return m, launchconfig.CmdLaunchSchema(m.client)
		}
		return m, nil
	case launchconfig.LaunchOverridesResultMsg:
		m.launchOverridesModal = nil
		if !msg.Cancelled {
			m.spawnLaunchOverrides = msg.Overrides
		}
		return m, nil
	case launchconfig.LaunchSettingsEditRequestMsg:
		if msg.Layer == "launch" {
			prompt := fmt.Sprintf("Edit %s (current: %s):", msg.Field, msg.CurrentValue)
			if msg.Field == "mcps" {
				prompt = fmt.Sprintf("Edit %s as JSON array, or name:command args... (current: %s):", msg.Field, msg.CurrentValue)
			}
			tag := "launch-override:" + msg.Field
			var modal textInputModal
			if msg.PathCompletion || launchconfig.LaunchSettingsFieldUsesPathCompletion(msg.Field) {
				modal = newPathTextInputModal(prompt, tag, msg.CurrentValue)
			} else {
				modal = newTextInputModalWithInput(prompt, tag, msg.CurrentValue)
			}
			m.followupModal = &modal
			return m, nil
		}
		prompt := fmt.Sprintf("Edit %s.%s (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
		if msg.Field == "mcps" {
			prompt = fmt.Sprintf("Edit %s.%s as JSON array, or name:command args... (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
		}
		tag := fmt.Sprintf("settings-edit:%s:%s", msg.Layer, msg.Field)
		var modal textInputModal
		if msg.PathCompletion || launchconfig.LaunchSettingsFieldUsesPathCompletion(msg.Field) {
			modal = newPathTextInputModal(prompt, tag, msg.CurrentValue)
		} else {
			modal = newTextInputModalWithInput(prompt, tag, msg.CurrentValue)
		}
		m.followupModal = &modal
		return m, nil
	case textInputResultMsg:
		if strings.HasPrefix(msg.Tag, "credential-set:") {
			provider := strings.TrimPrefix(msg.Tag, "credential-set:")
			m.followupModal = nil
			if msg.Cancelled || msg.Value == "" {
				return m, nil
			}
			if m.client != nil {
				return m, launchconfig.CmdAuthApiKeySet(m.client, provider, msg.Value)
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "oauth-redirect:") {
			parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "oauth-redirect:"), ":", 2)
			m.followupModal = nil
			if msg.Cancelled || msg.Value == "" {
				return m, nil
			}
			if len(parts) == 2 && m.client != nil {
				return m, launchconfig.CmdAuthLoginComplete(m.client, parts[0], parts[1], msg.Value)
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "launch-override:") {
			field := strings.TrimPrefix(msg.Tag, "launch-override:")
			m.followupModal = nil
			if msg.Cancelled {
				return m, nil
			}
			if m.launchOverridesModal != nil {
				updated, err := m.launchOverridesModal.ApplyEdit(field, msg.Value)
				if err != nil {
					m.err = err
					return m, nil
				}
				m.launchOverridesModal = &updated
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "settings-edit:") {
			parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "settings-edit:"), ":", 2)
			if len(parts) != 2 {
				return m, nil
			}
			layer, field := parts[0], parts[1]
			m.followupModal = nil
			if msg.Cancelled {
				return m, nil
			}
			if m.launchSettingsPanel == nil {
				return m, nil
			}
			panel, updatedLayer, err := m.launchSettingsPanel.ApplyEdit(field, msg.Value)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.launchSettingsPanel = &panel
			return m, launchconfig.CmdSetLayer(m.client, panel.CWD(), layer, updatedLayer)
		}
		return m, nil
	case launchconfig.AuthApiKeySetResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		if m.credentialsPanel != nil && m.client != nil {
			return m, launchconfig.CmdInstanceList(m.client)
		}
		return m, nil
	case launchconfig.AuthLoginStartResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		modal := newTextInputModal("Paste full redirect URL after sign-in:\n"+msg.URL, "oauth-redirect:"+msg.Provider+":"+msg.FlowID)
		m.followupModal = &modal
		return m, nil
	case launchconfig.AuthLoginCompleteResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		if m.credentialsPanel != nil && m.client != nil {
			return m, launchconfig.CmdInstanceList(m.client)
		}
		return m, nil
	case launchconfig.LaunchSetLayerResultMsg:
		if m.launchSettingsPanel != nil {
			updated, cmd := m.launchSettingsPanel.Update(msg)
			p := updated.(launchconfig.LaunchSettingsPanel)
			m.launchSettingsPanel = &p
			if msg.Err == nil && m.client != nil {
				// Refresh the just-saved layer from disk.
				return m, tea.Batch(cmd, launchconfig.CmdGetLayer(m.client, msg.CWD, msg.Layer))
			}
			return m, cmd
		}
		return m, nil
	case launchconfig.LaunchLayerResultMsg, launchconfig.LaunchResolveResultMsg, launchconfig.LaunchTrustResultMsg, launchconfig.LaunchSchemaResultMsg:
		if _, ok := msg.(launchconfig.LaunchSchemaResultMsg); ok && m.launchOverridesModal != nil {
			updated, cmd := m.launchOverridesModal.Update(msg)
			p := updated.(launchconfig.LaunchOverridesModal)
			m.launchOverridesModal = &p
			return m, cmd
		}
		if m.launchSettingsPanel != nil {
			updated, cmd := m.launchSettingsPanel.Update(msg)
			p := updated.(launchconfig.LaunchSettingsPanel)
			m.launchSettingsPanel = &p
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

func statusRefreshStatesMatchExpected(currentState, payloadState, expectedState string) bool {
	return currentState == expectedState && payloadState == expectedState
}
