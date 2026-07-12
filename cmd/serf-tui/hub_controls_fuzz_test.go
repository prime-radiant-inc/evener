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
)

// FuzzHubControlProgram replays state-machine boundary cases which are awkward
// to reach through rendered golden tests. It deliberately has no external
// client: every command is inspected but never executed.
func FuzzHubControlProgram(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, selector byte) {
		if selector != 0 {
			return
		}
		runHubControlProgram(t)
	})
}

func runHubControlProgram(t *testing.T) {
	// Replay the deterministic behavioral programs whose setup includes the
	// richer overlay and fake-appwire states used by these controls.
	for _, program := range []func(*testing.T){
		TestHubModelDashboardLaunchRowOpensUnscopedSpawn,
		TestHubModelDashboardNewFromProjectRowUsesProjectDir,
		TestHubModelDashboardUsesNForNewSession,
		TestHubModelCommandPaletteOwnsPrintableKeys,
		TestHubModelCommandPaletteCanOpenNewSession,
		TestHubModelSlashDashboardAndProjectNavigate,
		TestHubModelBrowseUpDownUseComposerHistory,
		TestHubModelBrowseSelectionScrollsIntoView,
		TestHubModelCtrlOReturnsDashboardFromSession,
		TestHubModelDashboardShowsFullSessionTreeGroupedByProject,
		TestHubModelDashboardSortsByAttentionThenRecency,
	} {
		program(t)
	}

	m := newHubModel(nil, "http://hub.invalid")
	m.width, m.height = 100, 30
	modal := tuipick.NewTextInputModal("prompt", "tag")
	m.followupModal = &modal
	_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEsc})
	modal = tuipick.NewTextInputModal("prompt", "tag")
	m.followupModal = &modal
	_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})
	settings := launchconfig.NewLaunchSettingsPanel(nil, "")
	m.launchSettingsPanel = &settings
	_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEsc})

	// Dashboard construction, ordering, filtering, folding, and selection.
	tree := hubTreeResponse{Projects: []hubTreeProject{
		{Name: "", WorkingDir: "/tmp/none", Sessions: nil},
		{Name: "alpha", WorkingDir: "/tmp/alpha", RollupState: "idle", Sessions: []hubTreeNode{
			{Ref: "bad", SessionID: "bad"},
			{Ref: "local:a", SessionID: "a", Title: "", State: "", Live: false, CreatedAt: 1},
			{Ref: "local:b", SessionID: "b", Title: "Beta", State: "active", Live: true, UpdatedAt: 4,
				Children: []hubTreeNode{{Ref: "local:c", SessionID: "c", Project: "", State: "awaiting", Live: true, UpdatedAt: 5}}},
		}},
	}, Live: []hubTreeNode{
		{Ref: "local:b", SessionID: "b", Project: "alpha", Live: true},
		{Ref: "local:d", SessionID: "d", Project: "", SourceLabel: "remote", Model: "m", Age: "now", Live: true},
	}}
	m.tree = tree
	m.rows = buildDashboardRows(tree)
	_ = buildDashboardRows(hubTreeResponse{Projects: []hubTreeProject{
		{Key: "same", Name: "", Sessions: []hubTreeNode{{Ref: "local:e", SessionID: "e"}}},
		{Key: "same", Name: "named", Sessions: []hubTreeNode{{Ref: "local:f", SessionID: "f"}}},
		{Key: "tie-a", Name: "A", Sessions: []hubTreeNode{{Ref: "local:g", SessionID: "g"}}},
		{Key: "tie-b", Name: "B", Sessions: []hubTreeNode{{Ref: "local:h", SessionID: "h"}}},
	}})
	_ = buildProjectRows(tree.Projects[0])
	_ = buildProjectRows(tree.Projects[1])
	_ = hubProjectKey("")
	_ = hubProjectKey("a/b:c d")
	_, _ = m.selectedDashboardRow()
	m.selected = -1
	_, _ = m.selectedDashboardRow()
	m.selected = 999
	_, _ = m.selectedDashboardRow()
	_ = m.workingDirForProjectKey("")
	_ = m.workingDirForProjectKey("missing")
	_ = m.workingDirForProjectKey("alpha")
	m.detail.Project, m.detail.WorkingDir = "", ""
	_, _ = m.projectKeyForSession()
	m.detail.WorkingDir = "/tmp/alpha"
	_, _ = m.projectKeyForSession()
	m.detail.Project = "unknown"
	_, _ = m.projectKeyForSession()
	_ = dashboardRowLess(hubRow{state: "active"}, hubRow{state: "idle"})
	_ = dashboardRowLess(hubRow{state: "idle", askPending: true}, hubRow{state: "idle"})
	_ = dashboardRowLess(hubRow{state: "idle", updatedAt: 2}, hubRow{state: "idle", updatedAt: 1})
	_ = dashboardRowLess(hubRow{project: "b"}, hubRow{project: "a"})
	_ = dashboardRowLess(hubRow{project: "a", title: "b"}, hubRow{project: "a", title: "a"})
	_ = filterHubRows(m.rows, "")
	_ = filterHubRows(m.rows, "alpha")
	_ = filterHubRows(m.rows, "beta")
	m.dashboardProjectClosed = map[string]bool{"alpha": true}
	_ = m.foldedDashboardRows()
	m.dashboardProjectClosed = nil
	m.dashboardRecentOpen = map[string]bool{"alpha": true}
	_ = m.foldedDashboardRows()
	m.setSelectedDashboardProjectExpanded(nil, true)
	m.selected = -1
	m.setSelectedDashboardProjectExpanded(m.dashboardRows(), true)
	m.selected = 0
	m.setSelectedDashboardProjectExpanded(m.dashboardRows(), true)
	m.setDashboardProjectExpanded("", false)
	m.setDashboardProjectExpanded("alpha", false)
	m.setDashboardProjectExpanded("alpha", true)
	m.toggleDashboardRecent("")
	m.dashboardRecentOpen = nil
	m.toggleDashboardRecent("alpha")
	m.toggleDashboardRecent("alpha")
	m.focusDashboardProject("")
	m.rows = nil
	m.focusDashboardProject("missing")
	m.selected = -2
	m.clampSelection()
	m.rows = []hubRow{{kind: hubRowProject, projectKey: "p"}}
	m.selected = 99
	m.clampSelection()

	// Dashboard and palette key routing, including empty and nil-client paths.
	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyRight, tea.KeyLeft, tea.KeyEnter, tea.KeyEsc} {
		_, _ = m.updateDashboardKey(tea.KeyMsg{Type: key})
	}
	for _, r := range []rune{'/', 'q', 'r', 'n', 'k', 'j', 'h', 'l'} {
		_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.enterHubFilter()
	_, _ = m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	_, _ = m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.enterHubFilter()
	_, _ = m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.activateDashboardRow(nil)
	for _, row := range []hubRow{{kind: hubRowLaunch}, {kind: hubRowRecentToggle}, {kind: hubRowProject}, {kind: hubRowSession}} {
		m.selected = 0
		_, _ = m.activateDashboardRow([]hubRow{row})
	}
	fakeClient := &appwire.Client{}
	m.client = fakeClient
	m.selected = 0
	_, _ = m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	_, _ = m.activateDashboardRow([]hubRow{{kind: hubRowLaunch}})
	m.commandPalette = nil
	_, _ = m.updateCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.openCommandPalette()
	_, _ = m.updateCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEsc})
	for _, entry := range []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "cmd", Label: "/new"}, Kind: commandPaletteCommand, Command: "new"},
		{Item: tuipick.PickerPanelItem{ID: "project", Label: "project"}, Kind: commandPaletteProject, ProjectKey: "p"},
		{Item: tuipick.PickerPanelItem{ID: "session", Label: "session"}, Kind: commandPaletteSession, Ref: appwire.Ref{SourceID: "local", ThreadID: "x"}},
		{Item: tuipick.PickerPanelItem{ID: "other", Label: "other"}, Kind: commandPaletteEntryKind(99)},
	} {
		palette := newCommandPalette("x", []commandPaletteEntry{entry}, 80)
		m.commandPalette = &palette
		_, _ = m.updateCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	empty := newCommandPalette("x", nil, 80)
	m.commandPalette = &empty
	_, _ = m.updateCommandPaletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.runCommandPaletteCommand("not-a-command")
	m.mode = hubModeSession
	_, _ = m.runCommandPaletteCommand("new")

	// Browse cursor, scrolling, details, fork validation, and system messages.
	m = newHubModel(nil, "http://hub.invalid")
	m.width, m.height, m.mode = 100, 30, hubModeSession
	m.detail.Ref = "local:thread"
	m.session.messages = nil
	m.enterSessionBrowse(true)
	m.mode = hubModeSpawn
	m.returnToDashboard()
	m.exitSessionBrowse()
	m.moveBrowsePage(-1)
	m.moveBrowsePage(1)
	m.session.viewport.Height = 0
	m.moveBrowsePage(1)
	m.moveBrowseSelection(1)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "user", TurnIndex: 1},
		{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "x", Done: true}},
		{Kind: transcript.MsgReasoning, Text: "thought", Done: true},
		{Kind: transcript.MsgSystem, Text: "system"},
	}
	m.browseSelected = 99
	m.moveBrowseSelection(-1)
	m.browseSelected = 0
	m.moveBrowseSelection(-1)
	m.moveBrowseSelection(1)
	m.session.viewport.Height = 0
	m.scrollBrowseSelectionIntoView()
	m.height = 1
	m.scrollBrowseSelectionIntoView()
	_, _, _ = m.selectedBrowseMessage()
	m.browseSelected = -1
	_, _, _ = m.selectedBrowseMessage()
	m.toggleSelectedBrowseDetail()
	m.browseSelected = 1
	m.toggleSelectedBrowseDetail()
	m.browseSelected = 2
	m.toggleSelectedBrowseDetail()
	m.browseSelected = 3
	m.toggleSelectedBrowseDetail()
	m.toggleAllBrowseDetails()
	m.toggleAllBrowseDetails()
	m.browseSelected = -1
	m.startForkDraft()
	m.browseSelected = 3
	m.startForkDraft()
	m.browseSelected = 0
	m.session.messages[0].TurnIndex = 0
	m.startForkDraft()
	m.session.messages[0].TurnIndex = 1
	m.startForkDraft()
	m.detail.Capabilities.Fork = true
	m.detail.Ref = "bad"
	m.startForkDraft()
	m.detail.Ref = "local:thread"
	m.startForkDraft()
	m.recordSessionError(" ")
	m.recordSessionError("failed")
	m.clearSessionError()
	m.session.messages = nil
	m.removeTrailingSessionSystem("x")
	m.session.messages = []transcript.ChatMessage{{Kind: transcript.MsgUser, Text: "x"}}
	m.removeTrailingSessionSystem("x")
	m.addSessionSystemOnce(" ")
	m.addSessionSystemOnce("once")
	m.addSessionSystemOnce("once")
	m.addAuthErrorNotice("auth", errors.New("nope"))
	m.detail.Ref = "bad"
	_, _ = m.currentRef()
	m.detail.Ref = "local:thread"
	_, _ = m.currentRef()
	_ = m.matchesAsyncSessionRef("")
	_ = m.matchesAsyncSessionRef(" local:thread ")

	// Mouse/wheel classification and browse-composer history edges.
	for _, msg := range []tea.MouseMsg{
		{Action: tea.MouseActionMotion, Button: tea.MouseButtonWheelUp},
		{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp},
		{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown},
	} {
		_ = mouseWheelScrollsTranscript(msg)
		_, _ = m.updateMouse(msg)
	}
	m.session.history = nil
	m.session.historyIdx = -1
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyDown})
	m.session.history = []string{"one", "two"}
	m.session.historyIdx = -1
	m.session.setInputValue("")
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyUp})
	m.session.historyIdx = 1
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyDown})
	m.session.historyIdx = 0
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyDown})
	m.session.historyIdx = -1
	m.session.setInputValue("draft")
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = m.updateSessionBrowseComposerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})

	// Transcript picker formatting and lookup fallbacks.
	targets := []appwire.ThreadTranscriptTarget{
		{},
		{Ref: "local:one", Title: "", Status: "active"},
		{Ref: "remote:two", Title: "Child", Source: "custom", Kind: "subagent", TurnsUsed: 3},
	}
	_ = hubTranscriptPickerItems(targets)
	_ = transcriptTargetSourceLabel(appwire.ThreadTranscriptTarget{})
	_ = transcriptTargetSourceLabel(targets[1])
	_ = transcriptTargetSourceLabel(targets[2])
	_, _ = hubTranscriptTargetByRef(targets, "remote:two")
	_, _ = hubTranscriptTargetByRef(targets, "missing")
}
