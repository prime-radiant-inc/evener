//go:build serffuzz

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

func FuzzFinalMainBackground(f *testing.F) {
	for mode := byte(0); mode < 6; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		mode %= 6
		oldWatch, oldTicker, oldNow := hubRosterWatch, hubTicker, hubNow
		oldSeed, oldGC, oldUpgrade := hubSeedDefaults, hubPluginGC, hubStartUpgrade
		t.Cleanup(func() {
			hubRosterWatch, hubTicker, hubNow = oldWatch, oldTicker, oldNow
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
	project := filepath.Join(root, "project")
	meta := schema.SessionMeta{ID: "wedged"}
	if err := schema.SaveSessionMeta(project, meta); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(project, "sessions", "wedged.transcript.jsonl")
	if err := os.WriteFile(transcript, []byte("{\"kind\":\"api_call\",\"error\":\"stopped\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past = hubcore.NewPastIndex(filepath.Join(root, "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster = hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: "wedged", Status: "active", Entry: rendezvous.Entry{SessionID: "wedged"}})
	start := time.Now()
	nowCalls := 0
	hubNow = func() time.Time {
		nowCalls++
		if nowCalls == 1 {
			return start
		}
		return start.Add(hubcore.StallThreshold + time.Second)
	}

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
