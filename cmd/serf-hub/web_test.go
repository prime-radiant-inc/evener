package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/selfupdate"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
)

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

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	source := &scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID: "thread_abc", SessionID: "thread_abc", Name: "opaque external thread", Source: "codex",
			Serf: appwire.SerfThread{Ref: "codex:thread_abc"},
		},
	}
	web.sources.Add(source)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/codex:thread_abc", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("external source-specific session route status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(source.readParams) == 0 {
		t.Fatalf("external source received read params %+v", source.readParams)
	}
	for _, params := range source.readParams {
		if params.Ref != "codex:thread_abc" {
			t.Fatalf("external source received rewritten ref in read params %+v", source.readParams)
		}
	}
}

// controlTag returns the opening <button …> tag whose attributes contain the
// given marker (a stable hook like a data-* attribute). Assertions check a
// control's state — disabled, capability flags — off this tag instead of
// pinning the exact class list or inner markup, which churn with styling.
func controlTag(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i == -1 {
		t.Fatalf("control %q not found in:\n%s", marker, body)
	}
	start := strings.LastIndex(body[:i], "<button")
	if start == -1 {
		t.Fatalf("no <button> opening tag precedes %q", marker)
	}
	rel := strings.Index(body[start:], ">")
	if rel == -1 {
		t.Fatalf("unterminated <button> tag for %q", marker)
	}
	return body[start : start+rel+1]
}

func findElement(root *html.Node, matches func(*html.Node) bool) *html.Node {
	if root.Type == html.ElementNode && matches(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, matches); found != nil {
			return found
		}
	}
	return nil
}

func hasHTMLAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}
	return false
}

func hasHTMLClass(node *html.Node, class string) bool {
	for _, attr := range node.Attr {
		if attr.Key == "class" {
			for _, nodeClass := range strings.Fields(attr.Val) {
				if nodeClass == class {
					return true
				}
			}
		}
	}
	return false
}

func isHTMLDescendant(ancestor, node *html.Node) bool {
	for node = node.Parent; node != nil; node = node.Parent {
		if node == ancestor {
			return true
		}
	}
	return false
}

func requireHTMLDescendant(t *testing.T, ancestor, node *html.Node, description string) {
	t.Helper()
	if ancestor == nil || node == nil || !isHTMLDescendant(ancestor, node) {
		t.Fatalf("%s", description)
	}
}

// injectMetasForTest replaces the past index with one holding the given metas.
func (s *WebServer) injectMetasForTest(metas []schema.SessionMeta) {
	idx := hubcore.NewPastIndex("")
	idx.SeedForTest(metas)
	s.cfg.Past = idx
}

// allTreeProjects returns every project in a TreeResponse regardless of
// activity tier. Task 4 split active/archived projects into separate wire
// arrays (Projects/ArchivedProjects); tests that only care whether a session
// shows up somewhere in the tree — not which tier its project landed in —
// scan both. Archived projects arrive as session-less stubs in /api/tree, so
// this hydrates each one from /api/tree/project?key= exactly like the
// sidebar's lazy expand does.
func allTreeProjects(t *testing.T, web *WebServer, resp hubapi.TreeResponse) []hubapi.TreeProject {
	t.Helper()
	out := append([]hubapi.TreeProject(nil), resp.Projects...)
	for _, p := range resp.ArchivedProjects {
		req := httptest.NewRequest(http.MethodGet, "/api/tree/project?key="+url.QueryEscape(p.Key), nil)
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("hydrating archived project %q: status=%d body=%s", p.Key, rec.Code, rec.Body.String())
		}
		var full hubapi.TreeProject
		if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
			t.Fatalf("hydrating archived project %q: %v", p.Key, err)
		}
		out = append(out, full)
	}
	return out
}

func TestWeb_Landing_Renders(t *testing.T) {
	r := hubcore.NewRoster(t.TempDir(), nil)
	idx := hubcore.NewPastIndex("")
	web := NewWebServer(hubcore.WebConfig{
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

// The htmx history element must be #workspace, not the default <body>. A
// body-wide history snapshot includes every asset <script> tag, and htmx
// re-executes them on history restore (e.g. iOS swipe-back), double-binding
// all delegated handlers so each tap toggles twice — every button "dead".
func TestWeb_AppShellScopesHtmxHistoryToWorkspace(t *testing.T) {
	r := hubcore.NewRoster(t.TempDir(), nil)
	idx := hubcore.NewPastIndex("")
	web := NewWebServer(hubcore.WebConfig{
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
	start := strings.Index(body, `<main id="workspace"`)
	if start == -1 {
		t.Fatalf("body missing <main id=\"workspace\">")
	}
	tag := body[start : strings.Index(body[start:], ">")+start+1]
	if !strings.Contains(tag, "hx-history-elt") {
		t.Errorf("#workspace must carry hx-history-elt so history restores never re-run body scripts; got tag %q", tag)
	}
}

func TestWebAPIUpgradeRunsSelfUpdater(t *testing.T) {
	var got selfupdate.Options
	previous := runHubSelfUpgrade
	runHubSelfUpgrade = func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		got = opts
		return selfupdate.Result{
			Release:        "snapshot",
			Channel:        "snapshot",
			Archive:        "serf_linux_amd64.tar.gz",
			ShareBinDir:    "/tmp/share/serf/bin",
			BinDir:         "/tmp/bin",
			RestartMessage: "Restart serf-tui and serf-hub to use the upgraded binaries.",
		}, nil
	}
	t.Cleanup(func() { runHubSelfUpgrade = previous })

	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/api/upgrade", strings.NewReader(`{"requested":"snapshot"}`))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp appwire.UpgradeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Channel != "snapshot" || resp.Archive != "serf_linux_amd64.tar.gz" {
		t.Fatalf("response=%+v", resp)
	}
	if got.Requested != "snapshot" {
		t.Fatalf("Requested=%q, want snapshot", got.Requested)
	}
	if got.CurrentChannel == "" {
		t.Fatal("CurrentChannel is empty")
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

func TestActiveTurnIDFromAppwireThreadPrefersSerfActiveTurn(t *testing.T) {
	got := activeTurnIDFromAppwireThread(appwire.Thread{
		Serf: appwire.SerfThread{ActiveTurnID: "turn_live"},
		Turns: []appwire.Turn{
			{ID: "turn_transcript", Status: appwire.TurnStatusCompleted},
		},
	})
	if got != "turn_live" {
		t.Fatalf("active turn id=%q, want turn_live", got)
	}
}

// TestHubDetailFromAppThreadCarriesGoal asserts the thread's Serf.Goal status
// and turn count flow into SessionDetail's flattened goal fields, which feed
// the live input-strip goal pill.
func TestHubDetailFromAppThreadCarriesGoal(t *testing.T) {
	detail := hubDetailFromAppThread(appwire.Thread{
		ID:        "th_goal",
		SessionID: "th_goal",
		Status:    appwire.ThreadStatus{Type: "idle"},
		Source:    "local",
		Serf: appwire.SerfThread{
			Ref:  "local:th_goal",
			Goal: &appwire.GoalState{Status: "active", Iterations: 2},
		},
	})
	if detail.GoalStatus != "active" || detail.GoalIterations != 2 {
		t.Fatalf("goal not carried through: status=%q iterations=%d", detail.GoalStatus, detail.GoalIterations)
	}

	noGoal := hubDetailFromAppThread(appwire.Thread{
		ID:        "th_nogoal",
		SessionID: "th_nogoal",
		Status:    appwire.ThreadStatus{Type: "idle"},
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:th_nogoal"},
	})
	if noGoal.GoalStatus != "" || noGoal.GoalIterations != 0 {
		t.Fatalf("expected empty goal when none set: status=%q iterations=%d", noGoal.GoalStatus, noGoal.GoalIterations)
	}

	// The workspace partial passes a WorkspaceData (not the /state data map) to
	// the input_status template, so it must carry the goal too.
	wd := workspaceDataFromAppThread(appwire.Thread{
		ID:     "th_goal",
		Source: "local",
		Status: appwire.ThreadStatus{Type: "idle"},
		Serf: appwire.SerfThread{
			Ref:           "local:th_goal",
			Goal:          &appwire.GoalState{Status: "active", Iterations: 2},
			ContextUsed:   42000,
			ContextWindow: 100000,
		},
	})
	if wd.GoalStatus != "active" || wd.GoalIterations != 2 {
		t.Fatalf("workspace data dropped goal: status=%q iterations=%d", wd.GoalStatus, wd.GoalIterations)
	}
	if wd.CompactContextNumbers != "42k / 100k" {
		t.Fatalf("workspace data compact context = %q, want 42k / 100k", wd.CompactContextNumbers)
	}
}

func TestWeb_WorkspaceTaskStatusInitialIsNeutral(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "codex",
		thread: appwire.Thread{
			ID: "th_tasks", SessionID: "th_tasks", Source: "codex",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:   appwire.SerfThread{Ref: "codex:th_tasks", Capabilities: appwire.ThreadCapabilities{Send: true}},
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	for _, project := range allTreeProjects(t, web, got) {
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	for _, project := range allTreeProjects(t, web, got) {
		for i := range project.Sessions {
			if project.Sessions[i].Ref == "codex:th_codex_ended" {
				found = &project.Sessions[i]
			}
		}
	}
	if found == nil {
		t.Fatalf("ended codex thread missing from project tree: %+v", got.Projects)
		return
	}
	if found.Live || found.State != "ended" {
		t.Fatalf("ended codex thread live metadata = %+v, want live=false state=ended", *found)
	}
}

func TestAPI_ManagedCodexSessionDetailEnsuresSource(t *testing.T) {
	launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
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
	launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
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
	for _, project := range allTreeProjects(t, web, got) {
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	if _, err := launcher.EnsureSource(context.Background(), "codex-managed", nil); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	first := launcherRunningProcess(t, launcher, "codex-managed")
	if err := first.Cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first codex: %v", err)
	}
	waitLaunchedCodexExited(t, first)

	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// The sidebar is now an empty client-rendered mount point (sidebar.js owns
	// its content via /api/tree, not an htmx-fetched partial), so only the
	// mount points themselves are app-shell assertions worth pinning here.
	if !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("missing #sidebar")
	}
	if !strings.Contains(body, `id="workspace"`) {
		t.Errorf("missing #workspace")
	}
	if !strings.Contains(body, `hx-get="/_partials/workspace/empty"`) {
		t.Errorf("missing workspace partial hx-get")
	}
}

func TestWeb_AppShellHasSidePaneRegion(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="side-panes"`, `id="pane-splitter"`, `panes.js`} {
		if !strings.Contains(body, want) {
			t.Fatalf("app shell missing %q", want)
		}
	}
}

// TestWeb_APITreeProjectUnknownKeyErrors is the /api/tree/project counterpart
// of the deleted sidebar-project HTML-partial test: an empty key is a client
// error (400, the handler's early-return validation) and an unrecognized key
// is a 404 (the "not found in the memoized tree" branch) — distinct from the
// old route, which 404'd on both.
func TestWeb_APITreeProjectUnknownKeyErrors(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	cases := []struct {
		key  string
		want int
	}{
		{"", http.StatusBadRequest},
		{"does-not-exist", http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/tree/project?key="+url.QueryEscape(c.key), nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("key=%q status=%d, want %d body=%q", c.key, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestWeb_InternalPartialsRequireHXRequest(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	for _, path := range []string{
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
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
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
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
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

// TestStateLabel_ErroredAndNeedsYou pins the two label changes the errored
// render lane requires: "awaiting" reads as "Your move" (not the flat,
// unlabeled "Awaiting"), and "errored" gets its own human label rather than
// echoing the raw lowercase state string.
func TestStateLabel_ErroredAndNeedsYou(t *testing.T) {
	if got := stateLabel("errored", false); got != "Error" {
		t.Fatalf("stateLabel(errored) = %q, want Error", got)
	}
	if got := stateLabel("awaiting", false); got != "Your move" {
		t.Fatalf("stateLabel(awaiting) = %q, want \"Your move\"", got)
	}
}

// TestWeb_Index_NewRouteForwardsPromptToWorkspace verifies that /new?prompt=<text>
// renders the app shell wired to /_partials/workspace/spawn?prompt=<text> so the textarea
// pre-fill kicks in once the workspace partial loads.
func TestWeb_Index_NewRouteForwardsPromptToWorkspace(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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

func (s *scriptedAppSource) SteerTurn(context.Context, appwire.TurnSteerParams) error {
	return appwire.Unavailable("scripted source does not steer turns")
}

func (s *scriptedAppSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return appwire.Unavailable("scripted source does not resolve escalations")
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

func (s *scriptedAppSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) error {
	return appwire.Unavailable("scripted source does not promote queued messages")
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

func (fakeSpawner) Spawn(_ context.Context, _ hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{PID: 1, Address: "127.0.0.1:0"}, nil
}
func (fakeSpawner) Resume(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
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
	got    hubcore.SpawnRequest
}

func (s *delayedRosterSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
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

func (s *delayedRosterSpawner) Resume(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, nil
}

func TestWeb_ApiSpawn_WaitsForSlowSpawnerAndReturnsSession(t *testing.T) {
	runDir := t.TempDir()
	workDir := t.TempDir()
	spawner := &delayedRosterSpawner{
		runDir: runDir,
		delay:  20 * time.Millisecond,
		entry: rendezvous.Entry{
			PID:        54,
			Address:    "127.0.0.1:4054",
			WorkingDir: workDir,
			Model:      "gpt-5",
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: srv.Listener.Addr().String(),
		Roster:  hubcore.NewRoster(runDir, fakeProber{sessionID: "01SLOWSPAWN", status: "idle"}),
		Past:    hubcore.NewPastIndex(""),
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
	wantWorkingDir, err := fspaths.CanonicalizeDir(workDir)
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

func TestWeb_ApiSpawn_AccessModeSetsSandboxOverride(t *testing.T) {
	for _, tc := range []struct {
		accessMode string
		want       string
	}{
		{accessMode: "full", want: "off"},
		{accessMode: "workspace-write", want: "workspace-write"},
		{accessMode: "restricted", want: "restricted"},
	} {
		t.Run(tc.accessMode, func(t *testing.T) {
			runDir := t.TempDir()
			workDir := t.TempDir()
			spawner := &delayedRosterSpawner{
				runDir: runDir,
				entry: rendezvous.Entry{
					PID:        56,
					Protocol:   appwire.ProtocolVersion,
					SourceID:   "local",
					ThreadID:   "01SANDBOX",
					SessionID:  "01SANDBOX",
					WorkingDir: workDir,
					Model:      "gpt-5",
				},
			}
			web := NewWebServer(hubcore.WebConfig{
				HubAddr: "127.0.0.1:9180",
				Roster:  hubcore.NewRoster(runDir, fakeProber{sessionID: "01SANDBOX", status: "idle"}),
				Past:    hubcore.NewPastIndex(""),
				Spawner: spawner,
			})
			body := strings.NewReader(fmt.Sprintf(
				`{"harness":"serf","model":"openai/gpt-5","working_dir":%q,"access_mode":%q}`,
				workDir,
				tc.accessMode,
			))
			req := httptest.NewRequest(http.MethodPost, "/api/spawn", body)
			req.Host = "127.0.0.1:9180"
			req.Header.Set("Origin", "http://127.0.0.1:9180")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if spawner.got.Resolved.Effective.Sandbox != tc.want {
				t.Fatalf("sandbox=%q, want %q", spawner.got.Resolved.Effective.Sandbox, tc.want)
			}
		})
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	t.Parallel()
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/spawn", strings.NewReader(`{"prompt":"`+strings.Repeat("x", hubcore.SendMaxRequestBytes)+`"}`))
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
		Spawner: &fakeSpawner{},
	})
	body := spawnRequest{
		Prompt: "look",
		Items: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      make([]byte, hubcore.SendMaxImageBytes+1),
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
	cfg := codexlaunch.CodexLaunchConfig{
		ID:     "codex-broken",
		Binary: "/tmp/serf-no-such-codex-binary",
		Listen: "ws://127.0.0.1:0",
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{cfg},
		CodexLauncher: codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{cfg}),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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

func TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/thread/anysession", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<!DOCTYPE html>`,
		`<body class="thread-document"`,
		`id="conversation"`,
		`data-input-form`,
		`/assets/renderer.js`,
		`/assets/appwire.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("thread document missing %q in %q", want, body)
		}
	}
	for _, forbidden := range []string{
		`id="sidebar"`,
		`id="search-dialog"`,
		`data-sidebar-toggle`,
		`settings-link`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("thread document should not contain %q in %q", forbidden, body)
		}
	}
}

func TestWeb_ThreadDocument_RouteEncoding(t *testing.T) {
	// Seed the past index with a local session whose canonical ID is valid.
	// (what canonicalRouteID produces by stripping the "local:" source prefix).
	// The test then requests an encoded local ref — the %3A must be decoded
	// to ':' before the prefix is stripped, otherwise the wrong key is used and
	// the registered session is not found.
	stateParent := t.TempDir()
	stateDir := filepath.Join(stateParent, "project-route-0000000000")
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	sessionMeta := schema.SessionMeta{
		ID:   sessionID,
		Name: "route-encoding-test-title",
	}
	if err := schema.SaveSessionMeta(stateDir, sessionMeta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	past := hubcore.NewPastIndex(stateParent + "/*")
	if _, err := past.Rebuild(); err != nil {
		t.Fatalf("past.Rebuild: %v", err)
	}

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    past,
	})

	// Encoded local ref: %3A must decode to ':' so the source prefix is stripped
	// and the local session is resolved from the past index.
	t.Run("encoded-local-ref", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thread/local%3A"+sessionID, nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "route-encoding-test-title") {
			t.Errorf("body should contain session title after URL decode; got:\n%s", rec.Body.String())
		}
	})

	// Non-local source: encoded separator still decoded, handler returns OK with
	// fallback workspace (no codex source configured in this test).
	t.Run("encoded-remote-ref", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thread/codex%3Ath_active", nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	})

	// Plain session ID with no encoding needed.
	t.Run("bare-session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thread/bare-session", nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	})
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

func TestWeb_ThreadDocument_CompactsSubagentChromeAndFooter(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	data := WorkspaceData{
		ID:                 "local:child",
		Title:              "child",
		SourceLabel:        "local",
		State:              "ended",
		StateLabel:         "ended",
		TurnCount:          36,
		WorkingDir:         "/projects/serf",
		Branch:             "main",
		Model:              "gpt-5.5",
		ParentRouteID:      "local:parent",
		ParentTitle:        "parent",
		ThreadDocumentMode: true,
		ShowSidebarToggle:  false,
	}
	rec := httptest.NewRecorder()
	if err := web.threadTmpl.ExecuteTemplate(rec, "thread_document", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{
		`class="subagent-parent-banner"`,
		`subagent-parent-esc`,
		`<span class="status-key">src</span>`,
		`<span class="status-key">cwd</span>`,
		`<span class="status-key">branch</span>`,
		`class="status-item turns"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("thread document contains compact-forbidden markup %q in:\n%s", forbidden, body)
		}
	}
	for _, required := range []string{
		`class="workspace-title-row"`,
		`class="message-input"`,
		`data-task-status-text`,
		`class="status-badge"`,
		`class="input-telemetry" data-input-telemetry`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("thread document missing compact-required markup %q in:\n%s", required, body)
		}
	}
	if !strings.Contains(body, `hx-get="/_partials/s/local:child/state?thread_document=1"`) {
		t.Fatalf("thread document status refresh must preserve thread-document mode:\n%s", body)
	}
}

func TestWeb_ThreadDocument_ComposerControlsLiveInsideInputCard(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	data := WorkspaceData{
		ID:                 "local:child",
		Title:              "child",
		State:              "idle",
		StateLabel:         "idle",
		Model:              "gpt-5.5",
		Capabilities:       hubapi.SessionCapabilities{Send: true, Steer: true},
		ThreadDocumentMode: true,
		ShowSidebarToggle:  false,
	}
	rec := httptest.NewRecorder()
	if err := web.threadTmpl.ExecuteTemplate(rec, "thread_document", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	document, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse rendered thread document: %v", err)
	}
	composerSurface := findElement(document, func(node *html.Node) bool { return hasHTMLAttribute(node, "data-composer-surface") })
	inputCard := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "input-card") })
	messageInput := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "message-input") })
	inputControls := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "input-controls") })
	controlsLeft := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "controls-left") })
	taskTrigger := findElement(document, func(node *html.Node) bool { return hasHTMLAttribute(node, "data-tasks-trigger") })
	composerModel := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "composer-model") })
	inputStatus := findElement(document, func(node *html.Node) bool {
		for _, attr := range node.Attr {
			if attr.Key == "id" && attr.Val == "input-status" {
				return true
			}
		}
		return false
	})
	if composerSurface == nil || inputCard == nil || messageInput == nil || inputControls == nil || controlsLeft == nil || taskTrigger == nil || composerModel == nil || inputStatus == nil {
		t.Fatalf("missing composer structure in rendered thread document")
	}
	requireHTMLDescendant(t, composerSurface, inputCard, "input-card should be inside data-composer-surface")
	requireHTMLDescendant(t, inputCard, messageInput, "message-input should be inside input-card")
	requireHTMLDescendant(t, inputCard, inputControls, "input-controls should be inside input-card")
	requireHTMLDescendant(t, inputControls, controlsLeft, "controls-left should be inside input-controls")
	requireHTMLDescendant(t, composerSurface, taskTrigger, "task trigger should be a descendant of data-composer-surface")
	statusRail := findElement(document, func(node *html.Node) bool { return hasHTMLClass(node, "input-status-rail") })
	if statusRail == nil {
		t.Fatalf("missing input-status-rail in rendered thread document")
	}
	requireHTMLDescendant(t, composerSurface, statusRail, "input-status-rail should be inside data-composer-surface")
	requireHTMLDescendant(t, statusRail, taskTrigger, "task trigger should live on the status rail, not in the input card")
	requireHTMLDescendant(t, statusRail, inputStatus, "input status should live on the status rail")
	if isHTMLDescendant(inputCard, taskTrigger) {
		t.Fatalf("task trigger must not live inside the input card")
	}
	if isHTMLDescendant(inputStatus, taskTrigger) {
		t.Fatalf("task trigger must sit outside the htmx input-status swap target so it survives swaps")
	}
	requireHTMLDescendant(t, composerSurface, composerModel, "composer model should be inside data-composer-surface")
	requireHTMLDescendant(t, composerSurface, inputStatus, "input status should be inside data-composer-surface")
	if strings.Contains(body, `send as steer`) {
		t.Fatalf("composer should use short steer label, not send as steer")
	}
	if tag := controlTag(t, body, "data-steer-trigger"); !strings.Contains(body, ">steer<") {
		t.Fatalf("composer should render the short steer button label: %s", tag)
	}
}

func TestWeb_WorkspacePartial_RemainsHXGated(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
	})

	req := httptest.NewRequest(http.MethodGet, "/_partials/s/anysession/workspace", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("workspace partial without HX should remain hidden: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestWeb_SessionRoute_LocalRefCanonicalizesWorkspaceURL(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	if got := worktreeLabel("/state/worktrees/serf/dlg_01H"); got != "dlg_01H" {
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
		WorktreePath: "/state/worktrees/serf/dlg_01H",
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir: "/state/worktrees/serf/dlg_01H",
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
	if data.WorkingDir != "/state/worktrees/serf/dlg_01H" {
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
		Entry:              rendezvous.Entry{PID: 41},
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

func TestSessionStateProjectsRunningInProcessSubagentActiveButNotLive(t *testing.T) {
	const childID = "02wMz5TxvHIJQPOuIBJQct"
	web := webWithPersistedInProcessSubagent(t, childID, childID)

	detail, ok := web.apiSessionState(childID)
	if !ok {
		t.Fatal("apiSessionState did not find persisted in-process child")
	}
	if detail.State != "active" {
		t.Fatalf("State = %q, want active", detail.State)
	}
	if detail.Live {
		t.Fatal("in-process child became independently routable")
	}
	if detail.Capabilities.Send || detail.Capabilities.Steer || detail.Capabilities.Interrupt {
		t.Fatal("lifecycle state granted live actions")
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

func TestWorkspaceDataUsesDaemonStatusTurnCountForLiveLocalSession(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id":  "02wMz5TxvFpYrooBkiqxAp",
			"state":       "active",
			"turns":       37,
			"model":       "gpt-5",
			"working_dir": "/tmp/turns",
		})
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{Address: addr, SessionID: "02wMz5TxvFpYrooBkiqxAp", Model: "gpt-5", WorkingDir: "/tmp/turns"},
		SessionID: "02wMz5TxvFpYrooBkiqxAp",
		Status:    "active",
	})
	web := NewWebServer(hubcore.WebConfig{Roster: roster})

	got := web.workspaceData("02wMz5TxvFpYrooBkiqxAp")
	if got.TurnCount != 37 {
		t.Fatalf("TurnCount = %d, want daemon /status turns 37", got.TurnCount)
	}
}

// TestWorkspaceData_LiveSessionCarriesCostEstimate verifies the roster-live
// branch of workspaceData computes Cost from the daemon-reported Model and
// Usage via appwire.EstimateCost, once both are finalized.
func TestWorkspaceData_LiveSessionCarriesCostEstimate(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id":  "02wMz5TxvHIJQPOuIBJQct",
			"state":       "active",
			"turns":       1,
			"model":       "claude-opus-4-5",
			"working_dir": "/tmp/costlive",
			"usage": map[string]any{
				"inputTokens":  100_000,
				"outputTokens": 20_000,
			},
		})
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{Address: addr, SessionID: "02wMz5TxvHIJQPOuIBJQct", Model: "claude-opus-4-5", WorkingDir: "/tmp/costlive"},
		SessionID: "02wMz5TxvHIJQPOuIBJQct",
		Status:    "active",
	})
	web := NewWebServer(hubcore.WebConfig{Roster: roster})

	got := web.workspaceData("02wMz5TxvHIJQPOuIBJQct")
	if got.Cost != "~$1.00" {
		t.Fatalf("Cost = %q, want ~$1.00", got.Cost)
	}
}

// TestWorkspaceData_PastSessionCarriesCostEstimate verifies the past-meta
// branch of workspaceData computes Cost from the persisted Model and
// CumulativeUsage.
func TestWorkspaceData_PastSessionCarriesCostEstimate(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "02wMz5TxvIl3yzzcpdlu4x"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:    sessionID,
		Model: "claude-opus-4-5",
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

	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx})
	got := web.workspaceData(sessionID)
	if got.Cost != "~$1.00" {
		t.Fatalf("Cost = %q, want ~$1.00", got.Cost)
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
		ID:    sessionID,
		Model: "claude-opus-4-5",
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRoster(t.TempDir(), nil), Past: idx})
	got := web.workspaceData(sessionID)
	if got.Cost != "" {
		t.Fatalf("Cost = %q, want empty for zero usage", got.Cost)
	}
}

// TestWeb_Send_ClosedSessionRequiresSpawner verifies that POSTing to /s/<id>/send
// when the session is not live and no spawner is configured returns 503.
func TestWeb_Send_ClosedSessionRequiresSpawner(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
		Past:    hubcore.NewPastIndex(""),
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
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
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
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: "idle"})
	roster.Refresh()
	resumeCalled := false
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  runDir,
		Roster:  roster,
		Spawner: &fakeRPCSpawner{
			resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
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
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		TurnCount: 2, OriginalPrompt: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
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
		WorkingDir: workingDir,
		Model:      "gpt-5",
		StartedAt:  time.Now().Add(-time.Hour),
	})
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID {
				t.Fatalf("resume session=%q, want %q", req.SessionID, sessionID)
			}
			if req.StateDir != stateDir || req.WorkingDir != workingDir {
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
				WorkingDir: workingDir,
				Model:      "gpt-5",
				StartedAt:  time.Now(),
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusClosed})
	roster.Refresh()
	web := NewWebServer(hubcore.WebConfig{
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
	proj := filepath.Join(root, "projects", "project-x-0123456789")

	// Build the parent session using the shared helper from agent fork tests.
	// We mirror the logic inline here since it's in a different package.
	parentID := "02wMz5Txv5aIxgf9yVdd0N"
	sessionsDir := filepath.Join(proj, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(sessionsDir, parentID+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: parentID, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("first task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("second task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: parentID, UpdatedAt: time.Now(), OriginalPrompt: "test fork",
		ProfileID: "openai", Model: "gpt-5",
	}); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{
		HubAddr:  "127.0.0.1:9180",
		Roster:   hubcore.NewRoster(t.TempDir(), nil),
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

// TestWeb_Fork_DeferInput verifies the fork-from-message REST flow (issue
// #42): POST /s/<id>/fork with defer_input forks at the turn without
// appending a replacement message, and the response carries the original
// input text so the client can stage it in the composer for editing.
func TestWeb_Fork_DeferInput(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")

	parentID := "02wMz5Txv5aIxgf9yVdd0N"
	sessionsDir := filepath.Join(proj, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(sessionsDir, parentID+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: parentID, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("first task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("second task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: parentID, UpdatedAt: time.Now(), OriginalPrompt: "test fork",
		ProfileID: "openai", Model: "gpt-5",
	}); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{
		HubAddr:  "127.0.0.1:9180",
		Roster:   hubcore.NewRoster(t.TempDir(), nil),
		Past:     idx,
		StateDir: proj,
	})
	reqBody := strings.NewReader(`{"turn":3,"defer_input":true}`)
	req := httptest.NewRequest(http.MethodPost, "/s/"+parentID+"/fork", reqBody)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		ChildSessionID string `json:"child_session_id"`
		OriginalInput  string `json:"original_input"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, rec.Body.String())
	}
	if resp.ChildSessionID == "" || resp.ChildSessionID == parentID {
		t.Fatalf("child_session_id=%q", resp.ChildSessionID)
	}
	if resp.OriginalInput != "second task" {
		t.Errorf("original_input=%q, want %q", resp.OriginalInput, "second task")
	}
	// The child transcript must hold only the prefix: no trailing USER_INPUT
	// turn that would auto-run the message on open.
	raw, err := os.ReadFile(filepath.Join(sessionsDir, resp.ChildSessionID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read child transcript: %v", err)
	}
	if strings.Contains(string(raw), "second task") {
		t.Errorf("deferred fork must not copy the diverging user message:\n%s", raw)
	}
}

// TestWeb_APIFork_DeferInputParity verifies the /api fork endpoint enforces
// the same defer_input + edited_message validation as the RPC thread/fork
// path and carries original_input in its response when the fork defers the
// input (issue #42 review): the REST path must not silently accept a
// contradictory body or drop the field the RPC returns.
func TestWeb_APIFork_DeferInputParity(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")

	parentID := "02wMz5Txv5aIxgf9yVdd0N"
	sessionsDir := filepath.Join(proj, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tpath := filepath.Join(sessionsDir, parentID+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: parentID, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("first task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("second task"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: parentID, UpdatedAt: time.Now(), OriginalPrompt: "test fork",
		ProfileID: "openai", Model: "gpt-5",
	}); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	web := NewWebServer(hubcore.WebConfig{
		HubAddr:  "127.0.0.1:9180",
		Roster:   hubcore.NewRoster(t.TempDir(), nil),
		Past:     idx,
		StateDir: proj,
	})

	// Mutual exclusion: defer_input + edited_message is rejected with 400 by
	// both REST endpoints, matching the RPC thread/fork validation.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"defer with edited message", `{"turn":3,"defer_input":true,"edited_message":"x"}`},
		{"non-deferred without edited message", `{"turn":3}`},
	} {
		rec := httptest.NewRecorder()
		web.handleAPIFork(rec, httptest.NewRequest(http.MethodPost, "/api/fork", strings.NewReader(tc.body)), parentID)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("/api fork %s: status %d, want 400 (body=%q)", tc.name, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		web.handleFork(rec, httptest.NewRequest(http.MethodPost, "/fork", strings.NewReader(tc.body)), parentID)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("web fork %s: status %d, want 400 (body=%q)", tc.name, rec.Code, rec.Body.String())
		}
	}

	// A deferred fork via /api carries original_input, as the RPC response does.
	rec := httptest.NewRecorder()
	web.handleAPIFork(rec, httptest.NewRequest(http.MethodPost, "/api/fork", strings.NewReader(`{"turn":3,"defer_input":true}`)), parentID)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api fork defer_input: status %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		SessionID     string `json:"session_id"`
		OriginalInput string `json:"original_input"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%q", err, rec.Body.String())
	}
	if resp.SessionID == "" || resp.SessionID == parentID {
		t.Errorf("session_id=%q", resp.SessionID)
	}
	if resp.OriginalInput != "second task" {
		t.Errorf("original_input=%q, want %q", resp.OriginalInput, "second task")
	}
}

// TestWeb_ApiSearch_FiltersPast populates the past index with two metas,
// queries for one by name, and asserts only that result is returned.
func TestWeb_ApiSearch_FiltersPast(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	_ = schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5TxvLgZ6BB3uYgqz5", UpdatedAt: time.Now(), OriginalPrompt: "fix the frobnitz",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	})
	_ = schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5TxvN5hMuXqRCJbTp", UpdatedAt: time.Now(), OriginalPrompt: "unrelated work",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/beta"},
	})
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
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
	if !strings.Contains(body, "02wMz5TxvLgZ6BB3uYgqz5") {
		t.Errorf("body missing 02wMz5TxvLgZ6BB3uYgqz5: %q", body)
	}
	if strings.Contains(body, "02wMz5TxvN5hMuXqRCJbTp") {
		t.Errorf("body incorrectly includes 02wMz5TxvN5hMuXqRCJbTp: %q", body)
	}
	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(resp.Past) != 1 {
		t.Fatalf("past results = %d, want 1: %q", len(resp.Past), rec.Body.String())
	}
	if resp.Past[0].Title != "session uYgqz5" {
		t.Fatalf("past title = %q, want compact generated ID title without original prompt", resp.Past[0].Title)
	}
}

func TestWeb_ApiSearch_PastUsesGeneratedNameTitle(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	_ = os.MkdirAll(proj, 0o755)
	_ = schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		UpdatedAt:      time.Now(),
		Name:           "Generated Frobnitz Title",
		OriginalPrompt: "unrelated original prompt",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	})
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRoster(t.TempDir(), nil),
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
	const (
		newestID = "02wMz5Txv1C3Hut0M8GCeB"
		olderID  = "02wMz5Txv2enqVTitaig6F"
		tieAID   = "02wMz5Txv47YP64RR3B9YJ"
		tieBID   = "02wMz5Txv5aIxgf9yVdd0N"
	)
	r := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 2, StartedAt: base.Add(-time.Hour), WorkingDir: "/projects/serf"},
			SessionID: olderID,
			Status:    appwire.ThreadStatusIdle,
		},
		hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 1, StartedAt: base, WorkingDir: "/projects/serf"},
			SessionID: newestID,
			Status:    appwire.ThreadStatusIdle,
		},
		hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/serf"},
			SessionID: tieBID,
			Status:    appwire.ThreadStatusIdle,
		},
		hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/serf"},
			SessionID: tieAID,
			Status:    appwire.ThreadStatusIdle,
		},
	)
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
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
	want := []string{newestID, olderID, tieAID, tieBID}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", gotIDs, want)
	}
}

// TestWeb_ApiSearch_LiveResultRefMatchesTreeAPI pins that a live search hit's
// additive Ref field carries the exact qualified ref /api/tree produces for
// the same session. The SPA opens sessions only by qualified ref (e.g.
// "local:<id>" — appwire.ParseRef rejects bare ids), so a bare id alone left
// SPA clients unable to open a search hit.
func TestWeb_ApiSearch_LiveResultRefMatchesTreeAPI(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01L", WorkingDir: project.CanonicalPath}, SessionID: "01L", Status: "active"},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})

	treeReq := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	treeRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(treeRec, treeReq)
	var tree hubapi.TreeResponse
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("want 1 project with 1 session, got %+v", tree.Projects)
	}
	treeNode := tree.Projects[0].Sessions[0]
	if treeNode.SessionID != "01L" || treeNode.Ref == "" {
		t.Fatalf("tree node = %+v, want SessionID 01L with a non-empty ref", treeNode)
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	searchRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(searchRec, searchReq)
	var search searchResponse
	if err := json.Unmarshal(searchRec.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(search.Live) != 1 {
		t.Fatalf("live results = %d, want 1: %+v", len(search.Live), search.Live)
	}
	if search.Live[0].Ref != treeNode.Ref {
		t.Fatalf("search live ref = %q, want tree ref %q", search.Live[0].Ref, treeNode.Ref)
	}
	if search.Live[0].Ref != "local:01L" {
		t.Fatalf("search live ref = %q, want %q", search.Live[0].Ref, "local:01L")
	}
}

// TestWeb_ApiSearch_PastResultRefMatchesTreeAPI pins the same qualified-ref
// guarantee as TestWeb_ApiSearch_LiveResultRefMatchesTreeAPI for a past
// (ended) session hit.
func TestWeb_ApiSearch_PastResultRefMatchesTreeAPI(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-30 * time.Hour), UpdatedAt: now.Add(-30 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir}},
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)

	treeReq := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	treeRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(treeRec, treeReq)
	var tree hubapi.TreeResponse
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("want 1 project with 1 session, got %+v", tree.Projects)
	}
	treeNode := tree.Projects[0].Sessions[0]
	if treeNode.SessionID != "01A" || treeNode.Ref == "" {
		t.Fatalf("tree node = %+v, want SessionID 01A with a non-empty ref", treeNode)
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	searchRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(searchRec, searchReq)
	var search searchResponse
	if err := json.Unmarshal(searchRec.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(search.Past) != 1 {
		t.Fatalf("past results = %d, want 1: %+v", len(search.Past), search.Past)
	}
	if search.Past[0].Ref != treeNode.Ref {
		t.Fatalf("search past ref = %q, want tree ref %q", search.Past[0].Ref, treeNode.Ref)
	}
	if search.Past[0].Ref != "local:01A" {
		t.Fatalf("search past ref = %q, want %q", search.Past[0].Ref, "local:01A")
	}
}

// TestWeb_ApiModels_ReturnsListWithProviderEnv verifies the endpoint
// shape — returns a JSON array of {provider, model, …} entries when
// run against a live provider API. Skips unless live tests are explicitly
// enabled and a real API key is set.
func TestWeb_ApiModels_ReturnsListWithProviderEnv(t *testing.T) {
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live provider model-list test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set; live list models requires a real API key")
	}

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
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

func isolateProviderConfigForModelTest(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.SERFProvidersConfig.Name, filepath.Join(t.TempDir(), "providers.toml"))
	t.Setenv(envvars.SERFStateDir.Name, t.TempDir())
}

func disableStoredOpenAIAuthForModelTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestWeb_ApiModels_ReturnsSerfLaunchContractWhenLiveUnavailable(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	disableLiveOllamaForModelTest(t)
	isolateProviderConfigForModelTest(t)

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCSpawner{
			launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
				return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}, nil
			},
		},
		Models: []hubcore.ModelDescriptor{{Provider: "openai", Model: "gpt-stale"}},
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

	web := NewWebServer(hubcore.WebConfig{
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
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	disableLiveOllamaForModelTest(t)
	isolateProviderConfigForModelTest(t)
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

	web := NewWebServer(hubcore.WebConfig{
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

	web := NewWebServer(hubcore.WebConfig{
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
	web := NewWebServer(hubcore.WebConfig{
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

func TestWeb_ApiModels_DiagnosticsParamReturnsModelsAndDiagnostics(t *testing.T) {
	// With ?diagnostics=1 the picker endpoint returns an object carrying both
	// the launchable models and the launch-check diagnostics, so a configured
	// provider that failed to list (e.g. bad key) surfaces a reason instead of
	// silently vanishing. Without the param the response stays a bare array.

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Spawner: &fakeRPCModelContractSpawner{
			contract: appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}},
				Diagnostics: []appwire.ModelListDiagnostic{
					{Provider: "kimi", Source: "provider", Title: "Provider error", Message: "list models: HTTP 401"},
				},
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/models?diagnostics=1", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Models      []map[string]any              `json:"models"`
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(got.Models) != 1 || got.Models[0]["model"] != "gpt-5.5" {
		t.Fatalf("models: %+v", got.Models)
	}
	if len(got.Diagnostics) != 1 {
		t.Fatalf("diagnostics: got %d, want 1; body=%s", len(got.Diagnostics), rec.Body.String())
	}
	if got.Diagnostics[0].Provider != "kimi" || got.Diagnostics[0].Message != "list models: HTTP 401" {
		t.Fatalf("diagnostic mismatch: %+v", got.Diagnostics[0])
	}
}

func TestWeb_ApiModels_FiltersOpenRouterLiveModelsToToolCapable(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	disableLiveOllamaForModelTest(t)
	isolateProviderConfigForModelTest(t)
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

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
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

// TestWeb_ApiModels_LiveFallbackAppliesInstanceModelOverride pins that the
// live-model-listing fallback in fetchLiveModels overlays a providers.toml
// instance's ModelConfig onto the entry, same as the serf-launch-contract
// path (modelDescriptorsToAPIModels) already does. Before the fix, an
// override configured for a live-listed model (here: a custom context_window
// on openrouter/deepseek/deepseek-chat) was silently ignored.
func TestWeb_ApiModels_LiveFallbackAppliesInstanceModelOverride(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	disableLiveOllamaForModelTest(t)
	isolateProviderConfigForModelTest(t)
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

	overrideCfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "openrouter",
				Type: "openrouter",
				Models: map[string]providercfg.ModelConfig{
					"deepseek/deepseek-chat": {ContextWindow: 999999},
				},
			},
		},
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", ProviderConfig: overrideCfg})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
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
	if len(models) != 1 {
		t.Fatalf("models: got %d, want 1; body=%s", len(models), rec.Body.String())
	}
	if models[0].ContextWindow != 999999 {
		t.Fatalf("context_window = %d, want 999999 (instance override not applied); body=%s", models[0].ContextWindow, rec.Body.String())
	}
}

// TestWeb_ApiModels_NoProvidersConfigured returns an empty list when no
// providers have keys in the environment.
func TestWeb_ApiModels_NoProvidersConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	disableLiveOllamaForModelTest(t)
	isolateProviderConfigForModelTest(t)
	disableStoredOpenAIAuthForModelTest(t)

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
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

// TestWeb_ApiDirCreate covers POST /api/dirs/create, used by the spawn flow to
// create a proposed working directory that does not exist yet.
func TestWeb_ApiDirCreate(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	post := func(path string) *httptest.ResponseRecorder {
		body := strings.NewReader(`{"path":` + strconv.Quote(path) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/api/dirs/create", body)
		req.Host = "127.0.0.1:9180"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("creates a missing directory and its parents", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "missing", "nested")
		rec := post(target)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if resp["created"] != true {
			t.Errorf("expected created:true, got %v", resp["created"])
		}
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			t.Errorf("directory was not created: err=%v", err)
		}
	})

	t.Run("existing directory is idempotent", func(t *testing.T) {
		target := t.TempDir()
		rec := post(target)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d body=%q", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["created"] != false {
			t.Errorf("expected created:false for an existing dir, got %v", resp["created"])
		}
	})

	t.Run("file at the path is a conflict", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := post(file)
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409 for a file path, got %d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		rec := post("relative/dir")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for a relative path, got %d", rec.Code)
		}
	})

	t.Run("GET is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/dirs/create?path=/tmp/x", nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for GET, got %d", rec.Code)
		}
	})
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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01SENDIMG", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
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
	daemon := startAppwireTestDaemon(t, dir, "02wMz5TxvHIJQPOuIBJQct", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
		})
	})
	defer daemon.Close()
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "02wMz5TxvHIJQPOuIBJQct", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
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
	req := httptest.NewRequest(http.MethodPost, "/s/02wMz5TxvHIJQPOuIBJQct/send", strings.NewReader(string(reqBody)))
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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01NOEMPTY", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
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
// rejects image input items larger than hubcore.SendMaxImageBytes with 413, before forwarding
// to the daemon.
func TestWeb_Send_RejectsOversizeImage(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 99, Address: "127.0.0.1:1"})
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01TOOBIG", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    hubcore.NewPastIndex(""),
	})

	// One byte over the per-image cap.
	bigData := make([]byte, hubcore.SendMaxImageBytes+1)
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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01ACTINT", status: appwire.ThreadStatusActive})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01ACTCMP", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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

func TestWeb_SessionAction_CompactResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		TurnCount: 2, OriginalPrompt: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Serf: appwire.SerfThread{
				Ref:          params.Ref,
				Capabilities: appwire.ThreadCapabilities{Compact: true},
			},
		}}, nil
	})
	compactCalled := false
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("compact ref=%q", params.Ref)
		}
		compactCalled = true
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        201,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})

	req := httptest.NewRequest(http.MethodPost, "/s/"+sessionID+"/compact", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if !compactCalled {
		t.Fatal("compact appwire handler was not called after resume")
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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01ACTSHD", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01ACTCLR", status: "idle"})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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
	r := hubcore.NewRoster(dir, fakeProber{})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

	for _, action := range []string{"interrupt", "compact", "shutdown", "clear"} {
		req := httptest.NewRequest(http.MethodPost, "/s/02wMz5Txv8Vo4rqb3QYZuV/"+action, nil)
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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01STEER", status: appwire.ThreadStatusActive})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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
	r := hubcore.NewRoster(dir, fakeProber{sessionID: "01STEEREMPTY", status: appwire.ThreadStatusActive})
	r.Refresh()

	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})
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
	r := hubcore.NewRoster(dir, fakeProber{})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/s/02wMz5Txv8Vo4rqb3QYZuV/steer", strings.NewReader(`{"text":"hello"}`))
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

func (p perAddrProber) Probe(entry rendezvous.Entry) hubcore.ProbeResult {
	v, present := p.byAddr[entry.Address]
	if !present {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{SessionID: v.SessionID, Status: v.Status, OK: true}
}

func TestWeb_APIHealth(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		RunDir:  "/tmp/serf-run",
		Past:    hubcore.NewPastIndex("/tmp/state/projects/*"),
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
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		CodexLaunches: []codexlaunch.CodexLaunchConfig{{ID: "codex-managed"}},
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
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	workingDir := filepath.Join(root, "project")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "01TREE", UpdatedAt: time.Now(), OriginalPrompt: "tree task",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 44, Address: "127.0.0.1:4444", WorkingDir: project.CanonicalPath, Model: "gpt-5",
	})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01TREE", status: appwire.ThreadStatusAwaiting})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})

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
	workingDir := filepath.Join(t.TempDir(), "serf")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 50, Address: "127.0.0.1:4050", WorkingDir: project.CanonicalPath, Model: "gpt-5"})
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 51, Address: "127.0.0.1:4051", WorkingDir: project.CanonicalPath, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, perAddrProber{byAddr: map[string]struct{ SessionID, Status string }{
		"127.0.0.1:4050": {SessionID: "02wMz5Txv9yYdSRJat13MZ", Status: appwire.ThreadStatusIdle},
		"127.0.0.1:4051": {SessionID: "02wMz5TxvBRJC3228LTWod", Status: appwire.ThreadStatusAwaiting},
	}})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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
	if serfProjects[0].WorkingDir != project.CanonicalPath {
		t.Fatalf("working_dir=%q, want %q", serfProjects[0].WorkingDir, project.CanonicalPath)
	}
}

func TestWeb_APITreeSkipsLiveEntriesUntilSessionIDKnown(t *testing.T) {
	workingDir := filepath.Join(t.TempDir(), "serf")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 52, Address: "127.0.0.1:4052", WorkingDir: project.CanonicalPath, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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

func TestWeb_APISessionDetailsLiveAndPast(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), OriginalPrompt: "details task", Model: "gpt-5", ProfileID: "openai", TurnCount: 3,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf", GitBranch: "serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 45, Address: "127.0.0.1:4545", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "02wMz5Txv1C3Hut0M8GCeB", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB", nil)
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
	if got.Ref != "local:02wMz5Txv1C3Hut0M8GCeB" || !got.Live || got.Title != "details task" || got.WorkingDir != "/projects/serf" {
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
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), OriginalPrompt: "details task", Model: "gpt-5", ProfileID: "openai", TurnCount: 3,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf", GitBranch: "serf-hub"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 45, Address: "127.0.0.1:4545", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "02wMz5Txv1C3Hut0M8GCeB", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB", nil)
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

func TestLiveWorkspaceSnapshotSkipsTurns(t *testing.T) {
	source := &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "01METADATA",
			SessionID: "01METADATA",
			Source:    "local",
			Serf: appwire.SerfThread{
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

func TestWeb_APISessionDetailsLocalLiveUsesAppWireForkCapability(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 65, Address: "127.0.0.1:6565", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01FORKCAP", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})
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

// TestWeb_APISessionDetailKeepsFullTurnCountForLiveThread guards the L6
// regression called out by the WS2 plan: apiSessionDetail is shared by
// /api/sessions/<id>, /api/sessions/<id>/details, and
// ensureSessionActionAvailable, and must keep fetching the full transcript
// (IncludeTurns: true) rather than adopt apiSessionState's lean
// IncludeTurns: false — naively dropping IncludeTurns from the shared
// function would silently zero TurnCount for all of them. The roster's
// daemon address is unreachable (no /status fake), so wd.TurnCount is 0 and
// cannot mask a broken full-transcript fetch by coincidence; the only way
// TurnCount comes out as 5 below is a correct IncludeTurns: true read of the
// scripted thread's five completed turns.
func TestWeb_APISessionDetailKeepsFullTurnCountForLiveThread(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 71, Address: "127.0.0.1:4571", WorkingDir: "/projects/serf", Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01FULLTURNS", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "01FULLTURNS",
			SessionID: "01FULLTURNS",
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			CWD:       "/projects/serf",
			Turns: []appwire.Turn{
				{ID: "t1", Status: appwire.TurnStatusCompleted},
				{ID: "t2", Status: appwire.TurnStatusCompleted},
				{ID: "t3", Status: appwire.TurnStatusCompleted},
				{ID: "t4", Status: appwire.TurnStatusCompleted},
				{ID: "t5", Status: appwire.TurnStatusCompleted},
			},
			Serf: appwire.SerfThread{
				Ref:          "local:01FULLTURNS",
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
	})

	for _, sub := range []string{"", "/details"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:01FULLTURNS"+sub, nil)
		req.Host = "127.0.0.1:9180"
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("sub=%q status=%d body=%q", sub, rec.Code, rec.Body.String())
		}
		var got hubapi.SessionDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("sub=%q decode: %v", sub, err)
		}
		if got.TurnCount != 5 {
			t.Errorf("sub=%q turn_count=%d, want 5 (from the full transcript's completed turns)", sub, got.TurnCount)
		}
	}
}

func TestWeb_ManagedCodexLiveWorkspaceCapabilitiesEnsureSource(t *testing.T) {
	launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		Past:          hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")},
		CodexLauncher: launcher,
	})

	caps := web.liveWorkspaceCapabilities("codex-managed:th_fake", hubapi.SessionCapabilities{})
	if !caps.Send {
		t.Fatalf("capabilities=%+v", caps)
	}
}

func TestWeb_APISpawnSchema(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		CodexSources: []appsource.CodexSourceConfig{{
			ID: "codex-local",
		}},
		CodexLaunches: []codexlaunch.CodexLaunchConfig{{ID: "codex-managed"}},
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
	effortValues := map[string]bool{}
	for _, f := range got.Fields {
		if f.Name == "reasoning_effort" {
			for _, v := range f.Values {
				effortValues[v] = true
			}
		}
	}
	// "none" is offered in the launch schema: in layered launch config it clears
	// an inherited default (distinct from "(default)" which inherits).
	for _, want := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "none"} {
		if !effortValues[want] {
			t.Fatalf("reasoning_effort schema missing %q: %+v", want, effortValues)
		}
	}
	if names["branch"] || names["access_mode"] {
		t.Fatalf("schema exposes unsupported field: %+v", got.Fields)
	}
}

func TestWeb_APISessionActionClearReturnsRef(t *testing.T) {
	var clearParams appwire.ThreadClearParams
	runDir := t.TempDir()
	daemon := startAppwireTestDaemon(t, runDir, "02wMz5Txv2enqVTitaig6F", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(_ context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			clearParams = params
			return appwire.ThreadClearResponse{
				Ref: "local:02wMz5Txv1C3Hut0M8GCeB",
				Thread: appwire.Thread{
					ID:        "02wMz5Txv1C3Hut0M8GCeB",
					SessionID: "02wMz5Txv1C3Hut0M8GCeB",
					Source:    "local",
					Serf:      appwire.SerfThread{Ref: "local:02wMz5Txv1C3Hut0M8GCeB"},
				},
			}, nil
		})
	})
	defer daemon.Close()
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "02wMz5Txv2enqVTitaig6F", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv2enqVTitaig6F/clear", nil)
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
	if clearResp.Ref != "local:02wMz5Txv1C3Hut0M8GCeB" || clearResp.SessionID != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("unexpected clear response: %+v", clearResp)
	}
	if clearParams.Ref != "local:02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("clear params ref=%q, want local:02wMz5Txv2enqVTitaig6F", clearParams.Ref)
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
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01MODEL", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

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

func TestWeb_APISessionActionReasoningEffortForwardsBody(t *testing.T) {
	var got appwire.ThreadReasoningEffortSetParams
	runDir := t.TempDir()
	daemon := startAppwireTestDaemon(t, runDir, "01EFFORT", func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer daemon.Close()
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01EFFORT", status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01EFFORT/reasoning-effort", strings.NewReader(`{"reasoning_effort":"high"}`))
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	// Reaching the daemon (no methodNotFound, no 'not available') proves the route
	// is wired and not blocked by a (nonexistent) capability gate.
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got.Ref != "local:01EFFORT" || got.ReasoningEffort != "high" {
		t.Fatalf("reasoning-effort params=%+v", got)
	}
}

// The spawn/model API must report the 1M context window for a "[1m]" ref, not
// the base catalog entry's window (the suffix selects the 1M-context beta).
func TestModelDescriptorsToAPIModels_OneMillionContext(t *testing.T) {
	out := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-5"},
		{Provider: "anthropic", Model: "claude-opus-4-5[1m]"},
	}, nil)
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}
	byModel := map[string]map[string]any{}
	for _, e := range out {
		byModel[e["model"].(string)] = e
	}
	base := byModel["claude-opus-4-5"]
	oneM := byModel["claude-opus-4-5[1m]"]
	if base["context_window"] == nil || oneM["context_window"] == nil {
		t.Fatalf("context_window missing: base=%v oneM=%v", base["context_window"], oneM["context_window"])
	}
	if oneM["context_window"] != 1_000_000 {
		t.Errorf("[1m] context_window = %v, want 1000000", oneM["context_window"])
	}
	if base["context_window"] == 1_000_000 {
		t.Errorf("base context_window = %v, want the smaller base window", base["context_window"])
	}
}

// TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent (kata model-picker
// Recent) verifies /api/models?diagnostics=1 carries a "recent" array
// resolved from the Past index, restricted to models the response actually
// offers, in most-recent-first order; the bare-array default response is
// unaffected. LiveModels is stubbed to keep the test hermetic (fetchLiveModels
// would otherwise call cmdutil.LoadClient() and touch the real host config).
func TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent(t *testing.T) {
	s := NewWebServer(hubcore.WebConfig{
		HubAddr:    "127.0.0.1:9180",
		LiveModels: func(context.Context) []map[string]any { return nil },
	})
	s.injectMetasForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "local", Model: "test-model-one", UpdatedAt: time.Now()},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/models?diagnostics=1", nil)
	rec := httptest.NewRecorder()
	s.handleApiModels(rec, req)

	var body struct {
		Models      []map[string]any `json:"models"`
		Diagnostics []map[string]any `json:"diagnostics"`
		Recent      []map[string]any `json:"recent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if body.Recent == nil {
		t.Fatal("recent should be an empty array, not null, when the envelope is requested")
	}
}

// The footer status row must show ONE live indicator. The legacy
// running-indicator ("running") duplicated the status badge ("Working" for an
// active session), so it was removed — an active session shows the StateLabel
// badge only, not a second "running" pill.
func TestInputStatus_NoDuplicateRunningIndicator(t *testing.T) {
	tmpl := template.Must(template.New("input_strip.html").Funcs(inputStripTemplateFuncs).ParseFS(templatesFS, "templates/partials/input_strip.html"))
	data := map[string]any{
		"Branch": "main", "Worktree": "task-2", "WorkingDir": "/workspace/serf",
		"ContextWindow": 100000, "ContextPercent": 42,
		"ContextNumbers": "42k / 100k tokens (58k left)", "CompactContextNumbers": "42k / 100k",
		"State": "active", "StateLabel": "Working",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "input_status", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `class="status-badge"`) || !strings.Contains(out, "Working") {
		t.Fatalf("active status row should show the StateLabel badge:\n%s", out)
	}
	if strings.Count(out, "Working") != 1 {
		t.Fatalf("active status rail should render exactly one visible Working state label:\n%s", out)
	}
	for _, want := range []string{
		`class="input-telemetry" data-input-telemetry`,
		`class="status-item location" data-status-location`,
		`class="status-item context" data-status-context`,
		`class="status-value context-numbers">42k / 100k<`,
		`class="status-location-part worktree"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compact input status missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		`class="status-item source"`,
		`class="status-item turns"`,
		`class="status-item work"`,
		`class="status-item tokens"`,
		`class="status-item cost"`,
		`class="status-item goal"`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("compact input status unexpectedly rendered %q:\n%s", unwanted, out)
		}
	}
}

// The context gauge must stay NEUTRAL until ~80% used, then turn AMBER with a
// glyph (mockup #17 Alt A). The threshold lives in the input_status template
// using the real ContextPercent; below 80% no warn class / glyph appears, at or
// above 80% the .context-fill carries .context-warn and a ⚠ glyph renders.
func TestInputStatusGaugeAmberThreshold(t *testing.T) {
	tmpl := template.Must(template.New("input_strip.html").Funcs(inputStripTemplateFuncs).ParseFS(templatesFS, "templates/partials/input_strip.html"))
	render := func(percent int) string {
		data := map[string]any{
			"ContextWindow":         272000,
			"ContextPercent":        percent,
			"ContextNumbers":        "23k / 272k tokens",
			"CompactContextNumbers": "23k / 272k",
			"State":                 "active",
			"StateLabel":            "Active",
			"TurnCount":             3,
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "input_status", data); err != nil {
			t.Fatalf("render percent=%d: %v", percent, err)
		}
		return buf.String()
	}

	for _, percent := range []int{0, 8, 50, 79} {
		out := render(percent)
		if strings.Contains(out, "context-warn") {
			t.Errorf("percent=%d: gauge must stay neutral (no context-warn), got:\n%s", percent, out)
		}
		if strings.Contains(out, "⚠") {
			t.Errorf("percent=%d: gauge must not show ⚠ glyph below threshold", percent)
		}
	}

	for _, percent := range []int{80, 85, 100} {
		out := render(percent)
		if !strings.Contains(out, "context-warn") {
			t.Errorf("percent=%d: gauge must turn amber (.context-warn), got:\n%s", percent, out)
		}
		if !strings.Contains(out, "⚠") {
			t.Errorf("percent=%d: gauge must show a ⚠ glyph near the limit", percent)
		}
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
		entries = append(entries, hubcore.LiveEntry{
			Entry: rendezvous.Entry{PID: i + 1}, SessionID: id, Status: appwire.ThreadStatusActive,
		})
	}
	return NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRosterWithEntries(entries...),
		Past:    idx,
	})
}

// A worker whose meta carries a LIVE observer surfaces that observer's route id
// in WorkspaceData.ObserverRouteIDs (the data-observers source for auto-open).
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

// An ordinary worker with no ObservedBy carries no observer route ids.
func TestWeb_WorkspaceData_NoObserversWhenUnwatched(t *testing.T) {
	web := observerWorkspaceFixture(t, nil)
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 0 {
		t.Fatalf("un-watched worker must have no observers; got %v", wd.ObserverRouteIDs)
	}
}

// writeObserverGrantLog writes a watching session's jobs.jsonl encoding a
// watch-read-grant: observer OBSERVER watching a delegate job whose transcript
// ref resolves to worker WORKER. This is the durable on-disk historical source
// for the observer link — no ObservedBy stamp involved (the 0/2211 case).
func writeObserverGrantLog(t *testing.T, project, watchingSessID, watchedJobID, workerRef, observerSessID string) {
	t.Helper()
	jobsDir := filepath.Join(project, "sessions", watchingSessID)
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"kind":"job_started","seq":1,"ts":"` + ts + `","job_id":"` + watchedJobID + `","type":"delegate","owner_session_id":"` + watchingSessID + `"}` + "\n" +
		`{"kind":"job_session_assigned","seq":2,"ts":"` + ts + `","job_id":"` + watchedJobID + `","transcript_ref":"` + workerRef + `"}` + "\n" +
		`{"kind":"watch_read_grant","seq":3,"ts":"` + ts + `","job_id":"` + watchedJobID + `","observer_session_id":"` + observerSessID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(jobsDir, "jobs.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write jobs.jsonl: %v", err)
	}
}

// observerGrantWorkspaceFixture writes a WORKER meta with NO ObservedBy stamp,
// plus a grant log under a watching PARENT session that resolves the observer to
// WORKER, then builds a hub with the named live roster sessions. It is the
// historical-source counterpart to observerWorkspaceFixture.
func observerGrantWorkspaceFixture(t *testing.T, observerSessID, workerRef string, liveSessionIDs ...string) *WebServer {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-observer-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), OriginalPrompt: "do work",
		IsSubagent: true, ParentSessionID: "02wMz5Txv2enqVTitaig6F", // no ObservedBy stamp
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/project-observer-0123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	writeObserverGrantLog(t, proj, "02wMz5Txv2enqVTitaig6F", "job_watched", workerRef, observerSessID)
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entries := make([]hubcore.LiveEntry, 0, len(liveSessionIDs))
	for i, id := range liveSessionIDs {
		entries = append(entries, hubcore.LiveEntry{
			Entry: rendezvous.Entry{PID: i + 1}, SessionID: id, Status: appwire.ThreadStatusActive,
		})
	}
	return NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  hubcore.NewRosterWithEntries(entries...),
		Past:    idx,
	})
}

// A worker whose observer link exists ONLY in the durable grant log (no
// ObservedBy stamp) still surfaces the live observer — the grant-history source
// feeds fillObserverLink, which is the whole point on existing data.
func TestWeb_WorkspaceData_CarriesObserverFromGrantHistory(t *testing.T) {
	web := observerGrantWorkspaceFixture(t, "02wMz5Txv47YP64RR3B9YJ", "local:02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv47YP64RR3B9YJ")
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 1 || wd.ObserverRouteIDs[0] != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("ObserverRouteIDs = %v, want [02wMz5Txv47YP64RR3B9YJ]", wd.ObserverRouteIDs)
	}
}

// A grant-history observer that is no longer live is filtered out, same as a
// stamped one — auto-open stays live-only regardless of source.
func TestWeb_WorkspaceData_IncludesEndedGrantHistoryObserver(t *testing.T) {
	web := observerGrantWorkspaceFixture(t, "02wMz5Txv47YP64RR3B9YJ", "local:02wMz5Txv1C3Hut0M8GCeB") // observer not live (ended)
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	if len(wd.ObserverRouteIDs) != 1 || wd.ObserverRouteIDs[0] != "02wMz5Txv47YP64RR3B9YJ" {
		t.Fatalf("ended grant-history observer must still be surfaced; got %v", wd.ObserverRouteIDs)
	}
}

// The grant-history source and the ObservedBy stamp union and dedup: an observer
// present in both sources surfaces exactly once.
func TestWeb_WorkspaceData_UnionsStampAndGrantHistory(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-observer-0123456789")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Worker carries STAMPED on its meta; GRANTED comes only from the grant log;
	// STAMPED is ALSO in the grant log (must not duplicate).
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now(), IsSubagent: true, ParentSessionID: "02wMz5Txv2enqVTitaig6F",
		ObservedBy: []string{"02wMz5Txv47YP64RR3B9YJ"},
		EnvInfo:    schema.EnvironmentInfo{WorkingDir: "/projects/project-observer-0123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	writeObserverGrantLog(t, proj, "02wMz5Txv2enqVTitaig6F", "job_w1", "local:02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv5aIxgf9yVdd0N")
	writeObserverGrantLog(t, proj, "02wMz5Txv733WHFsVy66SR", "job_w2", "local:02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv47YP64RR3B9YJ")
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster: hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 1}, SessionID: "02wMz5Txv47YP64RR3B9YJ", Status: appwire.ThreadStatusActive},
			hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 2}, SessionID: "02wMz5Txv5aIxgf9yVdd0N", Status: appwire.ThreadStatusActive},
		),
		Past: idx,
	})
	wd := web.workspaceData("02wMz5Txv1C3Hut0M8GCeB")
	seen := map[string]int{}
	for _, id := range wd.ObserverRouteIDs {
		seen[id]++
	}
	if len(wd.ObserverRouteIDs) != 2 || seen["02wMz5Txv47YP64RR3B9YJ"] != 1 || seen["02wMz5Txv5aIxgf9yVdd0N"] != 1 {
		t.Fatalf("ObserverRouteIDs = %v, want STAMPED+GRANTED once each", wd.ObserverRouteIDs)
	}
}
