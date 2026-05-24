package main

import (
	"strings"
	"time"
)

type hubTUISampleCorpus struct {
	DashboardTree    hubTreeResponse
	ProjectHistory   hubTreeProject
	Sessions         map[string]hubSessionDetail
	TranscriptEvents []chatMessage
	SpawnOptions     []tuiSpawnSample
	AuthStates       []tuiAuthSample
	PickerStates     []tuiPickerSample
	Diagnostics      []tuiNoticeSample
	Renders          []tuiSampleRender
	Interactions     []tuiInteractionSample
}

type tuiSpawnSample struct {
	Name        string
	Harness     string
	HarnessKind string
	Model       string
	WorkingDir  string
	AuthReason  string
}

type tuiAuthSample struct {
	Name   string
	Source string
	State  string
	Reason string
}

type tuiPickerSample struct {
	Name     string
	Title    string
	Items    []modelPickerItem
	Disabled []string
	Error    string
}

type tuiNoticeSample struct {
	Name       string
	Kind       string
	Summary    string
	Cause      string
	NextAction string
	Source     string
}

type tuiSampleRender struct {
	Name     string
	Width    int
	View     string
	Contains []string
	Theme    string // "dark" or "light"; empty defaults to dark
}

type tuiInteractionSample struct {
	Name     string
	Keys     []string
	Expected string
}

// newHubTUISampleCorpus keeps design-system examples in typed fixtures so
// widget tests can assert against the same scenarios across refactors.
func newHubTUISampleCorpus() hubTUISampleCorpus {
	tree := sampleDashboardTree()
	project := tree.Projects[0]
	sessions := sampleSessionDetails()
	transcript := sampleTranscriptEvents()
	diagnostics := sampleDiagnostics()
	authStates := sampleAuthStates()
	pickers := samplePickerStates()
	spawn := sampleSpawnOptions()

	return hubTUISampleCorpus{
		DashboardTree:    tree,
		ProjectHistory:   project,
		Sessions:         sessions,
		TranscriptEvents: transcript,
		SpawnOptions:     spawn,
		AuthStates:       authStates,
		PickerStates:     pickers,
		Diagnostics:      diagnostics,
		Renders:          sampleRenders(),
		Interactions:     sampleInteractions(),
	}
}

func sampleDashboardTree() hubTreeResponse {
	serfLive := hubTreeNode{
		Ref:         "local:01SERF",
		SessionID:   "01SERF",
		SourceLabel: "serf",
		Title:       "Restore hub TUI widgets",
		Project:     "serf",
		State:       "idle",
		Model:       "openai/gpt-5.5",
		Age:         "now",
		Live:        true,
	}
	serfBusy := hubTreeNode{
		Ref:         "local:01BUSY",
		SessionID:   "01BUSY",
		SourceLabel: "serf",
		Title:       "Stream markdown without flicker",
		Project:     "serf",
		State:       "active",
		Model:       "openai/gpt-5.5",
		Age:         "4m",
		Live:        true,
	}
	serfEnded := hubTreeNode{
		Ref:         "local:01ENDED",
		SessionID:   "01ENDED",
		SourceLabel: "serf",
		Title:       "Document protocol adoption",
		Project:     "serf",
		State:       "ended",
		Model:       "openai/gpt-5.4",
		Age:         "1h",
		Live:        false,
	}
	codexLive := hubTreeNode{
		Ref:         "codex-local:01CODEX",
		SessionID:   "01CODEX",
		SourceLabel: "codex-local",
		Title:       "Codex app-server smoke",
		Project:     "codex-src",
		State:       "idle",
		Model:       "gpt-5.3-codex",
		Age:         "9m",
		Live:        true,
	}
	return hubTreeResponse{
		Live: []hubTreeNode{serfLive, serfBusy, codexLive},
		Projects: []hubTreeProject{
			{
				Key:         "serf",
				Name:        "serf",
				WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf",
				RollupState: "active",
				Sessions:    []hubTreeNode{serfLive, serfBusy, serfEnded},
			},
			{
				Key:         "codex-src",
				Name:        "codex-src",
				WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf/inspo/codex",
				RollupState: "idle",
				Sessions:    []hubTreeNode{codexLive},
			},
		},
	}
}

func sampleSessionDetails() map[string]hubSessionDetail {
	return map[string]hubSessionDetail{
		"serf-idle": {
			Ref:         "local:01SERF",
			SessionID:   "01SERF",
			SourceLabel: "serf",
			Title:       "Restore hub TUI widgets",
			State:       "idle",
			Model:       "openai/gpt-5.5",
			WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf",
			Project:     "serf",
			TurnCount:   3,
			Live:        true,
			Capabilities: hubSessionCapabilities{
				Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true, Fork: true, Resume: true, Shutdown: true, ChangeModel: true,
			},
		},
		"codex-readonly": {
			Ref:         "codex-local:01CODEX",
			SessionID:   "01CODEX",
			SourceLabel: "codex-local",
			Title:       "Codex app-server smoke",
			State:       "idle",
			Model:       "gpt-5.3-codex",
			WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf/inspo/codex",
			Project:     "codex-src",
			TurnCount:   1,
			Live:        true,
			Capabilities: hubSessionCapabilities{
				Send: true, Resume: true,
			},
		},
		"busy-steer": {
			Ref:         "local:01BUSY",
			SessionID:   "01BUSY",
			SourceLabel: "serf",
			Title:       "Stream markdown without flicker",
			State:       "active",
			Model:       "openai/gpt-5.5",
			WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf",
			Project:     "serf",
			Live:        true,
			Capabilities: hubSessionCapabilities{
				Steer: true, Interrupt: true, Queue: true,
			},
		},
		"busy-readonly": {
			Ref:         "codex-local:01BUSY",
			SessionID:   "01BUSYCODEX",
			SourceLabel: "codex-local",
			Title:       "Codex busy read-only sample",
			State:       "active",
			Model:       "gpt-5.3-codex",
			Project:     "codex-src",
			Live:        true,
			Capabilities: hubSessionCapabilities{
				Interrupt: true,
			},
		},
		"ended": {
			Ref:         "local:01ENDED",
			SessionID:   "01ENDED",
			SourceLabel: "serf",
			Title:       "Document protocol adoption",
			State:       "ended",
			Model:       "openai/gpt-5.4",
			Project:     "serf",
			Live:        false,
			Capabilities: hubSessionCapabilities{
				Resume: true,
			},
		},
	}
}

func sampleTranscriptEvents() []chatMessage {
	return []chatMessage{
		{Kind: msgUser, Text: "What agent harness is running right now?", TurnIndex: 0},
		{Kind: msgAssistant, Text: "The running agent harness identifies itself as serf."},
		{Kind: msgTool, Tool: &toolCallInfo{
			Name:        "tasks",
			Description: "Understand task -> Do the work -> Verify",
			Output:      "all task steps completed",
			Duration:    2 * time.Second,
			Done:        true,
			Expanded:    true,
		}},
		{Kind: msgSystem, Text: "Model change is not available for this session."},
	}
}

func sampleDiagnostics() []tuiNoticeSample {
	return []tuiNoticeSample{
		{
			Name:       "launch-failed",
			Kind:       "error",
			Summary:    "Spawn failed: model provider is not reported by the Serf launch harness",
			Cause:      "selected provider openai was not present in harness discovery",
			NextAction: "refresh spawn options or choose a reported harness model",
			Source:     "serf",
		},
		{
			Name:       "action-unavailable",
			Kind:       "unavailable",
			Summary:    "Clear is not available for Codex app-server sessions",
			Cause:      "codex-local did not advertise thread/clear",
			NextAction: "open /help to see source-supported actions",
			Source:     "codex-local",
		},
	}
}

func sampleAuthStates() []tuiAuthSample {
	return []tuiAuthSample{
		{Name: "env-key", Source: "OPENAI_API_KEY", State: "ready"},
		{Name: "signed-out", Source: "serf-oauth", State: "login required", Reason: "no stored token"},
		{Name: "signed-in", Source: "serf-oauth", State: "ready"},
		{Name: "expired-refreshable", Source: "serf-oauth", State: "refreshable"},
		{Name: "refresh-failed", Source: "serf-oauth", State: "login required", Reason: "refresh token rejected"},
		{Name: "remote-pasteback", Source: "serf-oauth", State: "waiting for pasted redirect URL"},
	}
}

func samplePickerStates() []tuiPickerSample {
	return []tuiPickerSample{
		{Name: "loading", Title: "Select model"},
		{Name: "populated", Title: "Select model", Items: []modelPickerItem{{id: "openai/gpt-5.5", display: "openai/gpt-5.5"}, {id: "openai/gpt-5.4", display: "openai/gpt-5.4"}}},
		{Name: "filtered-empty", Title: "Select model", Items: []modelPickerItem{{id: "openai/gpt-5.5", display: "openai/gpt-5.5"}}},
		{Name: "disabled-row", Title: "Select model", Items: []modelPickerItem{{id: "openai/gpt-4.1", display: "openai/gpt-4.1"}}, Disabled: []string{"openai/gpt-4.1: login required"}},
		{Name: "fetch-error", Title: "Select model", Error: "provider listing failed"},
	}
}

func sampleSpawnOptions() []tuiSpawnSample {
	return []tuiSpawnSample{
		{Name: "serf-openai", Harness: "serf", HarnessKind: "serf", Model: "openai/gpt-5.5", WorkingDir: "/repo/serf"},
		{Name: "codex-local", Harness: "codex-local", HarnessKind: "codex", Model: "gpt-5.3-codex", WorkingDir: "/repo/serf/inspo/codex"},
		{Name: "auth-required", Harness: "serf", HarnessKind: "serf", Model: "openai/gpt-4.1", WorkingDir: "/repo/serf", AuthReason: "OpenAI login required"},
		{Name: "launch-error", Harness: "serf", HarnessKind: "serf", Model: "openai/gpt-5.5", WorkingDir: "/repo/serf", AuthReason: "harness did not report provider openai"},
	}
}

func sampleRenders() []tuiSampleRender {
	specs := []struct {
		name     string
		width    int
		contains []string
	}{
		{name: "dashboard-narrow", width: 60, contains: []string{"serf live", "n new", "/ palette"}},
		{name: "dashboard-normal", width: 100, contains: []string{"codex-local", "Restore hub TUI widgets"}},
		{name: "dashboard-wide", width: 140, contains: []string{"serf live", "Codex app-server smoke"}},
		{name: "session-idle", width: 100, contains: []string{"message", "draft stays visible"}},
		{name: "session-streaming", width: 100, contains: []string{"The running agent harness", "all task steps completed"}},
		{name: "session-busy-steer", width: 100, contains: []string{"queue", "ctrl+s: send as steer", "Please also check"}},
		{name: "session-busy-readonly", width: 100, contains: []string{"read-only", "source does not advertise queue"}},
		{name: "session-browse", width: 100, contains: []string{"esc/i/q: compose", "f: fork"}},
		{name: "session-fork", width: 100, contains: []string{"fork draft", "edited prompt"}},
		{name: "spawn-serf", width: 100, contains: []string{"Harness:  serf", "openai/gpt-5.5"}},
		{name: "spawn-codex", width: 100, contains: []string{"Harness:  codex-local", "codex-local/gpt-5.3-codex"}},
		{name: "spawn-auth-required", width: 100, contains: []string{"OpenAI login required", "openai/gpt-4.1"}},
		{name: "model-picker", width: 100, contains: []string{"Select model", "openai/gpt-5.5"}},
		{name: "theme-picker", width: 80, contains: []string{"Select theme", "dark", "light"}},
		{name: "auth-overlay", width: 100, contains: []string{"OpenAI", "Serf-owned"}},
		{name: "agents-picker", width: 100, contains: []string{"Select transcript", "main session"}},
		{name: "help-overlay", width: 100, contains: []string{"Available commands", "/model"}},
		{name: "diagnostics", width: 100, contains: []string{"Spawn failed", "Action unavailable"}},
		{name: "appshell-normal", width: 100, contains: []string{"serf live", "Live now", "ctrl+o dashboard"}},
		{name: "appshell-loading", width: 100, contains: []string{"serf live", "Loading hub dashboard"}},
		{name: "appshell-error", width: 100, contains: []string{"Hub unavailable", "Retry"}},
		{name: "topbar-session", width: 80, contains: []string{"serf / session / Restore hub TUI widgets"}},
		{name: "actionbar-normal", width: 80, contains: []string{"enter open", "ctrl+o dashboard"}},
		{name: "actionbar-wrapped", width: 28, contains: []string{"enter open", "ctrl+o dashboard"}},
		{name: "picker-empty", width: 80, contains: []string{"No matching items"}},
		{name: "picker-disabled", width: 80, contains: []string{"disabled: source does not advertise clear"}},
		{name: "picker-error", width: 80, contains: []string{"Provider unavailable", "provider listing failed"}},
	}

	out := make([]tuiSampleRender, 0, len(specs))
	for _, spec := range specs {
		render, ok := sampleRenderFromRealWidget(spec.name, spec.width)
		if !ok {
			continue
		}
		render.Contains = spec.contains
		out = append(out, render)
	}
	return out
}

func sampleInteractions() []tuiInteractionSample {
	return []tuiInteractionSample{
		{Name: "prompt-owns-printable-shortcuts", Keys: []string{"m", "h"}, Expected: "focused prompt value becomes mh"},
		{Name: "picker-owns-filter-navigation", Keys: []string{"/", "g", "p", "t", "down", "enter"}, Expected: "picker filter is gpt and selected row is chosen"},
		{Name: "composer-draft-survives-overlay", Keys: []string{"type draft", "/model", "esc"}, Expected: "composer still contains draft"},
		{Name: "busy-enter-queues-message", Keys: []string{"enter"}, Expected: "busy enter enqueues the composer text via turn/queue and clears the draft"},
		{Name: "busy-ctrl-s-drains-as-steer", Keys: []string{"ctrl+s"}, Expected: "ctrl+s drains the queue (plus any composer text) as a single STEERING message"},
		{Name: "unsupported-codex-actions-hidden-or-disabled", Keys: []string{"/help"}, Expected: "Codex clear/shutdown actions are absent or disabled with reasons"},
	}
}

func renderSample(name string, width int, view string, contains ...string) tuiSampleRender {
	return tuiSampleRender{
		Name:     name,
		Width:    width,
		View:     strings.TrimSpace(view),
		Contains: contains,
		Theme:    "dark",
	}
}

func sampleRenderFromRealWidget(name string, width int) (tuiSampleRender, bool) {
	switch name {
	case "dashboard-narrow", "dashboard-normal", "dashboard-wide":
		m := sampleHubModel(width)
		m.mode = hubModeDashboard
		return renderSample(name, width, m.dashboardView()), true
	case "session-idle":
		m := sampleSessionModel(width, sampleSessionDetails()["serf-idle"])
		m.session.setInputValue("draft stays visible")
		return renderSample(name, width, m.sessionView()), true
	case "session-streaming":
		m := sampleSessionModel(width, sampleSessionDetails()["serf-idle"])
		m.session.messages = sampleTranscriptEvents()
		m.session.refreshViewport()
		return renderSample(name, width, m.sessionView()), true
	case "session-busy-steer":
		m := sampleSessionModel(width, sampleSessionDetails()["busy-steer"])
		m.detail.ActiveTurnID = "turn_busy"
		m.session.processing = true
		m.session.setInputValue("Please also check old TUI command parity")
		return renderSample(name, width, m.sessionView()), true
	case "session-busy-readonly":
		m := sampleSessionModel(width, sampleSessionDetails()["busy-readonly"])
		m.session.processing = true
		m.session.setInputValue("draft kept")
		return renderSample(name, width, m.sessionView()), true
	case "session-browse":
		m := sampleSessionModel(width, sampleSessionDetails()["serf-idle"])
		m.session.messages = sampleTranscriptEvents()
		m.session.scrollMode = true
		m.browseSelected = 0
		m.session.refreshViewport()
		return renderSample(name, width, m.sessionView()), true
	case "session-fork":
		m := sampleSessionModel(width, sampleSessionDetails()["serf-idle"])
		m.forkDraft = &hubForkDraft{Turn: 1, OriginalText: "original before fork", Label: "original before fork"}
		m.session.setInputValue("edited prompt")
		return renderSample(name, width, m.sessionView()), true
	case "spawn-serf":
		m := sampleSpawnModel(width, sampleSpawnOptions()[0])
		m.session.setInputValue("Implement the next TUI task")
		return renderSample(name, width, m.spawnView()), true
	case "spawn-codex":
		m := sampleSpawnModel(width, sampleSpawnOptions()[1])
		return renderSample(name, width, m.spawnView()), true
	case "spawn-auth-required":
		m := sampleSpawnModel(width, sampleSpawnOptions()[2])
		m.err = stringsError("OpenAI login required")
		return renderSample(name, width, m.spawnView()), true
	case "model-picker":
		picker := newModelPicker([]modelPickerItem{
			{id: "openai/gpt-5.5", display: "openai/gpt-5.5"},
			{id: "openai/gpt-4.1", display: "openai/gpt-4.1"},
		}, "openai/gpt-5.5", width)
		return renderSample(name, width, picker.View()), true
	case "theme-picker":
		picker := newThemePicker()
		return renderSample(name, width, picker.View()), true
	case "auth-overlay":
		view := noticePanel{
			Title:      "OpenAI auth",
			Summary:    "Signed in with Serf-owned OAuth state.",
			Source:     "serf",
			NextAction: "Use /logout openai to sign out or paste final redirect URL during login.",
		}.Text()
		return renderSample(name, width, view), true
	case "agents-picker":
		picker := newTranscriptPicker([]modelPickerItem{
			{id: "local:01SERF", display: "main session"},
			{id: "local:01SERF:worker", display: "worker - restore auth commands"},
			{id: "local:01SERF:explorer", display: "explorer - inspect old TUI"},
		}, "local:01SERF", width)
		return renderSample(name, width, picker.View()), true
	case "help-overlay":
		return renderSample(name, width, hubSlashCommandHelp(sampleSessionDetails()["serf-idle"].Capabilities)), true
	case "diagnostics":
		view := noticePanel{
			Title:      "Spawn failed",
			Summary:    sampleDiagnostics()[0].Summary,
			Source:     sampleDiagnostics()[0].Source,
			Reason:     sampleDiagnostics()[0].Cause,
			NextAction: sampleDiagnostics()[0].NextAction,
		}.Text() + "\n\n" + noticePanel{
			Title:   "Action unavailable",
			Summary: sampleDiagnostics()[1].Summary,
			Source:  sampleDiagnostics()[1].Source,
			Reason:  sampleDiagnostics()[1].Cause,
		}.Text()
		return renderSample(name, width, view), true
	case "appshell-normal":
		return renderSample(name, width, appShell{
			TopBar: "serf live",
			Body:   "Live now\n> idle serf session\n  codex smoke",
			Footer: actionBarForWidth(width, "enter open", "n new", "ctrl+o dashboard", "q quit"),
		}.View()), true
	case "appshell-loading":
		return renderSample(name, width, appShell{
			TopBar: "serf live",
			Body:   "Loading hub dashboard...",
			Footer: actionBarForWidth(width, "ctrl+o dashboard", "q quit"),
		}.View()), true
	case "appshell-error":
		return renderSample(name, width, appShell{
			TopBar: "serf live",
			Body: noticePanel{
				Title:      "Hub unavailable",
				Summary:    "Could not reach the configured Hub.",
				NextAction: "Retry after checking the hub process.",
			}.Text(),
			Footer: actionBarForWidth(width, "r retry", "ctrl+o dashboard", "q quit"),
		}.View()), true
	case "topbar-session":
		title := "serf / session / Restore hub TUI widgets"
		return renderSample(name, width, truncateSessionLine(title, width)), true
	case "actionbar-normal":
		return renderSample(name, width, actionBarForWidth(width, "enter open", "p project", "n new", "ctrl+o dashboard", "q quit")), true
	case "actionbar-wrapped":
		return renderSample(name, width, actionBarForWidth(width, "enter open", "p project", "n new", "ctrl+o dashboard", "q quit")), true
	case "picker-empty":
		picker := newPickerPanel("Command palette", []pickerPanelItem{{ID: "open", Label: "Open session"}}, width)
		picker.filter = "missing"
		return renderSample(name, width, picker.View()), true
	case "picker-disabled":
		picker := newPickerPanel("Command palette", []pickerPanelItem{
			{ID: "clear", Label: "/clear", Detail: "clear transcript", DisabledReason: "source does not advertise clear"},
		}, width)
		return renderSample(name, width, picker.View()), true
	case "picker-error":
		return renderSample(name, width, noticePanel{
			Title:      "Provider unavailable",
			Summary:    "provider listing failed",
			NextAction: "Retry /model after signing in.",
		}.Text()), true
	default:
		return tuiSampleRender{}, false
	}
}

func sampleHubModel(width int) hubModel {
	m := newHubModel(nil, "http://hub.test")
	m.width = width
	m.height = 32
	m.tree = sampleDashboardTree()
	m.rows = buildDashboardRows(m.tree)
	m.clampSelection()
	return m
}

func sampleSessionModel(width int, detail hubSessionDetail) hubModel {
	m := sampleHubModel(width)
	m.mode = hubModeSession
	m.detail = detail
	m.session = newModel("", "", nil)
	m.session.width = width
	m.session.height = 32
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "Ready for the next task."}}
	m.session.refreshViewport()
	return m
}

func sampleSpawnModel(width int, sample tuiSpawnSample) hubModel {
	m := sampleHubModel(width)
	m.mode = hubModeSpawn
	m.spawnHarness = sample.Harness
	m.spawnHarnesses = []string{"serf", "codex-local"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf", "codex-local": "codex"}
	m.spawnModel = sample.Model
	m.spawnDir = sample.WorkingDir
	m.spawnProject = "serf"
	m.spawnModels = []modelPickerItem{{id: "openai/gpt-5.5", display: "openai/gpt-5.5"}, {id: "openai/gpt-4.1", display: "openai/gpt-4.1"}}
	m.spawnHarnessModels = map[string][]modelPickerItem{
		"codex-local": {{id: "gpt-5.3-codex", display: "gpt-5.3-codex"}},
	}
	m.setSpawnFocus(hubSpawnFieldPrompt)
	return m
}

type stringsError string

func (e stringsError) Error() string {
	return string(e)
}
