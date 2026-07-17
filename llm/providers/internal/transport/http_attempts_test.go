package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

type finishRequestOnCloseBody struct {
	io.ReadCloser
	requestBody io.ReadCloser
}

func (b *finishRequestOnCloseBody) Close() error {
	_, readErr := io.ReadAll(b.requestBody)
	requestCloseErr := b.requestBody.Close()
	responseCloseErr := b.ReadCloser.Close()
	return errors.Join(readErr, requestCloseErr, responseCloseErr)
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

func durableAttemptContext(t *testing.T, sessionID, groupID string) (context.Context, *llm.APIAttemptGroup, string) {
	t.Helper()
	stateDir := t.TempDir()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() {
		if err := logger.Close(); err != nil {
			t.Errorf("close API logger: %v", err)
		}
	})
	group := llm.NewAPIAttemptGroup(groupID)
	ctx := llm.WithAPILogContext(context.Background(), sessionID)
	ctx = llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(ctx, group), logger)
	return ctx, group, filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
}

func readDurableAttempts(t *testing.T, path string) []apilog.APIAttemptRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open API log: %v", err)
	}
	defer file.Close()
	decoder := apilog.NewDecoder(file, 1<<20)
	var attempts []apilog.APIAttemptRecord
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return attempts
		}
		if err != nil {
			t.Fatalf("decode API log: %v", err)
		}
		if attempt, ok := record.(apilog.APIAttemptRecord); ok {
			attempts = append(attempts, attempt)
		}
	}
}

func assertDurableAttemptExcludes(t *testing.T, attempt apilog.APIAttemptRecord, secrets ...string) {
	t.Helper()
	if attempt.Response == nil || !attempt.Response.Body.CredentialValuesExcluded {
		t.Fatalf("response body = %+v, want credential exclusion", attempt.Response)
	}
	encoded, err := apilog.MarshalRecord(attempt)
	if err != nil {
		t.Fatalf("marshal durable attempt: %v", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("durable attempt persisted candidate credential %q", secret)
		}
	}
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
		if errors.Is(err, io.EOF) {
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

func TestDoWithAPIAttemptsSharedGroupLearnsEachCallsCredentialValue(t *testing.T) {
	const (
		credentialHeader = "X-Shared-Credential"
		firstSecret      = "first-call-secret"
		secondSecret     = "second-call-secret"
	)
	ctx, group, path := durableAttemptContext(t, "sess-shared-group-credentials", "ag_shared_group_credentials")
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		body := "first response"
		status := http.StatusOK
		if request.URL.Path == "/second" {
			body = secondSecret
			status = http.StatusBadRequest
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	run := func(endpoint, secret string, build APIAttemptMetaBuilder, resultErr error) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/"+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(credentialHeader, secret)
		response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, build)
		if err != nil {
			t.Fatalf("DoWithAPIAttempts(%s): %v", endpoint, err)
		}
		if _, err := io.ReadAll(response.Body); err != nil {
			t.Fatalf("read %s response: %v", endpoint, err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close %s response: %v", endpoint, err)
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode, Err: resultErr}, llm.APITimeoutNone, nil, nil)
	}

	run("first", firstSecret, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{credentialHeader},
				nil,
			),
		}
	}, nil)
	run("second", secondSecret, attemptMeta, errors.New("provider echoed "+secondSecret))
	group.Settle(ctx, apilog.AttemptProviderReject)

	attempts := readDurableAttempts(t, path)
	if len(attempts) != 2 {
		t.Fatalf("durable attempts = %d, want 2", len(attempts))
	}
	second := attempts[1]
	if second.Response == nil || !second.Response.Body.CredentialValuesExcluded {
		t.Fatalf("second response body = %+v, want new shared-group credential value excluded", second.Response)
	}
	if strings.Contains(second.ErrorMessage, secondSecret) {
		t.Fatalf("second error retained new shared-group credential value: %q", second.ErrorMessage)
	}
	encoded, err := apilog.MarshalRecord(second)
	if err != nil {
		t.Fatalf("marshal second durable attempt: %v", err)
	}
	if bytes.Contains(encoded, []byte(secondSecret)) {
		t.Fatalf("second durable attempt retained new shared-group credential value %q", secondSecret)
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

func TestDoWithAPIAttemptsRedirectSourceWaitsForNextHopCredentialMaterial(t *testing.T) {
	const secret = "next-hop-secret"
	tests := []struct {
		name          string
		location      string
		configure     func(*http.Client)
		build         APIAttemptMetaBuilder
		assertNextHop func(*testing.T, *http.Request)
	}{
		{
			name:     "query location",
			location: "/final?access_token=" + url.QueryEscape(secret),
			build:    attemptMeta,
			assertNextHop: func(t *testing.T, request *http.Request) {
				t.Helper()
				if got := request.URL.Query().Get("access_token"); got != secret {
					t.Fatalf("next-hop access_token = %q, want %q", got, secret)
				}
			},
		},
		{
			name:     "redirect hook header and userinfo",
			location: "/final",
			configure: func(client *http.Client) {
				client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
					request.Header.Set("X-Redirect-Credential", secret)
					request.URL.User = url.UserPassword("redirect-user", secret)
					return nil
				}
			},
			build: func(*http.Request, []byte) llm.APIAttemptMeta {
				return llm.APIAttemptMeta{
					ProviderInstance: "test",
					RequestModel:     "test-model",
					CredentialMaterial: llm.NewAPILogCredentialMaterial(
						[]string{"X-Redirect-Credential"},
						nil,
					),
				}
			},
			assertNextHop: func(t *testing.T, request *http.Request) {
				t.Helper()
				if got := request.Header.Get("X-Redirect-Credential"); got != secret {
					t.Fatalf("next-hop redirect credential = %q, want %q", got, secret)
				}
				if request.URL.User == nil {
					t.Fatal("next-hop URL has no userinfo")
				}
				password, ok := request.URL.User.Password()
				if !ok || request.URL.User.Username() != "redirect-user" || password != secret {
					t.Fatalf("next-hop userinfo = %q, %q, %v", request.URL.User.Username(), password, ok)
				}
			},
		},
		{
			name:     "cookie jar",
			location: "/final",
			configure: func(client *http.Client) {
				jar, err := cookiejar.New(nil)
				if err != nil {
					panic(err)
				}
				client.Jar = jar
			},
			build: attemptMeta,
			assertNextHop: func(t *testing.T, request *http.Request) {
				t.Helper()
				cookie, err := request.Cookie("session")
				if err != nil || cookie.Value != secret {
					t.Fatalf("next-hop session cookie = %#v, %v; want %q", cookie, err, secret)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/start" {
					http.SetCookie(w, &http.Cookie{Name: "session", Value: secret, Path: "/"})
					w.Header().Set("Location", testCase.location)
					w.WriteHeader(http.StatusFound)
					_, _ = w.Write([]byte(secret))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("final response"))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			if testCase.configure != nil {
				testCase.configure(client)
			}
			baseTransport := client.Transport
			var finalRequest *http.Request
			client.Transport = responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/start" {
					finalRequest = request.Clone(request.Context())
				}
				return baseTransport.RoundTrip(request)
			})
			sink := &responseAssociationSink{}
			request, err := http.NewRequestWithContext(
				attemptContext("ag_redirect_source_credentials", sink),
				http.MethodGet,
				server.URL+"/start",
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, testCase.build)
			if err != nil {
				t.Fatalf("DoWithAPIAttempts: %v", err)
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatalf("read final response: %v", err)
			}
			attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

			if finalRequest == nil {
				t.Fatal("redirect did not reach the next hop")
			}
			testCase.assertNextHop(t, finalRequest)
			attempts := sink.snapshot()
			if len(attempts) != 2 {
				t.Fatalf("attempt count = %d, want redirect source and final hop", len(attempts))
			}
			source := attempts[0]
			if source.Response == nil || !source.Response.Body.CredentialValuesExcluded {
				t.Fatalf("redirect source body = %+v, want credential exclusion", source.Response)
			}
			encoded, err := apilog.MarshalRecord(source)
			if err != nil {
				t.Fatalf("marshal source attempt: %v", err)
			}
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("redirect source attempt persisted next-hop credential %q", secret)
			}
		})
	}
}

func TestDoWithAPIAttemptsFlushesRedirectSourceWhenNoNextRoundTripOccurs(t *testing.T) {
	tests := []struct {
		name      string
		location  string
		wantError error
	}{
		{
			name:      "redirect rejected",
			location:  "/final",
			wantError: errors.New("redirect policy sentinel"),
		},
		{
			name:     "invalid location",
			location: "%",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/start" {
					t.Errorf("unexpected next-hop request %q", request.URL)
					return
				}
				w.Header().Set("Location", testCase.location)
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte("closed redirect source"))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			if testCase.wantError != nil {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return testCase.wantError }
			}
			sink := &responseAssociationSink{}
			request, err := http.NewRequestWithContext(
				attemptContext("ag_redirect_without_next_round_trip", sink),
				http.MethodGet,
				server.URL+"/start",
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
			if err == nil {
				t.Fatal("DoWithAPIAttempts succeeded without a next RoundTrip")
			}
			if testCase.wantError != nil && !errors.Is(err, testCase.wantError) {
				t.Fatalf("DoWithAPIAttempts error = %v, want %v", err, testCase.wantError)
			}
			if attempt != nil {
				t.Fatalf("closed redirect source returned as outer attempt: %#v", attempt)
			}
			record := onlyAttempt(t, sink)
			if record.Response == nil || record.Response.StatusCode == nil || *record.Response.StatusCode != http.StatusFound {
				t.Fatalf("redirect source response = %+v, want status 302", record.Response)
			}
		})
	}
}

func TestDoWithAPIAttemptsRejectedRedirectCandidateSanitizesDurableSource(t *testing.T) {
	const (
		locationSecret = "candidate-location-secret"
		headerSecret   = "candidate-header-secret"
		userinfoSecret = "candidate-userinfo-secret"
		cookieSecret   = "candidate-cookie-secret"
		jarSecret      = "jar-only-secret"
		credentialName = "X-Candidate-Credential"
	)
	tests := []struct {
		name     string
		decision error
	}{
		{name: "redirect rejected", decision: errors.New("candidate rejected")},
		{name: "use last response", decision: http.ErrUseLastResponse},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if requestCount.Add(1) != 1 {
					t.Errorf("rejected candidate reached server: %s", request.URL)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "jar_candidate", Value: jarSecret, Path: "/"})
				w.Header().Set("Location", "/candidate?access_token="+url.QueryEscape(locationSecret))
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte(strings.Join([]string{
					locationSecret,
					headerSecret,
					userinfoSecret,
					cookieSecret,
				}, " ")))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("create cookie jar: %v", err)
			}
			client.Jar = jar
			client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				if _, err := request.Cookie("jar_candidate"); !errors.Is(err, http.ErrNoCookie) {
					t.Fatalf("cookie jar populated rejected candidate before RoundTrip: %v", err)
				}
				request.Header.Set(credentialName, headerSecret)
				request.Header.Set("Cookie", "candidate="+cookieSecret)
				request.URL.User = url.UserPassword("candidate-user", userinfoSecret)
				return testCase.decision
			}
			ctx, group, logPath := durableAttemptContext(
				t,
				"sess-rejected-candidate-"+strconv.Itoa(len(testCase.name)),
				"ag_rejected_candidate_"+strconv.Itoa(len(testCase.name)),
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
				return llm.APIAttemptMeta{
					ProviderInstance: "test",
					RequestModel:     "test-model",
					CredentialMaterial: llm.NewAPILogCredentialMaterial(
						[]string{credentialName},
						nil,
					),
				}
			})
			if errors.Is(testCase.decision, http.ErrUseLastResponse) {
				if err != nil {
					t.Fatalf("ErrUseLastResponse changed to error: %v", err)
				}
				if attempt == nil {
					t.Fatal("ErrUseLastResponse lost the source attempt")
				}
				if _, err := io.ReadAll(response.Body); err != nil {
					t.Fatalf("read last response: %v", err)
				}
				attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
			} else {
				if !errors.Is(err, testCase.decision) {
					t.Fatalf("rejected redirect error = %v, want %v", err, testCase.decision)
				}
				if attempt != nil {
					t.Fatalf("rejected redirect returned attempt %#v", attempt)
				}
			}
			if got := requestCount.Load(); got != 1 {
				t.Fatalf("server requests = %d, want source only", got)
			}
			serverURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			storedJarSecret := false
			for _, cookie := range client.Jar.Cookies(serverURL) {
				storedJarSecret = storedJarSecret || cookie.Name == "jar_candidate" && cookie.Value == jarSecret
			}
			if !storedJarSecret {
				t.Fatal("test did not establish that the response cookie reached the jar")
			}
			group.Settle(ctx, apilog.AttemptProviderReject)
			attempts := readDurableAttempts(t, logPath)
			if len(attempts) != 1 {
				t.Fatalf("durable attempts = %d, want one source", len(attempts))
			}
			assertDurableAttemptExcludes(
				t,
				attempts[0],
				locationSecret,
				headerSecret,
				userinfoSecret,
				cookieSecret,
			)
		})
	}
}

func TestDoWithAPIAttemptsDefaultRedirectRejectionSanitizesLocationCredential(t *testing.T) {
	const secret = "default-policy-location-secret"
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		count := requestCount.Add(1)
		location := "/hop/" + strconv.Itoa(int(count))
		body := "ordinary redirect"
		if count == 10 {
			location = "/rejected?access_token=" + url.QueryEscape(secret)
			body = secret
		}
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	ctx, group, logPath := durableAttemptContext(t, "sess-default-rejection", "ag_default_rejection")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, attemptMeta)
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("default redirect error = %v, want ten-redirect rejection", err)
	}
	if attempt != nil {
		t.Fatalf("default redirect rejection returned attempt %#v", attempt)
	}
	if got := requestCount.Load(); got != 10 {
		t.Fatalf("server requests = %d, want 10", got)
	}
	group.Settle(ctx, apilog.AttemptProviderReject)
	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 10 {
		t.Fatalf("durable attempts = %d, want 10 actual RoundTrips", len(attempts))
	}
	assertDurableAttemptExcludes(t, attempts[len(attempts)-1], secret)
}

func TestDoWithAPIAttemptsRejectedRedirectSanitizesRealJarCookieFromSource(t *testing.T) {
	const secret = "rejected-source-cookie-secret"
	tests := []struct {
		name     string
		decision error
	}{
		{name: "redirect rejected", decision: errors.New("jar candidate rejected")},
		{name: "use last response", decision: http.ErrUseLastResponse},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: secret, Path: "/"})
				w.Header().Set("Location", "/candidate")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte(secret))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("create cookie jar: %v", err)
			}
			client.Jar = jar
			client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				if _, err := request.Cookie("session"); !errors.Is(err, http.ErrNoCookie) {
					t.Fatalf("jar cookie reached rejected candidate before RoundTrip: %v", err)
				}
				return testCase.decision
			}
			ctx, group, logPath := durableAttemptContext(
				t,
				"sess-rejected-jar-"+strconv.Itoa(len(testCase.name)),
				"ag_rejected_jar_"+strconv.Itoa(len(testCase.name)),
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
			if errors.Is(testCase.decision, http.ErrUseLastResponse) {
				if err != nil {
					t.Fatalf("ErrUseLastResponse changed to error: %v", err)
				}
				if _, err := io.ReadAll(response.Body); err != nil {
					t.Fatalf("read last response: %v", err)
				}
				attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
			} else if !errors.Is(err, testCase.decision) {
				t.Fatalf("rejected redirect error = %v, want %v", err, testCase.decision)
			}
			group.Settle(ctx, apilog.AttemptProviderReject)
			attempts := readDurableAttempts(t, logPath)
			if len(attempts) != 1 {
				t.Fatalf("durable attempts = %d, want source only", len(attempts))
			}
			assertDurableAttemptExcludes(t, attempts[0], secret)
		})
	}
}

func TestDoWithAPIAttemptsRejectedRedirectSanitizesDecodedResponseCookie(t *testing.T) {
	const (
		encodedSecret = "abc%2Fdef"
		decodedSecret = "abc/def"
	)
	tests := []struct {
		name     string
		decision error
	}{
		{name: "redirect rejected", decision: errors.New("decoded cookie rejected")},
		{name: "use last response", decision: http.ErrUseLastResponse},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Set-Cookie", "session="+encodedSecret+"; Path=/")
				w.Header().Set("Location", "/candidate")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte(decodedSecret))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("create cookie jar: %v", err)
			}
			client.Jar = jar
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return testCase.decision }
			ctx, group, logPath := durableAttemptContext(
				t,
				"sess-decoded-cookie-"+strconv.Itoa(len(testCase.name)),
				"ag_decoded_cookie_"+strconv.Itoa(len(testCase.name)),
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
			if errors.Is(testCase.decision, http.ErrUseLastResponse) {
				if err != nil {
					t.Fatalf("ErrUseLastResponse changed to error: %v", err)
				}
				if _, err := io.ReadAll(response.Body); err != nil {
					t.Fatalf("read last response: %v", err)
				}
				attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
			} else {
				if !errors.Is(err, testCase.decision) {
					t.Fatalf("rejected redirect error = %v, want %v", err, testCase.decision)
				}
				if attempt != nil {
					t.Fatalf("rejected redirect returned attempt %#v", attempt)
				}
			}
			group.Settle(ctx, apilog.AttemptProviderReject)
			attempts := readDurableAttempts(t, logPath)
			if len(attempts) != 1 {
				t.Fatalf("durable attempts = %d, want one source", len(attempts))
			}
			assertDurableAttemptExcludes(t, attempts[0], encodedSecret, decodedSecret)
		})
	}
}

func TestDoWithAPIAttemptsFutureRedirectCredentialSanitizesEveryEarlierSource(t *testing.T) {
	const secret = "third-hop-future-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/start":
			w.Header().Set("Location", "/second")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte(secret))
		case "/second":
			w.Header().Set("Location", "/final?access_token="+url.QueryEscape(secret))
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte("second redirect"))
		case "/final":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("final response"))
		default:
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	ctx, group, logPath := durableAttemptContext(t, "sess-future-redirect", "ag_future_redirect")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read final response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	group.Settle(ctx, apilog.AttemptSuccess)
	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 3 {
		t.Fatalf("durable attempts = %d, want three actual RoundTrips", len(attempts))
	}
	assertDurableAttemptExcludes(t, attempts[0], secret)
}

type closeSequenceBody struct {
	reader io.Reader
	errors []error

	mu         sync.Mutex
	closeCount int
}

func (b *closeSequenceBody) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b *closeSequenceBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	index := b.closeCount
	b.closeCount++
	if index < len(b.errors) {
		return b.errors[index]
	}
	return nil
}

func (b *closeSequenceBody) closes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCount
}

func TestDoWithAPIAttemptsRedirectHookDoubleCloseDefersSourceOnce(t *testing.T) {
	const (
		secret         = "double-close-candidate-secret"
		credentialName = "X-Double-Close-Credential"
	)
	firstCloseErr := errors.New("first close sentinel")
	secondCloseErr := errors.New("second close sentinel")
	body := &closeSequenceBody{
		reader: strings.NewReader(secret),
		errors: []error{firstCloseErr, secondCloseErr},
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusFound,
			Header:        http.Header{"Location": []string{"https://provider.test/candidate"}},
			Body:          body,
			ContentLength: int64(len(secret)),
		}, nil
	})}
	client.CheckRedirect = func(candidate *http.Request, _ []*http.Request) error {
		if _, err := io.ReadAll(candidate.Response.Body); err != nil {
			t.Fatalf("read source body in redirect hook: %v", err)
		}
		candidate.Header.Set(credentialName, secret)
		if err := candidate.Response.Body.Close(); !errors.Is(err, firstCloseErr) {
			t.Fatalf("first source Close = %v, want %v", err, firstCloseErr)
		}
		if err := candidate.Response.Body.Close(); !errors.Is(err, secondCloseErr) {
			t.Fatalf("second source Close = %v, want %v", err, secondCloseErr)
		}
		return errors.New("reject after double close")
	}
	ctx, group, logPath := durableAttemptContext(t, "sess-double-close", "ag_double_close")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{credentialName},
				nil,
			),
		}
	})
	if err == nil {
		t.Fatal("double-close redirect unexpectedly succeeded")
	}
	if attempt != nil {
		t.Fatalf("double-close redirect returned attempt %#v", attempt)
	}
	if got := body.closes(); got != 3 {
		t.Fatalf("underlying closes = %d, want two hook closes plus net/http close", got)
	}
	group.Settle(ctx, apilog.AttemptProviderReject)
	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 1 {
		t.Fatalf("durable attempts = %d, want one source", len(attempts))
	}
	assertDurableAttemptExcludes(t, attempts[0], secret)
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

func TestDoWithAPIAttemptsWaitsForAsyncRequestTrailerBeforePersistence(t *testing.T) {
	const (
		trailerName   = "X-Gateway-Credential"
		trailerSecret = "async-request-trailer-secret-sentinel"
	)
	ctx, group, logPath := durableAttemptContext(t, "sess-async-trailer", "ag_async_trailer")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = http.Header{trailerName: nil}
	request.Body = &credentialTrailerBody{
		request: request,
		reader:  bytes.NewReader([]byte("request body")),
		name:    trailerName,
		value:   trailerSecret,
	}
	request.ContentLength = -1

	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &finishRequestOnCloseBody{
				ReadCloser:  io.NopCloser(strings.NewReader("provider echoed " + trailerSecret)),
				requestBody: request.Body,
			},
		}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{trailerName},
				nil,
			),
		}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	group.Settle(ctx, apilog.AttemptSuccess)

	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 1 {
		t.Fatalf("durable attempts = %d, want one", len(attempts))
	}
	assertDurableAttemptExcludes(t, attempts[0], trailerSecret)
}

func TestDoWithAPIAttemptsFreezesContextStateWhenCompletionIsQueued(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	sink := &responseAssociationSink{}
	requestCtx := attemptContext("ag_queued_completion_context", sink)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "https://provider.test/v1", io.NopCloser(strings.NewReader("request body")))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1

	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &finishRequestOnCloseBody{
				ReadCloser:  http.NoBody,
				requestBody: request.Body,
			},
		}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(parentCtx, client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	if sink.count() != 0 {
		t.Fatalf("attempt count before request close = %d, want 0", sink.count())
	}

	cancelParent()
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	if got := onlyAttempt(t, sink).Outcome; got != apilog.AttemptSuccess {
		t.Fatalf("outcome after post-completion cancellation = %q, want %q", got, apilog.AttemptSuccess)
	}
}

func TestDoWithAPIAttemptsFreezesRedirectContextWhenSourceCompletionIsQueued(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_queued_redirect_context", sink),
		http.MethodGet,
		"https://provider.test/start",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	var finalRequestBody io.ReadCloser
	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		switch transportCalls {
		case 1:
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://provider.test/final"}},
				Body:       http.NoBody,
			}, nil
		case 2:
			finalRequestBody = request.Body
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		default:
			return nil, errors.New("unexpected extra RoundTrip")
		}
	})}
	client.CheckRedirect = func(candidate *http.Request, _ []*http.Request) error {
		candidate.Method = http.MethodPost
		candidate.Body = io.NopCloser(strings.NewReader("final request body"))
		candidate.ContentLength = -1
		return nil
	}

	response, attempt, err := DoWithAPIAttempts(parentCtx, client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	if got := sink.count(); got != 0 {
		t.Fatalf("attempt count before final request close = %d, want 0", got)
	}
	if finalRequestBody == nil {
		t.Fatal("final RoundTrip did not retain its request body")
	}

	cancelParent()
	if err := finalRequestBody.Close(); err != nil {
		t.Fatalf("close final request: %v", err)
	}
	attempts := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempt count after final request close = %d, want redirect and final", len(attempts))
	}
	if got := attempts[0].Outcome; got != apilog.AttemptProviderReject {
		t.Fatalf("redirect outcome after post-close cancellation = %q, want %q", got, apilog.AttemptProviderReject)
	}
}

func TestRedirectSourceCapturesFinishedAtBeforeDeferredPersistence(t *testing.T) {
	sink := &responseAssociationSink{}
	requestCtx := attemptContext("ag_redirect_finished_at", sink)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt := BeginAPIAttempt(context.Background(), requestCtx, request, attemptMeta(request, nil))
	var pending *pendingAPIAttemptCompletion
	body := &apiAttemptResponseBody{
		observedBody: newObservedBody(requestCtx, http.NoBody, 0),
		attempt:      attempt,
		statusCode:   http.StatusFound,
		deferCompletion: func(completion *pendingAPIAttemptCompletion) {
			pending = completion
		},
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close redirect source: %v", err)
	}
	if pending == nil {
		t.Fatal("redirect source completion was not deferred")
	}
	defer pending.complete()
	if pending.result.FinishedAt.IsZero() {
		t.Fatal("redirect source did not capture FinishedAt before deferred persistence")
	}
}

func TestDoWithAPIAttemptsRedirectPropagatesDynamicTrailerCredentialToAllAttempts(t *testing.T) {
	const (
		trailerName   = "X-Gateway-Credential"
		trailerSecret = "redirect-trailer-secret-sentinel"
	)
	ctx, group, logPath := durableAttemptContext(t, "sess-redirect-trailer", "ag_redirect_trailer")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = http.Header{trailerName: nil}
	request.Body = &credentialTrailerBody{
		request: request,
		reader:  bytes.NewReader([]byte("request body")),
		name:    trailerName,
		value:   trailerSecret,
	}
	request.ContentLength = int64(len("request body"))

	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		switch transportCalls {
		case 1:
			if _, err := io.ReadAll(request.Body); err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://provider.test/final"}},
				Body:       io.NopCloser(strings.NewReader("source saw " + trailerSecret)),
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("final echoed " + trailerSecret)),
			}, nil
		default:
			return nil, errors.New("unexpected extra RoundTrip")
		}
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{trailerName},
				nil,
			),
		}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read final response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	group.Settle(ctx, apilog.AttemptSuccess)

	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 2 {
		t.Fatalf("durable attempts = %d, want redirect and final", len(attempts))
	}
	for _, durable := range attempts {
		assertDurableAttemptExcludes(t, durable, trailerSecret)
	}
}

func TestDoWithAPIAttemptsFinalDynamicTrailerSanitizesEarlierRedirectSource(t *testing.T) {
	const (
		trailerName   = "X-Gateway-Credential"
		trailerSecret = "final-trailer-secret-sentinel"
	)
	ctx, group, logPath := durableAttemptContext(t, "sess-final-redirect-trailer", "ag_final_redirect_trailer")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		switch transportCalls {
		case 1:
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://provider.test/final"}},
				Body:       io.NopCloser(strings.NewReader("source exposed " + trailerSecret)),
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: &finishRequestOnCloseBody{
					ReadCloser:  io.NopCloser(strings.NewReader("final saw " + trailerSecret)),
					requestBody: request.Body,
				},
			}, nil
		default:
			return nil, errors.New("unexpected extra RoundTrip")
		}
	})}
	client.CheckRedirect = func(candidate *http.Request, _ []*http.Request) error {
		candidate.Method = http.MethodPost
		candidate.Trailer = http.Header{trailerName: nil}
		candidate.Body = &credentialTrailerBody{
			request: candidate,
			reader:  bytes.NewReader([]byte("final request body")),
			name:    trailerName,
			value:   trailerSecret,
		}
		candidate.ContentLength = -1
		return nil
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance: "test",
			RequestModel:     "test-model",
			CredentialMaterial: llm.NewAPILogCredentialMaterial(
				[]string{trailerName},
				nil,
			),
		}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read final response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close final response: %v", err)
	}
	group.Settle(ctx, apilog.AttemptSuccess)

	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 2 {
		t.Fatalf("durable attempts = %d, want redirect and final", len(attempts))
	}
	for _, durable := range attempts {
		assertDurableAttemptExcludes(t, durable, trailerSecret)
	}
}

func TestDoWithAPIAttemptsAsyncTrailerOnTransportErrorSanitizesRedirectSource(t *testing.T) {
	const (
		trailerName   = "X-Gateway-Credential"
		trailerSecret = "async-error-trailer-secret-sentinel"
	)
	transportErr := errors.New("final transport failed")
	ctx, group, logPath := durableAttemptContext(t, "sess-error-redirect-trailer", "ag_error_redirect_trailer")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	releaseRequest := make(chan struct{})
	requestFinished := make(chan error, 1)
	transportCalls := 0
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		switch transportCalls {
		case 1:
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://provider.test/final"}},
				Body:       io.NopCloser(strings.NewReader("source exposed " + trailerSecret)),
			}, nil
		case 2:
			go func(body io.ReadCloser) {
				<-releaseRequest
				_, readErr := io.ReadAll(body)
				requestFinished <- errors.Join(readErr, body.Close())
			}(request.Body)
			return nil, transportErr
		default:
			return nil, errors.New("unexpected extra RoundTrip")
		}
	})}
	client.CheckRedirect = func(candidate *http.Request, _ []*http.Request) error {
		candidate.Method = http.MethodPost
		candidate.Trailer = http.Header{trailerName: nil}
		candidate.Body = &credentialTrailerBody{
			request: candidate,
			reader:  bytes.NewReader([]byte("final request body")),
			name:    trailerName,
			value:   trailerSecret,
		}
		candidate.ContentLength = -1
		return nil
	}
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
		t.Fatal("transport error lost final attempt")
	}
	attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, transportErr)
	close(releaseRequest)
	if err := <-requestFinished; err != nil {
		t.Fatalf("finish request body: %v", err)
	}
	group.Settle(ctx, apilog.AttemptTransportFail)

	attempts := readDurableAttempts(t, logPath)
	if len(attempts) != 2 {
		t.Fatalf("durable attempts = %d, want redirect and final failure", len(attempts))
	}
	for _, durable := range attempts {
		encoded, err := apilog.MarshalRecord(durable)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(trailerSecret)) {
			t.Fatalf("durable attempt persisted asynchronous trailer credential %q", trailerSecret)
		}
	}
}

func TestRedirectCredentialMaterialRetainsOnlyUniqueValues(t *testing.T) {
	roundTripper := &apiAttemptRoundTripper{}
	roundTripper.mergeCredentialMaterial(llm.NewAPILogCredentialMaterial(
		[]string{"X-Redirect-Credential"},
		nil,
		"initial-secret",
	))
	const redirects = 20
	for i := 0; i < redirects; i++ {
		secret := "redirect-secret-" + strconv.Itoa(i)
		candidate, err := http.NewRequest(http.MethodGet, "https://provider.test/hop?access_token="+url.QueryEscape(secret), nil)
		if err != nil {
			t.Fatal(err)
		}
		candidate.Header.Set("X-Redirect-Credential", secret)
		roundTripper.mergeRedirectCandidateMaterial(candidate)
	}
	roundTripper.mu.Lock()
	material := roundTripper.credentialMaterial
	roundTripper.mu.Unlock()
	if got, want := len(material.Values), redirects+1; got != want {
		t.Fatalf("credential values after %d redirects = %d, want %d unique values", redirects, got, want)
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
