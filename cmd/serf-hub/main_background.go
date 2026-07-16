package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/plugins"
)

var (
	hubRosterWatch = func(ctx context.Context, roster *hubcore.Roster) error { return roster.Watch(ctx) }
	hubTicker      = func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
	hubSeedDefaults = func() error {
		_, err := plugins.NewManager("").SeedDefaultMarketplaces()
		return err
	}
	hubPluginGC     = func() ([]string, error) { return plugins.NewManager("").Gc() }
	hubStartUpgrade = func(ctx context.Context, cfg Config, web *WebServer) {
		startPluginAutoUpgradeDaemon(ctx, plugins.NewManager(""), cfg.PluginAutoUpgradeInterval, web.appRPC)
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
	w := hubcore.NewAttentionWatcher(func(p hubcore.AttentionChangedPayload) {
		web.appRPC.BroadcastAll(appwire.NotifySerfAttentionChanged, p)
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

func seedHubMarketplaces() {
	if err := hubSeedDefaults(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] warning: seeding default marketplaces: %v\n", err)
	}
}

func startHubPluginMaintenance(ctx context.Context, cfg Config, web *WebServer, startBackground func(func())) {
	if cfg.PluginAutoUpgrade {
		startBackground(func() { hubStartUpgrade(ctx, cfg, web) })
	}
	if removed, err := hubPluginGC(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: %v\n", err)
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: removed %d superseded cache dir(s)\n", len(removed))
	}
}

func refreshHubRemoteThreads(ctx context.Context, poke <-chan struct{}, cache *hubcore.RemoteThreadCache, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	ticks, stop := hubTicker(30 * time.Second)
	defer stop()
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cache.Store(web.refreshRemoteThreads(refreshCtx))
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
