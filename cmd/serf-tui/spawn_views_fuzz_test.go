//go:build serffuzz

package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
	"primeradiant.com/serf/envvars"
)

// FuzzSpawnAndViewProgram is a deterministic branch program for the root TUI's
// spawn form and its dashboard/session renderers. It uses only in-memory model
// state; returned commands are intentionally not executed.
func FuzzSpawnAndViewProgram(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		m := newHubModel(nil, "hub")
		m.width, m.height = 130, 30

		// Layout boundary cases.
		_ = joinDashboardColumns("left\nsecond", "right\nsecond\nthird", 2, 3, 9)
		_ = truncateSessionLine("abcdef", 0)
		_ = truncateSessionLine("abcdef", 3)
		for _, h := range []int{-1, 1, 20} {
			_ = sessionShellBodyHeight(h, "top", "overlay", "footer")
			_ = paletteOverlayHeight(h, "top", "prefix", "footer")
			_ = dashboardRowLimit(h, "top", "prefix", "footer")
		}

		rows := []hubRow{
			{kind: hubRowLaunch, title: "new"},
			{kind: hubRowProject, project: "p", projectKey: "p", state: "idle", liveCount: 1, recentCount: 1},
			{kind: hubRowSession, ref: appwire.Ref{ThreadID: "s"}, title: "title", project: "p", projectKey: "p", state: "active", live: true},
			{kind: hubRowRecentToggle, project: "p", projectKey: "p", recentCount: 0},
			{kind: hubRowSession, title: "old", project: "p", projectKey: "p", state: "closed"},
		}
		for _, args := range [][3]int{{0, 0, 1}, {5, -2, 2}, {5, 99, 2}, {5, 2, 99}} {
			_, _ = dashboardRowWindow(args[0], args[1], args[2])
		}
		_ = renderDashboardRecentToggleRow(rows[3], false, false, 40)
		_ = renderDashboardRecentToggleRow(rows[3], true, true, 40)
		_ = dashboardTitle("\n first\nsecond")
		_ = dashboardTitle("\n\n")
		_ = dashboardRecentExpanded(rows, -1)
		_ = dashboardRecentExpanded(rows, 3)
		_ = dashboardProjectExpanded(rows, -1)
		_ = dashboardProjectExpanded(rows, 1)
		_ = dashboardProjectExpanded([]hubRow{{kind: hubRowProject, projectKey: "a"}, {kind: hubRowProject, projectKey: "b"}}, 0)
		_ = dashboardProjectExpanded([]hubRow{{kind: hubRowProject}}, 0)
		for _, state := range []string{"awaiting", "active", "warning", "idle", "ended", "errored", "mystery"} {
			_ = stateColor(state)
			_ = statusDot(state)
		}
		_ = dashboardFooter(60)
		_ = dashboardFooter(100)
		m.rows = rows
		for i := 0; i <= len(rows); i++ {
			m.selected = i
			_ = m.dashboardDetailsView(rows, 50)
		}
		m.err = errors.New("fixture")
		_ = m.dashboardDetailsView(rows, 50)
		m.err = nil
		_ = m.dashboardRecentDetails(rows[3])
		m.tree.Projects = []hubTreeProject{{Key: "p", WorkingDir: "/work/p"}}
		_ = m.dashboardRecentDetails(rows[3])
		m.selected = 0
		_ = m.dashboardDetailsView([]hubRow{{kind: hubRowKind(99)}}, 50)
		_ = dashboardSessionDetails(hubRow{})
		_, _ = projectSessionCounts(hubRow{kind: hubRowProject, projectKey: "p"}, rows)
		_ = projectSummary(hubRow{kind: hubRowProject, projectKey: "p", state: "idle"}, rows)

		// Dashboard's empty/filter and populated narrow/wide branches.
		m.rows = nil
		_ = m.dashboardView()
		m.dashboardFilter.SetValue("none")
		_ = m.dashboardView()
		m.dashboardFilter.SetValue("")
		m.rows, m.selected = rows, 2
		_ = m.dashboardView()
		m.width = 70
		_ = m.dashboardView()
		m.openCommandPalette()
		_ = m.dashboardView()
		m.commandPalette = nil
		followDash := tuipick.NewTextInputModal("prompt", "tag")
		m.followupModal = &followDash
		_ = m.dashboardView()
		m.followupModal = nil
		m.credentialsPanel = &launchconfig.CredentialsPanel{}
		_ = m.dashboardView()
		m.credentialsPanel = nil
		m.launchSettingsPanel = &launchconfig.LaunchSettingsPanel{}
		_ = m.dashboardView()
		m.launchSettingsPanel = nil
		m.pluginsPanel = &launchconfig.PluginsPanel{}
		_ = m.dashboardView()
		m.pluginsPanel = nil

		// Session header, readiness, chrome, transcript, and body fallbacks.
		m.width = 20
		m.detail.Title = "a title long enough to truncate"
		m.detail.Model = "provider/model"
		m.detail.Profile = "profile"
		_ = m.sessionHeaderLines()
		m.width = 1
		_ = m.sessionHeaderLines()
		m.width = 20
		m.authStatusSeen = true
		for _, source := range []string{"", "signed-out", "key"} {
			m.authStatus.ActiveSource = source
			_ = m.sessionAuthReadinessLabel()
		}
		m.authStatusSeen, m.detail.Profile, m.detail.Model = false, "", "plain-model"
		_ = m.sessionAuthReadinessLabel()
		m.err = errors.New("session error")
		m.forkDraft = &hubForkDraft{EntryIndex: 2}
		m.session.messages = []transcript.ChatMessage{
			{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{JobID: "job", Status: "running"}}},
			{Kind: transcript.MsgUser, Text: "hello"},
		}
		m.session.scrollMode, m.browseSelected = true, 0
		_ = m.renderSessionMainBody()
		m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Hidden: true}}}
		_ = m.renderSessionMainBody()
		m.session.scrollMode = false
		m.height = 0
		_ = m.sessionBody("", 0, false)
		m.syncSessionViewport()
		_ = m.sessionView()
		m.height = 30
		m.session.scrollMode = true
		_, _, _ = m.sessionChromeText()
		m.session.scrollMode = false
		panel := hubSessionPanel{Body: "details"}
		m.sessionPanel = &panel
		_ = m.sessionPanelOverlay()
		m.width = 0
		_ = m.sessionPanelOverlay()
		m.sessionPanel = nil
		_ = m.sessionPanelOverlay()
		question := newQuestionOverlay("ref", []askQuestion{{Header: "h", Question: "q"}}, 40)
		m.questionOverlay = question
		picker := tuipick.NewModelPicker([]tuipick.ModelPickerItem{{ID: "m"}}, "m", 40)
		m.sessionModelPicker = &picker
		themePicker := tuipick.NewThemePicker()
		m.sessionThemePicker = &themePicker
		transcriptPicker := tuipick.NewModelPicker([]tuipick.ModelPickerItem{{ID: "t"}}, "t", 40)
		m.sessionTranscriptPicker = &transcriptPicker
		m.openCommandPalette()
		overrides := launchconfig.NewLaunchOverridesModal()
		m.launchOverridesModal = &overrides
		followSession := tuipick.NewTextInputModal("prompt", "tag")
		m.followupModal = &followSession
		_, _, _ = m.sessionChromeText()

		// Spawn focus, harness selection, modal priority, and submit guards.
		m = newHubModel(nil, "hub")
		m.width, m.height = 80, 24
		m.setSpawnFocus(hubSpawnField(99))
		m.setSpawnFocus(hubSpawnFieldDir)
		m.spawnDir = "/tmp"
		m.spawnDirInput.SetValue("")
		m.setSpawnFocus(hubSpawnFieldDir)
		m.advanceSpawnFocus(-9)
		m.resizeSpawnInput()
		m.resizeSpawnInputFrom(-1)
		m.session.input.MaxHeight = 1
		m.session.input.SetValue("a\nb")
		m.resizeSpawnInputFrom(9)
		for _, focus := range []hubSpawnField{hubSpawnFieldPrompt, hubSpawnFieldHarness, hubSpawnFieldModel, hubSpawnFieldDir} {
			m.spawnFocus = focus
			_ = m.spawnFieldHint()
		}
		m.closeSpawnForm()
		t.Setenv(envvars.SERFModel.Name, "provider/model")
		m.resetSpawnForm()
		m.spawnFocus = hubSpawnFieldDir
		m.spawnDirInput.SetValue(".")
		_, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyTab})
		_, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlL})
		layer := appwire.LaunchConfigLayer{}
		m.spawnLaunchOverrides = &layer
		_, openOverrides := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlL})
		_ = openOverrides()
		m.spawnHarnesses = nil
		m.cycleSpawnHarness()
		m.spawnHarnesses = []string{"serf", "codex"}
		m.spawnHarness = "missing"
		m.cycleSpawnHarness()
		m.spawnHarnessKinds = map[string]string{"codex": "codex"}
		m.spawnHarness = "codex"
		_ = m.spawnHarnessKind()
		m.spawnModel = "provider/model"
		m.syncSpawnModelWithHarness()
		m.spawnHarness = "serf"
		m.spawnModels = []tuipick.ModelPickerItem{{ID: "disabled", DisabledReason: "no"}, {ID: "ok"}}
		m.spawnModel = ""
		m.syncSpawnModelWithHarness()
		_, _ = firstEnabledModel([]tuipick.ModelPickerItem{{DisabledReason: "no"}})
		_ = m.spawnModelDisabledReason("missing")
		m.spawnEmptyTaskReasons = map[string]string{"serf": "reason"}
		m.spawnEmptyTaskNext = map[string]string{"serf": "next"}
		_ = m.spawnEmptyTaskUnsupportedReason()
		_ = m.spawnEmptyTaskUnsupportedNextAction()
		m.spawnEmptyTaskReasons, m.spawnEmptyTaskNext = nil, nil
		_ = m.spawnEmptyTaskUnsupportedReason()
		_ = m.spawnEmptyTaskUnsupportedNextAction()
		m.spawnHarness = "codex"
		m.spawnModel = "model"
		_ = m.spawnHarnessModelDisplay()
		m.spawnModel = "provider/model"
		_ = m.spawnHarnessModelDisplay()
		m.rows, m.selected = nil, 0
		_ = m.spawnWorkingDir()
		_ = m.spawnProjectName()

		keys := []tea.KeyMsg{
			{Type: tea.KeyEsc}, {Type: tea.KeyTab}, {Type: tea.KeyShiftTab},
			{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyCtrlU},
			{Type: tea.KeyCtrlJ}, {Type: tea.KeyRunes, Runes: []rune("x")},
		}
		for _, focus := range []hubSpawnField{hubSpawnFieldPrompt, hubSpawnFieldHarness, hubSpawnFieldModel, hubSpawnFieldDir} {
			for _, key := range keys {
				m.spawnFocus = focus
				_, _ = m.updateSpawnKey(key)
			}
		}
		modal := launchconfig.NewLaunchOverridesModal()
		m.launchOverridesModal = &modal
		_, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
		follow := tuipick.NewTextInputModal("prompt", "tag")
		m.followupModal, m.launchOverridesModal = &follow, &modal
		_, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
		m.followupModal, m.launchOverridesModal = nil, nil
		m.openSpawnModelPicker(m.spawnModels)
		_, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
		_, _ = m.activateSpawnModelField()
		m.spawnModels = nil
		_, _ = m.activateSpawnModelField()
		_, _ = m.submitSpawnForm()
		client := &appwire.Client{}
		m.client = client
		m.spawnSubmitting = false
		m.spawnHarness = "codex"
		m.spawnHarnessKinds = map[string]string{"codex": "codex"}
		m.spawnModels = nil
		m.spawnHarnessModels = nil
		_, _ = m.activateSpawnModelField()
		m.spawnEmptyTaskReasons = map[string]string{"codex": "unsupported"}
		m.session.input.SetValue("")
		_, _ = m.submitSpawnForm()
		m.spawnEmptyTaskReasons = nil
		m.spawnHarness = "serf"
		m.spawnHarnessKinds = map[string]string{"serf": "serf"}
		m.client = nil
		m.spawnModels = nil
		_, _ = m.activateSpawnModelField()
		m.spawnModel = ""
		_, _ = m.submitSpawnForm()
		m.spawnModels = []tuipick.ModelPickerItem{{ID: "bad", DisabledReason: "disabled"}}
		m.spawnModel = "bad"
		_, _ = m.submitSpawnForm()
		m.spawnModels = []tuipick.ModelPickerItem{{ID: "ok"}}
		m.spawnModel = "ok"
		_, _ = m.submitSpawnForm()
		m.spawnFocus = hubSpawnFieldModel
		m.spawnHarness = "codex"
		m.spawnHarnessKinds = map[string]string{"codex": "codex"}
		m.spawnHarnessModels = nil
		_ = m.spawnFieldHint()
		m.spawnHarness = "serf"
		m.spawnHarnessKinds = map[string]string{"serf": "serf"}
		m.spawnModel, m.spawnModels = "", []tuipick.ModelPickerItem{{ID: "ok"}}
		_ = m.spawnView()
		m.selected = -1
		_ = m.spawnWorkingDir()
		m.tree.Projects = []hubTreeProject{{Key: "p", WorkingDir: "/work/p"}}
		m.rows = []hubRow{{kind: hubRowProject, projectKey: "p", project: "p"}}
		m.selected = 0
		_ = m.spawnWorkingDir()
		m.spawnModelPicker = &picker
		m.launchOverridesModal = &overrides
		m.followupModal = &followSession
		m.notices = []noticePanel{{Title: "notice", Reason: "reason"}}
		m.spawnSubmitting = true
		_ = m.spawnView()
		m.spawnSubmitting = true
		_, _ = m.submitSpawnForm()
		m.err = errors.New("spawn")
		m.spawnProject = "project"
		_ = m.spawnView()

		// Validation errors return before the inert client can issue RPC.
		submit := newHubModel(&appwire.Client{}, "hub")
		submit.spawnHarness = "serf"
		submit.spawnHarnessKinds = map[string]string{"serf": "serf"}
		submit.spawnModel = ""
		_, _ = submit.submitSpawnForm()
		submit.spawnModels = []tuipick.ModelPickerItem{{ID: "bad", DisabledReason: "disabled"}}
		submit.spawnModel = "bad"
		_, _ = submit.submitSpawnForm()
	})
}
