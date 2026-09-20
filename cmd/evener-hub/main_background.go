package hub

import (
	"context"
	"fmt"
	"os"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/plugins"
)

var (
	hubRosterWatch = func(ctx context.Context, roster *hubcore.Roster) error { return roster.Watch(ctx) }
	hubTicker      = func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
	hubSeedDefaults = func(ctx context.Context, mgr *plugins.Manager) error {
		_, err := mgr.SeedDefaultMarketplaces(ctx)
		return err
	}
	hubPluginGC           = func(ctx context.Context, mgr *plugins.Manager) ([]string, error) { return mgr.Gc(ctx) }
	hubStartUpgradeDaemon = func(ctx context.Context, mgr *plugins.Manager, interval time.Duration) {
		startPluginAutoUpgradeDaemon(ctx, mgr, interval)
	}
	// Every background path below runs on web.cfg.PluginManager — the one wired
	// Manager the server's plugin surfaces also use — so the store they
	// maintain is the store the server serves (#1780). A Manager minted here
	// over plugins.DefaultRoot() would be a different root whenever the caller
	// pointed cfg.PluginRoot inside a sandbox/test temp root.
	hubStartUpgrade = func(ctx context.Context, cfg Config, web *WebServer) {
		mgr := web.cfg.PluginManager
		hubStartUpgradeDaemon(ctx, mgr, cfg.PluginAutoUpgradeInterval)
	}
)

func refreshPastOnStatus(past *hubcore.PastIndex) func(string) {
	return func(sessionID string) { past.RefreshOne(sessionID) }
}

func watchHubRoster(ctx context.Context, roster *hubcore.Roster) {
	if ctx.Err() != nil {
		return
	}
	if err := hubRosterWatch(ctx, roster); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "[hub] roster watch: %v\n", err)
	}
}

func watchHubAttention(ctx context.Context, poke <-chan struct{}, archive *hubcore.ArchiveStore, past *hubcore.PastIndex, roster *hubcore.Roster, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	w := hubcore.NewAttentionWatcher(func(p appwire.AttentionChangedPayload) {
		web.appRPC.BroadcastAll(appwire.NotifyEvenerAttentionChanged, p)
	})
	ticks, stop := hubTicker(5 * time.Second)
	defer stop()
	run := func() {
		decisions, _ := archive.Decisions()
		m, sum := hubcore.DeriveAttention(past.AllMetas(), roster.List(), decisions)
		w.Tick(m, sum)
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			run()
		case <-poke:
			run()
		}
	}
}

// seedHubMarketplaces runs before the hub ever calls ListenAndServe (main.go
// binds hubListener but does not Serve it until well after this call), so no
// client could be connected when it runs — but it runs on web.cfg.PluginManager
// regardless: that Manager is wired to web's server, and a write no caller has
// to remember to pair with a broadcast is the point of OnStoreChanged (#1634),
// which only holds if nothing gets to stay a documented exception.
func seedHubMarketplaces(ctx context.Context, web *WebServer) {
	mgr := web.cfg.PluginManager
	if err := hubSeedDefaults(ctx, mgr); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] warning: seeding default marketplaces: %v\n", err)
	}
}

func startHubPluginMaintenance(ctx context.Context, cfg Config, web *WebServer, startBackground func(func())) {
	if cfg.PluginAutoUpgrade {
		startBackground(func() { hubStartUpgrade(ctx, cfg, web) })
	}
	// Also runs before ListenAndServe (see seedHubMarketplaces), on the same
	// shared Manager for the same reason: Gc's own lockStore acquisition can run
	// migrateMarketplaceNames, which writes both store files finishing a
	// rename an earlier run left half done.
	gcMgr := web.cfg.PluginManager
	if removed, err := hubPluginGC(ctx, gcMgr); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: %v\n", err)
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: removed %d superseded or orphaned dir(s)\n", len(removed))
	}
}

// refreshHubRemoteThreads refreshes the web server's remote-thread cache on a
// ~30s ticker + poke. Capture and publish both go through web's own
// RemoteThreadCache — the walk reads its per-source generations at read time
// and the publish compares them under the publish lock — so the two can never
// disagree about which cache owns a registration.
func refreshHubRemoteThreads(ctx context.Context, poke <-chan struct{}, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	ticks, stop := hubTicker(30 * time.Second)
	defer stop()
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// The walk captures each source's identity generation immediately
		// before it reads the source (refreshRemoteThreadSnapshot) and hands
		// the read-time capture to the publish, which compares it under the
		// publish lock: a source removed after its read — or removed and
		// re-added, which registers a strictly newer generation (the round-10
		// finding: the first registration's rows must not publish under the
		// re-added identity) — mismatches, while a host added mid-walk is
		// captured under the registration that owns its rows and still
		// appears on this tick.
		snapshot := web.refreshRemoteThreadSnapshot(refreshCtx)
		web.cfg.RemoteThreadCache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
			Threads:  snapshot.threads,
			Complete: snapshot.complete,
			Sources:  snapshot.sources,
		}, snapshot.sourceGenerations)
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			refresh()
		case <-poke:
			refresh()
		}
	}
}
