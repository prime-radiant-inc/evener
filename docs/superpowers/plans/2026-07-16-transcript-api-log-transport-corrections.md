# Transcript/API Log Transport Corrections Implementation Plan

> **For Jesse:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task.

**Goal:** Correct the remaining Project 2 transport, provider, credential-validation, and endpoint-provenance defects without widening transcript/API-log ownership or changing retry/fallback policy.

**Architecture:** Keep semantic model-call metadata owned by adapters and durable agent boundaries, while the shared transport owns one append-only API-attempt record per actual wire cycle. Transport capture records only bytes it observes; exactness is explicit and conservative whenever EOF, full request write, or raw transport ownership cannot be proven. Provider work consumes that corrected body-evidence contract rather than draining bodies or reconstructing transport evidence.

**Tech Stack:** Go 1.25 behavior oracles, `net/http`, `httptrace`, HTTP/1.1 and HTTP/2 test servers, deterministic scripted providers, local TCP listeners, Go race detector.

**Starting point:** Commit `0a73c01eed26737489a4d497b70058290e427207` on an isolated WIP branch.

---

## Scope and execution contract

This plan is limited to the following corrections:

- Transparent retries performed inside the standard Go transport produce one ascending API-attempt record per wire cycle.
- Request and response body evidence never performs an unbounded instrumentation drain and never waits indefinitely for application-owned I/O to finish.
- Raw body exactness remains true only when standard transport ownership and byte completeness are proven.
- The standard gzip response wrapper is race-safe under concurrent `Read` and `Close`, while retaining Go 1.25 protocol-specific close behavior.
- Explicit-empty `Accept-Encoding` and `Range` handling matches the distinct Go 1.25 HTTP/1.1 and HTTP/2 predicates.
- The named OpenAI, OpenAI-compatible, model-listing, and Hub defects are fixed without broader selection-policy changes.
- Semantic endpoint provenance uses the final response request URL and is sanitized again at the durable agent boundary.
- Anthropic terminal-order tests may receive the final, test-only deterministic cleanup described below.

Do not include any of the following:

- Retry or fallback selection changes beyond the Responses timeout/context and empty-stream bugs named in Task 6.
- Transcript schema changes, doctor work, Superpowers work, or Project 3 and later work.
- Backward-compatibility aliases or compatibility layers.
- Arbitrary transport, provider, or agent rewrites.
- Live provider calls, external network dependencies, credentials, quota, ambient model behavior, or timing sleeps in default tests.

Every task follows the same execution rule:

1. Assign a fresh implementer who has not implemented an earlier task in this plan.
2. Make the named real-behavior test fail for the stated defect before editing production code.
3. Make the smallest production change that satisfies that test and the existing contract.
4. Run the task-specific deterministic test commands.
5. Assign a different fresh reviewer who has not implemented or reviewed an earlier task in this plan.
6. Address all review findings and rerun the task-specific commands before committing.

The implementer must read `docs/testing.md` before changing tests. No task may make `make test` or `go test ./...` issue live provider requests.

## Binding prerequisite from the concurrent body-evidence core

Do not begin Task 1 until the concurrent body-evidence core is merged into the implementation branch. The following names and meanings are binding:

```go
type APIAttemptMeta struct {
    // Existing fields remain unchanged.
    RequestBodyInexact bool
}

type APIAttemptResult struct {
    // Existing fields remain unchanged.
    ResponseBodyInexact bool
}

type EncodedBody struct {
    // Existing fields remain unchanged.
    Exact bool `json:"exact"`
}

type Record struct {
    // Existing fields remain unchanged.
    CredentialValuesExcluded bool `json:"credential_values_excluded"`
}
```

The zero values on `RequestBodyInexact` and `ResponseBodyInexact` preserve current exact callers. The API-log conversion must set `EncodedBody.Exact` to `!RequestBodyInexact` and `!ResponseBodyInexact`, respectively. `CredentialValuesExcluded` remains a separate statement about credential handling; body inexactness must not change it.

If the prerequisite lands with different field names or reversed Boolean semantics, stop and reconcile the interface with Jesse before implementing this plan. Do not add aliases.

## Dependency and landing order

```text
body-evidence core prerequisite
            |
            +--> 1 per-wire lifecycle --> 2 bounded body capture --+
            |                                                     |
            +--> 3 transparent ownership --> 4 gzip race safety --+--> 5 protocol predicates
                                                                  |
                         +----------------------------------------+
                         |
                         +--> 6 OpenAI timeout/fallback
                         +--> 7 OpenAI-compatible DONE cancel
                         +--> 8 four model listers
                         +--> 9 Hub credential headers
                         +--> 10 endpoint provenance
                         +--> 11 Anthropic test cleanup
                                      |
                                      +--> 12 full integration gates
```

Task 2's drain and completeness semantics must land before Tasks 6 and 8. Task 3's transparent-transport contract must land before Tasks 4 and 5. Harness documentation follows this plan in a later change; it is not part of these commits.

## File map

| Concern | Production files | Primary tests |
|---|---|---|
| Wire cycles and body capture | `llm/providers/internal/transport/http_attempts.go`, `llm/providers/internal/transport/request_metadata.go` | `llm/providers/internal/transport/http_attempts_test.go`, `llm/providers/internal/transport/wire_fidelity_test.go` |
| Transport ownership and gzip | `llm/providers/internal/transport/response_compression.go`, `llm/adapter_timeout.go` | `llm/providers/internal/transport/http_attempts_test.go`, `llm/providers/internal/transport/wire_fidelity_test.go`, `llm/adapter_timeout_test.go` |
| OpenAI fallback | `llm/providers/openai/adapter.go`, `llm/providers/openai/responses.go` | `llm/providers/openai/adapter_test.go`, `llm/providers/openai/wire_capture_test.go`, `llm/providers/openai/response_header_timeout_test.go` |
| OpenAI-compatible streaming | `llm/providers/openaicompat/adapter.go` | `llm/providers/openaicompat/adapter_test.go`, `llm/providers/openaicompat/wire_capture_test.go` |
| Model listing | each provider's `models.go` | each provider's `models_fuzz_test.go` and `adapter_test.go` |
| Hub credentials | `cmd/serf-hub/spawn.go` | `cmd/serf-hub/spawn_test.go` |
| Endpoint provenance | `llm/apilog.go`, core adapter files, `agent/session_model_call.go` | adapter wire-capture tests, `agent/session_model_test.go`, `agent/atif_test.go` |
| Anthropic ordering tests | none | `llm/providers/anthropic/wire_capture_test.go` |

---

### Task 1: Record one API attempt per standard-transport wire cycle

**Files:**

- Modify: `llm/providers/internal/transport/http_attempts.go`
- Modify: `llm/providers/internal/transport/request_metadata.go`
- Test: `llm/providers/internal/transport/http_attempts_test.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`

**Fresh roles:** Assign a new implementer for this task, then a different new reviewer before commit.

**Step 1: Write a deterministic transparent-retry regression test**

Add a local raw-TCP test that exercises the real standard transport rather than calling an internal lifecycle method directly:

1. Start a loopback `net.Listener` and a `http.Transport` with one idle connection available.
2. Prime the connection with a successful request.
3. Send an idempotent, rewindable POST carrying an `Idempotency-Key`, a known body, and a `GetBody` replay function.
4. On the reused connection, read the complete second request and close the connection before writing a response.
5. Accept the retry on a new connection, read the complete replayed request, and return success.
6. Record a server-side event immediately before accepting/reading the replay.

Assert all of these public effects:

- The server received the same request body twice.
- Two API-attempt records were appended with attempt indices 1 and 2.
- Attempt 1 is a transport failure; attempt 2 is success.
- The sink's append event for attempt 1 occurs before the server observes the first byte of attempt 2.
- The method, sanitized endpoint, and captured request bytes belong to the corresponding wire cycle.
- Both request bodies are exact once the binding core fields are converted to API-log bodies.

Use channels and an event journal for ordering. Do not use `time.Sleep` or a negative timeout as proof of ordering.

Also add a local listener test where connection establishment fails before request bytes can be written. Specify and assert the representation:

```text
method:                 sanitized request method
endpoint:               sanitized requested endpoint
request headers:        no wire-written headers
request body:           exact empty bytes
outcome:                transport_failure
```

Run the focused tests and confirm the retry assertion fails because the current outer `RoundTrip` produces one attempt:

```bash
go test ./llm/providers/internal/transport -run 'TestDoWithAPIAttempts_(TransparentRetry|PreByteConnectionFailure)' -count=1
```

Expected: FAIL with one record instead of two, or with the first record appearing after the second wire write.

**Step 2: Add a per-wire lifecycle driven by `httptrace`**

Replace the single outer-attempt state with a lifecycle that owns one active wire cycle at a time. Keep the state private to the transport package:

```go
type wireAttemptLifecycle struct {
    mu      sync.Mutex
    next    int
    active  *wireAttemptCycle
    append  func(APIAttemptMeta, APIAttemptResult)
}

type wireAttemptCycle struct {
    index   int
    attempt APIAttemptMeta
    // Per-cycle request headers, request bytes, gzip ownership, and write state.
}
```

Install a fresh composed `httptrace.ClientTrace` for the request passed to the standard transport. Preserve and call any trace callbacks already present on the request context.

Lifecycle rules:

- `GetConn` begins a new candidate cycle for the HTTP/1.1 path.
- If `GetConn` arrives while an earlier cycle has reached `GotConn` or any write callback, synchronously finalize and append the prior cycle as a transparent internal transport failure before returning from `GetConn`. Because the next connection acquisition and write cannot proceed until the callback returns, this enforces append-before-next-write.
- `GotConn` binds the connection and negotiated protocol to the candidate cycle. If `GotConn` arrives for a cycle that already reached `GotConn`, first finalize and append that earlier cycle, then begin the new cycle inside the callback. This second boundary is required for HTTP/2's internal retry loop, which can emit repeated `GotConn` callbacks without a new outer `GetConn` callback.
- `WroteHeaderField`, `WroteHeaders`, or `WroteRequest` without an active candidate begins one defensively before recording the callback. This preserves one truthful cycle if a future standard-transport path omits an earlier trace callback.
- Per-cycle request-header, request-body, standard-gzip, and write-result state is reset when the new cycle begins.
- `WroteHeaderField`, `WroteHeaders`, and `WroteRequest` update only the active cycle.
- `WroteRequestInfo.Err == nil` proves the standard transport completed the request write for that cycle. A non-nil write error marks request evidence inexact.
- A final `RoundTrip` error finalizes the active cycle with that error.
- A final response leaves the active cycle attached to `APIAttemptCapture` so the adapter's later `Complete` call supplies semantic outcome and body consumption state.
- A failed cycle with no `GotConn`, no header callbacks, and no `WroteRequest` callback is the defined pre-byte failure above.

Wrap both the initial `Request.Body` and `Request.GetBody` results so every standard-transport replay feeds bytes to the active cycle rather than sharing one outer recorder.

Do not change Go's retry eligibility or add Serf retry policy. This task only observes retries already performed by `*http.Transport`.

**Step 3: Preserve one-attempt behavior for nonstandard transports**

When a round trip does not produce standard-transport lifecycle callbacks, synthesize exactly one outer cycle so existing custom transports still produce one record. Body exactness for opaque transports is tightened in Task 3; do not invent multiple cycles for a custom transport.

**Step 4: Run focused and package tests**

```bash
go test ./llm/providers/internal/transport -run 'TestDoWithAPIAttempts_(TransparentRetry|PreByteConnectionFailure)' -count=1
go test ./llm/providers/internal/transport -count=1
```

Expected: PASS, with event-journal assertions proving append order.

**Step 5: Fresh review and commit**

The reviewer must check that the code observes the standard transport rather than duplicating retry policy, that prior append is synchronous in `GetConn`, and that pre-byte failures cannot inherit state from another cycle.

```bash
git status --short
git add llm/providers/internal/transport/http_attempts.go \
  llm/providers/internal/transport/request_metadata.go \
  llm/providers/internal/transport/http_attempts_test.go \
  llm/providers/internal/transport/wire_fidelity_test.go
git commit -m "fix: record transparent transport wire attempts

Track standard-transport retries as separate API-attempt records using a
per-wire httptrace lifecycle. Reset request evidence for each cycle, append a
failed cycle before the next wire write, and define exact empty evidence for
pre-byte connection failures without changing retry policy."
```

---

### Task 2: Remove unbounded drains and report response completeness truthfully

**Files:**

- Modify: `llm/providers/internal/transport/http_attempts.go`
- Modify: `llm/providers/internal/transport/request_metadata.go`
- Test: `llm/providers/internal/transport/http_attempts_test.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Write blocking-body tests that expose instrumentation hangs**

Add channel-controlled `io.ReadCloser` test doubles for these cases:

- A redirect response returns a readable prefix and then blocks forever unless closed.
- A successful response yields a complete decodable JSON value and then blocks instead of returning EOF.
- A response yields a prefix and a sentinel read error.
- A request body remains open in a custom asynchronous transport after `RoundTrip` returns.

For each case, invoke the real capture completion path and require completion after the test releases only the operation that application code legitimately performed. Use an explicit completion channel with a one-second failure deadline solely as a deadlock guard; do not use elapsed duration as the behavior assertion.

Assert:

- Capture completion never issues an extra read to reach EOF.
- Capture completion calls `Close` where ownership requires it, but does not wait for blocked reads to finish.
- `RawResponseBody` is exactly the bytes observed before close.
- `ResponseBodyInexact` is true unless EOF or raw completeness is proven.
- A sentinel read error leaves the observed prefix in the record and marks it inexact.
- An unfinished asynchronous request body is snapshotted without waiting and marks `RequestBodyInexact` true.

Run and confirm current code blocks in `responseDrain`, redirect `Close`, admitted-read waiting, or request-recorder readiness:

```bash
go test ./llm/providers/internal/transport -run 'TestAPIAttemptCapture_(DoesNotDrain|SnapshotsBlocking|PreservesReadErrorPrefix)' -count=1 -timeout=5s
```

Expected: FAIL or test timeout before the production correction.

**Step 2: Define the response evidence state machine**

Track only transport-observed bytes and terminal facts:

```go
type responseBodyEvidence struct {
    mu            sync.Mutex
    raw           bytes.Buffer
    sawEOF        bool
    readErr       error
    contentLength int64
    opaque        bool
}
```

The actual field arrangement may follow the surrounding style, but the rules are exact:

- Append bytes returned by the wrapped response body before returning them to the caller.
- Mark `sawEOF` only when the underlying body returns `io.EOF`.
- Capture non-EOF read errors without replacing bytes already observed.
- `Close` never calls `Read` or `io.Copy`.
- `APIAttemptCapture.Complete` never calls `Read`, `io.Copy`, or waits for a read/body-close channel.
- The canonical response body is the bytes observed before close or completion.
- Evidence is exact only when the transport is known raw and either:
  - EOF was observed; or
  - `ContentLength >= 0` and the captured byte count equals `ContentLength`, including a known zero-length body.
- A non-EOF read error, unknown content length without EOF, active blocked read, or opaque transform makes `ResponseBodyInexact` true.

Snapshot the recorder under its state lock, then release the lock before calling the underlying `Close`. Never hold an evidence mutex across underlying `Read` or `Close`.

**Step 3: Remove all instrumentation drains**

Delete both drain paths rather than bounding them:

- Remove the decode-failure `responseDrain`/`io.Copy` from `APIAttemptCapture.Complete`.
- Remove the redirect/unclaimed-response `io.Copy` from `apiAttemptResponseBody.Close`.

The standard client's own redirect handling may perform its normal bounded slurp. Serf instrumentation must not add another drain.

For request evidence, replace readiness waits with a lock-protected snapshot. Exactness comes from the Task 1 per-cycle write result; an incomplete, errored, active, or opaque write is inexact.

**Step 4: Run focused tests and the race detector**

```bash
go test ./llm/providers/internal/transport -run 'TestAPIAttemptCapture_(DoesNotDrain|SnapshotsBlocking|PreservesReadErrorPrefix)' -count=1 -timeout=5s
go test -race ./llm/providers/internal/transport -count=1
```

Expected: PASS, with no goroutine leak or race report.

**Step 5: Fresh review and commit**

The reviewer must search for remaining completion-path drains and waits:

```bash
rg -n 'responseDrain|io\.Copy|<-.*ready|Wait\(' llm/providers/internal/transport
```

Any remaining match must be justified as application behavior outside instrumentation completion; otherwise remove it.

```bash
git status --short
git add llm/providers/internal/transport/http_attempts.go \
  llm/providers/internal/transport/request_metadata.go \
  llm/providers/internal/transport/http_attempts_test.go \
  llm/providers/internal/transport/wire_fidelity_test.go
git commit -m "fix: bound API attempt body evidence

Stop transport instrumentation from draining response bodies or waiting for
application-owned I/O. Snapshot only bytes actually observed, preserve read
errors, and mark request or response bodies inexact unless completion can be
proved from EOF, content length, or the per-wire write result."
```

---

### Task 3: Replace implicit compression ownership with an explicit transparent unwrap contract

**Files:**

- Modify: `llm/providers/internal/transport/response_compression.go`
- Modify: `llm/providers/internal/transport/http_attempts.go`
- Modify: `llm/adapter_timeout.go`
- Test: `llm/providers/internal/transport/http_attempts_test.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`
- Test: `llm/adapter_timeout_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Write transparent and opaque decorator tests**

Create two wrappers around a standard transport:

```go
type declaredTransparentTransport struct {
    base http.RoundTripper
}

func (t *declaredTransparentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    return t.base.RoundTrip(req)
}

func (t *declaredTransparentTransport) APIAttemptUnderlyingTransport() http.RoundTripper {
    return t.base
}

type opaqueTransformTransport struct {
    base http.RoundTripper
}
```

The opaque wrapper must transform or decompress the response body before returning it. Assert:

- Direct `*http.Transport` body evidence can be exact when byte completeness is proven.
- The declared-transparent wrapper recursively reaches the same standard transport and retains raw exactness.
- The opaque wrapper's request and response evidence is inexact even if its returned body reaches EOF.
- Unknown wrappers do not receive Serf-owned standard gzip injection or decompression.

Run and confirm the opaque assertion fails under the old Boolean ownership convention:

```bash
go test ./llm/providers/internal/transport -run 'TestDoWithAPIAttempts_(TransparentTransport|OpaqueTransform)' -count=1
```

Expected: FAIL because transformed bytes are currently treated as raw exact evidence or compression ownership is inferred without an unwrap proof.

**Step 2: Introduce one capability and remove the old one**

Use this exact private interface and method name:

```go
type transparentRoundTripper interface {
    APIAttemptUnderlyingTransport() http.RoundTripper
}
```

Implement a cycle-safe recursive resolver in the transport package:

```go
func standardTransport(rt http.RoundTripper) (*http.Transport, bool)
```

It returns true only when the chain consists of zero or more declared-transparent wrappers ending in `*http.Transport`. Detect nil and wrapper cycles and return false.

Implement the method on `llm.responseHeaderTimeoutTransport`:

```go
func (t *responseHeaderTimeoutTransport) APIAttemptUnderlyingTransport() http.RoundTripper {
    return t.base
}
```

Delete `standardCompressionOwner`, `APILogTransportUsesStandardCompression`, and every implementation or test of that old method. Do not preserve a compatibility alias.

The capability has two linked meanings: the wrapper promises not to transform wire bodies, and it exposes the next transport so Serf can prove the chain terminates in the standard transport. A wrapper that changes request or response bytes must not implement it.

**Step 3: Make opaque evidence conservative**

For a transport chain that cannot be proven transparent:

- Keep one API-attempt record for the outer round trip.
- Capture available diagnostic bytes.
- Set `RequestBodyInexact` and `ResponseBodyInexact` true.
- Do not claim standard raw-gzip ownership.
- Do not manually decode a returned `Content-Encoding: gzip` body as wire evidence.

Do not infer transparency from concrete wrapper names, embedded fields, reflection, or behavior observed after the fact.

**Step 4: Run focused tests and package tests**

```bash
go test ./llm/providers/internal/transport -run 'TestDoWithAPIAttempts_(TransparentTransport|OpaqueTransform)' -count=1
go test ./llm -run 'TestResponseHeaderTimeoutTransport' -count=1
go test ./llm/providers/internal/transport ./llm -count=1
```

Expected: PASS.

**Step 5: Fresh review and commit**

The reviewer must verify there is exactly one ownership capability and no compatibility alias:

```bash
rg -n 'APILogTransportUsesStandardCompression|standardCompressionOwner|APIAttemptUnderlyingTransport' llm
```

Expected: no old-name matches; new-name matches only the private interface, resolver use, timeout wrapper, and focused test doubles.

```bash
git status --short
git add llm/providers/internal/transport/response_compression.go \
  llm/providers/internal/transport/http_attempts.go \
  llm/providers/internal/transport/http_attempts_test.go \
  llm/providers/internal/transport/wire_fidelity_test.go \
  llm/adapter_timeout.go llm/adapter_timeout_test.go
git commit -m "fix: require transparent transport ownership proof

Replace the compression-owner hint with one recursive transparent-transport
unwrap contract. Preserve exact raw evidence only for chains proven to end in
the standard transport, and conservatively mark transformed or unknown
decorator evidence inexact without adding compatibility aliases."
```

---

### Task 4: Make the standard gzip response body safe under concurrent Read and Close

**Files:**

- Modify: `llm/providers/internal/transport/response_compression.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add deterministic sequential and concurrent oracles**

Add table-driven sequential tests for the Go 1.25 behavior Serf mirrors:

| Protocol mode | Read after close must return |
|---|---|
| HTTP/1.1 | `http: read on closed response body` |
| HTTP/2 | `fs.ErrClosed` |

Add a channel-gated compressed body for the concurrent case:

1. Start `Read` and block it inside the underlying body.
2. Wait for a channel proving the blocking read began.
3. Call `Close` concurrently and require the underlying close to occur without waiting for the read gate.
4. Release the read gate.
5. Assert the final read/close states match the selected protocol and no panic occurs.

Run under the race detector and confirm the existing HTTP/2 `zerr`/`zr` fields race:

```bash
go test -race ./llm/providers/internal/transport -run 'TestStandardGzipResponseBody_(ReadAfterClose|ConcurrentReadClose)' -count=1
```

Expected: FAIL with a race report or incorrect close behavior.

**Step 2: Add a state lock without holding it across blocking I/O**

Protect gzip initialization, terminal error, closed state, and active-reader bookkeeping with a mutex. The implementation must follow this shape:

1. Lock and inspect state.
2. Mark initialization/read ownership as active.
3. Unlock before `gzip.NewReader`, `zr.Read`, or the underlying body's `Close`.
4. Re-lock only to publish the resulting reader, error, or closed state.
5. If `Close` won while initialization/read was in flight, retain the protocol-specific closed terminal state rather than overwriting it with a later result.

`Close` must set the protocol-specific sentinel while holding the state lock, release the lock, then close the underlying body. It must not wait for a blocked read to return.

Do not serialize all calls by holding one mutex across `Read`; that would remove the data race by introducing a Close hang.

Use the Go 1.25 standard library as the behavioral oracle:

- HTTP/1.1: `net/http/transport.go` gzip reader close checks.
- HTTP/2: `net/http/h2_bundle.go` gzip reader close sentinel.

**Step 3: Run focused and package race tests**

```bash
go test -race ./llm/providers/internal/transport -run 'TestStandardGzipResponseBody_(ReadAfterClose|ConcurrentReadClose)' -count=1
go test -race ./llm/providers/internal/transport -count=1
```

Expected: PASS with no race report and no blocked `Close`.

**Step 4: Fresh review and commit**

The reviewer must trace every blocking call and confirm no state mutex is held over it.

```bash
git status --short
git add llm/providers/internal/transport/response_compression.go \
  llm/providers/internal/transport/wire_fidelity_test.go
git commit -m "fix: synchronize gzip response close state

Protect the standard gzip wrapper's initialization and terminal state across
concurrent Read and Close without holding a mutex over blocking I/O. Retain the
distinct Go 1.25 HTTP/1.1 and HTTP/2 read-after-close behavior."
```

---

### Task 5: Match Go 1.25 explicit-empty gzip predicates by negotiated protocol

**Files:**

- Modify: `llm/providers/internal/transport/http_attempts.go`
- Modify: `llm/providers/internal/transport/request_metadata.go`
- Modify: `llm/providers/internal/transport/response_compression.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add HTTP/1.1 and HTTP/2 differential tests**

Run the same request-header cases against local HTTP/1.1 and HTTP/2 servers through a real `*http.Transport`:

| Request state | HTTP/1.1 Go 1.25 predicate | HTTP/2 Go 1.25 predicate |
|---|---|---|
| no `Accept-Encoding`, no `Range` | inject gzip | inject gzip |
| `Accept-Encoding: ""` entry present | inject gzip | suppress gzip |
| `Range: ""` entry present | inject gzip | suppress gzip |
| nonempty `Accept-Encoding` | suppress gzip | suppress gzip |
| nonempty `Range` | suppress gzip | suppress gzip |

For every case, assert both what the server receives and whether Serf decodes the final gzip response while retaining compressed raw response evidence. These are behavior tests, not assertions over a generated command or script.

Use an `httptest.Server` for HTTP/1.1 and `httptest.NewUnstartedServer` with HTTP/2 enabled for HTTP/2. The server must be local and deterministic.

```bash
go test ./llm/providers/internal/transport -run 'TestStandardCompression_ExplicitEmptyHeadersByProtocol' -count=1
```

Expected: FAIL because the current pre-`RoundTrip` header predicate cannot distinguish the HTTP/1.1 `Header.Get` rule from the HTTP/2 map-entry-length rule.

**Step 2: Select the predicate inside the per-wire connection cycle**

Determine protocol in the Task 1 cycle's `GotConn` callback. For TLS connections, use the negotiated ALPN protocol; treat `h2` as HTTP/2 and other supported standard-transport connections as HTTP/1.1 behavior.

Apply these exact predicates:

```go
func shouldOwnHTTP1Gzip(h http.Header) bool {
    return h.Get("Accept-Encoding") == "" && h.Get("Range") == ""
}

func shouldOwnHTTP2Gzip(h http.Header) bool {
    return len(h["Accept-Encoding"]) == 0 && len(h["Range"]) == 0
}
```

When the selected predicate is true, set `Accept-Encoding: gzip` before the standard transport writes request headers and mark only that active wire cycle as owning gzip decoding. When false, leave the caller's header presence and values untouched.

Do not move this decision back before `RoundTrip`: the negotiated protocol is required to preserve the explicit-empty difference. Do not normalize explicit-empty headers.

**Step 3: Verify protocol and replay behavior**

Extend the Task 1 transparent-retry test with a gzip-eligible case and assert each cycle resets and recomputes its compression state. A prior failed cycle must not leak ownership into the successful cycle.

```bash
go test ./llm/providers/internal/transport -run 'TestStandardCompression_|TestDoWithAPIAttempts_TransparentRetry' -count=1
go test -race ./llm/providers/internal/transport -count=1
```

Expected: PASS.

**Step 4: Fresh review and commit**

The reviewer must compare the two predicates against the Go 1.25 standard-library sources and verify the test covers header absence separately from a present empty slice value.

```bash
git status --short
git add llm/providers/internal/transport/http_attempts.go \
  llm/providers/internal/transport/request_metadata.go \
  llm/providers/internal/transport/response_compression.go \
  llm/providers/internal/transport/wire_fidelity_test.go
git commit -m "fix: preserve protocol gzip header semantics

Choose Serf-owned standard gzip handling after connection protocol negotiation.
Mirror Go 1.25's HTTP/1.1 Header.Get predicate and HTTP/2 header-entry
predicate, including their intentional difference for explicit-empty headers."
```

---

### Task 6: Preserve the original OpenAI parent context and surface pre-content SSE timeouts

**Files:**

- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/responses.go`
- Test: `llm/providers/openai/adapter_test.go`
- Test: `llm/providers/openai/wire_capture_test.go`
- Test: `llm/providers/openai/response_header_timeout_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add two scripted-provider regressions**

Add a Responses-to-Chat fallback test where the Responses request returns the existing fallback-eligible 404 or 422 only after its derived attempt context is canceled. The Chat request must still receive a live context derived from the original caller context.

Add a streaming test where the Responses endpoint opens SSE, emits no content, and triggers `llm.ErrSSEReadTimeout`. Assert:

- The terminal stream error satisfies `errors.Is(err, llm.ErrSSEReadTimeout)`.
- No Chat Completions fallback request is issued.
- The error is not rewritten as the empty-Responses-stream sentinel.

These tests must use local scripted HTTP handlers or scripted providers. They must not call OpenAI.

```bash
go test ./llm/providers/openai -run 'TestAdapter_(ResponsesFallbackUsesParentContext|ResponsesTimeoutBeforeContent)' -count=1
```

Expected: FAIL because fallback currently receives the canceled derived context and timeout-before-content currently takes the empty-stream path.

**Step 2: Preserve the parent context for Chat fallback**

At the beginning of `Adapter.Complete`, retain the caller's context before deriving the Responses timeout context:

```go
parentCtx := ctx
```

Pass `parentCtx`, not the derived Responses context, to `completeViaChatCompletionsFallback`. Let the Chat path derive its own existing timeout from that parent. Do not change which statuses or provider errors are fallback eligible.

Apply the same ownership principle only where the current streaming flow derives a Responses-only context before invoking its existing Chat fallback. Do not introduce a new fallback.

**Step 3: Give the SSE timeout precedence over empty-stream fallback**

In `decodeResponsesStream`, after the SSE parser returns and before checking whether content was emitted, handle:

```go
if errors.Is(parseErr, llm.ErrSSEReadTimeout) {
    // Emit/return the timeout through the existing stream error path.
}
```

Retain the existing context-cancellation precedence. Only a genuinely complete, non-timeout stream with no content may take the existing empty-stream fallback path.

Do not change response-vs-Chat preference, status eligibility, or fallback policy for other errors.

**Step 4: Run focused and package tests**

```bash
go test ./llm/providers/openai -run 'TestAdapter_(ResponsesFallbackUsesParentContext|ResponsesTimeoutBeforeContent)' -count=1
go test ./llm/providers/openai -count=1
```

Expected: PASS.

**Step 5: Fresh review and commit**

The reviewer must compare fallback call sites before and after the change and confirm only context ownership and timeout classification changed.

```bash
git status --short
git add llm/providers/openai/adapter.go llm/providers/openai/responses.go \
  llm/providers/openai/adapter_test.go \
  llm/providers/openai/wire_capture_test.go \
  llm/providers/openai/response_header_timeout_test.go
git commit -m "fix: preserve OpenAI fallback context ownership

Start Chat fallback from the original model-call context instead of a canceled
Responses attempt context. Surface an SSE read timeout before any content as a
timeout rather than misclassifying it as an empty stream and falling back."
```

---

### Task 7: Cancel OpenAI-compatible streams immediately at DONE

**Files:**

- Modify: `llm/providers/openaicompat/adapter.go`
- Test: `llm/providers/openaicompat/adapter_test.go`
- Test: `llm/providers/openaicompat/wire_capture_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add a DONE lifecycle regression**

Use a scripted SSE server that emits `[DONE]`, flushes it, and then keeps the connection open until its request context is canceled. Consume the public stream to its terminal event and assert the server observes cancellation after DONE. The test must use a channel from `r.Context().Done()`; a sleep is not an acceptable cancellation oracle.

Also assert the consumer receives the existing successful terminal behavior rather than a context error.

```bash
go test ./llm/providers/openaicompat -run 'TestAdapter_StreamCancelsRequestAtDONE' -count=1
```

Expected: FAIL because the current DONE branch sets `finished` but leaves the request context live.

**Step 2: Cancel in the DONE branch**

In the existing `[DONE]` branch, set the current completion state and call the existing request cancel function immediately:

```go
finished = true
cancel()
```

Keep the state update before cancellation so the runner suppresses the expected context error for a successfully finished stream. Do not change parsing, terminal event selection, or fallback behavior.

**Step 3: Run focused and package tests**

```bash
go test ./llm/providers/openaicompat -run 'TestAdapter_StreamCancelsRequestAtDONE' -count=1
go test ./llm/providers/openaicompat -count=1
```

Expected: PASS.

**Step 4: Fresh review and commit**

```bash
git status --short
git add llm/providers/openaicompat/adapter.go \
  llm/providers/openaicompat/adapter_test.go \
  llm/providers/openaicompat/wire_capture_test.go
git commit -m "fix: cancel compatible streams at DONE

Cancel the OpenAI-compatible request as soon as the DONE marker establishes a
successful terminal stream. Preserve the finished state first so expected
context cancellation is not surfaced as a model error."
```

---

### Task 8: Remove post-decode model-list drains and preserve read-error precedence

**Files:**

- Modify: `llm/providers/openai/models.go`
- Modify: `llm/providers/openaicompat/models.go`
- Modify: `llm/providers/anthropic/models.go`
- Modify: `llm/providers/google/models.go`
- Test: `llm/providers/openai/models_fuzz_test.go`
- Test: `llm/providers/openai/adapter_test.go`
- Test: `llm/providers/openaicompat/models_fuzz_test.go`
- Test: `llm/providers/openaicompat/adapter_test.go`
- Test: `llm/providers/anthropic/models_fuzz_test.go`
- Test: `llm/providers/anthropic/adapter_test.go`
- Test: `llm/providers/google/models_fuzz_test.go`
- Test: `llm/providers/google/adapter_test.go`

**Fresh roles:** Assign one fresh implementer for the cross-provider consistency change and a different fresh reviewer who checks all four packages.

**Step 1: Add the same behavioral matrix to all four providers**

Use provider-local scripted `ReadCloser` test doubles to cover:

| Status/body behavior | Returned error | Raw evidence |
|---|---|---|
| non-OK, full body, EOF | provider status error | complete bytes, exact |
| non-OK, prefix then sentinel read error | sentinel read error | prefix, inexact |
| OK, valid JSON then body blocks | successful decoded model list | decoded bytes observed so far, inexact |
| OK, JSON prefix then sentinel read error | sentinel read error | prefix, inexact |
| OK, complete invalid JSON then EOF | decode error | complete bytes, exact |

The blocking-valid-JSON case must prove the model-list call returns after decode and close without waiting for EOF. Use a channel-gated body and a deadlock deadline, not elapsed time.

Assert raw body truth through the API-attempt sink and the binding `apilog.EncodedBody.Exact` field. The provider must not reconstruct a complete body from semantic data.

Run one focused command covering the new test name in all four packages:

```bash
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google \
  -run 'TestListModels_(ReadErrorPrecedence|DoesNotDrainAfterDecode)' -count=1 -timeout=10s
```

Expected: FAIL because non-OK paths discard `io.ReadAll` errors and OK paths drain after decoding.

**Step 2: Implement the same error precedence in every model lister**

For non-OK responses:

1. Read the body once through the existing transport wrapper.
2. If that read returns an error, return the read error.
3. Otherwise return the existing provider status error constructed from the completed body.

For OK responses:

1. Decode one JSON response using the existing decoder.
2. Do not call `io.Copy`, `io.ReadAll`, or another read after a successful decode.
3. If decode returns an underlying read error, return it.
4. Otherwise return the existing decode error or decoded model list.
5. Close through the existing ownership path without trying to establish EOF.

The transport capture from Task 2 is the sole owner of raw response evidence. Provider completion supplies semantic status/error only; it must not overwrite captured bytes or claim exactness. A successful decode followed by an unread tail is therefore useful but inexact evidence.

Keep provider-specific status-error types and message shaping unchanged except where a body read error now correctly has precedence.

**Step 3: Run all model-list tests and fuzz seed corpora**

```bash
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google \
  -run 'TestListModels_|Fuzz' -count=1 -timeout=20s
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google -count=1
```

Expected: PASS. The first command runs registered fuzz seed cases as ordinary tests; it does not start an unbounded fuzzing session.

**Step 4: Fresh review and commit**

The reviewer must compare all four implementations side-by-side, verify the same precedence, and confirm no model-list completion path drains after a successful decode:

```bash
rg -n 'io\.Copy|io\.ReadAll|json\.NewDecoder' \
  llm/providers/{openai,openaicompat,anthropic,google}/models.go
```

`io.ReadAll` may remain only for the deliberate non-OK full-body read whose returned error is checked.

```bash
git status --short
git add llm/providers/openai/models.go \
  llm/providers/openai/models_fuzz_test.go \
  llm/providers/openai/adapter_test.go \
  llm/providers/openaicompat/models.go \
  llm/providers/openaicompat/models_fuzz_test.go \
  llm/providers/openaicompat/adapter_test.go \
  llm/providers/anthropic/models.go \
  llm/providers/anthropic/models_fuzz_test.go \
  llm/providers/anthropic/adapter_test.go \
  llm/providers/google/models.go \
  llm/providers/google/models_fuzz_test.go \
  llm/providers/google/adapter_test.go
git commit -m "fix: preserve model-list body read failures

Stop all four model listers from draining successful JSON responses after
decode. Return observed body read failures ahead of status or decode errors and
leave complete versus inexact raw evidence to the shared transport capture."
```

---

### Task 9: Accept nonempty Hub credential headers without trusting ordinary headers

**Files:**

- Modify: `cmd/serf-hub/spawn.go`
- Test: `cmd/serf-hub/spawn_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add credential-source table tests**

Extend `validateProviderCredentials` tests with these exact cases:

| Configuration | Result |
|---|---|
| `CredentialHeaders["Authorization"] = "Bearer secret"` | accept |
| `CredentialHeaders["X-API-Key"] = "secret"` | accept |
| credential header exists but all values are empty/whitespace | reject |
| only `Headers["Authorization"] = "Bearer secret"` | reject |

Retain existing cases for API key, OAuth, configured environment credentials, and credential-free endpoints.

```bash
go test ./cmd/serf-hub -run 'TestValidateProviderCredentials' -count=1
```

Expected: FAIL because `CredentialHeaders` is currently ignored.

**Step 2: Recognize only declared credential headers**

Add one small helper in `spawn.go` that returns true when any value in `inst.CredentialHeaders` is nonempty after `strings.TrimSpace`. Call it from the existing credential-source validation.

Do not inspect `inst.Headers` for `Authorization` or any other secret-looking name. Ordinary headers are not a declared credential source and must not bypass validation.

Do not change command-line credential parsing or unrelated `cmdutil` behavior.

**Step 3: Run focused and Hub tests**

```bash
go test ./cmd/serf-hub -run 'TestValidateProviderCredentials' -count=1
go test ./cmd/serf-hub -count=1
```

Expected: PASS.

**Step 4: Fresh review and commit**

```bash
git status --short
git add cmd/serf-hub/spawn.go cmd/serf-hub/spawn_test.go
git commit -m "fix: recognize Hub credential headers

Treat a nonempty declared CredentialHeaders value as a valid provider
credential source. Continue rejecting ordinary Headers.Authorization so
nonsecret request configuration cannot bypass credential validation."
```

---

### Task 10: Stamp final response endpoints and sanitize again before durable persistence

**Files:**

- Modify: `llm/apilog.go`
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/chatcompletions.go`
- Modify: `llm/providers/openai/responses.go`
- Modify: `llm/providers/openaicompat/adapter.go`
- Modify: `llm/providers/anthropic/adapter.go`
- Modify: `llm/providers/google/adapter.go`
- Modify: `agent/session_model_call.go`
- Test: `llm/apilog_append_test.go`
- Test: `llm/providers/openai/wire_capture_test.go`
- Test: `llm/providers/openaicompat/wire_capture_test.go`
- Test: `llm/providers/anthropic/wire_capture_test.go`
- Test: `llm/providers/google/wire_capture_test.go`
- Test: `agent/session_model_test.go`
- Test: `agent/atif_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer.

**Step 1: Add final-redirect endpoint tests at the core adapters**

For each core adapter, route the request through a local redirect:

```text
/start?request_secret=SENTINEL
    -> /final?response_secret=SENTINEL
```

Return the provider's smallest valid complete or streaming response at `/final`. Assert the semantic model response contains the final scheme/host/path derived from `resp.Request.URL`, with no userinfo, query, or fragment. The assertion must fail if the adapter stamps only the originally constructed URL.

Where complete and streaming code have separate endpoint-stamping paths, cover both. Shared code can use one table test only when it executes both public paths.

```bash
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google \
  -run 'TestAdapter_.*FinalResponseEndpoint' -count=1
```

Expected: FAIL with `/start` instead of `/final` or with a query present.

**Step 2: Add a malicious custom-adapter durable-boundary test**

Create a scripted custom adapter that returns:

```go
Raw: map[string]any{
    "endpoint_url": "https://user:SENTINEL@example.test/final?token=SENTINEL#fragment",
}
```

Run the real agent model-call persistence path. Assert the durable transcript response metadata and ATIF export contain only:

```text
https://example.test/final
```

and contain no sentinel, userinfo, query, or fragment.

```bash
go test ./agent -run 'Test.*SanitizesAdapterEndpointAtDurableBoundary' -count=1
```

Expected: FAIL because the current durable boundary trusts adapter-provided raw metadata.

**Step 3: Centralize endpoint sanitization and final-response selection**

Add these exact exported helpers in `llm/apilog.go`:

```go
func SanitizeEndpointURL(endpoint string) string

func FinalResponseEndpointURL(resp *http.Response, fallback string) string
```

`SanitizeEndpointURL` must:

- parse the URL;
- require a supported HTTP(S) scheme and nonempty host;
- clear userinfo, raw query, forced-query state, and fragment;
- preserve scheme, host, escaped path, and an explicit nondefault port;
- return an empty string for invalid or unsafe input.

`FinalResponseEndpointURL` must prefer `resp.Request.URL.String()` when `resp`, `resp.Request`, and its URL are present, then sanitize it. It may sanitize and return `fallback` only for synthetic/test responses that lack a final request URL.

Keep `StampEndpointURL` as the single function that writes `Raw["endpoint_url"]`; make it call `SanitizeEndpointURL` so existing callers also receive the corrected safety behavior. This is not a compatibility alias: it remains the existing semantic stamping operation.

**Step 4: Stamp the final URL in every core adapter path**

Replace constructed-request endpoint stamping with:

```go
llm.StampEndpointURL(modelResp, llm.FinalResponseEndpointURL(resp, constructedEndpoint))
```

Apply it to:

- OpenAI Responses complete and stream.
- OpenAI Chat Completions complete and stream.
- OpenAI-compatible Chat complete and stream.
- Anthropic complete and stream.
- Google complete and stream.

The final URL belongs to semantic response provenance. API-attempt records continue to describe each wire request independently.

**Step 5: Sanitize again at the durable agent boundary**

In `completeAttemptMetadata`, treat any adapter's `Raw["endpoint_url"]` as untrusted input. Pass it through `llm.SanitizeEndpointURL` before writing transcript metadata. If sanitization returns empty, omit the endpoint rather than persisting the original.

This second boundary is required even though core adapters sanitize: custom adapters are allowed to return arbitrary raw metadata, and durable transcript/ATIF output must not leak credentials.

**Step 6: Run endpoint and agent tests**

```bash
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google \
  -run 'TestAdapter_.*FinalResponseEndpoint' -count=1
go test ./llm -run 'Test(SanitizeEndpointURL|FinalResponseEndpointURL|StampEndpointURL)' -count=1
go test ./agent -run 'Test.*(FinalResponseEndpoint|SanitizesAdapterEndpointAtDurableBoundary)' -count=1
go test ./llm ./agent ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google -count=1
```

Expected: PASS, including redirect provenance and malicious custom-adapter sanitation.

**Step 7: Fresh review and commit**

The reviewer must inspect every `StampEndpointURL` call and the durable boundary. Search for constructed URLs written directly into response raw metadata:

```bash
rg -n 'endpoint_url|StampEndpointURL|FinalResponseEndpointURL|SanitizeEndpointURL' llm agent
```

```bash
git status --short
git add llm/apilog.go llm/apilog_append_test.go \
  llm/providers/openai/adapter.go llm/providers/openai/chatcompletions.go \
  llm/providers/openai/responses.go \
  llm/providers/openai/wire_capture_test.go \
  llm/providers/openaicompat/adapter.go \
  llm/providers/openaicompat/wire_capture_test.go \
  llm/providers/anthropic/adapter.go \
  llm/providers/anthropic/wire_capture_test.go \
  llm/providers/google/adapter.go \
  llm/providers/google/wire_capture_test.go \
  agent/session_model_call.go agent/session_model_test.go agent/atif_test.go
git commit -m "fix: preserve sanitized final endpoint provenance

Stamp semantic responses from the final redirected response request URL while
stripping userinfo, query, and fragment data. Re-sanitize adapter metadata at
the durable agent boundary so a malicious custom adapter cannot leak secrets
into transcripts or ATIF exports."
```

---

### Task 11: Replace Anthropic terminal-order timing checks with causal synchronization

**Files:**

- Test only: `llm/providers/anthropic/wire_capture_test.go`

**Fresh roles:** Assign a fresh implementer and a different fresh reviewer. This task changes no production code.

**Step 1: Demonstrate the current test's weak oracle**

Inspect the terminal-order tests for fixed negative waits and duplicated timeout branches. Run them repeatedly before editing:

```bash
go test ./llm/providers/anthropic -run 'Test.*(Terminal|Settlement).*Order' -count=50
```

Record whether they pass, fail, or flake. Passing does not establish correctness; this task fixes the oracle, not a known production defect.

**Step 2: Introduce an event journal and explicit barriers**

Replace fixed 50 ms negative waits and duplicated `time.After` branches with channel barriers and a mutex-protected journal. Record these events at the relevant test boundaries:

```text
append_enter
append_return
terminal_observed
settlement_enter
```

Use a blocking sink to hold append until the test releases it. Assert the completed journal order contains:

```text
append_enter < append_return < terminal_observed
append_enter < append_return < settlement_enter
```

For a same-goroutine terminal-before-append regression, a nonblocking select immediately after `append_enter` is a causal assertion. Keep one bounded test deadline only as a deadlock guard, with exactly one failure branch.

Do not loosen the underlying requirement that terminal delivery and settlement occur after the attempt append returns.

**Step 3: Run repeated and race tests**

```bash
go test ./llm/providers/anthropic -run 'Test.*(Terminal|Settlement).*Order' -count=100
go test -race ./llm/providers/anthropic -run 'Test.*(Terminal|Settlement).*Order' -count=10
```

Expected: PASS without sleeps, duplicate timeout cases, or race reports.

**Step 4: Fresh review and commit**

The reviewer must confirm the diff is test-only and each order claim is based on an observed event, not the absence of an event for a duration.

```bash
git status --short
git add llm/providers/anthropic/wire_capture_test.go
git commit -m "test: make Anthropic terminal ordering causal

Replace timing-based negative assertions with explicit sink barriers and an
event journal. Prove API-attempt append returns before terminal delivery and
settlement without sleeps or duplicated timeout branches."
```

---

### Task 12: Run correction-wide verification and inspect scope

**Files:** No planned production changes. Any necessary correction must be committed in the owning task's files and reviewed by a fresh implementer/reviewer pair before this gate is repeated.

**Fresh roles:** Assign a fresh verifier who has not implemented prior tasks and a different fresh reviewer for the final diff and evidence.

**Step 1: Verify forbidden patterns and interface names**

```bash
rg -n 'APILogTransportUsesStandardCompression|standardCompressionOwner' llm
rg -n 'responseDrain|io\.Copy\(io\.Discard, resp\.Body\)' \
  llm/providers/internal/transport \
  llm/providers/{openai,openaicompat,anthropic,google}/models.go
rg -n 'TODO|TBD|placeholder|implement later' \
  llm/providers/internal/transport llm/providers agent cmd/serf-hub
```

Expected:

- No old compression-owner capability remains.
- No transport-completion or post-model-decode drain remains.
- No placeholder was introduced by this correction series. Existing unrelated matches, if any, must be identified by pre-series blame and left untouched.

**Step 2: Run focused contract suites**

```bash
go test ./llm/providers/internal/transport -count=1
go test ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google -count=1
go test ./llm ./agent ./cmd/serf-hub -count=1
```

Expected: PASS with no live credentials or external network.

**Step 3: Run the race detector on the concurrency-sensitive packages**

```bash
go test -race ./llm/providers/internal/transport -count=1
go test -race ./llm/providers/openai ./llm/providers/openaicompat \
  ./llm/providers/anthropic ./llm/providers/google -count=1
go test -race ./agent -count=1
```

Expected: PASS with no race report.

**Step 4: Run repository gates**

```bash
make test
go test ./... -count=1
```

Expected: PASS. Provider credentials in the environment must not cause a live request; `docs/testing.md` remains authoritative.

**Step 5: Audit the complete branch diff against scope**

```bash
git status --short
git log --oneline --decorate 0a73c01e..HEAD
git diff --stat 0a73c01e...HEAD
git diff --check 0a73c01e...HEAD
```

The final reviewer must explicitly confirm:

- Ascending attempt indices correspond to actual wire cycles, including standard transparent retries.
- Prior failed cycles append before the next cycle writes.
- Request and response evidence never performs an instrumentation drain or indefinite wait.
- Unknown decorators and incomplete bodies are inexact; proven raw complete standard-transport bodies remain exact.
- HTTP/1.1 and HTTP/2 explicit-empty header behavior match their Go 1.25 oracles.
- Only the named OpenAI fallback/context, timeout classification, compatible DONE cancellation, model-list read precedence, and Hub credential-header behavior changed.
- Redirected semantic endpoints are final and sanitized; custom-adapter metadata is sanitized again before persistence.
- No transcript/doctor, Superpowers, Project 3+, compatibility, or unrelated retry/fallback changes entered the diff.

**Step 6: Commit only if the verification task itself required a scoped correction**

If verification finds no defect, do not create an empty commit. If it finds a scoped defect, return it to a fresh implementer and reviewer under the owning task, then commit only that correction with a detailed message and repeat all Task 12 gates.

The implementation branch is ready for Jesse only when `git status --short` is empty and every command above has fresh passing output.
