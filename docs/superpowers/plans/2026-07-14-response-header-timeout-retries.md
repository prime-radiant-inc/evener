# Response-Header Timeout Retries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep Serf's ten-minute per-attempt response-header watchdog while allowing a completely stuck streaming request to advance through the existing ten-retry policy.

**Architecture:** Preserve the existing transport-phase detection and distinct `responseHeaderTimeoutError`. Change only that error's retry disposition, so both high-level generation and agent sessions automatically reuse their shared `RetryStream` policy. Add local-server contract tests for the exact initial-plus-retries count and immediate caller-cancellation stop; do not add a second retry loop, a whole-chain deadline, or provider-specific behavior.

**Tech Stack:** Go, `net/http`, `httptest`, Serf `llm.Error`, `RetryStream`, Go race detector.

## Scope and constraints

- The approved design is `docs/superpowers/specs/2026-07-13-codex-timeout-and-status-integrity-design.md`.
- This plan supersedes only the non-retryable disposition in `docs/superpowers/plans/2026-07-13-codex-response-header-timeout-retry.md`; the transport watchdog and status-integrity work in that branch remain in place.
- `AdapterTimeout.Request` remains a per-attempt response-header timeout for configured standard transports. The agent default remains ten minutes.
- `DefaultRetryPolicy` remains ten retries after the initial attempt. No retry budget or backoff values change.
- A post-write timeout is ambiguous and may duplicate provider work. That risk is accepted by the approved design.
- Caller cancellation or an enclosing total/step deadline must stop the active attempt, backoff, and all remaining retries.
- Tests use only local `httptest` servers and injected sleeps. They must not use provider credentials, external network access, or real backoff intervals.
- The root/subagent status fixes are already implemented on this branch. They receive regression verification here, not another redesign.

---

### Task 1: Specify retryable response-header timeout behavior

**Files:**
- Modify: `llm/errors_test.go:348`
- Modify: `llm/context_errors_test.go:1`
- Modify: `llm/providers/openai/response_header_timeout_test.go:1`

**Contracts:**
- The typed timeout remains `KindTimeout`, unwraps to `context.DeadlineExceeded`, and classifies as retryable.
- `WrapContextError` preserves its type, provider attribution, cause, and retry disposition.
- `MaxRetries: 2` produces exactly three HTTP requests when every attempt reaches the response-header watchdog.
- Cancellation during the first retry wait returns `context.Canceled` and prevents a second HTTP request.

- [ ] **Step 1: Replace the error-classification regression test**

Replace `TestResponseHeaderTimeoutError_IsNonRetryable` in `llm/errors_test.go` with:

```go
func TestResponseHeaderTimeoutError_IsRetryable(t *testing.T) {
	err := newResponseHeaderTimeoutError("openai", "timed out awaiting response headers", context.DeadlineExceeded)

	if got := Kind(err); got != KindTimeout {
		t.Fatalf("Kind(error) = %v, want %v", got, KindTimeout)
	}
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(error) = %v, want %v", got, ErrorClassRetryable)
	}
	var providerErr Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want llm.Error", err)
	}
	if !providerErr.Retryable() {
		t.Fatal("response-header timeout must be retryable")
	}
	if !retryableError(err) {
		t.Fatal("retryableError(response-header timeout) = false, want true")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline cause", err)
	}
}
```

- [ ] **Step 2: Pin error wrapping at the retry-policy boundary**

Add this test to `llm/context_errors_test.go`:

```go
func TestWrapContextError_ResponseHeaderTimeoutPreservesRetryability(t *testing.T) {
	original := newResponseHeaderTimeoutError(
		"",
		"timed out awaiting response headers",
		context.DeadlineExceeded,
	)
	wrapped := WrapContextError("openai", original)

	var timeout *responseHeaderTimeoutError
	if !errors.As(wrapped, &timeout) {
		t.Fatalf("WrapContextError(response-header timeout) = %T, want *responseHeaderTimeoutError", wrapped)
	}
	var providerErr Error
	if !errors.As(wrapped, &providerErr) {
		t.Fatalf("wrapped error = %T, want llm.Error", wrapped)
	}
	if providerErr.Provider() != "openai" {
		t.Fatalf("Provider() = %q, want %q", providerErr.Provider(), "openai")
	}
	if !providerErr.Retryable() {
		t.Fatal("wrapped response-header timeout must remain retryable")
	}
	if got := Classify(wrapped); got != ErrorClassRetryable {
		t.Fatalf("Classify(wrapped) = %v, want %v", got, ErrorClassRetryable)
	}
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatalf("wrapped error = %v, want context deadline cause", wrapped)
	}
}
```

- [ ] **Step 3: Extract one deterministic withheld-headers fixture**

Replace the repeated withheld-header server setup in `llm/providers/openai/response_header_timeout_test.go` with this helper near the imports:

```go
type withheldHeadersFixture struct {
	server       *httptest.Server
	firstRequest <-chan struct{}
	requests     *atomic.Int32
}

func newWithheldHeadersFixture(t *testing.T) withheldHeadersFixture {
	t.Helper()

	firstRequest := make(chan struct{})
	releaseHandlers := make(chan struct{})
	requests := &atomic.Int32{}
	var firstRequestOnce sync.Once
	var releaseOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
		firstRequestOnce.Do(func() { close(firstRequest) })
		<-releaseHandlers
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseHandlers) })
		server.Close()
	})

	return withheldHeadersFixture{
		server:       server,
		firstRequest: firstRequest,
		requests:     requests,
	}
}

func (f withheldHeadersFixture) waitForFirstRequest(t *testing.T) {
	t.Helper()
	select {
	case <-f.firstRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive the streaming request")
	}
}
```

Use `fixture.server.URL`, `fixture.server.Client()`, and `fixture.waitForFirstRequest(t)` in the response-header timeout tests. Keep the separate healthy-after-headers server unchanged because it exercises a different transport phase.

- [ ] **Step 4: Change the adapter-level expectation to retryable**

In `TestAdapter_Stream_ResponseHeaderTimeout`, retain the assertions for `errors.Is(err, context.DeadlineExceeded)` and `llm.KindTimeout`, then replace the disposition assertions with:

```go
if !providerErr.Retryable() {
	t.Fatal("response-header timeout must be retryable after the watchdog ends the attempt")
}
if got := llm.Classify(err); got != llm.ErrorClassRetryable {
	t.Fatalf("Classify(error) = %v, want %v", got, llm.ErrorClassRetryable)
}
```

- [ ] **Step 5: Replace the no-retry test with an exact configured-attempt test**

Replace `TestStreamGenerate_ResponseHeaderTimeoutDoesNotRetry` with:

```go
func TestStreamGenerate_ResponseHeaderTimeoutRetriesConfiguredAttempts(t *testing.T) {
	fixture := newWithheldHeadersFixture(t)
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: fixture.server.URL,
		Client:  fixture.server.Client(),
	}
	client := llm.NewClient()
	client.Register(adapter)
	prompt := "hello"
	result, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
		Client:         client,
		Model:          "gpt-5",
		Provider:       "openai",
		Prompt:         &prompt,
		AdapterTimeout: &llm.AdapterTimeout{Request: 50 * time.Millisecond, StreamRead: time.Second},
		RetryPolicy: &llm.RetryPolicy{
			MaxRetries: 2,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
			Jitter:     false,
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer func() { _ = result.Close() }()

	fixture.waitForFirstRequest(t)
	for range result.Events() {
	}
	_, resultErr := result.Response()
	if resultErr == nil || llm.Kind(resultErr) != llm.KindTimeout {
		t.Fatalf("error = %v, want timeout", resultErr)
	}
	if got := fixture.requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (initial request plus two retries)", got)
	}
}
```

- [ ] **Step 6: Add the cancellation stop test**

Add beside the configured-attempt test:

```go
func TestStreamGenerate_ResponseHeaderTimeoutRetryStopsOnCancellation(t *testing.T) {
	fixture := newWithheldHeadersFixture(t)
	adapter := &Adapter{
		APIKey:  "test-key",
		BaseURL: fixture.server.URL,
		Client:  fixture.server.Client(),
	}
	client := llm.NewClient()
	client.Register(adapter)
	prompt := "hello"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleepCalls atomic.Int32

	result, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
		Client:         client,
		Model:          "gpt-5",
		Provider:       "openai",
		Prompt:         &prompt,
		AdapterTimeout: &llm.AdapterTimeout{Request: 50 * time.Millisecond, StreamRead: time.Second},
		RetryPolicy: &llm.RetryPolicy{
			MaxRetries: 10,
			BaseDelay:  time.Second,
			MaxDelay:   time.Second,
			Jitter:     false,
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			sleepCalls.Add(1)
			cancel()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer func() { _ = result.Close() }()

	fixture.waitForFirstRequest(t)
	for range result.Events() {
	}
	_, resultErr := result.Response()
	if !errors.Is(resultErr, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", resultErr)
	}
	if got := fixture.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 after cancellation", got)
	}
	if got := sleepCalls.Load(); got != 1 {
		t.Fatalf("sleep calls = %d, want 1", got)
	}
}
```

This test cancels at the retry boundary. Existing transport tests continue to prove that caller cancellation during the active request is not reclassified as a response-header timeout.

- [ ] **Step 7: Format and run the focused tests to verify RED**

Run:

```bash
gofmt -w llm/errors_test.go llm/context_errors_test.go llm/providers/openai/response_header_timeout_test.go
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'Test(ResponseHeaderTimeoutError_IsRetryable|WrapContextError_ResponseHeaderTimeoutPreservesRetryability|Adapter_Stream_ResponseHeaderTimeout|StreamGenerate_ResponseHeaderTimeout)' -count=1
```

Expected: FAIL for the retry disposition. The configured-attempt test must observe one request instead of three, and the cancellation test must return the terminal timeout without invoking its injected retry wait. If the failures occur for fixture cleanup, request construction, or timing unrelated to disposition, correct the tests before proceeding.

---

### Task 2: Make the typed timeout participate in the existing retry policy

**Files:**
- Modify: `llm/errors.go:151`
- Test: `llm/errors_test.go`
- Test: `llm/context_errors_test.go`
- Test: `llm/providers/openai/response_header_timeout_test.go`

**Interfaces:**
- Produces: a retryable `responseHeaderTimeoutError`.
- Reuses unchanged: `Classify`, `retryableError`, `RetryStream`, `WrapContextError`, `DefaultRetryPolicy`, and the transport watchdog.

- [ ] **Step 1: Change only the typed error's documented disposition and retry bit**

Replace the type comment and constructor field in `llm/errors.go` with:

```go
// responseHeaderTimeoutError reports that a fully written request timed out
// before response headers arrived. It is retryable so a bounded stuck attempt
// advances through the configured retry policy; the provider may still complete
// the ambiguous timed-out request. Its category is [KindTimeout].
type responseHeaderTimeoutError struct{ httpBaseError }
```

```go
func newResponseHeaderTimeoutError(provider string, message string, cause error) error {
	base := httpBaseError{
		provider:   strings.TrimSpace(provider),
		statusCode: 0,
		message:    message,
		retryable:  true,
		cause:      cause,
	}
	return &responseHeaderTimeoutError{base}
}
```

Do not alter `RetryStream`, `DefaultRetryPolicy`, `Classify`, `WrapContextError`, or any session loop. Both `llm.StreamGenerate` and `agent.Session.callModel` already consume `RetryStream`, so adding another retry layer would multiply attempts.

- [ ] **Step 2: Format and run the focused tests to verify GREEN**

Run:

```bash
gofmt -w llm/errors.go
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'Test(ResponseHeaderTimeoutError_IsRetryable|WrapContextError_ResponseHeaderTimeoutPreservesRetryability|Adapter_Stream_ResponseHeaderTimeout|StreamGenerate_ResponseHeaderTimeout)' -count=1
```

Expected: PASS. The local server must see exactly three requests for `MaxRetries: 2`, and exactly one request when cancellation interrupts the first retry wait.

- [ ] **Step 3: Run the complete affected timeout packages**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -count=1
```

Expected: PASS, including the unchanged healthy-after-headers, caller-deadline, HTTP retry classification, and default retry-policy tests.

- [ ] **Step 4: Inspect the production diff for scope**

Run:

```bash
git diff -- llm/errors.go llm/errors_test.go llm/context_errors_test.go llm/providers/openai/response_header_timeout_test.go
git diff --check
```

Expected: one production behavior change (`retryable: false` to `true`), its comment, and deterministic contract tests. There must be no retry-loop, timeout-duration, or status-code changes.

- [ ] **Step 5: Commit the retry change and its tests**

Run:

```bash
git status --short
git add llm/errors.go llm/errors_test.go llm/context_errors_test.go llm/providers/openai/response_header_timeout_test.go
git commit -m "fix(llm): retry bounded response-header timeouts" -m "Keep the existing post-write response-header timeout type and ten-minute per-attempt watchdog, but let the shared retry policy advance after a completely stuck attempt. This accepts the approved duplicate-generation risk without adding a second retry loop or a whole-chain deadline.\n\nLocal withheld-header tests prove the configured initial-plus-retries count, error wrapping, timeout classification, and immediate cancellation of remaining retries."
```

Expected: the pre-commit hook passes; do not skip or disable it.

---

### Task 3: Verify timeout and status integrity together

**Files:**
- Verify: `llm/...`
- Verify: `agent/session_state.go`
- Verify: `agent/session_awaiting_test.go`
- Verify: `cmd/serf-hub/internal/hubcore/prober.go`
- Verify: `cmd/serf-hub/internal/hubcore/roster.go`
- Verify: `cmd/serf-hub/internal/hubcore/tree.go`
- Verify: `cmd/serf-hub/app_threadlist.go`
- Verify: `cmd/serf-hub/app_threadread.go`

**Proof obligations:**

| Concern | Required evidence |
|---|---|
| Completely stuck attempt | Local server withholds headers; attempt returns at the configured watchdog |
| Retry count | `MaxRetries: 2` yields exactly three requests; default-policy test still pins ten retries |
| Completely stuck chain | Every attempt is bounded; no whole-chain deadline; cancellation test stops remaining attempts |
| Healthy response | Existing healthy-after-headers test proves the watchdog stops at headers |
| Root status | Settled root with only a live child is idle; queued input or pending notification is active |
| Child status | Running in-process child projects active in tree/list/read until its job becomes terminal |
| Project rollup | A running child keeps the task tree working without counting parent and child separately |

- [ ] **Step 1: Run focused timeout tests under the race detector**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test -race ./llm ./llm/providers/openai -run 'Test(ResponseHeaderTimeout|Adapter_Stream_ResponseHeaderTimeout|StreamGenerate_ResponseHeaderTimeout|ClientWithAdapterTimeout|DefaultRetryPolicy_Values)' -count=1
```

Expected: PASS with no races. If the 50 ms local transport watchdog is load-sensitive under `-race`, diagnose the exact phase and adjust only the test fixture's watchdog while keeping a finite guard; do not add sleeps or weaken request-count assertions.

- [ ] **Step 2: Run focused status ownership tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestWireState_(LiveChildDoesNotMakeIdleParentActive|PendingParentWorkMakesIdleParentActive|AwaitingOutranksAutonomy)' -count=1
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub -run 'Test(HubThreadListProjectsRunningSubagentActive|PastThreadReadProjectsRunningSubagentActive)$' -count=1
```

Expected: PASS. `FuzzHubcoreScenarios` runs the checked-in seed corpus containing the status-prober, roster, tree-child, and one-task-tree rollup scenarios; the app tests pin thread-list and thread-read projection.

- [ ] **Step 3: Run all directly affected packages**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore -count=1
```

Expected: PASS.

- [ ] **Step 4: Build every shipped binary**

Run:

```bash
make build-all
```

Expected: PASS.

- [ ] **Step 5: Run the full repository suite**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./... -count=1
```

Expected target: PASS. This checkout previously exposed two unrelated, pre-existing environment-sensitive failures: `cmd/serf-fuzz-harvest/FuzzHarvestProgram/seed#3` and `cmd/serf-hub/internal/fspaths/FuzzResolveInRoot` seeds `#0` and `#4` (`/var` versus `/private/var`). If any failure remains, capture its exact package, test, seed, and message. Re-run it in isolation on `HEAD`; if it is one of those known failures, also run that exact test at the branch merge base in a disposable worktree to prove it is not introduced here. Do not report the full suite as passing when it did not.

- [ ] **Step 6: Audit the finished branch and record proof**

Run:

```bash
git diff --check
git status --short --branch
git log --oneline --decorate --max-count=15
git diff --stat "$(git merge-base HEAD origin/main)"...HEAD
```

Expected: no uncommitted implementation files, no whitespace errors, and only the approved timeout/status/spec/plan commits on the branch. Report separately:

1. focused timeout results, including exact request counts;
2. focused root/child/project status results;
3. race result;
4. build result;
5. full-suite result and any baseline comparison;
6. branch, merge, and push state.

Do not merge or push unless Jesse separately requests it.
