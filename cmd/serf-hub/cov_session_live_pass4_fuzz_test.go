package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

type pass4ActionSource struct {
	*scriptedAppSource
	err error
}

func (s *pass4ActionSource) SteerTurn(context.Context, appwire.TurnSteerParams) error { return s.err }
func (s *pass4ActionSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return s.err
}
func (s *pass4ActionSource) QueueTurn(context.Context, appwire.TurnQueueParams) error { return s.err }
func (s *pass4ActionSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return s.err
}
func (s *pass4ActionSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) error {
	return s.err
}

func (s *pass4ActionSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, nil
}
func (s *pass4ActionSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return s.err
}
func (s *pass4ActionSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, s.err
}

func pass4RemoteWeb(thread appwire.Thread, actionErr error) *WebServer {
	web := NewWebServer(hubcore.WebConfig{})
	source := &pass4ActionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, err: actionErr}
	source.startTurn = func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, actionErr
	}
	registry := appsource.NewRegistry()
	registry.Add(source)
	web.sources = registry
	return web
}

// FuzzSessionLivePass4 closes the successful remote-source paths left by the
// broad route fuzzers. The source is entirely in-process and never dials a
// daemon or provider.
func FuzzSessionLivePass4(f *testing.F) {
	for op := range uint8(11) {
		f.Add(op, "remote title", int64(1700000000))
	}
	f.Fuzz(func(t *testing.T, op uint8, title string, stamp int64) {
		stamp = 1700000000 + stamp%10000
		thread := appwire.Thread{
			ID: "thread-1", SessionID: "thread-1", Source: "remote", Name: title,
			CWD: "/work/project", ModelProvider: "provider/model", CreatedAt: stamp - 10,
			UpdatedAt: stamp, Status: appwire.ThreadStatus{Type: "active"},
			Turns: []appwire.Turn{{ID: "turn-1", Status: appwire.TurnStatusCompleted}},
			Serf: appwire.SerfThread{Ref: "remote:thread-1", ActiveTurnID: "turn-2",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Clear: true, Shutdown: true, Queue: true},
				Usage:        &appwire.SerfUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		}
		web := pass4RemoteWeb(thread, nil)
		ref := "remote:thread-1"
		record := func(method, target, body string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, target, strings.NewReader(body))
			returnRec := rec
			web.Handler().ServeHTTP(returnRec, req)
			return returnRec
		}

		switch op % 11 {
		case 0:
			_ = record(http.MethodPost, "/api/sessions/remote%3Athread-1/send", `{"text":"hello"}`)
			_ = record(http.MethodPost, "/api/sessions/remote%3Athread-1/send", `{"items":[{"type":"text","text":"item"}]}`)
		case 3:
			for _, action := range []string{"interrupt", "clear", "shutdown"} {
				web.handleSessionAction(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/"+action, strings.NewReader(`{"turn_id":"turn-2"}`)), ref, action)
			}
		case 4:
			_ = record(http.MethodGet, "/api/sessions/remote%3Athread-1", "")
			_ = record(http.MethodGet, "/api/sessions/remote%3Athread-1/details", "")
			_, _ = web.apiSessionState(ref)
		case 5:
			_ = record(http.MethodGet, "/api/tree", "")
			_ = record(http.MethodGet, "/api/tree?summary=1", "")
		case 6:
			metas, live, _ := web.navigationTreeInputs(context.Background())
			if len(metas) != 1 || len(live) != 1 {
				t.Fatalf("remote inputs metas=%d live=%d", len(metas), len(live))
			}
			_ = web.apiTreeSources()
		case 7:
			threads := web.refreshRemoteThreads(context.Background())
			if len(threads) != 1 || threads[0].Serf.Ref == "" {
				t.Fatalf("remote threads=%+v", threads)
			}
		case 8:
			thread.Serf.Ref = ""
			thread.Status.Type = appwire.ThreadStatusClosed
			web = pass4RemoteWeb(thread, nil)
			_ = record(http.MethodGet, "/api/tree", "")
			_, _ = web.apiSessionDetail(ref)
		case 9:
			source := &stubThreadLister{id: "remote", resp: appwire.ThreadListResponse{Data: []appwire.Thread{thread}}}
			_ = web.listThreadsWithFallback(context.Background(), source)
			source.resp.Data = nil
			_ = web.listThreadsWithFallback(context.Background(), source)
			source.err = context.DeadlineExceeded
			_ = web.listThreadsWithFallback(context.Background(), source)
			_ = time.Unix(stamp, 0)
		case 10:
			stateDir := t.TempDir()
			parentID := "01PARENT00000000000000001"
			if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
				t.Fatal(err)
			}
			writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{SessionID: parentID, ProfileID: "openai", Model: "gpt-5"})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User("parent prompt"))); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: parentID, ProfileID: "openai", Model: "gpt-5"}); err != nil {
				t.Fatal(err)
			}
			forkWeb := NewWebServer(hubcore.WebConfig{StateDir: stateDir})
			forkWeb.handleAPIFork(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/fork", strings.NewReader(`{"turn":1,"edited_message":"again"}`)), parentID)
		}
	})
}
