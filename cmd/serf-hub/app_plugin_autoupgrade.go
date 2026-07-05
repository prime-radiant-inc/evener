package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/plugins"
)

// runPluginAutoUpgradeTick is the plain, timer-free core of the auto-upgrade
// daemon (design doc §9.1): refresh every known marketplace, then upgrade
// every installed, git-backed plugin that has autoUpgrade enabled. It never
// returns an error itself — marketplace-refresh and per-plugin upgrade
// failures are collected into errs and written to stderr, so one bad
// marketplace or plugin never blocks the others (failure-isolated; the
// per-plugin isolation is inherited from plugins.Manager.UpdateAutoUpgrade).
//
// Factored out as a plain function (no ticker, no goroutine) so it is
// unit-testable without spinning a real timer: construct a Manager against a
// temp root, install fixtures, call this once, and assert on the result.
func runPluginAutoUpgradeTick(ctx context.Context, mgr *plugins.Manager, stderr io.Writer) (updated []plugins.UpgradedPlugin, errs []string) {
	mk, err := mgr.ListMarketplaces()
	if err != nil {
		msg := fmt.Sprintf("listing marketplaces: %v", err)
		_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %s\n", msg)
		return nil, []string{msg}
	}

	names := make([]string, 0, len(mk))
	for name := range mk {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := mgr.RefreshMarketplace(ctx, name); err != nil {
			msg := fmt.Sprintf("refreshing marketplace %q: %v", name, err)
			errs = append(errs, msg)
			_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %s\n", msg)
		}
	}

	updated, err = mgr.UpdateAutoUpgrade(ctx)
	if err != nil {
		errs = append(errs, err.Error())
		_, _ = fmt.Fprintf(stderr, "[hub] plugin auto-upgrade: %v\n", err)
	}
	return updated, errs
}

// startPluginAutoUpgradeDaemon runs runPluginAutoUpgradeTick once immediately
// (the design's "plus once on hub start") and then every interval until ctx is
// canceled, broadcasting serf/plugin/updated for each plugin actually
// upgraded. Meant to be launched with `go` from main, mirroring the hub's
// other ticker goroutines (roster watch, past-index rebuild, attention
// watcher).
func startPluginAutoUpgradeDaemon(ctx context.Context, mgr *plugins.Manager, interval time.Duration, server *appserver.Server) {
	tick := func() {
		updated, _ := runPluginAutoUpgradeTick(ctx, mgr, os.Stderr)
		if len(updated) > 0 {
			notifyPluginUpdated(server)
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// registerPluginAutoUpgradeHandlers registers the serf/plugin/checkNow RPC
// handler: it runs one daemon tick synchronously on demand and reports what
// happened, so a user isn't stuck waiting up to the full interval to see an
// opted-in plugin move. The full serf/plugin/* CRUD surface (list, install,
// upgrade, ...) is a separate phase's hubPluginsController; this handler only
// owns the auto-upgrade tick.
func registerPluginAutoUpgradeHandlers(server *appserver.Server, mgr *plugins.Manager) {
	appserver.HandleTyped(server.Router(), appwire.MethodSerfPluginCheckNow, func(ctx context.Context, _ appwire.EmptyParams) (appwire.PluginCheckNowResponse, error) {
		updated, errs := runPluginAutoUpgradeTick(ctx, mgr, os.Stderr)
		refs := make([]string, len(updated))
		for i, u := range updated {
			refs[i] = u.Plugin + "@" + u.Marketplace
		}
		if len(updated) > 0 {
			notifyPluginUpdated(server)
		}
		return appwire.PluginCheckNowResponse{Updated: refs, Errors: errs}, nil
	})
}
