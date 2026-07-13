package main

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type residueSource struct {
	*scriptedAppSource
	fail bool
}

func (s *residueSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	if s.fail {
		return appwire.ThreadClearResponse{}, errors.New("clear")
	}
	return appwire.ThreadClearResponse{Thread: appwire.Thread{ID: "renamed"}}, nil
}
func (s *residueSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	if s.fail {
		return errors.New("model")
	}
	return nil
}
func (s *residueSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	if s.fail {
		return errors.New("effort")
	}
	return nil
}
func (s *residueSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	if s.fail {
		return errors.New("rename")
	}
	return nil
}

func FuzzWebAPIResiduePass5(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		wd := filepath.Join(root, "work", "same")
		state := filepath.Join(root, "state")
		if err := os.MkdirAll(filepath.Join(state, "sessions", "ended"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(wd, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "work", ".hidden"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "work", "SameOther"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "work", "plain"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		meta := schema.SessionMeta{ID: "ended", Name: "old", UpdatedAt: time.Unix(1700000000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
		if err := schema.SaveSessionMeta(state, meta); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndex(filepath.Join(root, "*"))
		if err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		roster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{SessionID: ""},
			hubcore.LiveEntry{SessionID: "live", Status: "active"},
			hubcore.LiveEntry{SessionID: "zzz", Status: "idle"},
		)
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, PokeAttention: func() {}})
		caps := appwire.ThreadCapabilities{Clear: true, ChangeModel: true, Rename: true}
		src := &residueSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: appwire.Thread{ID: "live", SessionID: "live", Source: "remote", Name: "Live", CWD: wd, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Serf: appwire.SerfThread{Ref: "remote:live", Capabilities: caps}}}, fail: variant&1 != 0}
		web.sources.Add(src)
		call := func(fn func(http.ResponseWriter, *http.Request), method, target, body string) {
			r := httptest.NewRequest(method, target, strings.NewReader(body))
			w := httptest.NewRecorder()
			fn(w, r)
		}

		_ = inputStripTemplateFuncs["formatTokenCount"].(func(int64) string)(12)
		web.lockForSession("a")
		web.lockForSession("a")
		next := false
		validAssetPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { next = true })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
		bad := httptest.NewRequest(http.MethodGet, "/", nil)
		bad.URL.Path = string([]byte{'/', 0xff})
		validAssetPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), bad)
		_ = next
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/new?dir=%20/tmp/x%20&prompt=hi", nil))
		web.appTmpl = template.Must(template.New("app").Parse(`{{define "app"}}{{template "missing" .}}{{end}}`))
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		web.manifestFS = fstest.MapFS{}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		web.manifestFS = fstest.MapFS{"manifest.webmanifest": {Data: []byte("{")}}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		web.manifestFS = fstest.MapFS{"manifest.webmanifest": {Data: []byte(`{"start_url":"/"}`), Mode: fs.FileMode(0o644)}}
		web.cfg.AuthToken = "a b"
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))

		call(web.handleInternalPartial, "POST", "/_partials/workspace/empty", "")
		call(web.handleInternalPartial, http.MethodGet, "/_partials/workspace/empty", "")
		for _, p := range []string{"/_partials/s/", "/_partials/s/local:ended/workspace", "/_partials/s/ended/state", "/_partials/s/ended/details", "/_partials/s/ended/tasks", "/_partials/s/ended/nope", "/_partials/nope"} {
			r := httptest.NewRequest(http.MethodGet, p, nil)
			r.Header.Set("HX-Request", "true")
			web.handleInternalPartial(httptest.NewRecorder(), r)
		}
		_ = canonicalRouteID("local:x")

		call(web.handleApiSearch, http.MethodGet, "/api/search?q=no-match", "")
		_ = serfErrorInfoFromData(nil)
		_ = serfErrorInfoFromData(map[string]any{"serfErrorInfo": 1})
		webNil := NewWebServer(hubcore.WebConfig{})
		_ = webNil.apiStateGlob()
		_ = warningMessage([]byte(`{"warning":{}}`))
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "missing") }, "POST", "/", "")
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "remote:live") }, "POST", "/", "")
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIModel(w, r, "remote:live") }, "POST", "/", `{"model":"/"}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIReasoningEffort(w, r, "remote:live") }, "POST", "/", `{`)
		call(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix=", "")
		call(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix=~", "")
		call(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix="+filepath.Join(root, "work")+"/", "")
		call(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix="+filepath.Join(root, "work", "Sa"), "")
		call(web.handleAPIDirCreate, http.MethodGet, "/", "")
		call(web.handleAPIDirCreate, "POST", "/", `{`)
		call(web.handleAPIDirCreate, "POST", "/", `{"path":"~"}`)

		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, "POST", "/", `{"name":""}`)
		call(func(w http.ResponseWriter, r *http.Request) { web.handleAPIRename(w, r, "ended") }, "POST", "/", `{"name":"new"}`)
		web.refreshRenamedMeta("missing", "fallback")
		web.refreshRenamedMeta("ended", "fallback")
		if err := os.Remove(filepath.Join(state, "sessions", "ended.meta.json")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		web.refreshRenamedMeta("ended", "fallback")
	})
}
