package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// timeNowForTest is a fixed clock for tests that need deterministic timestamps
// without depending on wall-clock time.
func timeNowForTest() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func startAppwireTestDaemonWithProtocol(t *testing.T, dir, sessionID, protocolVersion string, register func(*appserver.Server)) *httptest.Server {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	if protocolVersion != appwire.ProtocolVersion {
		appserver.HandleTyped(app.Router(), appwire.MethodInitialize, func(_ context.Context, params appwire.InitializeParams) (appwire.InitializeResponse, error) {
			if params.ProtocolVersion != protocolVersion {
				return appwire.InitializeResponse{}, appwire.InvalidRequest(
					fmt.Sprintf("protocol version %q is incompatible; want %q", params.ProtocolVersion, protocolVersion),
				)
			}
			return appwire.InitializeResponse{ProtocolVersion: protocolVersion, SourceID: "local"}, nil
		})
	}
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref := params.Ref
		if ref == "" {
			ref = "local:" + sessionID
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			ModelProvider: "gpt-5", CWD: "/tmp",
			Evener: appwire.EvenerThread{
				Ref: ref,
				Capabilities: appwire.ThreadCapabilities{
					Send: true, Steer: true, Interrupt: true, Compact: true,
					Clear: true, Shutdown: true, ChangeModel: true,
				},
			},
		}}, nil
	})
	if register != nil {
		register(app)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		app.ServeWebSocket(w, r)
	}))
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 200, Address: strings.TrimPrefix(srv.URL, "http://"), Protocol: protocolVersion,
		Endpoint: "ws" + srv.URL[len("http"):] + "/rpc", SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, WorkingDir: "/tmp", Model: "gpt-5",
	})
	return srv
}

// perAddrProber returns a different (sessionID, status) per address. Used
// to stage a project with multiple live children in distinct states.
type perAddrProber struct {
	byAddr map[string]struct{ SessionID, Status string }
}

func (p perAddrProber) Probe(entry rendezvous.Entry) hubcore.ProbeResult {
	v, present := p.byAddr[entry.Address]
	if !present {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{SessionID: v.SessionID, Status: v.Status, OK: true}
}

// TestAssetsInvalidPathIs404 pins that the static asset handler maps an
// unreadable path — an invalid-UTF-8 byte that fs.ValidPath rejects — to a 404
// rather than the 500 a bare http.FileServer returns for fs.ErrInvalid. A
// merely-missing asset must also be 404. (Surfaced by FuzzWebHandler.)
func TestAssetsInvalidPathIs404(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	handler := web.Handler()
	for _, p := range []string{"/assets/%99", "/assets/%ff%fe", "/assets/missing.css"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status=%d, want 404", p, rec.Code)
		}
	}
}

func TestLocalRouteID_CleanBreakAndExternalRefs(t *testing.T) {
	if !isLocalRouteID("02wMz5TxvEMoJEDTDGOTil") {
		t.Fatal("valid new session ID should be local")
	}
	for _, legacy := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "local:01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if isLocalRouteID(legacy) {
			t.Fatalf("legacy local ref %q should not be local", legacy)
		}
	}
	if isLocalRouteID("codex:thread_abc") {
		t.Fatal("external source-qualified ref should not be classified as local")
	}
	if got := appRefFromRouteID("codex:thread_abc"); got != "codex:thread_abc" {
		t.Fatalf("external ref was rewritten: %q", got)
	}

}

// injectMetasForTest replaces the past index with one holding the given metas.
func (s *WebServer) injectMetasForTest(metas []schema.SessionMeta) {
	idx := hubcore.NewPastIndex("")
	idx.SeedForTest(metas)
	s.cfg.Past = idx
}

func TestActiveTurnIDFromAppwireThreadPrefersEvenerActiveTurn(t *testing.T) {
	got := activeTurnIDFromAppwireThread(appwire.Thread{
		Evener: appwire.EvenerThread{ActiveTurnID: "turn_live"},
		Turns: []appwire.Turn{
			{ID: "turn_transcript", Status: appwire.TurnStatusCompleted},
		},
	})
	if got != "turn_live" {
		t.Fatalf("active turn id=%q, want turn_live", got)
	}
}

func TestWeb_WorkspaceTaskStatusInitialIsNeutral(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID: "th_tasks", SessionID: "th_tasks", Source: "codex",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Ref: "codex:th_tasks", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex:th_tasks")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `data-task-status-text>loading…`) {
		t.Fatalf("task-status must not hard-code the spinning 'loading…' placeholder:\n%s", body)
	}
}

func TestStateLabel_ErroredAndNeedsYou(t *testing.T) {
	if got := stateLabel("errored", false); got != "Error" {
		t.Fatalf("stateLabel(errored) = %q, want Error", got)
	}
	if got := stateLabel("awaiting", false); got != "Your move" {
		t.Fatalf("stateLabel(awaiting) = %q, want \"Your move\"", got)
	}
}

// TestWeb_SessionImage_ServesShaReferencedInputImage verifies that USER_INPUT
// image bytes can be fetched lazily via /s/<id>/images/<sha>.
func TestWeb_SessionImage_ServesShaReferencedInputImage(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5TxvHIJQPOuIBJQct", UpdatedAt: time.Now(), OriginalPrompt: "image demo",
	}); err != nil {
		t.Fatal(err)
	}

	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y', 'l', 'o', 'a', 'd'}
	wantSha := imageSha(imgBytes)

	tpath := filepath.Join(proj, "sessions", "02wMz5TxvHIJQPOuIBJQct.transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: "02wMz5TxvHIJQPOuIBJQct", ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	userMsg := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "what color?"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: imgBytes, MediaType: "image/png"}},
	}}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, userMsg)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    idx,
	})

	imgReq := httptest.NewRequest(http.MethodGet, "/s/02wMz5TxvHIJQPOuIBJQct/images/"+wantSha, nil)
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

func TestWeb_SessionImage_ServesShaReferencedToolResultImage(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-toolimg-0000000000")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5TxvIl3yzzcpdlu4x", UpdatedAt: time.Now(), OriginalPrompt: "tool image demo",
	}); err != nil {
		t.Fatal(err)
	}

	imgBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 't', 'o', 'o', 'l'}
	wantSha := imageSha(imgBytes)

	tpath := filepath.Join(proj, "sessions", "02wMz5TxvIl3yzzcpdlu4x.transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: "02wMz5TxvIl3yzzcpdlu4x", ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_img", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_img",
				Name:           "screenshot",
				Content:        "captured image",
				ImageData:      imgBytes,
				ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    idx,
	})

	imgReq := httptest.NewRequest(http.MethodGet, "/s/02wMz5TxvIl3yzzcpdlu4x/images/"+wantSha, nil)
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	proj := filepath.Join(root, "projects", "project-y-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5TxvKDoXaaLN6ENX1", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(proj, "sessions", "02wMz5TxvKDoXaaLN6ENX1.transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: "02wMz5TxvKDoXaaLN6ENX1", ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("text only"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx})

	allZeros := strings.Repeat("0", 64)
	req := httptest.NewRequest(http.MethodGet, "/s/02wMz5TxvKDoXaaLN6ENX1/images/"+allZeros, nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404 (body=%q)", rec.Code, rec.Body.String())
	}
}

type scriptedAppSource struct {
	id            string
	thread        appwire.Thread
	notifications []appwire.Notification
	startTurn     func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	// readParams records every ReadThread call this source has served, so a
	// test can assert on which IncludeTurns value a caller actually
	// requested (e.g. a lean status fetch vs. a full-transcript fetch).
	readParams []appwire.ThreadReadParams
}

func (s *scriptedAppSource) ID() string { return s.id }

func (s *scriptedAppSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *scriptedAppSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

// ReadThread mimics a real source's handling of IncludeTurns: when a caller
// asks for the lean view (IncludeTurns: false) the returned thread's Turns
// are cleared, matching what a real daemon would omit. Every prior caller in
// this test suite requests IncludeTurns: true, so this is additive — it only
// changes behavior for a caller that explicitly asks for the lean view.
func (s *scriptedAppSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.readParams = append(s.readParams, params)
	thread := s.thread
	if !params.IncludeTurns {
		thread.Turns = nil
	}
	return appwire.ThreadReadResponse{Thread: thread}, nil
}

func (s *scriptedAppSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("scripted source does not start threads")
}

func (s *scriptedAppSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("scripted source does not resume threads")
}

func (s *scriptedAppSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("scripted source does not fork threads")
}

func (s *scriptedAppSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	if s.startTurn != nil {
		return s.startTurn(ctx, params)
	}
	return appwire.TurnStartResponse{}, appwire.Unavailable("scripted source does not start turns")
}

func (s *scriptedAppSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return appwire.TurnSteerResponse{}, appwire.Unavailable("scripted source does not steer turns")
}

func (s *scriptedAppSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return appwire.Unavailable("scripted source does not resolve escalations")
}

func (s *scriptedAppSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, appwire.Unavailable("scripted source does not interrupt turns")
}

func (s *scriptedAppSource) QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return appwire.TurnQueueResponse{}, appwire.Unavailable("scripted source does not queue turns")
}

func (s *scriptedAppSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("scripted source does not drain as steer")
}

func (s *scriptedAppSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("scripted source does not promote queued messages")
}

func (s *scriptedAppSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, appwire.Unavailable("scripted source does not cancel queued messages")
}

func (s *scriptedAppSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return appwire.Unavailable("scripted source does not compact threads")
}

func (s *scriptedAppSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return appwire.Unavailable("scripted source does not shut down threads")
}

func (s *scriptedAppSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("scripted source does not set models")
}

func (s *scriptedAppSource) SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error {
	return appwire.Unavailable("scripted source does not set vision models")
}

func (s *scriptedAppSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return appwire.Unavailable("scripted source does not set names")
}

func (s *scriptedAppSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return appwire.Unavailable("scripted source does not set reasoning effort")
}

func (s *scriptedAppSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, appwire.Unavailable("scripted source does not set goals")
}

func (s *scriptedAppSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("scripted source does not clear threads")
}

func (s *scriptedAppSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, appwire.Unavailable("scripted source does not list models")
}

func (s *scriptedAppSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("scripted source does not list tasks")
}

func (s *scriptedAppSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, appwire.Unavailable("scripted source does not list jobs")
}

func (s *scriptedAppSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, appwire.Unavailable("scripted source does not read job output")
}

func (s *scriptedAppSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification, len(s.notifications))
	for _, notification := range s.notifications {
		out <- notification
	}
	close(out)
	return out, nil
}

func testRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWeb_ThreadDocument_SecurityHeaders(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/thread/anysession", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("thread document should preserve same-origin frame policy, CSP=%q", csp)
	}
}

func TestFormatContextNumbersShowsUsedWindowAndRemaining(t *testing.T) {
	// Use remaining=55000 ≠ window-used=58000 so the test detects any mutation
	// that recomputes remaining as window-used instead of using the parameter.
	got := formatContextNumbers(42000, 100000, 55000)
	want := "42k / 100k tokens (55k left)"
	if got != want {
		t.Fatalf("formatContextNumbers() = %q, want %q", got, want)
	}
}

func TestFormatCompactContextNumbersShowsOnlyUsedAndWindow(t *testing.T) {
	cases := []struct {
		used, window int
		want         string
	}{
		{42000, 100000, "42k / 100k"},
		{999, 2048, "999 / 2k"},
		{42000, 0, ""},
	}
	for _, tc := range cases {
		if got := formatCompactContextNumbers(tc.used, tc.window); got != tc.want {
			t.Errorf("formatCompactContextNumbers(%d, %d) = %q, want %q", tc.used, tc.window, got, tc.want)
		}
	}
}

func TestWorktreeLabelUsesLeafAndIgnoresEmpty(t *testing.T) {
	if got := worktreeLabel("/state/worktrees/evener/dlg_01H"); got != "dlg_01H" {
		t.Fatalf("worktreeLabel() = %q, want dlg_01H", got)
	}
	if got := worktreeLabel(""); got != "" {
		t.Fatalf("worktreeLabel(empty) = %q, want empty", got)
	}
}

func TestWeb_WorkspaceDataUsesPersistedWorktree(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5TxvEMoJEDTDGOTil"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:           sessionID,
		WorktreePath: "/state/worktrees/evener/dlg_01H",
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir: "/state/worktrees/evener/dlg_01H",
			GitBranch:  "feature/compact-rail",
		},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx})
	data := web.workspaceData(sessionID)
	if data.Worktree != "dlg_01H" {
		t.Errorf("Worktree = %q, want dlg_01H", data.Worktree)
	}
	if data.WorkingDir != "/state/worktrees/evener/dlg_01H" {
		t.Errorf("WorkingDir = %q, want full worktree path", data.WorkingDir)
	}
	if data.Branch != "feature/compact-rail" {
		t.Errorf("Branch = %q, want feature/compact-rail", data.Branch)
	}
}

func webWithPersistedInProcessSubagent(t *testing.T, childID string, runningSubagentIDs ...string) *WebServer {
	t.Helper()

	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{
		ID:              childID,
		IsSubagent:      true,
		ParentSessionID: "02wMz5TxvEMoJEDTDGOTil",
	}})
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		PID:                41,
		SessionID:          "02wMz5TxvEMoJEDTDGOTil",
		Status:             appwire.ThreadStatusIdle,
		RunningSubagentIDs: append([]string(nil), runningSubagentIDs...),
	})
	return NewWebServer(hubcore.WebConfig{Roster: roster, Past: past})
}

func TestWorkspaceDataProjectsRunningInProcessSubagentActive(t *testing.T) {
	const childID = "02wMz5TxvFpYrooBkiqxAp"
	web := webWithPersistedInProcessSubagent(t, childID, childID)

	data := web.workspaceData(childID)
	if data.State != "active" {
		t.Fatalf("State = %q, want active", data.State)
	}
	if data.StateLabel != stateLabel("active", false) {
		t.Fatalf("StateLabel = %q, want %q", data.StateLabel, stateLabel("active", false))
	}
}

// A non-closed in-process subagent whose owner daemon carries its settled
// (idle) status renders idle, not active: RunningSubagentIDs is a liveness
// set, and the carried per-descendant state is what says whether it works.
func TestWorkspaceDataProjectsIdleInProcessSubagentIdle(t *testing.T) {
	childID := hubtest.SessionID(t)
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{
		ID:              childID,
		IsSubagent:      true,
		ParentSessionID: "02wMz5TxvEMoJEDTDGOTil",
	}})
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		PID:                   41,
		SessionID:             "02wMz5TxvEMoJEDTDGOTil",
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    []string{childID},
		RunningSubagentStates: map[string]string{childID: "idle"},
	})
	web := NewWebServer(hubcore.WebConfig{Roster: roster, Past: past})

	data := web.workspaceData(childID)
	if data.State != "idle" {
		t.Fatalf("workspace State = %q, want idle", data.State)
	}

}

func TestWorkspaceDataProjectsStoppedInProcessSubagentEnded(t *testing.T) {
	const childID = "02wMz5TxvIl3yzzcpdlu4x"
	web := webWithPersistedInProcessSubagent(t, childID, "02wMz5TxvKDoXaaLN6ENX1")

	data := web.workspaceData(childID)
	if data.State != "ended" {
		t.Fatalf("State = %q, want ended", data.State)
	}
	if data.StateLabel != stateLabel("ended", false) {
		t.Fatalf("StateLabel = %q, want %q", data.StateLabel, stateLabel("ended", false))
	}
}

func webWithLiveAppWireResponse(t *testing.T, live hubcore.LiveEntry, thread appwire.Thread) (*WebServer, <-chan appwire.ThreadReadParams) {
	t.Helper()
	reads := make(chan appwire.ThreadReadParams, 1)
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		select {
		case reads <- params:
		default:
		}
		return appwire.ThreadReadResponse{Thread: thread}, nil
	})
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		app.ServeWebSocket(w, r)
	}))
	t.Cleanup(daemon.Close)
	live.Address = strings.TrimPrefix(daemon.URL, "http://")
	live.Protocol = appwire.ProtocolVersion
	live.Endpoint = "ws" + daemon.URL[len("http"):] + "/rpc"
	live.SourceID = "local"
	roster := hubcore.NewRosterWithEntries(live)
	return NewWebServer(hubcore.WebConfig{Roster: roster}), reads
}

func webWithLiveAppWireStatus(t *testing.T, thread appwire.Thread) (*WebServer, <-chan appwire.ThreadReadParams) {
	t.Helper()
	return webWithLiveAppWireResponse(t, hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			ThreadID: thread.ID, SessionID: thread.SessionID, Model: thread.ModelProvider, WorkingDir: thread.CWD,
		},
		SessionID: thread.SessionID,
		Status:    thread.Status.Type,
	}, thread)
}

func TestWorkspaceDataRejectsEmptyOrMismatchedDaemonStatus(t *testing.T) {
	sessionID := hubtest.SessionID(t)
	differentSessionID := hubtest.SessionID(t)
	tests := []struct {
		name   string
		thread appwire.Thread
	}{
		{name: "empty response"},
		{
			name: "different root",
			thread: appwire.Thread{
				ID: differentSessionID, SessionID: differentSessionID, ModelProvider: "wrong-model", CWD: "/wrong/root",
				Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
				Evener: appwire.EvenerThread{
					Ref: localAppRef(differentSessionID), TurnCount: 99, WorkMillis: 3000,
					Usage: &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := hubcore.LiveEntry{
				Entry: rendezvous.Entry{
					ThreadID: sessionID, SessionID: sessionID, Model: "roster-model", WorkingDir: "/roster/work",
				},
				SessionID: sessionID,
				Status:    appwire.ThreadStatusActive,
			}
			web, _ := webWithLiveAppWireResponse(t, live, tc.thread)

			if got := web.fetchStatus(live); got != nil {
				t.Fatalf("fetchStatus = %+v, want nil for an unverified root", got)
			}
			got := web.workspaceData(sessionID)
			if got.State != appwire.ThreadStatusActive || got.Model != "roster-model" || got.WorkingDir != "/roster/work" {
				t.Fatalf("workspace live fallback = state %q model %q dir %q, want roster-backed values", got.State, got.Model, got.WorkingDir)
			}
			if got.TurnCount != 0 || got.WorkMillis != 0 || got.Usage != nil {
				t.Fatalf("workspace accepted unverified hydration metrics: turns=%d workMillis=%d usage=%+v", got.TurnCount, got.WorkMillis, got.Usage)
			}
		})
	}
}

func TestWorkspaceDataUsesDaemonStatusTurnCountForLiveLocalSession(t *testing.T) {
	const sessionID = "02wMz5TxvFpYrooBkiqxAp"
	web, reads := webWithLiveAppWireStatus(t, appwire.Thread{
		ID: sessionID, SessionID: sessionID, ModelProvider: "gpt-5", CWD: "/tmp/turns",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{
			Ref: "local:" + sessionID, TurnCount: 37,
		},
	})

	got := web.workspaceData(sessionID)
	if got.TurnCount != 37 {
		t.Fatalf("TurnCount = %d, want daemon AppWire turnCount 37", got.TurnCount)
	}
	select {
	case params := <-reads:
		if params.IncludeTurns {
			t.Fatal("workspace status hydration must not request the transcript")
		}
	default:
		t.Fatal("workspace status hydration did not call daemon thread/read")
	}
}

// TestWorkspaceData_LiveSessionCarriesCostEstimate verifies the roster-live
// branch of workspaceData renders the cost the daemon reported on
// thread.Evener.Cost, which it priced from its own session's registry row
// (spec §7.5), rather than re-deriving one here.
func TestWorkspaceData_LiveSessionCarriesCostEstimate(t *testing.T) {
	const sessionID = "02wMz5TxvHIJQPOuIBJQct"
	web, _ := webWithLiveAppWireStatus(t, appwire.Thread{
		ID: sessionID, SessionID: sessionID, ModelProvider: "claude-opus-4-5", CWD: "/tmp/costlive",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{
			Ref: "local:" + sessionID, TurnCount: 1,
			Usage: &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 20_000},
			Cost:  "~$1.00",
		},
	})

	got := web.workspaceData(sessionID)
	if got.Cost != "~$1.00" {
		t.Fatalf("Cost = %q, want ~$1.00", got.Cost)
	}
}

// TestWorkspaceData_PastSessionCarriesCostEstimate verifies the past-meta
// branch of workspaceData prices the persisted CumulativeUsage at the cost
// the session's own ProfileID/Model resolve to on the hub's registry.
func TestWorkspaceData_PastSessionCarriesCostEstimate(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5TxvIl3yzzcpdlu4x"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "anthropic",
		Model:     "claude-opus-4-5",
		CumulativeUsage: schema.CumulativeUsage{
			InputTokens:  100_000,
			OutputTokens: 20_000,
		},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	reg := pricingRegistry(t)
	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx, Registry: reg})
	got := web.workspaceData(sessionID)
	want := appwire.EstimateCost(costFor(reg, "anthropic", "claude-opus-4-5"), &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 20_000})
	if want == "" {
		t.Fatal("fixture registry has no cost for anthropic/claude-opus-4-5")
	}
	if got.Cost != want {
		t.Fatalf("Cost = %q, want %q", got.Cost, want)
	}

	// Flag day (spec §14.1): the same session on a hub with no registry
	// reports its tokens and no dollar figure.
	noRegistry := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx})
	if got := noRegistry.workspaceData(sessionID); got.Cost != "" {
		t.Fatalf("Cost = %q with no registry, want empty", got.Cost)
	}
}

// TestWorkspaceData_NoCostWhenUsageNil verifies a past session with zero
// CumulativeUsage renders no Cost, rather than a misleading "~$0.00".
func TestWorkspaceData_NoCostWhenUsageNil(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5TxvKDoXaaLN6ENX1"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "anthropic",
		Model:     "claude-opus-4-5",
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx, Registry: pricingRegistry(t)})
	got := web.workspaceData(sessionID)
	if got.Cost != "" {
		t.Fatalf("Cost = %q, want empty for zero usage", got.Cost)
	}
}

// TestWeb_Send_ClosedSessionRequiresSpawner verifies that POSTing to /s/<id>/send
// when the session is not live and no spawner is configured returns 503.
func TestWeb_APIHealth(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  "/tmp/evener-run",
		Past:    hubcore.NewPastIndex("/tmp/state/projects/*"),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"mobile_api_version":1`) {
		t.Fatalf("health JSON missing mobile API version: %s", rec.Body.String())
	}
	var got hubapi.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version == "" || got.HubAddr != "127.0.0.1:9180" {
		t.Fatalf("unexpected health: %+v", got)
	}
	if !got.Capabilities.TranscriptFollow {
		t.Fatalf("missing capabilities: %+v", got.Capabilities)
	}
}

func TestWeb_APIHealthExposesAssetIdentity(t *testing.T) {
	// Verify /api/health includes frontend asset hash and backend git SHA.
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
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
	// FrontendHash should be present (12 hex chars from frontendDistHash).
	if got.FrontendHash == "" {
		t.Fatalf("FrontendHash is empty: %+v", got)
	}
	if len(got.FrontendHash) != 12 {
		t.Fatalf("FrontendHash wrong length: got %q (len=%d), want 12 chars", got.FrontendHash, len(got.FrontendHash))
	}
	// BackendGitSha must mirror buildinfo.GitSHA exactly: empty under this
	// unstamped test binary (dev build), and precisely the stamped value when
	// the build has one. The handler reads the var at request time
	// (web_api.go handleAPIHealth), so stamping it in-process proves the wiring
	// end to end - the save/set/restore pattern buildinfo's own tests use.
	if got.BackendGitSha != buildinfo.GitSHA {
		t.Fatalf("BackendGitSha = %q, want buildinfo.GitSHA %q (unstamped test build)", got.BackendGitSha, buildinfo.GitSHA)
	}
	savedSHA := buildinfo.GitSHA
	buildinfo.GitSHA = "0123456789abcdef"
	t.Cleanup(func() { buildinfo.GitSHA = savedSHA })
	rec2 := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("stamped: status=%d body=%q", rec2.Code, rec2.Body.String())
	}
	var stamped hubapi.HealthResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &stamped); err != nil {
		t.Fatalf("stamped: decode: %v", err)
	}
	if stamped.BackendGitSha != "0123456789abcdef" {
		t.Fatalf("stamped: BackendGitSha = %q, want %q", stamped.BackendGitSha, "0123456789abcdef")
	}
	// Pin the raw wire key too: decoding through hubapi.HealthResponse alone
	// would round-trip a renamed json tag invisibly (both reviewers of kata
	// k22d flagged the unpinned key).
	if !strings.Contains(rec2.Body.String(), `"backend_git_sha":"0123456789abcdef"`) {
		t.Fatalf("stamped: body %q lacks the backend_git_sha wire key with the stamped value", rec2.Body.String())
	}
}

func TestWeb_WorkspaceDataLocalLiveUsesAppWireCapabilities(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 64, Address: "127.0.0.1:6464", WorkingDir: "/projects/evener", Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01CAPS", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:            "01CAPS",
			SessionID:     "01CAPS",
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			ModelProvider: "gpt-5",
			CWD:           "/projects/evener",
			Turns: []appwire.Turn{
				{ID: "turn_live", Status: appwire.TurnStatusInProgress},
			},
			Evener: appwire.EvenerThread{
				Ref:          "local:01CAPS",
				Capabilities: appwire.ThreadCapabilities{Steer: true, Interrupt: true, Compact: true},
			},
		},
	})

	got := web.workspaceData("01CAPS")
	if got.ActiveTurnID != "turn_live" {
		t.Fatalf("active turn id=%q", got.ActiveTurnID)
	}
	if !got.Capabilities.Compact {
		t.Fatalf("compact capability missing: %+v", got.Capabilities)
	}
	if !got.Capabilities.Steer || !got.Capabilities.Interrupt {
		t.Fatalf("turn capabilities missing: %+v", got.Capabilities)
	}
	if got.Capabilities.Send || got.Capabilities.Clear || got.Capabilities.Shutdown || got.Capabilities.ChangeModel {
		t.Fatalf("workspace exposed unsupported capabilities: %+v", got.Capabilities)
	}
}

func TestLiveWorkspaceSnapshotSkipsTurns(t *testing.T) {
	source := &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "01METADATA",
			SessionID: "01METADATA",
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          "local:01METADATA",
				Capabilities: appwire.ThreadCapabilities{Send: true},
				ActiveTurnID: "turn_active",
			},
		},
	}
	web := NewWebServer(hubcore.WebConfig{})
	web.sources.Add(source)

	caps, activeTurnID := web.liveWorkspaceSnapshot("local:01METADATA", hubapi.SessionCapabilities{})
	if len(source.readParams) != 1 {
		t.Fatalf("ReadThread calls=%d, want 1", len(source.readParams))
	}
	if got := source.readParams[0]; got.IncludeTurns {
		t.Fatal("workspace metadata requested transcript turns")
	}
	if !caps.Send || activeTurnID != "turn_active" {
		t.Fatalf("caps=%+v activeTurnID=%q", caps, activeTurnID)
	}
}

// observerWorkspaceFixture writes a worker meta whose ObservedBy lists the given
// observers, builds a hub over it, and returns a WebServer with the named live
// roster sessions. It is the shared setup for the observer-link flow tests.
func observerWorkspaceFixture(t *testing.T, observedBy []string, liveSessionIDs ...string) *WebServer {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-observer-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), OriginalPrompt: "do work",
		IsSubagent: true, ParentSessionID: "02wMz5Txv2enqVTitaig6F", ObservedBy: observedBy,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/project-observer-0123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entries := make([]hubcore.LiveEntry, 0, len(liveSessionIDs))
	for i, id := range liveSessionIDs {
		entries = append(entries, hubcore.LiveEntry{PID: i + 1, SessionID: id, Status: appwire.ThreadStatusActive})
	}
	return NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRosterWithEntries(entries...),
		Past:    idx,
	})
}

func TestWeb_WorkspaceData_CarriesLiveObserver(t *testing.T) {
	web := observerWorkspaceFixture(t, []string{"02wMz5Txv47YP64RR3B9YJ"}, "02wMz5Txv47YP64RR3B9YJ")
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 1 || wd.ObserverRouteIDs[0] != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("ObserverRouteIDs = %v, want [02wMz5Txv47YP64RR3B9YJ]", wd.ObserverRouteIDs)
	}
}

// An observer that has ended (absent from the live roster) is STILL surfaced so
// the renderer auto-opens its pane beside the worker (observers auto-open, live
// or ended). The flood of a worker with many past observers is bounded by the
// side-pane cap + closed-pane suppression on the client.
func TestWeb_WorkspaceData_IncludesEndedObserver(t *testing.T) {
	web := observerWorkspaceFixture(t, []string{"02wMz5Txv47YP64RR3B9YJ"}) // observer not in roster (ended)
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 1 || wd.ObserverRouteIDs[0] != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("ended observer must still be surfaced; got %v", wd.ObserverRouteIDs)
	}
}

// An ordinary worker with no ObservedBy carries no observer route ids even
// when unrelated durable job records exist beside its session metadata.
func TestWeb_WorkspaceData_NoObserversWhenUnwatched(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-observer-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions", "02wMz5Txv2enqVTitaig6F"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), OriginalPrompt: "do work",
		IsSubagent: true, ParentSessionID: "02wMz5Txv2enqVTitaig6F",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/project-observer-0123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "sessions", "02wMz5Txv2enqVTitaig6F", "jobs.jsonl"), []byte("unrelated durable job record\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: idx})
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 0 {
		t.Fatalf("un-watched worker must have no observers; got %v", wd.ObserverRouteIDs)
	}
}
