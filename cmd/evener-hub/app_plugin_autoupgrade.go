package hub

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

type pluginAutoUpgradeTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realPluginAutoUpgradeTicker struct{ *time.Ticker }

func (t realPluginAutoUpgradeTicker) Chan() <-chan time.Time { return t.C }

var newPluginAutoUpgradeTicker = func(interval time.Duration) pluginAutoUpgradeTicker {
	return realPluginAutoUpgradeTicker{time.NewTicker(interval)}
}

var pluginAutoUpgradeTick = runPluginAutoUpgradeTick

var (
	pluginListMarketplaces = func(ctx context.Context, mgr *plugins.Manager) (map[string]plugins.MarketplaceRef, plugins.StoreChanges, error) {
		return mgr.ListMarketplaces(ctx)
	}
	pluginRefreshMarketplace = func(ctx context.Context, mgr *plugins.Manager, name string) (plugins.StoreChanges, error) {
		return mgr.RefreshMarketplace(ctx, name)
	}
	pluginUpdateAutoUpgrade = func(ctx context.Context, mgr *plugins.Manager) ([]plugins.UpgradedPlugin, plugins.StoreChanges, error) {
		return mgr.UpdateAutoUpgrade(ctx)
	}
)

// runPluginAutoUpgradeTick is the plain, timer-free core of the auto-upgrade
// daemon (design doc §9.1): refresh every known marketplace, then upgrade
// every installed, git-backed plugin that has autoUpgrade enabled. It never
// returns an error itself — marketplace-refresh and per-plugin upgrade
// failures are collected into errs and written to stderr, so one bad
// marketplace or plugin never blocks the others (failure-isolated; the
// per-plugin isolation is inherited from plugins.Manager.UpdateAutoUpgrade).
//
// changes folds the listing, every refresh and the upgrade sweep's own
// answers into one: the daemon and checkNow (both callers) broadcast on it
// whether or not anything was actually upgraded, because a migration or a
// lazy-fetch backfill can change a store on its own.
//
// Factored out as a plain function (no ticker, no goroutine) so it is
// unit-testable without spinning a real timer: construct a Manager against a
// temp root, install fixtures, call this once, and assert on the result.
func runPluginAutoUpgradeTick(ctx context.Context, mgr *plugins.Manager, stderr io.Writer) (updated []plugins.UpgradedPlugin, changes plugins.StoreChanges, errs []string) {
	mk, mkChanges, err := pluginListMarketplaces(ctx, mgr)
	changes = changes.Merge(mkChanges)
	if err != nil {
		msg := fmt.Sprintf("listing marketplaces: %v", err)
		_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %s\n", msg)
		return nil, changes, []string{msg}
	}

	names := make([]string, 0, len(mk))
	for name := range mk {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		refreshChanges, err := pluginRefreshMarketplace(ctx, mgr, name)
		changes = changes.Merge(refreshChanges)
		if err != nil {
			msg := fmt.Sprintf("refreshing marketplace %q: %v", name, err)
			errs = append(errs, msg)
			_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %s\n", msg)
		}
	}

	var upgradeChanges plugins.StoreChanges
	updated, upgradeChanges, err = pluginUpdateAutoUpgrade(ctx, mgr)
	changes = changes.Merge(upgradeChanges)
	if err != nil {
		errs = append(errs, err.Error())
		_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %v\n", err)
	}
	return updated, changes, errs
}

// startPluginAutoUpgradeDaemon runs runPluginAutoUpgradeTick once immediately
// (the design's "plus once on hub start") and then every interval until ctx is
// canceled, broadcasting whichever store the tick actually changed. Meant to
// be launched with `go` from main, mirroring the hub's other ticker
// goroutines (roster watch, past-index rebuild, attention watcher).
func startPluginAutoUpgradeDaemon(ctx context.Context, mgr *plugins.Manager, interval time.Duration, server *appserver.Server) {
	tick := func() {
		_, changes, _ := pluginAutoUpgradeTick(ctx, mgr, os.Stderr)
		notifyStoreChanges(server, changes)
	}
	tick()
	ticker := newPluginAutoUpgradeTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			tick()
		}
	}
}

// registerPluginAutoUpgradeHandlers registers the evener/plugin/checkNow RPC
// handler: it runs one daemon tick synchronously on demand and reports what
// happened, so a user isn't stuck waiting up to the full interval to see an
// opted-in plugin move. The full evener/plugin/* CRUD surface (list, install,
// upgrade, ...) is a separate phase's hubPluginsController; this handler only
// owns the auto-upgrade tick.
func registerPluginAutoUpgradeHandlers(server *appserver.Server, mgr *plugins.Manager) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginCheckNow, func(ctx context.Context, _ appwire.EmptyParams) (appwire.PluginCheckNowResponse, error) {
		updated, changes, errs := runPluginAutoUpgradeTick(ctx, mgr, os.Stderr)
		refs := make([]string, len(updated))
		for i, u := range updated {
			refs[i] = u.Plugin + "@" + u.Marketplace
		}
		notifyStoreChanges(server, changes)
		return appwire.PluginCheckNowResponse{Updated: refs, Errors: errs}, nil
	})
}
