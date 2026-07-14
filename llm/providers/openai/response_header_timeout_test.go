package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

type withheldHeadersFixture struct {
	server       *httptest.Server
	firstRequest <-chan struct{}
	requests     *atomic.Int32
}

func newWithheldHeadersFixture(t *testing.T) withheldHeadersFixture {
	t.Helper()

	firstRequest := make(chan struct{})
	releaseHandlers := make(chan struct{})
	requests := &atomic.Int32{}
	var firstRequestOnce sync.Once
	var releaseOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		firstRequestOnce.Do(func() { close(firstRequest) })
		<-releaseHandlers
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseHandlers) })
		server.Close()
	})

	return withheldHeadersFixture{
		server:       server,
		firstRequest: firstRequest,
		requests:     requests,
	}
}

func (f withheldHeadersFixture) waitForFirstRequest(t *testing.T) {
	t.Helper()
	select {
	case <-f.firstRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive the streaming request")
	}
}

func TestAdapter_Stream_ResponseHeaderTimeout(t *testing.T) {
	fixture := newWithheldHeadersFixture(t)

	adapter := &Adapter{APIKey: "test-key", BaseURL: fixture.server.URL, Client: fixture.server.Client()}
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

	fixture.waitForFirstRequest(t)

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
			t.Fatal("response-header timeout must be retryable after the watchdog ends the attempt")
		}
		if got := llm.Classify(err); got != llm.ErrorClassRetryable {
			t.Fatalf("Classify(error) = %v, want %v", got, llm.ErrorClassRetryable)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after AdapterTimeout.Request")
	}
}

func TestStreamGenerate_ResponseHeaderTimeoutRetriesConfiguredAttempts(t *testing.T) {
	fixture := newWithheldHeadersFixture(t)
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: fixture.server.URL,
		Client:  fixture.server.Client(),
	}
	client := llm.NewClient()
	client.Register(adapter)
	prompt := "hello"
	result, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
		Client:         client,
		Model:          "gpt-5",
		Provider:       "openai",
		Prompt:         &prompt,
		AdapterTimeout: &llm.AdapterTimeout{Request: 50 * time.Millisecond, StreamRead: time.Second},
		RetryPolicy: &llm.RetryPolicy{
			MaxRetries: 2,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
			Jitter:     false,
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer func() { _ = result.Close() }()

	fixture.waitForFirstRequest(t)

	for range result.Events() {
	}
	_, resultErr := result.Response()
	if resultErr == nil || llm.Kind(resultErr) != llm.KindTimeout {
		t.Fatalf("error = %v, want timeout", resultErr)
	}
	if got := fixture.requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (initial request plus two retries)", got)
	}
}

func TestStreamGenerate_ResponseHeaderTimeoutRetryStopsOnCancellation(t *testing.T) {
	fixture := newWithheldHeadersFixture(t)
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: fixture.server.URL,
		Client:  fixture.server.Client(),
	}
	client := llm.NewClient()
	client.Register(adapter)
	prompt := "hello"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleepCalls atomic.Int32

	result, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
		Client:         client,
		Model:          "gpt-5",
		Provider:       "openai",
		Prompt:         &prompt,
		AdapterTimeout: &llm.AdapterTimeout{Request: 50 * time.Millisecond, StreamRead: time.Second},
		RetryPolicy: &llm.RetryPolicy{
			MaxRetries: 10,
			BaseDelay:  time.Second,
			MaxDelay:   time.Second,
			Jitter:     false,
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			sleepCalls.Add(1)
			cancel()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer func() { _ = result.Close() }()

	fixture.waitForFirstRequest(t)
	for range result.Events() {
	}
	_, resultErr := result.Response()
	if !errors.Is(resultErr, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", resultErr)
	}
	if got := fixture.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 after cancellation", got)
	}
	if got := sleepCalls.Load(); got != 1 {
		t.Fatalf("sleep calls = %d, want 1", got)
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
