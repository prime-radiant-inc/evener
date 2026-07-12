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
		_ = formatWorkMillis(millis)
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
		}

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
		route := routes[int(selector)%len(routes)]
		req := httptest.NewRequest(route.method, route.path, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 && rec.Code != http.StatusInternalServerError && rec.Code != http.StatusBadGateway && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("unexpected status %d", rec.Code)
		}
	})
}
