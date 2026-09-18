package hub

// Tests for wirePluginStoreBroadcast (issue #1634's hook): the plugin store's
// Manager.OnStoreChanged callback, wired here to the existing
// notifyMarketplaceUpdated/notifyPluginUpdated broadcasts. Both are vars
// (app_rpc.go) precisely so a test can observe them without a real connected
// client.

import (
	"context"
	"io"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// stubNotifies overrides notifyMarketplaceUpdated/notifyPluginUpdated for the
// duration of the calling test, counting each, and restores the originals on
// cleanup. Not for a parallel test: the two are package-level vars.
func stubNotifies(t *testing.T) (marketplaceCalls, pluginCalls *int) {
	t.Helper()
	origMkt, origPlg := notifyMarketplaceUpdated, notifyPluginUpdated
	marketplaceCalls, pluginCalls = new(int), new(int)
	notifyMarketplaceUpdated = func(*appserver.Server) { *marketplaceCalls++ }
	notifyPluginUpdated = func(*appserver.Server) { *pluginCalls++ }
	t.Cleanup(func() { notifyMarketplaceUpdated, notifyPluginUpdated = origMkt, origPlg })
	return marketplaceCalls, pluginCalls
}

// TestRegisterPluginAutoUpgradeHandlers_CheckNowBroadcastsARefreshWithNoUpgrade
// is "a write RPC broadcasts once", picked so the write and the broadcast it
// causes are attributable ONLY to wirePluginStoreBroadcast: checkNow's own
// per-handler code (app_plugin_autoupgrade.go) broadcasts evener/plugin/updated
// only when a plugin actually upgraded (none is installed here), and never
// broadcasts evener/marketplace/updated at all — yet the tick refreshes every
// marketplace, and RefreshMarketplace always writes known_marketplaces.json's
// LastUpdated, refreshed or not. Before this hook that write went out with no
// broadcast at all; this proves it now reaches clients exactly once.
func TestRegisterPluginAutoUpgradeHandlers_CheckNowBroadcastsARefreshWithNoUpgrade(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)
	marketplaceCalls, pluginCalls := stubNotifies(t)

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	wirePluginStoreBroadcast(ctl.mgr, server)
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
	if *marketplaceCalls != 1 {
		t.Fatalf("notifyMarketplaceUpdated called %d times, want 1", *marketplaceCalls)
	}
	if *pluginCalls != 0 {
		t.Fatalf("notifyPluginUpdated called %d times, want 0 (no plugin upgraded)", *pluginCalls)
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
	marketplaceCalls, pluginCalls := stubNotifies(t)

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	wirePluginStoreBroadcast(ctl.mgr, server)

	if _, errs := runPluginAutoUpgradeTick(context.Background(), ctl.mgr, io.Discard); len(errs) != 0 {
		t.Fatalf("tick errors: %v", errs)
	}
	if *marketplaceCalls != 1 {
		t.Fatalf("notifyMarketplaceUpdated called %d times, want 1", *marketplaceCalls)
	}
	if *pluginCalls != 0 {
		t.Fatalf("notifyPluginUpdated called %d times, want 0 (no plugin installed)", *pluginCalls)
	}
}
