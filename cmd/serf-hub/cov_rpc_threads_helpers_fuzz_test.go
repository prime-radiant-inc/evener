package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
)

// FuzzCovRPCThreadsHelpers drives the small branch-heavy helpers used by the
// RPC and thread handlers. Each selector has a seed so the deterministic
// corpus run covers the complete table; fuzzed bytes additionally vary the
// text fields without involving network, process, or ambient filesystem state.
func FuzzCovRPCThreadsHelpers(f *testing.F) {
	for selector := byte(0); selector < 14; selector++ {
		f.Add(selector, "needle")
	}
	f.Fuzz(func(t *testing.T, selector byte, text string) {
		switch selector % 14 {
		case 0:
			want := []struct {
				action string
				ok     bool
			}{
				{"send", true}, {"steer", true}, {"interrupt", true},
				{"compact", true}, {"clear", true}, {"fork", true},
				{"shutdown", true}, {"model", true}, {"rename", true},
				{"queue", true}, {"goal", true}, {text, false},
			}
			caps := appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true, ForkFromTurn: true, Shutdown: true, ChangeModel: true, Rename: true, Queue: true, Goal: true}
			for _, tc := range want {
				if got := threadActionAvailable(caps, tc.action); got != tc.ok {
					t.Fatalf("threadActionAvailable(%q)=%v, want %v", tc.action, got, tc.ok)
				}
			}
		case 1:
			for _, tc := range []struct{ in, want string }{{"active", appwire.ThreadStatusActive}, {"notloaded", appwire.ThreadStatusNotLoaded}, {"systemerror", appwire.ThreadStatusSystemError}, {"  CuStOm  ", "custom"}} {
				if got := normalizeThreadListStatusFilter(tc.in); got != tc.want {
					t.Fatalf("normalize(%q)=%q, want %q", tc.in, got, tc.want)
				}
			}
		case 2:
			for _, raw := range []string{"", "turn_0", "turn_nope", "turn_1", " 2 "} {
				turn, err := parseSourceTurnID(raw)
				if raw == "turn_1" && (err != nil || turn != 1) {
					t.Fatalf("parse valid: turn=%d err=%v", turn, err)
				}
			}
		case 3:
			thread := appwire.Thread{ID: "id", SessionID: "session", Source: "fallback", Serf: appwire.SerfThread{Ref: appwire.Ref{SourceID: "refsource", ThreadID: "id"}.String()}}
			if got := threadListSourceID("default", thread); got != "fallback" {
				t.Fatalf("source=%q", got)
			}
			thread.Source = ""
			if got := threadListSourceID("default", thread); got != "refsource" {
				t.Fatalf("ref source=%q", got)
			}
			thread.Serf.Ref = "bad"
			if got := threadListSourceID("default", thread); got != "default" {
				t.Fatalf("default source=%q", got)
			}
		case 4:
			params := appwire.ThreadListParams{SourceIDs: []string{"a", "b"}}
			if !sourceAllowedForList("a", params) || sourceAllowedForList("c", params) || !sourceAllowedForList("c", appwire.ThreadListParams{}) {
				t.Fatal("source filtering invariant")
			}
			if !sourceExplicitlyRequestedForList("b", params) || sourceExplicitlyRequestedForList("c", params) {
				t.Fatal("explicit source invariant")
			}
			if threadListSourceKey("local", "id") == "" || threadListSourceKey("local", "") != "" {
				t.Fatal("source key invariant")
			}
		case 5:
			thread := appwire.Thread{ID: "id", SessionID: "sid", Name: "Name", Preview: "Preview", CWD: "/work", Path: "/path", ModelProvider: "Provider", Source: "local", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}, Serf: appwire.SerfThread{Profile: "Profile"}}
			if !appThreadMatches(thread, appwire.ThreadListParams{SearchTerm: "preview", SourceIDs: []string{"local"}, Statuses: []string{"ACTIVE"}}) {
				t.Fatal("expected match")
			}
			if appThreadMatches(thread, appwire.ThreadListParams{SearchTerm: text + "absent-suffix"}) {
				t.Fatal("unexpected search match")
			}
			if appThreadMatches(thread, appwire.ThreadListParams{Statuses: []string{"idle"}}) || appThreadMatches(thread, appwire.ThreadListParams{SourceIDs: []string{"remote"}}) {
				t.Fatal("unexpected filtered match")
			}
			if !appThreadMatches(thread, appwire.ThreadListParams{}) {
				t.Fatal("unfiltered thread did not match")
			}
		case 6:
			items := []appwire.ThreadItem{{Type: "message", Text: "one"}, {Type: "message", Text: "two"}, {Type: "message", Text: "three"}, {Type: "message", Text: "four"}}
			got := subagentPreviewFromThread(appwire.Thread{Serf: appwire.SerfThread{Ref: "fallback"}, Turns: []appwire.Turn{{Items: items}}}, "", 2)
			if !got.Truncated || got.Ref != "fallback" || len(got.Items) != 2 {
				t.Fatalf("preview=%+v", got)
			}
			got = subagentPreviewFromThread(appwire.Thread{Turns: []appwire.Turn{{Items: items[:1]}}}, "explicit", 5)
			if got.Truncated || got.Ref != "explicit" || len(got.Items) != 1 {
				t.Fatalf("short preview=%+v", got)
			}
		case 7:
			if clampSubagentPreviewLimit(-1) != subagentPreviewDefaultLimit || clampSubagentPreviewLimit(99) != subagentPreviewMaxLimit || clampSubagentPreviewLimit(2) != 2 {
				t.Fatal("limit clamp")
			}
		case 8:
			if got := threadRef(appwire.Thread{Serf: appwire.SerfThread{Ref: " direct "}}); got != " direct " {
				t.Fatalf("direct ref=%q", got)
			}
			if got := threadRef(appwire.Thread{Source: "src", ID: "id"}); got == "" {
				t.Fatal("constructed ref empty")
			}
			if got := threadRef(appwire.Thread{Source: "src", SessionID: "sid"}); got == "" {
				t.Fatal("session ref empty")
			}
			if got := threadRef(appwire.Thread{ID: "id"}); got != "" {
				t.Fatalf("incomplete ref=%q", got)
			}
		case 9:
			valid := appwire.Ref{SourceID: "src", ThreadID: "id"}.String()
			if got := transcriptTargetSource(valid, "fallback"); got != "src" {
				t.Fatalf("parsed source=%q", got)
			}
			if got := transcriptTargetSource("bad", "fallback"); got != "fallback" {
				t.Fatalf("fallback=%q", got)
			}
		case 10:
			for _, tc := range []struct{ harness, want string }{{"", ""}, {"serf", "local"}, {" codex ", "codex"}} {
				if got := launchSourceID(appwire.ThreadStartParams{Harness: tc.harness}); got != tc.want {
					t.Fatalf("launchSourceID=%q want %q", got, tc.want)
				}
			}
			if threadForkRequiresTurnCapability(appwire.ThreadForkParams{}) ||
				!threadForkRequiresTurnCapability(appwire.ThreadForkParams{SourceTurnID: "1"}) ||
				!threadForkRequiresTurnCapability(appwire.ThreadForkParams{EditedInput: "x"}) ||
				!threadForkRequiresTurnCapability(appwire.ThreadForkParams{Label: "x"}) {
				t.Fatal("fork capability classification")
			}
		case 11:
			reg := appsource.NewRegistry()
			if _, err := sourceForThread(reg, "", ""); err == nil {
				t.Fatal("missing local source accepted")
			}
			local := &scriptedAppSource{id: "local"}
			reg.Add(local)
			for _, tc := range []struct{ ref, id string }{{"", ""}, {"", "id"}, {appwire.Ref{SourceID: "local", ThreadID: "id"}.String(), ""}} {
				got, err := sourceForThread(reg, tc.ref, tc.id)
				if err != nil || got != local {
					t.Fatalf("sourceForThread(%q,%q)=%v,%v", tc.ref, tc.id, got, err)
				}
			}
		case 12:
			if relayOnThreadRead(&readRelayDisabledSource{}) {
				t.Fatal("disabled relay policy ignored")
			}
			if !relayOnThreadRead(&scriptedAppSource{id: "plain"}) {
				t.Fatal("default relay policy disabled")
			}
		case 13:
			if isSessionUnavailableError(nil) || isSessionUnavailableError(assertionError(text)) {
				t.Fatal("non-wire error classified unavailable")
			}
			if !isSessionUnavailableError(appwire.SessionUnavailable("gone")) {
				t.Fatal("session-unavailable wire error not recognized")
			}
			if isSessionUnavailableError(appwire.Unavailable("action")) || isSessionUnavailableError(appwire.InvalidParams("bad")) {
				t.Fatal("wrong wire error classified session unavailable")
			}
			if !shouldResumeAfterTurnStartError(appwire.SessionUnavailable("gone")) || !shouldResumeAfterSessionUnavailable(appwire.SessionUnavailable("gone")) {
				t.Fatal("resume wrappers rejected session-unavailable")
			}
		}
	})
}

type assertionError string

func (e assertionError) Error() string { return string(e) }

func FuzzCovRPCThreadsHandlers(f *testing.F) {
	for selector := byte(0); selector < 11; selector++ {
		f.Add(selector)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		ctx := context.Background()
		switch selector % 11 {
		case 0, 1, 2, 3, 4:
			registry := appsource.NewRegistry()
			if selector%11 == 4 {
				registry.Add(&previewScriptedSource{thread: appwire.Thread{
					ID: "child", Serf: appwire.SerfThread{Ref: appwire.Ref{SourceID: "local", ThreadID: "child"}.String()},
					Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{Type: "message", Text: "done"}}}},
				}})
			}
			web := NewWebServer(hubcore.WebConfig{})
			web.sources = registry
			method, target := http.MethodGet, "/_api/subagent-preview?ref=local%3Achild&limit=1"
			switch selector % 11 {
			case 0:
				method = http.MethodPost
			case 1:
				target = "/_api/subagent-preview"
			case 2:
				target = "/_api/subagent-preview?ref=local%3Achild&limit=nope"
			case 3:
				target = "/_api/subagent-preview?ref=missing%3Achild"
			}
			rec := httptest.NewRecorder()
			web.handleSubagentPreview(rec, httptest.NewRequest(method, target, nil))
			want := []int{http.StatusMethodNotAllowed, http.StatusBadRequest, http.StatusBadRequest, http.StatusNotFound, http.StatusOK}[selector%11]
			if rec.Code != want {
				t.Fatalf("preview status=%d want %d body=%s", rec.Code, want, rec.Body.String())
			}
		case 5:
			source := &scriptedAppSource{id: "local", thread: appwire.Thread{Serf: appwire.SerfThread{Capabilities: appwire.ThreadCapabilities{}}}}
			if err := ensureThreadActionAvailable(ctx, source, "local:id", "", "compact"); err == nil {
				t.Fatal("compact accepted without capability")
			}
			source.thread.Serf.Capabilities.Compact = true
			if err := ensureThreadActionAvailable(ctx, source, "local:id", "", "compact"); err != nil {
				t.Fatalf("compact capability rejected: %v", err)
			}
		case 6:
			registry := appsource.NewRegistry()
			if err := compactThreadOnce(ctx, hubcore.WebConfig{}, registry, appwire.ThreadCompactStartParams{Ref: "missing:id"}); err == nil {
				t.Fatal("compact resolved missing source")
			}
			source := &scriptedAppSource{id: "local", thread: appwire.Thread{Serf: appwire.SerfThread{Capabilities: appwire.ThreadCapabilities{Compact: true}}}}
			registry.Add(source)
			err := compactThreadOnce(ctx, hubcore.WebConfig{}, registry, appwire.ThreadCompactStartParams{Ref: appwire.Ref{SourceID: "local", ThreadID: "id"}.String()})
			if err == nil {
				t.Fatal("scripted compact unexpectedly succeeded")
			}
		case 7:
			if _, err := hubCommandList(hubcore.WebConfig{}); err != nil {
				t.Fatal(err)
			}
		case 8:
			server := appserver.NewServer(appserver.ServerConfig{})
			notifyMarketplaceUpdated(server)
			notifyPluginUpdated(server)
		case 9:
			writer := &failingResponseWriter{header: make(http.Header)}
			writeJSON(writer, map[string]string{"ok": "yes"})
			if writer.status != http.StatusInternalServerError {
				t.Fatalf("writeJSON status=%d", writer.status)
			}
		case 10:
			registry := appsource.NewRegistry()
			registry.Add(&previewErrorSource{})
			web := NewWebServer(hubcore.WebConfig{})
			web.sources = registry
			rec := httptest.NewRecorder()
			web.handleSubagentPreview(rec, httptest.NewRequest(http.MethodGet, "/_api/subagent-preview?ref=local%3Achild", nil))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("read-error preview status=%d body=%s", rec.Code, rec.Body.String())
			}
		}
	})
}

type previewErrorSource struct{ relayLifecycleSource }

func (*previewErrorSource) ID() string { return "local" }
func (*previewErrorSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, errors.New("scripted read failure")
}

type failingResponseWriter struct {
	header http.Header
	status int
}

func (w *failingResponseWriter) Header() http.Header  { return w.header }
func (w *failingResponseWriter) WriteHeader(code int) { w.status = code }
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("scripted write failure")
}
