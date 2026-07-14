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
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
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

		oldCanonicalize, oldResolve, oldParse := hubCanonicalizeDir, hubResolveLaunch, hubParseModelRef
		oldRefresh, oldList, oldFork, oldEnsure := hubRosterRefresh, hubRosterList, hubForkSession, hubEnsureSource
		t.Cleanup(func() {
			hubCanonicalizeDir, hubResolveLaunch, hubParseModelRef = oldCanonicalize, oldResolve, oldParse
			hubRosterRefresh, hubRosterList, hubForkSession = oldRefresh, oldList, oldFork
			hubEnsureSource = oldEnsure
		})
		_ = oldList(hubcore.NewRosterWithEntries())
		hubEnsureSource = func(context.Context, *codexlaunch.CodexLauncher, string, *appsource.Registry) (appsource.Source, error) {
			return nil, nil
		}
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{CodexLauncher: bad}, appsource.NewRegistry(), appwire.ThreadStartParams{Harness: "managed"})
		hubEnsureSource = oldEnsure
		spawner := &fakeRPCModelContractSpawner{fakeRPCSpawner: fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{PID: 44}, nil
		}, resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{SessionID: "r"}, nil
		}}, contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}}
		localCfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Spawner: spawner}
		hubCanonicalizeDir = func(string) (string, error) { return "", errors.New("canonical") }
		_, _ = hubThreadStart(ctx, localCfg, reg, appwire.ThreadStartParams{CWD: "/work", Model: "openai/gpt-5"})
		hubCanonicalizeDir = oldCanonicalize
		hubResolveLaunch = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
			return launchconfig.Resolved{}, errors.New("resolve")
		}
		_, _ = hubThreadStart(ctx, localCfg, reg, appwire.ThreadStartParams{Model: "openai/gpt-5"})
		hubResolveLaunch = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
			return launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5"}}, nil
		}
		hubParseModelRef = func(string) (cmdutil.ModelRef, error) { return cmdutil.ModelRef{}, errors.New("parse") }
		_, _ = hubThreadStart(ctx, localCfg, reg, appwire.ThreadStartParams{})
		hubParseModelRef = oldParse
		_, _ = hubThreadStart(ctx, localCfg, appsource.NewRegistry(), appwire.ThreadStartParams{})
		roster := hubcore.NewRosterWithEntries()
		localCfg.Roster = roster
		hubRosterRefresh = func(*hubcore.Roster) {}
		hubRosterList = func(*hubcore.Roster) []hubcore.LiveEntry {
			return []hubcore.LiveEntry{{Entry: rendezvous.Entry{PID: 44, SessionID: "r"}, SessionID: "r"}}
		}
		_, _ = hubThreadStart(ctx, localCfg, reg, appwire.ThreadStartParams{})
		_, _ = hubThreadResume(ctx, localCfg, reg, appwire.ThreadResumeParams{Session: "r"})
		freshMissing := appsource.NewRegistry()
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{CodexLauncher: fallbackLaunch}, freshMissing, appwire.ThreadResumeParams{Ref: "remote:r"})
		hubResolveLaunch = oldResolve

		hubForkSession = func(string, string, int, string, string) (string, error) { return "child", nil }
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{StateDir: t.TempDir(), Past: hubcore.NewPastIndex("")}, reg, appwire.ThreadForkParams{Ref: "local:r", SourceTurnID: "1", EditedInput: "edit"})

		now := time.Unix(1700000000, 0).UTC()
		past := hubcore.NewPastIndex("")
		past.SeedForTest([]schema.SessionMeta{
			{ID: "active", Name: "active", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
			{ID: "fav", Name: "fav", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
		})
		fav := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "tree.db"))
		_ = fav.Set("session", "active", true, now)
		_ = fav.Set("session", "fav", true, now)
		treeRoster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/p", StartedAt: now}, SessionID: "active", Status: "waiting"},
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "/work/p", StartedAt: now}, SessionID: "orphan", Status: "error"},
		)
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: treeRoster, Favorite: fav})
		web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))

		oldBuild, oldDerive := hubBuildNavigationTree, hubDeriveNavigationAttention
		oldNormalize, oldRef, oldNavigation, oldLiveTitle, oldIsLive, oldWorkspace, oldRank := hubNormalizeTreeState, hubAppThreadRef, hubNavigationInputs, hubLiveTreeTitle, hubIsSessionLive, hubTreeWorkspaceData, hubTreeAttentionRank
		t.Cleanup(func() {
			hubBuildNavigationTree, hubDeriveNavigationAttention = oldBuild, oldDerive
			hubNormalizeTreeState, hubAppThreadRef = oldNormalize, oldRef
			hubNavigationInputs = oldNavigation
			hubLiveTreeTitle = oldLiveTitle
			hubIsSessionLive = oldIsLive
			hubTreeWorkspaceData = oldWorkspace
			hubTreeAttentionRank = oldRank
		})
		key := testProjectID(t, "/work/p")
		node := func(id, kind, state string, updated time.Time) hubcore.TreeNode {
			return hubcore.TreeNode{ID: id, Kind: kind, State: state, Title: id, UpdatedAt: updated, CreatedAt: updated}
		}
		hubBuildNavigationTree = func([]schema.SessionMeta, []hubcore.LiveEntry, map[hubcore.ArchiveKey]bool, map[string]identifier.Project) hubcore.Tree {
			return hubcore.Tree{
				NeedsYou: []hubcore.TreeNode{node("active", "session", "waiting", now)},
				Projects: []hubcore.TreeProject{{Key: key, Name: "p", WorkingDir: "/work/p", RollupState: "idle", Current: []hubcore.TreeNode{
					node("active", "session", "waiting", now), node("fav", "session", "idle", now.Add(-time.Minute)), node("fav2", "session", "idle", now.Add(-2*time.Minute)), node("group", "group", "idle", now),
				}}},
			}
		}
		_ = fav.Set("session", "fav2", true, now)
		structuredRoster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/p", StartedAt: now}, SessionID: "active", Status: "waiting"},
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "/work/p", StartedAt: now}, SessionID: "orphan", Status: "error"},
			hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "pathless", StartedAt: now}, SessionID: "pathless", Status: "idle"},
		)
		hubNavigationInputs = func(*WebServer, context.Context) ([]schema.SessionMeta, []hubcore.LiveEntry, map[string]identifier.Project) {
			return nil, []hubcore.LiveEntry{
				{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/p", StartedAt: now}, SessionID: "active", Status: "waiting"},
				{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "/work/p", StartedAt: now}, SessionID: "orphan", Status: "errored"},
				{Entry: rendezvous.Entry{SessionID: "pathless", StartedAt: now}, SessionID: "pathless", Status: "idle"},
			}, nil
		}
		rankCalls := 0
		hubTreeAttentionRank = func(string) int {
			rankCalls++
			if rankCalls%2 == 1 {
				return 10
			}
			return 0
		}
		structured := NewWebServer(hubcore.WebConfig{Roster: structuredRoster, Favorite: fav})
		structured.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))
		hubNavigationInputs = oldNavigation
		hubTreeAttentionRank = oldRank

		hubNormalizeTreeState = func(string) string { return "" }
		hubAppThreadRef = func(appwire.Thread) hubapi.Ref { return hubapi.Ref{HostID: "remote"} }
		_ = hubDetailFromAppThread(appwire.Thread{ID: "fallback"})
		hubNormalizeTreeState, hubAppThreadRef = oldNormalize, oldRef

		invalidCache := &hubcore.RemoteThreadCache{}
		invalidCache.Store([]appwire.Thread{{}})
		_, _, _ = NewWebServer(hubcore.WebConfig{RemoteThreadCache: invalidCache}).navigationTreeInputs(ctx)

		detailPast := hubcore.NewPastIndex("")
		detailPast.SeedForTest([]schema.SessionMeta{{ID: "r", Name: "past title", TurnCount: 3, CreatedAt: now, UpdatedAt: now}})
		detailRoster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "r"}, SessionID: "r", Status: "idle"})
		detailThread := appwire.Thread{ID: "r", Source: "local", Serf: appwire.SerfThread{Ref: "local:r"}}
		detailWeb := finalSessionWeb(hubcore.WebConfig{Past: detailPast, Roster: detailRoster}, detailThread)
		hubLiveTreeTitle = func(string, hubcore.LiveEntry, *hubcore.PastIndex) string { return "" }
		hubIsSessionLive = func(*WebServer, string) bool { return true }
		hubTreeWorkspaceData = func(*WebServer, string) WorkspaceData { return WorkspaceData{ID: "r", TurnCount: 3} }
		_, _ = detailWeb.apiSessionDetail("r")
		hubLiveTreeTitle = oldLiveTitle
		hubIsSessionLive = oldIsLive
		hubTreeWorkspaceData = oldWorkspace

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
