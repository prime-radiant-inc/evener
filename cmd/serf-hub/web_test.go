package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
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

func TestWriteSessionActionErrorSetsSerfErrorInfoHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/s/01TEST/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	writeSessionActionError(rec, req, appwire.QueuedDrainPartial("queued but drain failed"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Serf-Error-Info"); got != string(appwire.ErrorQueuedDrainPartial) {
		t.Fatalf("X-Serf-Error-Info=%q", got)
	}
}

func TestHubDetailFromAppThreadTreatsClosedAsNotLive(t *testing.T) {
	detail := hubDetailFromAppThread(appwire.Thread{
		ID:        "th_closed",
		SessionID: "th_closed",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:th_closed"},
	})
	if detail.Live {
		t.Fatalf("closed detail marked live: %+v", detail)
	}
}

func TestWeb_CodexSessionRouteReadsConfiguredSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "th_codex" {
			t.Fatalf("thread/read params=%+v", params)
		}
		return map[string]any{"thread": map[string]any{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "Codex task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     100,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
			"turns":         []any{},
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-local",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex-local:th_codex")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Codex task") || !strings.Contains(body, `data-session-id="codex-local:th_codex"`) {
		t.Fatalf("body=%s", body)
	}
	if !strings.Contains(body, `data-source-label="codex-local"`) || !strings.Contains(body, ">codex-local<") {
		t.Fatalf("body=%s", body)
	}
	for _, unsupported := range []string{`data-action-trigger="shutdown"`, `data-model-trigger`} {
		if strings.Contains(body, unsupported) {
			t.Fatalf("codex workspace advertised unsupported control %q:\n%s", unsupported, body)
		}
	}
	for _, disabledUntilTurn := range []string{`data-action-trigger="interrupt"`, `data-steer-trigger data-capability-steer="true" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty" disabled`} {
		if !strings.Contains(body, disabledUntilTurn) {
			t.Fatalf("codex workspace missing disabled turn control %q:\n%s", disabledUntilTurn, body)
		}
	}
	for _, unsupportedHeader := range []string{`data-action-trigger="compact"`, `data-action-trigger="shutdown"`} {
		if strings.Contains(body, unsupportedHeader) {
			t.Fatalf("workspace rendered removed header action %q:\n%s", unsupportedHeader, body)
		}
	}
	for _, supported := range []string{`class="btn btn-primary send-btn"`} {
		if !strings.Contains(body, supported) {
			t.Fatalf("codex workspace missing supported control %q:\n%s", supported, body)
		}
	}
}

func TestWeb_WorkspaceRendersDisabledSteerControlForIdleSendCapableAppThread(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Past: NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID:            "th_idle",
			SessionID:     "th_idle",
			Source:        "codex",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			ModelProvider: "gpt-5",
			Serf: appwire.SerfThread{
				Ref:          "codex:th_idle",
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: false},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex:th_idle")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-steer-trigger data-capability-steer="false"`) || !strings.Contains(body, `disabled>send as steer`) {
		t.Fatalf("workspace should render disabled steer control for idle send-capable app thread:\n%s", body)
	}
}

func TestWeb_WorkspaceRendersBottomStopForActiveSession(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Past: NewPastIndex("")})
	started := time.Now().Add(-2 * time.Minute).Unix()
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID:            "th_active",
			SessionID:     "th_active",
			Source:        "codex",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			ModelProvider: "gpt-5",
			Turns: []appwire.Turn{{
				ID:        "turn_1",
				Status:    appwire.TurnStatusInProgress,
				StartedAt: &started,
			}},
			Serf: appwire.SerfThread{
				Ref:          "codex:th_active",
				Capabilities: appwire.ThreadCapabilities{Send: false, Steer: true, Interrupt: true, Queue: true},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/_partials/s/"+url.PathEscape("codex:th_active")+"/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `class="header-action" data-action-trigger="interrupt"`) ||
		strings.Contains(body, `data-action-trigger="compact"`) ||
		strings.Contains(body, `data-action-trigger="shutdown"`) {
		t.Fatalf("workspace rendered removed header controls:\n%s", body)
	}
	if !strings.Contains(body, `class="btn btn-danger stop-btn" data-action-trigger="interrupt"`) ||
		!strings.Contains(body, `>Stop<`) {
		t.Fatalf("workspace missing bottom Stop control:\n%s", body)
	}
	if strings.Contains(body, `class="btn btn-danger stop-btn" data-action-trigger="interrupt" title="stop the in-flight turn" disabled`) {
		t.Fatalf("bottom Stop should be enabled for active session:\n%s", body)
	}
	if !strings.Contains(body, `data-running-indicator`) {
		t.Fatalf("workspace missing bottom running indicator:\n%s", body)
	}
}

func TestAPI_CodexSessionDetailReadsConfiguredSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":            params["threadId"],
			"sessionId":     params["threadId"],
			"preview":       "Codex API task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     100,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
			"turns":         []any{},
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+url.PathEscape("codex:th_codex"), nil)
	req.Host = "127.0.0.1:9180"
	rr := httptest.NewRecorder()
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var detail hubapi.SessionDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Ref != "codex:th_codex" || detail.Title != "Codex API task" {
		t.Fatalf("detail=%+v", detail)
	}
	if !detail.Capabilities.Send {
		t.Fatalf("codex supported capabilities missing: %+v", detail.Capabilities)
	}
	if !detail.Capabilities.Compact {
		t.Fatalf("codex compact capability missing: %+v", detail.Capabilities)
	}
	if !detail.Capabilities.Steer || !detail.Capabilities.Interrupt {
		t.Fatalf("codex turn action support missing: %+v", detail.Capabilities)
	}
	if detail.ActiveTurnID != "" {
		t.Fatalf("idle codex detail exposed active turn id %q", detail.ActiveTurnID)
	}
	if detail.Capabilities.Clear || detail.Capabilities.Fork || detail.Capabilities.Shutdown || detail.Capabilities.ChangeModel {
		t.Fatalf("codex unsupported capabilities exposed: %+v", detail.Capabilities)
	}
}

func TestWeb_APITreeIncludesConfiguredCodexSourceThreads(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "Codex tree task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     200,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/codex",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Live) != 1 {
		t.Fatalf("live=%+v", got.Live)
	}
	if got.Live[0].Ref != "codex:th_codex" || got.Live[0].HostID != "codex" || got.Live[0].SessionID != "th_codex" {
		t.Fatalf("codex live node lost source-qualified identity: %+v", got.Live[0])
	}
	var foundProject bool
	for _, project := range got.Projects {
		for _, session := range project.Sessions {
			if session.Ref == "codex:th_codex" && session.Title == "Codex tree task" {
				foundProject = true
			}
		}
	}
	if !foundProject {
		t.Fatalf("codex thread missing from project tree: %+v", got.Projects)
	}
}

func TestWeb_APITreeMarksConfiguredCodexEndedThreadsRecent(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{
			"id":            "th_codex_ended",
			"sessionId":     "th_codex_ended",
			"preview":       "Codex ended tree task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     200,
			"status":        map[string]any{"type": "closed"},
			"cwd":           "/work/codex",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Live) != 0 {
		t.Fatalf("ended codex thread appeared in live tree: %+v", got.Live)
	}
	var found *hubapi.TreeNode
	for _, project := range got.Projects {
		for i := range project.Sessions {
			if project.Sessions[i].Ref == "codex:th_codex_ended" {
				found = &project.Sessions[i]
			}
		}
	}
	if found == nil {
		t.Fatalf("ended codex thread missing from project tree: %+v", got.Projects)
	}
	if found.Live || found.State != "ended" {
		t.Fatalf("ended codex thread live metadata = %+v, want live=false state=ended", *found)
	}
}

func TestWeb_SidebarIncludesConfiguredCodexSourceThreads(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "Codex sidebar task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     200,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/codex",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/s/codex:th_codex"`) || !strings.Contains(body, `hx-get="/_partials/s/codex:th_codex/workspace"`) {
		t.Fatalf("sidebar missing source-qualified codex link:\n%s", body)
	}
	if !strings.Contains(body, "Codex sidebar task") {
		t.Fatalf("sidebar missing codex title:\n%s", body)
	}
}

func TestAPI_ManagedCodexSessionDetailEnsuresSource(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+url.PathEscape("codex-managed:th_fake"), nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail hubapi.SessionDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if detail.Ref != "codex-managed:th_fake" || !detail.Capabilities.Send {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestWeb_APITreeIncludesManagedCodexLaunchThreads(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	var hasSource, hasThread bool
	for _, source := range got.Sources {
		if source.ID == "codex-managed" && source.Online {
			hasSource = true
		}
	}
	for _, project := range got.Projects {
		for _, session := range project.Sessions {
			if session.Ref == "codex-managed:th_fake" && session.Title == "fake codex" {
				hasThread = true
			}
		}
	}
	if !hasSource || !hasThread {
		t.Fatalf("managed codex launch missing from tree: source=%v thread=%v tree=%+v", hasSource, hasThread, got)
	}
}

func TestAPI_CodexUnsupportedActionReturnsStructuredUnavailable(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":            params["threadId"],
			"sessionId":     params["threadId"],
			"preview":       "Codex API task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     100,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
			"turns":         []any{},
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+url.PathEscape("codex:th_codex")+"/shutdown", nil)
	req.Host = "127.0.0.1:9180"
	rr := httptest.NewRecorder()
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out hubapi.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if out.SerfErrorInfo != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("error=%+v", out)
	}
}

func TestAPI_CodexModelChangeReturnsStructuredUnavailable(t *testing.T) {
	source := &scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID:            "th_codex",
			SessionID:     "th_codex",
			Source:        "codex",
			Preview:       "Codex task",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			CWD:           "/work/project",
			ModelProvider: "openai",
			Serf: appwire.SerfThread{
				Ref:          "codex:th_codex",
				Capabilities: appwire.ThreadCapabilities{Send: true, Compact: true},
			},
		},
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
	})
	web.sources.Add(source)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+url.PathEscape("codex:th_codex")+"/model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	web.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out hubapi.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if out.SerfErrorInfo != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("error=%+v", out)
	}
}

func TestAPI_SendUnavailableCapabilityDoesNotStartTurn(t *testing.T) {
	startTurnCalls := 0
	source := &scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID:            "th_no_send",
			SessionID:     "th_no_send",
			Source:        "codex",
			Preview:       "Codex read-only task",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			CWD:           "/work/project",
			ModelProvider: "openai",
			Serf: appwire.SerfThread{
				Ref:          "codex:th_no_send",
				Capabilities: appwire.ThreadCapabilities{Compact: true},
			},
		},
		startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			startTurnCalls++
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_unavailable"}}, nil
		},
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
	})
	web.sources.Add(source)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+url.PathEscape("codex:th_no_send")+"/send", strings.NewReader(`{"text":"hi"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if startTurnCalls != 0 {
		t.Fatalf("StartTurn calls=%d, want 0", startTurnCalls)
	}
	var out hubapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if out.SerfErrorInfo != string(appwire.ErrorActionUnavailable) || !strings.Contains(out.Error, "send is not available") {
		t.Fatalf("error=%+v", out)
	}
}

func TestAPI_SendEnsuresManagedCodexAppServerAfterExit(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	if _, err := launcher.EnsureSource(context.Background(), "codex-managed", nil); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+url.PathEscape("codex-managed:th_fake")+"/send", strings.NewReader(`{"text":"hi"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	next := launcherRunningProcess(t, launcher, "codex-managed")
	if next == first {
		t.Fatal("send reused the exited managed Codex process")
	}
}

func TestWeb_LocalSendUnavailableCapabilityDoesNotStartTurn(t *testing.T) {
	startTurnCalls := 0
	source := &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "th_no_send",
			SessionID: "th_no_send",
			Source:    "local",
			Preview:   "Local read-only task",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			CWD:       "/work/project",
			Serf: appwire.SerfThread{
				Ref:          "local:th_no_send",
				Capabilities: appwire.ThreadCapabilities{Compact: true},
			},
		},
		startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			startTurnCalls++
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_unavailable"}}, nil
		},
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
	})
	web.sources.Add(source)

	req := httptest.NewRequest(http.MethodPost, "/s/th_no_send/send", strings.NewReader(`{"text":"hi"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if startTurnCalls != 0 {
		t.Fatalf("StartTurn calls=%d, want 0", startTurnCalls)
	}
	if !strings.Contains(rec.Body.String(), "send is not available") {
		t.Fatalf("body=%s", rec.Body.String())
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
	if !strings.Contains(body, `hx-get="/_partials/sidebar"`) {
		t.Errorf("missing sidebar hx-get")
	}
	if !strings.Contains(body, `hx-get="/_partials/workspace/empty"`) {
		t.Errorf("missing workspace partial hx-get")
	}
}

func TestWeb_InternalPartialsRequireHXRequest(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: NewRoster(t.TempDir(), nil), Past: NewPastIndex("")})
	for _, path := range []string{
		"/_partials/sidebar",
		"/_partials/workspace/empty",
		"/_partials/workspace/spawn",
		"/_partials/settings/general",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestWeb_LegacyPartialRoutesDoNotServeFragments(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: NewRoster(t.TempDir(), nil), Past: NewPastIndex("")})
	for _, path := range []string{"/sidebar", "/workspace/empty", "/workspace/spawn"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestWeb_SettingsFullPageLoadsInternalPartial(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: NewRoster(t.TempDir(), nil), Past: NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/settings/theme", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `hx-get="/_partials/settings/theme"`) {
		t.Fatalf("settings full page did not load internal partial:\n%s", rec.Body.String())
	}
}

func TestWeb_Sidebar_RendersTreeWithLiveAndProjects(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST", UpdatedAt: time.Now(), OriginalPrompt: "fix bug",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `project-section collapsed`) {
		t.Errorf("project section should default collapsed: %q", body)
	}
	if !strings.Contains(body, "fix bug") {
		t.Errorf("missing title")
	}
	if !strings.Contains(body, "sb-row") {
		t.Errorf("missing sb-row class")
	}
	if !strings.Contains(body, "/s/01PAST") {
		t.Errorf("missing session URL")
	}
}

func TestWeb_Sidebar_ProjectLinksEscapeWorkingDir(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "escaped")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	workingDir := "/projects/a&b?c#d"
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01ESCAPED", UpdatedAt: time.Now(), OriginalPrompt: "escaped project",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: workingDir},
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	escaped := url.QueryEscape(workingDir)
	for _, want := range []string{
		`href="/settings/project?cwd=` + escaped + `"`,
		`hx-get="/_partials/settings/project?cwd=` + escaped + `"`,
		`hx-push-url="/settings/project?cwd=` + escaped + `"`,
		`href="/new?dir=` + escaped + `"`,
		`hx-get="/_partials/workspace/spawn?dir=` + escaped + `"`,
		`hx-push-url="/new?dir=` + escaped + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sidebar missing escaped URL %q:\n%s", want, body)
		}
	}
}

func TestWeb_ProjectSettingsListEscapesWorkingDir(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "escaped-settings")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	workingDir := "/projects/a&b?c#d"
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PSET", UpdatedAt: time.Now(), OriginalPrompt: "project settings",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: workingDir},
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/project", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "settings-content")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	escaped := url.QueryEscape(workingDir)
	for _, want := range []string{
		`href="/settings/project?cwd=` + escaped + `"`,
		`hx-get="/_partials/settings/project?cwd=` + escaped + `"`,
		`hx-push-url="/settings/project?cwd=` + escaped + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("project settings list missing escaped URL %q:\n%s", want, body)
		}
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
	if !strings.Contains(body, `data-chip-value-harness`) {
		t.Errorf("body missing data-chip-value-harness: %q", body)
	}
	if !strings.Contains(body, `name="harness" value="serf"`) {
		t.Errorf("body missing default harness input: %q", body)
	}
}

func TestSpawnTemplate_HasSchemaAdvancedRoot(t *testing.T) {
	t.Setenv("SERF_MODEL", "openai/gpt-5")
	t.Setenv("SERF_REASONING_EFFORT", "high")
	t.Setenv("OPENAI_API_KEY", "secret-token")
	t.Setenv("SERF_API_TOKEN", "secret-token")
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-launch-advanced-root`,
		`data-launch-schema-loading`,
		`data-launch-env-fallbacks`,
		`data-launch-advanced-groups`,
		`id="spawn-advanced-schema"`,
		`data-launch-env-fallback data-env-name="SERF_MODEL" value="openai/gpt-5"`,
		`data-launch-env-fallback data-env-name="SERF_REASONING_EFFORT" value="high"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("spawn advanced missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `data-spawn-advanced-schema-root`) {
		t.Fatalf("spawn advanced still uses old root hook:\n%s", body)
	}
	for _, blocked := range []string{"OPENAI_API_KEY", "SERF_API_TOKEN", "secret-token"} {
		if strings.Contains(body, blocked) {
			t.Fatalf("spawn advanced exposed blocked env %q:\n%s", blocked, body)
		}
	}
	js, err := os.ReadFile("assets/spawn.js")
	if err != nil {
		t.Fatalf("read spawn.js: %v", err)
	}
	src := string(js)
	for _, want := range []string{
		`document.querySelector("[data-launch-advanced-root]")`,
		`root.querySelector("[data-launch-schema-loading]")`,
		`root.querySelector("[data-launch-advanced-groups]")`,
		`document.querySelectorAll("[data-launch-env-fallback]")`,
		`button.dataset.settingsModelPicker = "true"`,
		`validateAdvancedPathScalars`,
		`command.dataset.launchMcpCommand = "true"`,
		`validateMCPCommandInput(command)`,
		`input.className = "spawn-advanced-textarea"`,
		`input.rows = 6`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("spawn.js missing %q", want)
		}
	}
	css, err := os.ReadFile("assets/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	for _, want := range []string{
		`.spawn-advanced-textarea`,
		`min-height: 132px`,
		`resize: vertical`,
	} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("style.css missing %q", want)
		}
	}
	listBeforeControls := strings.Index(src, "wrap.appendChild(list);\n    wrap.appendChild(controls);")
	if listBeforeControls < 0 {
		t.Fatalf("spawn.js does not append list before add controls")
	}
}

func TestWeb_WorkspaceSpawn_DoesNotSubmitPlaceholderDefaults(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn?dir="+url.QueryEscape(dir), nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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

// TestWeb_WorkspaceSpawn_PrefillsPromptFromQuery verifies that ?prompt=<text>
// on /_partials/workspace/spawn (and the /new wrapper that forwards it) reaches the
// rendered textarea. The palette's /spawn command relies on this.
func TestWeb_WorkspaceSpawn_PrefillsPromptFromQuery(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn?prompt="+url.QueryEscape("do the thing"), nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// The textarea is name="prompt" and the content sits between the open/close tags.
	want := `name="prompt" placeholder="describe the prompt…" autofocus>do the thing</textarea>`
	if !strings.Contains(body, want) {
		t.Fatalf("spawn form missing prefilled prompt %q:\n%s", want, body)
	}
}

// TestWeb_Index_NewRouteForwardsPromptToWorkspace verifies that /new?prompt=<text>
// renders the app shell wired to /_partials/workspace/spawn?prompt=<text> so the textarea
// pre-fill kicks in once the workspace partial loads.
func TestWeb_Index_NewRouteForwardsPromptToWorkspace(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/new?prompt="+url.QueryEscape("hello world"), nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// html/template escapes "+" → "&#43;" inside attribute values.
	want := `/_partials/workspace/spawn?prompt=hello&#43;world`
	if !strings.Contains(body, want) {
		t.Fatalf("app shell missing forwarded ?prompt in workspace url %q:\n%s", want, body)
	}
}

// TestWeb_SessionImage_ServesShaReferencedInputImage verifies that USER_INPUT
// image bytes can be fetched lazily via /s/<id>/images/<sha>.
func TestWeb_SessionImage_ServesShaReferencedInputImage(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01IMG", UpdatedAt: time.Now(), OriginalPrompt: "image demo",
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

type scriptedAppSource struct {
	id            string
	thread        appwire.Thread
	notifications []appwire.Notification
	startTurn     func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
}

func (s *scriptedAppSource) ID() string { return s.id }

func (s *scriptedAppSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *scriptedAppSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
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

func (s *scriptedAppSource) SteerTurn(context.Context, appwire.TurnSteerParams) error {
	return appwire.Unavailable("scripted source does not steer turns")
}

func (s *scriptedAppSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) error {
	return appwire.Unavailable("scripted source does not interrupt turns")
}

func (s *scriptedAppSource) QueueTurn(context.Context, appwire.TurnQueueParams) error {
	return appwire.Unavailable("scripted source does not queue turns")
}

func (s *scriptedAppSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) error {
	return appwire.Unavailable("scripted source does not drain as steer")
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

func (s *scriptedAppSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("scripted source does not clear threads")
}

func (s *scriptedAppSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, appwire.Unavailable("scripted source does not list models")
}

func (s *scriptedAppSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("scripted source does not list tasks")
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

type fakeSpawner struct{}

func (fakeSpawner) Spawn(_ context.Context, _ SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Resume(_ context.Context, _ ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}

type fakeWorkingDirModelContractSpawner struct {
	fakeSpawner
	fallback              appwire.ModelListResponse
	contractForWorkingDir func(context.Context, string) (appwire.ModelListResponse, error)
}

func (f *fakeWorkingDirModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	return f.fallback, nil
}

func (f *fakeWorkingDirModelContractSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, cwd string) (appwire.ModelListResponse, error) {
	if f.contractForWorkingDir == nil {
		return appwire.ModelListResponse{}, nil
	}
	return f.contractForWorkingDir(ctx, cwd)
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
		Model:           "openai/gpt-5",
		WorkingDir:      workDir,
		Agent:           "reviewer",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if resp.Ref != "local:01SLOWSPAWN" || resp.SessionID != "01SLOWSPAWN" {
		t.Fatalf("spawn response=%+v", resp)
	}
	if spawner.got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5", spawner.got.Resolved.Effective.Model)
	}
	wantWorkingDir, err := canonicalizeDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if spawner.got.WorkingDir != wantWorkingDir {
		t.Fatalf("working_dir=%q, want %q", spawner.got.WorkingDir, wantWorkingDir)
	}
	if spawner.got.Resolved.Effective.Agent != "reviewer" {
		t.Fatalf("agent=%q, want reviewer", spawner.got.Resolved.Effective.Agent)
	}
	if spawner.got.Resolved.Effective.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort=%q, want high", spawner.got.Resolved.Effective.ReasoningEffort)
	}
}

func TestWeb_ApiSpawn_HarnessRoutesToConfiguredCodexSource(t *testing.T) {
	workDir := t.TempDir()
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var gotStart map[string]any
	var gotTurnStart map[string]any
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		gotStart = params
		return map[string]any{"thread": map[string]any{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "codex task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     100,
			"status":        map[string]any{"type": "idle"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		gotTurnStart = params
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	body := strings.NewReader(fmt.Sprintf(`{"harness":"codex","prompt":"hello codex","model":"gpt-5.1-codex","working_dir":%q}`, workDir))
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotStart["modelProvider"] != nil || gotStart["model"] != "gpt-5.1-codex" {
		t.Fatalf("codex start params=%+v", gotStart)
	}
	input, ok := gotTurnStart["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("codex turn input=%#v", gotTurnStart["input"])
	}
	text, ok := input[0].(map[string]any)
	if !ok || text["type"] != "text" || text["text"] != "hello codex" {
		t.Fatalf("codex turn text input=%#v", input[0])
	}
	var resp hubapi.SpawnResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Ref != "codex:th_codex" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestWeb_ApiSpawn_CodexSourcePassesRemoteWorkingDirThrough(t *testing.T) {
	remoteDir := "/remote/codex/workspace/not-on-hub"
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var gotStart map[string]any
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		gotStart = params
		return map[string]any{"thread": map[string]any{
			"id":        "th_codex",
			"sessionId": "th_codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	body := strings.NewReader(fmt.Sprintf(`{"harness":"codex","prompt":"hello codex","model":"gpt-5.1-codex","working_dir":%q}`, remoteDir))
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotStart["cwd"] != remoteDir {
		t.Fatalf("codex cwd=%q, want %q (params=%+v)", gotStart["cwd"], remoteDir, gotStart)
	}
}

func TestWeb_ApiSpawn_AllowsBlankCodexPromptWithoutTurnStart(t *testing.T) {
	workDir := t.TempDir()
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var startCalled bool
	var turnCalled bool
	appserver.HandleTyped(codex.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		startCalled = true
		return map[string]any{"thread": map[string]any{
			"id":        "th_blank",
			"sessionId": "th_blank",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(codex.Router(), appwire.MethodTurnStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		turnCalled = true
		return nil, errors.New("blank prompt should not start a turn")
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex",
			Endpoint: "ws" + strings.TrimPrefix(codexHTTP.URL, "http"),
		}},
	})
	body := strings.NewReader(fmt.Sprintf(`{"harness":"codex","prompt":"   ","model":"gpt-5.1-codex","working_dir":%q}`, workDir))
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out hubapi.SpawnResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%q", err, rec.Body.String())
	}
	if out.Ref != "codex:th_blank" {
		t.Fatalf("spawn response=%+v", out)
	}
	if !startCalled {
		t.Fatal("Codex source was not started for blank prompt")
	}
	if turnCalled {
		t.Fatal("Codex turn was started for blank prompt")
	}
}

func TestSpawnRequestJSONAcceptsPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "prompt", body: `{"prompt":"hello prompt"}`, want: "hello prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got spawnRequest
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Prompt != tc.want {
				t.Fatalf("Prompt=%q, want %q", got.Prompt, tc.want)
			}
		})
	}
}

func TestWeb_ApiSpawn_RejectsBareModel(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := strings.NewReader(`{"model":"gpt-5","working_dir":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "provider/model") {
		t.Fatalf("body=%q, want provider/model guidance", rec.Body.String())
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
	body := strings.NewReader(`{"prompt":"do something"}`)
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

func TestWeb_ApiSpawn_RejectsOversizeRequest(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", strings.NewReader(`{"prompt":"`+strings.Repeat("x", sendMaxRequestBytes)+`"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestWeb_ApiSpawn_RejectsOversizeItem(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := spawnRequest{
		Prompt: "look",
		Items: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      make([]byte, sendMaxImageBytes+1),
			Name:      "big.png",
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", bytes.NewReader(payload))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d, want 413; body=%q", rec.Code, rec.Body.String())
	}
}

func TestWeb_ApiSpawn_CodexLaunchFailureReturnsStructuredDiagnostic(t *testing.T) {
	cfg := CodexLaunchConfig{
		ID:     "codex-broken",
		Binary: "/tmp/serf-no-such-codex-binary",
		Listen: "ws://127.0.0.1:0",
	}
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{cfg},
		CodexLauncher: NewCodexLauncher([]CodexLaunchConfig{cfg}),
	})
	body := strings.NewReader(`{"harness":"codex-broken","working_dir":"/tmp","prompt":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d, want 503 body=%q", rec.Code, rec.Body.String())
	}
	var out hubapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%q", err, rec.Body.String())
	}
	if out.Code != appwire.CodeUnavailable || out.SerfErrorInfo != string(appwire.ErrorHubLaunch) || !strings.Contains(out.Error, "start codex app-server") {
		t.Fatalf("error response=%+v", out)
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
	if !strings.Contains(body, `hx-get="/_partials/s/anysession/workspace"`) {
		t.Errorf("full-page /s/<id> missing internal workspace partial URL")
	}
}

func TestWeb_SessionRoute_LocalRefCanonicalizesWorkspaceURL(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/s/local:01LOCAL", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/_partials/s/01LOCAL/workspace"`) {
		t.Fatalf("local ref route did not canonicalize workspace partial URL:\n%s", body)
	}
}

// TestWeb_WorkspacePartial_LiveSession_RendersHeader verifies that the internal
// session workspace partial renders the session title and status.
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01LIVE001/workspace", nil)
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
	for _, unavailable := range []string{`data-action-trigger="interrupt"`, `data-action-trigger="compact"`, `data-action-trigger="shutdown"`, `data-steer-trigger`, `data-model-trigger`} {
		if strings.Contains(body, unavailable) {
			t.Errorf("serf live workspace advertised unavailable control %q:\n%s", unavailable, body)
		}
	}
}

func TestWeb_WorkspacePartial_LocalRefCanonicalizesToLiveSession(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 10, Address: "127.0.0.1:55556"})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVE001", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/local:01LIVE001/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "01LIVE001") {
		t.Fatalf("body missing canonical local session id: %q", body)
	}
}

// TestWeb_WorkspacePartial_PastSession_RendersTitleAndState verifies that a past session
// renders via the workspace partial with its OriginalPrompt and state="ended".
func TestWeb_WorkspacePartial_PastSession_RendersTitleAndState(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PAST001", UpdatedAt: time.Now(), OriginalPrompt: "fix the widget", TurnCount: 7,
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01PAST001/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fix the widget") {
		t.Errorf("body missing OriginalPrompt 'fix the widget': %q", body)
	}
	if !strings.Contains(body, "ended") {
		t.Errorf("body missing state 'ended': %q", body)
	}
}

// TestWeb_WorkspacePartial_RendersBottomStripAffordances verifies that the
// workspace partial includes the bottom strip elements
// (attach button, drop zone, mode chip, three composer zones, status row).
func TestWeb_WorkspacePartial_RendersBottomStripAffordances(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01BOTTOM01", UpdatedAt: time.Now(), OriginalPrompt: "render bottom strip",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01BOTTOM01/workspace", nil)
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
		"controls-left",
		"controls-center",
		"controls-right",
		"input-status",
		"data-file-picker",
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
		ID: "01CWD00001", UpdatedAt: time.Now(), OriginalPrompt: "cwd test",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01CWD00001/workspace", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01STATE001/state", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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

func TestFormatContextNumbersShowsUsedWindowAndRemaining(t *testing.T) {
	got := formatContextNumbers(42000, 100000, 58000)
	want := "42k / 100k tokens (58k left)"
	if got != want {
		t.Fatalf("formatContextNumbers() = %q, want %q", got, want)
	}
}

func TestWorkspaceDataUsesDaemonStatusTurnCountForLiveLocalSession(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id":  "01TURNCOUNT",
			"state":       "active",
			"turns":       37,
			"model":       "gpt-5",
			"working_dir": "/tmp/turns",
		})
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	roster := &Roster{bySess: map[string]LiveEntry{
		"01TURNCOUNT": {
			Entry:     rendezvous.Entry{Address: addr, SessionID: "01TURNCOUNT", Model: "gpt-5", WorkingDir: "/tmp/turns"},
			SessionID: "01TURNCOUNT",
			Status:    "active",
		},
	}}
	web := NewWebServer(WebConfig{Roster: roster})

	got := web.workspaceData("01TURNCOUNT")
	if got.TurnCount != 37 {
		t.Fatalf("TurnCount = %d, want daemon /status turns 37", got.TurnCount)
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

func TestWeb_SendLiveStartTurnErrorDoesNotResume(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Serf:      appwire.SerfThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, appwire.InternalError("model provider exploded")
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		daemon.ServeWebSocket(w, r)
	}))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:        302,
		Address:    strings.TrimPrefix(daemonHTTP.URL, "http://"),
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + daemonHTTP.URL[len("http"):] + "/rpc",
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp/project",
		Model:      "gpt-5",
		StartedAt:  time.Now(),
	})
	roster := NewRoster(runDir, fakeProber{sessionID: sessionID, status: "idle"})
	roster.Refresh()
	resumeCalled := false
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  runDir,
		Roster:  roster,
		Spawner: &fakeRPCSpawner{
			resume: func(context.Context, ResumeRequest) (rendezvous.Entry, error) {
				resumeCalled = true
				return rendezvous.Entry{}, errors.New("resume should not be called")
			},
		},
		Past: past,
	})

	req := httptest.NewRequest(http.MethodPost, "/s/"+sessionID+"/send", strings.NewReader(`{"text":"resume work"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if resumeCalled {
		t.Fatal("resume was called for a live StartTurn error")
	}
	if !strings.Contains(rec.Body.String(), "model provider exploded") {
		t.Fatalf("body=%q, want original StartTurn error", rec.Body.String())
	}
}

func TestWeb_Send_EndedRosterEntryResumesForwardsAndKeepsReplay(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "past")
	sessionID := buildRPCParentSession(t, stateDir)
	past := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	var gotPrompt string
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		gotPrompt = inputTextForTest(params.Input)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		daemon.ServeWebSocket(w, r)
	}))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:        300,
		Address:    "127.0.0.1:1",
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws://127.0.0.1:1/rpc",
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp/project",
		Model:      "gpt-5",
		StartedAt:  time.Now().Add(-time.Hour),
	})
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID {
				t.Fatalf("resume session=%q, want %q", req.SessionID, sessionID)
			}
			if req.StateDir != stateDir || req.WorkingDir != "/tmp/project" {
				t.Fatalf("resume request=%+v", req)
			}
			if req.Resolved.Effective.Model != "openai/gpt-5" {
				t.Fatalf("resume model=%q, want openai/gpt-5", req.Resolved.Effective.Model)
			}
			entry := rendezvous.Entry{
				PID:        301,
				Address:    strings.TrimPrefix(daemonHTTP.URL, "http://"),
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):] + "/rpc",
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: "/tmp/project",
				Model:      "gpt-5",
				StartedAt:  time.Now(),
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusClosed})
	roster.Refresh()
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  runDir,
		Roster:  roster,
		Spawner: spawner,
		Past:    past,
	})

	req := httptest.NewRequest(http.MethodPost, "/s/"+sessionID+"/send", strings.NewReader(`{"text":"resume work"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%q", rec.Code, rec.Body.String())
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q, want resume work", gotPrompt)
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
		ID: parentID, UpdatedAt: time.Now(), OriginalPrompt: "test fork",
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
		ID: "01MATCH", UpdatedAt: time.Now(), OriginalPrompt: "fix the frobnitz",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	})
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01OTHER", UpdatedAt: time.Now(), OriginalPrompt: "unrelated work",
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
	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(resp.Past) != 1 {
		t.Fatalf("past results = %d, want 1: %q", len(resp.Past), rec.Body.String())
	}
	if resp.Past[0].Title != "01MATCH" {
		t.Fatalf("past title = %q, want compact ID without original prompt", resp.Past[0].Title)
	}
}

func TestWeb_ApiSearch_PastUsesGeneratedNameTitle(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	_ = agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID:             "01MATCH",
		UpdatedAt:      time.Now(),
		Name:           "Generated Frobnitz Title",
		OriginalPrompt: "unrelated original prompt",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/projects/alpha"},
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
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=generated", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(resp.Past) != 1 {
		t.Fatalf("past results = %d, want 1: %q", len(resp.Past), rec.Body.String())
	}
	if resp.Past[0].Title != "Generated Frobnitz Title" {
		t.Fatalf("past title = %q, want generated name", resp.Past[0].Title)
	}
}

func TestWeb_ApiSearch_OrdersLiveResultsByStartedAtAndID(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	r := NewRoster(t.TempDir(), nil)
	r.byPID = map[int]LiveEntry{
		2: {
			Entry:     rendezvous.Entry{PID: 2, StartedAt: base.Add(-time.Hour), WorkingDir: "/projects/serf"},
			SessionID: "02LIVEOLD",
			Status:    appwire.ThreadStatusIdle,
		},
		1: {
			Entry:     rendezvous.Entry{PID: 1, StartedAt: base, WorkingDir: "/projects/serf"},
			SessionID: "01LIVENEW",
			Status:    appwire.ThreadStatusIdle,
		},
		4: {
			Entry:     rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/serf"},
			SessionID: "04LIVETIEB",
			Status:    appwire.ThreadStatusIdle,
		},
		3: {
			Entry:     rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/serf"},
			SessionID: "03LIVETIEA",
			Status:    appwire.ThreadStatusIdle,
		},
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=live", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotIDs := make([]string, 0, len(got.Live))
	for _, result := range got.Live {
		gotIDs = append(gotIDs, result.ID)
	}
	want := []string{"01LIVENEW", "02LIVEOLD", "03LIVETIEA", "04LIVETIEB"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", gotIDs, want)
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/theme", nil)
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

func disableLiveOllamaForModelTest(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_BASE_URL", srv.URL+"/v1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OLLAMA_API_KEY", "")
}

func disableStoredOpenAIAuthForModelTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestWeb_ApiModels_ReturnsSerfLaunchContractWhenLiveUnavailable(t *testing.T) {
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
	disableLiveOllamaForModelTest(t)

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCSpawner{
			launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
				return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
			},
		},
		Models: []modelDescriptor{{Provider: "openai", Model: "gpt-stale"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models: got %d, want 1; body=%s", len(models), rec.Body.String())
	}
	if models[0].Provider != "openai" || models[0].Model != "gpt-5.5" {
		t.Fatalf("model mismatch: %+v", models[0])
	}
}

func TestWeb_ApiModels_UsesWorkingDirForSerfLaunchContract(t *testing.T) {
	spawner := &fakeWorkingDirModelContractSpawner{
		fallback: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "stale", Model: "wrong"}},
		},
		contractForWorkingDir: func(_ context.Context, cwd string) (appwire.ModelListResponse, error) {
			if cwd != "/tmp/project-with-oauth" {
				return appwire.ModelListResponse{}, fmt.Errorf("cwd=%q, want /tmp/project-with-oauth", cwd)
			}
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-visible-to-agent"}},
			}, nil
		},
	}

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: spawner,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models?cwd=/tmp/project-with-oauth", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 1 || models[0].Provider != "openai" || models[0].Model != "gpt-visible-to-agent" {
		t.Fatalf("models=%+v", models)
	}
}

func TestWeb_ApiModels_DoesNotUseLiveProvidersWhenLaunchContractIsEmpty(t *testing.T) {
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
	disableLiveOllamaForModelTest(t)
	disableStoredOpenAIAuthForModelTest(t)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"deepseek/deepseek-chat"}]}`)) //nolint:errcheck
	}))
	defer live.Close()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENROUTER_BASE_URL", live.URL+"/v1")

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCModelContractSpawner{
			contract: appwire.ModelListResponse{},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models=%+v", models)
	}
}

func TestWeb_ApiModels_RoutesCodexHarnessToSource(t *testing.T) {
	codex := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex-local"})
	var gotParams appwire.ModelListParams
	appserver.HandleTyped(codex.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		gotParams = params
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
	})
	codexHTTP := httptest.NewServer(http.HandlerFunc(codex.ServeWebSocket))
	defer codexHTTP.Close()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-local",
			Endpoint: "ws" + codexHTTP.URL[len("http"):],
		}},
		Spawner: &fakeRPCModelContractSpawner{
			contract: appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}},
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models?harness=codex-local", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if gotParams.Harness != "" {
		t.Fatalf("codex source received hub harness routing field: %+v", gotParams)
	}
	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 1 || models[0].Provider != "codex-local" || models[0].Model != "gpt-5.3-codex" {
		t.Fatalf("models=%+v", models)
	}
}

func TestWeb_ApiModels_ReturnsLaunchErrorWhenLaunchModelListerFails(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCModelContractSpawner{
			err: errors.New("serf launch-check returned invalid response"),
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid response") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestWeb_SettingsProvidersShowsLaunchModelDiagnostics(t *testing.T) {
	// The providers tab is now JS-driven via launchconfig.authList(); provider
	// data is no longer server-rendered. Verify the page loads and wires up the
	// JS hook correctly.
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCSpawner{},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/providers", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"providers-rows", "launchconfig.authList"} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings providers missing %q:\n%s", want, body)
		}
	}
}

func TestWeb_SettingsProvidersShowsLaunchModelErrorDiagnostic(t *testing.T) {
	// The providers tab is now JS-driven via launchconfig.authList(); error
	// diagnostics are surfaced at runtime by the JS, not server-rendered.
	// Verify the page still returns 200 and includes the JS entry point.
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCModelContractSpawner{
			err: errors.New("serf launch-check returned invalid response"),
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/providers", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "launchconfig.authList") {
		t.Fatalf("settings providers missing JS hook:\n%s", body)
	}
}

func TestWeb_ApiModels_FiltersOpenRouterLiveModelsToToolCapable(t *testing.T) {
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
	disableLiveOllamaForModelTest(t)
	disableStoredOpenAIAuthForModelTest(t)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"deepseek/deepseek-chat"},{"id":"morph/morph-v3-fast"},{"id":"unknown/no-tools"}]}`)) //nolint:errcheck
	}))
	defer live.Close()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENROUTER_BASE_URL", live.URL+"/v1")

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models: got %d, want 1; body=%s", len(models), rec.Body.String())
	}
	if models[0].Provider != "openrouter" || models[0].Model != "deepseek/deepseek-chat" {
		t.Fatalf("model mismatch: %+v", models[0])
	}
	if !models[0].SupportsTools {
		t.Fatalf("model should be marked tool-capable: %+v", models[0])
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
	disableLiveOllamaForModelTest(t)
	disableStoredOpenAIAuthForModelTest(t)

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

// TestWeb_Settings_Providers_RendersSerfLaunchContract checks that
// GET /settings/providers returns 200 and includes the JS entry point that
// fetches auth/list at runtime (provider data is no longer server-rendered).
func TestWeb_Settings_Providers_RendersSerfLaunchContract(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
		Spawner: &fakeRPCSpawner{
			launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
				return []appwire.ModelDescriptor{
					{Provider: "anthropic", Model: "claude-sonnet-4-6"},
					{Provider: "anthropic", Model: "claude-opus-4-7"},
					{Provider: "openai", Model: "gpt-5.5"},
				}, nil
			},
		},
		Models: []modelDescriptor{{Provider: "openai", Model: "gpt-stale"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/providers", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"providers-rows", "launchconfig.authList"} {
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
		ID: "01PASTTASK", UpdatedAt: time.Now(), OriginalPrompt: "demo",
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

	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01PASTTASK/tasks", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01NOTASKS/tasks", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01LIVETASK", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{Data: []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "live task", Status: agent.TaskInProgress}}}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(dir, fakeProber{sessionID: "01LIVETASK", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01LIVETASK/tasks", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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

func startAppwireTestDaemon(t *testing.T, dir, sessionID string, register func(*appserver.Server)) *httptest.Server {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		ref := params.Ref
		if ref == "" {
			ref = "local:" + sessionID
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:            sessionID,
			SessionID:     sessionID,
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			ModelProvider: "gpt-5",
			CWD:           "/tmp",
			Serf: appwire.SerfThread{
				Ref: ref,
				Capabilities: appwire.ThreadCapabilities{
					Send:        true,
					Steer:       true,
					Interrupt:   true,
					Compact:     true,
					Clear:       true,
					Shutdown:    true,
					ChangeModel: true,
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
		PID:        200,
		Address:    strings.TrimPrefix(srv.URL, "http://"),
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + srv.URL[len("http"):] + "/rpc",
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Model:      "gpt-5",
	})
	return srv
}

// TestWeb_Send_ForwardsTextAndImages verifies POST /s/<id>/send with both text
// and an image attachment forwards a JSON body matching server.InputRequest's
// schema to the daemon's /input.
func TestWeb_Send_ForwardsTextAndImages(t *testing.T) {
	var got appwire.TurnStartParams
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01SENDIMG", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
		})
	})
	defer daemon.Close()
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
		"items": []map[string]any{{
			"type":      "image",
			"mediaType": "image/png",
			"data":      imgBytes,
			"name":      "x.png",
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
	if inputTextForTest(got.Input) != "caption" {
		t.Errorf("prompt=%q, want %q", inputTextForTest(got.Input), "caption")
	}
	items := imageInputItems(got.Input)
	if len(items) != 1 {
		t.Fatalf("items=%d, want 1", len(items))
	}
	if items[0].MediaType != "image/png" {
		t.Errorf("media_type=%q, want image/png", items[0].MediaType)
	}
	if items[0].Name != "x.png" {
		t.Errorf("name=%q, want x.png", items[0].Name)
	}
	if len(items[0].Data) != len(imgBytes) {
		t.Errorf("image data len=%d, want %d", len(items[0].Data), len(imgBytes))
	}
}

// TestWeb_Send_ImageOnly_Forwards verifies that a send with empty text and one
// image is accepted and forwarded.
func TestWeb_Send_ImageOnly_Forwards(t *testing.T) {
	var got appwire.TurnStartParams
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01IMGONLY", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
		})
	})
	defer daemon.Close()
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
		"items": []map[string]any{{
			"type":      "image",
			"mediaType": "image/jpeg",
			"data":      imgBytes,
			"name":      "y.jpg",
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
	if inputTextForTest(got.Input) != "" {
		t.Errorf("prompt=%q, want empty", inputTextForTest(got.Input))
	}
	items := imageInputItems(got.Input)
	if len(items) != 1 {
		t.Fatalf("items=%d, want 1", len(items))
	}
	if items[0].MediaType != "image/jpeg" {
		t.Errorf("media_type=%q, want image/jpeg", items[0].MediaType)
	}
	if items[0].Name != "y.jpg" {
		t.Errorf("name=%q, want y.jpg", items[0].Name)
	}
	if len(items[0].Data) != len(imgBytes) {
		t.Errorf("image data len=%d, want %d", len(items[0].Data), len(imgBytes))
	}
}

// TestWeb_Send_RejectsEmptyTextAndNoItems verifies that the hub returns 400
// when neither text nor input items are supplied, matching the daemon's rule.
func TestWeb_Send_RejectsEmptyTextAndNoItems(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 23, Address: "127.0.0.1:55557"})
	r := NewRoster(dir, fakeProber{sessionID: "01NOEMPTY", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})

	cases := []string{`{}`, `{"text":""}`, `{"text":"","items":[]}`}
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
// rejects image input items larger than sendMaxImageBytes with 413, before forwarding
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
		Items: []appwire.InputItem{{
			Type: "image", MediaType: "image/png", Data: bigData, Name: "big.png",
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
// renders with data-state on the .sb-row anchor itself, so the CSS state
// accents (left border + tinted background) can apply.
func TestWeb_Sidebar_LiveRowDataState(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 30, Address: "127.0.0.1:55570"})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVEACC", status: appwire.ThreadStatusAwaiting})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// The sb-row anchor in the Live section must carry data-state="awaiting"
	// so the state-accent CSS rules match. The template line-wraps the anchor,
	// so flatten whitespace before looking for the two attributes adjacent on
	// the same element.
	if !strings.Contains(body, "sb-row") {
		t.Fatalf("body missing sb-row class: %q", body)
	}
	flat := strings.Join(strings.Fields(body), " ")
	if !strings.Contains(flat, `sb-row`) || !strings.Contains(flat, `data-state="awaiting"`) {
		t.Errorf("sb-row missing data-state=\"awaiting\": %q", body)
	}
	// And confirm they're on the same opening tag: find <a ... > containing both.
	tagFound := false
	for _, chunk := range strings.Split(flat, "<a ") {
		// The first split chunk is everything before the first <a; subsequent
		// chunks each begin with the anchor's attribute list.
		if !strings.HasPrefix(chunk, `class="sb-row`) {
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
		t.Errorf("data-state=\"awaiting\" not on the sb-row <a> element: %q", body)
	}
}

// TestWeb_Sidebar_ProjectHeader_HasChevronAndFolder verifies that the project
// header renders with the compact collapsed ▸ 📁 <name> shape.
func TestWeb_Sidebar_ProjectHeader_HasChevronAndFolder(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01PROJHDR", UpdatedAt: time.Now(), OriginalPrompt: "x",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	wants := []string{`class="project-header"`, "project-chevron", "▸", "📁", "project-name"}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("project-header missing %q: %q", w, body)
		}
	}
}

func TestWeb_WorkspacePartial_RosterEndedSessionKeepsResumeSendEnabled(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01ENDED001", UpdatedAt: time.Now(), OriginalPrompt: "resume this", TurnCount: 2,
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 10, Address: "127.0.0.1:55556", WorkingDir: "/projects/serf"})
	r := NewRoster(runDir, fakeProber{sessionID: "01ENDED001", status: appwire.ThreadStatusClosed})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    idx,
		Spawner: &fakeSpawner{},
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01ENDED001/workspace", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-capability-send="true"`) {
		t.Fatalf("ended resumable workspace did not enable send:\n%s", body)
	}
	if strings.Contains(body, `disabled title="send unavailable"`) {
		t.Fatalf("ended resumable workspace rendered disabled send:\n%s", body)
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
		OriginalPrompt: "the original prompt",
		ForkLabel:      "before TDD",
		DivergenceTurn: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// New branch — its ParentSessionID points back at the original.
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01NEWBRANCH", UpdatedAt: time.Now(),
		OriginalPrompt:  "the new branch title",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/s/01ORIGINAL/workspace", nil)
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
	var called bool
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01ACTINT", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, func(context.Context, appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
			called = true
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(dir, fakeProber{sessionID: "01ACTINT", status: appwire.ThreadStatusActive})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTINT/interrupt", strings.NewReader(`{"turn_id":"turn_1"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("interrupt appwire handler was not called")
	}
}

func TestWeb_SessionAction_CompactForwards(t *testing.T) {
	var called bool
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01ACTCMP", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, func(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
			called = true
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(dir, fakeProber{sessionID: "01ACTCMP", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTCMP/compact", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("compact appwire handler was not called")
	}
}

func TestWeb_SessionAction_ShutdownForwards(t *testing.T) {
	var called bool
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01ACTSHD", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadShutdown, func(context.Context, appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
			called = true
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(dir, fakeProber{sessionID: "01ACTSHD", status: "idle"})
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/01ACTSHD/shutdown", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("shutdown appwire handler was not called")
	}
}

func TestWeb_SessionAction_ClearForwards(t *testing.T) {
	var called bool
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01ACTCLR", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			called = true
			return appwire.ThreadClearResponse{Ref: "local:01ACTCLR"}, nil
		})
	})
	defer daemon.Close()
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
	if !called {
		t.Error("clear appwire handler was not called")
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
	var got appwire.TurnSteerParams
	dir := t.TempDir()
	daemon := startAppwireTestDaemon(t, dir, "01STEER", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(dir, fakeProber{sessionID: "01STEER", status: appwire.ThreadStatusActive})
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
	if got.Ref != "local:01STEER" || inputTextForTest(got.Input) != "stop using mocks" {
		t.Errorf("steer params=%+v", got)
	}
}

// TestWeb_Steer_RejectsEmptyText verifies that empty text returns 400
// without forwarding to the daemon.
func TestWeb_Steer_RejectsEmptyText(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 34, Address: "127.0.0.1:1"})
	r := NewRoster(dir, fakeProber{sessionID: "01STEEREMPTY", status: appwire.ThreadStatusActive})
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

func (p perAddrProber) Probe(entry rendezvous.Entry) (sessionID, status string, ok bool) {
	v, present := p.byAddr[entry.Address]
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
		ID: "01AWAIT", UpdatedAt: time.Now(), OriginalPrompt: "needs reply",
		EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01IDLE", UpdatedAt: time.Now(), OriginalPrompt: "ticking over",
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
		"127.0.0.1:1001": {SessionID: "01AWAIT", Status: appwire.ThreadStatusAwaiting},
		"127.0.0.1:1002": {SessionID: "01IDLE", Status: appwire.ThreadStatusIdle},
	}}
	r := NewRoster(rDir, prober)
	r.Refresh()

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
		ID: "01PAST", UpdatedAt: time.Now(), OriginalPrompt: "done long ago",
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/"+section, nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestLaunchSerfSettings_UsesSchemaRoot(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	globalBody := settingsRequest(t, web, "launch-serf")
	for _, want := range []string{
		`data-launch-settings-root`,
		`data-launch-settings-layer="global"`,
		`data-launch-settings-groups`,
		`launchconfig.schema()`,
		`LaunchConfigControls.render`,
		`includeEnvFallbacks: false`,
		`launchconfig.setLayer("/", "global"`,
	} {
		if !strings.Contains(globalBody, want) {
			t.Fatalf("global launch settings missing %q: %q", want, globalBody)
		}
	}
	for _, blocked := range []string{`data-launch-env-fallback`, `SERF_MODEL`, `SERF_REASONING_EFFORT`} {
		if strings.Contains(globalBody, blocked) {
			t.Fatalf("global launch settings exposed env fallback %q: %q", blocked, globalBody)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/project?cwd=/tmp/project", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("project status: %d body=%q", rec.Code, rec.Body.String())
	}
	projectBody := rec.Body.String()
	for _, want := range []string{
		`data-launch-settings-root`,
		`data-launch-settings-layer="project"`,
		`data-launch-settings-groups`,
		`launchconfig.schema()`,
		`LaunchConfigControls.render`,
		`includeEnvFallbacks: false`,
		`launchconfig.setLayer(cwd, "project"`,
	} {
		if !strings.Contains(projectBody, want) {
			t.Fatalf("project launch settings missing %q: %q", want, projectBody)
		}
	}
	for _, blocked := range []string{`data-launch-env-fallback`, `SERF_MODEL`, `SERF_REASONING_EFFORT`} {
		if strings.Contains(projectBody, blocked) {
			t.Fatalf("project launch settings exposed env fallback %q: %q", blocked, projectBody)
		}
	}
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

// TestWeb_Settings_PluginsPane_RendersClientScaffolding asserts that the
// plugins tab renders the client-side container and launchconfig script hook
// rather than SSR content. The actual list is populated by the browser.
func TestWeb_Settings_PluginsPane_RendersClientScaffolding(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := settingsRequest(t, web, "plugins")
	for _, want := range []string{"plugins-form", "launchconfig.getLayer", "pluginDirs"} {
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
	if !strings.Contains(body, "plugins-form") {
		t.Errorf("expected plugins-form container in body: %q", body)
	}
}

// TestWeb_Settings_SkillsPane_RendersClientScaffolding asserts that the
// skills tab renders the client-side container and launchconfig script hook
// rather than SSR content. The actual list is populated by the browser.
func TestWeb_Settings_SkillsPane_RendersClientScaffolding(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := settingsRequest(t, web, "skills")
	for _, want := range []string{"skills-form", "launchconfig.getLayer", "skillsDirs"} {
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
	if !strings.Contains(body, "skills-form") {
		t.Errorf("expected skills-form container in body: %q", body)
	}
}

// TestWeb_Settings_McpPane_RendersClientScaffolding asserts that the MCP tab
// renders the client-side container and launchconfig script hook rather than
// SSR content. The actual list is populated by the browser.
func TestWeb_Settings_McpPane_RendersClientScaffolding(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	body := settingsRequest(t, web, "mcp")
	for _, want := range []string{"mcps-form", "launchconfig.getLayer", "mcps-add"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
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
	if !strings.Contains(body, "mcps-form") {
		t.Errorf("expected mcps-form container in body: %q", body)
	}
}

func TestWeb_SettingsLaunchListPanesAvoidHTMLInterpolation(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	for _, section := range []string{"plugins", "skills", "mcp"} {
		body := settingsRequest(t, web, section)
		for _, unsafe := range []string{"${d}", "${m.name}", "${m.command}", "root.innerHTML = `"} {
			if strings.Contains(body, unsafe) {
				t.Fatalf("%s settings pane contains unsafe interpolation %q: %q", section, unsafe, body)
			}
		}
		if !strings.Contains(body, ".textContent") || !strings.Contains(body, "replaceChildren") {
			t.Fatalf("%s settings pane does not render dynamic values via DOM text nodes: %q", section, body)
		}
	}
}

func TestWeb_SettingsErrorPathsAvoidHTMLInterpolation(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/settings/project?cwd=/tmp/project", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("project status: %d body=%q", rec.Code, rec.Body.String())
	}
	bodies := map[string]string{
		"project": rec.Body.String(),
		"inrepo":  settingsRequest(t, web, "inrepo"),
	}
	for section, body := range bodies {
		if strings.Contains(body, "innerHTML = `<p class=\"settings-error\"") {
			t.Fatalf("%s settings pane interpolates errors into HTML: %q", section, body)
		}
		if !strings.Contains(body, ".textContent") || !strings.Contains(body, "replaceChildren") {
			t.Fatalf("%s settings pane does not render errors via DOM text nodes: %q", section, body)
		}
	}
}

// TestWeb_Settings_NavPresentForAllSections is a regression test for kata
// 3j2y: the settings shell (nav + header) must be included in the full-page
// response for plugins, skills, and mcp — not just for general/theme/etc.
// The full shell is rendered when HX-Target is anything other than
// "settings-content" (i.e. on initial workspace load or direct navigation).
func TestWeb_Settings_NavPresentForAllSections(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	for _, sec := range []string{"general", "plugins", "skills", "mcp", "theme", "hub"} {
		body := settingsRequest(t, web, sec)
		if !strings.Contains(body, "settings-nav") {
			t.Errorf("section %q: settings-nav missing from full-shell response", sec)
		}
		if !strings.Contains(body, "settings-content") {
			t.Errorf("section %q: settings-content missing from full-shell response", sec)
		}
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

func TestWeb_APIHealthReportsCodexLaunchSpawnCapability(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		CodexLaunches: []CodexLaunchConfig{{ID: "codex-managed"}},
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
	if !got.Capabilities.Spawn {
		t.Fatalf("spawn capability not reported for codex launches: %+v", got.Capabilities)
	}
}

func TestWeb_APITreeReturnsRefsAndNormalizesAwaitingInput(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01TREE", UpdatedAt: time.Now(), OriginalPrompt: "tree task",
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
	r := NewRoster(runDir, fakeProber{sessionID: "01TREE", status: appwire.ThreadStatusAwaiting})
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
		"127.0.0.1:4050": {SessionID: "01LIVEA", Status: appwire.ThreadStatusIdle},
		"127.0.0.1:4051": {SessionID: "01LIVEB", Status: appwire.ThreadStatusAwaiting},
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
	r := NewRoster(runDir, fakeProber{sessionID: "", status: appwire.ThreadStatusIdle})
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
	r := NewRoster(runDir, fakeProber{sessionID: "", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})

	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
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
		ID: "01DETAIL", UpdatedAt: time.Now(), OriginalPrompt: "details task", Model: "gpt-5", ProfileID: "openai", TurnCount: 3,
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
	r := NewRoster(runDir, fakeProber{sessionID: "01DETAIL", status: appwire.ThreadStatusIdle})
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
	if !got.Capabilities.Resume {
		t.Fatalf("missing resume capability: %+v", got.Capabilities)
	}
	if got.Capabilities.Send || got.Capabilities.Interrupt {
		t.Fatalf("advertised AppWire actions without a readable source: %+v", got.Capabilities)
	}
}

func TestWeb_APISessionDetailsLiveWithoutAppWireDoesNotAdvertiseActions(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01DETAIL", UpdatedAt: time.Now(), OriginalPrompt: "details task", Model: "gpt-5", ProfileID: "openai", TurnCount: 3,
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
	r := NewRoster(runDir, fakeProber{sessionID: "01DETAIL", status: appwire.ThreadStatusIdle})
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
	if !got.Live || !got.Capabilities.Resume {
		t.Fatalf("expected live past session with resume capability: %+v", got)
	}
	if got.Capabilities.Send || got.Capabilities.Steer || got.Capabilities.Interrupt || got.Capabilities.Compact || got.Capabilities.Clear || got.Capabilities.Shutdown || got.Capabilities.ChangeModel {
		t.Fatalf("live fallback advertised unavailable actions: %+v", got.Capabilities)
	}
}

func TestWeb_WorkspaceDataLocalLiveUsesAppWireCapabilities(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 64, Address: "127.0.0.1:6464", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, fakeProber{sessionID: "01CAPS", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:            "01CAPS",
			SessionID:     "01CAPS",
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			ModelProvider: "gpt-5",
			CWD:           "/projects/serf",
			Turns: []appwire.Turn{
				{ID: "turn_live", Status: appwire.TurnStatusInProgress},
			},
			Serf: appwire.SerfThread{
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

func TestWeb_APISessionDetailsLocalLiveUsesAppWireForkCapability(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 65, Address: "127.0.0.1:6565", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := NewRoster(runDir, fakeProber{sessionID: "01FORKCAP", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "01FORKCAP",
			SessionID: "01FORKCAP",
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			CWD:       "/projects/serf",
			Serf: appwire.SerfThread{
				Ref:          "local:01FORKCAP",
				Capabilities: appwire.ThreadCapabilities{Send: true, ForkFromTurn: true},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:01FORKCAP", nil)
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
	if !got.Capabilities.Fork {
		t.Fatalf("api session detail dropped fork capability: %+v", got.Capabilities)
	}
}

func TestWeb_ManagedCodexLiveWorkspaceCapabilitiesEnsureSource(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          NewPastIndex(""),
		CodexLaunches: []CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})

	caps := web.liveWorkspaceCapabilities("codex-managed:th_fake", hubapi.SessionCapabilities{})
	if !caps.Send {
		t.Fatalf("capabilities=%+v", caps)
	}
}

func TestWeb_APISpawnSchema(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		CodexSources: []appsource.CodexSourceConfig{{
			ID: "codex-local",
		}},
		CodexLaunches: []CodexLaunchConfig{{ID: "codex-managed"}},
	})
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
	for _, want := range []string{"prompt", "harness", "working_dir", "model", "agent", "reasoning_effort"} {
		if !names[want] {
			t.Fatalf("schema missing %q: %+v", want, got.Fields)
		}
	}
	harnessValues := []string{}
	for _, f := range got.Fields {
		if f.Name == "harness" {
			harnessValues = f.Values
		}
	}
	if len(harnessValues) != 3 || harnessValues[0] != "serf" || harnessValues[1] != "codex-local" || harnessValues[2] != "codex-managed" {
		t.Fatalf("harness values=%+v", harnessValues)
	}
	if names["branch"] || names["access_mode"] {
		t.Fatalf("schema exposes unsupported field: %+v", got.Fields)
	}
}

func TestWeb_APISessionActionClearReturnsRef(t *testing.T) {
	var clearParams appwire.ThreadClearParams
	runDir := t.TempDir()
	daemon := startAppwireTestDaemon(t, runDir, "01OLD", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(_ context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			clearParams = params
			return appwire.ThreadClearResponse{
				Ref: "local:01NEW",
				Thread: appwire.Thread{
					ID:        "01NEW",
					SessionID: "01NEW",
					Source:    "local",
					Serf:      appwire.SerfThread{Ref: "local:01NEW"},
				},
			}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(runDir, fakeProber{sessionID: "01OLD", status: appwire.ThreadStatusIdle})
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
	var clearResp hubapi.RefResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &clearResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if clearResp.Ref != "local:01NEW" || clearResp.SessionID != "01NEW" {
		t.Fatalf("unexpected clear response: %+v", clearResp)
	}
	if clearParams.Ref != "local:01OLD" {
		t.Fatalf("clear params ref=%q, want local:01OLD", clearParams.Ref)
	}
}

func TestWeb_APISessionActionModelForwardsBody(t *testing.T) {
	var got appwire.ThreadModelSetParams
	runDir := t.TempDir()
	daemon := startAppwireTestDaemon(t, runDir, "01MODEL", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := NewRoster(runDir, fakeProber{sessionID: "01MODEL", status: appwire.ThreadStatusIdle})
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
	if got.Ref != "local:01MODEL" || got.Model != "gpt-5.5" || got.ModelProvider != "" {
		t.Fatalf("model params=%+v", got)
	}
}
