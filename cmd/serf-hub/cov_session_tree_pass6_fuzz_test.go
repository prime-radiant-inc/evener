package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
)

type pass6SessionSource struct {
	*scriptedAppSource
	err error
}

func (s *pass6SessionSource) SteerTurn(context.Context, appwire.TurnSteerParams) error { return s.err }
func (s *pass6SessionSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return s.err
}
func (s *pass6SessionSource) QueueTurn(context.Context, appwire.TurnQueueParams) error { return s.err }
func (s *pass6SessionSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return s.err
}
func (s *pass6SessionSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) error {
	return s.err
}

func (s *pass6SessionSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, nil
}
func (s *pass6SessionSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return s.err
}
func (s *pass6SessionSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, s.err
}

func pass6SessionWeb(thread appwire.Thread, actionErr error) *WebServer {
	source := &pass6SessionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, err: actionErr}
	source.startTurn = func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, actionErr
	}
	registry := appsource.NewRegistry()
	registry.Add(source)
	web := NewWebServer(hubcore.WebConfig{})
	web.sources = registry
	return web
}

// FuzzSessionTreePass6 closes residual handler, detail, action, and tree
// projection branches using only process-local sources and stores.
func FuzzSessionTreePass6(f *testing.F) {
	for op := uint8(0); op < 9; op++ {
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
			Serf: appwire.SerfThread{Ref: "remote:thread-6", Profile: "profile", Goal: &appwire.GoalState{Status: "active", Iterations: 2}, Capabilities: appwire.ThreadCapabilities{
				Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
				ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true,
			}},
		}
		ref := "remote:thread-6"
		web := pass6SessionWeb(thread, nil)

		switch op % 9 {
		case 0:
			// Decode, empty-input, missing-live, method, and unknown-action gates.
			for _, body := range []string{"{", `{}`, `{"text":" "}`} {
				web.handleSend(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body)), ref)
			}
			web.handleSessionAction(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/action", nil), ref, "clear")
			web.handleSessionAction(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/action", nil), ref, "unknown")

		case 1:
			// Success and source-error action paths after capability discovery.
			for _, err := range []error{nil, appwire.Conflict("busy")} {
				web = pass6SessionWeb(thread, err)
				web.handleSend(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(`{"text":"x"}`)), ref)
				for _, action := range []string{"interrupt", "clear", "shutdown"} {
					web.handleSessionAction(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/"+action, strings.NewReader(`{"turn_id":" t "}`)), ref, action)
				}
			}

		case 2:
			// Disabled capabilities, absent source, and every capability selector.
			for _, action := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "shutdown", "model", "queue", "other"} {
				_ = sessionCapabilityAvailable(hubapiCapsAll(), action)
			}
			thread.Serf.Capabilities = appwire.ThreadCapabilities{}
			web = pass6SessionWeb(thread, nil)
			_ = web.ensureSessionActionAvailable(ref, "send")
			_ = web.ensureSessionActionAvailable("remote:absent", "send")

		case 3:
			// Detail conversion fallbacks, usage/goal branches, and state reads.
			_, _ = web.apiSessionDetail(ref)
			_, _ = web.apiSessionState(ref)
			thread.Name, thread.Preview, thread.SessionID, thread.CWD = "", "", "", ""
			thread.Status.Type = ""
			thread.Serf.Ref = "broken ref"
			thread.Serf.Goal = nil
			thread.Serf.Usage = nil
			_ = hubDetailFromAppThread(thread)
			_ = hubRefFromAppThread(thread)
			_, _, _ = appThreadTreeEntries(thread)

		case 4:
			// Remote list normalization, local-source skip, and last-good retention.
			thread.Serf.Ref = ""
			thread.Source = ""
			web = pass6SessionWeb(thread, nil)
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
			web = NewWebServer(hubcore.WebConfig{Past: past})
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/tree", nil))
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree?summary=1", nil))
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))
			for _, target := range []string{"/api/tree/project", "/api/tree/project?key=missing"} {
				web.handleAPITreeProject(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
			web.handleAPITreeProject(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/tree/project?key=x", nil))

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
			web = NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite})
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))

		case 7:
			// Session API parse/method/subroute matrix and fork decode failures.
			for _, target := range []string{"/api/sessions/x%3Ay%3Az", "/api/sessions/bad", "/api/sessions/remote%3Athread-6", "/api/sessions/remote%3Athread-6/details", "/api/sessions/remote%3Athread-6/nope"} {
				web.handleAPISession(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, target, nil))
			}
			web.handleAPIFork(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fork", nil), ref)
			web.handleAPIFork(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/fork", strings.NewReader("{")), ref)

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
			_ = hubUsageFromAppwire(&appwire.SerfUsage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalTokens: 6})
			_ = hubRefFromTreeNodeID("remote:thread-6")
			_ = hubRefFromTreeNodeID("local-id")
			node := hubcore.TreeNode{ID: "parent", State: "active", Children: []hubcore.TreeNode{{ID: "child", State: "ended"}}}
			_ = empty.apiTreeNode("project", "key", node, false)
			_ = empty.apiTreeProject("project", map[hubcore.ArchiveKey]bool{}, hubcore.TreeProject{
				Key: "key", Current: []hubcore.TreeNode{node}, Recent: []hubcore.TreeNode{node}, Archived: []hubcore.TreeNode{node},
			})
		}
	})
}

func hubapiCapsAll() hubapi.SessionCapabilities {
	return hubapi.SessionCapabilities{Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true, Fork: true, Shutdown: true, ChangeModel: true, Queue: true}
}
