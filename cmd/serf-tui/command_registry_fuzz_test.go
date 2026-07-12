//go:build serffuzz

package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzCommandRegistryProgram replays deterministic command, palette, auth, and
// model-formatting branches. Network-shaped commands use the package fake hub.
func FuzzCommandRegistryProgram(f *testing.F) {
	for i := 0; i < 6; i++ {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, program byte) {
		switch program % 6 {
		case 0:
			statuses := []authStatus{
				{Provider: "openai", ActiveSource: openai.AuthSourceEnv, HasStoredOAuth: true, Email: "a@b"},
				{Provider: "openai", ActiveSource: openai.AuthSourceEnv},
				{Provider: "openai", ActiveSource: openai.AuthSourceOAuth, NeedsLogin: true, StoredEmail: "s@b", Error: "expired"},
				{Provider: "openai", ActiveSource: openai.AuthSourceOAuth, NeedsRefresh: true},
				{Provider: "openai", ActiveSource: openai.AuthSourceSignedOut, HasStoredOAuth: true, Error: "bad"},
				{Provider: "", Error: "ignored"}, {Provider: "anthropic"},
			}
			for _, s := range statuses {
				_ = formatAuthStatusSummary(s)
			}
			_ = authProviderArg("   ")
			_ = authProviderArg("anthropic extra")
			_ = authStatusFromAppWire(appwire.AuthStatusResponse{})
		case 1:
			entries := []commandPaletteEntry{{Item: tuipick.PickerPanelItem{ID: "a", Label: "/a"}}, {Item: tuipick.PickerPanelItem{ID: "b", Label: "/b", Detail: "detail", DisabledReason: "why"}}}
			p := newCommandPalette("Commands", entries, 0)
			_ = p.Init()
			_ = p.renderItemsWindow(1)
			_ = p.ViewWithMaxHeight(5)
			_, _ = paletteItemWindow(0, 0, 1)
			_, _ = paletteItemWindow(5, -2, 2)
			_, _ = paletteItemWindow(5, 99, 2)
			_, _ = paletteItemWindow(5, 3, 2)
			_, _ = p.selectedEntry()
			updated, _ := tuipick.NewPickerPanel("x", entriesToItems(entries), 40).Update(tea.KeyMsg{Type: tea.KeyDown})
			p.panel = updated.(tuipick.PickerPanel)
			_, _ = p.selectedEntry()
			empty := newCommandPalette("Empty", nil, 40)
			_ = empty.renderItemsWindow(2)
		case 2:
			rows := []hubRow{
				{kind: hubRowProject, projectKey: "", project: "empty"},
				{kind: hubRowProject, projectKey: "p", project: "P"},
				{kind: hubRowProject, projectKey: "p", project: "duplicate"},
				{kind: hubRowSession, title: "bad"},
			}
			_ = commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
		case 3:
			_ = prettifyModelDisplayName("a--b-20250101")
			_ = formatModelContextWindow(999)
			_ = formatModelContextWindow(1000)
			_ = formatModelContextWindow(1_000_000)
			_ = buildModelPickerItems([]appwire.ModelDescriptor{{Model: "m"}, {Provider: "p"}, {Provider: "p", Model: "m"}}, false)
			_ = modelPickerItemsFromResponse(appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: "m"}}, Diagnostics: []appwire.ModelListDiagnostic{{}, {Provider: "p", Title: "x", Message: "x"}, {Provider: "p", Message: "ignored duplicate"}}}, false)
			_ = modelPickerItemProvider(tuipick.ModelPickerItem{})
			_ = modelPickerItemProvider(tuipick.ModelPickerItem{Display: "p/m"})
			_ = modelPickerItemProvider(tuipick.ModelPickerItem{ID: "p/m"})
			_ = modelDiagnosticDisabledReason(appwire.ModelListDiagnostic{Hint: "try"})
		case 4:
			m := newHubModel(nil, "")
			for _, name := range []string{"new", "refresh", "upgrade", "project", "tasks", "agents", "goal", "interrupt", "compact", "clear", "shutdown", "model", "credentials", "plugins"} {
				cmd, ok := hubCommandByName(name)
				if !ok {
					t.Fatal(name)
				}
				_ = runHubCommandDefinition(&m, cmd, "status")
			}
			m.mode = hubModeSession
			cmd, _ := hubCommandByName("upgrade")
			_ = cmd.Run(&m, "")
			_ = fetchCurrentHubSession(&m, "")
			_ = fetchCurrentHubStatus(&m, "")
			_ = runHubCommandDefinition(&m, hubCommandDefinition{}, "")
		case 5:
			m := newHubModel(nil, "")
			m.mode = hubModeSession
			m.detail.Ref = "local:thread"
			m.detail.Goal = &appwire.GoalState{Status: "active", Iterations: 2}
			_ = m.runHubGoal("status")
			_ = m.runHubGoal("clear")
			_ = m.runHubGoal("objective")
			_ = formatHubUpgradeResult(appwire.UpgradeResponse{})
			_ = formatHubUpgradeResult(appwire.UpgradeResponse{Release: "v1", Archive: "a", ShareBinDir: "s", BinDir: "b", RestartMessage: "r"})
		}
	})
}

func entriesToItems(entries []commandPaletteEntry) []tuipick.PickerPanelItem {
	items := make([]tuipick.PickerPanelItem, len(entries))
	for i := range entries {
		items[i] = entries[i].Item
	}
	return items
}
