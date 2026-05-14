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
		State:       "processing",
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
				RollupState: "processing",
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
			State:       "processing",
			Model:       "openai/gpt-5.5",
			WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf",
			Project:     "serf",
			Live:        true,
			Capabilities: hubSessionCapabilities{
				Steer: true, Interrupt: true,
			},
		},
		"busy-readonly": {
			Ref:         "codex-local:01BUSY",
			SessionID:   "01BUSYCODEX",
			SourceLabel: "codex-local",
			Title:       "Codex busy read-only sample",
			State:       "processing",
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
	return []tuiSampleRender{
		renderSample("dashboard-narrow", 60, "serf hub\nserf 2 live\n> Restore hub TUI widgets serf\nn new  / palette", "serf hub", "n new", "/ palette"),
		renderSample("dashboard-normal", 100, "serf hub connected openai:signed in\nserf 2 live\n> Restore hub TUI widgets serf idle\ncodex-src 1 live\nCodex app-server smoke codex-local", "codex-local", "Restore hub TUI widgets"),
		renderSample("dashboard-wide", 140, "serf hub connected\nleft: live sessions\nright: DetailsDrawer selected session capabilities and diagnostics", "DetailsDrawer", "diagnostics"),
		renderSample("project-narrow", 60, "project serf\nlive\n> Restore hub TUI widgets\nrecent\nDocument protocol adoption\nn new here  / palette", "live", "recent"),
		renderSample("project-normal", 100, "project serf\nLive now Restore hub TUI widgets\nRecent in this project Document protocol adoption", "Live now", "Recent in this project"),
		renderSample("project-wide", 140, "project serf\nSessionList left\nDetailsDrawer right with selected row capabilities", "DetailsDrawer", "capabilities"),
		renderSample("session-idle", 100, "session ZYMEYZ serf idle gpt-5.5\nmessage\n> draft stays visible\n/help /model /tasks", "message", "draft stays visible"),
		renderSample("session-streaming", 100, "assistant streaming\n1. Reproduce\n2. Patch\n3. Verify\nmarkdown render stays stable", "assistant streaming", "markdown"),
		renderSample("session-busy-steer", 100, "busy session\nsteer\n> Please also check old TUI command parity", "steer", "Please also check"),
		renderSample("session-busy-readonly", 100, "busy session\nread-only: source does not advertise steer\ndraft kept", "read-only", "draft kept"),
		renderSample("session-browse", 100, "browse ZYMEYZ\n> turn_0 user\nf fork user turn\nesc compose", "browse", "f fork"),
		renderSample("session-fork", 100, "fork draft from turn_0\noriginal before fork\n> edited prompt\nenter fork", "fork draft", "edited prompt"),
		renderSample("spawn-serf", 100, "new session\nHarness serf\nModel openai/gpt-5.5\nPrompt\n> Implement the next TUI task", "Harness serf", "openai/gpt-5.5"),
		renderSample("spawn-codex", 100, "new session\nHarness codex-local\nModel codex-local/gpt-5.3-codex\nNo OpenAI provider namespace", "codex-local", "gpt-5.3-codex"),
		renderSample("spawn-auth-required", 100, "new session\nModel openai/gpt-4.1 disabled\nOpenAI login required\n/login openai", "OpenAI login required", "/login openai"),
		renderSample("model-picker", 100, "Select model\nFilter: gpt\n> openai/gpt-5.5\nopenai/gpt-4.1 disabled: login required", "Select model", "login required"),
		renderSample("theme-picker", 80, "Select theme\n> system\n  dark\n  light", "Select theme", "system"),
		renderSample("auth-overlay", 100, "auth openai\nsigned in through Serf state dir\nlogout openai\npaste final redirect URL", "Serf state dir", "paste final redirect URL"),
		renderSample("agents-picker", 100, "Select transcript\n> main session\nworker - restore auth commands\nexplorer - inspect old TUI", "Select transcript", "main session"),
		renderSample("help-overlay", 100, "Available commands for this source\n/model available\n/clear unavailable: Codex source lacks clear", "Available commands", "unavailable"),
		renderSample("diagnostics", 100, "diagnostics\nspawn failed: model provider is not reported by harness\naction unavailable: clear", "spawn failed", "action unavailable"),
	}
}

func sampleInteractions() []tuiInteractionSample {
	return []tuiInteractionSample{
		{Name: "prompt-owns-printable-shortcuts", Keys: []string{"m", "h"}, Expected: "focused prompt value becomes mh"},
		{Name: "picker-owns-filter-navigation", Keys: []string{"/", "g", "p", "t", "down", "enter"}, Expected: "picker filter is gpt and selected row is chosen"},
		{Name: "composer-draft-survives-overlay", Keys: []string{"type draft", "/model", "esc"}, Expected: "composer still contains draft"},
		{Name: "busy-send-switches-to-steer", Keys: []string{"enter"}, Expected: "busy draft is preserved and sent through steer when supported"},
		{Name: "unsupported-codex-actions-hidden-or-disabled", Keys: []string{"/help"}, Expected: "Codex clear/shutdown actions are absent or disabled with reasons"},
	}
}

func renderSample(name string, width int, view string, contains ...string) tuiSampleRender {
	return tuiSampleRender{
		Name:     name,
		Width:    width,
		View:     strings.TrimSpace(view),
		Contains: contains,
	}
}
