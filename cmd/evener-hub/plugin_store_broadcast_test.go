package hub

// Tests for wirePluginStoreBroadcast (issue #1634's hook): the plugin store's
// Manager.OnStoreChanged callback, wired here to the existing
// notifyMarketplaceUpdated/notifyPluginUpdated broadcasts. Most of these
// drive it with a recordingBroadcaster (app_host_admin_test.go) rather than a
// real connected client, the same seam hostNotificationBroadcaster gives the
// host-admin fan-out tests; TestNewWebServer_WiresPluginStoreBroadcastToItsOwnServer
// drives the production NewWebServer path end to end instead.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
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
	assertOneBroadcast(t, broadcaster, appwire.NotifyEvenerMarketplaceUpdated)
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
	assertOneBroadcast(t, broadcaster, appwire.NotifyEvenerMarketplaceUpdated)
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
	assertOneBroadcast(t, broadcaster, appwire.NotifyEvenerMarketplaceUpdated)
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
	assertBroadcastMethods(t, broadcaster, appwire.NotifyEvenerMarketplaceUpdated, appwire.NotifyEvenerPluginUpdated)
}

// TestNewWebServer_TwoServersEachResolveTheirOwnPluginManager proves
// cfg.PluginManager (hubcore.WebConfig) is a value on each server's own cfg,
// not a package global, so a second server built later in the same process
// cannot answer for the first.
func TestNewWebServer_TwoServersEachResolveTheirOwnPluginManager(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	web1 := NewWebServer(hubcore.WebConfig{PluginRoot: root1})
	mgr1 := web1.cfg.PluginManager
	if mgr1 == nil || mgr1.Root != root1 {
		t.Fatalf("web1.cfg.PluginManager = %+v, want a Manager rooted at %q", mgr1, root1)
	}

	web2 := NewWebServer(hubcore.WebConfig{PluginRoot: root2})
	mgr2 := web2.cfg.PluginManager
	if mgr2 == nil || mgr2.Root != root2 {
		t.Fatalf("web2.cfg.PluginManager = %+v, want a Manager rooted at %q", mgr2, root2)
	}
	if mgr1 == mgr2 {
		t.Fatal("web1 and web2 share the same *plugins.Manager")
	}

	// The last-constructed server (web2) must not be able to answer for the
	// first: web1's own cfg still holds its own Manager after web2 exists.
	if web1.cfg.PluginManager != mgr1 {
		t.Fatalf("web1.cfg.PluginManager changed after web2 was constructed: %p -> %p", mgr1, web1.cfg.PluginManager)
	}
}

// TestRegisterPluginHandlers_MarketplaceEditBroadcastsExactlyOnce proves a
// re-source-only edit — which writes known_marketplaces.json alone
// (EditMarketplace's non-renaming branch never calls saveRegistry) —
// broadcasts exactly once: registerPluginHandlers makes no notify call of
// its own, so wirePluginStoreBroadcast is the sole path.
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

	assertOneBroadcast(t, broadcaster, appwire.NotifyEvenerMarketplaceUpdated)
}

// TestNewHubAppServer_NilPluginManagerFallbackBroadcasts proves the
// nil-cfg.PluginManager fallback in newHubAppServerWithNavigationAndTrace
// (app_rpc.go:519-523) wires wirePluginStoreBroadcast just like newWebServer's
// own cfg.PluginManager path does. Every other test in this file either
// injects its own Manager or drives NewWebServer, which pre-sets
// cfg.PluginManager before this constructor ever runs
// (TestNewWebServer_WiresPluginStoreBroadcastToItsOwnServer) — neither would
// notice if the fallback's own wirePluginStoreBroadcast call were deleted.
// This calls newHubAppServer directly, the same way a caller that never goes
// through NewWebServer (most tests, and any embedder using
// newHubAppServer/newHubAppServerWithNavigation) does, leaving
// cfg.PluginManager nil, and drives a real client connection over it.
func TestNewHubAppServer_NilPluginManagerFallbackBroadcasts(t *testing.T) {
	dir := t.TempDir()
	writeTestMarketplace(t, dir)

	server := newHubAppServer(hubcore.WebConfig{PluginRoot: t.TempDir()}, appsource.NewRegistry())
	hub := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.MarketplaceAdd(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dir},
	}); err != nil {
		t.Fatalf("MarketplaceAdd: %v", err)
	}

	select {
	case notif := <-client.Notifications():
		if notif.Method != appwire.NotifyEvenerMarketplaceUpdated {
			t.Fatalf("notification method = %q, want %q", notif.Method, appwire.NotifyEvenerMarketplaceUpdated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for evener/marketplace/updated: the nil-PluginManager fallback never wired a broadcast")
	}
}

// TestNewWebServer_WiresPluginStoreBroadcastToItsOwnServer proves web.go's
// production wiring (newWebServer's wirePluginStoreBroadcast call, made after
// the server exists) actually reaches a connected client. Every other test
// in this file injects its own Manager or recordingBroadcaster, so none of
// them would notice if that one line were deleted; this drives the real
// NewWebServer path — no injected manager, no injected broadcaster — over an
// actual client connection instead.
func TestNewWebServer_WiresPluginStoreBroadcastToItsOwnServer(t *testing.T) {
	dir := t.TempDir()
	writeTestMarketplace(t, dir)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:       hubcore.NewPastIndex(""),
		PluginRoot: t.TempDir(),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.MarketplaceAdd(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: dir},
	}); err != nil {
		t.Fatalf("MarketplaceAdd: %v", err)
	}

	select {
	case notif := <-client.Notifications():
		if notif.Method != appwire.NotifyEvenerMarketplaceUpdated {
			t.Fatalf("notification method = %q, want %q", notif.Method, appwire.NotifyEvenerMarketplaceUpdated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for evener/marketplace/updated: the store write never reached this client")
	}
}
