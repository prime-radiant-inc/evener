# Task 9 report — LLM tests

## Changes and deletions

Re-evaluated every test in the eight scoped files. Production code was not changed.

- `identifier/identifier_covtest_test.go`
  - Strengthened the UUID overflow case to require `errInvalidUUIDPayload` identity and the zero UUID result.
  - Kept the real-git branch fixture because it exercises git common-directory behavior at the external process/filesystem boundary; its skip remains conditional on git being unavailable.
- `llm/adapter_timeout_covtest_test.go`
  - Kept exact timeout-source and caller-cancellation classification cases.
  - Deleted `TestCovResponseHeaderTimeoutTransportNonTimeoutErrorAfterWrite` and `TestCovResponseHeaderTimeoutTransportTimeoutAfterWrite`. They depended on loopback listeners and wall-clock deadlines; one allowed a nil non-timeout error and the other accepted a raw timeout instead of requiring the wrapper. The concrete production `*http.Transport` field offers no test-only deterministic RoundTripper seam, and production changes were out of scope.
- `llm/api_attempt_covtest_test.go`
  - Passed actual nil contexts instead of `context.TODO()`.
  - Asserted inactive attempts preserve seeded request and credential metadata exactly.
  - Made the nil-sink completion case prove that the pending-attempt slot is released by synchronously settling the group and checking the exact settlement.
  - Strengthened sanitized-error and header-cloning outcomes, including surviving safe headers.
- `llm/api_attempt_sanitize_covtest_test.go`
  - Required exact decoded cookie variants and complete surviving endpoint/header maps.
  - Replaced the encoded-branch fixture with raw newline text and a `\\n` pattern that cannot match until JSON encoding.
  - Deleted the impossible JSON string marshal-error claim.
- `llm/apilog_covtest_test.go`
  - Deleted the duplicate API-log directory error case.
  - Replaced scheduler yields and oversized buffer assumptions with causal channel handshakes that independently isolate pump close, stream-error, and close-during-forward branches.
  - Asserted propagated provider error identity and exact settlement outcome.
- `llm/apilog_open_covtest_test.go`
  - Deleted four unconditional tests that only skipped unreachable or environment-dependent branches (`os.NewFile` nil, post-open stat failure, non-contention flock failure, chmod failure).
  - Required nil file results, exact regular-file outcomes and mode, `fs.ErrNotExist`, and `ErrAPILogTargetLocked` identity (plus exact wrapping text).
- `llm/client_covtest_test.go`
  - Passed actual nil contexts and asserted exact group/sink ownership.
  - Made the compatibility validator fake record the model and return a sentinel whose identity is required.
  - Made existing-group and owned-scope settlements observable through the external API-log sink boundary.
- `llm/misc_covtest_test.go`
  - Required the exact UTF-8-safe truncation result.
  - Made the malformed GIF fixture prove header decode success and full decode failure before requiring `RasterMediaType` to fail with an empty media type.
  - Deleted the unconditional unreachable unsupported-format skip.

No sleeps, polling, listener timing, scheduler yields, or production seams were added.

## Commands and results

- `gofmt -w` on all eight scoped test files — passed.
- `git diff --check -- <eight scoped test files>` — passed.
- Initial `go test ./llm ...` with the session-private module cache — setup failed because that cache was empty and network/DNS access to `proxy.golang.org` is disabled.
- `GOMODCACHE=/Users/jesse/go/pkg/mod go test ./llm ./identifier -count=1` — `identifier` passed; `llm` could not run fully because the existing, unscoped `llm/adapter_timeout_test.go:TestAPITimeoutSourceForTransportRecognizesOwnedResponseHeaderTimeout` panicked when `httptest` could not bind `[::1]:0` (`operation not permitted`).
- `GOMODCACHE=/Users/jesse/go/pkg/mod go test ./llm ./identifier -run '^(<all test names from the eight scoped files>)$' -count=1` — passed (`llm` and `identifier`).
- The same all-scoped command with `-count=20` — passed.
- `GOMODCACHE=/Users/jesse/go/pkg/mod go test ./identifier -count=1` — passed.

## Staged paths

- `.superpowers/sdd/pr422-test-cleanup-plan/task-9-report.md`
- `identifier/identifier_covtest_test.go`
- `llm/adapter_timeout_covtest_test.go`
- `llm/api_attempt_covtest_test.go`
- `llm/api_attempt_sanitize_covtest_test.go`
- `llm/apilog_covtest_test.go`
- `llm/apilog_open_covtest_test.go`
- `llm/client_covtest_test.go`
- `llm/misc_covtest_test.go`

## Commit

`test: strengthen LLM coverage tests` (this commit)

## Concerns

- Full `llm` package execution remains unverified in this sandbox because an existing unscoped listener test does not capability-skip and the sandbox denies loopback binds. The complete edited-test surface passes deterministically, including 20 repeated runs.
- Dependency setup required the existing read-only ambient module cache because the session-private cache began empty and network access is disabled.
