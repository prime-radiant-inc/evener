# Codex Response-Header Timeout Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop Serf from replaying a fully written model-generation request after its response-header wait times out.

**Architecture:** The configured standard HTTP transport records whether `WroteRequest` completed and converts only transport-owned post-write timeouts into a distinct `llm.Error`. That error remains `KindTimeout` but is non-retryable; generic request timeouts and retryable HTTP responses retain their current behavior.

**Tech Stack:** Go, `net/http`, `net/http/httptrace`, `httptest`, Serf's `llm.Error` and retry classifier.

## Global Constraints

- Preserve one ten-minute response-header window for a streaming model call.
- Never automatically replay a model-generation POST after it was fully written and then timed out waiting for response headers.
- Preserve retries for HTTP 408, HTTP 429, and retryable HTTP 5xx responses.
- Default tests must not use provider credentials or external network access.
- Use TDD and commit the independently testable timeout change before status work.

---

### Task 1: Specify the non-retryable timeout contract

**Files:**
- Modify: `llm/errors_test.go`
- Modify: `llm/providers/openai/response_header_timeout_test.go`

**Interfaces:**
- Consumes: `llm.Error`, `llm.Kind(error)`, `llm.Classify(error)`, and `llm.StreamGenerate`.
- Produces: executable expectations for a non-retryable `KindTimeout` and a single streaming request attempt.

- [ ] **Step 1: Change the adapter timeout expectation and add the retry-loop regression**

In `TestAdapter_Stream_ResponseHeaderTimeout`, require the returned `llm.Error`
to report `Retryable() == false` and `llm.Classify(err) == llm.ErrorClassPermanent`.

Add `TestStreamGenerate_ResponseHeaderTimeoutDoesNotRetry` beside it. Use an
`httptest.Server` whose handler increments an atomic request count, signals
receipt through a `sync.Once`-closed channel, and withholds headers until test
cleanup. Register the real OpenAI adapter in an `llm.Client`, run
`llm.StreamGenerate` with `MaxRetries: 1`, drain the result, and assert:

```go
if got := requests.Load(); got != 1 {
	t.Fatalf("requests = %d, want 1", got)
}
if err == nil || llm.Kind(err) != llm.KindTimeout {
	t.Fatalf("error = %v, want timeout", err)
}
```

In `llm/errors_test.go`, add a package-level contract test which constructs the
new internal response-header timeout with a `context.DeadlineExceeded` cause and
asserts `KindTimeout`, `ErrorClassPermanent`, `Retryable() == false`, and
`errors.Is(err, context.DeadlineExceeded)`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'Test(ResponseHeaderTimeout|Adapter_Stream_ResponseHeaderTimeout|StreamGenerate_ResponseHeaderTimeoutDoesNotRetry)' -count=1
```

Expected: FAIL because response-header timeouts still use the generic retryable
request-timeout type and the retry loop submits two requests.

### Task 2: Classify the post-write response-header phase

**Files:**
- Modify: `llm/adapter_timeout.go`
- Modify: `llm/errors.go`
- Modify: `llm/context_errors.go`
- Modify: `llm/classify.go`
- Modify: `llm/errorkind.go`
- Modify: `llm/adapter_timeout_test.go`

**Interfaces:**
- Produces: `responseHeaderTimeoutError`,
  `newResponseHeaderTimeoutError(provider, message string, cause error) error`,
  and an internal `responseHeaderTimeoutTransport` implementing
  `http.RoundTripper`.
- Consumes: the error type in `WrapContextError`, `Classify`, and `Kind`.

- [ ] **Step 1: Add the smallest error type and preserve it through wrapping**

Add a `responseHeaderTimeoutError` embedding `httpBaseError`. Its constructor
sets status code zero, the supplied provider and cause, and `retryable: false`.
Update `Kind` to map it to `KindTimeout`.

Update `WrapContextError` to check for this type before the generic
`context.DeadlineExceeded` branch and return the same timeout category with the
adapter's provider name. Update `Classify` so typed `llm.Error` disposition is
consulted before the bare deadline fallback. Generic
`NewRequestTimeoutError(..., context.DeadlineExceeded)` remains retryable
because its own retry bit is true.

- [ ] **Step 2: Add transport phase observation**

Wrap only configured standard transports when `AdapterTimeout.Request > 0`:

```go
type responseHeaderTimeoutTransport struct {
	base http.RoundTripper
}

func (t responseHeaderTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			wroteRequest.Store(true)
		}
	}}
	resp, err := t.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err == nil || !wroteRequest.Load() || req.Context().Err() != nil {
		return resp, err
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return nil, newResponseHeaderTimeoutError("", err.Error(), err)
	}
	return nil, err
}
```

`ClientWithAdapterTimeout` installs this wrapper around the cloned standard
transport. Opaque transports remain unchanged. Adjust helper tests to unwrap
the wrapper when inspecting the cloned `*http.Transport`, and add a real local
request test proving a caller-expired context is not reclassified.

- [ ] **Step 3: Run focused tests and verify GREEN**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'Test(ResponseHeaderTimeout|Adapter_Stream_ResponseHeaderTimeout|StreamGenerate_ResponseHeaderTimeoutDoesNotRetry|ClientWithAdapterTimeout|Classify|WrapContextError)' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run the complete affected packages**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the timeout fix**

```bash
git add llm/adapter_timeout.go llm/adapter_timeout_test.go llm/errors.go llm/errors_test.go llm/context_errors.go llm/classify.go llm/errorkind.go llm/providers/openai/response_header_timeout_test.go
git commit -m "fix(llm): do not replay response-header timeouts"
```
