//go:build serffuzz

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func FuzzFinalMainBackground(f *testing.F) {
	for mode := byte(0); mode < 6; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		mode %= 6
		oldWatch, oldTicker := hubRosterWatch, hubTicker
		oldSeed, oldGC, oldUpgrade := hubSeedDefaults, hubPluginGC, hubStartUpgrade
		t.Cleanup(func() {
			hubRosterWatch, hubTicker = oldWatch, oldTicker
			hubSeedDefaults, hubPluginGC, hubStartUpgrade = oldSeed, oldGC, oldUpgrade
		})

		root := t.TempDir()
		t.Setenv("HOME", root)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		roster := hubcore.NewRoster(filepath.Join(root, "run"), &hubcore.StatusProber{})

		switch mode {
		case 0:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			oldUpgrade(ctx, Config{PluginAutoUpgradeInterval: time.Hour}, NewWebServer(hubcore.WebConfig{}))
			past.SeedForTest([]schema.SessionMeta{{ID: "missing"}})
			refreshPastOnStatus(past)("missing")
		case 1:
			hubRosterWatch = func(context.Context, *hubcore.Roster) error { return errors.New("watch") }
			watchHubRoster(context.Background(), roster)
		case 2:
			hubSeedDefaults = func() error { return errors.New("seed") }
			seedHubMarketplaces()
		case 3, 4:
			hubPluginGC = func() ([]string, error) {
				if mode == 3 {
					return nil, errors.New("gc")
				}
				return []string{"old"}, nil
			}
			started := make(chan struct{})
			hubStartUpgrade = func(context.Context, Config, *WebServer) { close(started) }
			web := NewWebServer(hubcore.WebConfig{})
			startHubPluginMaintenance(context.Background(), Config{PluginAutoUpgrade: true}, web, func(fn func()) { go fn() })
			<-started
		case 5:
			runFinalBackgroundLoops(t, root, past, roster)
		}
	})
}

func runFinalBackgroundLoops(t *testing.T, root string, past *hubcore.PastIndex, roster *hubcore.Roster) {
	t.Helper()
	ticks := make(chan time.Time)
	hubTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	archive := hubcore.NewArchiveStore(filepath.Join(root, "archive.db"))
	web := NewWebServer(hubcore.WebConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	poke := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchHubAttention(ctx, poke, archive, past, roster, web)
		close(done)
	}()
	ticks <- time.Time{}
	poke <- struct{}{}
	cancel()
	<-done

	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	cache := &hubcore.RemoteThreadCache{}
	go func() {
		refreshHubRemoteThreads(ctx, poke, cache, web)
		close(done)
	}()
	ticks <- time.Time{}
	poke <- struct{}{}
	cancel()
	<-done
}
