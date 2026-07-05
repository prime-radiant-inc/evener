package main

// Tests for hubPluginsController: marketplace + plugin lifecycle CRUD wired
// over internal/plugins.Manager. Mirrors app_instances_test.go's shape.
//
// Fixtures use a directory-source marketplace (a plain dir with
// .claude-plugin/marketplace.json, referencing a "./plugins/widget" plugin in
// place) so the whole lifecycle — add marketplace, browse, install,
// enable/disable, autoUpgrade, upgrade, remove — runs with no git dependency
// and no network access.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
)

// newTestPluginsController points XDG_CONFIG_HOME at a fresh temp dir so
// plugins.NewManager("")'s default-root resolution lands under it — the same
// production wiring path newHubPluginsController("") uses — giving each test
// a fully isolated plugins store.
func newTestPluginsController(t *testing.T) *hubPluginsController {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return newHubPluginsController("")
}

// writeTestPluginManifest writes a minimal valid plugin manifest at dir.
func writeTestPluginManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"` + name + `","version":"1.0.0"}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestMarketplaceManifest writes a minimal directory-source marketplace
// manifest at dir under name, with the given raw JSON plugins-array body
// (e.g. "[]" or a full catalog-plugin list).
func writeTestMarketplaceManifest(t *testing.T, dir, name, pluginsJSON string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","owner":{"name":"o"},"plugins":` + pluginsJSON + `}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestMarketplace writes a directory-source marketplace named "acme" at
// dir, with one catalog plugin ("widget") referenced by a relative
// "./plugins/widget" source.
func writeTestMarketplace(t *testing.T, dir string) {
	t.Helper()
	writeTestMarketplaceManifest(t, dir, "acme", `[{"name":"widget","description":"a widget","category":"tools","source":"./plugins/widget"}]`)
	writeTestPluginManifest(t, filepath.Join(dir, "plugins", "widget"), "widget")
}

// addTestMarketplace registers the writeTestMarketplace fixture at dir via
// the controller, failing the test on error.
func addTestMarketplace(t *testing.T, ctl *hubPluginsController, dir string) {
	t.Helper()
	if _, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dir},
	}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Marketplaces
// ─────────────────────────────────────────────────────────────────────────────

func TestPlugins_Marketplace_AddListRemove(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)

	addResp, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dir},
	})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if len(addResp.Marketplaces) != 1 || addResp.Marketplaces[0].Name != "acme" {
		t.Fatalf("AddMarketplace response = %+v, want one entry named acme", addResp.Marketplaces)
	}
	entry := addResp.Marketplaces[0]
	if entry.Source.Kind != "directory" || entry.Source.Path != dir {
		t.Errorf("Source = %+v, want directory %q", entry.Source, dir)
	}
	if entry.LastUpdated == 0 {
		t.Error("LastUpdated not set after Add")
	}

	listResp, err := ctl.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(listResp.Marketplaces) != 1 {
		t.Fatalf("ListMarketplaces = %+v, want 1 entry", listResp.Marketplaces)
	}

	removeResp, err := ctl.RemoveMarketplace(appwire.MarketplaceNameParams{Name: "acme"})
	if err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if len(removeResp.Marketplaces) != 0 {
		t.Fatalf("RemoveMarketplace response = %+v, want empty", removeResp.Marketplaces)
	}
}

func TestPlugins_Marketplace_AddInvalidKind_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Name:   "bad",
		Source: appwire.MarketplaceSourceInput{Kind: "not-a-real-kind"},
	})
	if err == nil {
		t.Fatal("expected error for unknown source kind, got nil")
	}
}

func TestPlugins_Marketplace_RemoveUnknown_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.RemoveMarketplace(appwire.MarketplaceNameParams{Name: "nope"})
	if err == nil {
		t.Fatal("expected error removing unknown marketplace, got nil")
	}
}

func TestPlugins_Marketplace_Refresh(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	resp, err := ctl.RefreshMarketplace(context.Background(), appwire.MarketplaceNameParams{Name: "acme"})
	if err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	if len(resp.Marketplaces) != 1 {
		t.Fatalf("RefreshMarketplace response = %+v, want 1 entry", resp.Marketplaces)
	}
}

func TestPlugins_Marketplace_RefreshUnknown_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.RefreshMarketplace(context.Background(), appwire.MarketplaceNameParams{Name: "nope"})
	if err == nil {
		t.Fatal("expected error refreshing unknown marketplace, got nil")
	}
}

func TestPlugins_Marketplace_Browse(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	resp, err := ctl.Browse(context.Background(), appwire.MarketplaceBrowseParams{Name: "acme"})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if resp.Name != "acme" {
		t.Errorf("Name = %q, want acme", resp.Name)
	}
	if len(resp.Plugins) != 1 || resp.Plugins[0].Name != "widget" {
		t.Fatalf("Plugins = %+v, want one entry named widget", resp.Plugins)
	}
	if resp.Plugins[0].Description != "a widget" || resp.Plugins[0].Category != "tools" {
		t.Errorf("Plugins[0] = %+v, missing description/category", resp.Plugins[0])
	}
}

func TestPlugins_Marketplace_BrowseUnknown_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.Browse(context.Background(), appwire.MarketplaceBrowseParams{Name: "nope"})
	if err == nil {
		t.Fatal("expected error browsing unknown marketplace, got nil")
	}
}

// TestPlugins_ConcurrentAddMarketplace_NoLostUpdate exercises the claim
// behind hubPluginsController holding no mutex of its own (see app_plugins.go's
// doc comment): internal/plugins.Manager's single per-root flock — not an
// in-process mutex — is what serializes concurrent mutations, in-process or
// not. Two goroutines register distinct marketplaces on the same controller
// concurrently; both must succeed and both must land in the registry. A lost
// update (or a race flagged under -race) would mean the flock alone is not
// sufficient and the removed controller-level mutex was load-bearing after
// all.
func TestPlugins_ConcurrentAddMarketplace_NoLostUpdate(t *testing.T) {
	ctl := newTestPluginsController(t)
	dirAcme, dirBeta := t.TempDir(), t.TempDir()
	writeTestMarketplace(t, dirAcme)                       // registers as "acme"
	writeTestMarketplaceManifest(t, dirBeta, "beta", "[]") // no catalog plugins needed

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
			Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dirAcme},
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
			Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dirBeta},
		})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AddMarketplace[%d]: %v", i, err)
		}
	}

	resp, err := ctl.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	names := make(map[string]bool, len(resp.Marketplaces))
	for _, m := range resp.Marketplaces {
		names[m.Name] = true
	}
	if !names["acme"] || !names["beta"] {
		t.Fatalf("ListMarketplaces = %+v, want both acme and beta", resp.Marketplaces)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Plugins
// ─────────────────────────────────────────────────────────────────────────────

func TestPlugins_ListPlugins_Empty(t *testing.T) {
	ctl := newTestPluginsController(t)
	resp, err := ctl.ListPlugins()
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(resp.Plugins) != 0 {
		t.Fatalf("ListPlugins = %+v, want empty", resp.Plugins)
	}
}

func TestPlugins_Install_UnknownPlugin_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	_, err := ctl.Install(context.Background(), appwire.PluginRefParams{Plugin: "nonexistent", Marketplace: "acme"})
	if err == nil {
		t.Fatal("expected error installing unknown plugin, got nil")
	}
}

func TestPlugins_Lifecycle_InstallEnableDisableAutoUpgradeUpgradeRemove(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}

	installResp, err := ctl.Install(context.Background(), ref)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(installResp.Plugins) != 1 {
		t.Fatalf("Install response = %+v, want 1 entry", installResp.Plugins)
	}
	entry := installResp.Plugins[0]
	if entry.Plugin != "widget" || entry.Marketplace != "acme" {
		t.Errorf("entry = %+v, want widget@acme", entry)
	}
	if !entry.Enabled {
		t.Error("installed entry not enabled")
	}
	if entry.Broken {
		t.Error("installed entry reported broken")
	}
	if entry.InstalledAt == 0 {
		t.Error("InstalledAt not set")
	}

	disableResp, err := ctl.Disable(ref)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if disableResp.Plugins[0].Enabled {
		t.Error("entry still enabled after Disable")
	}

	enableResp, err := ctl.Enable(ref)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !enableResp.Plugins[0].Enabled {
		t.Error("entry not enabled after Enable")
	}

	autoResp, err := ctl.SetAutoUpgrade(appwire.PluginSetAutoUpgradeParams{Plugin: "widget", Marketplace: "acme", AutoUpgrade: true})
	if err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	if !autoResp.Plugins[0].AutoUpgrade {
		t.Error("autoUpgrade not set")
	}

	// A directory-source plugin's "upgrade" is inherently current (a true
	// no-op per internal/plugins' design) but must succeed, not error.
	upgradeResp, err := ctl.Upgrade(context.Background(), ref)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(upgradeResp.Plugins) != 1 {
		t.Fatalf("Upgrade response = %+v, want 1 entry", upgradeResp.Plugins)
	}

	removeResp, err := ctl.Remove(ref)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removeResp.Plugins) != 0 {
		t.Fatalf("Remove response = %+v, want empty", removeResp.Plugins)
	}
}

func TestPlugins_Remove_Unknown_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.Remove(appwire.PluginRefParams{Plugin: "nope", Marketplace: "nowhere"})
	if err == nil {
		t.Fatal("expected error removing unknown plugin, got nil")
	}
}
