package hub

// Tests for wirePluginStoreBroadcast (issue #1634's hook): the plugin store's
// Manager.OnStoreChanged callback, wired here to the existing
// notifyMarketplaceUpdated/notifyPluginUpdated broadcasts. Driven with a
// recordingBroadcaster (app_host_admin_test.go) rather than a real connected
// client, the same seam hostNotificationBroadcaster gives the host-admin
// fan-out tests.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

// TestRegisterPluginAutoUpgradeHandlers_CheckNowBroadcastsARefreshWithNoUpgrade
// is "a write RPC broadcasts once", picked so the write and the broadcast it
// causes are attributable ONLY to wirePluginStoreBroadcast: checkNow's own
// per-handler code (app_plugin_autoupgrade.go) broadcasts evener/plugin/updated
// only when a plugin actually upgraded (none is installed here), and never
// broadcasts evener/marketplace/updated at all — yet the tick refreshes every
// marketplace, and RefreshMarketplace always writes known_marketplaces.json's
// LastUpdated, refreshed or not. Before this hook that write went out with no
// broadcast at all; this proves it now reaches clients exactly once, as the
// real evener/marketplace/updated notification.
func TestRegisterPluginAutoUpgradeHandlers_CheckNowBroadcastsARefreshWithNoUpgrade(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	broadcaster := newRecordingBroadcaster()
	wirePluginStoreBroadcast(ctl.mgr, broadcaster)

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerPluginAutoUpgradeHandlers(server, ctl.mgr)

	resp, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerPluginCheckNow,
	})
	if err != nil {
		t.Fatalf("dispatch checkNow: %v", err)
	}
	result, ok := resp.(appwire.PluginCheckNowResponse)
	if !ok {
		t.Fatalf("dispatch checkNow returned %T, want PluginCheckNowResponse", resp)
	}
	if len(result.Updated) != 0 {
		t.Fatalf("checkNow Updated = %v, want none (no plugin installed)", result.Updated)
	}
	got := broadcaster.broadcasts()
	if len(got) != 1 || got[0].method != appwire.NotifyEvenerMarketplaceUpdated {
		t.Fatalf("broadcasts = %+v, want exactly one %s", got, appwire.NotifyEvenerMarketplaceUpdated)
	}
}

// TestRunPluginAutoUpgradeTick_BroadcastsAfterAMarketplaceRefreshThatWrote is
// "the daemon tick broadcasts after a refresh that wrote": runPluginAutoUpgradeTick
// is the ticker-free core startPluginAutoUpgradeDaemon runs on every interval
// (and once on hub start), so exercising it directly against a wired Manager
// proves the periodic daemon gets the same broadcast checkNow does — the
// mechanism, not any ticker or RPC plumbing around it.
func TestRunPluginAutoUpgradeTick_BroadcastsAfterAMarketplaceRefreshThatWrote(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	broadcaster := newRecordingBroadcaster()
	wirePluginStoreBroadcast(ctl.mgr, broadcaster)

	if _, errs := runPluginAutoUpgradeTick(context.Background(), ctl.mgr, io.Discard); len(errs) != 0 {
		t.Fatalf("tick errors: %v", errs)
	}
	got := broadcaster.broadcasts()
	if len(got) != 1 || got[0].method != appwire.NotifyEvenerMarketplaceUpdated {
		t.Fatalf("broadcasts = %+v, want exactly one %s", got, appwire.NotifyEvenerMarketplaceUpdated)
	}
}

// TestHubSeedDefaults_ASeedThatWritesBroadcasts covers seedHubMarketplaces'
// own Manager (main_background.go): SeedDefaultMarketplaces writes
// known_marketplaces.json the first time a store has none, and — unlike
// hubPluginGC/hubSeedDefaults before this PR — that write now reaches a
// broadcast even though it runs before the hub ever calls ListenAndServe (no
// client could be connected yet either way; wiring it is what keeps that from
// being a special case future code has to remember).
func TestHubSeedDefaults_ASeedThatWritesBroadcasts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mgr := plugins.NewManager("")
	broadcaster := newRecordingBroadcaster()
	wirePluginStoreBroadcast(mgr, broadcaster)

	if err := hubSeedDefaults(context.Background(), mgr); err != nil {
		t.Fatalf("hubSeedDefaults: %v", err)
	}
	got := broadcaster.broadcasts()
	if len(got) != 1 || got[0].method != appwire.NotifyEvenerMarketplaceUpdated {
		t.Fatalf("broadcasts = %+v, want exactly one %s", got, appwire.NotifyEvenerMarketplaceUpdated)
	}
}

// plantRefusedMarketplace writes known_marketplaces.json directly (the way an
// older evener, or a hand edit, would have left it) naming a marketplace
// under name — a shape the store refuses today (e.g. one carrying '@') and
// renames the next time anything takes its lock. Mirrors the fixture
// app_plugins_test.go's "browsing an entry recorded under a traversing name"
// case builds inline.
func plantRefusedMarketplace(t *testing.T, mgr *plugins.Manager, name string) {
	t.Helper()
	if err := os.MkdirAll(mgr.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{name: map[string]any{
		"source":      map[string]any{"source": "url", "url": filepath.Join(t.TempDir(), "absent.git")},
		"lastUpdated": "2031-04-01T00:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mgr.Root, "known_marketplaces.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHubPluginGC_MigratesALegacyNameAndBroadcasts covers startHubPluginMaintenance's
// own Manager: Gc always takes the store lock (lockStore), which runs
// migrateMarketplaceNames before Gc itself does anything, so a store still
// holding an entry under a name evener refuses today gets renamed — writing
// both store files — on the very first Gc a hub start runs.
func TestHubPluginGC_MigratesALegacyNameAndBroadcasts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mgr := plugins.NewManager("")
	plantRefusedMarketplace(t, mgr, "foo@bar")

	broadcaster := newRecordingBroadcaster()
	wirePluginStoreBroadcast(mgr, broadcaster)

	if _, err := hubPluginGC(context.Background(), mgr); err != nil {
		t.Fatalf("hubPluginGC: %v", err)
	}
	got := broadcaster.broadcasts()
	foundMarketplace, foundPlugin := false, false
	for _, b := range got {
		switch b.method {
		case appwire.NotifyEvenerMarketplaceUpdated:
			foundMarketplace = true
		case appwire.NotifyEvenerPluginUpdated:
			foundPlugin = true
		}
	}
	if !foundMarketplace || !foundPlugin {
		t.Fatalf("broadcasts = %+v, want both %s and %s (the migration's saveRename writes both files)",
			got, appwire.NotifyEvenerMarketplaceUpdated, appwire.NotifyEvenerPluginUpdated)
	}
}

// TestNewWebServer_TwoServersEachResolveTheirOwnPluginManager is Medium 1's
// regression test on the design itself: cfg.PluginResolveManager
// (hubcore.WebConfig) is a value on each server's own cfg, not a package
// global, so a second server built later in the same process cannot answer
// for the first — the bug class a shared global (however carefully
// mutex-guarded) could not avoid.
func TestNewWebServer_TwoServersEachResolveTheirOwnPluginManager(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	web1 := NewWebServer(hubcore.WebConfig{PluginRoot: root1})
	mgr1 := web1.cfg.PluginResolveManager(root1)
	if mgr1 == nil || mgr1.Root != root1 {
		t.Fatalf("web1.cfg.PluginResolveManager(%q) = %+v, want a Manager rooted there", root1, mgr1)
	}

	web2 := NewWebServer(hubcore.WebConfig{PluginRoot: root2})
	mgr2 := web2.cfg.PluginResolveManager(root2)
	if mgr2 == nil || mgr2.Root != root2 {
		t.Fatalf("web2.cfg.PluginResolveManager(%q) = %+v, want a Manager rooted there", root2, mgr2)
	}
	if mgr1 == mgr2 {
		t.Fatal("web1 and web2 resolved the same *plugins.Manager")
	}

	// The last-constructed server (web2) must not be able to answer for the
	// first: re-resolving through web1's own cfg after web2 exists must
	// still return web1's own Manager.
	if again := web1.cfg.PluginResolveManager(root1); again != mgr1 {
		t.Fatalf("web1.cfg.PluginResolveManager(%q) changed after web2 was constructed: %p -> %p", root1, mgr1, again)
	}
}

// TestRegisterPluginHandlers_MarketplaceEditBroadcastsExactlyOnce is Medium
// 2's regression test: registerPluginHandlers makes no notify call of its
// own any more (deleted; the hook is the sole path), so a re-source-only
// edit — which writes known_marketplaces.json alone (EditMarketplace's
// non-renaming branch never calls saveRegistry) — must broadcast exactly
// once, not twice.
func TestRegisterPluginHandlers_MarketplaceEditBroadcastsExactlyOnce(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)
	dir2 := t.TempDir()
	writeTestMarketplaceManifest(t, dir2, "acme-resourced", "[]")

	broadcaster := newRecordingBroadcaster()
	wirePluginStoreBroadcast(ctl.mgr, broadcaster)

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerPluginHandlers(server, ctl)

	resp, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerMarketplaceEdit,
		Params: mustMarshal(t, appwire.MarketplaceEditParams{
			Name:   "acme",
			Source: &appwire.MarketplaceSourceInput{Kind: "directory", Path: dir2},
		}),
	})
	if err != nil {
		t.Fatalf("dispatch edit: %v", err)
	}
	if _, ok := resp.(appwire.MarketplaceListResponse); !ok {
		t.Fatalf("dispatch edit returned %T, want MarketplaceListResponse", resp)
	}

	got := broadcaster.broadcasts()
	if len(got) != 1 || got[0].method != appwire.NotifyEvenerMarketplaceUpdated {
		t.Fatalf("broadcasts = %+v, want exactly one %s (not doubled by a manual notify call)",
			got, appwire.NotifyEvenerMarketplaceUpdated)
	}
}
