# Project 2 YAGNI Landing Design

Date: 2026-07-17
Status: Approved by Jesse
Project: Transcript and API-log separation

## Purpose

Land Project 2's user-visible improvements without turning the private API log
into a hostile-filesystem transaction system or changing unrelated provider
behavior.

The product contract remains:

- transcripts are bounded semantic session records;
- API logs contain the exact serialized request and raw provider response that
  the adapter observed, except credential material is never persisted;
- ordinary transcript reads never inspect API logs;
- API-log access is explicit, bounded, and uncommon;
- every logical model call has durable attempt grouping and truthful settlement
  evidence;
- API-log failures are visible forensic failures but do not rewrite provider
  results or retry policy.

This design narrows storage, credential exclusion, and transport capture to the
smallest mechanisms needed to satisfy that contract.

## Supersession and Branch Posture

This design supersedes the implementation direction in:

- `wip/p2-thirdwave-apilog-core`'s API-log storage-identity design;
- `wip/p2-thirdwave-apilog-core`'s API-log crash-durability plan;
- `docs/superpowers/plans/2026-07-16-transcript-api-log-transport-corrections.md`.

Those documents remain historical review evidence, not implementation targets.
Do not implement or preserve their rollback-marker, persistent-name-lock,
filesystem-identity, permission-framework, per-wire-cycle, or
compression-emulation work.

Do not merge `wip/p2-thirdwave-apilog-core` as a commit range. It contains
useful regressions and reviewed findings mixed with rejected architecture.
Harvest only behavior required by this design onto the program execution
branch. Existing accepted transcript, grouping, and fuzz work on
`wip/systemic-serf-harness-execution` remains the integration base.

## Supported Ownership Model

A session has one owning Serf process. Concurrent processes are not supported
writers for one session's transcript, job state, or API log.

The API-log target file carries one defensive ownership lock:

- Serf acquires a nonblocking exclusive lock on the opened target file and
  retains it for the logger's lifetime.
- Lock contention fails loudly. Serf does not wait, retry, steal, inspect a
  PID, or coordinate with the owner.
- When resuming a known session, the top-level run or serve path eagerly opens
  and locks `<state-dir>/sessions/<session-id>.api.jsonl` before restoring the
  session or mutating transcript, job, metadata, or runtime state.
- A resume lock conflict reports that the session is already running and tells
  the caller to send work to the live session or fork it.
- Fresh sessions may open their API log lazily because their generated session
  ID is unique. Children share the process logger and use unique session IDs.
- Process exit or crash releases the OS lock. There is no lease, heartbeat,
  takeover, stale-owner repair, PID file, or separate session-lock namespace.

The target-file lock is a guard against accidental duplicate daemons. It does
not make concurrent writers supported and does not defend against same-user
renames, replacement, deletion, or deliberate lock bypass.

## Simple Append and Recovery Contract

The canonical API log is one strict newline-delimited JSON file per session.

### Open

1. Open or create the exact session leaf without following symlinks.
2. Require a regular file and acquire its target-file lock.
3. Create new files with mode `0600` on the supported macOS and Linux targets.
4. Inspect a bounded suffix, independent of total historical file size, to find
   the append boundary. The bound derives from the maximum canonical record
   size: one possible incomplete fragment plus the immediately preceding
   complete record.
5. Strictly validate the complete record immediately before the append boundary.
   A malformed, oversized, or unsupported boundary record fails closed without
   mutation.
6. If and only if the final line is incomplete and bounded, truncate it to the
   last complete newline and sync the truncation before accepting appends.
7. An incomplete fragment or complete boundary record that exceeds the
   canonical record bound, a missing boundary within that bound, or an unsafe
   target fails closed without mutation.

The API log is forensic evidence, not session replay state. Explicit API-log
readers remain responsible for strict validation of every record they consume.
Open-time recovery protects the next append from a torn tail; it does not
revalidate ancient records on every daemon start. Without previously trusted
metadata, bounded startup and whole-history revalidation of an existing file
cannot both be guaranteed. This tail-boundary contract keeps resume work
bounded while preserving exclusive ownership and canonical append durability.

### Append

1. Build and validate one bounded canonical record in memory.
2. Write exactly one newline-terminated record while holding the logger's
   in-process mutex.
3. Require the write to report the complete byte count.
4. Sync the target before reporting append success or allowing the next Serf
   retry, fallback, terminal publication, or group settlement to advance.

Every canonical append is synchronously durable. Remove interval-based dirty
batching from this path; it adds ownership and pending-failure machinery without
serving this forensic contract.

### Failure

A short write, write error, or sync error:

- leaves the target bytes unchanged from what the OS produced;
- records one sticky logger failure in memory;
- emits the existing sanitized forensic-failure observation;
- quarantines the logger from every later append;
- prevents a green `Close`;
- does not change the provider response, provider error, or retry/fallback
  decision.

Serf does not truncate, roll back, publish intent, republish intent, or attempt
same-process recovery after an append failure. A later process handles only an
incomplete final line through the open-time rule above. Missing group settlement
after a failed append or crash truthfully means the forensic record is
incomplete.

### Explicitly Removed Storage Machinery

Project 2 does not include:

- `.serf-apilog-locks` or `.serf-apilog-rollback`;
- rollback marker v1 or v2;
- target inode, device, volume, or file-ID persistence;
- fresh-name revalidation around filesystem transitions;
- truncate-and-republish transactions;
- reserved-directory ownership or namespace durability protocols;
- POSIX/NFSv4 ACL inspection or a new permission framework;
- cross-process recovery coordination;
- operator repair or migration commands.

## Credential Exclusion

Credential exclusion remains a hard security boundary, but it operates on
structured evidence rather than trying to preserve partially redacted strings.

### Credential Sources

The logger learns credential material from the configured provider and each
request presented to the provider's `RoundTripper`:

- standard authentication, proxy-authentication, and cookie fields;
- declared custom credential headers;
- URL userinfo;
- declared credential query parameters;
- the actual values carried by those fields after request hooks and on each
  redirect hop.

Ordinary configured headers are not credentials. A secret-looking ordinary
header does not become trusted authentication and must not satisfy Hub
credential validation.

Keep the existing classification split:

- header and query names identify structured fields to omit;
- secret names and values are forbidden in provider-derived durable evidence;
- standard structural names such as `Authorization` are not themselves secret;
- a custom credential name may be secret and therefore forbidden globally.

### Omit, Do Not Rewrite

Never insert a redaction marker into provider evidence.

- Omit credential headers and credential query parameters structurally.
- Strip URL userinfo, query, and fragment from persisted endpoint provenance.
- If a request body, response body, ordinary header value, provider error, or
  other provider-derived field contains known credential material, omit that
  whole evidence field.
- Mark an omitted body `exact=false` and
  `credential_values_excluded=true`. Preserve exact bytes and their UTF-8 or
  base64 encoding only when the whole body is credential-free.
- Persist only inert sanitized error text. Rendering or unwrapping an untrusted
  error is bounded, panic-safe, and never preserves the original error object.

Before append, inspect only provider-derived durable evidence for known secret
literals. For string credentials, the fixed pattern set is the raw UTF-8 value,
its `url.QueryEscape` form, its `url.PathEscape` form, and the content form
produced by `encoding/json` string escaping. Use simple bounded literal
searches. Do not build a general decoder graph, Shift-And engine, recursive
semantic matcher, or arbitrary encoding recognizer. If admission cannot prove
the record credential-free, reject the append and report incomplete forensics.

Generated IDs, timestamps, outcome enums, schema keys, delimiters, and other
closed structural values are not credential-bearing evidence and do not cause
false rejection when a short credential happens to overlap them.

Outcome classification does not walk arbitrary error graphs. Caller
cancellation comes from the owning context; provider rejection, decoding,
timeout, and transport outcomes come from their explicit adapter paths; an
unclassified error is a transport failure. Durable error text is rendered once
under panic recovery and then detached from the original error.

## Transport Evidence Boundary

Project 2 records one attempt per `http.RoundTripper.RoundTrip` invocation at
the provider adapter boundary. Redirect hops naturally produce separate
RoundTrip invocations. Serf/provider retries, endpoint fallbacks, and model
fallbacks also produce separate attempts. Project 2 does not split one
RoundTrip call into internal TCP, HTTP/1.1, or HTTP/2 connection retry cycles.

Request evidence is the exact serialized body and headers presented on that
RoundTrip invocation after provider construction and request hooks.
Response evidence is the exact byte stream the provider adapter reads from
`http.Response.Body` before JSON or SSE interpretation. If the configured Go
transport transparently decompresses a response, the decompressed body exposed
to the adapter is the canonical raw provider body. Project 2 does not capture or
reconstruct compressed wire frames.

Instrumentation observes application reads; it does not issue extra reads,
drain bodies, emulate Go's gzip behavior, unwrap arbitrary RoundTripper stacks,
or change Read/Close ownership. Evidence is exact only when EOF or another
existing terminal condition proves completeness. Otherwise preserve the bytes
observed so far and mark the body inexact.

Do not implement per-wire `httptrace` lifecycle splitting, HTTP/1.1-versus-HTTP/2
gzip predicates, Serf-owned gzip readers, transparent transport interfaces, or
concurrent gzip Read/Close state machines.

## What We Keep

Keep and integrate the parts that directly implement the approved product:

- transcript v2 semantic-only hard break;
- strict bounded transcript readers and indexes;
- explicit `source="api_log"` summaries and explicit attempt expansion;
- default transcript reads that never open or stat API logs;
- attempt IDs, group IDs, attempt indexes, and append-only settlement records;
- one group across Serf/provider retry and fallback routes for one logical call;
- exact provider-adapter request and observed response bytes with honest body
  truth fields;
- strict canonical record decoding and bounded line/record sizes;
- `0600` Unix API-log creation and target-file ownership locking;
- sanitized final endpoint provenance;
- Hub recognition of declared nonempty credential headers;
- doctor reading the canonical API log rather than transcript API records;
- transcript/API-log deletion by exact session ownership;
- deterministic scripted-provider, reader-bound, grouping, resume-collision,
  secrecy, and partial-tail tests;
- the corrected transcript fuzz oracles and registered fuzz coverage.

## What We Cut or Defer

Cut from Project 2:

- all storage machinery listed in the removal section;
- per-connection-cycle retry forensics inside one RoundTrip;
- compressed on-the-wire response capture;
- custom compression emulation and transport-wrapper introspection;
- a generalized credential-matching engine;
- arbitrary error-graph preservation;
- broad provider lifecycle changes discovered during review, including OpenAI
  fallback-context behavior, SSE timeout policy, DONE cancellation, and
  model-list read precedence, unless the simplified logger itself changes that
  behavior;
- unrelated live-provider improvements, compatibility paths, migrations, and
  operator tooling.

Real provider bugs discovered during Project 2 review should be recorded as
separate follow-up work. They do not block this landing when the simplified
instrumentation preserves the pre-Project-2 provider behavior.

## Remaining Work Required to Land Project 2

### 1. Replace the overbuilt core

Implement the target-lock, strict append, synchronous sync, sticky quarantine,
bounded partial-final-line recovery, boundary-record validation, structured
omission, and bounded admission rules above on a fresh branch from the program
execution head. Reuse small reviewed helpers or tests only when they match this
design. Do not merge the Thirdwave core range.

### 2. Add eager resume ownership

Allow the top-level logger attachment to reserve a known resumed session ID.
Both `serf --resume` and `serf serve --resume` acquire the API-log target lock
before restore side effects. Add a real lock-contention test proving the second
resume fails without transcript, job, or metadata mutation and tells the user
to use the live session or fork.

### 3. Finish required credential consumers

Land only the consumer work that depends on final core truth fields:

- project body `exact` and `credential_values_excluded` accurately through
  explicit API-log summaries and body expansion;
- recognize nonempty declared credential headers in Hub launch validation;
- sanitize endpoint provenance again at the durable transcript/ATIF boundary;
- preserve exact-session artifact deletion and bounded doctor evidence.

### 4. Simplify transport capture

Replace the accepted transport-corrections plan with a small plan matching the
adapter boundary above. Prove that capture never drains or owns bodies, records
the bytes the adapter observed, marks incomplete evidence honestly, and creates
separate attempts only for explicit provider/Serf retries and fallbacks.

### 5. Integrate independent slices

Retain the already accepted transcript, grouping, and fuzz changes, resolving
only compile or truth-field conflicts caused by the simplified core. Rebase the
consumer slice and land only its required work after its body-truth finding is
corrected and reviewed. Do not reopen any independent slice's product scope.

### 6. Close with bounded verification

Required final evidence:

- focused core tests for canonical append, sync failure, sticky quarantine,
  bounded final-tail recovery, boundary corruption rejection, target lock, and
  eager resume;
- real scripted-provider tests for exact UTF-8 and binary request/response
  bodies, explicit retries/fallbacks, credential omission, and inexact bodies;
- transcript tests proving no API payloads and no default API-log access;
- explicit API-log reader bounds and exact attempt expansion;
- doctor, Hub, deletion, grouping, and semantic-turn join tests;
- full deterministic repository tests, touched-package race tests, vet, lint,
  plus Linux cross-compilation from the macOS development host;
- one independent security review limited to credential persistence and one
  independent whole-project review against this exact design.

The review gate requires zero Critical or Important findings that violate this
design. Findings that propose excluded capabilities are scope proposals for
Jesse, not automatic implementation requirements. A correction does not widen
the next review's contract.

## Landing Condition

Project 2 is complete when the program execution branch contains the simplified
core and required consumers, all listed gates pass, the two bounded reviews
close with zero in-scope Critical or Important findings, and no obsolete raw API
log or transcript `api_call` production path remains.

Project 2 completion does not require hostile-namespace recovery, concurrent
session writers, exact compressed wire bytes, internal net/http retry cycles, a
general secret-decoding engine, or fixes for unrelated provider lifecycle bugs.
