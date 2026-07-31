package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

// TestSessionImageServesAStillRunningSession pins that /s/<id>/images/<sha>
// answers for a session that is CURRENTLY LIVE, not only one that has exited.
//
// The daemon writes its meta.json at session init, before the first turn, and
// PastIndex indexes any meta it can see — "past" names where the hub reads
// from, not whether the session has ended. That is what makes the sha route
// usable by a live watcher at all, and it is load-bearing enough for the live
// tool-result thumbnail path to deserve a guard of its own: nothing else in
// this file exercises a session the roster also reports as running.
func TestSessionImageServesAStillRunningSession(t *testing.T) {
	const sessionID = "02wMz5Txv733WHFsVy66SR"
	root := t.TempDir()
	project := filepath.Join(root, "projects", "live-image-0123456789")
	if err := os.MkdirAll(filepath.Join(project, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID: sessionID, UpdatedAt: time.Now(), OriginalPrompt: "still running",
	}); err != nil {
		t.Fatal(err)
	}

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 's', 'h', 'o', 't'}
	w, err := transcript.NewWriter(
		filepath.Join(project, "sessions", sessionID+".transcript.jsonl"),
		transcript.Header{SessionID: sessionID, ProfileID: "openai", Model: "gpt-5"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_shot", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_shot", Name: "screenshot", Content: "captured",
				ImageData: png, ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr: "127.0.0.1:9180",
		Past:    past,
		Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			Entry:     rendezvous.Entry{PID: 4242, SessionID: sessionID},
			SessionID: sessionID,
			Status:    appwire.ThreadStatusActive,
		}),
	})
	if _, live := web.cfg.Roster.Find(sessionID); !live {
		t.Fatal("fixture is not exercising a live session: roster does not report it")
	}

	req := httptest.NewRequest(http.MethodGet, "/s/"+sessionID+"/images/"+imageSha(png), nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("live session image = status %d body %q, want 200", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("live session image body = %q, want the tool result's bytes", rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
}
