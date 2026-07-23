package main

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/rendezvous"
)

type pass6WorkspaceSource struct {
	*scriptedAppSource
	readErr, nameErr, tasksErr error
	tasks                      any
}

func (s *pass6WorkspaceSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}

func (s *pass6WorkspaceSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return s.nameErr
}

func (s *pass6WorkspaceSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	if s.tasksErr != nil {
		return appwire.TaskListResponse{}, s.tasksErr
	}
	return appwire.TaskListResponse{Data: s.tasks}, nil
}

// FuzzWorkspaceMutationsPass6 closes workspace rendering and destructive
// mutation branches using local state and scripted app sources only.
func FuzzWorkspaceMutationsPass6(f *testing.F) {
	for mode := uint8(0); mode < 10; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode uint8) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		work := filepath.Join(root, "same")
		otherWork := filepath.Join(root, "other", "same")
		for _, p := range []string{filepath.Join(state, "sessions"), work, otherWork} {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		parent := schema.SessionMeta{ID: "parent", Name: "Parent"}
		ended := schema.SessionMeta{ID: "ended", Model: "openai/gpt-4o", ProfileID: "profile", ParentSessionID: "parent", ForkLabel: "original", DivergenceTurn: 3, IsSubagent: true, OriginalPrompt: "prompt", TurnCount: 2, WorkMillis: 1500, LastInputTokens: 7, WorktreePath: filepath.Join(root, "tree"), ObservedBy: []string{"observer"}, CumulativeUsage: schema.CumulativeUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
		ended.EnvInfo.WorkingDir, ended.EnvInfo.GitBranch = work, "main"
		child := schema.SessionMeta{ID: "child", Name: "Child", ParentSessionID: "ended"}
		other := schema.SessionMeta{ID: "other"}
		other.EnvInfo.WorkingDir = otherWork
		for _, m := range []schema.SessionMeta{parent, ended, child, other} {
			if err := schema.SaveSessionMeta(state, m); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(state, "sessions", "ended.transcript.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "sessions", "ended.api.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(state, "tasks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "tasks", "ended.json"), []byte("[{}]"), 0o644); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndex(state)
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		thread := appwire.Thread{ID: "thread", SessionID: "thread", Source: "remote", Name: "Live", CWD: work, ModelProvider: "openai/gpt-4o", Status: appwire.ThreadStatus{Type: "active"}, Serf: appwire.SerfThread{Ref: "remote:thread", ActiveTurnID: "turn", Capabilities: appwire.ThreadCapabilities{Send: true}}}
		source := &pass6WorkspaceSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, tasks: []map[string]any{{"id": "task"}}}
		roster := hubcore.NewRosterWithEntries()
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
		web.sources.Add(source)
		call := func(fn func(http.ResponseWriter, *http.Request), method, target, body string) *httptest.ResponseRecorder {
			rr := httptest.NewRecorder()
			fn(rr, httptest.NewRequest(method, target, strings.NewReader(body)))
			return rr
		}

		switch mode % 10 {
		case 0:
			for _, target := range []string{"/s/remote:thread/send", "/s/remote:thread/fork", "/s/remote:thread/interrupt", "/s/remote:thread/compact", "/s/remote:thread/shutdown", "/s/remote:thread/clear", "/s/remote:thread/steer", "/s/remote:thread/queue", "/s/remote:thread/drain-as-steer", "/s/remote:thread/images/nope"} {
				web.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
			r := httptest.NewRequest(http.MethodGet, "/s/remote:thread", nil)
			r.Header.Set("HX-Request", "true")
			web.handleSession(httptest.NewRecorder(), r)
		case 1:
			bad := template.New("bad")
			web.appTmpl, web.threadTmpl = bad, bad
			web.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s/remote:thread", nil))
			web.renderThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "remote:missing")
		case 2:
			web.handleThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/thread/ended", nil))
			web.renderThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "remote:missing")
			web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "remote:thread")
			source.tasksErr = errors.New("tasks")
			web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "ended")
			web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing")
		case 3:
			_ = web.workspaceData("remote:thread")
			source.readErr = errors.New("read")
			_ = web.workspaceData("remote:thread")
			_, _ = web.liveWorkspaceSnapshot("remote:thread", hubapi.SessionCapabilities{Resume: true})
			_, _ = web.liveWorkspaceSnapshot("missing:thread", hubapi.SessionCapabilities{Resume: true})
		case 4:
			data := WorkspaceData{}
			web.fillForkLineage(&data, schema.SessionMeta{})
			web.fillForkLineage(&data, schema.SessionMeta{ID: "none", ForkLabel: "x"})
			webNil := NewWebServer(hubcore.WebConfig{})
			webNil.fillForkLineage(&data, ended)
			webNil.fillSubagentLineage(&data, ended)
			web.fillSubagentLineage(&data, schema.SessionMeta{})
			web.fillSubagentLineage(&data, schema.SessionMeta{ID: "x", IsSubagent: true, ParentSessionID: "unknown-parent-id"})
		case 5:
			call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, http.MethodGet, "/", "")
			call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, http.MethodPost, "/", "{")
			call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "missing") }, http.MethodPost, "/", `{"name":"x"}`)
			loadSessionMetaForRename = func(string, string) (schema.SessionMeta, error) { return schema.SessionMeta{}, errors.New("load") }
			t.Cleanup(func() { loadSessionMetaForRename = schema.LoadSessionMeta })
			call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, http.MethodPost, "/", `{"name":"x"}`)
		case 6:
			saveSessionMetaForRename = func(string, schema.SessionMeta) error { return errors.New("save") }
			t.Cleanup(func() { saveSessionMetaForRename = schema.SaveSessionMeta })
			call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, http.MethodPost, "/", `{"name":"x"}`)
			liveRoster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "thread"}, SessionID: "thread", Status: "active"})
			liveWeb := NewWebServer(hubcore.WebConfig{Past: past, Roster: liveRoster})
			liveWeb.sources.Add(source)
			source.nameErr = nil
			call = func(fn func(http.ResponseWriter, *http.Request), method, target, body string) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				fn(rr, httptest.NewRequest(method, target, strings.NewReader(body)))
				return rr
			}
			call(func(w http.ResponseWriter, r *http.Request) { liveWeb.handleAPIRename(w, r, "remote:thread") }, http.MethodPost, "/", `{"name":"x"}`)
			source.nameErr = errors.New("rename")
			call(func(w http.ResponseWriter, r *http.Request) { liveWeb.handleAPIRename(w, r, "remote:thread") }, http.MethodPost, "/", `{"name":"x"}`)
		case 7:
			call(web.handleAPIProjectDelete, http.MethodGet, "/", "")
			call(web.handleAPIProjectDelete, http.MethodPost, "/", "{")
			call(web.handleAPIProjectDelete, http.MethodPost, "/", `{}`)
			NewWebServer(hubcore.WebConfig{}).handleAPIProjectDelete(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"x","working_dir":"x"}`)))
			call(web.handleAPIProjectDelete, http.MethodPost, "/", `{"key":"wrong","working_dir":"`+work+`"}`)
		case 8:
			body := `{"key":"` + testProjectID(t, work) + `","working_dir":"` + work + `"}`
			live := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: "ended", Status: "active"})})
			call = func(fn func(http.ResponseWriter, *http.Request), method, target, b string) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				fn(rr, httptest.NewRequest(method, target, strings.NewReader(b)))
				return rr
			}
			call(live.handleAPIProjectDelete, http.MethodPost, "/", body)
			removeProjectSessionFile = func(string) error { return errors.New("remove") }
			t.Cleanup(func() { removeProjectSessionFile = os.Remove })
			call(web.handleAPIProjectDelete, http.MethodPost, "/", body)
		case 9:
			body := `{"key":"` + testProjectID(t, work) + `","working_dir":"` + work + `"}`
			removeProjectSessionDir = func(string) error { return errors.New("ignored") }
			t.Cleanup(func() { removeProjectSessionDir = os.RemoveAll })
			call(web.handleAPIProjectDelete, http.MethodPost, "/", body)
		}
	})
}
