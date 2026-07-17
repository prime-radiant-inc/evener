# Project 2 YAGNI Landing Implementation Plan

> **Execution:** Use `superpowers:subagent-driven-development`. Every implementer
> must use `superpowers:test-driven-development`, commit only its task, and report
> RED and GREEN commands. Review each parallel lane against its extracted brief
> before integration. Do not merge or preserve the rejected Thirdwave core range.

**Goal:** Land the approved transcript/API-log product contract with a simple,
single-owner, synchronously durable API log and observational HTTP capture.

**Architecture:** Keep the accepted semantic transcript, grouping, reader, and
fuzz work already on `wip/systemic-serf-harness-execution`. Replace the current
batched API-log writer with one locked target file per session, one validated
newline-terminated write plus sync per record, and sticky quarantine after any
write or sync uncertainty. Capture only the request/response bytes the provider
adapter presents or reads; never drain, decompress, or own HTTP bodies. Omit
credential-bearing provider evidence wholesale and make API-log reads explicit.

**Tech stack:** Go, `net/http`, macOS/Linux file locking through
`golang.org/x/sys/unix`, strict JSONL codecs, scripted providers, and local
`httptest` servers only.

**Binding spec:**
`docs/superpowers/specs/2026-07-17-project-2-yagni-landing-design.md`

## Global Constraints

These constraints bind every task and every review. Agents must implement only
the task brief and these constraints. A useful adjacent bug is a follow-up, not
permission to widen Project 2.

- This is a hard break. Add no compatibility reader, migration, fallback schema,
  operator repair command, or legacy transcript/API-log path.
- Do not modify Superpowers.
- Do not merge `wip/p2-thirdwave-apilog-core` or copy its rollback markers,
  storage-identity machinery, lock namespace, permission framework, compression
  emulation, `httptrace` wire-cycle splitting, or generalized secret matcher.
- A session has one owning Serf process. The target-file lock is only a
  nonblocking duplicate-owner detector; it is not a concurrent-writer protocol.
- A resumed session is locked before restore or any transcript, job, metadata,
  or runtime mutation. Fresh unique session IDs remain lazy.
- Every accepted record is one bounded canonical JSON object plus one newline,
  written once under the logger mutex and synchronously synced before append
  success. Remove interval batching.
- A short write, write error, or sync error is sticky: observe one sanitized
  forensic failure, quarantine the logger, fail later appends, and prevent a
  green `Close`. Do not roll back or recover in-process. Provider behavior and
  retry/fallback classification do not change.
- Open-time recovery truncates and syncs only an incomplete final line. Complete
  or interior corruption is rejected unchanged.
- Leaf creation is `0600` on supported macOS and Linux targets. Reject symlinks
  and non-regular leaves. Do not build a permission framework.
- Credential material is omitted, never replaced with a marker. Omit structured
  credential headers/query parameters. If known secret material occurs in any
  other provider-derived field, omit the whole field; rejected evidence must not
  leak through warnings.
- The only string credential patterns are raw UTF-8, `url.QueryEscape`,
  `url.PathEscape`, and JSON-string content escaping. Use bounded literal search.
- Header/query names identify structured omissions. Secret names and values are
  forbidden in provider evidence. Standard names such as `Authorization` are
  structural, while configured custom credential names can be secret.
- Render untrusted errors exactly once under panic recovery and detach inert
  text. Outcome classification uses owning context and explicit adapter result
  paths; unknown errors are transport failures. Do not walk arbitrary error
  graphs.
- One canonical attempt equals one `http.RoundTripper.RoundTrip` invocation.
  Redirects and explicit provider/Serf retry or fallback calls create distinct
  attempts. Internal connection retries do not.
- Capture only bytes the adapter reads. Do not add reads, drains, closes, gzip
  wrappers, transparent-transport unwrapping, or body-ownership changes.
- Default tests are deterministic and credential/network independent. Use
  scripted providers and local test servers at external boundaries, never mocks
  of Serf internals.
- Do not fix OpenAI fallback context, SSE timeout policy, DONE cancellation,
  model-list precedence, or other unrelated provider behavior in this project.
- Tests assert structured behavior, not large rendered strings or generated
  implementation text.

## Parallel Execution Lanes

```text
Lane A: Task 1 canonical evidence and credential omission ---------+
Lane B: Task 2 locked storage and eager resume ownership ----------+
Lane C: Task 3 observational HTTP capture -------------------------+--> integration
Lane D: Task 3 endpoint provenance sub-slice -----------------------+
Lane E: Task 4 doctor, Hub, and deletion sub-slices ----------------+
                                                                    |
                 Task 4 body-truth reader projection after Lane A --+
                                                                    |
                 Task 5 bounded verification and final reviews <----+
```

Run independent lanes concurrently in distinct managed worktrees. No two
writing agents may share a worktree. Lane B may leave the final one-line switch
to Task 1's `apilog.MarshalRecord` for integration; Lane C may leave the final
inexact-field wiring for integration. The controller integrates Lane A first,
then rebases/cherry-picks the other lanes and resolves only those declared
interface seams. Review the combined behavior, not intermediate uncompilable
interface states.

### Task 1: Make canonical evidence truthful and credential-free

**Files:**

- Modify: `llm/apilog/body.go`
- Create: `llm/apilog/header.go`
- Modify: `llm/apilog/record.go`
- Modify: `llm/apilog/codec.go`
- Modify: `llm/api_attempt.go`
- Modify: `llm/api_attempt_sanitize.go`
- Modify only required credential-material constructor call sites under:
  `llm/providers/{anthropic,google,openai,openaicompat,kimi}/`
- Test: `llm/apilog/{body,header,record,codec}_test.go`
- Test: `llm/api_attempt_sanitize_test.go`
- Test: `llm/api_attempt_test.go`
- Test only required provider wire tests under the same provider directories

**Scope lock:** This task owns durable record shape, canonical encoding,
credential admission, error flattening, and explicit outcome inputs. It does not
open files, change HTTP body ownership, change retry policy, or edit readers.

**Step 1: Write failing schema and byte-fidelity tests.**

Add focused tests proving:

- `EncodedBody` requires explicit `exact` and
  `credential_values_excluded` fields on decode.
- `exact=true` with `credential_values_excluded=true` is invalid.
- UTF-8 and arbitrary binary body bytes round-trip exactly.
- HTTP header values, including invalid UTF-8 bytes, round-trip through an
  `EncodedHeaderValue` UTF-8/base64 representation.
- `MarshalRecord` validates a concrete attempt/settlement and emits the same
  strict canonical record shape accepted by `DecodeRecord`.
- Unknown fields, trailing JSON, invalid durable enums, oversized content, and
  absent truth fields fail closed.

Run:

```sh
go test ./llm/apilog -run 'Test(EncodedBody|EncodedHeader|MarshalRecord|APIAttemptDecodeRecord)' -count=1
```

Confirm RED for missing truth fields/header encoding/canonical marshal.

**Step 2: Implement the smallest strict codec.**

- Add `Exact bool` and `CredentialValuesExcluded bool` with presence-aware
  strict unmarshalling to `EncodedBody`.
- `EncodeBody` preserves exact UTF-8 or base64 bytes and sets `Exact=true`.
- Add `EncodedHeaderValue` with the same exact UTF-8/base64 byte contract.
- Change durable request headers from `map[string][]string` to the encoded
  header-value representation.
- Add `apilog.MarshalRecord(APILogRecord) ([]byte, error)`; validate before
  marshal and keep strict canonical decode. Do not accept `any` at the storage
  boundary.

Run the focused test until GREEN, then:

```sh
go test ./llm/apilog -count=1
```

**Step 3: Write failing credential-omission tests.**

Add end-to-end record-building tests with credentials appearing as:

- raw UTF-8;
- query-escaped and path-escaped strings, including lowercase/mixed-case hex
  input where the configured pattern itself contains those bytes;
- JSON-string content escaping;
- a configured custom credential header name and value introduced after a
  redirect/request hook;
- URL userinfo/query/fragment;
- request/response binary bodies, ordinary header values, error text, response
  model/finish reason, and provider-derived usage numbers.

Assert structured omission: no marker is inserted; a secret-bearing body has no
durable content, `exact=false`, and `credential_values_excluded=true`; other
secret-bearing evidence fields are empty/absent; final admitted records contain
none of the fixed secret patterns. Also prove a short credential overlapping a
generated ID, timestamp, schema key, enum, or delimiter does not reject an
otherwise safe record.

Add tests proving `Error()` is invoked once, panic is contained, no raw error or
unwrap behavior survives, owning-context cancellation is explicit, explicit
provider/decode/timeout results retain their class, and an unknown error becomes
`transport_failure`.

Run:

```sh
go test ./llm -run 'Test(API.*Credential|Sanitize.*API|BuildAPIAttempt|SettleResult|APILogError)' -count=1
```

Confirm RED because current code rewrites evidence and preserves behavior-
bearing errors.

**Step 4: Implement structured omission and bounded admission.**

- Keep credential header/query names separate from globally forbidden custom
  secret names and values.
- Precompute only the four approved string variants and use bounded literal
  searches. Do not decode or recursively transform evidence.
- Use one predicate for recognizing credential-bearing request names when both
  collecting dynamic values and omitting structured fields.
- Strip endpoint userinfo, query, and fragment.
- Omit the whole provider field on any known-secret match. Treat provider usage
  integers as provider-derived decimal evidence during admission.
- Make durable errors inert strings rendered once under panic recovery.
- Classify results from context and explicit adapter outcome inputs, with
  unknown as transport failure.
- Validate final provider-derived evidence immediately before canonical
  marshal; generated/closed structural fields are outside that scan.

Run focused tests until GREEN, then:

```sh
go test ./llm ./llm/apilog ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openai ./llm/providers/openaicompat ./llm/providers/kimi -count=1
go test -race ./llm ./llm/apilog -count=1
go vet ./llm/... 
```

**Step 5: Commit only Task 1.**

```sh
git status --short
git add llm/apilog/body.go llm/apilog/header.go llm/apilog/record.go llm/apilog/codec.go \
  llm/apilog/body_test.go llm/apilog/header_test.go llm/apilog/record_test.go llm/apilog/codec_test.go \
  llm/api_attempt.go llm/api_attempt_sanitize.go llm/api_attempt_test.go llm/api_attempt_sanitize_test.go \
  llm/providers/anthropic llm/providers/google llm/providers/openai llm/providers/openaicompat llm/providers/kimi
git diff --cached --check
git commit -m 'Make API attempt evidence truthful and credential-free'
```

The commit message must explain the hard-break schema, whole-field omission,
and why provider behavior is unchanged.

### Task 2: Lock resumed sessions and synchronously persist each record

**Files:**

- Modify: `llm/apilog.go`
- Create: `llm/apilog_open_unix.go`
- Test: `llm/apilog_append_test.go`
- Test: `llm/apilog_lock_test.go`
- Modify: `cmdutil/api_logging.go`
- Test: `cmdutil/api_logging_test.go`
- Modify only resume attachment seams in: `cmd/serf/run.go`,
  `cmd/serf/serve.go`
- Test: `cmd/serf/run_test.go`, `cmd/serf/serve_test.go`, and a narrowly named
  resume-ownership integration test if needed

**Scope lock:** This task owns file open/lock/recovery/append/close and eager
resume reservation. No lease, PID, lock directory, inode identity, rollback,
ACL framework, concurrent-writer support, or transcript format change.

**Step 1: Write failing file-contract tests.**

Add real-file tests proving:

- a second logger cannot open/lock the same target while the first is alive;
- closing/crashing ownership releases the OS lock for a later open;
- symlink and non-regular targets fail closed;
- new Unix leaves are `0600`;
- an incomplete final line alone is truncated and synced before append;
- malformed complete/interior records and oversized complete records are
  rejected without mutation;
- every append performs one full newline-terminated write and a sync before
  success;
- short write, write error, or sync error becomes sticky, produces one
  sanitized observation, rejects later appends without filesystem access, and
  makes `Close` fail;
- `Close` waits for admitted appends and rejects new ones.

Use narrow injected file-operation seams already present in `llm/apilog.go` for
short-write/sync failures; do not add a general filesystem abstraction.

Run:

```sh
go test ./llm -run 'Test(APILogger|SessionAPILogger|RecoverCanonical|APILogTarget)' -count=1
```

Confirm RED for lock ownership, synchronous append, and quarantine.

**Step 2: Implement the simple logger.**

- The macOS/Linux open helper opens/creates the exact leaf without following a link,
  require a regular file, acquire a nonblocking exclusive target-file lock, and
  retain the descriptor until `Close`.
- Use `O_NOFOLLOW`, require a regular file, create/chmod `0600`, and retain a
  nonblocking exclusive `flock` for the logger lifetime.
- Add `(*APILogger).ReserveSession(sessionID string) error` that eagerly opens
  the routed target through the same code as lazy append.
- Replace `json.Marshal(any)` with Task 1's `apilog.MarshalRecord`.
- Remove `SyncInterval`, dirty maps, pending-sync ownership, and periodic sync.
- Under the logger mutex, require exactly one full record write then sync.
- Store the first write/sync failure as the logger's sticky quarantine error.
  Observer output is already credential-safe; later appends return without
  reopening, rewriting, syncing, or observing duplicates.
- `Close` returns the sticky error after closing descriptors and never reports
  green after quarantine.

Run focused tests until GREEN, then:

```sh
go test ./llm ./cmdutil -count=1
go test -race ./llm ./cmdutil -count=1
```

**Step 3: Write failing eager-resume tests.**

Through real top-level `run` and `serve` seams, hold a logger lock for an
existing session, attempt `--resume`, and assert:

- failure happens before restore-side transcript, job, metadata, or runtime
  mutation;
- the error says the session is already running and directs the caller to send
  work to it or fork it;
- no provider request occurs;
- a fresh session remains lazy;
- every current path that invokes `RestoreSessionFromMetaWithConfig`, including
  `--resume-last` and `--resume-with`, reserves the target before restore. If a
  later explicit fork path creates a new session ID, that new path remains lazy.

Run:

```sh
go test ./cmd/serf -run 'Test(Run|Serve).*Resume.*Running|Test.*ResumeWith.*Lock' -count=1
```

Confirm RED because attachment cannot reserve a session today.

**Step 4: Wire eager reservation before restore.**

- Change `cmdutil.AttachAPILogger` to accept an optional resumed session ID and
  call `ReserveSession` immediately after logger construction.
- Update run and serve call sites only. Pass the selected existing session for
  every path that restores it; pass no reservation for fresh sessions.
- Wrap lock contention with the required user-facing guidance. Preserve the
  underlying error for diagnostics.

Run focused tests until GREEN, then:

```sh
go test ./cmd/serf ./cmdutil ./llm -count=1
go test -race ./cmd/serf ./cmdutil ./llm -count=1
GOOS=linux GOARCH=amd64 go test ./llm ./cmdutil -run '^$'
```

**Step 5: Commit only Task 2.**

```sh
git status --short
git add llm/apilog.go llm/apilog_open_unix.go \
  llm/apilog_append_test.go llm/apilog_lock_test.go \
  cmdutil/api_logging.go cmdutil/api_logging_test.go \
  cmd/serf/run.go cmd/serf/serve.go cmd/serf/run_test.go cmd/serf/serve_test.go
git diff --cached --check
git commit -m 'Lock and synchronously persist resumed session API logs'
```

### Task 3: Observe adapter HTTP bytes without owning bodies

**Files:**

- Modify: `llm/providers/internal/transport/api_attempt.go`
- Modify: `llm/providers/internal/transport/http_attempts.go`
- Delete only rejected compression/wire-cycle helpers if present and now unused
- Test: `llm/providers/internal/transport/api_attempt_test.go`
- Test: `llm/providers/internal/transport/http_attempts_test.go`
- Test: `llm/providers/internal/transport/wire_fidelity_test.go`
- Modify only necessary endpoint stamping call sites under:
  `llm/providers/{anthropic,google,openai,openaicompat,kimi}/`
- Modify: `agent/session_model_call.go`
- Test: `agent/session_model_call_test.go` or the nearest existing focused test

**Scope lock:** This task observes application-level bytes and endpoint
provenance. It must not drain, close, decompress, unwrap arbitrary transports,
split internal connection cycles, or alter retry/fallback/timeout behavior.

**Step 1: Write failing observational-capture tests.**

Use real `io.ReadCloser` fakes and local HTTP servers to prove:

- one `RoundTrip` call creates one attempt;
- request bytes are exactly those read by the underlying RoundTripper;
- response bytes are exactly those read by the adapter, before JSON/SSE decode;
- EOF makes observed request/response evidence exact;
- partial read, early close, decode failure, redirect close, cancellation, and
  transport error preserve observed bytes but mark the relevant body inexact;
- completion performs no additional `Read` or `Close` and does not block waiting
  for an unconsumed body;
- redirect hops and explicit outer retries/fallbacks create separate attempts,
  while `httptrace` connection events do not;
- Go transport decompression means the adapter-visible decompressed stream is
  canonical; the instrumentation does not emulate gzip.

Run:

```sh
go test ./llm/providers/internal/transport -run 'Test.*(Attempt|Body|Redirect|Retry|Gzip|NoDrain)' -count=1
```

Confirm RED because current capture drains/owns responses and carries wire-cycle
and gzip machinery.

**Step 2: Implement observation-only wrappers.**

- Wrap request and response bodies with small observers that append only bytes
  returned to the downstream reader.
- Snapshot current bytes without issuing a read or close. Track exactness only
  when EOF or an existing terminal condition proves completion.
- Set Task 1's request/response inexact input explicitly.
- Remove response-drain callbacks, capture-owned closes, `httptrace` attempt
  splitting, standard-gzip reader ownership, and arbitrary wrapper unwrapping.
- Keep attempt completion tied to existing adapter result paths. A transport
  error with no response is still one failed attempt.

Run focused tests until GREEN, then:

```sh
go test ./llm/providers/internal/transport -count=1
go test -race ./llm/providers/internal/transport -count=1
```

**Step 3: Sanitize final endpoint provenance at durable boundaries.**

Add focused tests proving redirected/final endpoint provenance retains only
scheme, host, and path; userinfo, query, and fragment never reach attempt records
or `agent.ModelCallMeta`. Invalid endpoint text becomes empty rather than being
persisted.

Use one small `llm.SanitizeEndpointURL` helper at provider stamping and again at
the durable session-model-call boundary. Do not change endpoint selection or
fallback behavior.

Run:

```sh
go test ./llm ./agent ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openai ./llm/providers/openaicompat ./llm/providers/kimi -run 'Test.*Endpoint' -count=1
```

**Step 4: Run affected deterministic suites and commit Task 3.**

```sh
go test ./llm/providers/internal/transport ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openai ./llm/providers/openaicompat ./llm/providers/kimi ./agent -count=1
go test -race ./llm/providers/internal/transport ./llm -count=1
git status --short
git add llm/providers/internal/transport llm/providers/anthropic llm/providers/google \
  llm/providers/openai llm/providers/openaicompat llm/providers/kimi \
  llm/api_attempt.go llm/apilog_append_test.go agent/session_model_call.go agent/session_model_call_test.go
git diff --cached --check
git commit -m 'Capture only provider adapter HTTP evidence'
```

### Task 4: Finish explicit consumers and remove obsolete production paths

**Files:**

- Modify: `agent/apilog_read.go`
- Test: `agent/apilog_read_test.go`
- Modify/test only the already-scoped doctor files under `agent/doctor/` and
  `cmd/serf-doctor/`
- Modify/test exact-session deletion under `cmd/serf-hub/`
- Modify: `cmd/serf-hub/spawn.go`
- Test: `cmd/serf-hub/spawn_test.go`
- Modify only compile/truth-field fallout in accepted transcript/grouping/fuzz
  files already on this branch
- Delete obsolete raw API-call carrier or transcript-production code only when
  it is now unused and the binding spec requires removal

**Scope lock:** Reuse the reviewed consumer intent from branch
`wip/p2-thirdwave-consumers`, but do not cherry-pick blindly and do not change
writers, retry/grouping policy, schema ownership, or transport. Correct the
known missing body-truth projection while integrating.

**Step 1: Write failing API-log reader truth tests.**

Prove through the public transcript-reading tool that:

- ordinary transcript reads never open or stat the API log;
- `source="api_log"` is required for API summaries and expansion;
- summaries and explicit request/response expansion project `exact` and
  `credential_values_excluded` from the stored body without inference;
- header expansion preserves ordered names and exact UTF-8/base64 values;
- settlement state is complete and tied to the matching group;
- range, record-count, line-size, metadata, and output-byte bounds remain
  enforced.

Run:

```sh
go test ./agent -run 'Test.*API(Log|Attempt|Transcript.*DoesNotOpen)' -count=1
```

Confirm RED specifically for truth-field projection.

**Step 2: Integrate the minimum reviewed consumer behavior.**

Use `git show` on these commits as review evidence, not as permission to copy
unrelated code:

- `b23aa482b` for bounded attempt metadata and header/settlement expansion;
- `b6f6d9964` for structured doctor settlement evidence;
- `382c6e52a` for exact-session artifact deletion.

Port only hunks matching the binding spec and final Task 1 schema. Add the
missing `Exact` and `CredentialValuesExcluded` projection for both summaries and
body expansion. Doctor must read canonical API records, not transcript API
records, and must not reconstruct body-derived error text.

Run focused tests until GREEN, then:

```sh
go test ./agent ./agent/doctor ./cmd/serf-doctor ./cmd/serf-hub -count=1
```

**Step 3: Write and implement the Hub credential-source test.**

Add a table test proving a nonempty configured `CredentialHeaders` map satisfies
Hub launch credential validation, while ordinary `Headers.Authorization` does
not. Keep API keys and existing supported sources unchanged.

Run RED, implement the smallest condition in `cmd/serf-hub/spawn.go`, then run:

```sh
go test ./cmd/serf-hub -run 'Test.*Credential' -count=1
```

**Step 4: Audit and remove obsolete production paths.**

The following searches must show no production path that writes raw API payloads
to transcripts or maintains an obsolete parallel raw-body carrier:

```sh
rg -n '"api_call"|transcript.*api_call|api_call.*transcript' --glob '*.go' .
rg -n 'RawHTTPBodyError|RawHTTPBodies|ErrorFromHTTPStatusWithRawBodies|NewStreamErrorWithRawBodies|CaptureRawBody' --glob '*.go' .
```

Tests/fixtures may mention rejected legacy shapes only to prove strict failure.
Delete production residue only when unused; do not refactor adjacent code.

**Step 5: Run affected suites and commit Task 4.**

```sh
go test ./agent ./agent/transcript ./agent/doctor ./internal/apptranscript ./cmd/serf-doctor ./cmd/serf-hub ./server -count=1
go test -race ./agent ./internal/apptranscript ./cmd/serf-hub -count=1
go vet ./agent/... ./internal/apptranscript ./cmd/serf-doctor ./cmd/serf-hub ./server
git status --short
git add agent/apilog_read.go agent/apilog_read_test.go agent/doctor cmd/serf-doctor \
  cmd/serf-hub internal/apptranscript agent/transcript server
git diff --cached --check
git commit -m 'Finish explicit bounded API-log consumers'
```

### Task 5: Run the bounded landing gate and close reviews

**No product changes are authorized by this task.** If a gate exposes a bug,
write a failing focused test, make the smallest correction in the owning task's
scope, rerun its focused gate, commit the fix, and then restart this task's
affected gates. Do not add excluded architecture to satisfy a reviewer.

**Step 1: Focused behavioral gate.**

```sh
go test ./llm/apilog ./llm ./llm/providers/internal/transport \
  ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openai \
  ./llm/providers/openaicompat ./llm/providers/kimi ./cmdutil ./cmd/serf \
  ./agent ./agent/transcript ./agent/doctor ./internal/apptranscript \
  ./cmd/serf-doctor ./cmd/serf-hub ./server -count=1
```

**Step 2: Race, vet, lint, and full deterministic tests.**

```sh
go test -race ./llm/apilog ./llm ./llm/providers/internal/transport ./cmdutil ./cmd/serf ./agent ./internal/apptranscript ./cmd/serf-hub -count=1
go vet ./...
make lint
make test
git diff --check
git status --short
```

**Step 3: Supported-platform compilation.**

```sh
GOOS=linux GOARCH=amd64 go test ./llm ./llm/apilog ./llm/providers/internal/transport ./cmdutil ./cmd/serf -run '^$'
```

Do not add or claim Windows support in Project 2.

**Step 4: Inspect one real local artifact pair produced by scripted execution.**

Confirm transcript kinds are semantic only, API-log kinds are only `attempt` and
`settlement`, the Unix API-log mode is `0600`, each completed group has one
settlement, and ordinary transcript read made no API-log access. Do not use live
provider credentials.

**Step 5: Run two bounded independent reviews.**

- Security review: credential sources, structured omission, fixed-pattern
  admission, inert errors/warnings, endpoint sanitization, and body truth only.
- Whole-project review: the exact binding spec and merge-base-to-HEAD diff,
  including all explicit exclusions.

Both reviews must report zero in-scope Critical or Important findings. A
suggestion to implement an explicitly excluded capability is a scope proposal,
not an automatic fix. Any actual in-scope correction receives one focused fix
commit and both affected reviews rerun against the new exact HEAD.

**Step 6: Landing report.**

Report separately:

- local branch/merge state;
- focused/full/race/vet/lint/cross-compile verification state;
- supported macOS/Linux platform state;
- review state;
- push state;
- any separate provider follow-ups discovered but not implemented.
