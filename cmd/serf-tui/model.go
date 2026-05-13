package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/server"
)

type model struct {
	addr      string
	stateDir  string
	connected bool
	err       error
	width     int
	height    int

	// Session info (from SSE events)
	sessionModel      string
	sessionProfile    string
	sessionID         string
	turns             int
	contextTokens     int // input tokens from last ASSISTANT_TEXT_END
	contextWindowSize int // context window size from SESSION_START
	processing        bool
	turnInputTokens   int // input tokens accumulated since last USER_INPUT
	turnOutputTokens  int // output tokens accumulated since last USER_INPUT

	// UI components
	viewport viewport.Model
	input    textarea.Model
	messages []chatMessage

	// Input history
	history      []string // escaped entries (one per line)
	historyIdx   int      // -1 = not browsing; len(history) = back to draft
	historyDraft string   // saved current input when entering history browse

	// Track active tool calls by call ID -> index in messages
	activeTools       map[string]int
	lastInterrupt     time.Time
	lastSentText      string                // last user input, used for auto-steer on busy
	picker            *modelPicker          // non-nil when model picker is active
	transcriptPicker  *modelPicker          // non-nil when transcript picker is active
	themePicker       *themePicker          // non-nil when theme picker is active
	scrollMode        bool                  // true when scrolling history; input is blurred
	focusedToolIdx    int                   // index into messages of focused tool call in scroll mode; -1 = none
	transcriptView    *transcriptViewState  // non-nil when viewing a transcript instead of live chat
	observedSubagents map[string]subagentUI // subagents seen via SSE or /status

	// Embedded runtime lifecycle.
	configProvider string
	configModel    string
	embeddedCfg    embeddedConfig
	embedded       *embeddedServer
	asyncCh        chan tea.Msg
	streamCancel   context.CancelFunc
	streamID       int
	authBootstrap  bool

	authController  *authController
	authStatus      authStatus
	authStatusSeen  bool
	authLoginFlow   bool
	authFlowID      int
	authLoginCancel context.CancelFunc
	authRedirectCh  chan string
	showAuthStatus  bool
}

// applyInputTheme sets the textarea's style to match the active theme colours.
// Must be called after initTheme() and again whenever the theme changes.
func applyInputTheme(ta *textarea.Model) {
	base := lipgloss.NewStyle().
		Background(activeTheme.inputBg).
		Foreground(activeTheme.inputFg)
	textStyle := lipgloss.NewStyle().Foreground(activeTheme.inputFg)
	for _, s := range []*textarea.Style{&ta.FocusedStyle, &ta.BlurredStyle} {
		s.Base = base
		s.Text = textStyle
		s.CursorLine = base
	}
	// The cursor block uses Reverse(true) which swaps fg/bg. Set the cursor
	// Style foreground to inputFg so the reversed block is visible on the
	// themed background.
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(activeTheme.inputFg)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(activeTheme.inputFg)
}

func newModel(addr, stateDir string, initialMessages []chatMessage) model {
	return newConfiguredModel(addr, stateDir, initialMessages, embeddedConfig{}, nil, nil, false)
}

func newConfiguredModel(addr, stateDir string, initialMessages []chatMessage, cfg embeddedConfig, embedded *embeddedServer, asyncCh chan tea.Msg, authBootstrap bool) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.MaxHeight = 5
	ta.Focus()
	ta.CharLimit = 0
	// Disable the textarea's own newline binding — we handle enter ourselves.
	ta.KeyMap.InsertNewline.SetEnabled(false)
	applyInputTheme(&ta)

	configProvider := ""
	configModel := strings.TrimSpace(cfg.model)
	if provider, modelName, ok := strings.Cut(configModel, "/"); ok {
		configProvider = strings.ToLower(strings.TrimSpace(provider))
		configModel = strings.TrimSpace(modelName)
	}

	return model{
		addr:              addr,
		stateDir:          stateDir,
		input:             ta,
		messages:          initialMessages,
		activeTools:       make(map[string]int),
		history:           loadHistory(stateDir),
		historyIdx:        -1,
		observedSubagents: make(map[string]subagentUI),
		configProvider:    configProvider,
		configModel:       configModel,
		embeddedCfg:       cfg,
		embedded:          embedded,
		asyncCh:           asyncCh,
		authBootstrap:     authBootstrap,
		authController:    newAuthController(stateDir),
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.input.Focus()}
	if m.asyncCh != nil {
		cmds = append(cmds, waitForAsync(m.asyncCh))
	}
	if m.isOpenAIProvider() && m.authController != nil {
		cmds = append(cmds, loadAuthStatusCmd(m.authController, "openai"))
	}
	return tea.Batch(cmds...)
}

// toolIndices returns the indices in m.messages that are msgTool entries.
func (m *model) toolIndices() []int {
	var idx []int
	for i := range m.messages {
		if m.messages[i].Kind == msgTool && m.messages[i].Tool != nil && !m.messages[i].Tool.Hidden {
			idx = append(idx, i)
		}
	}
	return idx
}

// focusTool moves the focused tool to the previous (dir=-1) or next (dir=+1) tool.
func (m *model) focusTool(dir int) {
	indices := m.toolIndices()
	if len(indices) == 0 {
		return
	}
	if m.focusedToolIdx < 0 {
		// No focus yet; default to the last tool.
		m.focusedToolIdx = indices[len(indices)-1]
		return
	}
	// Find current position in the list.
	curPos := -1
	for i, idx := range indices {
		if idx == m.focusedToolIdx {
			curPos = i
			break
		}
	}
	if curPos < 0 {
		// Focused index is not in the list (e.g., hidden); reset to last.
		m.focusedToolIdx = indices[len(indices)-1]
		return
	}
	curPos += dir
	if curPos < 0 {
		curPos = 0
	} else if curPos >= len(indices) {
		curPos = len(indices) - 1
	}
	m.focusedToolIdx = indices[curPos]
}

// isToolFocused returns true if the given message index is the focused tool.
func (m *model) isToolFocused(msgIdx int) bool {
	return m.scrollMode && msgIdx == m.focusedToolIdx
}

// scrollToMessage scrolls the viewport so the rendered output for the given
// message index is visible, roughly centered in the viewport.
func (m *model) scrollToMessage(msgIdx int) {
	line := 0
	for i, msg := range m.messages {
		focused := m.isToolFocused(i)
		rendered := renderMessage(msg, m.width, focused)
		if rendered == "" {
			continue
		}
		if i == msgIdx {
			// Center the target in the viewport.
			target := line - m.viewport.Height/2
			if target < 0 {
				target = 0
			}
			m.viewport.SetYOffset(target)
			return
		}
		line += strings.Count(rendered, "\n") + 2 // +1 for the line itself, +1 for blank separator
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if wrapped, ok := msg.(asyncMsg); ok {
		if m.asyncCh != nil {
			cmds = append(cmds, waitForAsync(m.asyncCh))
		}
		msg = wrapped.msg
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.picker != nil {
			updated, cmd := m.picker.Update(msg)
			p := updated.(modelPicker)
			m.picker = &p
			if p.done {
				selected := p.selected
				m.picker = nil
				return m.handlePickerSelection(selected, cmd, cmds)
			}
			return m, cmd
		}

		if m.themePicker != nil {
			p, cmd := m.themePicker.Update(msg)
			m.themePicker = &p
			if p.done {
				m.themePicker = nil
				if p.selected != "" {
					setTheme(p.selected)
					initMarkdownRenderer(m.width)
					m.viewport.Style = viewportStyle
					applyInputTheme(&m.input)
					m.messages = append(m.messages, chatMessage{
						Kind: msgSystem,
						Text: fmt.Sprintf("Switched to %s theme.", p.selected),
					})
				}
				m.refreshViewport()
			}
			return m, cmd
		}

		if m.transcriptPicker != nil {
			updated, cmd := m.transcriptPicker.Update(msg)
			p := updated.(modelPicker)
			m.transcriptPicker = &p
			if p.done {
				selectedID := p.selected
				selectedTitle := pickerDisplay(p.items, selectedID)
				m.transcriptPicker = nil
				if selectedID != "" {
					cmd = m.enterTranscriptView(selectedID, selectedTitle)
				}
				m.refreshViewport()
			}
			return m, cmd
		}

		if m.transcriptView != nil {
			switch msg.String() {
			case "esc", "i", "q":
				m.exitTranscriptView()
			default:
				m.viewport, _ = m.viewport.Update(msg)
			}
			return m, nil
		}

		// In scroll mode, route keys to the viewport; esc/i/q return to input.
		if m.scrollMode {
			switch msg.String() {
			case "esc", "i", "q":
				m.scrollMode = false
				m.focusedToolIdx = -1
				m.input.Focus()
			case "up":
				m.focusTool(-1)
				m.refreshViewport()
				if m.focusedToolIdx >= 0 {
					m.scrollToMessage(m.focusedToolIdx)
				} else {
					m.viewport, _ = m.viewport.Update(msg)
				}
			case "down":
				m.focusTool(1)
				m.refreshViewport()
				if m.focusedToolIdx >= 0 {
					m.scrollToMessage(m.focusedToolIdx)
				} else {
					m.viewport, _ = m.viewport.Update(msg)
				}
			case "tab", "enter":
				// Toggle expand/collapse of the focused tool.
				if m.focusedToolIdx >= 0 && m.focusedToolIdx < len(m.messages) {
					msg := &m.messages[m.focusedToolIdx]
					if msg.Kind == msgTool && msg.Tool != nil && msg.Tool.Done {
						msg.Tool.Expanded = !msg.Tool.Expanded
						m.refreshViewport()
						m.scrollToMessage(m.focusedToolIdx)
					}
				}
			default:
				m.viewport, _ = m.viewport.Update(msg)
			}
			return m, nil
		}

		// alt+enter or ctrl+j inserts a newline without submitting.
		if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
			m.input.InsertString("\n")
			wantH := m.input.LineCount()
			if wantH < 1 {
				wantH = 1
			}
			if wantH > m.input.MaxHeight {
				wantH = m.input.MaxHeight
			}
			if wantH != m.input.Height() {
				m.input.SetHeight(wantH)
				m.viewport.Height = m.vpHeight()
			}
			return m, nil
		}

		switch msg.String() {
		case "esc":
			if m.authLoginFlow {
				m.cancelOpenAILogin("OpenAI login cancelled.")
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
		case "ctrl+c":
			now := time.Now()
			if now.Sub(m.lastInterrupt) < time.Second {
				m.cancelOpenAILogin("")
				return m, tea.Quit
			}
			m.lastInterrupt = now
			m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Interrupted. Press ctrl+c again to quit, or use /quit."})
			m.refreshViewport()
			return m, sendInterrupt(m.addr)
		case "up":
			if m.input.Line() == 0 && len(m.history) > 0 {
				if m.historyIdx == -1 {
					// Enter history mode: save current draft.
					m.historyDraft = m.input.Value()
					m.historyIdx = len(m.history) - 1
				} else if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.setInputValue(unescapeHistory(m.history[m.historyIdx]))
				return m, nil
			}
		case "down":
			if m.historyIdx >= 0 {
				if m.historyIdx < len(m.history)-1 {
					m.historyIdx++
					m.setInputValue(unescapeHistory(m.history[m.historyIdx]))
				} else {
					// Past end of history: restore draft.
					m.historyIdx = -1
					m.setInputValue(m.historyDraft)
					m.historyDraft = ""
				}
				return m, nil
			}
		case "pgup":
			m.scrollMode = true
			m.focusedToolIdx = -1
			m.input.Blur()
			m.viewport, _ = m.viewport.Update(msg)
			return m, nil
		case "tab":
			// Toggle the most recent tool call's expanded state (default behavior outside scroll mode).
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].Kind == msgTool && m.messages[i].Tool != nil && m.messages[i].Tool.Done {
					m.messages[i].Tool.Expanded = !m.messages[i].Tool.Expanded
					m.refreshViewport()
					break
				}
			}
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, tea.Batch(cmds...)
			}
			if m.authLoginFlow {
				if m.authRedirectCh != nil {
					select {
					case m.authRedirectCh <- text:
					default:
					}
				}
				m.resetInput()
				m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Finishing OpenAI login..."})
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			// Check for slash commands.
			if cmd, args := parseSlashCommand(text); cmd != "" {
				switch cmd {
				case "quit":
					m.cancelOpenAILogin("")
					return m, tea.Quit
				case "help":
					m.resetInput()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: slashCommandHelp()})
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				case "status":
					if m.authBootstrap && m.addr == "" {
						m.resetInput()
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "No session is running yet. Use /auth to sign in first."})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					m.resetInput()
					return m, fetchStatus(m.addr)
				case "tasks":
					if m.authBootstrap && m.addr == "" {
						m.resetInput()
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "No session is running yet. Use /auth to sign in first."})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					m.resetInput()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Fetching tasks..."})
					m.refreshViewport()
					cmds = append(cmds, fetchTasks(m.addr))
					return m, tea.Batch(cmds...)
				case "agents":
					if m.authBootstrap && m.addr == "" {
						m.resetInput()
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "No session is running yet. Use /auth to sign in first."})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					m.resetInput()
					cmds = append(cmds, fetchTranscriptTargets(m.addr))
					return m, tea.Batch(cmds...)
				case "model":
					if m.authBootstrap && m.addr == "" {
						m.resetInput()
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "No session is running yet. Use /auth to sign in first."})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					m.resetInput()
					if args == "" {
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Fetching available models..."})
						m.refreshViewport()
						cmds = append(cmds, fetchModels(m.addr))
						return m, tea.Batch(cmds...)
					}
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: fmt.Sprintf("Switching to model %s...", args)})
					m.refreshViewport()
					cmds = append(cmds, sendModel(m.addr, args))
					return m, tea.Batch(cmds...)
				case "theme":
					m.resetInput()
					p := newThemePicker()
					m.themePicker = &p
					return m, tea.Batch(cmds...)
				case "clear":
					if m.authBootstrap && m.addr == "" {
						m.resetInput()
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "No session is running yet. Use /auth to sign in first."})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					m.resetInput()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Starting new session..."})
					m.refreshViewport()
					cmds = append(cmds, sendClear(m.addr))
					return m, tea.Batch(cmds...)
				case "auth":
					m.resetInput()
					provider := m.currentProvider()
					if provider != "openai" {
						m.messages = append(m.messages, chatMessage{
							Kind: msgSystem,
							Text: formatAuthStatusSummary(authStatus{Provider: provider}),
						})
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					picker := newActionPicker("Auth", "↑/↓ navigate  enter select  esc cancel", []modelPickerItem{
						{id: "auth-login", display: "Sign in with OpenAI"},
						{id: "auth-status", display: "Show auth status"},
						{id: "auth-logout", display: "Sign out of OpenAI"},
					}, m.width)
					m.picker = &picker
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				case "compact":
					m.resetInput()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Compacting context..."})
					m.refreshViewport()
					cmds = append(cmds, sendCompact(m.addr))
					return m, tea.Batch(cmds...)
				default:
					m.resetInput()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: fmt.Sprintf("Unknown command: /%s", cmd)})
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				}
			}
			if m.authBootstrap && m.addr == "" && m.isOpenAIProvider() {
				m.resetInput()
				m.messages = append(m.messages, chatMessage{
					Kind: msgSystem,
					Text: "OpenAI login required. Use /auth to sign in before sending prompts.",
				})
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			m.messages = append(m.messages, chatMessage{Kind: msgUser, Text: text})
			m.lastSentText = text
			m.addHistory(text)
			m.resetInput()
			m.refreshViewport()
			cmds = append(cmds, sendInput(m.addr, text))
			return m, tea.Batch(cmds...)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		initMarkdownRenderer(m.width)
		m.viewport = viewport.New(m.width, m.vpHeight())
		m.viewport.YPosition = 0
		m.viewport.Style = viewportStyle
		m.input.SetWidth(m.width) // textarea accounts for prompt width internally
		// Paint textarea rows with the theme background and foreground.
		applyInputTheme(&m.input)
		m.refreshViewport()
		return m, nil

	case sseConnectedMsg:
		m.connected = true
		return m, tea.Batch(cmds...)

	case sseErrorMsg:
		m.connected = false
		m.err = msg.err
		return m, tea.Batch(cmds...)

	case sseEventMsg:
		m.handleSSEEvent(SSEEvent(msg))
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case inputSentMsg:
		if msg.err != nil {
			if strings.Contains(msg.err.Error(), "busy") && m.lastSentText != "" {
				text := m.lastSentText
				m.lastSentText = ""
				m.messages = append(m.messages, chatMessage{
					Kind: msgSystem,
					Text: "Steering...",
				})
				m.refreshViewport()
				return m, sendSteer(m.addr, text)
			}
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Error: %s", msg.err),
			})
			m.refreshViewport()
		}
		return m, nil

	case steerSentMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Steer failed: %s", msg.err),
			})
			m.refreshViewport()
		}
		return m, nil

	case statusResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Status error: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: renderDetailedStatus(msg.info, m.width),
			})
		}
		m.refreshViewport()
		return m, nil

	case tasksResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Tasks error: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: renderTasks(msg.tasks, m.width),
			})
		}
		m.refreshViewport()
		return m, nil

	case transcriptTargetsResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Could not fetch session transcripts: %s", msg.err),
			})
			m.refreshViewport()
			return m, nil
		}
		if m.sessionID == "" {
			m.sessionID = msg.info.SessionID
		}
		items := m.buildTranscriptPickerItems(msg.info)
		if len(items) == 0 {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "No session transcripts are available yet.",
			})
			m.refreshViewport()
			return m, nil
		}
		activeID := m.sessionID
		if m.transcriptView != nil {
			activeID = m.transcriptView.sessionID
		}
		picker := newTranscriptPicker(items, activeID, m.width)
		m.transcriptPicker = &picker
		m.refreshViewport()
		return m, nil

	case clearDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Clear failed: %s", msg.err),
			})
		} else {
			m.messages = nil
			m.activeTools = make(map[string]int)
			m.turns = 0
			m.transcriptPicker = nil
			m.transcriptView = nil
			m.observedSubagents = make(map[string]subagentUI)
			m.scrollMode = false
			m.focusedToolIdx = -1
			m.input.Focus()
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "New session started.",
			})
		}
		m.refreshViewport()
		return m, nil

	case modelDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Model switch failed: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Model updated.",
			})
		}
		m.refreshViewport()
		return m, nil

	case compactDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Compact failed: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Context compacted.",
			})
		}
		m.refreshViewport()
		return m, nil

	case modelsResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Could not fetch models: %s\nUse /model <name> to switch manually.", msg.err),
			})
			m.refreshViewport()
			return m, nil
		}
		if len(msg.models) == 0 {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "No models available from provider.",
			})
			m.refreshViewport()
			return m, nil
		}
		picker := newModelPicker(msg.models, m.sessionModel, m.width)
		m.picker = &picker
		// Remove the "Fetching..." message.
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Text == "Fetching available models..." {
			m.messages = m.messages[:len(m.messages)-1]
		}
		m.refreshViewport()
		return m, nil

	case transcriptRefreshMsg:
		if m.transcriptView == nil || m.transcriptView.root {
			return m, nil
		}
		m.refreshTranscriptViewState()
		m.refreshViewport()
		return m, scheduleTranscriptRefresh()

	case embeddedStartedMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("OpenAI session startup failed: %s", msg.err),
			})
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		if m.embedded != nil {
			if m.streamCancel != nil {
				m.streamCancel()
				m.streamCancel = nil
			}
			m.embedded.Close()
		}
		m.embedded = msg.server
		m.addr = msg.server.addr
		m.stateDir = msg.server.stateDir()
		m.connected = false
		m.err = nil
		m.authBootstrap = false
		cmds = append(cmds, m.startSSEStream())
		return m, tea.Batch(cmds...)

	case authStatusMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("OpenAI auth status failed: %s", msg.err),
			})
		} else {
			m.authStatus = msg.status
			m.authStatusSeen = true
			if m.showAuthStatus {
				m.messages = append(m.messages, chatMessage{
					Kind: msgSystem,
					Text: formatAuthStatusSummary(msg.status),
				})
			}
		}
		m.showAuthStatus = false
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case authURLMsg:
		if msg.flowID != m.authFlowID {
			return m, tea.Batch(cmds...)
		}
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: "OpenAI sign-in URL:\n" + msg.url,
		})
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case authBrowserErrorMsg:
		if msg.flowID != m.authFlowID {
			return m, tea.Batch(cmds...)
		}
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: fmt.Sprintf("Browser open failed: %s", msg.err),
		})
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case authLoginMsg:
		if msg.flowID != m.authFlowID {
			return m, tea.Batch(cmds...)
		}
		m.authLoginFlow = false
		m.authRedirectCh = nil
		m.authLoginCancel = nil
		m.resetInput()
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("OpenAI login failed: %s", msg.err),
			})
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		m.authStatus = msg.status
		m.authStatusSeen = true
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: fmt.Sprintf("OpenAI login complete. %s", formatAuthStatusSummary(msg.status)),
		})
		if m.authBootstrap && m.addr == "" && m.embedded == nil && m.isOpenAIProvider() {
			cmds = append(cmds, startEmbeddedCmd(m.embeddedCfg))
		} else if m.embedded != nil && m.isOpenAIProvider() && msg.status.ActiveSource != authopenai.AuthSourceEnv {
			cmds = append(cmds, startEmbeddedCmd(m.embeddedCfg))
		}
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case authLogoutMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("OpenAI logout failed: %s", msg.err),
			})
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		m.authStatus = msg.status
		m.authStatusSeen = true
		if msg.removed {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "OpenAI sign-out complete.",
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "OpenAI auth was already signed out.",
			})
		}
		if m.embedded != nil && m.isOpenAIProvider() && msg.status.ActiveSource == authopenai.AuthSourceSignedOut {
			if m.streamCancel != nil {
				m.streamCancel()
				m.streamCancel = nil
			}
			m.embedded.Close()
			m.embedded = nil
			m.addr = ""
			m.connected = false
			m.authBootstrap = true
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "OpenAI login required before sending more prompts. Use /auth to sign in again.",
			})
		}
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	// Update sub-components — only pass non-key messages to the viewport so its
	// built-in key bindings don't fire while the user is typing. Key events go
	// only to the input; the viewport is updated via scroll mode above.
	var cmd tea.Cmd
	if _, isKey := msg.(tea.KeyMsg); isKey {
		prevHeight := m.input.Height()
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		// Auto-grow the textarea up to MaxHeight based on line count.
		wantHeight := m.input.LineCount()
		if wantHeight < 1 {
			wantHeight = 1
		}
		if wantHeight > m.input.MaxHeight {
			wantHeight = m.input.MaxHeight
		}
		if wantHeight != prevHeight {
			m.input.SetHeight(wantHeight)
			m.viewport.Height = m.vpHeight()
		}
	} else {
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) handleSSEEvent(ev SSEEvent) {
	switch ev.Event {
	case "SESSION_START":
		var d struct {
			SessionID         string `json:"session_id"`
			Model             string `json:"model"`
			Profile           string `json:"profile"`
			Restored          bool   `json:"restored"`
			Turns             int    `json:"turns"`
			LastInputTokens   int    `json:"last_input_tokens"`
			ContextWindowSize int    `json:"context_window_size"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		m.sessionID = d.SessionID
		m.sessionModel = d.Model
		prevProfile := m.sessionProfile
		m.sessionProfile = d.Profile
		if d.ContextWindowSize > 0 {
			m.contextWindowSize = d.ContextWindowSize
		}
		if d.Restored {
			m.turns = d.Turns
			m.contextTokens = d.LastInputTokens
		}
		if prevProfile != "openai" && m.sessionProfile == "openai" && m.authController != nil && m.asyncCh != nil {
			go func() {
				m.asyncCh <- loadAuthStatusCmd(m.authController, "openai")()
			}()
		}

	case "USER_INPUT", "STEERING_INJECTED":
		m.processing = true
		m.turnInputTokens = 0
		m.turnOutputTokens = 0
		var d struct {
			Text string `json:"text"`
			Turn int    `json:"turn"`
		}
		if ev.Event == "USER_INPUT" && json.Unmarshal([]byte(ev.Data), &d) == nil && strings.TrimSpace(d.Text) != "" {
			if len(m.messages) == 0 || m.messages[len(m.messages)-1].Kind != msgUser || m.messages[len(m.messages)-1].Text != d.Text {
				m.messages = append(m.messages, chatMessage{Kind: msgUser, Text: d.Text, TurnIndex: d.Turn})
			}
		}

	case "SESSION_END":
		m.processing = false

	case "COMMUNICATE":
		var d struct {
			Message string `json:"message"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if d.Message != "" {
			if len(m.messages) > 0 &&
				m.messages[len(m.messages)-1].Kind == msgAssistant &&
				strings.TrimSpace(m.messages[len(m.messages)-1].Text) == strings.TrimSpace(d.Message) {
				m.messages[len(m.messages)-1].Kind = msgCommunicate
				break
			}
			m.messages = append(m.messages, chatMessage{Kind: msgCommunicate, Text: d.Message})
		}

	case "ASSISTANT_TEXT_END":
		m.turns++
		var d struct {
			Text  string `json:"text"`
			Usage *struct {
				InputTokens  int  `json:"input_tokens"`
				OutputTokens int  `json:"output_tokens"`
				CacheRead    *int `json:"cache_read_tokens"`
				CacheWrite   *int `json:"cache_write_tokens"`
				CacheWrite1h *int `json:"cache_write_1h_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &d); err == nil && d.Usage != nil {
			u := d.Usage
			total := u.InputTokens
			if u.CacheRead != nil {
				total += *u.CacheRead
			}
			if u.CacheWrite != nil {
				total += *u.CacheWrite
			}
			if u.CacheWrite1h != nil {
				total += *u.CacheWrite1h
			}
			m.contextTokens = total
			m.turnInputTokens += total
			m.turnOutputTokens += u.OutputTokens
		}
		if strings.TrimSpace(d.Text) != "" {
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Kind == msgAssistant {
				if m.messages[len(m.messages)-1].Text == "" {
					m.messages[len(m.messages)-1].Text = d.Text
				}
			} else {
				m.messages = append(m.messages, chatMessage{Kind: msgAssistant, Text: d.Text})
			}
		}

	case "ASSISTANT_TEXT_DELTA":
		var d struct {
			Delta string `json:"delta"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		// Append to current assistant message or create new one
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Kind == msgAssistant {
			m.messages[len(m.messages)-1].Text += d.Delta
		} else {
			m.messages = append(m.messages, chatMessage{Kind: msgAssistant, Text: d.Delta})
		}

	case "TOOL_CALL_START":
		var d struct {
			CallID        string `json:"call_id"`
			ToolName      string `json:"tool_name"`
			ArgumentsJSON string `json:"arguments_json"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		toolDesc, toolDetail := summarizeTool(d.ToolName, d.ArgumentsJSON)
		idx := len(m.messages)
		m.messages = append(m.messages, chatMessage{
			Kind: msgTool,
			Tool: &toolCallInfo{
				Name:        d.ToolName,
				Description: toolDesc,
				Detail:      toolDetail,
				Hidden:      d.ToolName == "communicate",
			},
		})
		m.activeTools[d.CallID] = idx

	case "TOOL_CALL_OUTPUT_DELTA":
		var d struct {
			CallID string `json:"call_id"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if idx, ok := m.activeTools[d.CallID]; ok && idx < len(m.messages) {
			if m.messages[idx].Tool != nil {
				m.messages[idx].Tool.Output += d.Delta
			}
		}

	case "TOOL_CALL_END":
		var d struct {
			CallID     string `json:"call_id"`
			ToolName   string `json:"tool_name"`
			Error      string `json:"error"`
			Result     string `json:"result"`
			Output     string `json:"output"`
			DurationMS int64  `json:"duration_ms"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if idx, ok := m.activeTools[d.CallID]; ok && idx < len(m.messages) {
			tc := m.messages[idx].Tool
			if tc != nil {
				tc.Done = true
				tc.Duration = time.Duration(d.DurationMS) * time.Millisecond
				tc.Error = d.Error
				if tc.Output == "" {
					tc.Output = d.Result
					if tc.Output == "" {
						tc.Output = d.Output
					}
				}
				// Auto-expand: expand if Detail exists, or if output is short.
				if tc.Detail != "" {
					tc.Expanded = true
				} else {
					lines := strings.Count(tc.Output, "\n") + 1
					tc.Expanded = lines <= toolCollapseThreshold
				}
			}
		}
		delete(m.activeTools, d.CallID)

	case "SUBAGENT_START":
		var d struct {
			AgentID string `json:"agent_id"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if d.AgentID != "" {
			m.trackSubagent(server.SubagentStatusInfo{
				ID:     d.AgentID,
				Status: "running",
			})
		}

	case "SUBAGENT_END":
		var d struct {
			AgentID   string `json:"agent_id"`
			Status    string `json:"status"`
			TurnsUsed int    `json:"turns_used"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if d.AgentID != "" {
			m.trackSubagent(server.SubagentStatusInfo{
				ID:        d.AgentID,
				Status:    d.Status,
				TurnsUsed: d.TurnsUsed,
			})
		}
	}
}

// resetInput clears the input and shrinks it back to one line.
func (m *model) resetInput() {
	m.input.Reset()
	m.input.Placeholder = "Type a message..."
	m.input.SetHeight(1)
	m.viewport.Height = m.vpHeight()
	m.historyIdx = -1
	m.historyDraft = ""
}

// setInputValue replaces the textarea contents and adjusts its height.
func (m *model) setInputValue(s string) {
	m.input.Reset()
	m.input.SetValue(s)
	wantH := m.input.LineCount()
	if wantH < 1 {
		wantH = 1
	}
	if wantH > m.input.MaxHeight {
		wantH = m.input.MaxHeight
	}
	m.input.SetHeight(wantH)
	m.viewport.Height = m.vpHeight()
}

// addHistory appends text to the in-memory history and persists it to disk.
func (m *model) addHistory(text string) {
	escaped := strings.ReplaceAll(text, "\n", `\n`)
	m.history = append(m.history, escaped)
	if len(m.history) > maxHistoryEntries {
		m.history = m.history[len(m.history)-maxHistoryEntries:]
	}
	appendHistory(m.stateDir, text)
}

// and input dimensions. statusBar=1, border=1, textarea rows=input.Height().
func (m model) vpHeight() int {
	h := m.height - 1 - 1 - m.input.Height()
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) refreshViewport() {
	// Resize viewport if input height changed (textarea grew or shrank).
	if newH := m.vpHeight(); newH != m.viewport.Height {
		m.viewport.Height = newH
	}
	var lines []string
	if m.transcriptView != nil {
		lines = append(lines, systemStyle.Width(m.width).Render(m.transcriptView.banner()), "")
		if m.transcriptView.readErr != "" {
			lines = append(lines, systemStyle.Width(m.width).Render(m.transcriptView.readErr), "")
		}
	}
	currentMessages := m.visibleMessages()
	for i, msg := range currentMessages {
		focused := m.isToolFocused(i)
		rendered := renderMessage(msg, m.width, focused)
		if rendered != "" {
			lines = append(lines, rendered, "") // blank line between messages
		}
	}
	if m.transcriptView != nil && len(currentMessages) == 0 && m.transcriptView.readErr == "" {
		lines = append(lines, systemStyle.Width(m.width).Render("Transcript is empty so far."), "")
	}
	content := strings.Join(lines, "\n")
	m.viewport.SetContent(content)
	if !m.scrollMode {
		m.viewport.GotoBottom()
	}
}

func (m *model) isOpenAIProvider() bool {
	return m.currentProvider() == "openai"
}

func (m *model) currentProvider() string {
	provider := strings.TrimSpace(m.sessionProfile)
	if provider == "" {
		provider = strings.TrimSpace(m.configProvider)
	}
	return provider
}

func (m *model) activeModelName() string {
	if strings.TrimSpace(m.sessionModel) != "" {
		return m.sessionModel
	}
	return strings.TrimSpace(m.configModel)
}

func (m *model) cancelOpenAILogin(message string) {
	if m.authLoginCancel != nil {
		m.authLoginCancel()
		m.authLoginCancel = nil
	}
	m.authLoginFlow = false
	m.authFlowID++
	m.authRedirectCh = nil
	m.resetInput()
	if message != "" {
		m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: message})
	}
}

func (m model) handlePickerSelection(selected string, cmd tea.Cmd, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch selected {
	case "":
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	case "auth-login":
		if m.processing || m.authLoginFlow {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Wait for the current session to go idle before changing OpenAI auth.",
			})
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.authLoginFlow = true
		m.authFlowID++
		m.authLoginCancel = cancel
		m.authRedirectCh = make(chan string, 1)
		m.input.Placeholder = "Paste the full OpenAI redirect URL..."
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: "Opening OpenAI sign-in. The URL will also be shown here for manual copy/paste.",
		})
		m.refreshViewport()
		cmds = append(cmds, authLoginCmd(ctx, m.authFlowID, m.authController, "openai", m.asyncCh, openURLInBrowser, m.authRedirectCh))
		return m, tea.Batch(cmds...)
	case "auth-status":
		m.showAuthStatus = true
		cmds = append(cmds, loadAuthStatusCmd(m.authController, "openai"))
		return m, tea.Batch(cmds...)
	case "auth-logout":
		if m.processing || m.authLoginFlow {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Wait for the current session to go idle before changing OpenAI auth.",
			})
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		cmds = append(cmds, authLogoutCmd(m.authController, "openai"))
		return m, tea.Batch(cmds...)
	default:
		if selected != "" && selected != m.sessionModel {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Switching to model %s...", selected),
			})
			m.refreshViewport()
			if m.addr != "" {
				return m, sendModel(m.addr, selected)
			}
		}
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	bgStyle := lipgloss.NewStyle().
		Background(activeTheme.viewportBg).
		Width(m.width).
		Height(m.height)

	authHintText := ""
	if m.isOpenAIProvider() && m.authStatusSeen {
		authHintText = authHint(m.authStatus)
	}
	statusBar := renderStatusBar(m.connected, m.activeModelName(), m.sessionID, authHintText, m.turns, m.contextTokens, m.contextWindowSize, m.processing, m.turnInputTokens, m.turnOutputTokens, m.scrollMode, m.width)

	var body string
	if m.picker != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			m.picker.View(),
		)
	} else if m.transcriptPicker != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			m.transcriptPicker.View(),
		)
	} else if m.themePicker != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			m.themePicker.View(),
		)
	} else if m.transcriptView != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
		)
	} else {
		inputView := inputBorderStyle.Width(m.width).Render(m.input.View())
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			inputView,
		)
	}

	return bgStyle.Render(body)
}

// renderDetailedStatus formats a StatusInfo into a multi-panel text display.
// All sections are shown (with counts) even when empty.
// Tool lists wrap at width to avoid horizontal scrolling.
func renderDetailedStatus(info server.StatusInfo, width int) string {
	if width <= 0 {
		width = 80
	}

	var b strings.Builder

	// Header section (always shown).
	pressure := fmt.Sprintf("%.0f%%", info.ContextPressure*100)
	b.WriteString(fmt.Sprintf("Session:  %s\nModel:    %s (%s)\nTurns:    %d\nContext:  %s used",
		info.SessionID, info.Model, info.Profile, info.Turns, pressure))

	ds := info.Detailed
	if ds == nil {
		return b.String()
	}

	// Tools section — group by source.
	core := []string{}
	mcp := map[string][]string{} // server name → tool names
	custom := []string{}

	for _, t := range ds.Tools {
		switch {
		case t.Source == "core":
			core = append(core, t.Name)
		case strings.HasPrefix(t.Source, "mcp:"):
			srv := t.Source[4:]
			mcp[srv] = append(mcp[srv], t.Name)
		default:
			custom = append(custom, t.Name)
		}
	}

	b.WriteString(fmt.Sprintf("\n\nTools (%d):", len(ds.Tools)))
	if len(core) > 0 {
		writeWrappedList(&b, "Core:", core, width)
	}
	for srv, tools := range mcp {
		writeWrappedList(&b, fmt.Sprintf("MCP [%s]:", srv), tools, width)
	}
	if len(custom) > 0 {
		writeWrappedList(&b, "Custom:", custom, width)
	}

	// MCP Servers section.
	b.WriteString(fmt.Sprintf("\n\nMCP Servers (%d):", len(ds.MCP)))
	for _, srv := range ds.MCP {
		b.WriteString(fmt.Sprintf("\n  %s (%d tools)", srv.Name, len(srv.Tools)))
	}

	// Skills section.
	b.WriteString(fmt.Sprintf("\n\nSkills (%d):", len(ds.Skills)))
	for _, skill := range ds.Skills {
		b.WriteString(fmt.Sprintf("\n  %s", skill.Name))
	}

	// Plugins section.
	b.WriteString(fmt.Sprintf("\n\nPlugins (%d):", len(ds.Plugins)))
	for _, p := range ds.Plugins {
		version := p.Version
		if version == "" {
			version = "?"
		}
		b.WriteString(fmt.Sprintf("\n  %s v%s (%d skills, %d agents, %d hooks)",
			p.Name, version, p.SkillCount, p.AgentCount, p.HookCount))
	}

	// Hooks section.
	b.WriteString(fmt.Sprintf("\n\nHooks (%d):", len(ds.Hooks)))
	if len(ds.Hooks) > 0 {
		parts := []string{}
		for event, count := range ds.Hooks {
			parts = append(parts, fmt.Sprintf("%s: %d", event, count))
		}
		sort.Strings(parts)
		b.WriteString("\n  " + strings.Join(parts, "  "))
	}

	// Subagents section.
	b.WriteString(fmt.Sprintf("\n\nSubagents (%d):", len(ds.Subagents)))
	for _, sub := range ds.Subagents {
		b.WriteString(fmt.Sprintf("\n  %s (%s, %d turns)", sub.ID, sub.Status, sub.TurnsUsed))
	}

	// Plugin agents section.
	b.WriteString(fmt.Sprintf("\n\nAgents (%d):", len(ds.Agents)))
	for _, name := range ds.Agents {
		b.WriteString(fmt.Sprintf("\n  %s", name))
	}

	return b.String()
}

// writeWrappedList writes a labeled comma-separated list that wraps at width.
// Output format: "\n  Label: item1, item2,\n         item3, item4"
func writeWrappedList(b *strings.Builder, label string, items []string, width int) {
	prefix := "  " + label + " "
	indent := strings.Repeat(" ", len(prefix))

	b.WriteString("\n" + prefix)
	col := len(prefix)
	for i, item := range items {
		entry := item
		if i < len(items)-1 {
			entry += ","
		}
		needed := len(entry)
		if i > 0 {
			needed++ // space before item
		}
		if col+needed > width && col > len(prefix) {
			b.WriteString("\n" + indent)
			col = len(indent)
		} else if i > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(entry)
		col += len(entry)
	}
}

// renderTasks formats a list of agent tasks for display.
func renderTasks(tasks []agent.Task, width int) string {
	if width <= 0 {
		width = 80
	}
	if len(tasks) == 0 {
		return "No tasks."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tasks (%d):\n", len(tasks)))

	statusIcon := map[agent.TaskStatus]string{
		agent.TaskOpen:       "○",
		agent.TaskInProgress: "◐",
		agent.TaskDone:       "●",
		agent.TaskCancelled:  "✗",
	}

	for _, t := range tasks {
		icon := statusIcon[t.Status]
		if icon == "" {
			icon = "?"
		}
		b.WriteString(fmt.Sprintf("  %s [%d] %s — %s", icon, t.ID, t.Type, t.Description))
		if len(t.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf(" (depends on: %v)", t.DependsOn))
		}
		if t.ReasoningEffort != "" {
			b.WriteString(fmt.Sprintf(" [%s]", t.ReasoningEffort))
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}
