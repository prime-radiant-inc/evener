package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/hubapi"
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

func TestWeb_WorkspaceSpawn_DoesNotSubmitPlaceholderDefaults(t *testing.T) {
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
	for _, want := range []string{
		`name="model" value=""`,
		`name="working_dir" value=""`,
		`name="branch" value=""`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("spawn form missing %q:\n%s", want, body)
		}
	}
}

func TestWeb_WorkspaceSpawn_SubmitsPrefilledWorkingDir(t *testing.T) {
	dir := t.TempDir()
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/workspace/spawn?dir="+url.QueryEscape(dir), nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	resolved, err := canonicalizeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := `name="working_dir" value="` + resolved + `"`
	if !strings.Contains(body, want) {
		t.Fatalf("spawn form missing %q:\n%s", want, body)
	}
}

// TestWeb_PastReplay_ImagesAreShaReferenced verifies that USER_INPUT turns
// containing image bytes get their bytes stripped from the SSE replay; the
// renderer fetches lazily via /s/<id>/images/<sha>. Without this, multi-MB
// transcripts would re-emit every byte on every reload.
func TestWeb_PastReplay_ImagesAreShaReferenced(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01IMG", UpdatedAt: time.Now(), OriginalTask: "image demo",
	}); err != nil {
		t.Fatal(err)
	}

	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y', 'l', 'o', 'a', 'd'}
	wantSha := imageSha(imgBytes)

	tpath := filepath.Join(proj, "sessions", "01IMG.transcript.jsonl")
	tw, err := agent.NewTranscriptWriter(tpath, agent.TranscriptHeader{
		SessionID: "01IMG", ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	userMsg := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "what color?"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: imgBytes, MediaType: "image/png"}},
	}}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, userMsg)); err != nil {
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

	// Replay should emit sha, not data, in the USER_INPUT event.
	req := httptest.NewRequest(http.MethodGet, "/past/01IMG/replay", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status=%d, body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"sha":"`+wantSha+`"`) {
		t.Errorf("replay missing sha reference; body=%q", body)
	}
	// USER_INPUT replay payload must not carry the raw bytes (base64-encoded
	// or otherwise). Check for the field name itself.
	if strings.Contains(body, `"data":`) {
		t.Errorf("replay still includes raw image data field; body excerpt=%q", body)
	}

	// Image fetch should return the original bytes.
	imgReq := httptest.NewRequest(http.MethodGet, "/s/01IMG/images/"+wantSha, nil)
	imgReq.Host = "127.0.0.1:9180"
	imgRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(imgRec, imgReq)
	if imgRec.Code != http.StatusOK {
		t.Fatalf("image status=%d, body=%q", imgRec.Code, imgRec.Body.String())
	}
	if !bytes.Equal(imgRec.Body.Bytes(), imgBytes) {
		t.Errorf("image bytes mismatch: got %d bytes, want %d", imgRec.Body.Len(), len(imgBytes))
	}
	if ct := imgRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type=%q, want image/png", ct)
	}
}

// TestWeb_SessionImage_BadSha verifies that non-hex sha paths get 400.
func TestWeb_SessionImage_BadSha(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/s/01ABC/images/not-hex", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 (body=%q)", rec.Code, rec.Body.String())
	}
}

// TestWeb_SessionImage_UnknownSha verifies 404 when the sha isn't in any
// USER_INPUT turn for the session.
func TestWeb_SessionImage_UnknownSha(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "y")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01NOIMG", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(proj, "sessions", "01NOIMG.transcript.jsonl")
	tw, err := agent.NewTranscriptWriter(tpath, agent.TranscriptHeader{
		SessionID: "01NOIMG", ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("text only"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: NewRoster(t.TempDir(), nil), Past: idx})

	allZeros := strings.Repeat("0", 64)
	req := httptest.NewRequest(http.MethodGet, "/s/01NOIMG/images/"+allZeros, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 (body=%q)", rec.Code, rec.Body.String())
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

func TestHubReplayUserInputIncludesTurnIndex(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01REPLAYTURN", UpdatedAt: time.Now(), OriginalTask: "demo",
	}); err != nil {
		t.Fatal(err)
	}

	tpath := filepath.Join(proj, "sessions", "01REPLAYTURN.transcript.jsonl")
	tw, err := agent.NewTranscriptWriter(tpath, agent.TranscriptHeader{
		SessionID: "01REPLAYTURN",
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("first"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnAssistant, llm.Assistant("reply"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(agent.NewTurn(agent.TurnUserInput, llm.User("second"))); err != nil {
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

	req := httptest.NewRequest(http.MethodGet, "/past/01REPLAYTURN/replay", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"text":"first","turn":1`, `"text":"second","turn":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("replay body missing %q\nbody:\n%s", want, body)
		}
	}
}

type fakeSpawner struct{}

func (fakeSpawner) Spawn(_ context.Context, _ SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Resume(_ context.Context, _ ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}

type delayedRosterSpawner struct {
	runDir string
	delay  time.Duration
	entry  rendezvous.Entry
	got    SpawnRequest
}

func (s *delayedRosterSpawner) Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error) {
	s.got = req
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return rendezvous.Entry{}, ctx.Err()
	case <-timer.C:
	}
	entry := s.entry
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now().UTC()
	}
	if _, err := rendezvous.Write(s.runDir, entry); err != nil {
		return rendezvous.Entry{}, err
	}
	return entry, nil
}

func (s *delayedRosterSpawner) Resume(_ context.Context, _ ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, nil
}

func TestWeb_ApiSpawn_WaitsForSlowSpawnerAndReturnsSession(t *testing.T) {
	runDir := t.TempDir()
	workDir := t.TempDir()
	spawner := &delayedRosterSpawner{
		runDir: runDir,
		delay:  1500 * time.Millisecond,
		entry: rendezvous.Entry{
			PID:        54,
			Address:    "127.0.0.1:4054",
			WorkingDir: workDir,
			Model:      "gpt-5",
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(WebConfig{
		HubAddr: srv.Listener.Addr().String(),
		Roster:  NewRoster(runDir, fakeProber{sessionID: "01SLOWSPAWN", status: "idle"}),
		Past:    NewPastIndex(""),
		Spawner: spawner,
	})
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client, err := hubapi.NewClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Spawn(context.Background(), hubapi.SpawnRequest{
		Model:      "openai/gpt-5",
		WorkingDir: workDir,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if resp.Ref != "local:01SLOWSPAWN" || resp.SessionID != "01SLOWSPAWN" {
		t.Fatalf("spawn response=%+v", resp)
	}
	if spawner.got.Provider != "openai" || spawner.got.Model != "gpt-5" {
		t.Fatalf("spawn model provider=%q model=%q", spawner.got.Provider, spawner.got.Model)
	}
	wantWorkingDir, err := canonicalizeDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if spawner.got.WorkingDir != wantWorkingDir {
		t.Fatalf("working_dir=%q, want %q", spawner.got.WorkingDir, wantWorkingDir)
	}
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

// stubDaemonForSend stands up an httptest server that records the body posted
// to /input and replies 202. Returns the server (caller must Close), a pointer
// to the captured body bytes, and the host:port string.
func stubDaemonForSend(t *testing.T) (*httptest.Server, *[]byte, string) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/input" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured = buf
		w.WriteHeader(http.StatusAccepted)
	}))
	return srv, &captured, strings.TrimPrefix(srv.URL, "http://")
}

// TestWeb_Send_ForwardsTextAndImages verifies POST /s/<id>/send with both text
// and an image attachment forwards a JSON body matching server.InputRequest's
// schema to the daemon's /input.
func TestWeb_Send_ForwardsTextAndImages(t *testing.T) {
	daemon, captured, addr := stubDaemonForSend(t)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 21, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01SENDIMG", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header
	reqBody, err := json.Marshal(map[string]any{
		"text": "caption",
		"images": []map[string]any{{
			"media_type": "image/png",
			"data":       imgBytes,
			"name":       "x.png",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01SENDIMG/send", strings.NewReader(string(reqBody)))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	if len(*captured) == 0 {
		t.Fatal("daemon did not receive a request body")
	}
	var got sendRequest
	if err := json.Unmarshal(*captured, &got); err != nil {
		t.Fatalf("decode forwarded body: %v (raw=%q)", err, *captured)
	}
	if got.Text != "caption" {
		t.Errorf("forwarded text=%q, want %q", got.Text, "caption")
	}
	if len(got.Images) != 1 {
		t.Fatalf("forwarded images=%d, want 1", len(got.Images))
	}
	if got.Images[0].MediaType != "image/png" {
		t.Errorf("media_type=%q, want image/png", got.Images[0].MediaType)
	}
	if got.Images[0].Name != "x.png" {
		t.Errorf("name=%q, want x.png", got.Images[0].Name)
	}
	if len(got.Images[0].Data) != len(imgBytes) {
		t.Errorf("forwarded image data len=%d, want %d", len(got.Images[0].Data), len(imgBytes))
	}
}

// TestWeb_Send_ImageOnly_Forwards verifies that a send with empty text and one
// image is accepted and forwarded.
func TestWeb_Send_ImageOnly_Forwards(t *testing.T) {
	daemon, captured, addr := stubDaemonForSend(t)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 22, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01IMGONLY", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	imgBytes := []byte{0xff, 0xd8, 0xff, 0xe0} // JPEG header bytes
	reqBody, err := json.Marshal(map[string]any{
		"text": "",
		"images": []map[string]any{{
			"media_type": "image/jpeg",
			"data":       imgBytes,
			"name":       "y.jpg",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01IMGONLY/send", strings.NewReader(string(reqBody)))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var got sendRequest
	if err := json.Unmarshal(*captured, &got); err != nil {
		t.Fatalf("decode forwarded body: %v (raw=%q)", err, *captured)
	}
	if got.Text != "" {
		t.Errorf("forwarded text=%q, want empty", got.Text)
	}
	if len(got.Images) != 1 {
		t.Fatalf("forwarded images=%d, want 1", len(got.Images))
	}
	if got.Images[0].MediaType != "image/jpeg" {
		t.Errorf("media_type=%q, want image/jpeg", got.Images[0].MediaType)
	}
	if len(got.Images[0].Data) != len(imgBytes) {
		t.Errorf("forwarded image data len=%d, want %d", len(got.Images[0].Data), len(imgBytes))
	}
}

// TestWeb_Send_RejectsEmptyTextAndNoImages verifies that the hub returns 400
// when neither text nor images are supplied — matching the daemon's rule.
func TestWeb_Send_RejectsEmptyTextAndNoImages(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 23, Address: "127.0.0.1:55557"})
	r := NewRoster(dir, fakeProber{sessionID: "01NOEMPTY", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	cases := []string{`{}`, `{"text":""}`, `{"text":"","images":[]}`}
	for _, payload := range cases {
		req := httptest.NewRequest(http.MethodPost, "/s/01NOEMPTY/send", strings.NewReader(payload))
		req.Host = "127.0.0.1:9180"
		req.Header.Set("Origin", "http://127.0.0.1:9180")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload=%s: status=%d, want 400 (body=%q)", payload, rec.Code, rec.Body.String())
		}
	}
}

// TestWeb_Send_RejectsOversizeImage verifies that the hub-side accept cap
// rejects images larger than sendMaxImageBytes with 413, before forwarding
// to the daemon.
func TestWeb_Send_RejectsOversizeImage(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 99, Address: "127.0.0.1:1"})
	r := NewRoster(dir, fakeProber{sessionID: "01TOOBIG", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	// One byte over the per-image cap.
	bigData := make([]byte, sendMaxImageBytes+1)
	body := sendRequest{
		Text: "look",
		Images: []agent.ImageAttachment{{
			MediaType: "image/png", Data: bigData, Name: "big.png",
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/s/01TOOBIG/send", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	// Either a 413 (body parse succeeded, individual image too big) or a 400
	// (MaxBytesReader tripped first because the total request exceeded the
	// outer cap) is acceptable: both reject the upload before forwarding.
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 413 or 400 (body=%q)", rec.Code, rec.Body.String())
	}
}

// TestWeb_Sidebar_LiveRowDataState verifies that a live entry in the sidebar
// renders with data-state on the .live-row anchor itself, so the CSS state
// accents (left border + tinted background) can apply.
func TestWeb_Sidebar_LiveRowDataState(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 30, Address: "127.0.0.1:55570"})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVEACC", status: "AWAITING_REPLY"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// The live-row anchor must carry data-state="awaiting" so the state-accent
	// CSS rules match. The template line-wraps the anchor, so flatten whitespace
	// before looking for the two attributes adjacent on the same element.
	if !strings.Contains(body, "live-row") {
		t.Fatalf("body missing live-row class: %q", body)
	}
	flat := strings.Join(strings.Fields(body), " ")
	if !strings.Contains(flat, `live-row`) || !strings.Contains(flat, `data-state="awaiting"`) {
		t.Errorf("live-row missing data-state=\"awaiting\": %q", body)
	}
	// And confirm they're on the same opening tag: find <a ... > containing both.
	tagFound := false
	for _, chunk := range strings.Split(flat, "<a ") {
		// The first split chunk is everything before the first <a; subsequent
		// chunks each begin with the anchor's attribute list.
		if !strings.HasPrefix(chunk, "class=\"live-row") {
			continue
		}
		end := strings.Index(chunk, ">")
		if end < 0 {
			continue
		}
		if strings.Contains(chunk[:end], `data-state="awaiting"`) {
			tagFound = true
			break
		}
	}
	if !tagFound {
		t.Errorf("data-state=\"awaiting\" not on the live-row <a> element: %q", body)
	}
}

// TestWeb_Sidebar_ProjectHeader_HasChevronAndFolder verifies that the project
// header renders with the new ▾ 📁 <name> shape so the spec mockups match.
func TestWeb_Sidebar_ProjectHeader_HasChevronAndFolder(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PROJHDR", UpdatedAt: time.Now(), OriginalTask: "x",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/widgets"},
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
	wants := []string{`class="project-header"`, "project-chevron", "📁", "project-name"}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("project-header missing %q: %q", w, body)
		}
	}
}

// TestWeb_Workspace_ForkOriginalBanner verifies that a session whose meta
// carries ForkLabel renders the "↳ original of <new-branch-title>, divergence
// at turn N" banner above the workspace title.
func TestWeb_Workspace_ForkOriginalBanner(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Original (preserved) branch — carries ForkLabel.
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01ORIGINAL", UpdatedAt: time.Now().Add(-time.Hour),
		OriginalTask:   "the original task",
		ForkLabel:      "before TDD",
		DivergenceTurn: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// New branch — its ParentSessionID points back at the original.
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01NEWBRANCH", UpdatedAt: time.Now(),
		OriginalTask:    "the new branch title",
		ParentSessionID: "01ORIGINAL",
		DivergenceTurn:  5,
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
	req := httptest.NewRequest(http.MethodGet, "/s/01ORIGINAL", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wants := []string{
		"fork-original-banner",
		"↳ original of",
		"the new branch title",
		"divergence at turn 5",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("fork banner missing %q: %q", w, body)
		}
	}
}

// stubDaemonForAction stands up an httptest server that records the path of
// any POST and replies with the given status code. Returns the server, a
// pointer to the captured path, and the host:port string.
func stubDaemonForAction(t *testing.T, replyStatus int) (*httptest.Server, *string, string) {
	t.Helper()
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		capturedPath = r.URL.Path
		w.WriteHeader(replyStatus)
	}))
	return srv, &capturedPath, strings.TrimPrefix(srv.URL, "http://")
}

func TestWeb_SessionAction_InterruptForwards(t *testing.T) {
	daemon, capturedPath, addr := stubDaemonForAction(t, http.StatusNoContent)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 30, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01ACTINT", status: "processing"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTINT/interrupt", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if *capturedPath != "/interrupt" {
		t.Errorf("daemon path=%q, want /interrupt", *capturedPath)
	}
}

func TestWeb_SessionAction_CompactForwards(t *testing.T) {
	daemon, capturedPath, addr := stubDaemonForAction(t, http.StatusAccepted)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 31, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01ACTCMP", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTCMP/compact", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 (body=%q)", rec.Code, rec.Body.String())
	}
	if *capturedPath != "/compact" {
		t.Errorf("daemon path=%q, want /compact", *capturedPath)
	}
}

func TestWeb_SessionAction_ShutdownForwards(t *testing.T) {
	daemon, capturedPath, addr := stubDaemonForAction(t, http.StatusAccepted)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 32, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01ACTSHD", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTSHD/shutdown", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 (body=%q)", rec.Code, rec.Body.String())
	}
	if *capturedPath != "/shutdown" {
		t.Errorf("daemon path=%q, want /shutdown", *capturedPath)
	}
}

func TestWeb_SessionAction_ClearForwards(t *testing.T) {
	daemon, capturedPath, addr := stubDaemonForAction(t, http.StatusNoContent)
	defer daemon.Close()

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 33, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01ACTCLR", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTCLR/clear", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if *capturedPath != "/clear" {
		t.Errorf("daemon path=%q, want /clear", *capturedPath)
	}
}

// TestWeb_SessionAction_NotLive_404 verifies that posting to an action route
// for a session with no roster entry returns 404 rather than auto-resuming
// or otherwise side-effecting.
func TestWeb_SessionAction_NotLive_404(t *testing.T) {
	dir := t.TempDir()
	r := NewRoster(dir, fakeProber{})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	for _, action := range []string{"interrupt", "compact", "shutdown", "clear"} {
		req := httptest.NewRequest(http.MethodPost, "/s/01NOLIVE/"+action, nil)
		req.Host = "127.0.0.1:9180"
		req.Header.Set("Origin", "http://127.0.0.1:9180")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status=%d, want 404 (body=%q)", action, rec.Code, rec.Body.String())
		}
	}
}

// TestWeb_Steer_ForwardsBodyToDaemon verifies that POST /s/<id>/steer with a
// JSON body forwards both path and body to the daemon's /steer endpoint.
func TestWeb_Steer_ForwardsBodyToDaemon(t *testing.T) {
	var capturedPath string
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		buf, _ := io.ReadAll(r.Body)
		capturedBody = string(buf)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 33, Address: addr})
	r := NewRoster(dir, fakeProber{sessionID: "01STEER", status: "processing"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01STEER/steer", strings.NewReader(`{"text":"stop using mocks"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if capturedPath != "/steer" {
		t.Errorf("daemon path=%q, want /steer", capturedPath)
	}
	if !strings.Contains(capturedBody, "stop using mocks") {
		t.Errorf("daemon body=%q, want to contain 'stop using mocks'", capturedBody)
	}
}

// TestWeb_Steer_RejectsEmptyText verifies that empty text returns 400
// without forwarding to the daemon.
func TestWeb_Steer_RejectsEmptyText(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 34, Address: "127.0.0.1:1"})
	r := NewRoster(dir, fakeProber{sessionID: "01STEEREMPTY", status: "processing"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/s/01STEEREMPTY/steer", strings.NewReader(`{"text":"   "}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (body=%q)", rec.Code, rec.Body.String())
	}
}

// TestWeb_Steer_NotLive_404 verifies that steering an ended session returns
// 404 (no auto-resume — steering an ended model isn't meaningful).
func TestWeb_Steer_NotLive_404(t *testing.T) {
	dir := t.TempDir()
	r := NewRoster(dir, fakeProber{})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01STEEROFF/steer", strings.NewReader(`{"text":"hello"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 (body=%q)", rec.Code, rec.Body.String())
	}
}

// perAddrProber returns a different (sessionID, status) per address. Used
// to stage a project with multiple live children in distinct states.
type perAddrProber struct {
	byAddr map[string]struct{ SessionID, Status string }
}

func (p perAddrProber) Probe(addr string) (sessionID, status string, ok bool) {
	v, present := p.byAddr[addr]
	if !present {
		return "", "", false
	}
	return v.SessionID, v.Status, true
}

// TestWeb_Sidebar_RollupState_AwaitingHasPriority confirms that when a
// project has both an awaiting and an idle live child, the rollup dot
// reflects "awaiting" — the most-attention-needing state per spec.
func TestWeb_Sidebar_RollupState_AwaitingHasPriority(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01AWAIT", UpdatedAt: time.Now(), OriginalTask: "needs reply",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01IDLE", UpdatedAt: time.Now(), OriginalTask: "ticking over",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	rDir := t.TempDir()
	if _, err := rendezvous.Write(rDir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:1001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rendezvous.Write(rDir, rendezvous.Entry{PID: 1002, Address: "127.0.0.1:1002"}); err != nil {
		t.Fatal(err)
	}
	prober := perAddrProber{byAddr: map[string]struct{ SessionID, Status string }{
		"127.0.0.1:1001": {SessionID: "01AWAIT", Status: "AWAITING_REPLY"},
		"127.0.0.1:1002": {SessionID: "01IDLE", Status: "IDLE"},
	}}
	r := NewRoster(rDir, prober)
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	req := httptest.NewRequest(http.MethodGet, "/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="project-rollup-dot" data-state="awaiting"`) {
		t.Errorf("rollup dot should have data-state=\"awaiting\"; body=\n%s", body)
	}
}

// TestWeb_Sidebar_RollupState_NoLiveChildrenHides confirms that a
// past-only project renders the dot with an empty data-state, which the
// CSS rule hides.
func TestWeb_Sidebar_RollupState_NoLiveChildrenHides(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST", UpdatedAt: time.Now(), OriginalTask: "done long ago",
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
	if !strings.Contains(body, `class="project-rollup-dot" data-state=""`) {
		t.Errorf("past-only project rollup dot should have empty data-state; body=\n%s", body)
	}
}

// settingsRequest is a small helper for the settings pane tests.
func settingsRequest(t *testing.T, web *WebServer, section string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/settings/"+section, nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// writeFakePlugin creates a minimal plugin tree at <root>/<name>:
//
//	.claude-plugin/plugin.json with the given name+version
//	skills/<skillName>/SKILL.md with name+description frontmatter (when set)
func writeFakePlugin(t *testing.T, root, name, version, skillName, skillDesc string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"` + name + `","version":"` + version + `"}`)
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if skillName != "" {
		skillDir := filepath.Join(dir, "skills", skillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + skillName + "\ndescription: " + skillDesc + "\n---\n\nbody\n"
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestWeb_Settings_PluginsPane_RendersDiscoveredPlugins seeds a fake
// plugin dir and asserts the rendered HTML contains its name and path.
func TestWeb_Settings_PluginsPane_RendersDiscoveredPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := writeFakePlugin(t, root, "demo-plugin", "1.2.3", "", "")
	web := NewWebServer(WebConfig{
		HubAddr:    "127.0.0.1:9180",
		Roster:     NewRoster(t.TempDir(), nil),
		Past:       NewPastIndex(""),
		PluginDirs: []string{pluginDir},
	})
	body := settingsRequest(t, web, "plugins")
	for _, want := range []string{"demo-plugin", "1.2.3", pluginDir, "open in editor"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
}

// TestWeb_Settings_PluginsPane_EmptyState renders cleanly when no
// PluginDirs are configured and the default root has no plugins.
func TestWeb_Settings_PluginsPane_EmptyState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty XDG → no plugins
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := settingsRequest(t, web, "plugins")
	if !strings.Contains(body, "No plugins found") {
		t.Errorf("expected empty-state copy in body: %q", body)
	}
}

// TestWeb_Settings_SkillsPane_RendersDiscoveredSkills seeds a plugin
// containing one skill and asserts the rendered HTML contains the skill's
// name and description.
func TestWeb_Settings_SkillsPane_RendersDiscoveredSkills(t *testing.T) {
	root := t.TempDir()
	pluginDir := writeFakePlugin(t, root, "demo-plugin", "0.1.0", "frobnicate", "Use this when you need to frobnicate widgets.")
	web := NewWebServer(WebConfig{
		HubAddr:    "127.0.0.1:9180",
		Roster:     NewRoster(t.TempDir(), nil),
		Past:       NewPastIndex(""),
		PluginDirs: []string{pluginDir},
	})
	body := settingsRequest(t, web, "skills")
	for _, want := range []string{"frobnicate", "demo-plugin", "frobnicate widgets"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
}

// TestWeb_Settings_SkillsPane_EmptyState renders cleanly when nothing
// has been discovered.
func TestWeb_Settings_SkillsPane_EmptyState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := settingsRequest(t, web, "skills")
	if !strings.Contains(body, "No skills discovered") {
		t.Errorf("expected empty-state copy in body: %q", body)
	}
}

// TestWeb_Settings_McpPane_RendersConfiguredServers writes a small mcp.json
// and asserts the server name + command appear in the rendered HTML.
func TestWeb_Settings_McpPane_RendersConfiguredServers(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")
	cfg := `{"mcpServers":{"linear":{"command":"npx","args":["-y","@linear/mcp"]}}}`
	if err := os.WriteFile(mcpPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Roster:        NewRoster(t.TempDir(), nil),
		Past:          NewPastIndex(""),
		MCPConfigPath: mcpPath,
	})
	body := settingsRequest(t, web, "mcp")
	// "available" because npx is normally on PATH; if not, "missing" is fine.
	// Either way, the status pill must NOT say "unknown" — that was the old
	// placeholder before #22.
	for _, want := range []string{"linear", "npx", "open in editor"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
	if !strings.Contains(body, "status-available") && !strings.Contains(body, "status-missing") {
		t.Errorf("expected status-available or status-missing pill, got: %q", body)
	}
	if strings.Contains(body, "status-unknown") {
		t.Errorf("status-unknown pill should not appear for stdio configs: %q", body)
	}
}

// TestWeb_Settings_McpPane_EmptyState renders cleanly when the config
// file is missing.
func TestWeb_Settings_McpPane_EmptyState(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Roster:        NewRoster(t.TempDir(), nil),
		Past:          NewPastIndex(""),
		MCPConfigPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	body := settingsRequest(t, web, "mcp")
	if !strings.Contains(body, "No MCP servers configured") {
		t.Errorf("expected empty-state copy in body: %q", body)
	}
}

func TestWeb_APIHealth(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  "/tmp/serf-run",
		Past:    NewPastIndex("/tmp/state/projects/*"),
		Spawner: &fakeSpawner{},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version == "" || got.HubAddr != "127.0.0.1:9180" {
		t.Fatalf("unexpected health: %+v", got)
	}
	if !got.Capabilities.Tree || !got.Capabilities.SpawnSchema || !got.Capabilities.TranscriptFollow {
		t.Fatalf("missing capabilities: %+v", got.Capabilities)
	}
}

func TestWeb_APITreeReturnsRefsAndNormalizesAwaitingInput(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01TREE", UpdatedAt: time.Now(), OriginalTask: "tree task",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 44, Address: "127.0.0.1:4444", WorkingDir: "/projects/serf", Model: "gpt-5",
	})
	r := NewRoster(runDir, fakeProber{sessionID: "01TREE", status: "AWAITING_INPUT"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Live) != 1 {
		t.Fatalf("live=%d: %+v", len(got.Live), got.Live)
	}
	if got.Live[0].Ref != "local:01TREE" || got.Live[0].RowID != "live:local:01TREE" || got.Live[0].State != "awaiting" {
		t.Fatalf("unexpected live node: %+v", got.Live[0])
	}
	if len(got.Projects) != 1 || len(got.Projects[0].Sessions) != 1 {
		t.Fatalf("projects=%+v", got.Projects)
	}
	if got.Projects[0].Sessions[0].Ref != "local:01TREE" {
		t.Fatalf("project node missing ref: %+v", got.Projects[0].Sessions[0])
	}
}

func TestWeb_APITreeGroupsLiveOnlySessionsByProject(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 50, Address: "127.0.0.1:4050", WorkingDir: "/projects/serf", Model: "gpt-5"})
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 51, Address: "127.0.0.1:4051", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, perAddrProber{byAddr: map[string]struct{ SessionID, Status string }{
		"127.0.0.1:4050": {SessionID: "01LIVEA", Status: "IDLE"},
		"127.0.0.1:4051": {SessionID: "01LIVEB", Status: "AWAITING_INPUT"},
	}})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var serfProjects []hubapi.TreeProject
	for _, p := range got.Projects {
		if p.Name == "serf" {
			serfProjects = append(serfProjects, p)
		}
	}
	if len(serfProjects) != 1 {
		t.Fatalf("serf projects=%d: %+v", len(serfProjects), got.Projects)
	}
	if len(serfProjects[0].Sessions) != 2 || serfProjects[0].RollupState != "awaiting" {
		t.Fatalf("unexpected serf project: %+v", serfProjects[0])
	}
	if serfProjects[0].WorkingDir != "/projects/serf" {
		t.Fatalf("working_dir=%q, want /projects/serf", serfProjects[0].WorkingDir)
	}
}

func TestWeb_APITreeSkipsLiveEntriesUntilSessionIDKnown(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 52, Address: "127.0.0.1:4052", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, fakeProber{sessionID: "", status: "IDLE"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Live) != 0 || len(got.Projects) != 0 {
		t.Fatalf("tree rendered undrillable live entry: %+v", got)
	}
}

func TestWeb_SidebarSkipsLiveEntriesUntilSessionIDKnown(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 53, Address: "127.0.0.1:4053", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, fakeProber{sessionID: "", status: "IDLE"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodGet, "/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/s/`) {
		t.Fatalf("sidebar rendered undrillable session link:\n%s", rec.Body.String())
	}
}

func TestWeb_APISessionDetailsLiveAndPast(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01DETAIL", UpdatedAt: time.Now(), OriginalTask: "details task", Model: "gpt-5", ProfileID: "openai", TurnCount: 3,
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf", GitBranch: "serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 45, Address: "127.0.0.1:4545", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, fakeProber{sessionID: "01DETAIL", status: "IDLE"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:01DETAIL", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.SessionDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ref != "local:01DETAIL" || !got.Live || got.Title != "details task" || got.WorkingDir != "/projects/serf" {
		t.Fatalf("unexpected detail: %+v", got)
	}
	if !got.Capabilities.Send || !got.Capabilities.Interrupt || !got.Capabilities.Resume {
		t.Fatalf("missing live capabilities: %+v", got.Capabilities)
	}
}

func TestWeb_APISpawnSchema(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/spawn-schema", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.SpawnSchema
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := map[string]bool{}
	for _, f := range got.Fields {
		names[f.Name] = true
	}
	for _, want := range []string{"task", "working_dir", "model", "agent", "reasoning_effort"} {
		if !names[want] {
			t.Fatalf("schema missing %q: %+v", want, got.Fields)
		}
	}
	if names["branch"] || names["access_mode"] {
		t.Fatalf("schema exposes unsupported field: %+v", got.Fields)
	}
}

func TestWeb_APISessionActionClearReturnsRef(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clear":
			if r.Method != http.MethodPost {
				t.Fatalf("clear method=%s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"01NEW","state":"IDLE","model":"gpt-5","profile":"openai"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer daemon.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 46, Address: strings.TrimPrefix(daemon.URL, "http://")})
	r := NewRoster(runDir, fakeProber{sessionID: "01OLD", status: "IDLE"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01OLD/clear", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.RefResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ref != "local:01NEW" || got.SessionID != "01NEW" {
		t.Fatalf("unexpected clear response: %+v", got)
	}
}

func TestWeb_APISessionActionModelForwardsBody(t *testing.T) {
	var gotPath, gotBody string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer daemon.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 47, Address: strings.TrimPrefix(daemon.URL, "http://")})
	r := NewRoster(runDir, fakeProber{sessionID: "01MODEL", status: "IDLE"})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01MODEL/model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if gotPath != "/model" || gotBody != `{"model":"gpt-5.5"}` {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}
