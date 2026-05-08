package main

import (
	"context"
	"encoding/json"
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
	body := rec.Body.String()
	if !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("body missing #sidebar: %q", body)
	}
	if !strings.Contains(body, `id="workspace"`) {
		t.Errorf("body missing #workspace: %q", body)
	}
}

func TestWeb_AppShell_RendersSidebarAndWorkspaceMounts(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: NewRoster(t.TempDir(), nil), Past: NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("missing #sidebar")
	}
	if !strings.Contains(body, `id="workspace"`) {
		t.Errorf("missing #workspace")
	}
	if !strings.Contains(body, `hx-get="/sidebar"`) {
		t.Errorf("missing sidebar hx-get")
	}
}

func TestWeb_Sidebar_RendersTreeWithLiveAndProjects(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST", UpdatedAt: time.Now(), OriginalTask: "fix bug",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
	}); err != nil {
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
	req := httptest.NewRequest(http.MethodGet, "/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fix bug") {
		t.Errorf("missing title")
	}
	if !strings.Contains(body, "session-row") {
		t.Errorf("missing session-row class")
	}
	if !strings.Contains(body, "/s/01PAST") {
		t.Errorf("missing session URL")
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

func TestWeb_WorkspaceSpawn_RendersForm(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/workspace/spawn", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="spawn-pane"`) {
		t.Errorf("body missing spawn-pane class: %q", body)
	}
	if !strings.Contains(body, `data-chip-value-model`) {
		t.Errorf("body missing data-chip-value-model: %q", body)
	}
	if !strings.Contains(body, `data-chip-value-working_dir`) {
		t.Errorf("body missing data-chip-value-working_dir: %q", body)
	}
	if !strings.Contains(body, `data-chip-value-branch`) {
		t.Errorf("body missing data-chip-value-branch: %q", body)
	}
	if !strings.Contains(body, `data-chip-value-access_mode`) {
		t.Errorf("body missing data-chip-value-access_mode: %q", body)
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

func (fakeSpawner) Spawn(_ context.Context, _ SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Resume(_ context.Context, sessionID string) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}

func TestWeb_ApiSpawn_RejectsRelativeWorkingDir(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := strings.NewReader(`{"working_dir":"relative/path"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400", rec.Code)
	}
}

func TestWeb_ApiSpawn_RejectsMissingWorkingDir(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := strings.NewReader(`{"working_dir":"/this/path/does/not/exist/1234567890"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestWeb_ApiSpawn_503WhenNoSpawner(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := strings.NewReader(`{"task":"do something"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
}

func TestWeb_PastResults_PartialOnlyNoBaseChrome(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01HTMX", UpdatedAt: time.Now(), OriginalTask: "the-thing",
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
	req := httptest.NewRequest(http.MethodGet, "/past/results?q=thing", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "01HTMX") {
		t.Errorf("body missing id 01HTMX")
	}
	if strings.Contains(body, "<html") {
		t.Errorf("partial response should not include base chrome: %q", body)
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

// TestWeb_SessionRoute_FullPage_ServesAppShell verifies that GET /s/<id> without
// HX-Request returns the app shell (not the workspace partial).
func TestWeb_SessionRoute_FullPage_ServesAppShell(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/s/anysession", nil)
	req.Host = "127.0.0.1:9180"
	// No HX-Request header — should serve full app shell.
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("full-page /s/<id> missing app shell sidebar")
	}
	if !strings.Contains(body, `id="workspace"`) {
		t.Errorf("full-page /s/<id> missing app shell workspace mount")
	}
}

// TestWeb_WorkspacePartial_LiveSession_RendersHeader verifies that GET /s/<id>
// with HX-Request:true returns the workspace partial with the session title and status.
func TestWeb_WorkspacePartial_LiveSession_RendersHeader(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 10, Address: "127.0.0.1:55556"})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVE001", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/s/01LIVE001", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "01LIVE001") {
		t.Errorf("body missing session id 01LIVE001: %q", body)
	}
	if !strings.Contains(body, "idle") {
		t.Errorf("body missing status 'idle': %q", body)
	}
	if !strings.Contains(body, "workspace-header") {
		t.Errorf("body missing workspace-header class: %q", body)
	}
}

// TestWeb_WorkspacePartial_PastSession_RendersTitleAndState verifies that a past session
// renders via the workspace partial with its OriginalTask and state="ended".
func TestWeb_WorkspacePartial_PastSession_RendersTitleAndState(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST001", UpdatedAt: time.Now(), OriginalTask: "fix the widget", TurnCount: 7,
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
	req := httptest.NewRequest(http.MethodGet, "/s/01PAST001", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fix the widget") {
		t.Errorf("body missing OriginalTask 'fix the widget': %q", body)
	}
	if !strings.Contains(body, "ended") {
		t.Errorf("body missing state 'ended': %q", body)
	}
}

// TestWeb_WorkspacePartial_RendersBottomStripAffordances verifies that the
// workspace partial includes the new bordered-card bottom strip elements
// (attach button, drop zone, mode chip, controls spacer, status row).
func TestWeb_WorkspacePartial_RendersBottomStripAffordances(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01BOTTOM01", UpdatedAt: time.Now(), OriginalTask: "render bottom strip",
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
	req := httptest.NewRequest(http.MethodGet, "/s/01BOTTOM01", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wants := []string{
		"data-attach-trigger",
		"data-drop-zone",
		"mode-chip",
		"controls-spacer",
		"input-status",
		"data-file-picker",
		`data-steer-trigger`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("workspace partial missing %q", w)
		}
	}
}

// TestWeb_WorkspacePartial_RendersWorkingDirInStatusRow verifies that a session
// with EnvInfo.WorkingDir populated renders the cwd in the status row.
func TestWeb_WorkspacePartial_RendersWorkingDirInStatusRow(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01CWD00001", UpdatedAt: time.Now(), OriginalTask: "cwd test",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/tmp/foo", GitBranch: "feature/bar"},
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
	req := httptest.NewRequest(http.MethodGet, "/s/01CWD00001", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/tmp/foo") {
		t.Errorf("status row missing WorkingDir '/tmp/foo': %q", body)
	}
	if !strings.Contains(body, "feature/bar") {
		t.Errorf("status row missing Branch 'feature/bar': %q", body)
	}
	if !strings.Contains(body, `class="cwd"`) {
		t.Errorf("status row missing cwd span: %q", body)
	}
}

// TestWeb_State_RendersInputStatusPartial verifies the polled /state endpoint
// returns the new input_status block content.
func TestWeb_State_RendersInputStatusPartial(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01STATE001", UpdatedAt: time.Now(),
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/tmp/wd", GitBranch: "main"},
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
	req := httptest.NewRequest(http.MethodGet, "/s/01STATE001/state", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/tmp/wd") {
		t.Errorf("state partial missing WorkingDir '/tmp/wd': %q", body)
	}
	if !strings.Contains(body, "main") {
		t.Errorf("state partial missing Branch 'main': %q", body)
	}
}

// TestWeb_Send_ClosedSessionRequiresSpawner verifies that POSTing to /s/<id>/send
// when the session is not live and no spawner is configured returns 503.
func TestWeb_Send_ClosedSessionRequiresSpawner(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := strings.NewReader(`{"text":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/s/NOSESSION/send", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503", rec.Code)
	}
}

// TestWeb_Fork_CallsForkSession verifies end-to-end fork: set up a parent transcript
// + meta, POST /s/<id>/fork, expect 200 + JSON child_session_id.
func TestWeb_Fork_CallsForkSession(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")

	// Build the parent session using the shared helper from agent fork tests.
	// We mirror the logic inline here since it's in a different package.
	parentID := "01PARENT00000000000000001"
	sessionsDir := filepath.Join(proj, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(sessionsDir, parentID+".transcript.jsonl")
	tw, err := agent.NewTranscriptWriter(tpath, agent.TranscriptHeader{
		SessionID: parentID, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("first task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnAssistant, llm.Assistant("first reply"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("second task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: parentID, UpdatedAt: time.Now(), OriginalTask: "test fork",
		ProfileID: "openai", Model: "gpt-5",
	}); err != nil {
		t.Fatal(err)
	}

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(WebConfig{
		HubAddr:  "127.0.0.1:9180",
		Roster:   NewRoster(t.TempDir(), nil),
		Past:     idx,
		StateDir: proj,
	})
	reqBody := strings.NewReader(`{"turn":3,"edited_message":"second task revised","label":"old branch"}`)
	req := httptest.NewRequest(http.MethodPost, "/s/"+parentID+"/fork", reqBody)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "child_session_id") {
		t.Errorf("response missing child_session_id: %q", respBody)
	}
}

// TestWeb_ApiSearch_FiltersPast populates the past index with two metas,
// queries for one by name, and asserts only that result is returned.
func TestWeb_ApiSearch_FiltersPast(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01MATCH", UpdatedAt: time.Now(), OriginalTask: "fix the frobnitz",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	})
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01OTHER", UpdatedAt: time.Now(), OriginalTask: "unrelated work",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/beta"},
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
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=frobnitz", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "01MATCH") {
		t.Errorf("body missing 01MATCH: %q", body)
	}
	if strings.Contains(body, "01OTHER") {
		t.Errorf("body incorrectly includes 01OTHER: %q", body)
	}
}

// TestWeb_Settings_Theme_Renders checks that GET /settings/theme returns 200
// with the theme radio inputs present.
func TestWeb_Settings_Theme_Renders(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/settings/theme", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="theme"`) {
		t.Errorf("body missing theme radio inputs: %q", body)
	}
	for _, val := range []string{"system", "dark", "light"} {
		if !strings.Contains(body, `value="`+val+`"`) {
			t.Errorf("body missing radio value %q: %q", val, body)
		}
	}
}

// TestWeb_ApiModels_ReturnsListWithProviderEnv verifies the endpoint
// shape — returns a JSON array of {provider, model, …} entries when
// run against a live provider API. Skips when no real API key is set.
func TestWeb_ApiModels_ReturnsListWithProviderEnv(t *testing.T) {
	// Force-clear cache to make the test run a fresh fetch.
	liveModelsCache.mu.Lock()
	liveModelsCache.expires = time.Time{}
	liveModelsCache.models = nil
	liveModelsCache.mu.Unlock()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set; live list models requires a real API key")
	}

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var models []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("expected at least one model from live provider API, got 0")
	}
	for _, key := range []string{"provider", "model"} {
		if _, ok := models[0][key]; !ok {
			t.Errorf("model missing field %q: %v", key, models[0])
		}
	}
}

// TestWeb_ApiModels_NoProvidersConfigured returns an empty list when no
// providers have keys in the environment.
func TestWeb_ApiModels_NoProvidersConfigured(t *testing.T) {
	liveModelsCache.mu.Lock()
	liveModelsCache.expires = time.Time{}
	liveModelsCache.models = nil
	liveModelsCache.mu.Unlock()
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "")

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "null" && body != "[]" {
		t.Errorf("expected empty list when no providers configured, got: %s", body)
	}
}

// TestWeb_ApiDirs_ReturnsMatchingDirs verifies that /api/dirs?prefix= returns a
// JSON object with a results array of directories.
func TestWeb_ApiDirs_ReturnsMatchingDirs(t *testing.T) {
	// Use os.TempDir() as a known directory with children.
	parent := t.TempDir()
	childA := filepath.Join(parent, "aardvark")
	childB := filepath.Join(parent, "zebra")
	for _, d := range []string{childA, childB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/dirs?prefix="+parent+"/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	rawResults, ok := resp["results"]
	if !ok {
		t.Fatal("response missing 'results' key")
	}
	results, ok := rawResults.([]any)
	if !ok {
		t.Fatalf("results is not an array: %T", rawResults)
	}
	if len(results) < 2 {
		t.Errorf("expected at least 2 results, got %d", len(results))
	}
	// Verify the results include "aardvark".
	found := false
	for _, r := range results {
		m, _ := r.(map[string]any)
		if m["name"] == "aardvark" {
			found = true
		}
	}
	if !found {
		t.Errorf("results missing 'aardvark' directory")
	}
}

// TestWeb_ApiDirs_FiltersByBasename verifies that /api/dirs?prefix=<dir>/prefix
// filters to only directories whose name starts with the given prefix.
func TestWeb_ApiDirs_FiltersByBasename(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"apple", "apricot", "banana"} {
		if err := os.MkdirAll(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	prefix := parent + "/ap"
	req := httptest.NewRequest(http.MethodGet, "/api/dirs?prefix="+prefix, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, r := range resp.Results {
		if !strings.HasPrefix(r.Name, "ap") {
			t.Errorf("result %q does not start with 'ap'", r.Name)
		}
	}
	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results (apple, apricot), got %d", len(resp.Results))
	}
}

// TestWeb_Settings_Providers_RendersConfigured checks that GET /settings/providers
// with Models in WebConfig renders the provider names.
func TestWeb_Settings_Providers_RendersConfigured(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Models: []modelDescriptor{
			{Provider: "anthropic", Model: "claude-sonnet-4-6"},
			{Provider: "anthropic", Model: "claude-opus-4-7"},
			{Provider: "openai", Model: "gpt-5"},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/settings/providers", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"anthropic", "openai", "claude-sonnet-4-6", "gpt-5"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
}

// TestWeb_SessionTasks_PastReturnsPersistedFile verifies that GET /s/<id>/tasks
// for an ended session reads <StateDir>/tasks/<id>.json and returns its contents.
func TestWeb_SessionTasks_PastReturnsPersistedFile(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PASTTASK", UpdatedAt: time.Now(), OriginalTask: "demo",
	}); err != nil {
		t.Fatal(err)
	}
	tasks := []agent.Task{
		{ID: 1, Type: agent.TaskTypeImplement, Description: "add foo", Status: agent.TaskDone},
		{ID: 2, Type: agent.TaskTypeVerify, Description: "test foo", Status: agent.TaskOpen},
	}
	tasksDir := filepath.Join(proj, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(tasks, "", "  ")
	if err := os.WriteFile(filepath.Join(tasksDir, "01PASTTASK.json"), data, 0o644); err != nil {
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

	req := httptest.NewRequest(http.MethodGet, "/s/01PASTTASK/tasks", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var got []agent.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v body=%q", err, rec.Body.String())
	}
	if len(got) != 2 || got[0].Description != "add foo" || got[1].Status != agent.TaskOpen {
		t.Errorf("unexpected tasks: %+v", got)
	}
}

// TestWeb_SessionTasks_PastNoTasksFile returns an empty array when no
// tasks have been persisted for the session.
func TestWeb_SessionTasks_PastNoTasksFile(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01NOTASKS", UpdatedAt: time.Now(),
	}); err != nil {
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
	req := httptest.NewRequest(http.MethodGet, "/s/01NOTASKS/tasks", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Errorf("expected empty array, got %q", body)
	}
}

// TestWeb_SessionTasks_LiveProxiesDaemon stands up a fake daemon serving /tasks
// and verifies the hub proxies through.
func TestWeb_SessionTasks_LiveProxiesDaemon(t *testing.T) {
	daemonResp := `[{"id":1,"type":"implement","description":"live task","status":"in_progress"}]`
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(daemonResp))
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 11, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVETASK", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/s/01LIVETASK/tasks", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"live task"`) {
		t.Errorf("body missing daemon payload: %q", rec.Body.String())
	}
}
