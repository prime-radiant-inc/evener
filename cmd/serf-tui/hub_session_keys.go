package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubstart"
	"primeradiant.com/serf/cmd/serf-tui/internal/inputhistory"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func (m *hubModel) resizeSessionInputFrom(prevHeight int) {
	wantHeight := m.session.input.LineCount()
	if wantHeight < 1 {
		wantHeight = 1
	}
	if wantHeight > m.session.input.MaxHeight {
		wantHeight = m.session.input.MaxHeight
	}
	if wantHeight != prevHeight {
		m.session.input.SetHeight(wantHeight)
		m.session.viewport.Height = m.session.vpHeight()
	}
}

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil && m.launchOverridesModal != nil {
		// followupModal is open for a launch-override edit — route to it
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(tuipick.TextInputModal)
		m.followupModal = &modal
		if modal.Done() {
			m.followupModal = nil
		}
		return m, cmd
	}

	if m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchconfig.LaunchOverridesModal)
		m.launchOverridesModal = &p
		if p.Done() {
			m.launchOverridesModal = nil
		}
		return m, cmd
	}

	if m.sessionThemePicker != nil {
		picker, cmd := m.sessionThemePicker.Update(msg)
		m.sessionThemePicker = &picker
		if picker.Done() {
			m.sessionThemePicker = nil
			if picker.Selected() != "" {
				tuitheme.SetThemeAndPersist(m.stateDir, picker.Selected())
				msgrender.InitMarkdownRenderer(m.width)
				m.session.viewport.Style = tuitheme.ViewportStyle
				applyInputTheme(&m.session.input)
				m.addSessionSystem(fmt.Sprintf("Switched to %s theme.", picker.Selected()))
			} else {
				m.session.refreshViewport()
			}
		}
		return m, cmd
	}

	if m.sessionModelPicker != nil {
		updated, cmd := m.sessionModelPicker.Update(msg)
		picker := updated.(tuipick.ModelPicker)
		m.sessionModelPicker = &picker
		if picker.Done() {
			selected := picker.Selected()
			m.sessionModelPicker = nil
			if selected != "" {
				ref, ok := m.currentRef()
				if !ok {
					m.addSessionSystem("Session ref is invalid.")
					return m, nil
				}
				m.addSessionSystem(fmt.Sprintf("Switching to model %s...", selected))
				return m, sendHubAction(m.client, ref, selected, "")
			}
			m.session.refreshViewport()
		}
		return m, cmd
	}

	if m.sessionTranscriptPicker != nil {
		updated, cmd := m.sessionTranscriptPicker.Update(msg)
		picker := updated.(tuipick.ModelPicker)
		m.sessionTranscriptPicker = &picker
		if picker.Done() {
			selected := picker.Selected()
			m.sessionTranscriptPicker = nil
			if selected != "" {
				target, ok := hubTranscriptTargetByRef(m.transcriptTargets, selected)
				if !ok {
					m.addSessionSystem("Transcript target is no longer available.")
					return m, nil
				}
				if target.Kind == "main" {
					m.transcriptView = nil
					m.session.scrollMode = false
					m.session.input.Focus()
					m.session.refreshViewport()
					return m, nil
				}
				return m, fetchHubTranscript(m.client, target)
			}
			m.session.refreshViewport()
		}
		return m, cmd
	}

	if m.sessionPanel != nil && msg.String() == "esc" {
		m.sessionPanel = nil
		m.session.refreshViewport()
		return m, nil
	}

	if m.transcriptView != nil {
		switch msg.String() {
		case "esc", "i", "q":
			m.transcriptView = nil
			m.session.scrollMode = false
			m.session.focusedToolIdx = -1
			m.browseSelected = -1
			m.session.input.Focus()
			m.session.refreshViewport()
		default:
			m.session.viewport, _ = m.session.viewport.Update(msg)
		}
		return m, nil
	}

	if m.forkDraft != nil {
		switch msg.String() {
		case "esc":
			m.forkDraft = nil
			m.session.resetInput()
			m.enterSessionBrowse(false)
			m.addSessionSystem("Fork cancelled.")
			return m, nil
		case "enter":
			if m.forkDraft.Submitting {
				return m, nil
			}
			text := strings.TrimSpace(m.session.input.Value())
			if text == "" {
				m.addSessionSystem("Fork message cannot be empty.")
				return m, nil
			}
			if m.client == nil {
				m.addSessionSystem("Fork is not available without a hub client.")
				return m, nil
			}
			draft := *m.forkDraft
			m.forkDraft.Submitting = true
			m.addSessionSystem(fmt.Sprintf("Forking from turn %d...", draft.Turn))
			return m, sendHubFork(m.client, draft.Ref, hubForkRequest{
				Turn:          draft.Turn,
				EditedMessage: text,
				Label:         draft.Label,
			})
		}
	}

	if m.session.scrollMode {
		switch msg.String() {
		case "esc", "i", "q":
			m.exitSessionBrowse()
		case "up", "down", "left", "right":
			return m.updateSessionBrowseComposerKey(msg)
		case "k":
			m.moveBrowseSelection(-1)
		case "j":
			m.moveBrowseSelection(1)
		case "pgup":
			m.moveBrowsePage(-1)
		case "pgdown":
			m.moveBrowsePage(1)
		case "f":
			m.startForkDraft()
		case "ctrl+t":
			m.toggleAllBrowseToolEntries()
		default:
			if msg.Type == tea.KeyRunes || msg.Paste {
				prevHeight := m.session.input.Height()
				var cmd tea.Cmd
				m.session.input, cmd = m.session.input.Update(msg)
				m.resizeSessionInputFrom(prevHeight)
				return m, cmd
			}
			m.session.viewport, _ = m.session.viewport.Update(msg)
		}
		return m, nil
	}

	if strings.TrimSpace(m.authLoginFlowID) != "" {
		switch msg.String() {
		case "esc":
			m.authLoginProvider = ""
			m.authLoginFlowID = ""
			m.session.resetInput()
			m.addSessionSystem("OpenAI login cancelled.")
			return m, nil
		case "enter":
			redirectURL := strings.TrimSpace(m.session.input.Value())
			if redirectURL == "" {
				return m, nil
			}
			provider := m.authLoginProvider
			flowID := m.authLoginFlowID
			m.session.resetInput()
			m.addSessionSystem("Finishing OpenAI login...")
			return m, completeHubAuthLogin(m.client, provider, flowID, redirectURL)
		}
	}

	if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
		prevHeight := m.session.input.Height()
		m.session.input.InsertString("\n")
		m.resizeSessionInputFrom(prevHeight)
		return m, nil
	}
	if msg.String() == "/" && strings.TrimSpace(m.session.input.Value()) == "" {
		m.openCommandPalette()
		return m, nil
	}
	if msg.Type == tea.KeyCtrlP || msg.String() == "ctrl+p" {
		m.openCommandPalette()
		return m, nil
	}
	if msg.Type == tea.KeyCtrlL {
		var initial *appwire.LaunchConfigLayer
		if m.spawnLaunchOverrides != nil {
			cp := *m.spawnLaunchOverrides
			initial = &cp
		}
		return m, func() tea.Msg { return launchconfig.LaunchOverridesOpenMsg{Initial: initial} }
	}
	if msg.Type == tea.KeyCtrlS {
		return m.handleSessionForceSteer()
	}
	if msg.Type == tea.KeyCtrlV || isAltVKey(msg) {
		return m.handleClipboardPaste()
	}
	// Alt+Backspace removes the most recently added attachment chip
	// (kata 5vxd). Plain Ctrl-H is not safe here because many terminals
	// report ordinary Backspace as Ctrl-H.
	if msg.Alt && (msg.Type == tea.KeyBackspace || msg.Type == tea.KeyCtrlH) && len(m.pendingAttachments) > 0 {
		m.removePendingAttachment(len(m.pendingAttachments) - 1)
		return m, nil
	}
	if msg.Paste {
		if cmd, handled := m.handleBracketedPaste(string(msg.Runes)); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		now := time.Now()
		if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) <= hubCtrlCQuitWindow {
			m.postQuitMessage = m.restoreInstructionMessage()
			return m, tea.Quit
		}
		m.lastCtrlC = now
		// First ctrl+c during an active turn interrupts the turn (matching
		// muscle-memory from the legacy standalone TUI). Second ctrl+c
		// within hubCtrlCQuitWindow always quits, regardless of state.
		if m.client != nil && m.detail.Capabilities.Interrupt {
			if turnID := strings.TrimSpace(m.detail.ActiveTurnID); turnID != "" {
				if ref, ok := m.currentRef(); ok {
					m.addSessionSystem("Interrupting active turn. Press ctrl+c again to quit.")
					return m, sendHubAction(m.client, ref, "interrupt", turnID)
				}
			}
		}
		return m, nil
	case "esc":
		m.enterSessionBrowse(false)
		return m, nil
	case "up":
		if len(m.session.history) > 0 {
			if m.session.historyIdx >= 0 {
				if m.session.historyIdx > 0 {
					m.session.historyIdx--
				}
			} else if m.session.input.Value() == "" {
				m.session.historyDraft = m.session.input.Value()
				m.session.historyIdx = len(m.session.history) - 1
			} else {
				return m, nil
			}
			m.session.setInputValue(inputhistory.UnescapeHistory(m.session.history[m.session.historyIdx]))
			return m, nil
		}
	case "down":
		if m.session.historyIdx >= 0 {
			if m.session.historyIdx < len(m.session.history)-1 {
				m.session.historyIdx++
				m.session.setInputValue(inputhistory.UnescapeHistory(m.session.history[m.session.historyIdx]))
			} else {
				m.session.historyIdx = -1
				m.session.setInputValue(m.session.historyDraft)
				m.session.historyDraft = ""
			}
			return m, nil
		}
	case "pgup":
		m.enterSessionBrowse(true)
		return m, nil
	}

	if msg.String() == "enter" {
		draft := m.session.input.Value()
		text := strings.TrimSpace(draft)
		if text == "" && len(m.pendingAttachments) == 0 {
			return m, nil
		}
		if text != "" {
			if cmd, args := parseSlashCommand(text); cmd != "" {
				m.session.resetInput()
				next := m.runHubSlashCommand(cmd, args)
				return m, next
			}
		}
		composerMode := m.sessionComposerMode()
		if composerMode == hubComposerModeQueue {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return m, nil
			}
			// Clear the composer optimistically; the queue preview above
			// the composer will show the enqueued line. On failure the
			// hubQueueMsg handler restores the draft.
			if text != "" {
				m.session.addHistory(text)
			}
			m.session.resetInput()
			m.session.refreshViewport()
			attachments := m.snapshotPendingAttachmentsForSubmit()
			return m, sendHubQueue(m.client, ref, text, draft, attachments)
		}
		if composerMode == hubComposerModeReadOnly || !m.sessionCanStartTurn() {
			reason := m.sessionComposerReadOnlyReason()
			if reason == "" {
				reason = "send is not available for this session"
			}
			m.addActionUnavailableNotice("send", "Send is not available for this session.", reason)
			return m, nil
		}
		ref, ok := m.currentRef()
		if !ok {
			m.addSessionSystem("Session ref is invalid.")
			return m, nil
		}
		reducer := m.sessionTranscriptReducer()
		reducer.ApplyUserMessageEcho(text)
		m.applySessionTranscriptReducer(reducer)
		if text != "" {
			m.session.addHistory(text)
		}
		m.session.resetInput()
		m.session.refreshViewport()
		attachments := m.snapshotPendingAttachmentsForSubmit()
		return m, sendHubInput(m.client, ref, text, draft, attachments)
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSessionInputFrom(prevHeight)
	return m, cmd
}

func (m *hubModel) runHubSlashCommand(cmd, args string) tea.Cmd {
	definition, ok := hubCommandByName(cmd)
	if !ok || definition.Scopes&hubCommandSession == 0 {
		m.addSessionSystem("Unknown command: /" + cmd + ". Type /help for available commands.")
		return nil
	}
	available, reason := hubCommandAvailable(definition, hubCommandContext{mode: hubModeSession, caps: m.detail.Capabilities})
	if !available {
		m.addActionUnavailableNotice(definition.UnavailableAction, definition.UnavailableSummary, reason)
		return nil
	}
	return runHubCommandDefinition(m, definition, args)
}

func (m hubModel) restoreInstructionMessage() string {
	hubURL := strings.TrimSpace(m.hubURL)
	if hubURL == "" {
		hubURL = hubstart.DefaultHubAddr
	}
	ref := strings.TrimSpace(m.detail.Ref)
	if ref == "" {
		ref = strings.TrimSpace(m.session.sessionID)
	}
	if ref == "" {
		return ""
	}
	return fmt.Sprintf("Restore this session: serf-tui --hub-addr %s, then open %s", hubURL, ref)
}

// handleSessionForceSteer routes the Ctrl+S force-steer keybind: drain
// every queued message into a single STEERING entry for the in-flight turn
// (kata 0bq1). If the composer has unsent text or attachments, they ride on
// the drain request so the daemon appends and drains atomically. With nothing
// to steer, the binding fires a transient banner instead of calling the hub.
func (m hubModel) handleSessionForceSteer() (tea.Model, tea.Cmd) {
	if m.sessionComposerMode() != hubComposerModeQueue {
		// Not in a queue-able state; nothing to do. Silently no-op so the
		// keybind doesn't fight with idle-state composing.
		return m, nil
	}
	if !m.detail.Capabilities.Steer {
		m.addSessionSystem("Force-steer is not available: source does not advertise steer.")
		return m, nil
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return m, nil
	}
	draft := m.session.input.Value()
	pending := strings.TrimSpace(draft)
	hasAttachments := len(m.pendingAttachments) > 0
	if pending == "" && len(m.sessionQueue) == 0 && !hasAttachments {
		m.addSessionSystem("Nothing to steer: the queue is empty.")
		return m, nil
	}
	if pending == "" && !hasAttachments {
		// Pure drain of the existing queue. Clear nothing on the composer.
		return m, sendHubDrainAsSteer(m.client, ref, "", "", nil, len(m.sessionQueue))
	}
	// Composer has text and/or attachments. sendHubDrainAsSteer sends the
	// payload on turn/drainAsSteer so the daemon folds it into the same
	// STEERING entry as everything already queued.
	if pending != "" {
		m.session.addHistory(pending)
	}
	m.session.resetInput()
	m.session.refreshViewport()
	attachments := m.snapshotPendingAttachmentsForSubmit()
	return m, sendHubDrainAsSteer(m.client, ref, pending, draft, attachments, len(m.sessionQueue))
}

func isQueuedDrainPartial(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.SerfErrorInfo == appwire.ErrorQueuedDrainPartial
	case map[string]any:
		return data["serfErrorInfo"] == string(appwire.ErrorQueuedDrainPartial)
	default:
		return false
	}
}

// isAltVKey reports whether the keypress is Alt+v / Ctrl+Alt+V. WSL
// terminals frequently swallow Ctrl+V on the Windows side, so we accept
// Alt+v as the equivalent shortcut for clipboard paste.
func isAltVKey(msg tea.KeyMsg) bool {
	if !msg.Alt {
		return false
	}
	if msg.Type != tea.KeyRunes {
		return false
	}
	if len(msg.Runes) != 1 {
		return false
	}
	r := msg.Runes[0]
	return r == 'v' || r == 'V'
}

// handleClipboardPaste reads an image from the system clipboard and
// pushes it onto pendingAttachments. On failure the user gets a
// system-message banner explaining the cause instead of a silent miss.
func (m hubModel) handleClipboardPaste() (tea.Model, tea.Cmd) {
	src := m.clipboardSource
	if src == nil {
		src = clipboard.NewSystemClipboardSource()
		m.clipboardSource = src
	}
	img, err := clipboard.PasteClipboardImage(src)
	if err != nil {
		m.addSessionSystem("Clipboard paste failed: " + err.Error())
		return m, nil
	}
	m.addPendingAttachment(img)
	return m, nil
}

// handleBracketedPaste inspects bracketed-paste payloads for the
// "single image path" shape. When the text resolves to an existing
// image file, the path is attached and the textarea is left alone;
// otherwise the caller falls through and the textarea receives the
// paste as normal text.
func (m *hubModel) handleBracketedPaste(text string) (tea.Cmd, bool) {
	resolved := clipboard.NormalizePastedPath(text)
	if resolved == "" {
		return nil, false
	}
	if !clipboard.IsImageFile(resolved) {
		return nil, false
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return nil, false
	}
	m.addPendingAttachment(&clipboard.PastedImage{
		Path:      resolved,
		MediaType: clipboard.MediaTypeForPath(resolved),
		Size:      int(info.Size()),
		Origin:    "path",
	})
	return nil, true
}
