package anthropic

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
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type wireCaptureSink struct {
	attempts []apilog.APIAttemptRecord
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close() //nolint:errcheck // Test transport honors the RoundTripper request-body contract.
	}
	return f(request)
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

type cancellationOrderingSink struct {
	mu                sync.Mutex
	attempts          []apilog.APIAttemptRecord
	appendEntered     chan struct{}
	releaseAppend     chan struct{}
	settlementEntered chan struct{}
}

func (s *cancellationOrderingSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	close(s.appendEntered)
	<-s.releaseAppend
	s.mu.Lock()
	s.attempts = append(s.attempts, record)
	s.mu.Unlock()
	return nil
}

func (s *cancellationOrderingSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	close(s.settlementEntered)
	return nil
}

func (s *cancellationOrderingSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestStreamWireCaptureRecordsExactAttemptBeforeFinish(t *testing.T) {
	responseBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
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
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_stream_wire")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hello")}})
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

func TestStreamingHappensBeforeSuccessPublication(t *testing.T) {
	responseBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	sink := &cancellationOrderingSink{
		appendEntered:     make(chan struct{}),
		releaseAppend:     make(chan struct{}),
		settlementEntered: make(chan struct{}),
	}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_success_order")),
		sink,
	)
	stream, err := (&Adapter{APIKey: "test-key", BaseURL: server.URL, Client: server.Client()}).Stream(
		ctx,
		llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hello")}},
	)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	terminal := make(chan llm.StreamEvent, 1)
	go func() {
		for event := range stream.Events() {
			if event.Type == llm.StreamEventError || event.Type == llm.StreamEventFinish {
				terminal <- event
			}
		}
	}()

	select {
	case <-sink.appendEntered:
	case <-time.After(time.Second):
		t.Fatal("successful stream did not reach canonical append")
	}
	select {
	case event := <-terminal:
		t.Fatalf("terminal event %q was published before canonical append returned", event.Type)
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.releaseAppend)
	select {
	case event := <-terminal:
		if event.Type != llm.StreamEventFinish {
			t.Fatalf("terminal event = %q/%v, want finish", event.Type, event.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("successful stream did not publish finish after append returned")
	}
	attempts := sink.snapshot()
	if len(attempts) != 1 || attempts[0].Outcome != apilog.AttemptSuccess {
		t.Fatalf("canonical attempts = %+v, want one success", attempts)
	}
}

func TestStreamWithoutAPIAttemptPublishesFinishBeforeTerminalCancelReturns(t *testing.T) {
	responseBody := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	defer close(releaseCancel)
	cancel := func() {
		close(cancelEntered)
		<-releaseCancel
	}
	stream := llm.NewChanStream(nil)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}
	go (&Adapter{BaseURL: "https://anthropic.test"}).decodeStream(
		context.Background(), cancel, response, stream,
		llm.Request{Model: "claude-test"}, nil, nil,
	)

	select {
	case <-cancelEntered:
	case <-time.After(time.Second):
		t.Fatal("stream decoder did not reach terminal cancellation")
	}
	for {
		select {
		case event := <-stream.Events():
			if event.Type == llm.StreamEventFinish {
				return
			}
		default:
			t.Fatal("finish was delayed until terminal cancel returned without an active API attempt")
		}
	}
}

func TestStreamWireCaptureClassifiesSSEReadTimeoutAsProviderTimeout(t *testing.T) {
	body := &blockingReadCloser{closed: make(chan struct{})}
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: "https://anthropic.test",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
			}, nil
		})},
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_sse_timeout")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{
		Model:          "claude-test",
		Messages:       []llm.Message{llm.User("hello")},
		AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events() { //nolint:revive // Drain to the terminal timeout evidence.
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	if got := sink.attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
		t.Fatalf("SSE-read timeout outcome = %q, want provider_timeout", got)
	}
}

func TestStreamingHappensBeforeCancellationAndSettlement(t *testing.T) {
	body := &blockingReadCloser{closed: make(chan struct{})}
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: "https://anthropic.test",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
			}, nil
		})},
	}
	sink := &cancellationOrderingSink{
		appendEntered:     make(chan struct{}),
		releaseAppend:     make(chan struct{}),
		settlementEntered: make(chan struct{}),
	}
	group := llm.NewAPIAttemptGroup("ag_anthropic_cancel_order")
	parentCtx, cancel := context.WithCancel(context.Background())
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(parentCtx, group), sink)
	stream, err := adapter.Stream(ctx, llm.Request{
		Model:          "claude-test",
		Messages:       []llm.Message{llm.User("hello")},
		AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Second},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	terminal := make(chan llm.StreamEvent, 1)
	streamClosed := make(chan struct{})
	go func() {
		defer close(streamClosed)
		for event := range stream.Events() {
			if event.Type == llm.StreamEventError || event.Type == llm.StreamEventFinish {
				terminal <- event
			}
		}
	}()

	cancel()
	select {
	case <-sink.appendEntered:
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not reach canonical append")
	}
	settled := make(chan struct{})
	go func() {
		group.Settle(ctx, apilog.AttemptCallerCancel)
		close(settled)
	}()
	select {
	case event := <-terminal:
		t.Fatalf("terminal event %q was published before canonical append returned", event.Type)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-sink.settlementEntered:
		t.Fatal("group settlement append began before canonical attempt append returned")
	case <-time.After(50 * time.Millisecond):
	}

	close(sink.releaseAppend)
	select {
	case event := <-terminal:
		if event.Type != llm.StreamEventError || !errors.Is(event.Err, context.Canceled) {
			t.Fatalf("terminal event = %q/%v, want caller cancellation", event.Type, event.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not publish terminal error after append returned")
	}
	select {
	case <-sink.settlementEntered:
	case <-time.After(time.Second):
		t.Fatal("group settlement did not begin after attempt append returned")
	}
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("group settlement did not finish")
	}
	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not close")
	}
	attempts := sink.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(attempts))
	}
	if got := attempts[0].Outcome; got != apilog.AttemptCallerCancel {
		t.Fatalf("cancelled stream outcome = %q, want caller_cancellation", got)
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
	const responseBody = `{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
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
		name:              "anthropic-test",
		APIKey:            "secret-api-key",
		BaseURL:           server.URL,
		Client:            server.Client(),
		DefaultHeaders:    map[string]string{"X-Visible": "visible-value"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret-gateway-key"},
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_wire")),
		sink,
	)

	response, err := adapter.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hello")}})
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
	if record.ProviderInstance != "anthropic-test" || record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt provenance/outcome = %q/%q", record.ProviderInstance, record.Outcome)
	}
	if got := record.Request.Headers["X-Visible"]; len(got) != 1 || got[0] != "visible-value" {
		t.Fatalf("visible headers = %#v, want one visible-value", got)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	for _, secret := range []string{"secret-api-key", "secret-gateway-key", "X-Gateway-Key", "X-Api-Key"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("canonical attempt contains credential sentinel %q: %s", secret, encoded)
		}
	}
}
