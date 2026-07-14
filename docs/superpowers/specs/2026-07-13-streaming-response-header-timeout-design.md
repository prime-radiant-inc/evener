# Streaming Response-Header Timeout Design

**Date:** 2026-07-13
**Status:** Implemented

## Problem

Serf does not bound the time a streaming provider request may spend waiting for
HTTP response headers.

The streaming timeout path currently has three parts:

1. `AdapterTimeout.Connect` bounds connection establishment.
2. `AdapterTimeout.Request` is deliberately omitted from the request context so
   that Serf does not add an overall deadline to a healthy long-running stream.
   Any caller-supplied context or `http.Client.Timeout` remains authoritative.
3. `AdapterTimeout.StreamRead` begins only after `client.Do` returns a response
   and bounds the time between SSE lines.

This leaves `client.Do` unbounded between connection establishment and receipt
of response headers. A provider can accept the connection and then leave Serf
waiting indefinitely without producing a stream. Because job notifications are
delivered at model-turn boundaries, a parent session blocked in that state also
cannot observe a completed child job.

The observed session `01KXF0CAQQYX73BMT40NJKPXZN` exhibited exactly this
failure mode. The child review job completed and its notification became
pending, but the parent's OpenAI Responses request did not receive response
headers for roughly 29 minutes and 32 seconds. Once the provider created the
response, it completed in about five seconds and Serf delivered the queued job
notification immediately afterward. This is evidence of an unbounded
pre-header wait, not a lost notification.

## Goals

- Bound streaming requests while they wait for response headers.
- Avoid adding a Serf-owned total stream deadline after headers arrive while
  preserving caller-supplied context and HTTP client policy.
- Preserve the existing per-SSE-line idle timeout.
- Apply the behavior through shared HTTP client construction so all HTTP
  streaming providers receive the same contract.
- Preserve the useful defaults and settings of Go's standard HTTP transport.
- Keep timeout failures compatible with Serf's existing retry classification.
- Verify the behavior with deterministic local tests that do not contact a
  provider.

## Non-Goals

- Interrupting an active model request when a child job completes.
- Delivering notifications in the middle of a model input or output.
- Changing notification queueing, wakeup, or turn-boundary semantics.
- Changing retry counts, backoff, or partial-output retry behavior.
- Adopting WebSockets for OpenAI Responses requests.
- Adding a new timeout configuration field.
- Adding provider-specific timeout implementations.

## Decision

Use `AdapterTimeout.Request` as the response-header timeout for streaming
requests.

The field retains its existing whole-request meaning for non-streaming calls.
For streaming calls, it bounds the wait for response headers after the request
body has been written. It does not remain active while the response body is
being consumed.

The resulting timeout contract is:

| Setting | Non-streaming request | Streaming request |
|---|---|---|
| `Connect` | Bounds network dialing | Bounds network dialing |
| `Request` | Bounds the entire request and response | Bounds the wait for response headers |
| `StreamRead` | Not used | Bounds the wait between SSE lines after headers |

With the session defaults, a streaming attempt therefore has these phases:

```text
dial               request write       wait for headers                 SSE body
 |<- Connect: 10s ->|  transport-owned  |<-- Request: 10m -->|<- StreamRead: 30s per line ->
```

This closes the observed unbounded response-header wait without imposing a
ten-minute lifetime on a healthy stream.

## HTTP Client Construction

Replace `ClientWithConnectTimeout` with `ClientWithAdapterTimeout`, whose name
reflects the full adapter timeout contract.

The helper returns a shallow copy of the supplied `http.Client` and does not
mutate the caller's client or transport. Caller client fields, including an
explicit `http.Client.Timeout`, remain unchanged and authoritative. When the
underlying transport is the standard `*http.Transport`, the helper:

1. Clone the transport.
2. When `AdapterTimeout.Connect` is non-zero, wrap an existing `DialContext`
   with a Connect-bounded context. If both `DialContext` and deprecated `Dial`
   are nil, use the normal zero-value `net.Dialer` behavior under that context.
3. Preserve deprecated context-free `Dial` unchanged and caller-authoritative;
   safely imposing a context deadline would require a goroutine whose dial could
   outlive cancellation and leak.
4. Likewise wrap an existing `DialTLSContext`, which owns non-proxied HTTPS
   dialing and otherwise bypasses `DialContext`.
5. Preserve deprecated context-free `DialTLS` unchanged and caller-authoritative;
   safely imposing a context deadline would require a goroutine whose dial could
   outlive cancellation and leak.
6. Set `ResponseHeaderTimeout` from `AdapterTimeout.Request` when non-zero.
7. Install the cloned transport on the copied client.

When the client has no explicit transport, the helper will clone
`http.DefaultTransport`, which is a `*http.Transport`. It must not construct a
bare `http.Transport`, because doing so discards standard proxy, TLS,
HTTP/2, connection-pool, and idle-connection behavior.

An injected, non-standard `http.RoundTripper` is an explicit transport seam and
must remain installed. Serf cannot safely clone or configure opaque transports;
such a transport owns its connection and response-header timeout behavior. This
also preserves scripted transports used by deterministic provider tests.

All adapters that currently call `ClientWithConnectTimeout` will use the
generalized helper. The timeout semantics must not be implemented separately in
OpenAI Responses, OpenAI Chat, Anthropic, Google, OpenAI-compatible, or other
HTTP adapters.

## Request and Stream Behavior

`ApplyAdapterTimeout` continues to avoid adding a `Request`-based context
deadline for streaming calls. Such a deadline would remain active after headers
and incorrectly terminate a healthy stream. Existing caller context deadlines
and caller-supplied `http.Client.Timeout` values remain active and authoritative.

For a standard HTTP transport, `ResponseHeaderTimeout` starts after the request
body is fully written and applies only until response headers are received.
After `client.Do` returns:

- the response-header timer is no longer relevant;
- Serf adds no total-duration bound while the response body is read;
- the SSE reader continues applying `StreamRead` independently to each line;
- normal caller cancellation and caller HTTP client policy still terminate the
  request and stream.

No provider adapter needs an additional goroutine, timer, or body wrapper for
this behavior.

## Errors and Retries

A response-header timeout occurs before a provider stream is available and
before Serf can emit any partial model output. Go's standard transport reports
this timeout as a deadline-exceeded transport error. Existing Serf error
wrapping must classify it as a retryable request-timeout error.

The existing stream retry policy remains unchanged. In particular, the new
timeout bounds one streaming attempt; it does not bypass configured retries or
guarantee that a queued child-job notification is delivered immediately after
the first timed-out attempt.

This distinction is intentional:

```text
child job completes
        |
        v
notification queued ----> active model attempt continues
                                  |
                         headers arrive or timeout
                                  |
                         existing retry policy runs
                                  |
                         next model-turn boundary
                                  |
                         notification delivered
```

Transport liveness and notification preemption are separate concerns. This
design fixes the former without changing the latter.

## Testing

Before changing tests, implementation work must follow `docs/testing.md`.
Tests will use local scripted transports or `httptest` servers and must not
depend on provider credentials, external network access, quota, or current
model behavior.

The implementation must cover these contracts:

1. **Stalled response headers time out.** A local server accepts a request,
   signals that its handler is running, and withholds headers. The streaming
   call returns a typed retryable request-timeout error. The test coordinates
   the handler with channels and does not use an arbitrary sleep.
2. **Serf adds no overall request deadline to a healthy stream.** Headers are
   returned, then the response body is held open under test control. The stream
   remains usable after establishment and is governed by `StreamRead`, not by a
   lingering `Request` context deadline. Caller context and HTTP client policy
   remain authoritative.
3. **Standard transport settings are preserved.** Client construction clones
   the original standard transport, retains unrelated settings, wraps custom
   context-aware dial hooks with the Connect deadline, sets the response-header
   timeout, and does not mutate the original.
4. **Default transport behavior is preserved.** A client with a nil transport
   receives a clone of `http.DefaultTransport`, not a zero-value transport.
5. **Opaque transports remain authoritative.** A custom scripted
   `RoundTripper` remains installed and usable rather than being replaced.
6. **Deprecated `Dial` and `DialTLS` remain authoritative.** Their context-free
   signatures prevent safe deadline wrapping, so they remain unchanged and
   control their respective dials when the context-aware hook is absent.
7. **Explicit client timeouts remain authoritative.** The copied client retains
   caller-supplied `http.Client.Timeout`, even though that policy may bound the
   total lifetime of a stream.
8. **Non-streaming semantics do not change.** `Request` remains the overall
   request deadline for ordinary completion calls.
9. **Provider plumbing uses the shared contract.** At least one representative
   streaming adapter test exercises the standard-client response-header
   timeout, while helper-focused tests establish the behavior shared by the
   other adapters.

Existing deterministic job-control scenarios continue to cover the
notification boundary contract. This change does not require a new live
subagent or live provider scenario.

## Alternatives Rejected

### Set or overwrite `http.Client.Timeout`

`http.Client.Timeout` includes reading the response body. It would impose an
overall lifetime on a streaming request and terminate healthy long responses.
Serf therefore does not set or clear it; an existing caller value is preserved
as authoritative policy.

### Apply `AdapterTimeout.Request` as a streaming context deadline

The context remains attached to the response body after headers. This has the
same incorrect overall-stream behavior as `http.Client.Timeout`.

### Add a `StreamStart` timeout setting

The existing `Request` setting already represents the user's tolerance for a
request that has not produced a response. A new setting adds configuration and
documentation without solving a distinct current requirement.

### Interrupt the model request when a child job completes

Codex-style subagent mailboxes are drained between model sampling calls; they
do not establish a requirement to cancel an active sampling call. Mid-turn
preemption would introduce transcript, retry, partial-output, and provider
cancellation semantics far beyond this transport bug.

### Adopt OpenAI WebSockets

Codex commonly uses WebSockets for OpenAI Responses and applies separate
connection and message-idle limits. Serf has multiple HTTP streaming providers,
and the demonstrated defect is in shared HTTP lifecycle handling. A WebSocket
implementation would be a broad provider-specific feature, not the smallest
root-cause fix.

### Patch only the OpenAI Responses adapter

Every HTTP streaming adapter can encounter the same pre-header stall. A
provider-local timer would duplicate lifecycle logic and leave the shared
contract inconsistent.

### Replace the transport with a bare `http.Transport`

This would set the desired timeout while silently discarding standard transport
behavior and caller configuration. Cloning the standard transport is both
smaller and safer.

## Scope of the Implementation

The implementation should be limited to:

- documenting the phase-dependent meaning of `AdapterTimeout.Request`;
- generalizing shared HTTP client construction;
- migrating existing adapter call sites to that helper;
- adding deterministic transport and representative provider tests; and
- updating comments that currently describe streaming timeouts as only connect
  plus stream-read limits.

It should not alter session scheduling, notification delivery, provider
selection, WebSocket support, or retry policy.
