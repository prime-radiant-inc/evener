package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/rendezvous"
)

// FuzzSessionTreePass3 drives the presentation and HTTP boundary branches that
// are otherwise difficult for the broad route fuzzers to reach. All sources
// and filesystem roots are process-local and deterministic.
func FuzzSessionTreePass3(f *testing.F) {
	for op := uint8(0); op < 16; op++ {
		f.Add(op, "alpha\r\nbeta", int64(90_000))
	}
	f.Fuzz(func(t *testing.T, op uint8, text string, number int64) {
		now := time.Now().UnixMilli()
		started := now - 90000
		usage := &appwire.SerfUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 18}
		thread := appwire.Thread{
			ID: "thread-1", SessionID: "session-1", Source: "remote", Name: text,
			Preview: "preview", CWD: "/work/project", ModelProvider: "openai/gpt",
			CreatedAt: now - 100, UpdatedAt: now, Status: appwire.ThreadStatus{Type: "active"},
			Turns: []appwire.Turn{{ID: "done", Status: appwire.TurnStatusCompleted}, {ID: "run", Status: appwire.TurnStatusInProgress, StartedAt: &started}},
			Serf:  appwire.SerfThread{Ref: "remote:thread-1", ActiveTurnID: "run-explicit", ContextUsed: 10, ContextWindow: 20, ContextRemaining: 10, WorkMillis: number, Usage: usage, Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Queue: true}},
		}

		switch op % 16 {
		case 0:
			_ = workspaceDataFromAppThread(thread)
			thread.Name, thread.Preview, thread.Serf.Ref = "", "", ""
			thread.Status.Type = ""
			_ = workspaceDataFromAppThread(thread)
		case 1:
			_ = activeTurnRunningFor(thread)
			thread.Turns[1].StartedAt = nil
			_ = activeTurnRunningFor(thread)
			_ = compactDuration(-time.Second)
			_ = compactDuration(500 * time.Millisecond)
			_ = compactDuration(90 * time.Second)
			_ = compactDuration(2*time.Hour + 5*time.Minute)
		case 2:
			_ = activeTurnIDFromAppwireThread(thread)
			thread.Serf.ActiveTurnID = ""
			_ = activeTurnIDFromAppwireThread(thread)
			thread.Turns = nil
			_ = activeTurnIDFromAppwireThread(thread)
			_ = completedTurnCount([]appwire.Turn{{Status: appwire.TurnStatusCompleted}, {Status: appwire.TurnStatusFailed}})
		case 3:
			for _, ref := range []string{"bad", "local:one", "remote:one"} {
				_ = sourceLabelFromRefText(ref)
			}
			for _, n := range []int{-1, 0, 999, 1000, 1499} {
				_ = formatTokenCount(n)
			}
			_ = formatContextNumbers(1, 0, -1)
			_ = formatContextNumbers(1, 2, -1)
			_ = formatCompactContextNumbers(1, 0)
			_ = formatCompactContextNumbers(1000, 2000)
		case 4:
			for _, p := range []string{"", ".", "/", "/work/tree"} {
				_ = worktreeLabel(p)
			}
			for _, p := range []string{"", " first\r\nsecond ", strings.Repeat("x", 90)} {
				_ = compactSessionPromptTitle(p)
			}
			for _, m := range []schema.SessionMeta{{Name: " named "}, {OriginalPrompt: "prompt"}, {ID: "0123456789abcdef"}} {
				_ = sessionTitleFromMeta(m)
			}
		case 5:
			_ = hubDetailFromAppThread(thread)
			thread.Name, thread.Preview, thread.SessionID, thread.CWD = "", "", "", ""
			thread.Status.Type = appwire.ThreadStatusClosed
			thread.Serf.Ref = "malformed"
			thread.Serf.Usage = nil
			_ = hubDetailFromAppThread(thread)
			_ = hubUsageFromAppwire(nil)
		case 6:
			_, _, _ = appThreadTreeEntries(thread)
			thread.Serf.Ref = "bad"
			thread.Source = ""
			_, _, _ = appThreadTreeEntries(thread)
			thread.Source = "remote"
			thread.ID = ""
			_, _, _ = appThreadTreeEntries(thread)
			for _, status := range []string{appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded, "active"} {
				thread.Status.Type = status
				_ = appThreadTreeLive(thread)
			}
		case 7:
			_ = hubCapabilitiesFromAppwire(thread.Serf.Capabilities)
			_ = hubRefFromAppThread(thread)
			thread.Serf.Ref = "bad"
			_ = hubRefFromAppThread(thread)
			_ = hubRefFromTreeNodeID("bad")
		case 8:
			web := NewWebServer(hubcore.WebConfig{})
			l := &stubThreadLister{id: "remote", resp: appwire.ThreadListResponse{Data: []appwire.Thread{thread}}}
			_ = web.listThreadsWithFallback(context.Background(), l)
			l.err = errors.New("offline")
			_ = web.listThreadsWithFallback(context.Background(), l)
		case 9:
			web := NewWebServer(hubcore.WebConfig{})
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				rec := httptest.NewRecorder()
				web.handleAPITree(rec, httptest.NewRequest(method, "/api/tree?summary=1", nil))
			}
			for _, target := range []string{"/api/tree/project", "/api/tree/project?key=missing"} {
				rec := httptest.NewRecorder()
				web.handleAPITreeProject(rec, httptest.NewRequest(http.MethodGet, target, nil))
			}
			rec := httptest.NewRecorder()
			web.handleAPITreeProject(rec, httptest.NewRequest(http.MethodPost, "/api/tree/project", nil))
		case 10:
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "live", Model: "m"}, SessionID: "live", Status: "active"})
			web := NewWebServer(hubcore.WebConfig{Roster: roster})
			_ = web.isLive("live")
			_ = web.isLive("missing")
			_, _ = web.liveEntry("live")
			_ = web.apiTreeNode("project", "key", hubcore.TreeNode{ID: "live", Children: []hubcore.TreeNode{{ID: "child", State: "ended"}}}, true)
			p := hubcore.TreeProject{Key: "key", Current: []hubcore.TreeNode{{ID: "live", State: "active"}}, Recent: []hubcore.TreeNode{{ID: "recent", State: "ended"}}, Archived: []hubcore.TreeNode{{ID: "old", State: "closed"}}}
			_ = projectSessions(p)
			_ = web.apiTreeProject("project", map[hubcore.ArchiveKey]bool{{Kind: "session", ID: "live"}: true}, p)
			_ = web.rowRenameable("live")
			_ = hubAttentionSummaryFromCore(hubcore.AttentionSummary{NeedsYou: 1})
			_ = web.apiTreeSources()
		case 11:
			web := NewWebServer(hubcore.WebConfig{})
			for _, target := range []string{"/api/sessions/bad", "/api/sessions/local%3Amissing", "/api/sessions/local%3Amissing/details", "/api/sessions/local%3Amissing/nope"} {
				rec := httptest.NewRecorder()
				web.handleAPISession(rec, httptest.NewRequest(http.MethodGet, target, nil))
			}
		case 12:
			for _, action := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "shutdown", "model", "queue", "other"} {
				_ = sessionCapabilityAvailable(hubapi.SessionCapabilities{Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true, Fork: true, Shutdown: true, ChangeModel: true, Queue: true}, action)
			}
			for _, err := range []error{appwire.Unavailable("unavailable"), appwire.Conflict("conflict"), appwire.SessionUnavailable("missing"), errors.New("x")} {
				_ = isActionUnavailable(err)
				rec := httptest.NewRecorder()
				writeSessionActionError(rec, httptest.NewRequest(http.MethodPost, "/", nil), err)
			}
		case 14:
			web := NewWebServer(hubcore.WebConfig{})
			for _, body := range []string{"{", `{}`, `{"text":"x"}`} {
				rec := httptest.NewRecorder()
				web.handleSend(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), "local")
			}
			_, _ = web.resumeRequestFor("missing")
			for _, action := range []string{"interrupt", "compact", "shutdown", "unknown"} {
				rec := httptest.NewRecorder()
				web.handleSessionAction(rec, httptest.NewRequest(http.MethodGet, "/", nil), "missing", action)
			}
			{
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				web.handleAPIFork(rec, req, "missing")
			}
			_, _, _ = web.forkSession("missing", forkRequest{})
			_ = waitForRosterMatch(hubcore.NewRosterWithEntries(), "missing", 1, 0)
		case 15:
			dir := t.TempDir()
			meta := schema.SessionMeta{ID: "0123456789abcdef", Name: text, OriginalPrompt: "prompt"}
			_ = schema.SaveSessionMeta(dir, meta)
			pe := hubcore.PastEntry{StateDir: dir, Meta: meta}
			_ = pastTitle(pe)
			_ = searchPastTitle(pe)
			_ = liveTitle(meta.ID, hubcore.LiveEntry{}, nil)
			_ = stateLabel("active", true)
			_ = filepath.Base(dir)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"active"}`)) }))
			defer srv.Close()
			_ = NewWebServer(hubcore.WebConfig{}).fetchStatus(hubcore.LiveEntry{Entry: rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}})
			_ = NewWebServer(hubcore.WebConfig{}).fetchStatus(hubcore.LiveEntry{Entry: rendezvous.Entry{Address: "[bad"}})
		}
	})
}
