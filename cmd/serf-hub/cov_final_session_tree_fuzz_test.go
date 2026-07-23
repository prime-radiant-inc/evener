package main

import (
	"context"
	"encoding/base64"
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
	"primeradiant.com/serf/rendezvous"
)

type finalSessionSource struct {
	*pass4ActionSource
	startErrs []error
}

func (s *finalSessionSource) StartTurn(ctx context.Context, p appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	if len(s.startErrs) == 0 {
		return appwire.TurnStartResponse{}, nil
	}
	err := s.startErrs[0]
	s.startErrs = s.startErrs[1:]
	return appwire.TurnStartResponse{}, err
}

type finalSessionSpawner struct {
	entry rendezvous.Entry
	err   error
}

func (s finalSessionSpawner) Spawn(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return s.entry, s.err
}

func (s finalSessionSpawner) Resume(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
	return s.entry, s.err
}

func finalSessionWeb(cfg hubcore.WebConfig, thread appwire.Thread, startErrs ...error) *WebServer {
	base := &pass4ActionSource{scriptedAppSource: &scriptedAppSource{id: thread.Source, thread: thread}}
	source := &finalSessionSource{pass4ActionSource: base, startErrs: startErrs}
	reg := appsource.NewRegistry()
	reg.Add(source)
	web := NewWebServer(cfg)
	web.sources = reg
	return web
}

func FuzzFinalSessionTree(f *testing.F) {
	for op := uint8(0); op < 16; op++ {
		f.Add(op)
	}
	f.Fuzz(func(t *testing.T, op uint8) {
		now := time.Now().UTC()
		caps := appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true, ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true}
		thread := appwire.Thread{ID: "thread", SessionID: "thread", Source: "remote", Name: "title", CWD: "/work/project", ModelProvider: "p/m", CreatedAt: now.Add(-time.Minute).Unix(), UpdatedAt: now.Unix(), Status: appwire.ThreadStatus{Type: "active"}, Serf: appwire.SerfThread{Ref: "remote:thread", Capabilities: caps}}
		call := func(fn func(http.ResponseWriter, *http.Request), body string) {
			fn(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		}

		switch op % 16 {
		case 0:
			web := finalSessionWeb(hubcore.WebConfig{}, thread)
			call(func(w http.ResponseWriter, r *http.Request) { web.handleSend(w, r, "remote:thread") }, `{"text":"ok"}`)
			huge := base64.StdEncoding.EncodeToString(make([]byte, hubcore.SendMaxImageBytes+1))
			for _, body := range []string{`{"items":[{"type":"image"}` + strings.Repeat(`,{"type":"image"}`, hubcore.SendMaxImageItems) + `]}`, `{"items":[{"type":"text","text":"x","data":"` + huge + `"}]}`} {
				call(func(w http.ResponseWriter, r *http.Request) { web.handleSend(w, r, "local") }, body)
			}
		case 2:
			web := finalSessionWeb(hubcore.WebConfig{}, thread)
			for _, action := range []string{"interrupt", "clear", "shutdown", "unknown"} {
				call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "remote:thread", action) }, `{}`)
			}
		case 4:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "local", Name: "local", Model: "p/m", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"}}})
			web := NewWebServer(hubcore.WebConfig{Past: past})
			call(func(w http.ResponseWriter, r *http.Request) { web.handleSend(w, r, "local") }, `{"text":"x"}`)
		case 5:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "local", Name: "local", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"}}})
			web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries(), Spawner: finalSessionSpawner{err: errors.New("spawn failed")}})
			call(func(w http.ResponseWriter, r *http.Request) { web.handleSend(w, r, "local") }, `{"text":"x"}`)
		case 6:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{
				{ID: "active", Name: "active", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/a"}},
				{ID: "test", Name: "test", Origin: "test", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/tests"}},
				{ID: "old", Name: "old", CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now.Add(-30 * 24 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/old"}},
			})
			roster := hubcore.NewRosterWithEntries(
				hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "active", WorkingDir: "/work/a", StartedAt: now}, SessionID: "active", Status: "active"},
				hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "same-project-orphan", WorkingDir: "/work/a", StartedAt: now}, SessionID: "same-project-orphan", Status: "error"},
				hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "orphan", WorkingDir: "", StartedAt: now}, SessionID: "orphan", Status: "error"},
				hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "ended-live", WorkingDir: "/work/end", StartedAt: now}, SessionID: "ended-live", Status: "ended"},
			)
			web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))
		case 7:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "fav", Name: "fav", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/fav"}}})
			dir := t.TempDir()
			fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
			_ = fav.Set("session", "fav", true, now)
			web := NewWebServer(hubcore.WebConfig{Past: past, Favorite: fav})
			web.handleAPITree(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tree", nil))
		case 8:
			cache := &hubcore.RemoteThreadCache{}
			cache.Store([]appwire.Thread{{ID: "r", Source: "remote", Status: appwire.ThreadStatus{Type: "closed"}}})
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "p", CreatedAt: now, UpdatedAt: now}})
			web := NewWebServer(hubcore.WebConfig{Past: past, RemoteThreadCache: cache})
			_, _, _ = web.navigationTreeInputs(context.Background())
		case 9:
			web := NewWebServer(hubcore.WebConfig{})
			web.sources = appsource.NewRegistry()
			_ = web.refreshRemoteThreads(context.Background())
			_ = web.apiTreeSources()
			_, _, _ = appThreadTreeEntries(appwire.Thread{ID: "x", Source: "remote", GitInfo: &appwire.GitInfo{Branch: "b", OriginURL: "u"}})
		case 10:
			thread.Name, thread.Preview, thread.SessionID, thread.CWD, thread.Status.Type = "", "", "", "", ""
			thread.Serf.Goal = &appwire.GoalState{Status: "active", Iterations: 2}
			_ = hubDetailFromAppThread(thread)
		case 11:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "local", Name: "past title", Model: "past-model", ProfileID: "profile", TurnCount: 3, ParentSessionID: "parent", DivergenceTurn: 2, ForkLabel: "fork", IsSubagent: true, CreatedAt: now, UpdatedAt: now}})
			web := NewWebServer(hubcore.WebConfig{Past: past})
			_, _ = web.apiSessionDetail("local")
			_, _ = web.apiSessionState("local")
		case 12:
			local := thread
			local.ID, local.SessionID, local.Source, local.Serf.Ref, local.Name, local.CWD, local.ModelProvider = "local", "local", "local", "local:local", "", "", ""
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "local"}, SessionID: "local"})
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "local", Name: "resolved", Model: "fallback", TurnCount: 2, CreatedAt: now, UpdatedAt: now}})
			web := finalSessionWeb(hubcore.WebConfig{Past: past, Roster: roster}, local)
			_, _ = web.apiSessionDetail("local")
			_, _ = web.apiSessionState("local")
		case 13:
			web := NewWebServer(hubcore.WebConfig{})
			_ = web.ensureSessionActionAvailable("missing", "send")
			web.handleSessionAction(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing", "compact")
		case 15:
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "local"}, SessionID: "local"})
			web := NewWebServer(hubcore.WebConfig{Roster: roster})
			for _, action := range []string{"interrupt", "clear", "shutdown", "unknown"} {
				call(func(w http.ResponseWriter, r *http.Request) { web.handleSessionAction(w, r, "local", action) }, `{}`)
			}
		}
	})
}
