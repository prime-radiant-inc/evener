package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestSubagentPreviewBoundsLatestDirectItems(t *testing.T) {
	resp := subagentPreviewFromThread(appwire.Thread{
		Serf: appwire.SerfThread{Ref: "local:child-preview"},
		Turns: []appwire.Turn{
			{ID: "turn_1", Items: []appwire.ThreadItem{
				{Type: "agentMessage", Text: "older item"},
				{Type: "agentMessage", Text: "found three callers"},
			}},
			{ID: "turn_2", Items: []appwire.ThreadItem{
				{Type: "commandExecution", ToolName: "grep_files", Description: "search billing", Status: "completed"},
				{Type: "agentMessage", Text: "recommended fix"},
			}},
		},
	}, "local:child-preview", 3)

	if resp.Ref != "local:child-preview" {
		t.Fatalf("ref=%q", resp.Ref)
	}
	if !resp.Truncated {
		t.Fatal("expected truncated preview when older direct items are hidden")
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items=%d, want 3: %+v", len(resp.Items), resp.Items)
	}
	texts := []string{resp.Items[0].Text, resp.Items[1].Description, resp.Items[2].Text}
	want := []string{"found three callers", "search billing", "recommended fix"}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("item %d=%q, want %q (items=%+v)", i, texts[i], want[i], resp.Items)
		}
	}
	if resp.Items[0].ID != "" || resp.Items[0].TurnID != "" || resp.Items[0].Raw != nil {
		t.Fatalf("preview leaked non-display identity/raw fields: %+v", resp.Items[0])
	}
}

func TestSubagentPreviewClampLimit(t *testing.T) {
	thread := appwire.Thread{Turns: []appwire.Turn{{ID: "turn_1", Items: []appwire.ThreadItem{
		{Type: "agentMessage", Text: "one"},
		{Type: "agentMessage", Text: "two"},
		{Type: "agentMessage", Text: "three"},
		{Type: "agentMessage", Text: "four"},
		{Type: "agentMessage", Text: "five"},
		{Type: "agentMessage", Text: "six"},
	}}}}
	if got := len(subagentPreviewFromThread(thread, "local:child", 0).Items); got != 3 {
		t.Fatalf("default limit items=%d, want 3", got)
	}
	if got := len(subagentPreviewFromThread(thread, "local:child", 99).Items); got != 5 {
		t.Fatalf("clamped max items=%d, want 5", got)
	}
	if got := len(subagentPreviewFromThread(thread, "local:child", -2).Items); got != 3 {
		t.Fatalf("negative limit should default to 3, got %d", got)
	}
}

func TestSubagentPreviewEndpointReadsRefWithoutChangingJobSemantics(t *testing.T) {
	fake := &previewScriptedSource{thread: appwire.Thread{
		ID: "child-preview", SessionID: "child-preview", Serf: appwire.SerfThread{Ref: "local:child-preview"},
		Turns: []appwire.Turn{{ID: "turn_1", Items: []appwire.ThreadItem{
			{Type: "agentMessage", Text: "first"},
			{Type: "commandExecution", ToolName: "grep_files", Description: "search billing", Status: "completed"},
			{Type: "agentMessage", Text: "third"},
			{Type: "agentMessage", Text: "fourth"},
		}}},
	}}
	sources := appsource.NewRegistry()
	sources.Add(fake)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.sources = sources

	req := httptest.NewRequest(http.MethodGet, "/_api/subagent-preview?ref=local%3Achild-preview&limit=2", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.readParams.Ref != "local:child-preview" || !fake.readParams.IncludeTurns || fake.readParams.ItemsView != "full" {
		t.Fatalf("read params=%+v", fake.readParams)
	}
	if strings.Contains(rec.Body.String(), "first") || strings.Contains(rec.Body.String(), "search billing") {
		t.Fatalf("response should be bounded to latest two items, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "third") || !strings.Contains(rec.Body.String(), "fourth") {
		t.Fatalf("response missing expected preview snippets: %s", rec.Body.String())
	}
}

type previewScriptedSource struct {
	relayLifecycleSource
	thread     appwire.Thread
	readParams appwire.ThreadReadParams
}

func (s *previewScriptedSource) ID() string { return "local" }

func (s *previewScriptedSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.readParams = params
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}
