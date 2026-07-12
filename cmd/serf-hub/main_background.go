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

func refreshPastOnStatus(past *hubcore.PastIndex) func(string) {
	return func(sessionID string) { past.RefreshOne(sessionID) }
}

func watchHubRoster(ctx context.Context, roster *hubcore.Roster) {
	if err := roster.Watch(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "[hub] roster watch: %v\n", err)
	}
}

func watchHubAttention(ctx context.Context, poke <-chan struct{}, archive *hubcore.ArchiveStore, past *hubcore.PastIndex, roster *hubcore.Roster, web *WebServer) {
	w := hubcore.NewAttentionWatcher(func(p hubcore.AttentionChangedPayload) {
		web.appRPC.BroadcastAll(appwire.NotifySerfAttentionChanged, p)
	})
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	seenActive := map[string]time.Time{}
	run := func() {
		decisions, _ := archive.Decisions()
		m, sum := hubcore.DeriveAttention(past.AllMetas(), roster.List(), decisions)
		for _, id := range hubcore.StaleActives(seenActive, m, time.Now()) {
			if entry, ok := past.Find(id); ok && hubcore.WedgedStatus(entry) {
				hubcore.ApplyWedgeOverride(m, &sum, id)
			}
		}
		w.Tick(m, sum)
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			run()
		case <-poke:
			run()
		}
	}
}

func seedHubMarketplaces() {
	if _, err := plugins.NewManager("").SeedDefaultMarketplaces(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] warning: seeding default marketplaces: %v\n", err)
	}
}

func startHubPluginMaintenance(ctx context.Context, cfg Config, web *WebServer) {
	if cfg.PluginAutoUpgrade {
		go startPluginAutoUpgradeDaemon(ctx, plugins.NewManager(""), cfg.PluginAutoUpgradeInterval, web.appRPC)
	}
	if removed, err := plugins.NewManager("").Gc(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: %v\n", err)
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "[hub] plugin gc: removed %d superseded cache dir(s)\n", len(removed))
	}
}

func refreshHubRemoteThreads(ctx context.Context, poke <-chan struct{}, cache *hubcore.RemoteThreadCache, web *WebServer) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
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
		case <-tick.C:
			refresh()
		case <-poke:
			refresh()
		}
	}
}
