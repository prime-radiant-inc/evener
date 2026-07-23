package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// FuzzCovWebTreeSessionPure drives the input-shaped presentation and tree
// adapters. The seed matrix deliberately includes every boundary bucket while
// fuzz input continues to vary the strings and numeric values.
func FuzzCovWebTreeSessionPure(f *testing.F) {
	f.Add("local:thread", "title", int64(0), 0)
	f.Add("codex:thread", strings.Repeat("x", 100)+"\nrest", int64(65_000), 1500)
	f.Fuzz(func(t *testing.T, refText, title string, millis int64, tokens int) {
		if len(refText) > 256 || len(title) > 512 {
			t.Skip()
		}
		now := time.Now().Unix() - 2
		for _, d := range []time.Duration{-time.Second, 0, time.Second, time.Minute, time.Hour + time.Minute} {
			_ = compactDuration(d)
		}
		for _, n := range []int{-1, 0, 999, 1000, tokens} {
			_ = formatTokenCount(n)
			_ = formatContextNumbers(n, 0, n)
			_ = formatContextNumbers(n, 1000, -1)
			_ = formatCompactContextNumbers(n, 0)
			_ = formatCompactContextNumbers(n, 1000)
		}
		_ = htmlEscape(title)
		_ = sourceLabelFromRefText(refText)
		_ = sourceLabelFromRefText("local:x")
		_ = sourceLabelFromRefText("remote:x")
		_ = worktreeLabel("")
		_ = worktreeLabel("/")
		_ = worktreeLabel("/tmp/worktree")
		for _, prompt := range []string{"", title, "first\nsecond", strings.Repeat("z", 100)} {
			_ = compactSessionPromptTitle(prompt)
		}
		for _, meta := range []schema.SessionMeta{{ID: "id"}, {ID: "id", Name: title}, {ID: "id", OriginalPrompt: title}} {
			_ = sessionTitleFromMeta(meta)
			_ = searchPastTitle(hubcore.PastEntry{Meta: meta})
		}
		_ = liveTitle("0123456789", hubcore.LiveEntry{}, nil)
		_ = hubUsageFromAppwire(nil)
		_ = hubUsageFromAppwire(&appwire.SerfUsage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalTokens: 6})

		thread := appwire.Thread{
			ID: "thread", SessionID: "session", Source: "remote", Name: title,
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Turns: []appwire.Turn{
				{ID: "done", Status: appwire.TurnStatusCompleted},
				{ID: "running", Status: appwire.TurnStatusInProgress, StartedAt: &now},
			},
			Serf: appwire.SerfThread{Ref: refText, ActiveTurnID: "active", Goal: &appwire.GoalState{Status: "active", Iterations: 2}},
		}
		_ = workspaceDataFromAppThread(thread)
		_ = activeTurnRunningFor(thread)
		_ = hubDetailFromAppThread(thread)
		_, _, _ = appThreadTreeEntries(thread)
		_, _ = appThreadTreeRef(thread)
		_ = hubRefFromAppThread(thread)
		thread.Serf.ActiveTurnID = ""
		_ = activeTurnIDFromAppwireThread(thread)
		thread.Turns = nil
		_ = activeTurnIDFromAppwireThread(thread)
		_ = activeTurnRunningFor(thread)
		thread.Preview, thread.SessionID = "", "session"
		_ = workspaceDataFromAppThread(thread)
		thread.Serf.Ref = "bad"
		_ = hubRefFromAppThread(thread)
		thread.Serf.Ref = ""
		thread.Name, thread.Preview, thread.SessionID = "", title, ""
		_ = workspaceDataFromAppThread(thread)
		_, _, _ = appThreadTreeEntries(thread)
		thread.Source, thread.ID = "", ""
		_, _, _ = appThreadTreeEntries(thread)
		for _, status := range []string{appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded, appwire.ThreadStatusActive} {
			thread.Status.Type = status
			_ = appThreadTreeLive(thread)
		}
		_ = workspaceDataFromAppThread(appwire.Thread{ID: "id", Preview: "preview"})
		_ = workspaceDataFromAppThread(appwire.Thread{ID: "id"})
	})
}

// FuzzCovWebTreeSessionHandlers exercises rejection and routing paths through
// the real WebServer handler. newSandbox provides contained filesystem state
// and scripted local boundaries.
func FuzzCovWebTreeSessionHandlers(f *testing.F) {
	f.Add(byte(0), []byte(`{}`))
	f.Add(byte(1), []byte(`{"text":"hello"}`))
	f.Fuzz(func(t *testing.T, selector byte, body []byte) {
		if len(body) > hubcore.SendMaxRequestBytes+1 {
			t.Skip()
		}
		sb := newSandbox(t)
		h := sb.Web.Handler()
		routes := []struct{ method, path string }{
			{http.MethodPost, "/api/tree"},
			{http.MethodPost, "/api/tree/project"},
			{http.MethodGet, "/api/tree/project"},
			{http.MethodGet, "/api/sessions/not-a-ref"},
			{http.MethodPost, "/api/sessions/local%3A" + sandboxSessionID + "/fork"},
			{http.MethodPost, "/s/" + sandboxSessionID + "/send"},
			{http.MethodPost, "/s/" + sandboxSessionID + "/steer"},
			{http.MethodPost, "/s/" + sandboxSessionID + "/queue"},
			{http.MethodPost, "/s/" + sandboxSessionID + "/drain-as-steer"},
			{http.MethodGet, "/s/" + sandboxSessionID + "/interrupt"},
		}
		// Rotate the matrix while still executing every row. This retains useful
		// fuzz variation without leaving seed coverage to corpus scheduling.
		for i := range routes {
			route := routes[(i+int(selector))%len(routes)]
			req := httptest.NewRequest(route.method, route.path, bytes.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code >= 500 && rec.Code != http.StatusInternalServerError && rec.Code != http.StatusBadGateway && rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("unexpected status %d", rec.Code)
			}
		}

		for _, action := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "shutdown", "model", "queue", "unknown"} {
			_ = sessionCapabilityAvailable(hubCapabilitiesFromAppwire(appwire.ThreadCapabilities{
				Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
				ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true,
			}), action)
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, action := range []string{"interrupt", "clear", "shutdown", "compact", "unknown"} {
				req := httptest.NewRequest(method, "/s/missing/"+action, bytes.NewReader(body))
				rec := httptest.NewRecorder()
				sb.Web.handleSessionAction(rec, req, "missing", action)
			}
		}
		for _, target := range []string{"/api/tree?summary=1", "/api/tree", "/api/tree/project?key=missing"} {
			rec := httptest.NewRecorder()
			sb.Web.handleAPITree(rec, httptest.NewRequest(http.MethodGet, target, nil))
		}
		for _, target := range []string{"/api/tree/project", "/api/tree/project?key=missing"} {
			rec := httptest.NewRecorder()
			sb.Web.handleAPITreeProject(rec, httptest.NewRequest(http.MethodGet, target, nil))
		}
		_ = sb.Web.archiveDecisions()
		_ = sb.Web.favoriteDecisions()
		_, _ = sb.Web.memoTree(t.Context())
		_, _, _ = sb.Web.navigationTreeInputs(t.Context())
		_ = sb.Web.remoteTreeThreads(t.Context())
		_ = inputItemsForText("")
		_ = inputItemsForText(" text ")
		{
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/local%3Amissing/fork", bytes.NewReader(body))
			sb.Web.handleAPIFork(rec, req, "missing")
		}
		for _, api := range []bool{false, true} {
			target := "/s/missing/send"
			if api {
				target = "/api/sessions/local%3Amissing/send"
			}
			rec := httptest.NewRecorder()
			writeSessionActionError(rec, httptest.NewRequest(http.MethodPost, target, nil), appwire.Unavailable("unavailable"))
		}
	})
}
