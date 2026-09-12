//go:build evenerfuzz

package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
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
		remote := &pass6LifecycleSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: appwire.Thread{ID: "r", Source: "remote", Evener: appwire.EvenerThread{Ref: "remote:r"}}}}
		reg := appsource.NewRegistry()
		reg.Add(remote)

		missing := appsource.NewRegistry()
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{}, missing, appwire.ThreadResumeParams{Ref: "remote:r"})
		resumeSpawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{SessionID: "r"}, nil
		}}
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{Spawner: resumeSpawner}, reg, appwire.ThreadResumeParams{Ref: "local:r"})

		oldCanonicalize, oldResolve, oldParse := hubCanonicalizeDir, hubResolveLaunch, hubParseModelRef
		oldRefresh, oldList, oldFork := hubRosterRefresh, hubRosterList, hubForkSession
		t.Cleanup(func() {
			hubCanonicalizeDir, hubResolveLaunch, hubParseModelRef = oldCanonicalize, oldResolve, oldParse
			hubRosterRefresh, hubRosterList, hubForkSession = oldRefresh, oldList, oldFork
		})
		_ = oldList(hubcore.NewRosterWithEntries())
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
		hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
		hubRosterList = func(*hubcore.Roster) []hubcore.LiveEntry {
			return []hubcore.LiveEntry{{Entry: rendezvous.Entry{PID: 44, SessionID: "r"}, SessionID: "r"}}
		}
		_, _ = hubThreadStart(ctx, localCfg, reg, appwire.ThreadStartParams{})
		_, _ = hubThreadResume(ctx, localCfg, reg, appwire.ThreadResumeParams{Session: "r"})
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

		oldBuild, oldDerive := hubBuildNavigationTree, hubDeriveNavigationAttention
		oldNavigation, oldRank := hubNavigationInputs, hubTreeAttentionRank
		t.Cleanup(func() {
			hubBuildNavigationTree, hubDeriveNavigationAttention = oldBuild, oldDerive
			hubNavigationInputs = oldNavigation
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
		hubNavigationInputs = func(*WebServer, context.Context) navigationSnapshot {
			return navigationSnapshot{live: []hubcore.LiveEntry{
				{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/p", StartedAt: now}, SessionID: "active", Status: "waiting"},
				{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "/work/p", StartedAt: now}, SessionID: "orphan", Status: "errored"},
				{Entry: rendezvous.Entry{SessionID: "pathless", StartedAt: now}, SessionID: "pathless", Status: "idle"},
			}}
		}
		rankCalls := 0
		hubTreeAttentionRank = func(string) int {
			rankCalls++
			if rankCalls%2 == 1 {
				return 10
			}
			return 0
		}
		_ = NewWebServer(hubcore.WebConfig{Roster: structuredRoster, Favorite: fav})
		hubNavigationInputs = oldNavigation
		hubTreeAttentionRank = oldRank

		invalidCache := &hubcore.RemoteThreadCache{}
		invalidCache.Store([]appwire.Thread{{}})
		_, _, _ = NewWebServer(hubcore.WebConfig{RemoteThreadCache: invalidCache}).navigationTreeInputs(ctx)

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

	})
}
