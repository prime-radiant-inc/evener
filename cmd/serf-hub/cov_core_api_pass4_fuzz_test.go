package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type coreAPIPass4Source struct {
	*scriptedAppSource
	fail bool
}

func (s *coreAPIPass4Source) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	if s.fail {
		return appwire.ThreadClearResponse{}, appwire.Conflict("clear conflict")
	}
	return appwire.ThreadClearResponse{Thread: appwire.Thread{ID: "cleared", Serf: appwire.SerfThread{Ref: "remote:cleared"}}}, nil
}

func (s *coreAPIPass4Source) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	if s.fail {
		return errors.New("model failed")
	}
	return nil
}

func (s *coreAPIPass4Source) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	if s.fail {
		return appwire.Unavailable("effort unavailable")
	}
	return nil
}

func (s *coreAPIPass4Source) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	if s.fail {
		return appwire.MethodNotFound("rename unsupported")
	}
	return nil
}

func FuzzCoreAPIPass4(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "state")
		workingDir := filepath.Join(root, "work", "project")
		if err := os.MkdirAll(workingDir, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := schema.SessionMeta{ID: "ended", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir}}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
		liveMeta := schema.SessionMeta{ID: "thread", Name: "live old", UpdatedAt: time.Unix(1_700_000_001, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir}}
		if err := schema.SaveSessionMeta(stateDir, liveMeta); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndex("")
		past.SeedForTest([]schema.SessionMeta{meta})
		// SeedForTest has no state directory, so replace the row with a real one.
		past = hubcore.NewPastIndex(filepath.Join(root, "*"))
		if err := os.Rename(stateDir, filepath.Join(root, "project-state")); err != nil {
			t.Fatal(err)
		}
		if err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}

		caps := appwire.ThreadCapabilities{Clear: true, ChangeModel: true, Rename: true}
		base := &scriptedAppSource{id: "remote", thread: appwire.Thread{
			ID: "thread", SessionID: "thread", Source: "remote", Name: "Live Name", CWD: workingDir,
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:   appwire.SerfThread{Ref: "remote:thread", Capabilities: caps},
		}}
		source := &coreAPIPass4Source{scriptedAppSource: base, fail: variant&1 != 0}
		poke := 0
		web := NewWebServer(hubcore.WebConfig{
			HubAddr: "127.0.0.1:9180", Past: past, Roster: hubcore.NewRosterWithEntries(),
			PokeAttention: func() { poke++ },
			GitHeadBranch: func(context.Context, string) (string, error) {
				if variant&1 != 0 {
					return "", errors.New("git failed")
				}
				return "main", nil
			},
		})
		web.sources.Add(source)

		call := func(method, target, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, target, strings.NewReader(body))
			req.Host = "127.0.0.1:9180"
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			return rec
		}

		call(http.MethodGet, "/api/search?q=live", "")
		call(http.MethodGet, "/api/search?q=", "")
		call(http.MethodGet, "/api/health", "")
		call(http.MethodPost, "/api/health", "")
		direct := func(fn func(http.ResponseWriter, *http.Request), method, target, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, target, strings.NewReader(body))
			rec := httptest.NewRecorder()
			fn(rec, req)
			return rec
		}
		direct(web.handleAPISpawnSchema, http.MethodGet, "/api/spawn/schema", "")
		direct(web.handleAPISpawnSchema, http.MethodPost, "/api/spawn/schema", "")
		direct(web.handleAPIUpgrade, http.MethodGet, "/api/upgrade", "")
		direct(web.handleAPIUpgrade, http.MethodPost, "/api/upgrade", "{")
		call(http.MethodPost, "/api/sessions/remote:thread/clear", "")
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "remote:thread") }, http.MethodGet, "/clear", "")
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIClear(w, r, "local:missing") }, http.MethodPost, "/clear", "")
		call(http.MethodPost, "/api/sessions/remote:thread/model", `{"model":"openai/gpt-pass4"}`)
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIModel(w, r, "remote:thread") }, http.MethodGet, "/model", "")
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIModel(w, r, "local:missing") }, http.MethodPost, "/model", `{}`)
		call(http.MethodPost, "/api/sessions/remote:thread/reasoning-effort", `{"reasoning_effort":" high "}`)
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIReasoningEffort(w, r, "remote:thread") }, http.MethodGet, "/effort", "")
		direct(func(w http.ResponseWriter, r *http.Request) { web.handleAPIReasoningEffort(w, r, "local:missing") }, http.MethodPost, "/effort", `{}`)
		call(http.MethodPost, "/api/sessions/remote:thread/rename", `{"name":" renamed "}`)
		call(http.MethodPost, "/api/sessions/remote:thread/model", `{`)
		call(http.MethodGet, "/api/git/head?cwd="+workingDir, "")
		call(http.MethodGet, "/api/git/head?cwd=/definitely/missing/pass4", "")
		call(http.MethodPost, "/api/git/head", "")
		direct(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix="+workingDir+"/", "")
		direct(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix=../bad", "")
		direct(web.handleApiDirs, http.MethodGet, "/api/dirs?prefix=/definitely/missing/pass4/", "")
		direct(web.handleAPIPathValidate, http.MethodGet, "/api/path/validate?path="+workingDir+"&kind=directory", "")
		direct(web.handleAPIPathValidate, http.MethodPost, "/api/path/validate", "")
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{}`)
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{"path":"relative"}`)
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{"path":"`+workingDir+`"}`)
		filePath := filepath.Join(root, "already-file")
		if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{"path":"`+filePath+`"}`)
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{"path":"`+filepath.Join(root, "created-dir")+`"}`)
		web.cfg.MkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") }
		direct(web.handleAPIDirCreate, http.MethodPost, "/api/dirs/create", `{"path":"`+filepath.Join(root, "new-dir")+`"}`)

		call(http.MethodPost, "/api/sessions/local:ended/rename", `{"name":" ended renamed "}`)
		call(http.MethodPost, "/api/sessions/local:missing/rename", `{"name":"x"}`)
		call(http.MethodPost, "/api/sessions/local:ended/rename", `{`)
		call(http.MethodPost, "/api/project/delete", `{`)
		call(http.MethodPost, "/api/project/delete", `{}`)
		call(http.MethodGet, "/api/project/delete", "")
		projectBody := `{"key":"` + hubcore.ProjectSlug(workingDir) + `","working_dir":"` + workingDir + `"}`
		web.cfg.Roster = hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: "ended", Status: "active"})
		direct(web.handleAPIProjectDelete, http.MethodPost, "/api/project/delete", projectBody)
		web.cfg.Roster = hubcore.NewRosterWithEntries()
		direct(web.handleAPIProjectDelete, http.MethodPost, "/api/project/delete", projectBody)

		// Exercise the real process boundary for both a repository and an error.
		repo := filepath.Join(root, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "pass4@example.invalid"}, {"config", "user.name", "Pass Four"}} {
			if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "tracked"}, {"commit", "-q", "-m", "seed"}} {
			if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		_, _ = gitHeadBranch(context.Background(), repo)
		if out, err := exec.Command("git", "-C", repo, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
			t.Fatalf("detach: %v: %s", err, out)
		}
		_, _ = gitHeadBranch(context.Background(), repo)
		_, _ = gitHeadBranch(context.Background(), filepath.Join(root, "missing"))

		_ = warningPayload([]byte(`{"warning":{"message":"nested"},"source":"daemon","title":"Title","hint":"Hint"}`))
		_ = warningPayload([]byte(`{"message":"plain"}`))
		_ = warningMessage([]byte(`{"warning":"warning text"}`))
		_ = warningMessage([]byte(`not-json`))
		writeAPIWireError(httptest.NewRecorder(), http.StatusBadGateway, appwire.WireError{Code: appwire.CodeInvalidParams, Message: "bad", Data: appwire.ErrorData{SerfErrorInfo: "detail"}})
		writeAPIWireError(httptest.NewRecorder(), http.StatusBadGateway, appwire.WireError{Code: appwire.CodeInternalError, Message: "bad", Data: map[string]any{"serfErrorInfo": "detail"}})
		for _, code := range []int{appwire.CodeInvalidRequest, appwire.CodeMethodNotFound, appwire.CodeConflict, appwire.CodeUnavailable, -999} {
			_ = statusForWireError(appwire.WireError{Code: code}, http.StatusTeapot)
		}
		_ = poke
	})
}
