package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// runPluginsOpen opens /plugins on m and feeds every message its initial
// reads produce back through the model, as the program would.
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
	for _, c := range run().(tea.BatchMsg) {
		if msg := c(); msg != nil {
			updated, _ := m.Update(msg)
			m = updated.(hubModel)
		}
	}
	return m
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
	plugin := appwire.PluginEntry{Plugin: "widget", Marketplace: "acme", Enabled: true}
	checked := plugin
	checked.UpdateAvailable = true
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{plugin}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginCheckUpdates, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{checked}}, nil
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
