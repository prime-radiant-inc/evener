# Streaming Response-Header Timeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** Implemented

**Goal:** Bound each HTTP streaming attempt while it waits for response headers without adding a Serf-owned overall deadline to a healthy response stream.

**Architecture:** Replace the connect-only client helper with shared adapter-timeout client construction that clones a standard `http.Transport`, wraps context-aware dial hooks with `Connect`, and applies `Request` to `ResponseHeaderTimeout`. Keep context-free `DialTLS`, opaque `RoundTripper` implementations, and caller `http.Client.Timeout` policy authoritative; do not add a streaming request-context deadline; continue applying `StreamRead` only while consuming SSE lines.

**Tech Stack:** Go 1.26, `net/http`, `httptest`, Serf's `llm` timeout/error abstractions, and deterministic Go tests.

**Design Spec:** `docs/superpowers/specs/2026-07-13-streaming-response-header-timeout-design.md`

## Global Constraints

- Follow `docs/testing.md`: default tests must be deterministic and must not use provider credentials, external network access, quota, current model behavior, arbitrary sleeps, or polling races.
- `AdapterTimeout.Request` continues to bound the entire request/response cycle for non-streaming calls.
- For streaming calls, `AdapterTimeout.Request` bounds only the wait for response headers after the request body is written.
- Serf must not add an overall streaming request-context or `http.Client.Timeout`
  deadline. Caller-supplied contexts and explicit client timeouts remain
  unchanged and authoritative after response headers arrive.
- `AdapterTimeout.StreamRead` continues to bound the wait between SSE lines after headers arrive.
- Clone `http.DefaultTransport` or a caller-supplied `*http.Transport`; never replace either with a bare `http.Transport`.
- Wrap caller `DialContext` and `DialTLSContext` hooks with the Connect-bounded
  context rather than replacing them. Preserve context-free `DialTLS` unchanged
  as caller-authoritative because it cannot be safely deadline-wrapped.
- Preserve opaque caller-supplied `http.RoundTripper` implementations and treat their timeout behavior as authoritative.
- Do not add a timeout setting, provider-specific timer, goroutine/body-wrapper timeout, WebSocket path, compatibility alias, or notification-preemption behavior.
- Do not change stream retry counts, backoff, partial-output retry behavior, session scheduling, or job-notification delivery.

---

## File Map

| File | Responsibility in this change |
|---|---|
| `llm/adapter_timeout.go` | Define the shared standard-transport cloning, dial-hook wrapping, and adapter-timeout client contract. |
| `llm/adapter_timeout_test.go` | Exercise cloned/default/custom transports, context-aware dial-hook deadlines, deprecated DialTLS authority, response-header configuration, and unchanged context behavior. |
| `llm/types.go` | Document the phase-dependent meaning of `AdapterTimeout.Request`. |
| `llm/client.go` | Document the bounded network phases applied by `Complete` and `Stream`. |
| `llm/all_tests_replay_fuzz_test.go` | Keep the root replay suite calling the renamed timeout contract tests. |
| `llm/core_contracts_fuzz_test.go` | Keep timeout contract fuzz coverage on the renamed client helper. |
| `llm/providers/openai/response_header_timeout_test.go` | Prove a real Responses stream times out before headers and remains alive after headers. |
| `llm/providers/openai/adapter.go` | Use the shared client helper for non-streaming Responses requests. |
| `llm/providers/openai/responses.go` | Use the shared client helper for streaming Responses requests. |
| `llm/providers/openai/chatcompletions.go` | Use the shared client helper for Chat Completions streaming requests. |
| `llm/providers/openai/token_count.go` | Use the shared client helper for token-count requests. |
| `llm/providers/anthropic/adapter.go` | Use the shared helper for completion, token-count, and streaming requests. |
| `llm/providers/google/adapter.go` | Use the shared helper for completion, token-count, and streaming requests. |
| `llm/providers/openaicompat/adapter.go` | Use the shared helper for completion and streaming requests. |
| `llm/providers/kimi/adapter.go` | Use the shared helper for token-count requests. |
| `llm/providers/{openai,anthropic,google,openaicompat}/complete_roundtrip_fuzz_test.go` | Remove comments that incorrectly say adapter timeouts replace opaque transports. |

Files are kept in their existing roles. The focused OpenAI regression gets a
new test file instead of adding more unrelated coverage to the already-large
`adapter_test.go`.

### Task 1: Shared Streaming Response-Header Timeout Contract

**Files:**
- Modify: `llm/adapter_timeout.go:9-43`
- Modify: `llm/adapter_timeout_test.go:42-140`
- Modify: `llm/types.go:520-525`
- Modify: `llm/client.go:99-153`
- Modify: `llm/all_tests_replay_fuzz_test.go:5-17`
- Modify: `llm/core_contracts_fuzz_test.go:82-110`
- Create: `llm/providers/openai/response_header_timeout_test.go`
- Modify: `llm/providers/openai/adapter.go:539-570`
- Modify: `llm/providers/openai/responses.go:227-258`
- Modify: `llm/providers/openai/chatcompletions.go:40-65`
- Modify: `llm/providers/openai/token_count.go:25-47`
- Modify: `llm/providers/anthropic/adapter.go:120-272`
- Modify: `llm/providers/google/adapter.go:135-315`
- Modify: `llm/providers/openaicompat/adapter.go:265-290,636-661`
- Modify: `llm/providers/kimi/adapter.go:100-125`
- Modify: `llm/providers/openai/complete_roundtrip_fuzz_test.go:53-59`
- Modify: `llm/providers/anthropic/complete_roundtrip_fuzz_test.go:54-60`
- Modify: `llm/providers/google/complete_roundtrip_fuzz_test.go:55-60`
- Modify: `llm/providers/openaicompat/complete_roundtrip_fuzz_test.go:78-86`

**Interfaces:**
- Consumes: `AdapterTimeout{Connect, Request, StreamRead}`, `ApplyAdapterTimeout`, `StreamReadSSEOptions`, `http.DefaultTransport`, and provider-owned `*http.Client` values.
- Produces: `ClientWithAdapterTimeout(client *http.Client, at *AdapterTimeout) *http.Client` and the streaming contract `Connect -> response-header wait -> per-SSE-line read`.

- [ ] **Step 1: Add failing shared transport contract tests**

Keep `TestApplyAdapterTimeout_Request`, `TestApplyAdapterTimeout_Nil`, and
`TestApplyAdapterTimeout_Streaming`. Replace the transport/client tests below
them in `llm/adapter_timeout_test.go` with:

```go
type adapterTimeoutRoundTripper struct {
	called bool
}

func (rt *adapterTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.called = true
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestAdapterTransport_ConfiguresDefaultTransport(t *testing.T) {
	at := &AdapterTimeout{Connect: 5 * time.Second, Request: 7 * time.Second}
	transport := AdapterTransport(at)
	if transport == nil {
		t.Fatal("expected configured transport")
	}

	defaultTransport := http.DefaultTransport.(*http.Transport)
	if transport == defaultTransport {
		t.Fatal("expected a clone of http.DefaultTransport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil; connect timeout would not be enforced")
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if (transport.Proxy == nil) != (defaultTransport.Proxy == nil) {
		t.Fatal("default proxy behavior was not preserved")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	conn, err := transport.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext to local listener failed: %v", err)
	}
	_ = conn.Close()
	<-accepted
}

func TestAdapterTransport_NoTransportTimeoutReturnsNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   *AdapterTimeout
	}{
		{name: "nil", at: nil},
		{name: "zero", at: &AdapterTimeout{}},
		{name: "stream read only", at: &AdapterTimeout{StreamRead: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if transport := AdapterTransport(tc.at); transport != nil {
				t.Fatalf("AdapterTransport() = %T, want nil", transport)
			}
		})
	}
}

func TestClientWithAdapterTimeout_ConfiguresStandardTransport(t *testing.T) {
	originalTransport := http.DefaultTransport.(*http.Transport).Clone()
	originalTransport.MaxIdleConnsPerHost = 23
	originalTransport.ResponseHeaderTimeout = 3 * time.Second
	original := &http.Client{Transport: originalTransport, Timeout: 30 * time.Second}

	client := ClientWithAdapterTimeout(original, &AdapterTimeout{
		Connect: 5 * time.Second,
		Request: 7 * time.Second,
	})
	if client == original {
		t.Fatal("expected a copied client")
	}
	if client.Timeout != original.Timeout {
		t.Fatalf("client Timeout = %v, want %v", client.Timeout, original.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport == originalTransport {
		t.Fatal("expected a cloned transport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != 23 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 23", transport.MaxIdleConnsPerHost)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil; connect timeout would not be enforced")
	}
	if originalTransport.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("original ResponseHeaderTimeout mutated to %v", originalTransport.ResponseHeaderTimeout)
	}
}

func TestClientWithAdapterTimeout_ClonesDefaultTransport(t *testing.T) {
	original := &http.Client{Timeout: 30 * time.Second}
	client := ClientWithAdapterTimeout(original, &AdapterTimeout{Request: 7 * time.Second})
	if client == original {
		t.Fatal("expected a copied client")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	defaultTransport := http.DefaultTransport.(*http.Transport)
	if transport == defaultTransport {
		t.Fatal("expected a clone of http.DefaultTransport")
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 7s", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
	if (transport.Proxy == nil) != (defaultTransport.Proxy == nil) {
		t.Fatal("default proxy behavior was not preserved")
	}
}

func TestClientWithAdapterTimeout_PreservesOpaqueTransport(t *testing.T) {
	transport := &adapterTimeoutRoundTripper{}
	original := &http.Client{Transport: transport}
	client := ClientWithAdapterTimeout(original, &AdapterTimeout{
		Connect: time.Second,
		Request: time.Second,
	})

	if client == original {
		t.Fatal("expected a copied client")
	}
	if client.Transport != transport {
		t.Fatalf("Transport = %T, want original opaque transport", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if !transport.called {
		t.Fatal("opaque transport was not called")
	}
}

func TestClientWithAdapterTimeout_NoTransportTimeoutReturnsOriginal(t *testing.T) {
	original := &http.Client{Timeout: 30 * time.Second}
	for _, tc := range []struct {
		name string
		at   *AdapterTimeout
	}{
		{name: "nil", at: nil},
		{name: "zero", at: &AdapterTimeout{}},
		{name: "stream read only", at: &AdapterTimeout{StreamRead: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if client := ClientWithAdapterTimeout(original, tc.at); client != original {
				t.Fatal("client copied without a connect or request timeout")
			}
		})
	}
}
```

Final review adds three focused client-construction contracts in the same test
file: a custom `DialContext` HTTP request and custom `DialTLSContext` HTTPS
request must both run through real `http.Client.Do` calls with the Connect
deadline on their contexts, while deprecated context-free `DialTLS` remains
unchanged and caller-authoritative.

- [ ] **Step 2: Add failing OpenAI Responses lifecycle tests**

Create `llm/providers/openai/response_header_timeout_test.go`:

```go
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
```

The two-second `time.After` cases are failure guards, not success-path sleeps.
The healthy-stream timer is deliberately tied to twice `requestTimeout`; it is
the behavior under test and remains comfortably below `StreamRead`.

- [ ] **Step 3: Run the new tests and verify the root-cause failure**

Run:

```bash
go test ./llm ./llm/providers/openai -run 'Test(AdapterTransport|ClientWithAdapterTimeout|Adapter_Stream_(ResponseHeaderTimeout|RequestTimeoutStopsAtResponseHeaders))' -count=1
```

Expected: build failure because `ClientWithAdapterTimeout` is undefined. If the
shared tests are temporarily excluded to isolate the provider behavior,
`TestAdapter_Stream_ResponseHeaderTimeout` must fail with `Stream did not
return after AdapterTimeout.Request`; it must not fail because of provider
credentials or an external request.

- [ ] **Step 4: Implement shared standard-transport cloning**

In `llm/adapter_timeout.go`, keep `StreamReadSSEOptions` and replace the timeout
comments and transport/client helpers with:

```go
// ApplyAdapterTimeout creates a context with the appropriate deadline from AdapterTimeout.
// For non-streaming requests, it uses the Request timeout for the whole call.
// Streaming requests get no additional overall context deadline: Request is
// applied while waiting for HTTP response headers, and StreamRead is checked per
// SSE line. Caller-supplied context and HTTP client policies remain authoritative.
// If at is nil, returns the context unchanged.
func ApplyAdapterTimeout(ctx context.Context, at *AdapterTimeout, streaming bool) (context.Context, context.CancelFunc) {
	if at == nil {
		return ctx, func() {}
	}
	if !streaming && at.Request > 0 {
		return context.WithTimeout(ctx, at.Request)
	}
	return ctx, func() {}
}

// AdapterTransport returns a configured clone of http.DefaultTransport. Connect
// bounds context-aware dialing without replacing caller hooks, and Request bounds
// the wait for response headers. Returns nil when neither is configured.
func AdapterTransport(at *AdapterTimeout) *http.Transport {
	return configuredAdapterTransport(http.DefaultTransport.(*http.Transport), at)
}

func configuredAdapterTransport(base *http.Transport, at *AdapterTimeout) *http.Transport {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return nil
	}
	transport := base.Clone()
	if at.Connect > 0 {
		connectTimeout := at.Connect
		dialContext := transport.DialContext
		if dialContext == nil {
			dialContext = (&net.Dialer{}).DialContext
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			return dialContext(ctx, network, address)
		}

		if dialTLSContext := transport.DialTLSContext; dialTLSContext != nil {
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, connectTimeout)
				defer cancel()
				return dialTLSContext(ctx, network, address)
			}
		}
		// DialTLS has no context to bound safely without a goroutine that may leak.
		// Preserve it unchanged as caller-authoritative transport policy.
	}
	if at.Request > 0 {
		transport.ResponseHeaderTimeout = at.Request
	}
	return transport
}

// ClientWithAdapterTimeout returns a copy of client configured with adapter
// transport timeouts. Standard transports are cloned; opaque RoundTrippers and
// caller client policy, including Timeout, remain authoritative.
func ClientWithAdapterTimeout(client *http.Client, at *AdapterTimeout) *http.Client {
	if at == nil || (at.Connect <= 0 && at.Request <= 0) {
		return client
	}

	copy := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return &copy
	}
	copy.Transport = configuredAdapterTransport(transport, at)
	return &copy
}
```

Do not retain `ClientWithConnectTimeout` as an alias. Every repository call
site is migrated in Step 6.

- [ ] **Step 5: Update the timeout contract comments and replay coverage**

In `llm/types.go`, change the `AdapterTimeout` declaration to:

```go
// AdapterTimeout defines granular timeout configuration for adapter-level HTTP operations.
type AdapterTimeout struct {
	Connect    time.Duration `json:"connect"`     // time to establish the network connection (default: 10s)
	Request    time.Duration `json:"request"`     // whole non-stream call, or streaming response-header wait (default: 120s)
	StreamRead time.Duration `json:"stream_read"` // max time between consecutive stream events (default: 30s)
}
```

In `llm/client.go`, use these exact comments above the default timeout blocks:

```go
// Complete defaults to a bounded network profile covering connection setup and
// the whole request/response cycle.
```

```go
// Stream defaults to phase-bounded connection, response-header, and per-line
// stream-read timeouts without adding an overall stream deadline. Caller-supplied
// context and HTTP client policies remain authoritative.
```

In `llm/all_tests_replay_fuzz_test.go`, replace timeout test calls with:

```go
		TestApplyAdapterTimeout_Request(t)
		TestApplyAdapterTimeout_Nil(t)
		TestApplyAdapterTimeout_Streaming(t)
		TestAdapterTransport_ConfiguresDefaultTransport(t)
		TestAdapterTransport_NoTransportTimeoutReturnsNil(t)
		TestClientWithAdapterTimeout_ConfiguresStandardTransport(t)
		TestClientWithAdapterTimeout_WrapsDialContextWithConnectDeadline(t)
		TestClientWithAdapterTimeout_WrapsDialTLSContextWithConnectDeadline(t)
		TestClientWithAdapterTimeout_PreservesDialTLSAuthority(t)
		TestClientWithAdapterTimeout_ClonesDefaultTransport(t)
		TestClientWithAdapterTimeout_PreservesOpaqueTransport(t)
		TestClientWithAdapterTimeout_NoTransportTimeoutReturnsOriginal(t)
```

In `fuzzTimeoutAndHeaders` in `llm/core_contracts_fuzz_test.go`, preserve the
existing `AdapterTransport` assertions and replace only the client-helper block
with:

```go
	client := &http.Client{}
	if ClientWithAdapterTimeout(client, nil) != client {
		t.Fatal("client copied without transport timeouts")
	}
	if cp := ClientWithAdapterTimeout(client, at); cp == client || cp.Transport == nil {
		t.Fatal("client transport was not copied and configured")
	}
```

- [ ] **Step 6: Migrate every HTTP adapter to the shared helper**

At every existing call site in the files below, make this exact symbol change:

```go
client := llm.ClientWithAdapterTimeout(a.Client, req.AdapterTimeout)
```

Apply it to:

```text
llm/providers/openai/adapter.go
llm/providers/openai/responses.go
llm/providers/openai/chatcompletions.go
llm/providers/openai/token_count.go
llm/providers/anthropic/adapter.go
llm/providers/google/adapter.go
llm/providers/openaicompat/adapter.go
llm/providers/kimi/adapter.go
```

Delete the obsolete comments claiming `ClientWithConnectTimeout` replaces fake
transports from these files; retain the function-level descriptions that say
the helpers assemble fuzzed requests:

```text
llm/providers/openai/complete_roundtrip_fuzz_test.go
llm/providers/anthropic/complete_roundtrip_fuzz_test.go
llm/providers/google/complete_roundtrip_fuzz_test.go
llm/providers/openaicompat/complete_roundtrip_fuzz_test.go
```

Verify the old helper and stale connect-only description are gone:

```bash
rg -n 'ClientWithConnectTimeout|connect \+ stream read' llm
```

Expected: no matches.

- [ ] **Step 7: Format and run the focused red-to-green tests**

Run:

```bash
gofmt -w llm/adapter_timeout.go llm/adapter_timeout_test.go llm/types.go llm/client.go llm/all_tests_replay_fuzz_test.go llm/core_contracts_fuzz_test.go llm/providers/openai/response_header_timeout_test.go llm/providers/openai/adapter.go llm/providers/openai/responses.go llm/providers/openai/chatcompletions.go llm/providers/openai/token_count.go llm/providers/anthropic/adapter.go llm/providers/google/adapter.go llm/providers/openaicompat/adapter.go llm/providers/kimi/adapter.go llm/providers/openai/complete_roundtrip_fuzz_test.go llm/providers/anthropic/complete_roundtrip_fuzz_test.go llm/providers/google/complete_roundtrip_fuzz_test.go llm/providers/openaicompat/complete_roundtrip_fuzz_test.go
go test ./llm ./llm/providers/openai -run 'Test(ApplyAdapterTimeout|AdapterTransport|ClientWithAdapterTimeout|Adapter_Stream_(ResponseHeaderTimeout|RequestTimeoutStopsAtResponseHeaders))' -count=1
```

Expected: both packages report `ok`. The stalled-header test returns a typed,
retryable timeout; the healthy stream test completes after its body is held
open for twice `AdapterTimeout.Request`.

- [ ] **Step 8: Run all directly affected provider packages**

Run:

```bash
go test ./llm ./llm/providers/openai ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openaicompat ./llm/providers/kimi -count=1
```

Expected: every listed package reports `ok`; no scripted transport attempts a
real network connection.

- [ ] **Step 9: Run the repository verification gate**

Run:

```bash
make test
git diff --check
git status --short
```

Expected: `make test` exits successfully, `git diff --check` prints nothing,
and `git status --short` lists only the intended implementation files plus the
pre-existing user-owned untracked files. Do not add the `.private-journal`
entries or `docs/superpowers/plans/2026-05-07-serf-daemon-prereqs.md`.

- [ ] **Step 10: Commit the implementation**

Review the exact paths before staging, then commit only the files named in this
task:

```bash
git status --short
git add llm/adapter_timeout.go llm/adapter_timeout_test.go llm/types.go llm/client.go llm/all_tests_replay_fuzz_test.go llm/core_contracts_fuzz_test.go llm/providers/openai/response_header_timeout_test.go llm/providers/openai/adapter.go llm/providers/openai/responses.go llm/providers/openai/chatcompletions.go llm/providers/openai/token_count.go llm/providers/anthropic/adapter.go llm/providers/google/adapter.go llm/providers/openaicompat/adapter.go llm/providers/kimi/adapter.go llm/providers/openai/complete_roundtrip_fuzz_test.go llm/providers/anthropic/complete_roundtrip_fuzz_test.go llm/providers/google/complete_roundtrip_fuzz_test.go llm/providers/openaicompat/complete_roundtrip_fuzz_test.go
git diff --cached --check
git commit -m "fix: bound streaming response-header waits" -m "Apply AdapterTimeout.Request as the standard HTTP transport response-header timeout without adding an overall stream deadline. Preserve caller-supplied context and HTTP client policy. Clone standard transports so proxy, TLS, HTTP/2, pool behavior, and context-aware dial hooks survive, and preserve context-free DialTLS and opaque injected RoundTrippers as authoritative seams.\n\nMigrate every HTTP provider to the shared helper and add deterministic regressions for stalled headers, retryable timeout classification, and a stream that remains healthy beyond the request timeout after headers arrive."
```

Expected: the commit succeeds without bypassing hooks, and `git status --short`
afterward contains only the pre-existing user-owned untracked files.
