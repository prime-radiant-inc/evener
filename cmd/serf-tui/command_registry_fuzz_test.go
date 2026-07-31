//go:build serffuzz

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
	"primeradiant.com/serf/internal/appserver"
)

// FuzzCommandRegistryProgram replays deterministic command, palette, auth, and
// model-formatting branches. Network-shaped commands use the package fake hub.
func FuzzCommandRegistryProgram(f *testing.F) {
	for i := 0; i < 9; i++ {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, program byte) {
		switch program % 9 {
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
		case 6:
			// A selected panel ID absent from entries reaches the defensive miss.
			panel := tuipick.NewPickerPanel("x", []tuipick.PickerPanelItem{{ID: "selected", Label: "x"}}, 0)
			updated, _ := panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
			panel = updated.(tuipick.PickerPanel)
			p := commandPalette{panel: panel}
			_, _ = p.selectedEntry()
			_ = (commandPalette{}).ViewWithMaxHeight(0)

			// Exercise the generic disabled-reason fallback with a temporary
			// definition; restore the registry before leaving the seed.
			original := hubCommandRegistry
			hubCommandRegistry = append(hubCommandRegistry, hubCommandDefinition{
				Name: "unavailable", Scopes: hubCommandDashboard,
				Available: func(hubCommandContext) (bool, string) { return false, "" },
			})
			_ = commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, []hubRow{{kind: hubRowSession}})
			hubCommandRegistry = original
		case 7:
			client, frames, cleanup := newTestHubClientWithFeed(t, nil)
			ref, _ := appwire.ParseRef("local:thread")
			// The fake server intentionally lacks these methods, yielding stable
			// appwire errors and covering every command's error result branch.
			cmds := []tea.Cmd{
				fetchHubTree(client), fetchHubSession(frames, client, ref),
				subscribeChildActivity(client, ref.String()), fetchHubStatus(client, ref),
				fetchHubTranscriptTargets(client, ref),
				fetchHubModelsForHarness(client, "serf", "/tmp"),
				fetchHubSessionModels(client, "/tmp"), fetchHubSpawnOptions(client, "/tmp"),
				fetchHubTasks(client, ref), sendHubInput(client, ref, "x", "", nil),
			}
			for _, cmd := range cmds {
				_ = cmd()
			}

			m := newHubModel(client, "")
			for _, name := range []string{"new", "refresh", "credentials", "settings", "plugins"} {
				definition, _ := hubCommandByName(name)
				if cmd := definition.Run(&m, ""); cmd != nil && name != "settings" {
					_ = cmd()
				}
			}
			m.mode = hubModeSession
			m.detail.Ref = ref.String()
			definition, _ := hubCommandByName("model")
			_ = definition.Run(&m, "")()

			cleanup()
			_ = fetchHubSpawnOptions(client, "/tmp")()
			_ = waitHubNotification(frames)()
		case 8:
			client, cleanup := commandRegistryOptionsClient(t)
			defer cleanup()
			_ = fetchHubSpawnOptions(client, " /tmp ")()
			_ = modelPickerItemsFromResponse(appwire.ModelListResponse{
				Data:        []appwire.ModelDescriptor{{Model: "raw"}},
				Diagnostics: []appwire.ModelListDiagnostic{{Provider: "missing", Message: "message"}},
			}, true)
			_ = modelDiagnosticDisabledReason(appwire.ModelListDiagnostic{Message: "message"})

			bad := newHubModel(nil, "")
			bad.mode = hubModeSession
			bad.detail.ActiveTurnID = "turn"
			for _, name := range []string{"interrupt", "model"} {
				definition, _ := hubCommandByName(name)
				_ = definition.Run(&bad, "")
			}
			_ = bad.runHubGoal("clear")
			_ = sendHubInput(nil, appwire.Ref{}, "", "", []*clipboard.PastedImage{{Path: "/definitely/missing"}})()
		}
	})
}

func commandRegistryOptionsClient(t *testing.T) (*appwire.Client, func()) {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test", SourceID: "local"})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{{}, {ID: "blank-kind", EmptyTaskUnsupportedReason: "reason", EmptyTaskUnsupportedNextAction: "next"}}}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	return client, func() { _ = client.Close(); httpServer.Close() }
}

func entriesToItems(entries []commandPaletteEntry) []tuipick.PickerPanelItem {
	items := make([]tuipick.PickerPanelItem, len(entries))
	for i := range entries {
		items[i] = entries[i].Item
	}
	return items
}
