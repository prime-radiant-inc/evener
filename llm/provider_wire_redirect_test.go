package llm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

type redirectProvider struct {
	name         string
	responseBody []byte
	new          func(string, *http.Client) llm.ProviderAdapter
}

func redirectProviders() []redirectProvider {
	return []redirectProvider{
		{
			name:         "openai",
			responseBody: []byte(`{"id":"resp-1","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`),
			new: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &openai.Adapter{
					APIKey:              "test-key",
					BaseURL:             baseURL,
					Client:              client,
					DisableChatFallback: true,
					DefaultHeaders:      map[string]string{"X-Initial-Hop": "initial", "X-Stays-Visible": "visible"},
				}
			},
		},
		{
			name:         "anthropic",
			responseBody: []byte(`{"id":"msg_1","model":"test-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`),
			new: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &anthropic.Adapter{
					APIKey:         "test-key",
					BaseURL:        baseURL,
					Client:         client,
					DefaultHeaders: map[string]string{"X-Initial-Hop": "initial", "X-Stays-Visible": "visible"},
				}
			},
		},
		{
			name:         "google",
			responseBody: []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"test-model"}`),
			new: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &google.Adapter{
					APIKey:         "test-key",
					BaseURL:        baseURL,
					Client:         client,
					DefaultHeaders: map[string]string{"X-Initial-Hop": "initial", "X-Stays-Visible": "visible"},
				}
			},
		},
		{
			name:         "openai-compatible",
			responseBody: []byte(`{"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`),
			new: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &openaicompat.Adapter{
					APIKey:         "test-key",
					BaseURL:        baseURL,
					Client:         client,
					DefaultHeaders: map[string]string{"X-Initial-Hop": "initial", "X-Stays-Visible": "visible"},
				}
			},
		},
	}
}

type redirectRequest struct {
	method string
	url    *url.URL
	header http.Header
	body   []byte
}

type blockingRedirectSink struct {
	mu           sync.Mutex
	attempts     []apilog.APIAttemptRecord
	firstEntered chan struct{}
	releaseFirst chan struct{}
	releaseOnce  sync.Once
}

func (s *blockingRedirectSink) release() {
	s.releaseOnce.Do(func() { close(s.releaseFirst) })
}

func (s *blockingRedirectSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	if record.AttemptIndex == 1 {
		close(s.firstEntered)
		<-s.releaseFirst
	}
	s.mu.Lock()
	s.attempts = append(s.attempts, record)
	s.mu.Unlock()
	return nil
}

func (*blockingRedirectSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *blockingRedirectSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func TestCoreCompleteWireCaptureRecordsAcceptedRedirectHopsExactly(t *testing.T) {
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 2)
			secondStarted := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				if r.URL.Path != "/redirect-target" {
					http.SetCookie(w, &http.Cookie{Name: "redirect_cookie", Value: "present", Path: "/"})
					w.Header().Set("Location", "/redirect-target")
					w.WriteHeader(http.StatusFound)
					_, _ = w.Write([]byte("redirect-response"))
					return
				}
				close(secondStarted)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(provider.responseBody)
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("create cookie jar: %v", err)
			}
			client.Jar = jar
			baseTransport := client.Transport
			var transportCalls atomic.Int32
			client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				transportCalls.Add(1)
				return baseTransport.RoundTrip(request)
			})
			var redirectChecks int
			var redirectCheckErr error
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				redirectChecks++
				if len(via) != 1 {
					redirectCheckErr = fmt.Errorf("redirect history length = %d, want 1", len(via))
				}
				req.Header.Del("X-Initial-Hop")
				req.Header["X-Redirect-Hop"] = []string{"first", "second"}
				return nil
			}
			sink := &blockingRedirectSink{firstEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
			defer sink.release()
			groupID := "ag_redirect_" + provider.name
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			done := make(chan error, 1)
			go func() {
				response, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
					Model:    "test-model",
					Messages: []llm.Message{llm.User("hello")},
				})
				if err == nil && response.Text() != "hello" {
					err = errors.New("redirected response text was not hello")
				}
				done <- err
			}()

			select {
			case <-sink.firstEntered:
			case <-time.After(time.Second):
				t.Fatal("first redirect hop did not reach canonical append")
			}
			select {
			case <-secondStarted:
				t.Fatal("redirect target started before first hop append returned")
			default:
			}
			sink.release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("redirected completion did not finish")
			}
			if redirectChecks != 1 {
				t.Fatalf("CheckRedirect calls = %d, want 1", redirectChecks)
			}
			if redirectCheckErr != nil {
				t.Fatal(redirectCheckErr)
			}
			if got := transportCalls.Load(); got != 2 {
				t.Fatalf("custom transport calls = %d, want 2", got)
			}

			firstRequest := <-requests
			secondRequest := <-requests
			attempts := sink.snapshot()
			if len(attempts) != 2 {
				t.Fatalf("canonical attempts = %d, want one per actual request", len(attempts))
			}
			assertRedirectAttempt(t, attempts[0], groupID, 1, firstRequest, []byte("redirect-response"), http.StatusFound, apilog.AttemptProviderReject)
			assertRedirectAttempt(t, attempts[1], groupID, 2, secondRequest, provider.responseBody, http.StatusOK, apilog.AttemptSuccess)
			if firstRequest.method != http.MethodPost || secondRequest.method != http.MethodGet {
				t.Fatalf("wire methods = %s -> %s, want POST -> GET", firstRequest.method, secondRequest.method)
			}
			if len(secondRequest.body) != 0 {
				t.Fatalf("redirect GET body = %q, want empty", secondRequest.body)
			}
			if got := secondRequest.header.Get("Cookie"); got != "redirect_cookie=present" {
				t.Fatalf("redirect cookie = %q, want cookie from original client jar", got)
			}
			if got := attempts[1].Request.Headers["X-Redirect-Hop"]; len(got) != 2 || got[0] != "first" || got[1] != "second" {
				t.Fatalf("redirect hop header = %q, want [first second]", got)
			}
			if _, present := attempts[1].Request.Headers["X-Initial-Hop"]; present {
				t.Fatalf("redirect attempt retained removed header: %#v", attempts[1].Request.Headers)
			}
			if got := attempts[1].Request.Headers["X-Stays-Visible"]; len(got) != 1 || got[0] != "visible" {
				t.Fatalf("preserved redirect header = %q, want [visible]", got)
			}
		})
	}
}

func TestCoreCompleteWithoutAPIAttemptPreservesRedirectClientBehavior(t *testing.T) {
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				if r.URL.Path != "/redirect-target" {
					http.SetCookie(w, &http.Cookie{Name: "redirect_cookie", Value: "present", Path: "/"})
					w.Header().Set("Location", "/redirect-target")
					w.WriteHeader(http.StatusFound)
					_, _ = w.Write([]byte("redirect-response"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(provider.responseBody)
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("create cookie jar: %v", err)
			}
			client.Jar = jar
			baseTransport := client.Transport
			var transportCalls atomic.Int32
			client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				transportCalls.Add(1)
				return baseTransport.RoundTrip(request)
			})
			var redirectChecks atomic.Int32
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				redirectChecks.Add(1)
				if len(via) != 1 {
					return fmt.Errorf("redirect history length = %d, want 1", len(via))
				}
				req.Header.Set("X-Redirect-Hop", "direct")
				return nil
			}

			response, err := provider.new(server.URL, client).Complete(context.Background(), llm.Request{
				Model:    "test-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			if err != nil {
				t.Fatalf("Complete without API-attempt context: %v", err)
			}
			if response.Text() != "hello" {
				t.Fatalf("response text = %q, want hello", response.Text())
			}
			if got := redirectChecks.Load(); got != 1 {
				t.Fatalf("CheckRedirect calls = %d, want 1", got)
			}
			if got := transportCalls.Load(); got != 2 {
				t.Fatalf("custom transport calls = %d, want 2", got)
			}
			if len(requests) != 2 {
				t.Fatalf("actual requests = %d, want 2", len(requests))
			}
			<-requests
			secondRequest := <-requests
			if got := secondRequest.header.Get("Cookie"); got != "redirect_cookie=present" {
				t.Fatalf("redirect cookie = %q, want cookie from original client jar", got)
			}
			if got := secondRequest.header.Get("X-Redirect-Hop"); got != "direct" {
				t.Fatalf("redirect callback header = %q, want direct", got)
			}
		})
	}
}

func TestCoreCompleteWireCaptureUsesActualBodyReadAfterBodyPreservingRedirect(t *testing.T) {
	for _, provider := range redirectProviders() {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(provider.name+"/"+http.StatusText(status), func(t *testing.T) {
				requests := make(chan redirectRequest, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read request body: %v", err)
						return
					}
					requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
					if r.URL.Path != "/redirect-target" {
						w.Header().Set("Location", "/redirect-target")
						w.WriteHeader(status)
						_, _ = w.Write([]byte("body-preserving-redirect"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(provider.responseBody)
				}))
				t.Cleanup(server.Close)

				replacementBody := []byte(`{"redirect":"replacement-body"}`)
				client := server.Client()
				var staleGetBodyObserved bool
				client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
					if request.GetBody == nil {
						return errors.New("body-preserving redirect unexpectedly lacks inherited GetBody")
					}
					stale, err := request.GetBody()
					if err != nil {
						return err
					}
					staleBytes, err := io.ReadAll(stale)
					_ = stale.Close()
					if err != nil {
						return err
					}
					staleGetBodyObserved = !bytes.Equal(staleBytes, replacementBody)
					request.Body = io.NopCloser(bytes.NewReader(replacementBody))
					request.ContentLength = int64(len(replacementBody))
					return nil
				}
				sink := &outcomeSink{}
				groupID := fmt.Sprintf("ag_body_redirect_%s_%d", provider.name, status)
				ctx := llm.WithAPIAttemptSink(
					llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
					sink,
				)
				response, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
					Model:    "test-model",
					Messages: []llm.Message{llm.User("hello")},
				})
				if err != nil || response.Text() != "hello" {
					t.Fatalf("Complete = (%q, %v), want redirected hello", response.Text(), err)
				}
				if !staleGetBodyObserved {
					t.Fatal("test did not establish stale inherited GetBody")
				}
				if len(requests) != 2 || len(sink.attempts) != 2 {
					t.Fatalf("requests/attempts = %d/%d, want 2/2", len(requests), len(sink.attempts))
				}
				firstRequest := <-requests
				secondRequest := <-requests
				assertRedirectAttempt(t, sink.attempts[0], groupID, 1, firstRequest, []byte("body-preserving-redirect"), status, apilog.AttemptProviderReject)
				assertRedirectAttempt(t, sink.attempts[1], groupID, 2, secondRequest, provider.responseBody, http.StatusOK, apilog.AttemptSuccess)
				if secondRequest.method != http.MethodPost {
					t.Fatalf("redirect method = %q, want POST", secondRequest.method)
				}
				if !bytes.Equal(secondRequest.body, replacementBody) {
					t.Fatalf("server redirect body = %q, want replacement %q", secondRequest.body, replacementBody)
				}
			})
		}
	}
}

func TestCoreCompleteWireCaptureAppendsUnreadLargeRedirectBeforeNextHop(t *testing.T) {
	largeRedirectBody := bytes.Repeat([]byte("redirect-evidence-"), 512)
	if len(largeRedirectBody) <= 2<<10 {
		t.Fatalf("test body = %d bytes, want larger than net/http redirect slurp limit", len(largeRedirectBody))
	}
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 2)
			secondStarted := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				if r.URL.Path != "/redirect-target" {
					w.Header().Set("Location", "/redirect-target")
					w.Header().Set("Content-Length", strconv.Itoa(len(largeRedirectBody)))
					w.WriteHeader(http.StatusFound)
					_, _ = w.Write(largeRedirectBody)
					return
				}
				close(secondStarted)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(provider.responseBody)
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			sink := &blockingRedirectSink{firstEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
			defer sink.release()
			groupID := "ag_large_redirect_" + provider.name
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			done := make(chan error, 1)
			go func() {
				_, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
					Model:    "test-model",
					Messages: []llm.Message{llm.User("hello")},
				})
				done <- err
			}()

			select {
			case <-sink.firstEntered:
			case <-time.After(time.Second):
				t.Fatal("large redirect did not reach first canonical append")
			}
			select {
			case <-secondStarted:
				t.Fatal("second hop started before unread redirect attempt append returned")
			default:
			}
			sink.release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("redirected completion did not finish")
			}

			firstRequest := <-requests
			<-requests
			attempts := sink.snapshot()
			if len(attempts) != 2 {
				t.Fatalf("canonical attempts = %d, want 2", len(attempts))
			}
			assertRedirectAttempt(t, attempts[0], groupID, 1, firstRequest, nil, http.StatusFound, apilog.AttemptProviderReject)
		})
	}
}

func TestCoreCompleteWireCapturePreservesErrUseLastResponse(t *testing.T) {
	const responseBody = `{"error":{"message":"redirect response"}}`
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte(responseBody))
			}))
			t.Cleanup(server.Close)

			client := server.Client()
			var redirectChecks atomic.Int32
			client.CheckRedirect = func(*http.Request, []*http.Request) error {
				redirectChecks.Add(1)
				return http.ErrUseLastResponse
			}
			sink := &outcomeSink{}
			groupID := "ag_use_last_response_" + provider.name
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			_, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
				Model:    "test-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			var statusErr interface{ StatusCode() int }
			if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusFound {
				t.Fatalf("ErrUseLastResponse provider error = %v, want status 302", err)
			}
			if got := redirectChecks.Load(); got != 1 {
				t.Fatalf("CheckRedirect calls = %d, want 1", got)
			}
			if len(requests) != 1 {
				t.Fatalf("actual requests = %d, want original hop only", len(requests))
			}
			request := <-requests
			if len(sink.attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want original hop only", len(sink.attempts))
			}
			assertRedirectAttempt(t, sink.attempts[0], groupID, 1, request, []byte(responseBody), http.StatusFound, apilog.AttemptProviderReject)
		})
	}
}

func TestCoreCompleteWireCaptureRecordsRedirectThenFinalTransportFailure(t *testing.T) {
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte("redirect-before-transport-failure"))
			}))
			t.Cleanup(server.Close)

			transportErr := errors.New("final redirect transport sentinel")
			client := server.Client()
			baseTransport := client.Transport
			client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/redirect-target" {
					if request.Body != nil {
						_, _ = io.ReadAll(request.Body)
					}
					return nil, transportErr
				}
				return baseTransport.RoundTrip(request)
			})
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			sink := &outcomeSink{}
			groupID := "ag_redirect_transport_failure_" + provider.name
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			_, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
				Model:    "test-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			if !errors.Is(err, transportErr) {
				t.Fatalf("Complete error = %v, want final transport identity", err)
			}
			if len(requests) != 1 || len(sink.attempts) != 2 {
				t.Fatalf("server requests/attempts = %d/%d, want 1/2", len(requests), len(sink.attempts))
			}
			firstRequest := <-requests
			assertRedirectAttempt(t, sink.attempts[0], groupID, 1, firstRequest, []byte("redirect-before-transport-failure"), http.StatusFound, apilog.AttemptProviderReject)
			finalAttempt := sink.attempts[1]
			if finalAttempt.AttemptIndex != 2 || finalAttempt.Request.Method != http.MethodGet || finalAttempt.Outcome != apilog.AttemptTransportFail {
				t.Fatalf("final transport attempt = %+v, want index 2 GET transport_failure", finalAttempt)
			}
			endpoint, err := url.Parse(finalAttempt.Request.Endpoint)
			if err != nil || endpoint.Path != "/redirect-target" {
				t.Fatalf("final transport endpoint = %q, %v", finalAttempt.Request.Endpoint, err)
			}
			requestBody, err := apilog.DecodeBody(finalAttempt.Request.Body)
			if err != nil || len(requestBody) != 0 {
				t.Fatalf("final redirected GET body = %q, %v; want empty", requestBody, err)
			}
			if finalAttempt.Response != nil {
				t.Fatalf("final transport attempt invented response: %+v", finalAttempt.Response)
			}
		})
	}
}

func TestCoreCompleteWireCaptureRecordsRejectedRedirectWithoutInventingHop(t *testing.T) {
	for _, provider := range redirectProviders() {
		t.Run(provider.name, func(t *testing.T) {
			requests := make(chan redirectRequest, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				requests <- redirectRequest{method: r.Method, url: cloneURL(r.URL), header: r.Header.Clone(), body: body}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(http.StatusFound)
				_, _ = w.Write([]byte("rejected-redirect-response"))
			}))
			t.Cleanup(server.Close)

			redirectErr := errors.New("redirect policy sentinel")
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return redirectErr }
			sink := &outcomeSink{}
			groupID := "ag_rejected_redirect_" + provider.name
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			_, err := provider.new(server.URL, client).Complete(ctx, llm.Request{
				Model:    "test-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			if !errors.Is(err, redirectErr) {
				t.Fatalf("redirect error = %v, want original policy error identity", err)
			}
			if len(requests) != 1 {
				t.Fatalf("actual requests = %d, want 1 rejected redirect source only", len(requests))
			}
			request := <-requests
			if len(sink.attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want 1 actual request", len(sink.attempts))
			}
			assertRedirectAttempt(t, sink.attempts[0], groupID, 1, request, []byte("rejected-redirect-response"), http.StatusFound, apilog.AttemptProviderReject)
		})
	}
}

func assertRedirectAttempt(t *testing.T, attempt apilog.APIAttemptRecord, groupID string, index int, request redirectRequest, responseBody []byte, status int, outcome apilog.AttemptOutcomeClass) {
	t.Helper()
	if attempt.AttemptGroupID != groupID || attempt.AttemptIndex != index {
		t.Fatalf("attempt group/index = %q/%d, want %q/%d", attempt.AttemptGroupID, attempt.AttemptIndex, groupID, index)
	}
	if attempt.Request.Method != request.method {
		t.Fatalf("attempt method = %q, want wire method %q", attempt.Request.Method, request.method)
	}
	endpoint, err := url.Parse(attempt.Request.Endpoint)
	if err != nil {
		t.Fatalf("parse attempt endpoint: %v", err)
	}
	if endpoint.Path != request.url.Path {
		t.Fatalf("attempt endpoint path = %q, want wire path %q", endpoint.Path, request.url.Path)
	}
	recordedRequest, err := apilog.DecodeBody(attempt.Request.Body)
	if err != nil || !bytes.Equal(recordedRequest, request.body) {
		t.Fatalf("recorded request body = %q, %v; want wire bytes %q", recordedRequest, err, request.body)
	}
	if attempt.Response == nil || attempt.Response.StatusCode == nil || *attempt.Response.StatusCode != status {
		t.Fatalf("attempt response = %+v, want status %d", attempt.Response, status)
	}
	recordedResponse, err := apilog.DecodeBody(attempt.Response.Body)
	if err != nil || !bytes.Equal(recordedResponse, responseBody) {
		t.Fatalf("recorded response body = %q, %v; want %q", recordedResponse, err, responseBody)
	}
	if attempt.Outcome != outcome {
		t.Fatalf("attempt outcome = %q, want %q", attempt.Outcome, outcome)
	}
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}
