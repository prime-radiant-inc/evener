package openaicompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// affinityAdapter builds a direct chat-completions adapter (Adaptive off) whose
// quirks enable session-affinity headers.
func affinityAdapter(baseURL string, client *http.Client, headers map[string]string) *Adapter {
	q := ProviderQuirks{}
	q.SendSessionAffinityHeaders = true
	return &Adapter{
		APIKey:         "k",
		BaseURL:        baseURL,
		Client:         client,
		Quirks:         q,
		DefaultHeaders: headers,
	}
}

const chatCompletionOKBody = `{
  "id": "chatcmpl-1",
  "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`

func TestSessionAffinityHeaders_OnComplete(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody))
	}))
	t.Cleanup(srv.Close)

	a := affinityAdapter(srv.URL, srv.Client(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Complete(ctx, llm.Request{Model: "m", SessionID: "sess-9", Messages: []llm.Message{llm.User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	assertAffinityHeaders(t, got, "sess-9")
}

func TestSessionAffinityHeaders_OnStream(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := affinityAdapter(srv.URL, srv.Client(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := a.Stream(ctx, llm.Request{Model: "m", SessionID: "sess-9", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range s.Events() {
	}
	_ = s.Close()
	assertAffinityHeaders(t, got, "sess-9")
}

func TestSessionAffinityHeaders_AbsentWhenFlagOff(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Complete(ctx, llm.Request{Model: "m", SessionID: "sess-9", Messages: []llm.Message{llm.User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if v := got.Get("session_id"); v != "" {
		t.Errorf("session_id = %q, want absent when flag off", v)
	}
	if v := got.Get("x-session-affinity"); v != "" {
		t.Errorf("x-session-affinity = %q, want absent when flag off", v)
	}
}

func TestSessionAffinityHeaders_UserDefaultHeaderWins(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody))
	}))
	t.Cleanup(srv.Close)

	a := affinityAdapter(srv.URL, srv.Client(), map[string]string{"session_id": "user-override"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Complete(ctx, llm.Request{Model: "m", SessionID: "sess-9", Messages: []llm.Message{llm.User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if v := got.Get("session_id"); v != "user-override" {
		t.Errorf("session_id = %q, want user-override (user DefaultHeaders win)", v)
	}
	// The other derived affinity headers still ride along.
	if v := got.Get("x-session-affinity"); v != "sess-9" {
		t.Errorf("x-session-affinity = %q, want sess-9", v)
	}
}

func assertAffinityHeaders(t *testing.T, h http.Header, want string) {
	t.Helper()
	for _, name := range []string{"session_id", "x-client-request-id", "x-session-affinity"} {
		if v := h.Get(name); v != want {
			t.Errorf("header %q = %q, want %q", name, v, want)
		}
	}
}
