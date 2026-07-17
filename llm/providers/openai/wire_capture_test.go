package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close() //nolint:errcheck // Test transport honors the RoundTripper request-body contract.
	}
	return f(request)
}

type deferredRequestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f deferredRequestRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
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

func TestResponsesStreamWireCaptureRecordsExactAttemptBeforeFinish(t *testing.T) {
	responseBody := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
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

	adapter := &Adapter{
		APIKey:              "secret-api-key",
		BaseURL:             server.URL,
		Client:              server.Client(),
		DisableChatFallback: true,
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_stream_wire")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
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

func TestChatStreamWireCaptureRecordsExactAttemptBeforeFinish(t *testing.T) {
	responseBody := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{APIKey: "secret-api-key", BaseURL: server.URL, Client: server.Client()}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_chat_stream_wire")),
		sink,
	)
	stream, err := adapter.streamViaChatCompletions(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("streamViaChatCompletions: %v", err)
	}
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", event.Err)
		}
		if event.Type == llm.StreamEventFinish && len(sink.attempts) != 1 {
			t.Fatalf("attempts visible at finish = %d, want 1", len(sink.attempts))
		}
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	gotResponseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if !bytes.Equal(gotResponseBody, []byte(responseBody)) {
		t.Fatalf("recorded response bytes = %q, want %q", gotResponseBody, responseBody)
	}
	if record.Request.EndpointFamily != "openai_chat_completions" || record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt family/outcome = %q/%q", record.Request.EndpointFamily, record.Outcome)
	}
}

func TestCompleteFallbackWaitsForResponsesAttemptAppend(t *testing.T) {
	chatStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"model not supported"}}`)
		case "/v1/chat/completions":
			chatStarted <- struct{}{}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{APIKey: "test-key", BaseURL: server.URL, Client: server.Client()}
	sink := &blockingWireCaptureSink{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_fallback_order")),
		sink,
	)
	type completeResult struct {
		response llm.Response
		err      error
	}
	done := make(chan completeResult, 1)
	go func() {
		response, err := adapter.Complete(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
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
		t.Fatal("fallback completion did not return")
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
}

func TestCompleteFallbackWaitsForResponsesRequestCredentialScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			trailerName   = "X-Gateway-Credential"
			trailerSecret = "responses-fallback-trailer-secret-sentinel"
		)
		releaseResponsesRequest := make(chan struct{})
		chatStarted := make(chan struct{}, 1)
		client := &http.Client{Transport: deferredRequestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/v1/responses":
				request.Trailer = http.Header{trailerName: nil}
				go func(body io.ReadCloser) {
					<-releaseResponsesRequest
					request.Trailer.Set(trailerName, trailerSecret)
					_, _ = io.ReadAll(body)
					_ = body.Close()
				}(request.Body)
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + trailerSecret + `"}}`)),
				}, nil
			case "/v1/chat/completions":
				chatStarted <- struct{}{}
				_, _ = io.ReadAll(request.Body)
				_ = request.Body.Close()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader(strings.Join([]string{
						`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"` + trailerSecret + `"},"finish_reason":"stop"}]}`,
						``,
						`data: [DONE]`,
						``,
						``,
					}, "\n"))),
				}, nil
			default:
				return nil, errors.New("unexpected request path")
			}
		})}
		adapter := &Adapter{
			APIKey:            "test-key",
			BaseURL:           "https://provider.test",
			Client:            client,
			CredentialHeaders: map[string]string{trailerName: "configured-placeholder"},
		}
		sink := &wireCaptureSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_fallback_credential_scope")),
			sink,
		)
		type completeResult struct {
			response llm.Response
			err      error
		}
		done := make(chan completeResult, 1)
		go func() {
			response, err := adapter.Complete(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
			done <- completeResult{response: response, err: err}
		}()

		synctest.Wait()
		select {
		case <-chatStarted:
			t.Error("Chat fallback started before the Responses request credential scope was durable")
		default:
		}
		close(releaseResponsesRequest)
		synctest.Wait()
		result := <-done
		if result.err != nil || result.response.Text() != trailerSecret {
			t.Fatalf("fallback result = %q, %v", result.response.Text(), result.err)
		}
		if len(sink.attempts) != 2 {
			t.Fatalf("canonical attempts = %d, want 2", len(sink.attempts))
		}
		for i, attempt := range sink.attempts {
			if attempt.AttemptIndex != i+1 {
				t.Fatalf("attempt %d index = %d, want %d", i+1, attempt.AttemptIndex, i+1)
			}
			encoded, err := apilog.MarshalRecord(attempt)
			if err != nil {
				t.Fatalf("marshal attempt %d: %v", i+1, err)
			}
			if bytes.Contains(encoded, []byte(trailerSecret)) {
				t.Fatalf("attempt %d retained Responses credential %q", i+1, trailerSecret)
			}
		}
	})
}

func TestStreamFallbackWaitsForResponsesAttemptAppend(t *testing.T) {
	chatStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			// A successful but empty Responses stream triggers Chat fallback.
		case "/v1/chat/completions":
			chatStarted <- struct{}{}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{APIKey: "test-key", BaseURL: server.URL, Client: server.Client()}
	sink := &blockingWireCaptureSink{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_stream_fallback_order")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	type streamResult struct {
		response *llm.Response
		err      error
	}
	done := make(chan streamResult, 1)
	go func() {
		var result streamResult
		for event := range stream.Events() {
			if event.Type == llm.StreamEventError {
				result.err = event.Err
			}
			if event.Type == llm.StreamEventFinish {
				result.response = event.Response
			}
		}
		done <- result
	}()

	select {
	case <-sink.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("Responses stream attempt did not enter canonical append")
	}
	select {
	case <-chatStarted:
		t.Fatal("Chat stream fallback started before Responses attempt append returned")
	default:
	}
	close(sink.releaseFirst)

	var result streamResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("stream fallback did not finish")
	}
	if result.err != nil || result.response == nil || result.response.Text() != "fallback" {
		t.Fatalf("stream fallback result = %#v, %v", result.response, result.err)
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 || attempts[0].AttemptIndex != 1 || attempts[1].AttemptIndex != 2 ||
		attempts[0].Outcome != apilog.AttemptDecodeFail || attempts[1].Outcome != apilog.AttemptSuccess {
		t.Fatalf("stream fallback attempt sequence = %+v", attempts)
	}
}

func TestResponsesStreamWireCaptureClassifiesSSEReadTimeoutAsProviderTimeout(t *testing.T) {
	adapter := &Adapter{
		APIKey:              "test-key",
		BaseURL:             "https://openai.test",
		DisableChatFallback: true,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &blockingReadCloser{closed: make(chan struct{})},
			}, nil
		})},
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_sse_timeout")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{
		Model:          "gpt-test",
		Messages:       []llm.Message{llm.User("hello")},
		AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events() { //nolint:revive // Drain to the terminal timeout evidence.
	}
	if len(sink.attempts) != 2 {
		t.Fatalf("canonical attempts = %d, want Responses plus existing empty-stream Chat fallback", len(sink.attempts))
	}
	for i, attempt := range sink.attempts {
		if attempt.Outcome != apilog.AttemptProviderTimeout {
			t.Fatalf("SSE-read timeout attempt %d outcome = %q, want provider_timeout", i+1, attempt.Outcome)
		}
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
	const responseBody = `{"id":"r1","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
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
		name:              "openai-test",
		APIKey:            "secret-api-key",
		BaseURL:           server.URL,
		Client:            server.Client(),
		DefaultHeaders:    map[string]string{"X-Visible": "visible-value"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret-gateway-key"},
	}
	sink := &wireCaptureSink{}
	group := llm.NewAPIAttemptGroup("ag_openai_wire")
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), group), sink)

	response, err := adapter.Complete(ctx, llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}})
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
	if record.ProviderInstance != "openai-test" || record.RequestModel != "gpt-test" {
		t.Fatalf("attempt provenance = %q/%q, want openai-test/gpt-test", record.ProviderInstance, record.RequestModel)
	}
	if record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want %q", record.Outcome, apilog.AttemptSuccess)
	}
	if got := record.Request.Headers["X-Visible"]; len(got) != 1 || got[0] != "visible-value" {
		t.Fatalf("visible headers = %#v, want one visible-value", got)
	}
	for _, secret := range []string{"secret-api-key", "secret-gateway-key", "X-Gateway-Key", "Authorization"} {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal attempt: %v", err)
		}
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("canonical attempt contains credential sentinel %q: %s", secret, encoded)
		}
	}
}
