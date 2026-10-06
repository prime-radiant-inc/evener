package tui

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
)

// runPluginsOpen opens /plugins on m and feeds every message its initial
// reads produce back through the model, delivering them in the worst order
// the program allows (openCommandMessages).
func runPluginsOpen(t *testing.T, m hubModel) hubModel {
	t.Helper()
	plugins, ok := hubCommandByName("plugins")
	if !ok {
		t.Fatal("registry missing /plugins")
	}
	run := plugins.Run(&m, "")
	if run == nil {
		t.Fatal("/plugins should issue its initial reads")
	}
	for _, msg := range openCommandMessages(t, run) {
		updated, _ := m.Update(msg)
		m = updated.(hubModel)
	}
	return m
}

// openCommandMessages runs cmd and returns the messages it produces in an
// order the program could deliver them. A batch's commands run in the order
// given, but nothing orders their answers, so they are delivered last first: an
// answer read early can land after one read later. A sequence's commands run
// and deliver in order. tea.Sequence's message type is unexported, so it is
// read as a slice of commands (as TestQuitClearsWindowTitleBeforeQuitting does).
func openCommandMessages(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	msg := cmd()
	if msg == nil {
		return nil
	}
	children := reflect.ValueOf(msg)
	if children.Kind() != reflect.Slice {
		return []tea.Msg{msg}
	}
	var parts [][]tea.Msg
	for i := range children.Len() {
		child, ok := reflect.TypeAssert[tea.Cmd](children.Index(i))
		if !ok {
			return []tea.Msg{msg}
		}
		parts = append(parts, openCommandMessages(t, child))
	}
	if _, batch := msg.(tea.BatchMsg); batch {
		slices.Reverse(parts)
	}
	return slices.Concat(parts...)
}

// installedTabView renders m's plugins panel on its Installed tab.
func installedTabView(t *testing.T, m hubModel) string {
	t.Helper()
	if m.pluginsPanel == nil {
		t.Fatal("/plugins did not open the panel")
	}
	var panel tea.Model = *m.pluginsPanel
	for range 2 {
		panel, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	return panel.View()
}

// Opening the plugins panel asks the hub which plugins have updates, as the
// web and phone plugins views do, so the panel can offer Upgrade only there.
func TestPluginsPanelOpenChecksForUpdates(t *testing.T) {
	// Like the hub, plugin/list reports what the last check found.
	var mu sync.Mutex
	plugin := appwire.PluginEntry{Plugin: "widget", Marketplace: "acme", Enabled: true}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{plugin}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginCheckUpdates, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			plugin.UpdateAvailable = true
			// An empty answer, so only the panel's re-read of the list can
			// show the flag.
			return appwire.PluginListResponse{}, nil
		})
	})
	defer cleanup()

	m := runPluginsOpen(t, hubModel{client: client})
	if v := installedTabView(t, m); !strings.Contains(v, "UPDATE AVAILABLE") {
		t.Fatalf("panel after opening does not show the checked update:\n%s", v)
	}
}

// A hub without evener/plugin/checkUpdates (an older one) answers it with an
// error; the panel keeps its list and flags nothing.
func TestPluginsPanelOpenKeepsTheListWhenTheUpdateCheckFails(t *testing.T) {
	plugin := appwire.PluginEntry{Plugin: "widget", Marketplace: "acme", Enabled: true}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{plugin}}, nil
		})
	})
	defer cleanup()

	m := runPluginsOpen(t, hubModel{client: client})
	v := installedTabView(t, m)
	if !strings.Contains(v, "widget") || strings.Contains(v, "Error") || strings.Contains(v, "UPDATE AVAILABLE") {
		t.Fatalf("a failed update check changed the panel:\n%s", v)
	}
}

// A check lost with a dropped connection answers nothing, so an open plugins
// panel would show no update until it was reopened: the reconnect reads the
// list and checks for updates again, as opening the panel does.
func TestPluginsPanelReconnectChecksForUpdatesAgain(t *testing.T) {
	var mu sync.Mutex
	checks := 0
	plugin := appwire.PluginEntry{Plugin: "widget", Marketplace: "acme", Enabled: true}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{plugin}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginCheckUpdates, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			checks++
			plugin.UpdateAvailable = true
			return appwire.PluginListResponse{}, nil
		})
	})
	defer cleanup()
	// The model sits on a connection that has dropped, its check with it.
	oldClient, _, dropOldConnection := newTestHubClientWithFeed(t, nil)
	panel := launchconfig.NewPluginsPanel()
	m := hubModel{client: oldClient, pluginsPanel: &panel}
	dropOldConnection()

	cmd := m.applyHubReconnect(hubReconnectMsg{client: client, frames: feed})
	if cmd == nil {
		t.Fatal("reconnect should schedule its recovery reads")
	}
	// The reconnect batches long-lived listeners (the frame pump) with its
	// reads, so each batched command runs on its own and only those that
	// answer promptly are delivered, in the order they finish.
	answers := make(chan []tea.Msg, 16)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("reconnect command = %T, want a batch", cmd)
	}
	for _, child := range batch {
		if child != nil {
			go func() { answers <- openCommandMessages(t, child) }()
		}
	}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case msgs := <-answers:
			for _, msg := range msgs {
				updated, _ := m.Update(msg)
				m = updated.(hubModel)
			}
		case <-deadline:
			break collect
		}
	}
	mu.Lock()
	ran := checks
	mu.Unlock()
	if ran != 1 {
		t.Fatalf("update checks after reconnect = %d, want 1", ran)
	}
	if v := installedTabView(t, m); !strings.Contains(v, "UPDATE AVAILABLE") {
		t.Fatalf("panel after reconnect does not show the checked update:\n%s", v)
	}
}
