//go:build serffuzz

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

type exactTreeLister struct {
	*scriptedAppSource
	data []appwire.Thread
	err  error
}

func (s *exactTreeLister) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: s.data}, s.err
}

func FuzzExactLifecycleTree(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		ctx := context.Background()
		remote := &pass6LifecycleSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: appwire.Thread{ID: "r", Source: "remote", Serf: appwire.SerfThread{Ref: "remote:r"}}}}
		reg := appsource.NewRegistry()
		reg.Add(remote)

		// Managed launch failures and nil managed sources exercise the defensive
		// lifecycle outcomes without starting an external process.
		bad := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{ID: "managed", Binary: "/does/not/exist"}})
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{CodexLauncher: bad}, reg, appwire.ThreadStartParams{Harness: "managed"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{CodexLauncher: bad}, reg, appwire.ThreadResumeParams{Ref: "managed:r"})
		missing := appsource.NewRegistry()
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{CodexLauncher: bad}, missing, appwire.ThreadResumeParams{Ref: "remote:r"})
		fallbackLaunch := codexlaunch.NewCodexLauncher(nil)
		fallbackLaunch.Sources["remote"] = remote
		fallbackLaunch.Running["remote"] = &codexlaunch.LaunchedCodex{Exited: make(chan struct{})}
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{CodexLauncher: fallbackLaunch}, missing, appwire.ThreadStartParams{Harness: "remote"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{CodexLauncher: fallbackLaunch}, missing, appwire.ThreadResumeParams{Ref: "remote:r"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{Spawner: finalSessionSpawner{entry: rendezvous.Entry{SessionID: "r"}}}, reg, appwire.ThreadResumeParams{Ref: "local:r"})

		now := time.Unix(1700000000, 0).UTC()
		past := hubcore.NewPastIndex("")
		past.SeedForTest([]schema.SessionMeta{
			{ID: "active", Name: "active", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
			{ID: "fav", Name: "fav", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
		})
		fav := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "tree.db"))
		_ = fav.Set("session", "active", true, now)
		_ = fav.Set("session", "fav", true, now)
		roster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/p", StartedAt: now}, SessionID: "active", Status: "waiting"},
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "/work/p", StartedAt: now}, SessionID: "orphan", Status: "error"},
		)
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, Favorite: fav})
		web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))

		// Invalid remote rows are ignored, local sources are skipped, successful
		// empty lists clear last-good data, and nil registries are valid.
		_ = (&WebServer{}).refreshRemoteThreads(ctx)
		_ = (&WebServer{}).apiTreeSources()
		_ = (&WebServer{}).listThreadsWithFallback(ctx, &exactTreeLister{scriptedAppSource: &scriptedAppSource{id: "fresh"}})
		local := &exactTreeLister{scriptedAppSource: &scriptedAppSource{id: "local"}}
		lister := &exactTreeLister{scriptedAppSource: &scriptedAppSource{id: "other"}, data: []appwire.Thread{{}, {SessionID: "sid"}}}
		sources := appsource.NewRegistry()
		sources.Add(local)
		sources.Add(lister)
		web.sources = sources
		_ = web.refreshRemoteThreads(ctx)
		lister.err = errors.New("temporary")
		_ = web.listThreadsWithFallback(ctx, lister)
		lister.err, lister.data = nil, nil
		_ = web.listThreadsWithFallback(ctx, lister)
		_ = web.apiTreeSources()

		_ = hubDetailFromAppThread(appwire.Thread{ID: "fallback", Source: "bad source"})
		_, _ = web.apiSessionDetail("bad ref value")
		_, _ = web.apiSessionState("bad ref value")
	})
}
