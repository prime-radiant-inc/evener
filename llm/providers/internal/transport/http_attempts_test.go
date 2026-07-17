package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type credentialTrailerBody struct {
	request *http.Request
	reader  *bytes.Reader
	name    string
	value   string
}

func (b *credentialTrailerBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if b.reader.Len() == 0 {
		b.request.Trailer.Set(b.name, b.value)
	}
	return n, err
}

func (*credentialTrailerBody) Close() error { return nil }

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

func TestDoWithAPIAttemptsRecordsEffectiveRedirectHost(t *testing.T) {
	tests := []struct {
		name             string
		host             string
		credentialValues []string
		wantRecorded     bool
	}{
		{
			name:         "ordinary override",
			host:         "tenant.example",
			wantRecorded: true,
		},
		{
			name:             "credential-bearing override",
			host:             "credential.example",
			credentialValues: []string{"credential.example"},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			observedHost := make(chan string, 1)
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				observedHost <- request.Host
				writer.WriteHeader(http.StatusOK)
			}))
			defer target.Close()

			redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, target.URL, http.StatusFound)
			}))
			defer redirect.Close()

			client := &http.Client{}
			client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				request.Header.Set("Host", "ignored-header.example")
				request.Host = testCase.host
				return nil
			}
			sink := &responseAssociationSink{}
			request, err := http.NewRequestWithContext(
				attemptContext("ag_redirect_host", sink),
				http.MethodGet,
				redirect.URL,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			build := func(*http.Request, []byte) llm.APIAttemptMeta {
				return llm.APIAttemptMeta{
					ProviderInstance: "test",
					RequestModel:     "test-model",
					CredentialMaterial: llm.NewAPILogCredentialMaterial(
						nil,
						nil,
						testCase.credentialValues...,
					),
				}
			}
			response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, build)
			if err != nil {
				t.Fatalf("DoWithAPIAttempts: %v", err)
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatalf("read final response: %v", err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("close final response: %v", err)
			}
			attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

			if got := <-observedHost; got != testCase.host {
				t.Fatalf("target Host = %q, want %q", got, testCase.host)
			}
			attempts := sink.snapshot()
			if len(attempts) != 2 {
				t.Fatalf("attempt count = %d, want redirect and final", len(attempts))
			}
			values, recorded := attempts[1].Request.Headers["Host"]
			if recorded != testCase.wantRecorded {
				t.Fatalf("recorded Host presence = %t, want %t (values %q)", recorded, testCase.wantRecorded, values)
			}
			if testCase.wantRecorded && (len(values) != 1 || values[0] != testCase.host) {
				t.Fatalf("recorded Host = %q, want [%q]", values, testCase.host)
			}
		})
	}
}

func TestDoWithAPIAttemptsDoesNotUseGetBodyClone(t *testing.T) {
	original := &observingReadCloser{reader: strings.NewReader("request")}
	var clone *observingReadCloser
	getBodyCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(request.Body); err != nil {
			t.Fatalf("transport read request: %v", err)
		}
		if err := request.Body.Close(); err != nil {
			t.Fatalf("transport close request: %v", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_get_body", sink),
		http.MethodPost,
		"https://provider.test/v1",
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = int64(len("request"))
	request.GetBody = func() (io.ReadCloser, error) {
		getBodyCalls++
		clone = &observingReadCloser{reader: strings.NewReader("request")}
		return clone, nil
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

	if getBodyCalls != 0 {
		t.Fatalf("GetBody calls = %d, want 0", getBodyCalls)
	}
	if clone != nil {
		reads, closes := clone.counts()
		t.Fatalf("instrumentation used request clone: reads:%d closes:%d", reads, closes)
	}
	reads, closes := original.counts()
	if reads == 0 || closes != 1 {
		t.Fatalf("original request operations = reads:%d closes:%d, want transport reads and one close", reads, closes)
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

func TestDoWithAPIAttemptsLearnsCredentialTrailerPopulatedWhileReadingRequestBody(t *testing.T) {
	const (
		trailerName      = "X-Gateway-Credential"
		configuredSecret = "configured-secret-sentinel"
		trailerSecret    = "trailer-secret-sentinel"
	)
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_credential_trailer_success", sink),
		http.MethodPost,
		"https://provider.test/v1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = http.Header{trailerName: nil}
	request.Header.Set("X-Visible-Debug", configuredSecret)
	request.Body = &credentialTrailerBody{
		request: request,
		reader:  bytes.NewReader([]byte("request body")),
		name:    trailerName,
		value:   trailerSecret,
	}
	request.ContentLength = int64(len("request body"))

	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(request.Body); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("provider echoed " + request.Trailer.Get(trailerName))),
			ContentLength: int64(len("provider echoed ") + len(trailerSecret)),
		}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{trailerName},
				nil,
				configuredSecret,
			),
		}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("adapter read response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

	record := onlyAttempt(t, sink)
	if _, ok := record.Request.Headers["X-Visible-Debug"]; ok {
		t.Fatal("pre-RoundTrip header containing configured credential was persisted")
	}
	if record.Response == nil {
		t.Fatal("successful attempt omitted response metadata")
	}
	if !record.Response.Body.CredentialValuesExcluded || record.Response.Body.Exact {
		t.Fatalf("credential-bearing response body truth = %+v", record.Response.Body)
	}
	if body, err := apilog.DecodeBody(record.Response.Body); err != nil || len(body) != 0 {
		t.Fatalf("credential-bearing response body = %q, %v; want omitted", body, err)
	}
}

func TestDoWithAPIAttemptsLearnsCredentialTrailerMutatedBeforeTransportError(t *testing.T) {
	const (
		trailerName   = "X-Gateway-Credential"
		initialSecret = "initial-trailer-secret-sentinel"
		finalSecret   = "final-trailer-secret-sentinel"
	)
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_credential_trailer_transport_error", sink),
		http.MethodPost,
		"https://provider.test/v1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = http.Header{trailerName: {initialSecret}}
	request.Body = &credentialTrailerBody{
		request: request,
		reader:  bytes.NewReader([]byte("request body")),
		name:    trailerName,
		value:   finalSecret,
	}
	request.ContentLength = int64(len("request body"))

	transportErr := errors.New("provider echoed " + initialSecret + " then " + finalSecret)
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(request.Body); err != nil {
			return nil, err
		}
		return nil, transportErr
	})}
	_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{trailerName},
				nil,
			),
		}
	})
	if !errors.Is(err, transportErr) {
		t.Fatalf("DoWithAPIAttempts error = %v, want %v", err, transportErr)
	}
	if attempt == nil {
		t.Fatal("transport failure lost its attempt")
	}
	attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, transportErr)

	record := onlyAttempt(t, sink)
	if record.ErrorMessage != "" {
		t.Fatalf("credential-bearing transport error persisted as %q", record.ErrorMessage)
	}
}

func TestDoWithAPIAttemptsNilResponsePreservesHTTPClientError(t *testing.T) {
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})}
	inactive := runHTTPClientAttempt(t, client, http.MethodGet, false)
	active := runHTTPClientAttempt(t, client, http.MethodGet, true)
	assertMatchingHTTPClientErrors(t, inactive.err, active.err)
	if inactive.response != nil || inactive.attempt != nil {
		t.Fatalf("inactive result = response:%#v attempt:%#v, want nil response and attempt", inactive.response, inactive.attempt)
	}
	if active.response != nil {
		t.Fatalf("active response = %#v, want nil", active.response)
	}
	if active.attempt == nil {
		t.Fatal("active nil-response error lost its attempt")
	}
	active.attempt.Complete(llm.APIAttemptResult{Err: active.err}, llm.APITimeoutNone, nil, active.err)
	if got := onlyAttempt(t, active.sink).Outcome; got != apilog.AttemptTransportFail {
		t.Fatalf("active nil-response outcome = %q, want transport_failure", got)
	}
}

func TestDoWithAPIAttemptsNilBodyWithPositiveLengthPreservesHTTPClientError(t *testing.T) {
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			ContentLength: 1,
		}, nil
	})}
	inactive := runHTTPClientAttempt(t, client, http.MethodGet, false)
	active := runHTTPClientAttempt(t, client, http.MethodGet, true)
	assertMatchingHTTPClientErrors(t, inactive.err, active.err)
	if inactive.response != nil || inactive.attempt != nil {
		t.Fatalf("inactive result = response:%#v attempt:%#v, want nil response and attempt", inactive.response, inactive.attempt)
	}
	if active.response != nil {
		t.Fatalf("active response = %#v, want nil", active.response)
	}
	if active.attempt == nil {
		t.Fatal("active nil-body error lost its attempt")
	}
	active.attempt.Complete(llm.APIAttemptResult{Err: active.err}, llm.APITimeoutNone, nil, active.err)
	if got := onlyAttempt(t, active.sink).Outcome; got != apilog.AttemptTransportFail {
		t.Fatalf("active nil-body outcome = %q, want transport_failure", got)
	}
}

type httpClientAttemptResult struct {
	response *http.Response
	attempt  *APIAttemptCapture
	err      error
	sink     *responseAssociationSink
}

func runHTTPClientAttempt(t *testing.T, client *http.Client, method string, active bool) httpClientAttemptResult {
	t.Helper()
	ctx := context.Background()
	sink := &responseAssociationSink{}
	if active {
		ctx = attemptContext("ag_http_client_parity", sink)
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	return httpClientAttemptResult{response: response, attempt: attempt, err: err, sink: sink}
}

func assertMatchingHTTPClientErrors(t *testing.T, inactiveErr, activeErr error) {
	t.Helper()
	if inactiveErr == nil || activeErr == nil {
		t.Fatalf("HTTP client errors = inactive:%v active:%v, want both requests rejected", inactiveErr, activeErr)
	}
	var inactiveURL, activeURL *url.Error
	if !errors.As(inactiveErr, &inactiveURL) || !errors.As(activeErr, &activeURL) {
		t.Fatalf("HTTP client errors = inactive:%T active:%T, want wrapped client errors", inactiveErr, activeErr)
	}
	if inactiveURL.Op != activeURL.Op || inactiveURL.URL != activeURL.URL {
		t.Fatalf("HTTP client context = inactive:%s %s active:%s %s, want parity", inactiveURL.Op, inactiveURL.URL, activeURL.Op, activeURL.URL)
	}
	if inactiveURL.Err == nil || activeURL.Err == nil {
		t.Fatalf("HTTP client errors = inactive:%v active:%v, want concrete client causes", inactiveErr, activeErr)
	}
}

func TestDoWithAPIAttemptsNormalizesNilBodyWhenHTTPClientPermitsIt(t *testing.T) {
	testCases := []struct {
		name          string
		method        string
		contentLength int64
	}{
		{name: "head with positive length", method: http.MethodHead, contentLength: 1},
		{name: "get with zero length", method: http.MethodGet, contentLength: 0},
		{name: "get with unknown length", method: http.MethodGet, contentLength: -1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					ContentLength: testCase.contentLength,
				}, nil
			})}
			inactive := runHTTPClientAttempt(t, client, testCase.method, false)
			active := runHTTPClientAttempt(t, client, testCase.method, true)
			if inactive.err != nil || active.err != nil {
				t.Fatalf("HTTP client errors = inactive:%v active:%v, want both requests accepted", inactive.err, active.err)
			}
			if inactive.attempt != nil {
				t.Fatalf("inactive request returned attempt %#v", inactive.attempt)
			}
			assertEmptyResponseBody(t, inactive.response)
			assertEmptyResponseBody(t, active.response)
			if active.attempt == nil {
				t.Fatal("active request lost its attempt")
			}
			active.attempt.Complete(llm.APIAttemptResult{StatusCode: active.response.StatusCode}, llm.APITimeoutNone, nil, nil)
			if got := onlyAttempt(t, active.sink).Outcome; got != apilog.AttemptSuccess {
				t.Fatalf("active outcome = %q, want success", got)
			}
		})
	}
}

func assertEmptyResponseBody(t *testing.T, response *http.Response) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil || len(body) != 0 {
		t.Fatalf("response body = %q, %v; want empty body", body, err)
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
