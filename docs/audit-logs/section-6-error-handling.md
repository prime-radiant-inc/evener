# Audit: Section 6 -- Error Handling and Retry

**Auditor:** Bot (Claude Opus 4.6)
**Date:** 2026-02-11
**Spec:** unified-llm-spec.md, lines 1274-1468
**Codebase:** internal/llm/ (errors.go, sdk_errors.go, retry.go, retry_util.go, context_errors.go, ratelimit.go, generate.go, stream_generate.go, generate_object.go, middleware.go, providers/*)

---

## Summary

Section 6 compliance is strong overall. The error taxonomy, HTTP status mapping, retry policy, and backoff algorithm are well-implemented. Eight gaps identified, ranging from missing error hierarchy formalism to subtle retryability deviations.

---

## 6.1 Error Taxonomy

### PASS: All error types exist

Every error type in the spec hierarchy is implemented:

| Spec Type | Implementation | File |
|-----------|---------------|------|
| SDKError (base) | `Error` interface + `httpErrorBase`/`nonHTTPErrorBase` structs | errors.go, sdk_errors.go |
| ProviderError | `httpErrorBase` struct (all HTTP error types embed it) | errors.go:39-63 |
| AuthenticationError | `AuthenticationError` struct | errors.go:66 |
| AccessDeniedError | `AccessDeniedError` struct | errors.go:67 |
| NotFoundError | `NotFoundError` struct | errors.go:68 |
| InvalidRequestError | `InvalidRequestError` struct | errors.go:65 |
| RateLimitError | `RateLimitError` struct | errors.go:73 |
| ServerError | `ServerError` struct | errors.go:74 |
| ContentFilterError | `ContentFilterError` struct | errors.go:71 |
| ContextLengthError | `ContextLengthError` struct | errors.go:70 |
| QuotaExceededError | `QuotaExceededError` struct | errors.go:72 |
| RequestTimeoutError | `RequestTimeoutError` struct | errors.go:69 |
| AbortError | `AbortError` struct | sdk_errors.go:35 |
| NetworkError | `NetworkError` struct | sdk_errors.go:36 |
| StreamError | `StreamError` struct | sdk_errors.go:37 |
| InvalidToolCallError | `InvalidToolCallError` struct | sdk_errors.go:38 |
| NoObjectGeneratedError | `NoObjectGeneratedError` struct | sdk_errors.go:39-42 |
| ConfigurationError | `ConfigurationError` struct | errors.go:23-37 |

### GAP 1: Extra error type not in spec -- `UnknownHTTPError` and `UnsupportedToolChoiceError`

**Severity: Low**
**Files:** errors.go:75, sdk_errors.go:43

The codebase defines `UnknownHTTPError` (errors.go:75) and `UnsupportedToolChoiceError` (sdk_errors.go:43), neither of which appear in the spec hierarchy. `UnknownHTTPError` is used as a catch-all for unmapped HTTP status codes (which the spec says should default to retryable). `UnsupportedToolChoiceError` is used when an adapter receives an unsupported tool_choice mode.

These are practical additions, but they are spec deviations. The spec hierarchy is defined as exhaustive.

**Recommendation:** Document as intentional extensions, or fold `UnknownHTTPError` behavior into `ServerError` (since both are retryable) and `UnsupportedToolChoiceError` into `InvalidRequestError` or `ConfigurationError`.

---

## 6.2 ProviderError Fields

### PASS: All required fields present

The `httpErrorBase` struct (errors.go:39-48) and the `Error` interface (errors.go:13-21) expose all six spec-required fields:

| Spec Field | Go Method | Impl |
|-----------|----------|------|
| provider | `Provider()` | httpErrorBase.provider |
| status_code | `StatusCode()` | httpErrorBase.statusCode |
| error_code | `ErrorCode()` | httpErrorBase.errorCode (extracted from raw body) |
| retryable | `Retryable()` | httpErrorBase.retryable |
| retry_after | `RetryAfter()` | httpErrorBase.retryAfter (*time.Duration) |
| raw | `Raw()` | httpErrorBase.rawResponse |

### PASS: SDKError base fields (message, cause)

All error types implement `Error() string` (message) and `Unwrap() error` (cause). Tests verify this: errors_test.go:177-214.

### PASS: error_code extraction

`extractErrorCode()` (errors.go:79-93) correctly extracts from both OpenAI (`error.code`) and Anthropic (`error.type`) response formats.

---

## 6.3 Retryability Classification

### PASS: Non-retryable errors correctly classified

All non-retryable errors match the spec table:
- AuthenticationError (401): retryable=false
- AccessDeniedError (403): retryable=false
- NotFoundError (404): retryable=false
- InvalidRequestError (400, 422): retryable=false
- ContextLengthError (413): retryable=false
- QuotaExceededError: retryable=false
- ContentFilterError: retryable=false
- ConfigurationError: retryable=false

### PASS: Retryable errors correctly classified

- RateLimitError (429): retryable=true
- ServerError (500-504): retryable=true
- RequestTimeoutError (408): retryable=true
- NetworkError: retryable=true
- StreamError: retryable=true

### PASS: Unknown errors default to retryable

Both implementations handle this:
1. `ErrorFromHTTPStatus` default case (errors.go:134-136): unknown status codes produce `UnknownHTTPError` with retryable=true.
2. `retryableError` in retry_util.go:27-39: errors that don't implement the `Error` interface are treated as retryable (line 39: `return true`).

### GAP 2: `NewRequestTimeoutError` (non-HTTP context deadline) is non-retryable, deviating from spec

**Severity: Medium**
**File:** errors.go:162-170

The spec says `RequestTimeoutError` is always retryable (section 6.3 table). `ErrorFromHTTPStatus(408)` correctly sets retryable=true. However, `NewRequestTimeoutError()` (used for context.DeadlineExceeded via context_errors.go:16) sets retryable=false.

Additionally, `retryableError()` in retry_util.go:31 explicitly short-circuits context.DeadlineExceeded as non-retryable before checking the Error interface.

This is arguably correct behavior (a caller-imposed deadline shouldn't be retried), but it creates a split where the same error type (`RequestTimeoutError`) has different retryability depending on construction path. The spec makes no such distinction.

**Recommendation:** Either document this as an intentional deviation from spec, or use a different error type for caller-imposed timeouts (e.g., `AbortError` for context cancellation already exists -- context deadlines could be treated similarly).

---

## 6.4 HTTP Status Code Mapping

### PASS: All HTTP status codes correctly mapped

`ErrorFromHTTPStatus` (errors.go:95-138) implements every row of the spec table:

| Status | Spec Error Type | Code Maps To | Correct |
|--------|----------------|-------------|---------|
| 400 | InvalidRequestError | InvalidRequestError (with message override) | Yes |
| 401 | AuthenticationError | AuthenticationError | Yes |
| 403 | AccessDeniedError | AccessDeniedError | Yes |
| 404 | NotFoundError | NotFoundError | Yes |
| 408 | RequestTimeoutError | RequestTimeoutError (retryable=true) | Yes |
| 413 | ContextLengthError | ContextLengthError | Yes |
| 422 | InvalidRequestError | InvalidRequestError (with message override) | Yes |
| 429 | RateLimitError | RateLimitError (retryable=true) | Yes |
| 500 | ServerError | ServerError (retryable=true) | Yes |
| 502 | ServerError | ServerError (retryable=true) | Yes |
| 503 | ServerError | ServerError (retryable=true) | Yes |
| 504 | ServerError | ServerError (retryable=true) | Yes |

### PASS: All adapters use ErrorFromHTTPStatus consistently

Every adapter (openai, anthropic, google, openaicompat) routes HTTP errors through `llm.ErrorFromHTTPStatus()`:
- openai/adapter.go:172
- anthropic/adapter.go:200
- google/adapter.go:211
- openaicompat/adapter.go:77

All pass the raw response body and Retry-After header.

### GAP 3: Gemini gRPC status code mapping is not implemented in the adapter

**Severity: Medium**
**File:** providers/google/adapter.go

The spec defines a gRPC-to-error-type mapping table (section 6.4) specifically for Gemini:

| gRPC Code | Spec Error Type |
|-----------|----------------|
| DEADLINE_EXCEEDED | RequestTimeoutError |
| UNAVAILABLE | ServerError |
| INTERNAL | ServerError |

The Google adapter relies solely on `ErrorFromHTTPStatus()` with the HTTP status code. When Gemini returns gRPC status codes embedded in its HTTP error response body (e.g., `{"error":{"status":"DEADLINE_EXCEEDED"}}`), the gRPC status is NOT used for classification.

The test at google/adapter_test.go:509-653 documents this behavior but does not test the spec-required gRPC-based classification. For example, `DEADLINE_EXCEEDED` with HTTP 504 maps to `ServerError` (via HTTP mapping), but the spec says it should map to `RequestTimeoutError` (via gRPC mapping).

Critically, Gemini can return HTTP 400 with gRPC status `RESOURCE_EXHAUSTED`, which would be classified as `InvalidRequestError` via HTTP mapping, but should be `RateLimitError` per the gRPC table.

**Recommendation:** Add a Gemini-specific error classification step that inspects the `error.status` field in the response body and overrides the HTTP-based classification when a known gRPC status code is present.

---

## 6.5 Error Message Classification

### PASS: Message-based classification implemented

`classifyByMessage` (errors.go:142-157) correctly handles all spec-required patterns:

| Spec Pattern | Implementation | Match |
|-------------|---------------|-------|
| "not found" or "does not exist" | `strings.Contains(lower, "not found") \|\| strings.Contains(lower, "does not exist")` | Yes |
| "unauthorized" or "invalid key" | `strings.Contains(lower, "unauthorized") \|\| strings.Contains(lower, "invalid key")` | Yes |
| "context length" or "too many tokens" | `strings.Contains(lower, "context length") \|\| strings.Contains(lower, "too many tokens")` | Yes |
| "content filter" or "safety" | `strings.Contains(lower, "content filter") \|\| strings.Contains(lower, "safety")` | Yes |

### PASS: Message classification only for ambiguous status codes

`classifyByMessage` is correctly invoked only for 400 and 422 (errors.go:106-111). Unambiguous status codes (401, 403, 404, 429, etc.) bypass message classification. This matches the spec intent of "ambiguous cases."

### GAP 4: Extra message-based classifications not in spec (quota/billing)

**Severity: Low**
**File:** errors.go:149

The implementation adds classification for `"quota"` and `"billing"` keywords mapping to `QuotaExceededError` (errors.go:149). The spec section 6.5 does not list these patterns. While this is arguably a useful enhancement, it is a deviation from the spec's explicit list.

**Recommendation:** Document as intentional enhancement or add to spec.

---

## 6.6 Retry Policy

### PASS: RetryPolicy record matches spec

`RetryPolicy` (retry.go:5-23) implements all spec fields:

| Spec Field | Go Field | Default | Spec Default | Match |
|-----------|---------|---------|-------------|-------|
| max_retries | MaxRetries | 2 | 2 | Yes |
| base_delay | BaseDelay | 1s | 1.0s | Yes |
| max_delay | MaxDelay | 60s | 60.0s | Yes |
| backoff_multiplier | BackoffMultiplier | 2.0 | 2.0 | Yes |
| jitter | Jitter | true | true | Yes |
| on_retry | OnRetry | nil | None | Yes |

### PASS: Exponential backoff formula correct

`retryDelay` (retry_util.go:89-123) implements: `delay = MIN(base_delay * (multiplier ^ n), max_delay)`. Jitter is `delay * (0.5 + rand)` which gives the range [0.5, 1.5], matching the spec's "+/- 50% jitter". Verified by tests at retry_util_test.go:172-201.

### PASS: Retry-After header handling correct

- If `RetryAfter <= MaxDelay`: uses provider delay (retry_util.go:101)
- If `RetryAfter > MaxDelay`: aborts without retry (retry_util.go:97-99)
- Tested at retry_util_test.go:55-124

### PASS: What gets retried

- `Generate()` (generate.go:189): Each step's LLM call is retried independently via `Retry()`. A retry on step 3 does NOT re-execute steps 1 and 2.
- `StreamGenerate()` (stream_generate.go:176-178): Only the initial `client.Stream()` connection is retried. Once streaming begins and events are consumed, errors emit `StreamEventError` with no retry (stream_generate.go:216-224).
- `GenerateObject()` (generate_object.go:37): LLM call is retried (delegates to `Generate()`). Schema validation failures return `NoObjectGeneratedError` which is non-retryable.

### PASS: Adapter-level retry behavior

No adapter retries internally. All adapters make a single HTTP request per call. Retry logic is composed at the Layer 4 level (`Generate`/`StreamGenerate`) via the `Retry()` utility. This matches the spec: "Provider adapters do NOT retry by default."

### PASS: Disabling retries

`MaxRetries = 0` disables retries. Tested at retry_util_test.go:151-170.

### GAP 5: `Retry()` is generic but not exposed as a standalone utility

**Severity: Low**
**File:** retry_util.go

The spec section 6.6 says: "Applications using the low-level API can compose retry behavior using a standalone `retry()` utility." The `Retry()` function exists in retry_util.go and is generic (`Retry[T any]`), but it is not exported from the package for external consumption beyond `internal/llm`. Since the entire `internal/` tree is unexported in Go, external applications cannot use this utility.

This may be intentional (serf is an application, not a library), but it technically doesn't satisfy the spec's implied public API contract.

**Recommendation:** Not actionable unless serf's llm package is intended for external use. Document as N/A for internal use.

### GAP 6: `StreamGenerate` retries the stream connection but spec says "Only the initial connection is retried"

**Severity: Low**
**File:** stream_generate.go:176-178

The `StreamGenerate` implementation wraps `client.Stream(callCtx, req)` in `Retry()`. This retries the entire stream establishment, including the HTTP request. If `client.Stream()` returns a `Stream` object but then the stream fails mid-consumption, no retry occurs -- which is correct.

However, there is a subtle issue: if the adapter's `Stream()` method returns an error before returning the `Stream` object (e.g., HTTP error on the initial request), the retry applies. But if the adapter successfully returns a `Stream` and then the first event is an error, no retry happens. This is correct per spec: "Once streaming has begun and partial data has been delivered, the library does not retry."

This is a PASS, just documenting the nuance.

---

## 6.7 Rate Limit Handling

### PASS: RateLimitError correctly raised for HTTP 429

`ErrorFromHTTPStatus(429)` returns `RateLimitError` with retryable=true and retry_after extracted from headers.

### PASS: Retry-After extracted from response header

All adapters pass `llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())` to `ErrorFromHTTPStatus`. `ParseRetryAfter` supports both integer-seconds and HTTP-date formats.

### PASS: Rate limit info exposed on successful responses

All adapters set `r.RateLimit = llm.ParseRateLimitHeaders(resp.Header)` on successful responses. `ParseRateLimitHeaders` (ratelimit.go:12-48) extracts x-ratelimit-* headers.

### GAP 7: No rate-limit middleware provided

**Severity: Low**
**File:** middleware.go

The spec section 6.7 says: "For applications that need proactive rate limiting... use middleware" and provides a `rate_limit_middleware` example. The codebase has a `Middleware` interface (middleware.go) that could support this, but no rate-limiting middleware implementation exists.

This is an optional feature per the spec (the middleware example is illustrative, not required), but it means proactive rate limiting is not available out of the box.

**Recommendation:** Low priority. The reactive rate-limit handling (retry on 429) is sufficient for serf's use case.

### GAP 8: `RateLimitError` does not extract `retry_after` from the response body

**Severity: Medium**
**File:** errors.go, ratelimit.go

The spec says: "the library raises RateLimitError with `retry_after` extracted from the response header." The implementation extracts `Retry-After` from HTTP headers, which is correct for the spec's primary requirement.

However, some providers (notably Anthropic) include retry-after information in the JSON error response body (e.g., `{"error":{"message":"Rate limited","retry_after":30}}`) rather than in HTTP headers. The current implementation only checks HTTP headers, which means these provider-specific retry hints are lost.

The `raw` field on the error does contain the full response body, so callers could extract this themselves, but the library doesn't do it automatically.

**Recommendation:** Consider checking the response body for retry-after hints as a fallback when the HTTP header is absent, at least for known provider formats.

---

## Test Coverage Assessment

Error handling tests are comprehensive:

- **errors_test.go**: 287 lines covering all HTTP status code mappings, message-based classification, error interface conformance, error code extraction, raw response exposure, and error unwrapping.
- **retry_util_test.go**: 232 lines covering exponential backoff, jitter, Retry-After header override, Retry-After abort, non-retryable error behavior, disabled retries, and unknown error retryability.
- **ratelimit_test.go**: 130 lines covering rate limit header parsing.
- **providers/google/adapter_test.go**: gRPC status code mapping tests (lines 509-653).

Notable test gaps:
- No test for `context.DeadlineExceeded` producing a non-retryable `RequestTimeoutError`.
- No test for `retryableError()` returning false on `context.Canceled`.
- No integration test for retry across `StreamGenerate()`.

---

## Findings Summary

| # | Gap | Severity | Section | Status |
|---|-----|---------|---------|--------|
| 1 | Extra error types (`UnknownHTTPError`, `UnsupportedToolChoiceError`) not in spec | Low | 6.1 | Open |
| 2 | `NewRequestTimeoutError` for context deadlines is non-retryable (spec says retryable) | Medium | 6.3 | Open |
| 3 | Gemini gRPC status code mapping not implemented in adapter | Medium | 6.4 | Open |
| 4 | Extra message-based classifications (quota/billing) not in spec | Low | 6.5 | Open |
| 5 | `Retry()` utility not publicly accessible (internal package) | Low | 6.6 | N/A (internal use) |
| 6 | StreamGenerate retry behavior documented (nuance, not a gap) | N/A | 6.6 | PASS |
| 7 | No proactive rate-limit middleware provided | Low | 6.7 | Open |
| 8 | `retry_after` not extracted from response body (only headers) | Medium | 6.7 | Open |
