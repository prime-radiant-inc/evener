package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// FuzzSessionTreePass6 closes residual tree projection branches using only
// process-local sources and stores.
func FuzzSessionTreePass6(f *testing.F) {
	for op := range uint8(9) {
		f.Add(op, "pass6")
	}
	f.Fuzz(func(t *testing.T, op uint8, title string) {
		now := time.Unix(1700000000, 0)
		thread := appwire.Thread{
			ID: "thread-6", SessionID: "thread-6", Source: "remote", Name: title,
			Preview: "preview", CWD: "/work/project", ModelProvider: "provider/model",
			CreatedAt: now.Add(-time.Minute).Unix(), UpdatedAt: now.Unix(),
			Status: appwire.ThreadStatus{Type: "active"},
			Turns:  []appwire.Turn{{ID: "done", Status: appwire.TurnStatusCompleted}},
			Evener: appwire.EvenerThread{Ref: "remote:thread-6", Profile: "profile", Goal: &appwire.GoalState{Status: "active", Iterations: 2}, Capabilities: appwire.ThreadCapabilities{
				Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
				ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true,
			}},
		}
		switch op % 9 {
		case 4:
			// Remote list normalization, local-source skip, and last-good retention.
			thread.Evener.Ref = ""
			thread.Source = ""
			web := NewWebServer(hubcore.WebConfig{})
			_ = web.refreshRemoteThreads(context.Background())
			source := &stubThreadLister{id: "remote", resp: appwire.ThreadListResponse{Data: []appwire.Thread{thread}}}
			_ = web.listThreadsWithFallback(context.Background(), source)
			source.err = errors.New("transient")
			_ = web.listThreadsWithFallback(context.Background(), source)
			_, _, _ = appThreadTreeEntries(appwire.Thread{})

		case 5:
			// Tree method/summary/project gates and populated project projection.
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "past-6", Name: title, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"}}})
			_ = NewWebServer(hubcore.WebConfig{Past: past})

		case 6:
			// Archived project stub and favorite/pinned projection.
			dir := t.TempDir()
			archive := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
			favorite := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "past-6", Name: title, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"}}})
			if err := archive.Set("project", testProjectID(t, "/work/project"), true, now); err != nil {
				t.Fatal(err)
			}
			if err := favorite.Set("session", "past-6", true, now); err != nil {
				t.Fatal(err)
			}
			_ = NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite})

		case 8:
			// No-roster/no-source helpers and fetchStatus transport failures.
			empty := NewWebServer(hubcore.WebConfig{})
			_, _, _ = empty.navigationTreeInputs(context.Background())
			_ = empty.apiTreeSources()
			_, _ = empty.liveEntry("missing")
			_ = empty.fetchStatus(hubcore.LiveEntry{})
			_ = empty.apiSessionCapabilities("missing", false)
			_ = empty.apiSessionCapabilities("missing", true)
			_ = appThreadTreeLive(appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}})
			_ = appThreadTreeLive(appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded}})
			_ = appThreadTreeLive(thread)
			_ = hubRefFromTreeNodeID("remote:thread-6")
			_ = hubRefFromTreeNodeID("local-id")
		}
	})
}
