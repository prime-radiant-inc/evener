package hub

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
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
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

	removeResp, err := ctl.RemoveMarketplace(context.Background(), appwire.MarketplaceNameParams{Name: "acme"})
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
	_, err := ctl.RemoveMarketplace(context.Background(), appwire.MarketplaceNameParams{Name: "nope"})
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

// Browsing a marketplace classifies the manager's refusals the way adding,
// editing, removing and refreshing one do. Browse takes a name from the caller,
// so an unknown one is the caller's mistake — including the name an entry was
// recorded under before the store renamed it.
func TestPlugins_Marketplace_BrowseRefusalsAreWireErrors(t *testing.T) {
	ctl := newTestPluginsController(t)
	ctx := context.Background()

	t.Run("browsing an unknown marketplace", func(t *testing.T) {
		_, err := ctl.Browse(ctx, appwire.MarketplaceBrowseParams{Name: "nope"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("Browse = %v, want an InvalidParams wire error", err)
		}
	})

	// A marketplaces file an older evener or a hand edit left with a
	// traversing name. Taking the store lock renames such an entry before the
	// browse looks it up, so the recorded name is unknown by then and the
	// listing shows what it became. The url source names a path that does not
	// exist, so nothing here reaches out.
	t.Run("browsing an entry recorded under a traversing name", func(t *testing.T) {
		ctl.mgr.Stderr = io.Discard
		store := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "plugins")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{"../../escape": map[string]any{
			"source":      map[string]any{"source": "url", "url": filepath.Join(t.TempDir(), "absent.git")},
			"lastUpdated": "2031-04-01T00:00:00Z",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "known_marketplaces.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = ctl.Browse(ctx, appwire.MarketplaceBrowseParams{Name: "../../escape"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("Browse = %v, want an InvalidParams wire error", err)
		}
		list, err := ctl.ListMarketplaces()
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Marketplaces) != 1 || list.Marketplaces[0].Name != "escape" {
			t.Fatalf("marketplaces = %+v, want the one entry renamed to escape", list.Marketplaces)
		}
	})
}

// TestPlugins_ConcurrentAddMarketplace_NoLostUpdate exercises the claim
// behind hubPluginsController holding no mutex of its own (see app_plugins.go's
// doc comment): internal/plugins.Manager's own per-root store flock — not an
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

	disableResp, err := ctl.Disable(context.Background(), ref)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if disableResp.Plugins[0].Enabled {
		t.Error("entry still enabled after Disable")
	}

	enableResp, err := ctl.Enable(context.Background(), ref)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !enableResp.Plugins[0].Enabled {
		t.Error("entry not enabled after Enable")
	}

	autoResp, err := ctl.SetAutoUpgrade(context.Background(), appwire.PluginSetAutoUpgradeParams{Plugin: "widget", Marketplace: "acme", AutoUpgrade: true})
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

	removeResp, err := ctl.Remove(context.Background(), ref)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removeResp.Plugins) != 0 {
		t.Fatalf("Remove response = %+v, want empty", removeResp.Plugins)
	}
}

func TestPlugins_Remove_Unknown_Errors(t *testing.T) {
	ctl := newTestPluginsController(t)
	_, err := ctl.Remove(context.Background(), appwire.PluginRefParams{Plugin: "nope", Marketplace: "nowhere"})
	if err == nil {
		t.Fatal("expected error removing unknown plugin, got nil")
	}
}

func TestPlugins_Marketplace_EditRenamesAndReturnsTheList(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	resp, err := ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "acme2"})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if len(resp.Marketplaces) != 1 || resp.Marketplaces[0].Name != "acme2" {
		t.Fatalf("EditMarketplace response = %+v, want one entry named acme2", resp.Marketplaces)
	}
	if resp.Marketplaces[0].Source.Kind != "directory" || resp.Marketplaces[0].Source.Path != dir {
		t.Fatalf("Source = %+v", resp.Marketplaces[0].Source)
	}
}

// A present Source is the only path through marketplaceSourceFromWire on this
// method, and the frontend's re-source flow is entirely that path.
func TestPlugins_Marketplace_EditReplacesTheSource(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)
	moved := t.TempDir()
	writeTestMarketplace(t, moved)

	resp, err := ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{
		Name:   "acme",
		Source: &appwire.MarketplaceSourceInput{Kind: "directory", Path: moved},
	})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if len(resp.Marketplaces) != 1 || resp.Marketplaces[0].Name != "acme" {
		t.Fatalf("EditMarketplace response = %+v, want one entry named acme", resp.Marketplaces)
	}
	if got := resp.Marketplaces[0].Source; got.Kind != "directory" || got.Path != moved {
		t.Fatalf("Source = %+v, want directory %q", got, moved)
	}
}

func TestPlugins_Marketplace_EditRefusalsAreWireErrors(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)
	other := t.TempDir()
	writeTestMarketplaceManifest(t, other, "beta", "[]")
	if _, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: other},
	}); err != nil {
		t.Fatalf("AddMarketplace beta: %v", err)
	}

	_, err := ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "beta"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("rename onto a taken name = %v, want a Conflict wire error", err)
	}
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "nope", NewName: "x"})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unknown marketplace = %v, want an InvalidParams wire error", err)
	}
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "../escape"})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a traversing new name = %v, want an InvalidParams wire error", err)
	}
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: ".old"})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a new name that is the edit's own scratch directory = %v, want an InvalidParams wire error", err)
	}
	// The store's own managed-clone directory, which newTestPluginsController
	// puts under XDG_CONFIG_HOME the way production's default root resolution does.
	marketplaces := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "plugins", "marketplaces")
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{
		Name:   "acme",
		Source: &appwire.MarketplaceSourceInput{Kind: "directory", Path: filepath.Join(marketplaces, "acme")},
	})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a source inside the managed clone = %v, want an InvalidParams wire error", err)
	}
	// The swap's scratch directory: inside the store, though it is no
	// marketplace's clone.
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{
		Name:   "acme",
		Source: &appwire.MarketplaceSourceInput{Kind: "directory", Path: filepath.Join(marketplaces, ".old")},
	})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a source in the store's scratch directory = %v, want an InvalidParams wire error", err)
	}
	// A clone a removed marketplace left behind still occupies gamma, so that
	// name is taken on disk though no marketplace records it.
	if err := os.MkdirAll(filepath.Join(marketplaces, "gamma"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "gamma"})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("a leftover under the new name = %v, want a Conflict wire error", err)
	}
}

// Adding a marketplace classifies the manager's refusals the way editing one
// does: a source inside the store and a name the store cannot carry are the
// caller's mistakes, and the sheet can only say so if the hub says so. The add
// path has no Conflict case to check — re-adding a registered name re-points
// it at the new source rather than refusing it.
func TestPlugins_Marketplace_AddRefusalsAreWireErrors(t *testing.T) {
	ctl := newTestPluginsController(t)
	var wire appwire.WireError

	// A source inside the store's own managed-clone directory, holding a real
	// catalog, so the refusal is the containment rule's and not a parse failure.
	marketplaces := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "plugins", "marketplaces")
	inStore := filepath.Join(marketplaces, "acme")
	writeTestMarketplaceManifest(t, inStore, "acme", "[]")
	_, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: inStore},
	})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a source inside the store = %v, want an InvalidParams wire error", err)
	}

	// A fetched catalog naming the marketplace with an '@', which separates
	// plugin from marketplace in an installed-plugin key.
	dir := t.TempDir()
	writeTestMarketplaceManifest(t, dir, "ac@me", "[]")
	_, err = ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dir},
	})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("a catalog name the store cannot carry = %v, want an InvalidParams wire error", err)
	}
}

// Removing and refreshing a marketplace classify the manager's refusals the way
// adding and editing one do. Both take a name from the caller, so an unknown one
// is the caller's mistake; and both derive a store directory from the name the
// entry is recorded under, so an entry recorded under a name the store cannot
// carry is refused rather than acted on.
func TestPlugins_Marketplace_RemoveAndRefreshRefusalsAreWireErrors(t *testing.T) {
	ctl := newTestPluginsController(t)
	ctx := context.Background()

	t.Run("removing an unknown marketplace", func(t *testing.T) {
		_, err := ctl.RemoveMarketplace(ctx, appwire.MarketplaceNameParams{Name: "nope"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("RemoveMarketplace = %v, want an InvalidParams wire error", err)
		}
	})

	t.Run("refreshing an unknown marketplace", func(t *testing.T) {
		_, err := ctl.RefreshMarketplace(ctx, appwire.MarketplaceNameParams{Name: "nope"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("RefreshMarketplace = %v, want an InvalidParams wire error", err)
		}
	})

	// A registry an older evener or a hand edit left with a traversing key: the
	// clone a removal would delete is <marketplaces>/../../escape, outside the
	// store, so the manager refuses the recorded name. A directory source keeps
	// the plant harmless — that branch deletes no clone even when it runs.
	t.Run("removing an entry recorded under a traversing name", func(t *testing.T) {
		store := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "plugins")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		recorded := t.TempDir()
		writeTestMarketplaceManifest(t, recorded, "escape", "[]")
		body, err := json.Marshal(map[string]any{"../../escape": map[string]any{
			"source":          map[string]any{"source": "directory", "path": recorded},
			"installLocation": recorded,
			"lastUpdated":     "2031-04-01T00:00:00Z",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "known_marketplaces.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = ctl.RemoveMarketplace(ctx, appwire.MarketplaceNameParams{Name: "../../escape"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("RemoveMarketplace = %v, want an InvalidParams wire error", err)
		}
	})
}
