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
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func covWebRequest(t *testing.T, web *WebServer, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestCovWebCoreAPIHelpersAndRoutes(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{})

	// Pure wire/diagnostic helpers.
	for _, tc := range []struct {
		code int
		want int
	}{
		{appwire.CodeInvalidParams, 400}, {appwire.CodeInvalidRequest, 400},
		{appwire.CodeMethodNotFound, 404}, {appwire.CodeConflict, 409},
		{appwire.CodeUnavailable, 503}, {appwire.CodeInternalError, 500},
		{12345, 418},
	} {
		if got := statusForWireError(appwire.WireError{Code: tc.code}, 418); got != tc.want {
			t.Fatalf("statusForWireError(%q)=%d, want %d", tc.code, got, tc.want)
		}
	}
	if got := serfErrorInfoFromData(map[string]any{"serfErrorInfo": "map"}); got != "map" {
		t.Fatal(got)
	}
	if got := serfErrorInfoFromData(map[string]any{"serfErrorInfo": 2}); got != "" {
		t.Fatal(got)
	}
	if got := serfErrorInfoFromData(nil); got != "" {
		t.Fatal(got)
	}
	for _, raw := range []string{
		`not-json`, `{"message":" explicit "}`, `{"warning":"warning text"}`,
		`{"warning":{"message":"nested"}}`, `{"warning":{"message":2}}`, `{}`,
	} {
		_ = warningPayload([]byte(raw))
	}
	p := map[string]any{"source": "s", "title": "t", "hint": "h"}
	addDiagnosticDefaults(p, "message")
	if gotP, gotM := splitProviderModel(" openai/model "); gotP != "openai" || gotM != "model" {
		t.Fatalf("split=%q/%q", gotP, gotM)
	}

	// Router and page branches.
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/new?dir=%20/tmp/x%20&prompt=hello"},
		{http.MethodGet, "/missing"},
		{http.MethodPost, "/_partials/workspace/empty"},
		{http.MethodGet, "/_partials/unknown"},
		{http.MethodGet, "/_partials/s/"},
		{http.MethodGet, "/_partials/s/id/unknown"},
		{http.MethodGet, "/manifest.webmanifest"},
		{http.MethodPost, "/api/health"}, {http.MethodGet, "/api/health"},
		{http.MethodPost, "/api/spawn-schema"}, {http.MethodGet, "/api/spawn-schema"},
		{http.MethodPost, "/api/path/validate"}, {http.MethodGet, "/api/path/validate?path=/tmp&kind=directory"},
		{http.MethodPost, "/api/git/head"},
	} {
		_ = covWebRequest(t, web, tc.method, tc.target, "")
	}

	// Directory listing and creation remain inside a temp root.
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, name := range []string{"alpha", "Beta", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "alpha", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"/api/dirs", "/api/dirs?prefix=~%2F", "/api/dirs?prefix=" + root + "/a",
		"/api/dirs?prefix=" + root + "/.", "/api/dirs?prefix=../bad",
		"/api/dirs?prefix=" + root + "/missing/",
	} {
		_ = covWebRequest(t, web, http.MethodGet, target, "")
	}
	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""}, {http.MethodPost, "{"}, {http.MethodPost, `{}`},
		{http.MethodPost, `{"path":"relative"}`}, {http.MethodPost, `{"path":"~"}`},
		{http.MethodPost, `{"path":"` + root + `/file"}`},
		{http.MethodPost, `{"path":"` + root + `/created"}`},
		{http.MethodPost, `{"path":"` + root + `/created"}`},
	} {
		_ = covWebRequest(t, web, tc.method, "/api/dirs/create", tc.body)
	}

	failWeb := NewWebServer(hubcore.WebConfig{MkdirAll: func(string, os.FileMode) error { return errors.New("mkdir failed") }})
	_ = covWebRequest(t, failWeb, http.MethodPost, "/api/dirs/create", `{"path":"`+root+`/fail"}`)
	gitWeb := NewWebServer(hubcore.WebConfig{GitHeadBranch: func(context.Context, string) (string, error) { return "branch", nil }})
	_ = covWebRequest(t, gitWeb, http.MethodGet, "/api/git/head?cwd="+root, "")
	gitWeb.cfg.GitHeadBranch = func(context.Context, string) (string, error) { return "", errors.New("git failed") }
	_ = covWebRequest(t, gitWeb, http.MethodGet, "/api/git/head?cwd="+root, "")
}

func TestCovWebCoreAPIDecisionValidation(t *testing.T) {
	root := t.TempDir()
	archive := hubcore.NewArchiveStore(filepath.Join(root, "decisions.db"))
	favorite := hubcore.NewFavoriteStore(filepath.Join(root, "decisions.db"))
	poked := 0
	web := NewWebServer(hubcore.WebConfig{Archive: archive, Favorite: favorite, PokeAttention: func() { poked++ }})

	for _, endpoint := range []string{"archive", "favorite"} {
		for _, tc := range []struct{ method, body string }{
			{http.MethodGet, ""}, {http.MethodPost, "{"},
			{http.MethodPost, `{"kind":"bad","id":"x"}`},
			{http.MethodPost, `{"kind":"session","id":""}`},
		} {
			_ = covWebRequest(t, web, tc.method, "/api/"+endpoint, tc.body)
		}
	}
	_ = covWebRequest(t, NewWebServer(hubcore.WebConfig{}), http.MethodPost, "/api/archive", `{"kind":"session","id":"x","archived":true}`)
	_ = covWebRequest(t, NewWebServer(hubcore.WebConfig{}), http.MethodPost, "/api/favorite", `{"kind":"project","id":"x","favorited":true}`)
	_ = covWebRequest(t, web, http.MethodPost, "/api/archive", `{"kind":"session","id":"x","archived":true}`)
	_ = covWebRequest(t, web, http.MethodPost, "/api/archive", `{"kind":"project","id":"/a/proj","archived":false}`)
	_ = covWebRequest(t, web, http.MethodPost, "/api/favorite", `{"kind":"project","id":"x","favorited":true}`)
	if poked != 3 {
		t.Fatalf("pokes=%d", poked)
	}

	badPath := filepath.Join(root, "dbdir")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := NewWebServer(hubcore.WebConfig{Archive: hubcore.NewArchiveStore(badPath), Favorite: hubcore.NewFavoriteStore(badPath)})
	_ = covWebRequest(t, bad, http.MethodPost, "/api/archive", `{"kind":"session","id":"x","archived":true}`)
	_ = covWebRequest(t, bad, http.MethodPost, "/api/favorite", `{"kind":"session","id":"x","favorited":true}`)
}

func TestCovWebCoreAPIDeleteAndRenameValidation(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{})
	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""}, {http.MethodPost, "{"}, {http.MethodPost, `{}`},
		{http.MethodPost, `{"key":"k","working_dir":"/w"}`},
	} {
		req := httptest.NewRequest(tc.method, "/api/project/delete", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		web.handleAPIProjectDelete(rec, req)
	}

	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""}, {http.MethodPost, "{"}, {http.MethodPost, `{}`},
		{http.MethodPost, `{"name":" renamed "}`},
	} {
		req := httptest.NewRequest(tc.method, "/rename", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		web.handleAPIRename(rec, req, "missing")
	}

	// An indexed entry with no meta file reaches the deterministic load error.
	stateDir := t.TempDir()
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{ID: "gone", UpdatedAt: time.Unix(1, 0)}})
	loadFail := NewWebServer(hubcore.WebConfig{Past: past})
	// SeedForTest has no StateDir, which is sufficient to exercise LoadSessionMeta's error path.
	req := httptest.NewRequest(http.MethodPost, "/rename", strings.NewReader(`{"name":"new"}`))
	loadFail.handleAPIRename(httptest.NewRecorder(), req, "gone")

	// refreshRenamedMeta covers both the persisted-meta and fallback-index paths.
	meta := schema.SessionMeta{ID: "saved", Name: "disk", UpdatedAt: time.Unix(2, 0)}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(stateDir, "missing-glob"))
	idx.SeedForTest([]schema.SessionMeta{meta})
	refresh := NewWebServer(hubcore.WebConfig{Past: idx})
	refresh.refreshRenamedMeta("saved", "fallback")
	refresh.refreshRenamedMeta("missing", "fallback")
}
