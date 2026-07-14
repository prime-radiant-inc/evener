package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestAdapter_Stream_ResponseHeaderTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHandler) })
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	t.Cleanup(func() {
		release()
		srv.Close()
	})

	adapter := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		stream, err := adapter.Stream(ctx, llm.Request{
			Model:          "gpt-5",
			Messages:       []llm.Message{llm.User("hello")},
			AdapterTimeout: &llm.AdapterTimeout{Request: 50 * time.Millisecond, StreamRead: time.Second},
		})
		if stream != nil {
			_ = stream.Close()
		}
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive the streaming request")
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Stream returned nil error while the server withheld headers")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context deadline exceeded", err)
		}
		if llm.Kind(err) != llm.KindTimeout {
			t.Fatalf("Kind(error) = %v, want %v", llm.Kind(err), llm.KindTimeout)
		}
		var providerErr llm.Error
		if !errors.As(err, &providerErr) {
			t.Fatalf("error = %T, want llm.Error", err)
		}
		if !providerErr.Retryable() {
			t.Fatal("response-header timeout must be retryable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after AdapterTimeout.Request")
	}
}

func TestAdapter_Stream_RequestTimeoutStopsAtResponseHeaders(t *testing.T) {
	const requestTimeout = 250 * time.Millisecond
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseBody) })
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		<-releaseBody
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		flusher.Flush()
	}))
	t.Cleanup(func() {
		release()
		srv.Close()
	})

	adapter := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := adapter.Stream(ctx, llm.Request{
		Model:          "gpt-5",
		Messages:       []llm.Message{llm.User("hello")},
		AdapterTimeout: &llm.AdapterTimeout{Request: requestTimeout, StreamRead: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	afterRequestTimeout := time.NewTimer(2 * requestTimeout)
	defer afterRequestTimeout.Stop()
	select {
	case <-afterRequestTimeout.C:
		release()
	case <-ctx.Done():
		t.Fatalf("waiting past Request timeout: %v", ctx.Err())
	}

	gotFinish := false
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			t.Fatalf("stream error after response headers: %v", event.Err)
		}
		if event.Type == llm.StreamEventFinish {
			gotFinish = true
		}
	}
	if !gotFinish {
		t.Fatal("stream completed without a finish event")
	}
}
