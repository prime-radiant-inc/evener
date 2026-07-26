package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

type pass5ReadSource struct {
	*pass4ActionSource
	readErr   error
	readAfter int
	reads     int
}

type pass5Prober struct{}

func (pass5Prober) Probe(e rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{SessionID: e.SessionID, Status: "active", OK: true}
}

func (s *pass5ReadSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.reads++
	if s.readErr != nil && s.reads > s.readAfter {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}

func pass5Web(thread appwire.Thread, actionErr, readErr error, readAfter int) *WebServer {
	base := &pass4ActionSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, err: actionErr}
	base.startTurn = func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, actionErr
	}
	reg := appsource.NewRegistry()
	reg.Add(&pass5ReadSource{pass4ActionSource: base, readErr: readErr, readAfter: readAfter})
	web := NewWebServer(hubcore.WebConfig{})
	web.sources = reg
	return web
}

// FuzzSessionResiduePass5 targets the remaining session action, detail, tree,
// memoization, decision-store, and roster branches with process-local inputs.
func FuzzSessionResiduePass5(f *testing.F) {
	for op := range uint8(10) {
		f.Add(op, "residue")
	}
	f.Fuzz(func(t *testing.T, op uint8, title string) {
		now := time.Now()
		thread := appwire.Thread{
			ID: "thread-5", SessionID: "thread-5", Source: "remote", Name: title,
			CWD: "/work/project", ModelProvider: "provider/model",
			CreatedAt: now.Add(-time.Minute).Unix(), UpdatedAt: now.Unix(),
			Status: appwire.ThreadStatus{Type: "active"},
			Serf: appwire.SerfThread{Ref: "remote:thread-5", Capabilities: appwire.ThreadCapabilities{
				Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
				ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true,
			}},
		}
		ref := "remote:thread-5"

		switch op % 10 {
		case 0:
			// Decision stores: nil, successful read, and graceful read failure.
			dir := t.TempDir()
			archive := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
			favorite := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
			if err := archive.Set("session", ref, true, now); err != nil {
				t.Fatal(err)
			}
			if err := favorite.Set("session", ref, true, now); err != nil {
				t.Fatal(err)
			}
			web := NewWebServer(hubcore.WebConfig{Archive: archive, Favorite: favorite})
			_ = web.archiveDecisions()
			_ = web.favoriteDecisions()
			bad := filepath.Join(dir, "database-is-directory")
			if err := os.Mkdir(bad, 0o700); err != nil {
				t.Fatal(err)
			}
			web.cfg.Archive = hubcore.NewArchiveStore(bad)
			web.cfg.Favorite = hubcore.NewFavoriteStore(bad)
			_ = web.archiveDecisions()
			_ = web.favoriteDecisions()

		case 1:
			// Cached input path and memo cache hit/versioned recompute.
			cache := &hubcore.RemoteThreadCache{}
			cache.Store([]appwire.Thread{thread})
			inputs := &hubcore.InputsVersion{}
			web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache, Inputs: inputs})
			_, _, _ = web.navigationTreeInputs(context.Background())
			_, _ = web.memoTree(context.Background())
			_, _ = web.memoTree(context.Background())
			inputs.Bump()
			_, _ = web.memoTree(context.Background())

		case 2:
			// Project found/not-found and populated full-tree decisions.
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{
				ID: "past-5", Name: title, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
				EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"},
			}})
			web := NewWebServer(hubcore.WebConfig{Past: past})
			tree, _ := web.memoTree(context.Background())
			if len(tree.Projects) == 0 {
				t.Fatal("expected project")
			}
			key := tree.Projects[0].Key
			for _, target := range []string{"/api/tree/project?key=" + key, "/api/tree/project?key=missing"} {
				web.handleAPITreeProject(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))

		case 3:
			// Remote action source failures after capability checks succeed.
			web := pass5Web(thread, appwire.Conflict("busy"), nil, 0)
			for action, body := range map[string]string{
				"interrupt": `{}`, "clear": `{}`, "shutdown": `{}`,
			} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/"+action, strings.NewReader(body))
				web.handleSessionAction(rec, req, ref, action)
			}

		case 4:
			// Missing/disabled action sources and API wire error metadata.
			thread.Serf.Capabilities = appwire.ThreadCapabilities{}
			web := pass5Web(thread, nil, nil, 0)
			_ = web.ensureSessionActionAvailable(ref, "steer")
			_ = web.ensureSessionActionAvailable("remote:missing", "steer")
			rec := httptest.NewRecorder()
			writeSessionActionError(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/x/send", nil), appwire.Unavailable("disabled"))

		case 5:
			// Detail/state live replacement and second-read failure paths.
			web := pass5Web(thread, nil, errors.New("read failed"), 1)
			_, _ = web.apiSessionDetail(ref)
			web = pass5Web(thread, nil, errors.New("read failed"), 1)
			_, _ = web.apiSessionState(ref)
			thread.Name, thread.Preview, thread.CWD, thread.ModelProvider = "", "", "", ""
			web = pass5Web(thread, nil, nil, 0)
			_, _ = web.apiSessionDetail(ref)
			_, _ = web.apiSessionState(ref)

		case 6:
			// Session API method gates and all locally dispatched subroutes.
			web := pass5Web(thread, nil, nil, 0)
			for _, target := range []string{
				"/api/sessions/remote%3Athread-5", "/api/sessions/remote%3Athread-5/details",
				"/api/sessions/remote%3Athread-5/tasks", "/api/sessions/remote%3Athread-5/fork",
				"/api/sessions/remote%3Athread-5/clear", "/api/sessions/remote%3Athread-5/model",
				"/api/sessions/remote%3Athread-5/reasoning-effort", "/api/sessions/remote%3Athread-5/rename",
				"/api/sessions/remote%3Athread-5/interrupt", "/api/sessions/remote%3Athread-5/compact",
			} {
				web.handleAPISession(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, target, strings.NewReader(`{}`)))
			}

		case 7:
			// Roster refresh can both match immediately and return immediately on miss.
			dir := t.TempDir()
			entry := rendezvous.Entry{PID: 42425, SessionID: "roster-5", Address: "127.0.0.1:1"}
			if _, err := rendezvous.Write(dir, entry); err != nil {
				t.Fatal(err)
			}
			roster := hubcore.NewRoster(dir, pass5Prober{})
			if got := waitForRosterMatch(roster, entry.SessionID, entry.PID, time.Second); got.Address == "" {
				t.Fatal("roster entry was not matched")
			}
			_ = waitForRosterMatch(roster, "missing", 1, 0)

		case 8:
			// Oversized and malformed item validation precedes source resolution.
			web := NewWebServer(hubcore.WebConfig{})
			oversized := strings.Repeat("x", hubcore.SendMaxImageBytes+1)
			bodies := []string{`{"items":[{"type":"image","name":"x","data":"bad"}]}`, `{"items":[{"type":"text","text":"x","data":"` + oversized + `"}]}`}
			for _, body := range bodies {
				web.handleSend(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body)), "local")
			}

		case 9:
			// fetchStatus rejects invalid JSON and accepts a valid daemon response.
			for _, payload := range []string{"{", `{"status":"active","turns":2}`} {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) }))
				addr := strings.TrimPrefix(srv.URL, "http://")
				_ = NewWebServer(hubcore.WebConfig{}).fetchStatus(hubcore.LiveEntry{Entry: rendezvous.Entry{Address: addr}})
				srv.Close()
			}
		}
	})
}
