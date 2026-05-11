package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseSSEEvents(t *testing.T) {
	input := "id: 1\nevent: ASSISTANT_TEXT_DELTA\ndata: {\"delta\":\"hello\"}\n\n" +
		"id: 2\nevent: TOOL_CALL_START\ndata: {\"tool_name\":\"shell\"}\n\n"

	r := strings.NewReader(input)
	events, err := parseSSEStream(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].ID != "1" || events[0].Event != "ASSISTANT_TEXT_DELTA" {
		t.Errorf("event 0: %+v", events[0])
	}
	if events[1].Event != "TOOL_CALL_START" {
		t.Errorf("event 1: %+v", events[1])
	}
}

func TestParseSSEEvent_SingleLine(t *testing.T) {
	input := "id: 42\nevent: SESSION_START\ndata: {\"model\":\"gpt-5\"}\n\n"
	r := strings.NewReader(input)
	events, err := parseSSEStream(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "42" {
		t.Errorf("id: got %q, want 42", events[0].ID)
	}
	if events[0].Data != `{"model":"gpt-5"}` {
		t.Errorf("data: got %q", events[0].Data)
	}
}

func TestParseSSEStream_Empty(t *testing.T) {
	events, err := parseSSEStream(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestParseSSEStream_IgnoresComments(t *testing.T) {
	input := ": this is a comment\nid: 1\nevent: SESSION_START\ndata: {}\n\n"
	events, err := parseSSEStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != "1" {
		t.Errorf("id: got %q, want 1", events[0].ID)
	}
}

func TestParseSSEStream_DataOnly(t *testing.T) {
	input := "data: {\"status\":\"ok\"}\n\n"
	events, err := parseSSEStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != `{"status":"ok"}` {
		t.Errorf("data: got %q", events[0].Data)
	}
	if events[0].Event != "" {
		t.Errorf("event should be empty, got %q", events[0].Event)
	}
}

func TestStreamSSE(t *testing.T) {
	// Set up an HTTP server that serves SSE events then closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		fmt.Fprintf(w, "id: 1\nevent: SESSION_START\ndata: {\"model\":\"test\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "id: 2\nevent: ASSISTANT_TEXT_DELTA\ndata: {\"delta\":\"hi\"}\n\n")
		flusher.Flush()
		// Handler returns, closing the connection.
	}))
	defer srv.Close()

	var mu sync.Mutex
	var msgs []tea.Msg

	send := func(msg tea.Msg) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
	}

	// Strip "http://" to get addr for streamSSE (which adds its own "http://").
	addr := strings.TrimPrefix(srv.URL, "http://")
	streamSSE(context.Background(), addr, send)

	mu.Lock()
	defer mu.Unlock()

	// Expect: sseConnectedMsg, 2x sseEventMsg, sseErrorMsg (stream closed).
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d: %+v", len(msgs), msgs)
	}

	if _, ok := msgs[0].(sseConnectedMsg); !ok {
		t.Errorf("msgs[0]: expected sseConnectedMsg, got %T", msgs[0])
	}

	ev1, ok := msgs[1].(sseEventMsg)
	if !ok {
		t.Fatalf("msgs[1]: expected sseEventMsg, got %T", msgs[1])
	}
	if ev1.Event != "SESSION_START" {
		t.Errorf("msgs[1].Event: got %q, want SESSION_START", ev1.Event)
	}

	ev2, ok := msgs[2].(sseEventMsg)
	if !ok {
		t.Fatalf("msgs[2]: expected sseEventMsg, got %T", msgs[2])
	}
	if ev2.Event != "ASSISTANT_TEXT_DELTA" {
		t.Errorf("msgs[2].Event: got %q, want ASSISTANT_TEXT_DELTA", ev2.Event)
	}

	// Last message should be sseErrorMsg (stream closed).
	last := msgs[len(msgs)-1]
	if _, ok := last.(sseErrorMsg); !ok {
		t.Errorf("last msg: expected sseErrorMsg, got %T", last)
	}
}

func TestStreamSSE_ConnectionRefused(t *testing.T) {
	var msgs []tea.Msg
	send := func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}

	// Use a port that nothing is listening on.
	streamSSE(context.Background(), "127.0.0.1:0", send)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if errMsg, ok := msgs[0].(sseErrorMsg); !ok {
		t.Errorf("expected sseErrorMsg, got %T", msgs[0])
	} else if errMsg.err == nil {
		t.Error("expected non-nil error")
	}
}

func TestParseSSEStream_LargeDataLine(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	input := "id: 1\nevent: TOOL_CALL_END\ndata: {\"output\":\"" + large + "\"}\n\n"

	events, err := parseSSEStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !strings.Contains(events[0].Data, large) {
		t.Fatal("large data payload was not preserved")
	}
}

func TestParseSSEStream_MultilineDataAndNoSpaceAfterColon(t *testing.T) {
	input := "id:42\nevent:ASSISTANT_TEXT_END\ndata:{\"text\":\"hello\"\ndata:\"world\"}\n\n"
	events, err := parseSSEStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	if events[0].ID != "42" || events[0].Event != "ASSISTANT_TEXT_END" {
		t.Fatalf("event=%+v", events[0])
	}
	if events[0].Data != "{\"text\":\"hello\"\n\"world\"}" {
		t.Fatalf("data=%q", events[0].Data)
	}
}

func TestParseSSEStream_FinalEventWithoutBlankTerminator(t *testing.T) {
	events, err := parseSSEStream(strings.NewReader("event: REPLAY_DONE\ndata: {}\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 || events[0].Event != "REPLAY_DONE" {
		t.Fatalf("events=%+v", events)
	}
}

func TestStreamSSEURL_Non200ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer srv.Close()

	var msgs []tea.Msg
	streamSSEURL(context.Background(), srv.URL, "", func(msg tea.Msg) {
		msgs = append(msgs, msg)
	})
	if len(msgs) != 1 {
		t.Fatalf("msgs=%d: %+v", len(msgs), msgs)
	}
	if _, ok := msgs[0].(sseErrorMsg); !ok {
		t.Fatalf("msg=%T", msgs[0])
	}
}

func TestStreamSSEURL_SendsLastEventID(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: REPLAY_DONE\ndata: {}\n\n")
	}))
	defer srv.Close()

	streamSSEURL(context.Background(), srv.URL, "ev-123", func(tea.Msg) {})
	if header := <-got; header != "ev-123" {
		t.Fatalf("Last-Event-ID=%q", header)
	}
}
