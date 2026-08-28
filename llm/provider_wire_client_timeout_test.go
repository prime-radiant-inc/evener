package llm_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

type lockedAttemptSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

type streamCloseAttemptSink struct {
	mu            sync.Mutex
	attempts      []apilog.APIAttemptRecord
	appendEntered chan struct{}
	releaseAppend chan struct{}
}

func (s *streamCloseAttemptSink) AppendAttempt(_ context.Context, attempt apilog.APIAttemptRecord) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	close(s.appendEntered)
	<-s.releaseAppend
	return nil
}

func (*streamCloseAttemptSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *streamCloseAttemptSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

type streamCloseResponseBody struct {
	readEntered chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

type closeCountingReadCloser struct {
	io.Reader
	mu       sync.Mutex
	count    int
	closeErr error
}

func (b *closeCountingReadCloser) Close() error {
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	return b.closeErr
}

func (b *closeCountingReadCloser) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

func (b *streamCloseResponseBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.readEntered) })
	<-b.closed
	return 0, io.EOF
}

func (b *streamCloseResponseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (s *lockedAttemptSink) AppendAttempt(_ context.Context, attempt apilog.APIAttemptRecord) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	return nil
}

func (*lockedAttemptSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *lockedAttemptSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func TestCoreStreamCloseRecordsCallerCancellationBeforeCloseReturns(t *testing.T) {
	for _, provider := range wireProviders() {
		t.Run(provider.name, func(t *testing.T) {
			body := &streamCloseResponseBody{
				readEntered: make(chan struct{}),
				closed:      make(chan struct{}),
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       body,
				}, nil
			})}
			sink := &streamCloseAttemptSink{
				appendEntered: make(chan struct{}),
				releaseAppend: make(chan struct{}),
			}
			released := false
			t.Cleanup(func() {
				if !released {
					close(sink.releaseAppend)
				}
			})
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_stream_close")),
				sink,
			)
			req := providerRequest(provider.name, "test-model")
			req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Hour}
			stream, err := provider.wireClient(t, "https://provider.test", client, nil).Stream(ctx, req)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			<-body.readEntered
			closeReturned := make(chan error, 1)
			go func() { closeReturned <- stream.Close() }()
			<-sink.appendEntered
			select {
			case err := <-closeReturned:
				t.Fatalf("Stream.Close returned before canonical append: %v", err)
			default:
			}
			attempts := sink.snapshot()
			close(sink.releaseAppend)
			released = true
			if err := <-closeReturned; err != nil {
				t.Fatalf("Stream.Close: %v", err)
			}
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts at append = %d, want exactly 1", len(attempts))
			}
			if got := attempts[0].Outcome; got != apilog.AttemptCallerCancel {
				t.Fatalf("Stream.Close attempt outcome = %q, want %q", got, apilog.AttemptCallerCancel)
			}
		})
	}
}

func TestCoreActiveFinalBodyClosesUnderlyingExactlyOnce(t *testing.T) {
	for _, provider := range wireProviders() {
		for _, streaming := range []bool{false, true} {
			name := "complete"
			if streaming {
				name = "stream"
			}
			t.Run(provider.name+"/"+name, func(t *testing.T) {
				responseBody := provider.completeBody
				contentType := "application/json"
				if streaming {
					responseBody = provider.streamBody
					contentType = "text/event-stream"
				}
				closeErr := errors.New("underlying response close sentinel")
				body := &closeCountingReadCloser{Reader: strings.NewReader(responseBody), closeErr: closeErr}
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{contentType}},
						Body:       body,
					}, nil
				})}
				sink := &lockedAttemptSink{}
				ctx := llm.WithAPIAttemptSink(
					llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_final_close_once")),
					sink,
				)
				wire := provider.wireClient(t, "https://provider.test", client, nil)
				if streaming {
					stream, err := wire.Stream(ctx, providerRequest(provider.name, "test-model"))
					if err != nil {
						t.Fatalf("Stream: %v", err)
					}
					var finish *llm.Response
					for event := range stream.Events() {
						if event.Type == llm.StreamEventFinish {
							finish = event.Response
						}
					}
					if finish == nil {
						t.Fatal("stream returned no semantic finish")
					}
				} else {
					response, err := wire.Complete(ctx, providerRequest(provider.name, "test-model"))
					if err != nil || response.Text() != "hello" {
						t.Fatalf("Complete = (%q, %v), want hello success", response.Text(), err)
					}
				}
				if got := body.closeCount(); got != 1 {
					t.Fatalf("underlying response Close count = %d, want exactly 1", got)
				}
				attempts := sink.snapshot()
				if len(attempts) != 1 || attempts[0].Outcome != apilog.AttemptSuccess {
					t.Fatalf("canonical attempts = %+v, want one semantic success", attempts)
				}
			})
		}
	}
}

func TestCoreCompleteWireCaptureWithClientTimeoutRetainsFinalSemanticOwnership(t *testing.T) {
	for _, provider := range wireProviders() {
		for _, testCase := range []struct {
			name        string
			body        string
			blockBody   bool
			wantOutcome apilog.AttemptOutcomeClass
			wantSuccess bool
		}{
			{name: "valid response metadata", body: provider.completeBody, wantOutcome: apilog.AttemptSuccess, wantSuccess: true},
			{name: "malformed 2xx", body: `{"broken"`, wantOutcome: apilog.AttemptDecodeFail},
			{name: "body read timeout", blockBody: true, wantOutcome: apilog.AttemptProviderTimeout},
		} {
			t.Run(provider.name+"/"+testCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if testCase.blockBody {
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					_, _ = w.Write([]byte(testCase.body))
				}))
				t.Cleanup(server.Close)

				client := server.Client()
				client.Timeout = 2 * time.Second
				if testCase.blockBody {
					client.Timeout = 50 * time.Millisecond
				}
				sink := &lockedAttemptSink{}
				ctx := llm.WithAPIAttemptSink(
					llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_complete_client_timeout")),
					sink,
				)
				response, err := provider.wireClient(t, server.URL, client, nil).Complete(ctx, providerRequest(provider.name, "test-model"))
				if testCase.wantSuccess {
					if err != nil || response.Text() != "hello" {
						t.Fatalf("Complete = (%q, %v), want hello success", response.Text(), err)
					}
				}
				attempts := sink.snapshot()
				if len(attempts) != 1 {
					t.Fatalf("canonical attempts = %d, want exactly 1: %+v", len(attempts), attempts)
				}
				if attempts[0].Outcome != testCase.wantOutcome {
					t.Fatalf("attempt outcome = %q, want %q", attempts[0].Outcome, testCase.wantOutcome)
				}
				if testCase.wantSuccess {
					if attempts[0].Response == nil || attempts[0].Response.Model == "" ||
						attempts[0].Response.Usage.InputTokens == nil || attempts[0].Response.Usage.OutputTokens == nil ||
						*attempts[0].Response.Usage.InputTokens+*attempts[0].Response.Usage.OutputTokens == 0 {
						t.Fatalf("attempt lost final semantic response metadata: %+v", attempts[0].Response)
					}
				}
			})
		}
	}
}

func TestCoreStreamWireCaptureWithClientTimeoutRetainsFinalSemanticOwnership(t *testing.T) {
	for _, provider := range wireProviders() {
		for _, testCase := range []struct {
			name        string
			body        string
			blockBody   bool
			wantOutcome apilog.AttemptOutcomeClass
			wantSuccess bool
		}{
			{name: "valid response metadata", body: provider.streamBody, wantOutcome: apilog.AttemptSuccess, wantSuccess: true},
			{name: "malformed 2xx", body: provider.streamPrefix + "data: not-json\n\n", wantOutcome: apilog.AttemptDecodeFail},
			{name: "SSE body read timeout", blockBody: true, wantOutcome: apilog.AttemptProviderTimeout},
		} {
			t.Run(provider.name+"/"+testCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if testCase.blockBody {
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						_, _ = w.Write([]byte(provider.streamPrefix))
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					_, _ = w.Write([]byte(testCase.body))
				}))
				t.Cleanup(server.Close)

				client := server.Client()
				client.Timeout = 2 * time.Second
				if testCase.blockBody {
					client.Timeout = 50 * time.Millisecond
				}
				sink := &lockedAttemptSink{}
				ctx := llm.WithAPIAttemptSink(
					llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_stream_client_timeout")),
					sink,
				)
				stream, err := provider.wireClient(t, server.URL, client, nil).Stream(ctx, providerRequest(provider.name, "test-model"))
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				var finish *llm.Response
				var streamErr error
				for event := range stream.Events() {
					if event.Type == llm.StreamEventFinish {
						finish = event.Response
					}
					if event.Type == llm.StreamEventError {
						streamErr = event.Err
					}
				}
				if testCase.wantSuccess {
					if streamErr != nil || finish == nil || finish.Text() != "hello" {
						t.Fatalf("stream = (finish %+v, error %v), want hello success", finish, streamErr)
					}
				} else if streamErr == nil {
					t.Fatal("stream had no terminal error, want malformed/timeout error")
				}
				attempts := sink.snapshot()
				if len(attempts) != 1 {
					t.Fatalf("canonical attempts = %d, want exactly 1: %+v", len(attempts), attempts)
				}
				if attempts[0].Outcome != testCase.wantOutcome {
					t.Fatalf("attempt outcome = %q, want %q", attempts[0].Outcome, testCase.wantOutcome)
				}
				if testCase.wantSuccess {
					if attempts[0].Response == nil || attempts[0].Response.Model == "" ||
						attempts[0].Response.Usage.InputTokens == nil || attempts[0].Response.Usage.OutputTokens == nil ||
						*attempts[0].Response.Usage.InputTokens+*attempts[0].Response.Usage.OutputTokens == 0 {
						t.Fatalf("attempt lost final semantic response metadata: %+v", attempts[0].Response)
					}
				}
			})
		}
	}
}

func TestCoreCompleteWireCaptureClassifiesAdapterRequestBodyDeadline(t *testing.T) {
	for _, provider := range wireProviders() {
		t.Run(provider.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(server.Close)

			sink := &lockedAttemptSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_adapter_request_body_timeout")),
				sink,
			)
			req := providerRequest(provider.name, "test-model")
			req.AdapterTimeout = &llm.AdapterTimeout{Request: 50 * time.Millisecond}
			_, _ = provider.wireClient(t, server.URL, server.Client(), nil).Complete(ctx, req)
			attempts := sink.snapshot()
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want exactly 1", len(attempts))
			}
			if attempts[0].Outcome != apilog.AttemptProviderTimeout {
				t.Fatalf("adapter request body deadline outcome = %q, want %q", attempts[0].Outcome, apilog.AttemptProviderTimeout)
			}
		})
	}
}

func TestCoreCompleteWireCaptureClassifiesAdapterConnectDeadline(t *testing.T) {
	for _, provider := range wireProviders() {
		t.Run(provider.name, func(t *testing.T) {
			dialStarted := make(chan struct{})
			var startOnce sync.Once
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					startOnce.Do(func() { close(dialStarted) })
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}}
			sink := &lockedAttemptSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_adapter_connect_timeout")),
				sink,
			)
			req := providerRequest(provider.name, "test-model")
			req.AdapterTimeout = &llm.AdapterTimeout{Connect: 50 * time.Millisecond}
			_, err := provider.wireClient(t, "http://provider.test", client, nil).Complete(ctx, req)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("connect error = %v, want original deadline identity", err)
			}
			select {
			case <-dialStarted:
			default:
				t.Fatal("test did not enter configured DialContext")
			}
			attempts := sink.snapshot()
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want exactly 1", len(attempts))
			}
			if attempts[0].Outcome != apilog.AttemptProviderTimeout {
				t.Fatalf("adapter connect deadline outcome = %q, want %q", attempts[0].Outcome, apilog.AttemptProviderTimeout)
			}
		})
	}
}
