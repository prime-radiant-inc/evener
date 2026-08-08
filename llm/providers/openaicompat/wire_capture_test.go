package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type wireCaptureSink struct {
	attempts []apilog.APIAttemptRecord
}

type blockingWireCaptureSink struct {
	mu           sync.Mutex
	attempts     []apilog.APIAttemptRecord
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func (s *blockingWireCaptureSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	if record.AttemptIndex == 1 {
		close(s.firstEntered)
		<-s.releaseFirst
	}
	s.mu.Lock()
	s.attempts = append(s.attempts, record)
	s.mu.Unlock()
	return nil
}

func (*blockingWireCaptureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *blockingWireCaptureSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func TestStreamWireCaptureRecordsExactAttemptBeforeFinish(t *testing.T) {
	responseBody := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{APIKey: "secret-api-key", BaseURL: server.URL, Client: server.Client()}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_compat_stream_wire")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "compat-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var finish *llm.Response
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", event.Err)
		}
		if event.Type == llm.StreamEventFinish {
			finish = event.Response
			if len(sink.attempts) != 1 {
				t.Fatalf("attempts visible at finish = %d, want 1", len(sink.attempts))
			}
		}
	}
	if finish == nil || finish.Text() != "hello" {
		t.Fatalf("finish response = %#v, want hello", finish)
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	gotRequestBody, err := apilog.DecodeBody(record.Request.Body)
	if err != nil {
		t.Fatalf("decode recorded request: %v", err)
	}
	if !bytes.Equal(gotRequestBody, requestBody) {
		t.Fatalf("recorded request bytes = %q, want server bytes %q", gotRequestBody, requestBody)
	}
	gotResponseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if !bytes.Equal(gotResponseBody, []byte(responseBody)) {
		t.Fatalf("recorded response bytes = %q, want %q", gotResponseBody, responseBody)
	}
	if record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want success", record.Outcome)
	}
}

func TestAdaptiveCompleteFallbackWaitsForResponsesAttemptAppend(t *testing.T) {
	chatStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"model not supported"}}`)
		case "/chat/completions":
			chatStarted <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"message":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{name: "compat-test", APIKey: "test-key", BaseURL: server.URL, Client: server.Client(), Adaptive: true}
	sink := &blockingWireCaptureSink{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_compat_fallback_order")),
		sink,
	)
	type completeResult struct {
		response llm.Response
		err      error
	}
	done := make(chan completeResult, 1)
	go func() {
		response, err := adapter.Complete(ctx, llm.Request{Model: "compat-test", Messages: []llm.Message{llm.User("hello")}})
		done <- completeResult{response: response, err: err}
	}()

	select {
	case <-sink.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("Responses attempt did not enter canonical append")
	}
	select {
	case <-chatStarted:
		t.Fatal("Chat fallback started before Responses attempt append returned")
	default:
	}
	close(sink.releaseFirst)

	var result completeResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("adaptive fallback completion did not return")
	}
	if result.err != nil || result.response.Text() != "fallback" {
		t.Fatalf("fallback result = %q, %v", result.response.Text(), result.err)
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("canonical attempts = %d, want 2", len(attempts))
	}
	if attempts[0].AttemptGroupID != attempts[1].AttemptGroupID ||
		attempts[0].AttemptIndex != 1 || attempts[1].AttemptIndex != 2 ||
		attempts[0].Outcome != apilog.AttemptProviderReject || attempts[1].Outcome != apilog.AttemptSuccess {
		t.Fatalf("fallback attempt sequence = %+v", attempts)
	}
	for i, attempt := range attempts {
		if attempt.ProviderInstance != "compat-test" {
			t.Fatalf("attempt %d provider instance = %q, want compat-test", i+1, attempt.ProviderInstance)
		}
	}
}

func TestStreamingHappensBeforeRetryPreservesGroupAndPolicy(t *testing.T) {
	const successfulBody = "data: {\"id\":\"chatcmpl-2\",\"model\":\"compat-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"
	var requests atomic.Int32
	secondStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			_, _ = io.WriteString(w, "data: not-json\n\n")
			return
		}
		if call == 2 {
			close(secondStarted)
			_, _ = io.WriteString(w, successfulBody)
			return
		}
		http.Error(w, "unexpected extra retry", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	sink := &blockingWireCaptureSink{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	groupID := "ag_compat_stream_retry_order"
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
		sink,
	)
	adapter := &Adapter{APIKey: "test-key", BaseURL: server.URL, Client: server.Client()}
	var retryNotifications atomic.Int32
	var sleepCalls atomic.Int32
	var attemptCalls atomic.Int32
	secondAttemptEntered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- llm.RetryStream(ctx, llm.RetryStreamOptions{
			Policy: llm.RetryPolicy{
				MaxRetries:        1,
				BaseDelay:         time.Millisecond,
				MaxDelay:          time.Millisecond,
				BackoffMultiplier: 2,
				OnRetry: func(error, int, time.Duration) {
					retryNotifications.Add(1)
				},
			},
			Sleep: func(context.Context, time.Duration) error {
				sleepCalls.Add(1)
				return nil
			},
		}, func(attemptCtx context.Context) (llm.AttemptReport, error) {
			if attemptCalls.Add(1) == 2 {
				close(secondAttemptEntered)
			}
			stream, err := adapter.Stream(attemptCtx, llm.Request{
				Model:    "compat-test",
				Messages: []llm.Message{llm.User("hello")},
			})
			if err != nil {
				return llm.AttemptReport{}, err
			}
			partial := false
			for event := range stream.Events() {
				if event.Type == llm.StreamEventTextDelta {
					partial = true
				}
				if event.Type == llm.StreamEventError {
					return llm.AttemptReport{PartialOutput: partial}, event.Err
				}
			}
			return llm.AttemptReport{PartialOutput: partial}, nil
		})
	}()

	select {
	case <-sink.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first stream attempt did not reach canonical append")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests while first append blocked = %d, want 1", got)
	}
	if got := attemptCalls.Load(); got != 1 {
		t.Fatalf("RetryStream attempt calls while first append blocked = %d, want 1", got)
	}
	if got := retryNotifications.Load(); got != 0 {
		t.Fatalf("retry notifications while first append blocked = %d, want 0", got)
	}
	if got := sleepCalls.Load(); got != 0 {
		t.Fatalf("retry sleeps while first append blocked = %d, want 0", got)
	}
	select {
	case <-secondAttemptEntered:
		t.Fatal("second RetryStream attempt entered before first canonical append returned")
	default:
	}
	select {
	case <-secondStarted:
		t.Fatal("second stream HTTP request started before first canonical append returned")
	default:
	}

	close(sink.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RetryStream: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RetryStream did not finish after canonical append was released")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (initial plus one retry)", got)
	}
	if got := attemptCalls.Load(); got != 2 {
		t.Fatalf("RetryStream attempt calls = %d, want 2", got)
	}
	if got := retryNotifications.Load(); got != 1 {
		t.Fatalf("retry notifications = %d, want 1", got)
	}
	if got := sleepCalls.Load(); got != 1 {
		t.Fatalf("retry sleeps = %d, want 1", got)
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("canonical attempts = %d, want 2", len(attempts))
	}
	for i, attempt := range attempts {
		if attempt.AttemptGroupID != groupID || attempt.AttemptIndex != i+1 {
			t.Fatalf("attempt %d group/index = %q/%d, want %q/%d", i+1, attempt.AttemptGroupID, attempt.AttemptIndex, groupID, i+1)
		}
	}
	if attempts[0].Outcome != apilog.AttemptDecodeFail || attempts[1].Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt outcomes = %q, %q; want response_decoding_failure, success", attempts[0].Outcome, attempts[1].Outcome)
	}
}

func TestStreamWireCaptureClassifiesMalformedSSEAsDecodeFailure(t *testing.T) {
	const responseBody = "data: not-json\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	adapter := &Adapter{APIKey: "test-key", BaseURL: server.URL, Client: server.Client()}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_compat_malformed_sse")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "compat-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events() { //nolint:revive // Drain to terminal decode evidence.
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	if got := sink.attempts[0].Outcome; got != apilog.AttemptDecodeFail {
		t.Fatalf("malformed SSE outcome = %q, want response_decoding_failure", got)
	}
	gotBody, err := apilog.DecodeBody(sink.attempts[0].Response.Body)
	if err != nil || !bytes.Equal(gotBody, []byte(responseBody)) {
		t.Fatalf("recorded response body = %q, %v", gotBody, err)
	}
}

func (s *wireCaptureSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.attempts = append(s.attempts, record)
	return nil
}

func (*wireCaptureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func TestCompleteWireCaptureRecordsExactCredentialFreeAttempt(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{
		name:              "compat-test",
		APIKey:            "secret-api-key",
		BaseURL:           server.URL,
		Client:            server.Client(),
		DefaultHeaders:    map[string]string{"X-Visible": "visible-value"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret-gateway-key"},
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_compat_wire")),
		sink,
	)

	response, err := adapter.Complete(ctx, llm.Request{Model: "compat-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text() != "hello" {
		t.Fatalf("response text = %q, want hello", response.Text())
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	gotRequestBody, err := apilog.DecodeBody(record.Request.Body)
	if err != nil {
		t.Fatalf("decode recorded request: %v", err)
	}
	if !bytes.Equal(gotRequestBody, requestBody) {
		t.Fatalf("recorded request bytes = %q, want server bytes %q", gotRequestBody, requestBody)
	}
	gotResponseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if !bytes.Equal(gotResponseBody, []byte(responseBody)) {
		t.Fatalf("recorded response bytes = %q, want %q", gotResponseBody, responseBody)
	}
	if record.ProviderInstance != "compat-test" || record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt provenance/outcome = %q/%q", record.ProviderInstance, record.Outcome)
	}
	if got := record.Request.Headers["X-Visible"]; len(got) != 1 || got[0] != "visible-value" {
		t.Fatalf("visible headers = %#v, want one visible-value", got)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	for _, secret := range []string{"secret-api-key", "secret-gateway-key", "X-Gateway-Key", "Authorization"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("canonical attempt contains credential sentinel %q: %s", secret, encoded)
		}
	}
}
