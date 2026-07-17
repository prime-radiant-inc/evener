package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type responseAssociationSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *responseAssociationSink) AppendAttempt(_ context.Context, attempt apilog.APIAttemptRecord) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	return nil
}

func (*responseAssociationSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *responseAssociationSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func (s *responseAssociationSink) count() int {
	return len(s.snapshot())
}

type responseAssociationRoundTripper func(*http.Request) (*http.Response, error)

func (f responseAssociationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func attemptContext(groupID string, sink llm.APIAttemptSink) context.Context {
	return llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
		sink,
	)
}

func attemptMeta(*http.Request, []byte) llm.APIAttemptMeta {
	return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
}

func onlyAttempt(t *testing.T, sink *responseAssociationSink) apilog.APIAttemptRecord {
	t.Helper()
	attempts := sink.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
	return attempts[0]
}

func TestDoWithAPIAttemptsRecordsOneAttemptPerRoundTrip(t *testing.T) {
	const (
		requestBytes  = "request bytes read by transport"
		responseBytes = "response bytes read by adapter"
	)
	transportCalls := 0
	var requestObservation bodyObservationReporter
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		var ok bool
		requestObservation, ok = request.Body.(bodyObservationReporter)
		if !ok {
			t.Fatalf("request body %T does not report observed bytes", request.Body)
		}
		got, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("transport read request: %v", err)
		}
		if string(got) != requestBytes {
			t.Fatalf("transport request = %q, want %q", got, requestBytes)
		}
		if err := request.Body.Close(); err != nil {
			t.Fatalf("transport close request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBytes)),
		}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_one_round_trip", sink),
		http.MethodPost,
		"https://provider.test/v1",
		io.NopCloser(strings.NewReader(requestBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	gotResponse, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("adapter read response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

	if transportCalls != 1 {
		t.Fatalf("RoundTrip calls = %d, want 1", transportCalls)
	}
	if !requestObservation.observedExactly() {
		t.Fatal("request observation is inexact after transport reached EOF")
	}
	if got := string(requestObservation.observedBytes()); got != requestBytes {
		t.Fatalf("observed request = %q, want %q", got, requestBytes)
	}
	if string(gotResponse) != responseBytes {
		t.Fatalf("adapter response = %q, want %q", gotResponse, responseBytes)
	}
	record := onlyAttempt(t, sink)
	recordedRequest, err := apilog.DecodeBody(record.Request.Body)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if string(recordedRequest) != requestBytes {
		t.Fatalf("recorded request = %q, want %q", recordedRequest, requestBytes)
	}
	recordedResponse, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(recordedResponse) != responseBytes {
		t.Fatalf("recorded response = %q, want %q", recordedResponse, responseBytes)
	}
}

func TestDoWithAPIAttemptsRedirectCloseDoesNotReadBody(t *testing.T) {
	redirectBody := &observingReadCloser{reader: strings.NewReader(strings.Repeat("r", 3<<10))}
	baseCalls := 0
	var redirectObservation bodyObservationReporter
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		baseCalls++
		if baseCalls == 1 {
			return &http.Response{
				StatusCode:    http.StatusFound,
				Header:        http.Header{"Location": []string{"https://provider.test/final"}},
				Body:          redirectBody,
				ContentLength: 3 << 10,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("final")),
		}, nil
	})}
	client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		var ok bool
		redirectObservation, ok = request.Response.Body.(bodyObservationReporter)
		if !ok {
			return errors.New("redirect response does not report observations")
		}
		return nil
	}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(attemptContext("ag_redirect", sink), http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read final response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

	reads, closes := redirectBody.counts()
	if reads != 0 {
		t.Fatalf("instrumentation read redirect body %d times, want 0", reads)
	}
	if closes != 1 {
		t.Fatalf("HTTP client redirect closes = %d, want 1 pass-through close", closes)
	}
	if redirectObservation == nil {
		t.Fatal("redirect response observation was not captured")
	}
	if redirectObservation.observedExactly() {
		t.Fatal("unread redirect response was marked exact after Close")
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want redirect and final", len(attempts))
	}
	redirectBytes, err := apilog.DecodeBody(attempts[0].Response.Body)
	if err != nil {
		t.Fatalf("decode redirect body: %v", err)
	}
	if len(redirectBytes) != 0 {
		t.Fatalf("redirect evidence contains %d unread bytes", len(redirectBytes))
	}
}

func TestDoWithAPIAttemptsRedirectCredentialMaterialRemainsCumulative(t *testing.T) {
	const (
		sessionID        = "sess-redirect-credentials"
		credentialHeader = "X-Redirect-Credential"
		secret           = "hop-two-secret"
	)
	stateDir := t.TempDir()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	group := llm.NewAPIAttemptGroup("ag_redirect_credentials")
	ctx := llm.WithAPILogContext(context.Background(), sessionID)
	ctx = llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(ctx, group), logger)

	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		transportCalls++
		location := ""
		switch transportCalls {
		case 1:
			location = "https://provider.test/credential"
		case 2:
			location = "https://provider.test/echo"
		}
		if location != "" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{location}},
				Body:       http.NoBody,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		switch len(via) {
		case 1:
			request.Header.Set(credentialHeader, secret)
		case 2:
			request.Header.Del(credentialHeader)
			request.Header.Set("X-Echo", secret)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{credentialHeader},
				nil,
			),
		}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if transportCalls != 3 {
		t.Fatalf("RoundTrip calls = %d, want 3", transportCalls)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	group.Settle(ctx, apilog.AttemptSuccess)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close logger: %v", err)
	}

	file, err := os.Open(filepath.Join(stateDir, "sessions", sessionID+".api.jsonl"))
	if err != nil {
		t.Fatalf("open API log: %v", err)
	}
	defer file.Close()
	decoder := apilog.NewDecoder(file, 1<<20)
	var attempts []apilog.APIAttemptRecord
	for {
		record, err := decoder.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode API log: %v", err)
		}
		if attempt, ok := record.(apilog.APIAttemptRecord); ok {
			attempts = append(attempts, attempt)
		}
	}
	if len(attempts) != 3 {
		t.Fatalf("durable attempts = %d, want 3", len(attempts))
	}
	third := attempts[2]
	if _, ok := third.Request.Headers["X-Echo"]; ok {
		t.Fatalf("third durable request retained prior-hop credential echo: %+v", third.Request.Headers)
	}
	encoded, err := apilog.MarshalRecord(third)
	if err != nil {
		t.Fatalf("marshal third durable attempt: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("third durable attempt contains prior-hop credential %q", secret)
	}
}

func TestDoWithAPIAttemptsExplicitRetriesCreateSeparateAttempts(t *testing.T) {
	sink := &responseAssociationSink{}
	ctx := attemptContext("ag_explicit_retries", sink)
	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		transportCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})}
	for i := 0; i < 2; i++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	}
	if transportCalls != 2 {
		t.Fatalf("RoundTrip calls = %d, want 2", transportCalls)
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempt records = %d, want 2", len(attempts))
	}
	if attempts[0].AttemptIndex != 1 || attempts[1].AttemptIndex != 2 {
		t.Fatalf("attempt indexes = %d, %d; want 1, 2", attempts[0].AttemptIndex, attempts[1].AttemptIndex)
	}
}

func TestDoWithAPIAttemptsTransportErrorRetainsOneAttempt(t *testing.T) {
	transportErr := errors.New("transport failed")
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(attemptContext("ag_transport_error", sink), http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if !errors.Is(err, transportErr) {
		t.Fatalf("DoWithAPIAttempts error = %v, want %v", err, transportErr)
	}
	if attempt == nil {
		t.Fatal("transport failure lost its attempt")
	}
	attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, transportErr)
	record := onlyAttempt(t, sink)
	if record.Outcome != apilog.AttemptTransportFail {
		t.Fatalf("outcome = %q, want transport_failure", record.Outcome)
	}
	if record.Response != nil {
		t.Fatalf("transport failure invented response: %#v", record.Response)
	}
}

func TestDoWithAPIAttemptsInactiveRequestRemainsUnwrapped(t *testing.T) {
	body := io.NopCloser(bytes.NewReader([]byte("response")))
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}
	request, err := http.NewRequest(http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if attempt != nil {
		t.Fatalf("inactive request returned attempt %#v", attempt)
	}
	if response.Body != body {
		t.Fatalf("inactive response body was wrapped: %T", response.Body)
	}
}
