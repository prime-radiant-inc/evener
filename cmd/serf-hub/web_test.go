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

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

func TestWeb_Landing_Renders(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	idx := NewPastIndex("")
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "live sessions") {
		t.Errorf("body missing 'live sessions': %q", rec.Body.String())
	}
}

func TestWeb_Assets_ServeHtmx(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/assets/htmx.min.js", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.Len() < 1000 {
		t.Errorf("htmx.min.js too small: %d bytes", rec.Body.Len())
	}
}

func TestWeb_LiveRoster_Partial(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no live daemons") {
		t.Errorf("expected empty roster message")
	}
}

func TestWeb_PastSearch(t *testing.T) {
	root := t.TempDir()
	proj := root + "/projects/x"
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01A", UpdatedAt: time.Now(), OriginalTask: "fix the bug",
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(root + "/projects/*")
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodGet, "/past?q=bug", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "01A") {
		t.Errorf("expected to find 01A in body, got: %q", rec.Body.String())
	}
}

func TestWeb_DrivePage_KnownSession(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1, Address: "127.0.0.1:55555"})
	r := NewRoster(dir, fakeProber{sessionID: "01SESS001"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "transcript") {
		t.Errorf("body missing transcript: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "01SESS001") {
		t.Errorf("body missing session id")
	}
}

func TestWeb_DrivePage_UnknownSession_404(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live/bogus", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d, want 404", rec.Code)
	}
}

func TestWeb_Assets_ServeRenderer(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/assets/renderer.js", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SerfRenderer") {
		t.Errorf("renderer.js does not export SerfRenderer")
	}
}

func TestWeb_LiveNew_GET_RendersForm(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &HubSpawner{Cfg: Config{
			SpawnTemplates: []SpawnTemplate{
				{Name: "code, gpt", Provider: "openai", Model: "gpt-5.2"},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/live/new", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "code, gpt") {
		t.Errorf("body missing template name: %q", rec.Body.String())
	}
}

func TestWeb_LiveNew_POST_503WhenNoSpawner(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodPost, "/live/new", strings.NewReader(""))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
}

func TestWeb_PastSearch_RendersResults(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST", UpdatedAt: time.Now(), OriginalTask: "search-target",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/tmp/wd"},
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodGet, "/past?q=search-target", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "01PAST") {
		t.Errorf("body missing id: %q", rec.Body.String())
	}
}

func TestWeb_PastView_404Unknown(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/past/nope", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d, want 404", rec.Code)
	}
}

func TestWeb_PastReplay_TranslatesTurnsToSSEEvents(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01REPLAY", UpdatedAt: time.Now(), OriginalTask: "demo",
	}); err != nil {
		t.Fatal(err)
	}

	// Build a transcript using the real writer so the format stays in sync.
	tpath := filepath.Join(proj, "sessions", "01REPLAY.transcript.jsonl")
	tw, err := agent.NewTranscriptWriter(tpath, agent.TranscriptHeader{
		SessionID: "01REPLAY",
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnAssistant, llm.Assistant("hi there"))); err != nil {
		t.Fatal(err)
	}
	toolCallMsg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID: "call_42", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`),
		},
	}}}
	if err := tw.Append(agent.NewTurn(agent.TurnAssistant, toolCallMsg)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnToolResults,
		llm.ToolResultNamed("call_42", "shell", "file1\nfile2", false))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    idx,
	})

	req := httptest.NewRequest(http.MethodGet, "/past/01REPLAY/replay", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: SESSION_START",
		"event: USER_INPUT",
		`"text":"hello"`,
		"event: ASSISTANT_TEXT_START",
		"event: ASSISTANT_TEXT_END",
		`"text":"hi there"`,
		"event: TOOL_CALL_START",
		`"tool_name":"shell"`,
		`"call_id":"call_42"`,
		"event: TOOL_CALL_END",
		"file1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("replay body missing %q\nbody:\n%s", want, body)
		}
	}
}

type fakeSpawner struct{}

func (fakeSpawner) Spawn(_ context.Context, _, _ string) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Resume(_ context.Context, sessionID string) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Templates() []SpawnTemplate { return nil }

func TestWeb_LiveNew_POST_RejectsRelativeWorkingDir(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := strings.NewReader("template=t&working_dir=relative/path")
	req := httptest.NewRequest(http.MethodPost, "/live/new", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400", rec.Code)
	}
}

func TestWeb_LiveNew_POST_RejectsMissingWorkingDir(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := strings.NewReader("template=t&working_dir=/this/path/does/not/exist/1234567890")
	req := httptest.NewRequest(http.MethodPost, "/live/new", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestWeb_DrivePage_ShowsForkedFrom(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1, Address: "127.0.0.1:55555"})
	r := NewRoster(dir, fakeProber{sessionID: "01NEW"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live/01NEW?from=01OLD", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "01OLD") {
		t.Errorf("body missing forked-from id 01OLD")
	}
	if !strings.Contains(strings.ToLower(body), "forked") {
		t.Errorf("body missing 'forked' annotation")
	}
}

func TestWeb_PastResume_503WhenNoSpawner(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{ID: "01ID", UpdatedAt: time.Now()})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = idx.Rebuild()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodPost, "/past/01ID/resume", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
}
