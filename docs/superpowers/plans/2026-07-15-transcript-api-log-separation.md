# Transcript and API Log Separation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make session transcripts the bounded semantic conversation record and make one private per-session API log the lossless, credential-free record of every provider transport attempt.

**Architecture:** Introduce transcript format version 2 as a hard break: it contains only headers and semantic entries, and assistant turns carry an `attempt_group_id` join key. Replace the existing summary/optional-raw logger with immediate, synchronously appended `api_attempt` records captured at each provider's final HTTP boundary, followed by a separate append-only `attempt_group_settlement` record that declares finality and count without rewriting or delaying an attempt. A shared API-log codec owns durable record/tail decoding, while transcript, Hub, agent-tool, and doctor callers keep their own projections and bounds. Normal transcript readers remain transcript-only; API summaries and byte-bounded exact body expansion require explicit API-log selection.

**Tech Stack:** Go, JSONL, existing `llm` provider adapters, deterministic scripted clients and `httptest`/fake `http.RoundTripper` transports, existing transcript/session/doctor/Hub test harnesses.

## Global Constraints

This spec does not:

- preserve compatibility with legacy mixed transcripts;
- migrate existing session files;
- log credential values;
- put raw provider data in transcript, Hub cold-load, or normal agent context;
- redesign transcript typography;
- add a general blob/content-addressed storage system;
- change provider retry or fallback policy.

Additional constraints:

- Approved program order: this is Project 2 and assumes the delegate-budget truthfulness project has landed and is green before work begins. This is an approved sequencing/preservation gate, not an additional transcript/API-log product requirement.
- Changes in `agent/session_lifecycle.go`, `agent/session.go`, event/transcript paths, and their tests must preserve five-turn steering, typed budget exhaustion, durable `exhausted` job state, retained partial evidence, and every existing projection of those states. Do not reimplement or expand the delegate-budget project in this plan; treat any regression as a failure of this project.
- Treat every requirement outside `docs/superpowers/specs/2026-07-15-transcript-api-log-separation-design.md` as a defect. Stop and ask Jesse instead of implementing it.
- This is an intentional format break. Do not add a legacy reader, legacy writer, migration, dual writer, alias, fallback path, compatibility flag, or translation shim.
- A version-2 transcript contains only `header` and semantic `entry` records. It never contains `api_call`, serialized provider requests/responses, system/tool copies added only for diagnostics, or embedded API-log data.
- The canonical API log is `<state-dir>/sessions/<session-id>.api.jsonl`. It contains one `api_attempt` record for every actual adapter/transport attempt, including retries, endpoint fallbacks, transport failures, provider rejections, timeouts, caller cancellation, decoding failures, and successes.
- API records preserve exact serialized request and raw response bytes using explicit UTF-8/base64 encoding, but never credential values. Capture bytes, method, resolved credential-free endpoint, and final non-secret request headers at the transport boundary; never reconstruct them from `llm.Request`.
- Complete each synchronous attempt append, including the logger's existing configured sync decision, before control can start a retry, endpoint/provider fallback, or group settlement. Never hold a completed attempt in memory waiting for a later finality decision. Record group finality/count in a separate append-only settlement record, and make an attempt-without-settlement after a crash an explicit readable state.
- API-log storage failures are harness/forensic failures, not provider failures. Report them through the logger's warning/failure observer and owning diagnostics; never return, join, wrap, or classify them as the model call's provider error.
- Preserve streaming happens-before: a completed stream attempt is appended before retry/fallback starts or terminal settlement is appended; `Close` waits for all admitted append/sync operations and admits no new records.
- Enforce `0600` on every API-log file. Use `0700` only when this logger creates a new private directory; never chmod a pre-existing shared `sessions` directory.
- Credential exclusion is provider/config-aware, not a fixed header-name denylist. Exclude configured credential header values and names, URL userinfo, credential query values/keys, and credentials echoed into persisted error text while preserving every non-secret header name/value exactly.
- Distinguish caller-owned cancellation/deadline from adapter/provider-owned timeouts using the preserved parent and derived attempt contexts. Explicit caller cancel and caller deadline are `caller_cancellation`; adapter deadline, response-header timeout, and SSE read timeout are `provider_timeout`.
- Keep provider failures in the API log. Keep harness, session, sandbox, notification, and Hub faults in their existing owning stores. Do not relabel them as provider failures.
- The default `read_session_transcript` path must not open, stat, scan, or preload an API log. API-log access requires `source="api_log"` or an explicit `attempt_id` expansion.
- API log files must be mode `0600`; directories newly created by this logger must be `0700`, while pre-existing shared directories keep their mode. Preserve append, `fsync`, partial-tail tolerance, and bounded-read behavior.
- Before changing tests, reread `docs/testing.md`. Default tests must use scripted clients, `httptest`, or fake transports; no provider credential, network, quota, timing luck, or current model behavior may affect `go test ./...` or `make test`.
- Tests must assert behavior and structured records. Do not test generated JSON, scripts, commands, HTML, or large strings with broad regular expressions.
- After each task, run `git status --short`, stage only the named files, and commit with the detailed message shown. Never use `git add -A`.
- Known code conflict: `cmd/serf-hub/internal/hubcore/wedge.go` currently infers a wedged session from a failed transcript `api_call` tail. Do not preserve that heuristic by reading the API log during Hub cold-load. Remove the transcript dependency in Task 7. If current owning session/status state cannot represent the signal, stop and report that separate behavior gap to Jesse; do not invent a sidecar, compatibility record, or provider-log cold-load path in this change.

---

## Task 1: Define the hard transcript-v2 boundary and semantic join key

**Files:**
- Modify: `agent/schema/turn.go`
- Modify: `agent/transcript/transcript.go`
- Modify: `agent/transcript_read.go`
- Modify: `agent/fork.go`
- Modify: `agent/doctor/transcript.go`
- Modify: `internal/apptranscript/apptranscript.go`
- Modify: `internal/apptranscript/turn_cache.go`
- Modify: `internal/apptranscript/turn_index.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/image_serve.go`
- Test: `agent/transcript_test.go`
- Test: `agent/s4cov_transcript_read_test.go`
- Test: `agent/cov_s2_fork_test.go`
- Test: `agent/doctor/transcript_test.go`
- Test: `agent/doctor/transcript_load_fuzz_test.go`
- Test: `internal/apptranscript/apptranscript_test.go`
- Test: `internal/apptranscript/turn_cache_test.go`
- Test: `internal/apptranscript/turn_index_test.go`
- Test: `internal/apptranscript/projectturn_fuzz_test.go`
- Test: `cmd/serf-hub/app_threadread_test.go`
- Test: `cmd/serf-hub/app_rpc_test.go`
- Test: `cmd/serf-hub/web_test.go`

**Interfaces:**

```go
package transcript

const FormatVersion = 2

var ErrUnsupportedFormat = errors.New("unsupported transcript format")

type Header struct {
    Kind          string `json:"kind"`
    FormatVersion int    `json:"format_version"`
    // existing fields remain unchanged
}
```

```go
package schema

type Turn struct {
    // existing semantic fields remain unchanged
    AttemptGroupID string `json:"attempt_group_id,omitempty"`
}
```

`transcript.OpenWriter`, `readTranscript`, `readTranscriptFull`, the strict child reader, `fork`, doctor's `loadTranscript`, `internal/apptranscript`, and every Hub cold-load/image reader must all apply the same rules:

1. Require exactly one first non-empty `header` with `format_version: 2`.
2. Accept only subsequent `entry` records.
3. Return an error wrapping `transcript.ErrUnsupportedFormat` for version 1, a missing/zero format version, any `api_call`, or any other recognized legacy record kind.
4. Preserve the existing distinction between an incomplete final line and corrupt interior data; only an incomplete final line may be ignored/truncated.

Cut over the real application reader interfaces so failure cannot be hidden by an empty result:

```go
func ScanPrelude(path string, maxLineBytes int) (transcript.Header, error)
func TurnsFromFile(path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error)
func (c *TurnCache) TurnsFromFile(path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error)
func (c *TurnCache) LatestFromFile(path string, maxLineBytes int, limit int, project BoundedEntryProjector) (turns []appwire.Turn, olderCursor string, err error)
func (c *TurnCache) PageFromFile(path string, maxLineBytes int, cursor string, limit int, project BoundedEntryProjector) (FilePage, error)
func (c *TurnCache) TurnCountFromFile(path string, maxLineBytes int, project BoundedEntryProjector) (int, error)
```

Remove `PreludeTurn`'s `*transcript.APICall` input and remove `FirstCall`, failed-API-call nodes, `api_call` visibility, and API-derived tool seeds from turn-index/cache disk and journal records. Include `transcript.FormatVersion` in the turn-index/cache integrity stamp. On v1, missing-version, or mixed input: invalidate/delete the affected in-memory and disk index/cache entry, return an error wrapping `transcript.ErrUnsupportedFormat`, and do not retain a partial projection. Never consult an API log to reconstruct the removed prelude, failure turn, tool seed, image, or turn count.

- [ ] **Step 1: Add failing transcript format tests.**

Add table-driven behavioral tests that create actual JSONL files for:

```go
tests := []struct {
    name string
    body string
}{
    {"version one", `{"kind":"header","format_version":1}` + "\n"},
    {"missing version", `{"kind":"header"}` + "\n"},
    {"mixed api call", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"api_call"}` + "\n"},
}
```

Assert `errors.Is(err, transcript.ErrUnsupportedFormat)` through the writer reopen path, all three session reader paths, fork, doctor, `ScanPrelude`, direct `TurnsFromFile`, turn cache/page/count, Hub past-thread cold-load, and `/s/<id>/images/<sha>`. Add a positive v2 file with a user entry and an assistant entry carrying `attempt_group_id: "ag_test"` and assert exact round-trip preservation, cold-load projection, and transcript-backed image fetch.

Prime both the in-memory turn cache and persisted turn-index sidecars from a valid v2 file, replace the transcript with v1 and mixed variants, then assert the next read invalidates both caches and returns `ErrUnsupportedFormat`. Place a sibling API log containing otherwise usable request/response/image sentinels and assert no Hub path opens or returns them.

- [ ] **Step 2: Run the focused tests and confirm RED.**

Run:

```bash
go test ./agent ./agent/transcript ./agent/doctor ./internal/apptranscript ./cmd/serf-hub -run 'Transcript|Fork|LoadTranscript|ScanPrelude|TurnCache|TurnIndex|SessionImage'
```

Expected: failures because version 1/missing-version/mixed files are currently accepted, skipped, indexed, or projected, API-bearing cache fields still compile, and `Turn.AttemptGroupID` does not compile.

- [ ] **Step 3: Implement transcript v2 and delete the mixed-record API.**

Set `FormatVersion: transcript.FormatVersion` in `NewWriter`. Validate the header before resuming a writer or returning entries. Delete `transcript.APICall`, `Writer.AppendAPICall`, `transcriptData.APICalls`, and `apiLines`; delete all reader branches that accept or silently skip `api_call`. Keep sequence allocation based only on semantic entries.

Use one small helper for all transcript readers, including `internal/apptranscript`, rather than reintroducing subtly different format acceptance:

```go
func ValidateHeader(h Header) error {
    if h.Kind != "header" || h.FormatVersion != FormatVersion {
        return fmt.Errorf("%w: require transcript format_version %d", ErrUnsupportedFormat, FormatVersion)
    }
    return nil
}

func ValidateRecordKind(kind string) error {
    if kind != "entry" {
        return fmt.Errorf("%w: record kind %q is not valid in transcript format %d", ErrUnsupportedFormat, kind, FormatVersion)
    }
    return nil
}
```

Do not add a version switch with a version-1 arm.

- [ ] **Step 4: Update affected transcript/fork/doctor tests to author v2 semantic fixtures.**

Remove tests whose contract is APICall round-trip, API-derived prelude/failure turns, API-bearing turn indices, or silent `api_call` skipping. Replace them with rejection, cache invalidation, and semantic-turn contracts above. Propagate the new reader errors through Hub thread-read and image responses instead of returning an empty/partial thread. Preserve unrelated coverage for truncation, corruption, sequence, resume history, structured content, and semantic image replay. Update fuzz seeds so legacy mixed input is an unsupported-format result, not a successful round trip.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

Run:

```bash
go test ./agent/transcript ./agent/doctor ./agent ./internal/apptranscript ./cmd/serf-hub -run 'Transcript|Fork|LoadTranscript|ScanPrelude|TurnCache|TurnIndex|SessionImage'
```

Expected: `ok` for all packages; no reader, cache, index, Hub cold-load, or image path accepts or retains a mixed transcript.

- [ ] **Step 6: Commit the hard format boundary.**

```bash
git status --short
git add agent/schema/turn.go agent/transcript/transcript.go agent/transcript_read.go agent/fork.go agent/doctor/transcript.go internal/apptranscript/apptranscript.go internal/apptranscript/turn_cache.go internal/apptranscript/turn_index.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_rpc.go cmd/serf-hub/image_serve.go agent/transcript_test.go agent/s4cov_transcript_read_test.go agent/cov_s2_fork_test.go agent/doctor/transcript_test.go agent/doctor/transcript_load_fuzz_test.go internal/apptranscript/apptranscript_test.go internal/apptranscript/turn_cache_test.go internal/apptranscript/turn_index_test.go internal/apptranscript/projectturn_fuzz_test.go cmd/serf-hub/app_threadread_test.go cmd/serf-hub/app_rpc_test.go cmd/serf-hub/web_test.go
git commit -m "Break transcripts from provider API records

Introduce transcript format version 2 as a semantic-only record stream. Reject legacy mixed transcripts instead of translating or skipping API records, and add the attempt-group join key to semantic turns.

This implements the hard-format boundary in the 2026-07-15 transcript/API-log separation design."
```

## Task 2: Replace summary/raw split logging with one private lossless API record

**Files:**
- Modify: `identifier/domains.go`
- Modify: `identifier/domains_test.go`
- Add: `llm/apilog/record.go`
- Add: `llm/apilog/body.go`
- Add: `llm/apilog/codec.go`
- Add: `llm/apilog/record_test.go`
- Add: `llm/apilog/codec_test.go`
- Rewrite in place: `llm/apilog.go`
- Modify: `llm/apilog_test.go`
- Modify: `llm/apilog_session_test.go`
- Modify: `llm/apilog_session_write_fuzz_test.go`
- Modify: `llm/apilog_write_fuzz_test.go`
- Modify: `llm/apilog_edges_fuzz_test.go`
- Modify: `cmdutil/api_logging.go`
- Modify: `cmdutil/api_logging_test.go`

**Interfaces:**

```go
package identifier

func NewAPIAttemptID() (string, error)       { return newDomainID("att_") }
func ValidateAPIAttemptID(v string) error    { return validateDomainID(v, "att_") }
func MustNewAPIAttemptID() string            { return mustDomainID(NewAPIAttemptID) }
```

```go
package apilog

type BodyEncoding string

const (
    BodyUTF8   BodyEncoding = "utf8"
    BodyBase64 BodyEncoding = "base64"
)

type EncodedBody struct {
    Encoding  BodyEncoding `json:"encoding"`
    Data      string       `json:"data"`
    ByteCount int          `json:"byte_count"`
}

type AttemptOutcomeClass string

const (
    AttemptSuccess         AttemptOutcomeClass = "success"
    AttemptProviderReject  AttemptOutcomeClass = "provider_rejection"
    AttemptTransportFail   AttemptOutcomeClass = "transport_failure"
    AttemptProviderTimeout AttemptOutcomeClass = "provider_timeout"
    AttemptCallerCancel    AttemptOutcomeClass = "caller_cancellation"
    AttemptDecodeFail      AttemptOutcomeClass = "response_decoding_failure"
)

type APIAttemptRequest struct {
    Method         string              `json:"method"`
    Endpoint       string              `json:"endpoint"`
    Headers        map[string][]string `json:"headers,omitempty"`
    Body           EncodedBody         `json:"body"`
    Model          string              `json:"model,omitempty"`
    HistoryMode    string              `json:"history_mode,omitempty"`
    EndpointFamily string              `json:"endpoint_family,omitempty"`
}

type APIAttemptResponse struct {
    StatusCode    int         `json:"status_code,omitempty"`
    Body          EncodedBody `json:"body"`
    Model         string      `json:"model,omitempty"`
    FinishReason  string      `json:"finish_reason,omitempty"`
    Usage         Usage       `json:"usage,omitempty"`
}

type Usage struct {
    InputTokens      int  `json:"input_tokens,omitempty"`
    OutputTokens     int  `json:"output_tokens,omitempty"`
    TotalTokens      int  `json:"total_tokens,omitempty"`
    CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
    CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

type APIAttemptRecord struct {
    Kind             string              `json:"kind"` // api_attempt
    SchemaVersion    int                 `json:"schema_version"` // 1
    AttemptID        string              `json:"attempt_id"`
    AttemptGroupID   string              `json:"attempt_group_id"`
    AttemptIndex     int                 `json:"attempt_index"` // 1-based
    Timestamp        time.Time           `json:"timestamp"`
    LatencyMS        int64               `json:"latency_ms"`
    ProviderInstance string              `json:"provider_instance"`
    RequestModel     string              `json:"request_model"`
    Request          APIAttemptRequest   `json:"request"`
    Response         *APIAttemptResponse `json:"response,omitempty"`
    Outcome          AttemptOutcomeClass `json:"outcome"`
    ErrorClass       string              `json:"error_class,omitempty"`
    ErrorMessage     string              `json:"error_message,omitempty"`
}

type APIAttemptGroupSettlement struct {
    Kind              string              `json:"kind"` // attempt_group_settlement
    SchemaVersion     int                 `json:"schema_version"` // 1
    AttemptGroupID    string              `json:"attempt_group_id"`
    FinalAttemptID    string              `json:"final_attempt_id,omitempty"`
    FinalAttemptCount int                 `json:"final_attempt_count"`
    Outcome           AttemptOutcomeClass `json:"outcome"`
    ForensicIncomplete bool               `json:"forensic_incomplete,omitempty"`
    SettledAt         time.Time           `json:"settled_at"`
}

type APILogRecord interface {
    RecordKind() string
    validateRecord() error
}

var ErrPartialTail = errors.New("partial API-log tail")

func (APIAttemptRecord) RecordKind() string
func (APIAttemptGroupSettlement) RecordKind() string
func DecodeRecord(line []byte) (APILogRecord, error)
func NewDecoder(r io.Reader, maxLineBytes int) *Decoder
func (d *Decoder) Next() (APILogRecord, error) // io.EOF or ErrPartialTail at end
```

`EncodeBody` must use UTF-8 only when `utf8.Valid(data)`; otherwise base64-encode the original bytes. `DecodeBody(EncodeBody(b))` must equal `b` for every byte slice.

`llm/apilog` is the only durable on-disk codec. Its primitive concrete record structs are the top-level JSONL objects; `APILogRecord` is the durable-format interface they implement, and `DecodeRecord` peeks at `kind`, decodes the matching concrete type, validates it, and returns the interface for a consumer type-switch. The package owns record kinds, validation, per-line decoding, line/offset accounting, and partial-final-line detection. It does not import parent package `llm` and does not know clients, sessions, routing, sync cadence, transcript refs, pagination, rendering, doctor rows, or agent tool envelopes; those remain in parent `llm` or the caller.

The logger has one destination only. Delete `APILogEntry`, `APIRawLogEntry`, `RawBodyEnabled`, `EnableRawLogging`, `EnableSessionRawLogging`, `BuildAPILogRequest`, and the summary/raw dual-write path. Keep per-session routing and sync controls:

```go
type APIAttemptSink interface {
    AppendAttempt(context.Context, apilog.APIAttemptRecord) error
    AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error
}

type APILogFailure struct {
    Operation      string
    SessionID      string
    AttemptGroupID string
    AttemptID      string
    Err            error
}

func NewSessionAPILogger(stateDir string) (*APILogger, error)
func (l *APILogger) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error
func (l *APILogger) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error
func (l *APILogger) SetFailureObserver(func(APILogFailure))
func (l *APILogger) Middleware() Middleware
func (l *APILogger) Close() error
```

- [ ] **Step 1: Add failing record fidelity, uniqueness, permission, and crash-tail tests.**

Cover:

- escaped valid UTF-8 (`[]byte("{\"text\":\"line\\nquote\\\"\"}")`) remains exact after JSONL marshal/unmarshal/decode;
- binary bytes (`[]byte{0x00, 0xff, 0x80, '\n'}`) use base64 and round-trip exactly;
- 1,000 generated `att_` identifiers are valid and unique;
- each `<sid>.api.jsonl` file is `0600`; every directory newly created by this logger is `0700`; an existing state directory and an existing shared `sessions` directory each retain their exact mode, including the case where only the missing `sessions` child is created;
- append plus `Close` survives reopen; a partial final JSON line is ignored by a reader but valid preceding attempts remain available;
- `apilog.Decoder` returns the `apilog.APILogRecord` interface implemented by top-level attempt and settlement records, rejects unknown/interior-corrupt records, and distinguishes `ErrPartialTail` from `io.EOF`; consumers type-switch on `apilog.APIAttemptRecord` and `apilog.APIAttemptGroupSettlement`;
- there is no `api-raw.jsonl` sibling and no environment toggle is required for exact bodies.

- [ ] **Step 2: Run focused tests and confirm RED.**

```bash
go test ./identifier ./llm/apilog ./llm ./cmdutil -run 'APIAttempt|Settlement|APILog|AttachAPILogger|EncodedBody|GeneratedID|PartialTail'
```

Expected: compile failures for the shared record/decoder types and behavioral failures because current files use `0644`, raw bodies are optional/separate, and no settlement record exists.

- [ ] **Step 3: Implement the single canonical append-only writer.**

Stat the parent before creation. If this logger creates the directory, create it with `0700`; if it already exists, leave its mode unchanged. Open API files with `0600` and call `file.Chmod(0o600)` so an existing permissive API file is repaired. Marshal one `apilog.APIAttemptRecord` or `apilog.APIAttemptGroupSettlement` per line under the existing mutex and preserve current sync semantics.

Append/sync errors return from the low-level sink to the coordinator, which immediately invokes `APILogFailure`'s observer. Middleware must then return the provider response/error unchanged. `cmdutil.AttachAPILogger` wires the existing `warnings io.Writer` to a structured one-line forensic warning containing operation/session/group/attempt identifiers and the sanitized storage error; it never disables the client or turns the warning into an LLM error. `Close` may return its own shutdown error to the process shutdown path, but it must not retroactively alter a completed provider call.

`cmdutil.AttachAPILogger` must always attach this logger and must not inspect `SERF_LOG_RAW_HTTP`.

- [ ] **Step 4: Remove summary/raw fixtures and update deterministic fuzz programs.**

Keep fuzz coverage for arbitrary structured requests/responses, settlements, append failures, per-session routing, concurrent writes, and partial tails, but assert canonical typed entries and lossless encoded bytes. Do not retain obsolete raw-enable callbacks to preserve fuzz coverage.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

```bash
go test ./identifier ./llm/apilog ./llm ./cmdutil -run 'APIAttempt|Settlement|APILog|AttachAPILogger|EncodedBody|GeneratedID|PartialTail'
```

Expected: `ok`; exact bodies are present without environment opt-in, API files are `0600`, logger-created directories are private, and pre-existing shared directory modes are unchanged.

- [ ] **Step 6: Commit the canonical storage format.**

```bash
git status --short
git add identifier/domains.go identifier/domains_test.go llm/apilog/record.go llm/apilog/body.go llm/apilog/codec.go llm/apilog/record_test.go llm/apilog/codec_test.go llm/apilog.go llm/apilog_test.go llm/apilog_session_test.go llm/apilog_session_write_fuzz_test.go llm/apilog_write_fuzz_test.go llm/apilog_edges_fuzz_test.go cmdutil/api_logging.go cmdutil/api_logging_test.go
git commit -m "Make the per-session API log lossless and private

Replace the split summary and optional raw logs with one typed append-only API log. Preserve exact UTF-8 or binary bodies, add explicit group settlements, assign globally unique attempt IDs, and enforce private API-file permissions without mutating shared directory modes."
```

## Task 3: Append every attempt immediately and settle groups append-only

**Files:**
- Modify: `llm/apilog.go`
- Modify: `llm/retry_util.go`
- Modify: `llm/stream_retry.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_stream.go`
- Test: `llm/apilog_test.go`
- Test: `llm/retry_util_test.go`
- Test: `llm/stream_retry_test.go`
- Test: `agent/session_model_call_phase8_test.go`
- Test: `agent/session_model_test.go`

**Interfaces:**

Create one group before the first model attempt and carry it through ordinary retries, streaming retries, continuation fallback, endpoint fallback, and configured model fallback:

```go
type APIAttemptGroup struct {
    ID string
    // unexported mutex, next 1-based index, final attempt ID/count,
    // settlement guard, sink, and APILogFailure observer
}

func NewAPIAttemptGroup(id string) *APIAttemptGroup
func WithAPIAttemptGroup(ctx context.Context, group *APIAttemptGroup) context.Context
func WithAPIAttemptSink(ctx context.Context, sink APIAttemptSink) context.Context
func BeginAPIAttempt(ctx context.Context, meta APIAttemptMeta) *APIAttempt
func (a *APIAttempt) Complete(result APIAttemptResult)
func (g *APIAttemptGroup) Settle(ctx context.Context, outcome apilog.AttemptOutcomeClass)
```

`BeginAPIAttempt` assigns a new `att_` identifier and the next index. `APIAttempt.Complete` constructs one `apilog.APIAttemptRecord` and synchronously calls `AppendAttempt` before it returns to the adapter, retry helper, fallback selector, or stream caller. It never holds the completed record for later. On append/sync failure it invokes the `APILogFailure` observer before returning, but it does not alter the provider response/error.

`Settle`, called exactly once by the outer `callModelWithFallback` after success/error/cancellation and after the final attempt append completes, synchronously appends one `apilog.APIAttemptGroupSettlement` with `AttemptGroupID`, `FinalAttemptID`, `FinalAttemptCount`, the outer outcome, `ForensicIncomplete`, and `SettledAt`. The group sets `ForensicIncomplete` when any earlier attempt append/sync failed. It never rewrites an attempt. If no transport attempt began, settlement records count 0 and an empty final ID so the no-attempt outer outcome remains explicit. A process crash or injected failure between an attempt append and settlement leaves the completed attempt readable as an unsettled group; this is required, not repaired by a reader. If settlement itself cannot be appended, the owning `APILogFailure` warning plus the missing settlement marks incompleteness.

`APILogger.Middleware` must create, attach, and settle an implicit one-call group when its caller supplied no group; this preserves logging for compaction and direct `Client.Complete`/`Client.Stream` callers. When a session-supplied group is present, middleware attaches only the sink and leaves settlement to `callModelWithFallback`.

Keep `Retry` and `RetryStream` selection/backoff semantics unchanged. Their only new responsibility is to reuse the group context when invoking the next attempt; no error becomes retryable or non-retryable because of logging.

- [ ] **Step 1: Add failing attempt-sequence tests.**

Use scripted attempt functions, no network:

1. failure then success yields `api_attempt(1)`, `api_attempt(2)`, then `attempt_group_settlement(final_attempt_id=2, final_attempt_count=2, success)` in that exact append order;
2. two failures ending in outer-call failure preserve both attempt outcomes, then one failure settlement naming attempt 2;
3. caller cancellation before transport writes only a count-0 settlement and no attempt;
4. cancellation during transport appends one `caller_cancellation` attempt before its matching settlement;
5. streaming retry and non-stream retry produce the same group/index/settlement contract;
6. continuation endpoint fallback plus configured provider fallback share the original group and increase indices rather than starting over;
7. injected process-stop/failing-sink windows after attempt 1 and after the final attempt leave each successfully appended attempt decodable even though no settlement exists;
8. an attempt-append or settlement-append failure calls the forensic observer with operation/group/attempt identity while the returned provider response/error and retry classification remain byte-for-byte/type-for-type unchanged.

Assert retry invocation counts and selected providers remain identical to pre-change expectations.

- [ ] **Step 2: Run the sequence tests and confirm RED.**

```bash
go test ./llm/apilog ./llm ./agent -run 'AttemptGroup|Settlement|CrashWindow|Retry.*APILog|Fallback.*Attempt|Cancellation.*Attempt|APILogStorageFailure'
```

Expected: current logs reuse index 1/count 1, endpoint attempts are only partially grouped, there is no settlement record, and failed logging is either silent or entangled with the call boundary.

- [ ] **Step 3: Implement the group coordinator and thread it through existing retry paths.**

Replace `modelAttemptRecorder`'s separate counter with `APIAttemptGroup`. Keep `ModelAttemptMetadata.AttemptGroupID` as the semantic join value, but delete `AdapterAttempts` after the transcript writer no longer consumes it. `APILogger.Middleware` supplies the sink and failure observer before it invokes the provider; this keeps sessions that have no attached logger valid while letting the group span every middleware invocation. Add one outer settlement defer whose outcome is derived from the existing final response/error without changing that error:

```go
func (s *Session) callModelWithFallback(/* existing args */) (modelResp sessionModelResponse, usedReq llm.Request, meta ModelAttemptMetadata, err error) {
    group := llm.NewAPIAttemptGroup(identifier.MustNewAgentCallID())
    ctx = llm.WithAPIAttemptGroup(ctx, group)
    defer func() { group.Settle(ctx, llm.APIAttemptOutcomeForOuterResult(modelResp.Response, err)) }()
    // existing retry/fallback selection follows unchanged
}
```

`APIAttemptOutcomeForOuterResult` only selects the settlement outcome already established by the adapter result; it does not reclassify errors. Do not return an API-log append error, call `errors.Join`, or create a new group in `Retry`, `RetryStream`, an adapter fallback, or a configured-provider fallback.

- [ ] **Step 4: Run sequence tests and confirm GREEN.**

```bash
go test ./llm/apilog ./llm ./agent -run 'AttemptGroup|Settlement|CrashWindow|Retry.*APILog|Fallback.*Attempt|Cancellation.*Attempt|APILogStorageFailure'
```

Expected: `ok`; every completed attempt is readable before the next action, group finality is a later settlement, storage failures remain forensic warnings, and retry/fallback behavior is unchanged.

- [ ] **Step 5: Commit attempt ownership.**

```bash
git status --short
git add llm/apilog.go llm/retry_util.go llm/stream_retry.go agent/session_model_call.go agent/session_stream.go llm/apilog_test.go llm/retry_util_test.go llm/stream_retry_test.go agent/session_model_call_phase8_test.go agent/session_model_test.go
git commit -m "Track each provider attempt in one logical group

Allocate stable group and unique attempt identifiers across retry, streaming, continuation, endpoint, and provider fallback paths. Append each completed attempt synchronously, then append one group settlement for finality/count without rewriting, delaying, or changing provider behavior."
```

## Task 4: Capture exact wire attempts in every core HTTP adapter

**Files:**
- Modify: `llm/providercfg/providercfg.go`
- Modify: `llm/providercfg/load.go`
- Modify: `llm/providercfg/materialize.go`
- Modify: `llm/providercfg/mutate.go`
- Modify: `llm/providercfg/headers_test.go`
- Modify: `llm/providercfg/materialize_test.go`
- Modify: `llm/providercfg/mutate_test.go`
- Modify: `llm/providercfg/persistence_fuzz_test.go`
- Modify: `llm/providers_config.go`
- Modify: `llm/providers_config_test.go`
- Modify: `llm/headers_test.go`
- Modify: `llm/providers/internal/transport/runner.go`
- Modify: `llm/providers/internal/transport/runner_test.go`
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/responses.go`
- Modify: `llm/providers/openai/chatcompletions.go`
- Modify: `llm/providers/openai/adapter_test.go`
- Modify: `llm/providers/anthropic/adapter.go`
- Modify: `llm/providers/anthropic/adapter_test.go`
- Modify: `llm/providers/google/adapter.go`
- Modify: `llm/providers/google/adapter_test.go`
- Modify: `llm/providers/openaicompat/adapter.go`
- Modify: `llm/providers/openaicompat/adapter_test.go`
- Modify: `llm/providers/openaicompat/openrouter_stream_capture_test.go`
- Modify: `llm/providers/glm/adapter.go`
- Modify: `llm/providers/glm/adapter_test.go`
- Modify: `llm/providers/kimi/adapter.go`
- Modify: `llm/providers/kimi/adapter_test.go`
- Modify: `llm/providers/ollama/adapter.go`
- Modify: `llm/providers/ollama/adapter_test.go`
- Modify: `llm/providers/openrouter/adapter.go`
- Modify: `llm/providers/openrouter/adapter_test.go`
- Modify: `llm/providers/openrouter_anthropic/adapter.go`
- Modify: `llm/providers/openrouter_anthropic/adapter_test.go`
- Modify: `llm/providers/kimi_anthropic/adapter.go`
- Modify: `llm/providers/kimi_anthropic/adapter_test.go`
- Modify: `llm/providers/minimax/adapter.go`
- Modify: `llm/providers/minimax/adapter_test.go`
- Modify: `llm/adapter_timeout.go`
- Modify: `llm/adapter_timeout_test.go`
- Modify: `llm/provider_error_raw_logging_test.go`
- Modify: `llm/sdk_errors.go`
- Modify: `llm/types.go`
- Modify: `docs/llm-provider-config-and-launch.md`

**Interfaces:**

```go
package providercfg

type InstanceConfig struct {
    // existing fields
    Headers           map[string]string `toml:"headers"`            // explicitly non-secret
    CredentialHeaders map[string]string `toml:"credential_headers"` // secret names and values
}
```

The provider config source of truth is explicit:

```toml
[instances.gateway]
headers = { "X-Trace-Label" = "team-a" }
credential_headers = { "X-Gateway-Key" = "$GATEWAY_KEY" }
```

`Headers` remains fully logged as non-secret custom header data unless a common credential-name defense-in-depth rule catches an obvious auth header. `CredentialHeaders` is the only config mechanism for arbitrary secret header names/values. Both maps use the existing `$ENV`/`${ENV}`/`$$` resolution at provider construction, but materialization keeps the authored expression on disk and passes the resolved credential header names/values separately to the adapter. Validation canonicalizes names case-insensitively and rejects a name duplicated within or across `headers` and `credential_headers`; it does not silently move, hide, or reinterpret an existing `headers` entry. Do not add a migration, compatibility alias, inferred-secret rule for all custom headers, UI redesign, or new credential store.

Every adapter instance/factory parameter that currently carries `Headers map[string]string` gains a parallel `CredentialHeaders map[string]string`. Provider factories pass both maps without merging them. The transport applies both maps to the request, while `APILogCredentialMaterial` receives all credential-header names/values and only the non-secret `Headers` remain eligible for exact logging.

```go
type APIAttemptMeta struct {
    ProviderInstance string
    RequestModel     string
    HistoryMode      HistoryMode
    EndpointFamily   string
    Method           string
    Endpoint         string
    Headers          http.Header
    RequestBody      []byte
    StartedAt        time.Time
    CredentialMaterial APILogCredentialMaterial
}

type APIAttemptResult struct {
    StatusCode   int
    ResponseBody []byte
    Response     *Response
    Outcome      apilog.AttemptOutcomeClass
    ErrorClass   string
    Err          error
    FinishedAt   time.Time
}
```

Add provider/config-aware credential filtering at this boundary:

```go
type APILogCredentialMaterial struct {
    HeaderNames map[string]struct{}
    QueryNames  map[string]struct{}
    Values      []string
}

func NewAPILogCredentialMaterial(headerNames, queryNames []string, values ...string) APILogCredentialMaterial
func SanitizeRequestForAPILog(req *http.Request, material APILogCredentialMaterial) (endpoint string, headers map[string][]string)
func SanitizeErrorForAPILog(text string, material APILogCredentialMaterial) string
```

Each adapter constructs `APILogCredentialMaterial` from the credential sources actually selected for that provider instance: built-in auth, OAuth/bearer token, configured API key, every resolved `CredentialHeaders` name/value, URL userinfo, and provider-specific credential query key/value. The sanitizer clones the final request, removes URL userinfo, removes marked query keys and query values equal to credential material, omits marked headers and headers whose values contain credential material, and copies every remaining header name/value exactly after all adapter headers are set. `SanitizeErrorForAPILog` replaces every non-empty raw and URL-escaped credential value before `ErrorMessage` or an `APILogFailure.Err` reaches JSONL/warnings. A common-name denylist remains defense in depth, but it is not the source of truth and cannot replace provider/config-derived names and values or hide every custom `Headers` entry.

Preserve context ownership explicitly:

```go
type APITimeoutSource string

const (
    APITimeoutNone           APITimeoutSource = ""
    APITimeoutAdapter        APITimeoutSource = "adapter_deadline"
    APITimeoutResponseHeader APITimeoutSource = "response_header_timeout"
    APITimeoutSSERead        APITimeoutSource = "sse_read_timeout"
)

type APIAttemptContextOwnership struct {
    Parent        context.Context
    Attempt       context.Context
    TimeoutSource APITimeoutSource
}

func ClassifyAPIAttemptOutcome(owner APIAttemptContextOwnership, statusCode int, decodeErr, transportErr error) apilog.AttemptOutcomeClass
```

Classification checks `owner.Parent.Err()` first: explicit parent cancel and parent deadline are `caller_cancellation`. Only a derived adapter deadline or an explicitly identified response-header/SSE-read timeout is `provider_timeout`. Other round-trip failures are `transport_failure`; HTTP non-2xx is `provider_rejection`; HTTP success decode failure is `response_decoding_failure`; decoded completion is `success`.

Always capture response bytes used by JSON/SSE decoders; the logger's presence decides whether they are retained after the call, not an environment variable. Classify at the adapter boundary where the cause is known:

- HTTP non-2xx with a response body -> `provider_rejection`;
- request/round-trip error -> the context-ownership classifier above;
- HTTP success whose JSON/SSE body cannot be decoded -> `response_decoding_failure`;
- decoded completion -> `success`.

- [ ] **Step 1: Add failing fake-transport contract tests for all four core adapters.**

First add provider-config tests that load, validate, marshal, mutate, materialize, and resolve a fixture containing both maps. Assert:

- `headers.X-Trace-Label = "team-a"` round-trips and reaches the adapter as non-secret `Headers`;
- `credential_headers.X-Gateway-Key = "$GATEWAY_KEY"` persists as the authored reference, resolves only in the runtime `InstanceConfig`, and reaches the adapter separately as `CredentialHeaders{"X-Gateway-Key":"secret-sentinel"}`;
- case-insensitive duplicates within or across the two maps fail with the instance and both source fields named;
- `TestWriteFilePreservesAuthoredCredentialHeaders` calls `WriteFile` with a runtime config containing the resolved `secret-sentinel`, then proves the existing authored `credential_headers.X-Gateway-Key = "$GATEWAY_KEY"` expression survives and the resolved value is never written;
- an ordinary custom `Headers{"X-Customer":"visible"}` value appears exactly in the API attempt record, while the sibling credential header never appears.

For OpenAI, Anthropic, Google, and OpenAI-compatible, use `httptest.Server` or a fake `RoundTripper` to return deterministic success, non-2xx, malformed JSON/SSE, binary body, timeout, and caller cancellation cases. Assert the recorded request bytes equal the bytes received by the fake server, the recorded response bytes equal what it sent, the method and sanitized resolved endpoint are exact, selected non-secret headers survive byte-for-byte, and unique credential sentinels do not occur anywhere in marshaled `apilog.APIAttemptRecord`, `apilog.APIAttemptGroupSettlement`, storage-warning, or persisted error JSON.

For every core adapter and each wrapper factory listed in this task, include credentials in: a standard auth header, an arbitrary configured `CredentialHeaders` entry, URL username/password, a provider query parameter, and an error string that echoes the final URL/header. Include a non-secret `Headers` entry adjacent to them and assert its exact values/order are retained. The test must fail if credential safety depends only on a common-name denylist or if the implementation hides all custom headers.

Add a context-classification table with five independent cases: explicit caller cancel, caller deadline, derived adapter deadline, response-header timeout, and SSE read timeout. Assert the first two are `caller_cancellation` and the latter three are `provider_timeout`, without changing whether the existing retry policy retries them.

Include both complete and stream calls. Exercise OpenAI Responses-to-Chat fallback and OpenAI-compatible adaptive endpoint fallback; assert each actual HTTP request becomes its own indexed record.

Add a blocking fake sink that records method-entry/method-return events. Assert this happens-before sequence for stream success, midstream retry, cancellation, and endpoint fallback:

```text
transport/decoder finishes -> AppendAttempt enters -> AppendAttempt returns ->
retry or fallback may begin OR AppendSettlement enters
```

For `APILogger.Close`, start an admitted blocked append, call `Close`, prove `Close` blocks until append/sync returns, then prove a later append is rejected through `APILogFailure` without a write and without changing the provider result. Settlement must never appear before its final attempt append returns.

- [ ] **Step 2: Run adapter tests and confirm RED.**

```bash
go test ./llm/providercfg ./llm/providers/internal/transport ./llm/providers/openai ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openaicompat ./llm/providers/glm ./llm/providers/kimi ./llm/providers/ollama ./llm/providers/openrouter ./llm/providers/openrouter_anthropic ./llm/providers/kimi_anthropic ./llm/providers/minimax ./llm -run 'Headers|CredentialHeaders|APIAttempt|WireCapture|Credential|Outcome|TimeoutOwnership|StreamingHappensBefore|CloseWaits'
```

Expected: `CredentialHeaders` does not compile/materialize, raw capture is environment-gated, provider-derived credentials and timeout ownership are not represented safely, streaming append ordering is not enforced, and several failure attempts are not represented canonically.

- [ ] **Step 3: Implement boundary capture without reconstructing requests.**

Add `CredentialHeaders` to the `providercfg.InstanceConfig` load/validation/marshal/mutate/materialize path and resolve it beside `Headers` in `llm/providers_config.go`, keeping the maps distinct. In `llm/providercfg/mutate.go`, extend the existing on-disk API-key snapshot to retain each instance's authored `CredentialHeaders`; before `WriteFile` marshals a runtime `Config`, scrub its resolved `CredentialHeaders` and restore a cloned copy of the corresponding on-disk map exactly as it already scrubs/restores `APIKey`. This preserves `$ENV` expressions without mutating the caller's maps or ever serializing resolved secret values. Update the listed provider factories/instance params to apply both maps to HTTP but pass only credential-header names/values into `APILogCredentialMaterial`. Document `headers` as explicitly non-secret and `credential_headers` as the source of truth for secret custom headers in `docs/llm-provider-config-and-launch.md`.

The mutation boundary has this shape (names may replace the narrower existing `onDiskAPIKeys` helper):

```go
type persistedCredentialFields struct {
    APIKey            string
    CredentialHeaders map[string]string
}

disk := onDiskCredentialFields(fs, path)
for i := range scrubbed.Instances {
    authored := disk[scrubbed.Instances[i].Name]
    scrubbed.Instances[i].APIKey = authored.APIKey
    scrubbed.Instances[i].CredentialHeaders = maps.Clone(authored.CredentialHeaders)
}
```

In each adapter, call `BeginAPIAttempt` only after JSON serialization and final header/URL construction, immediately before transport. Preserve both the caller parent context and the adapter-derived context/timeout source. Call `Complete` exactly once on every return path and do not return from that path until its synchronous append attempt has returned. Refactor shared SSE transport to tee exact bytes unconditionally when an attempt is active and pass those bytes to `Complete`; do not keep `RawRequestBody`/`RawResponseBody` fields on `llm.Response` or raw-body error wrappers solely as a second logging channel.

Guard `APILogger` append admission and active operations with a closing flag plus `sync.WaitGroup`: admission occurs before increment; `Close` stops admission, waits for admitted operations, performs final sync/close, and reports late attempts through the failure observer. Do not serialize network calls under the logger mutex.

Provider wrappers (`glm`, `kimi`, `kimi_anthropic`, `minimax`, `ollama`, `openrouter`, and `openrouter_anthropic`) must inherit capture through their OpenAI-compatible, OpenAI, or Anthropic core adapter. Do not add duplicate wrapper-level records.

- [ ] **Step 4: Replace environment-gated raw tests with always-on canonical tests.**

Delete subprocess setup for `SERF_LOG_RAW_HTTP`. Preserve provider error wrapping tests that are part of the public SDK contract, but remove raw-body carrier APIs that exist only for the obsolete split logger.

- [ ] **Step 5: Run adapter tests and confirm GREEN.**

```bash
go test ./llm/providercfg ./llm/providers/internal/transport ./llm/providers/openai ./llm/providers/anthropic ./llm/providers/google ./llm/providers/openaicompat ./llm/providers/glm ./llm/providers/kimi ./llm/providers/ollama ./llm/providers/openrouter ./llm/providers/openrouter_anthropic ./llm/providers/kimi_anthropic ./llm/providers/minimax ./llm -run 'Headers|CredentialHeaders|APIAttempt|WireCapture|Credential|Outcome|TimeoutOwnership|StreamingHappensBefore|CloseWaits|ProviderError'
```

Expected: `ok`; `Headers` remain visible/exact, `CredentialHeaders` remain separate and secret, no credential sentinel appears in records/warnings/errors/materialized config, decoded bodies match fake wire bytes, timeout ownership is correct, and all append-order assertions hold.

- [ ] **Step 6: Commit wire capture.**

```bash
git status --short
git add llm/providercfg/providercfg.go llm/providercfg/load.go llm/providercfg/materialize.go llm/providercfg/mutate.go llm/providercfg/headers_test.go llm/providercfg/materialize_test.go llm/providercfg/mutate_test.go llm/providercfg/persistence_fuzz_test.go llm/providers_config.go llm/providers_config_test.go llm/headers_test.go llm/providers/internal/transport/runner.go llm/providers/internal/transport/runner_test.go llm/providers/openai/adapter.go llm/providers/openai/responses.go llm/providers/openai/chatcompletions.go llm/providers/openai/adapter_test.go llm/providers/anthropic/adapter.go llm/providers/anthropic/adapter_test.go llm/providers/google/adapter.go llm/providers/google/adapter_test.go llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go llm/providers/openaicompat/openrouter_stream_capture_test.go llm/providers/glm/adapter.go llm/providers/glm/adapter_test.go llm/providers/kimi/adapter.go llm/providers/kimi/adapter_test.go llm/providers/ollama/adapter.go llm/providers/ollama/adapter_test.go llm/providers/openrouter/adapter.go llm/providers/openrouter/adapter_test.go llm/providers/openrouter_anthropic/adapter.go llm/providers/openrouter_anthropic/adapter_test.go llm/providers/kimi_anthropic/adapter.go llm/providers/kimi_anthropic/adapter_test.go llm/providers/minimax/adapter.go llm/providers/minimax/adapter_test.go llm/adapter_timeout.go llm/adapter_timeout_test.go llm/provider_error_raw_logging_test.go llm/sdk_errors.go llm/types.go docs/llm-provider-config-and-launch.md
git commit -m "Capture exact provider bytes at the HTTP boundary

Record exact serialized requests, raw responses, credential-free endpoints/errors, and exact non-secret final headers for complete and streaming calls. Add explicit `CredentialHeaders` config ownership for arbitrary secret headers, preserve context ownership for timeout classification, and enforce append-before-retry/fallback/settlement ordering through shutdown."
```

## Task 5: Stop transcript API writes and join semantic turns to attempt groups

**Files:**
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session.go`
- Modify: `agent/session_compaction.go`
- Modify: `agent/apilog_session_attribution_test.go`
- Modify: `agent/session_openai_continuation_phase5b_test.go`
- Modify: `agent/session_turn_completion_program_fuzz_test.go`
- Modify: `agent/session_model_call_tail_coverage_fuzz_test.go`
- Modify: `agent/session_go_tail_coverage_fuzz_test.go`
- Modify: `agent/internal/atif/atif.go`
- Modify: `agent/internal/atif/atif_test.go`
- Modify: `agent/internal/atif/convert_fuzz_test.go`

**Behavior:**

- Delete `Session.logAPICall`, `appendModelAPICall`, `buildTranscriptAPILogResponse`, and their call after `callModelWithFallback`.
- Delete transcript-only copies of system prompt, tool definitions, request metadata, adapter attempts, and provider bodies.
- On a successful model-produced assistant turn, set `Turn.AttemptGroupID = finalAttempt.AttemptGroupID` in `appendAssistantTurn`.
- Preserve compact response provenance already present on assistant turns: response ID/hash, provider, response/request model, endpoint family, usage, request/storage fingerprints, and context marker.
- Compaction calls receive their own attempt group and canonical API records but do not synthesize an ordinary conversation turn solely to expose that group.
- Do not put API records back into ATIF export. ATIF continues to export semantic turns and their compact provenance, including `attempt_group_id` where its schema permits extension data.

- [ ] **Step 1: Add a failing end-to-end scripted session test.**

Run a real `Session` below a deterministic scripted/fake provider boundary. Force one retry then success. Read both on-disk files and assert:

```go
// transcript
requireKinds(t, transcriptLines, "header", "entry", "entry")
require.NotContains(t, transcriptBytes, []byte(`"kind":"api_call"`))
require.NotContains(t, transcriptBytes, exactRequestSentinel)
require.NotContains(t, transcriptBytes, exactResponseSentinel)

// join
assert.Equal(t, assistant.AttemptGroupID, attempts[0].AttemptGroupID)
assert.Equal(t, assistant.AttemptGroupID, attempts[1].AttemptGroupID)
assert.Equal(t, assistant.AttemptGroupID, settlement.AttemptGroupID)
assert.Equal(t, attempts[1].AttemptID, settlement.FinalAttemptID)
assert.Equal(t, 2, settlement.FinalAttemptCount)
```

Also test terminal failure: transcript contains no API payload/record, API log contains every failed attempt followed by its group settlement, and the existing `events.EventError` cause remains the original provider error rather than a harness error. Inject attempt and settlement storage failures separately and assert the logger's configured forensic-warning sink receives `APILogFailure` while the session returns the same provider error type/classification and preserves retry count; do not convert that warning into `events.EventError`. Run the existing five-turn steering, typed exhaustion, durable `exhausted` job state, partial-evidence, and projection contract tests unchanged as regression gates; do not add alternate budget behavior here.

- [ ] **Step 2: Run session/ATIF tests and confirm RED.**

```bash
go test ./agent ./agent/internal/atif -run 'TranscriptAPILogSeparation|AttemptGroupJoin|Attribution|ATIF|Budget|Exhaust|Steering|PartialEvidence'
```

Expected: transcript still contains `api_call` records and the assistant turn lacks the join field.

- [ ] **Step 3: Remove transcript API writes and attach the semantic join.**

Pass `ModelAttemptMetadata` only as far as assistant-turn provenance needs it. Do not retain an `AdapterAttempts` snapshot in memory after the canonical logger owns attempt records. Keep failure events in their existing event stream; adding `attempt_group_id` to an error event is optional and must not delay this required separation.

- [ ] **Step 4: Update ATIF and fuzz fixtures to semantic-only transcripts.**

Delete `api_calls` from transcript-derived fixture structs and converters. Add `attempt_group_id` to assistant trajectory metadata if ATIF already has an extension/provenance map; do not invent a new ATIF compatibility schema in this task.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

```bash
go test ./agent ./agent/internal/atif -run 'TranscriptAPILogSeparation|AttemptGroupJoin|Attribution|ATIF|Budget|Exhaust|Steering|PartialEvidence'
```

Expected: `ok`; the scripted request/response sentinels occur only in the canonical API file.

- [ ] **Step 6: Commit semantic-only session persistence.**

```bash
git status --short
git add agent/session_lifecycle.go agent/session_model_call.go agent/session.go agent/session_compaction.go agent/apilog_session_attribution_test.go agent/session_openai_continuation_phase5b_test.go agent/session_turn_completion_program_fuzz_test.go agent/session_model_call_tail_coverage_fuzz_test.go agent/session_go_tail_coverage_fuzz_test.go agent/internal/atif/atif.go agent/internal/atif/atif_test.go agent/internal/atif/convert_fuzz_test.go
git commit -m "Keep provider API records out of semantic transcripts

Remove session transcript API-call writes and attach the stable attempt-group join key to successful model-produced turns. Keep compact provenance on semantic turns and canonical request/response evidence in the private API log."
```

## Task 6: Add bounded explicit API-log access to `read_session_transcript`

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_transcript.go`
- Modify: `agent/transcript_render.go`
- Add: `agent/apilog_read.go`
- Modify: `agent/transcript_test.go`
- Modify: `agent/transcript_render_fuzz_test.go`
- Modify: `agent/transcript_render_lookup_exact_fuzz_test.go`
- Modify: `agent/transcript_structured_fuzz_test.go`
- Modify: `agent/transcript_roundtrip_fuzz_test.go`
- Modify: `test/scenarios/transcript-read-jsonl-debug-hatch.md`

**Tool input contract:**

```go
type readSessionTranscriptArgs struct {
    TranscriptRef    string `json:"transcript_ref"`
    Source           string `json:"source,omitempty"` // transcript (default) | api_log
    Format           string `json:"format,omitempty"` // markdown | outline | jsonl, transcript only
    Range            string `json:"range,omitempty"`  // transcript turn range or API record range
    ExpandTurn       *int   `json:"expand_turn,omitempty"`
    AttemptID        string `json:"attempt_id,omitempty"`
    Body             string `json:"body,omitempty"` // request | response; requires attempt_id
    OffsetBytes      int    `json:"offset_bytes,omitempty"`
    MaxBytes         int    `json:"max_bytes,omitempty"`
}
```

Rules:

```go
type apiLogSettlementState string

const (
    apiLogSettled            apiLogSettlementState = "settled"
    apiLogUnsettled          apiLogSettlementState = "unsettled"
    apiLogUnknownOutsideRange apiLogSettlementState = "unknown_outside_range"
)
```

- Omitted `source` is `transcript`. That code path resolves and opens only the transcript.
- `source="api_log"` with no `attempt_id` returns metadata summaries for a bounded record range; request/response body data is replaced by encoding and byte-count evidence.
- API-log summaries type-switch over `apilog.APILogRecord`: attempt summaries carry attempt identity/outcome/byte counts, and settlement summaries carry group ID/final attempt ID/final count/outcome/settled time. For each selected group, `settlement_state` is `settled` only when its settlement is in the selected range; `unsettled` only when the selected range reaches confirmed clean EOF after the attempt with no settlement; otherwise it is `unknown_outside_range`. Absence from a bounded page or a page ending in `ErrPartialTail` never implies `unsettled`.
- `attempt_id` implies `source="api_log"`. It returns one attempt's metadata. Exact body bytes appear only when `body` is explicitly `request` or `response`.
- Body expansion decodes stored bytes, returns at most `max_bytes` (default 16 KiB, hard maximum 64 KiB), and returns a continuation handle `{attempt_id, body, offset_bytes}` when bytes remain. UTF-8 chunks are strings only when the chunk is valid UTF-8; otherwise return base64 plus its encoding.
- API summary reads default to the last 20 records, hard-limit 100 records and 64 KiB serialized output. Transcript markdown/outline/jsonl retains existing turn and byte bounds.
- Oversized exact transcript entries return bounded head/tail evidence and an `expand_turn`/`offset_bytes` continuation handle; no single entry bypasses output limits.
- Every API-log result states `credential_values_excluded: true`.
- Invalid source/format/body combinations return a structured tool argument error before opening either file.

- [ ] **Step 1: Add failing source-isolation and bounds tests.**

Inject existing file-read seams or add narrow `openTranscript`/`openAPILog` test seams. Assert:

1. the default markdown, outline, and transcript-jsonl calls succeed while an API-log opener that panics/is counted is never invoked;
2. `source=api_log` summary omits body `data`, includes IDs/outcomes/byte counts, and respects 20/100-record plus 64-KiB bounds;
3. explicit `attempt_id` plus `body=request` returns exact escaped bytes in bounded chunks with a usable next-offset handle;
4. binary response expansion returns base64 chunks that concatenate to the original bytes;
5. oversized semantic turn expansion is bounded and returns a continuation handle;
6. a missing/corrupt/partial API-log tail produces clear bounded forensic output without weakening transcript-format rejection.
7. a page containing an attempt but ending before its later settlement reports `unknown_outside_range`; the page containing the settlement reports `settled`; a selected range reaching clean EOF after an attempt with no settlement reports `unsettled`; and a partial tail reports `unknown_outside_range`. Pagination never fabricates settlement/finality.

- [ ] **Step 2: Run tool tests and confirm RED.**

```bash
go test ./llm/apilog ./agent ./agent/internal/tool -run 'ReadSessionTranscript|APILogSource|AttemptExpansion|OversizedExpansion|UnsettledGroup'
```

Expected: schema rejects the new arguments, default/full readers still model interleaved API lines, and exact expansion is not byte-pageable.

- [ ] **Step 3: Implement source dispatch after validation, not before.**

Move transcript path resolution into the transcript branch and API path resolution into the explicit API branch. Derive `<sid>.api.jsonl` from the located session identity, not a caller-provided arbitrary path. Consume `apilog.NewDecoder`/`apilog.DecodeRecord` from Task 2 and type-switch on `apilog.APIAttemptRecord` and `apilog.APIAttemptGroupSettlement`; do not add an agent-local durable decoder. The agent reader alone owns record ranges, byte budgets, summary envelopes, attempt lookup, body pagination, and range-honest `settlement_state`. Track whether the selected range itself reaches clean EOF after each selected attempt. Treat `apilog.ErrPartialTail` as an ignored final fragment plus `partial_tail:true` and `unknown_outside_range`, but return an error for corrupt interior records.

For transcript `format=jsonl`, return only the v2 header and semantic entries in the selected turn range. Update the hint to describe JSONL as semantic transcript data, not system prompts or API logs.

- [ ] **Step 4: Update the behavioral scenario.**

Change `test/scenarios/transcript-read-jsonl-debug-hatch.md` so the explicit debug lane proves transcript JSONL is semantic-only, then makes an explicit `source=api_log` summary call and a separate `attempt_id` body expansion. Do not require live provider traffic in default Go tests; the scenario remains an opt-in/manual product contract.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

```bash
go test ./llm/apilog ./agent ./agent/internal/tool -run 'ReadSessionTranscript|APILogSource|AttemptExpansion|OversizedExpansion|UnsettledGroup'
```

Expected: `ok`; the sentinel API opener count remains zero for every default transcript call.

- [ ] **Step 6: Commit explicit bounded readers.**

```bash
git status --short
git add agent/internal/tool/definitions.go agent/session_tools_transcript.go agent/transcript_render.go agent/apilog_read.go agent/transcript_test.go agent/transcript_render_fuzz_test.go agent/transcript_render_lookup_exact_fuzz_test.go agent/transcript_structured_fuzz_test.go agent/transcript_roundtrip_fuzz_test.go test/scenarios/transcript-read-jsonl-debug-hatch.md
git commit -m "Require explicit bounded reads for private API attempts

Keep normal transcript reads semantic and isolated from API files. Add bounded API-attempt summaries and explicit byte-paged request or response expansion with credential-exclusion disclosure."
```

## Task 7: Move doctor to the canonical API log and remove Hub transcript coupling

**Files:**
- Modify: `agent/doctor/locate.go`
- Modify: `agent/doctor/doctor.go`
- Modify: `agent/doctor/apilog.go`
- Modify: `agent/doctor/apilog_test.go`
- Modify: `agent/doctor/filesystem_program_fuzz_test.go`
- Modify: `agent/doctor/dr2_build_report_fuzz_test.go`
- Modify: `cmd/serf-doctor/main.go`
- Modify: `cmd/serf-doctor/main_test.go`
- Modify: `cmd/serf-doctor/README.md`
- Modify: `cmd/serf-hub/internal/hubcore/wedge.go`
- Modify: `cmd/serf-hub/internal/hubcore/wedge_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`
- Modify: `cmd/serf-hub/main_background.go`
- Modify: `cmd/serf-hub/app_threadlist.go`
- Modify: `cmd/serf-hub/main.go`

**Doctor behavior:**

Add `APILogPath` to `doctor.Paths`, resolved as `<bucket>/sessions/<sid>.api.jsonl`. `doctor.APILog` opens that file directly and consumes the same `apilog.NewDecoder`/`apilog.DecodeRecord` durable codec from Task 2. Doctor owns only its row aggregation/filtering/rendering. It type-switches over attempts and settlements, tolerates only `apilog.ErrPartialTail` at EOF, and derives existing filters/totals from parsed response/outcome fields. Extend rows with `attempt_id`, `attempt_group_id`, `attempt_index`, `provider_instance`, `outcome`, derived `final`, `settlement_state`, and `final_attempt_count`; do not print bodies or credential-bearing material in default human/JSON summary output. A full clean-EOF doctor scan may report `unsettled`; a partial tail reports `unknown_outside_range` and no invented final count.

`doctor.Count` now counts structural semantic tool calls and assistant-text mentions only. Delete `mentions_api_calls`; doctor users who need provider request inspection use `serf-doctor apilog` explicitly.

**Hub behavior:**

Delete the failed-`api_call` transcript-tail interpretation. Hub transcript projection/list/tree/cold-load paths must remain transcript-only. Do not replace it with an API-log scan. Keep `StaleActives` timing only if it still has an owning-state probe; otherwise remove the dead override path and report the pre-existing status-state gap to Jesse as required by Global Constraints.

- [ ] **Step 1: Add failing doctor canonical-source tests.**

Write a v2 transcript with no API records and a sibling canonical API file containing success, provider rejection, decode failure, retry-group attempts, a settlement, and a second group ending at clean EOF without settlement. Assert existing token/empty/error/cache filters and totals come solely from the API file, group/index/finality columns derive from settlements, the clean-EOF group reports `unsettled`, bodies do not appear in output, and corrupt interior records fail clearly. Repeat with a partial tail after the final attempt and assert `unknown_outside_range`, not `unsettled`.

- [ ] **Step 2: Add a failing Hub cold-load isolation test.**

Place a valid v2 transcript beside an API file whose open would fail or whose body contains a unique sentinel. Exercise thread list, transcript projection, and background cold-load; assert they succeed from semantic/owning state and neither expose nor require the sentinel API data. Assert a failed `api_call` legacy transcript is rejected as unsupported rather than used as a wedge signal.

- [ ] **Step 3: Run doctor and Hub tests and confirm RED.**

```bash
go test ./llm/apilog ./agent/doctor ./cmd/serf-doctor ./cmd/serf-hub/... -run 'APILog|Settlement|Unsettled|Count|Wedge|ColdLoad|Transcript'
```

Expected: doctor still reads `transcript.APICall`, Count still scans API payloads, and Hub wedge logic still depends on transcript `api_call` tails.

- [ ] **Step 4: Implement canonical doctor reads and remove the Hub dependency.**

Use only the shared durable decoder introduced in Task 2. Preserve current diagnostic filters/rendering except where exact attempt identity/outcome and settlement-derived finality replace round-level transcript snapshots. Do not share agent pagination/rendering with doctor and do not add a raw-body flag; exact bodies remain an explicit attempt expansion concern.

For Hub, remove only the incompatible transcript API-call heuristic and its callers/tests. If a durable owning-state error signal already exists, test and use that signal without reading the API log. If it does not exist, stop and report the behavior gap; changing session lifecycle state or adding a new sidecar is outside this spec.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

```bash
go test ./llm/apilog ./agent/doctor ./cmd/serf-doctor ./cmd/serf-hub/... -run 'APILog|Settlement|Unsettled|Count|Wedge|ColdLoad|Transcript'
```

Expected: `ok`; doctor operates without any transcript API record and Hub cold-load never opens the API file.

- [ ] **Step 6: Commit consumer separation.**

```bash
git status --short
git add agent/doctor/locate.go agent/doctor/doctor.go agent/doctor/apilog.go agent/doctor/apilog_test.go agent/doctor/filesystem_program_fuzz_test.go agent/doctor/dr2_build_report_fuzz_test.go cmd/serf-doctor/main.go cmd/serf-doctor/main_test.go cmd/serf-doctor/README.md cmd/serf-hub/internal/hubcore/wedge.go cmd/serf-hub/internal/hubcore/wedge_test.go cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go cmd/serf-hub/main_background.go cmd/serf-hub/app_threadlist.go cmd/serf-hub/main.go
git commit -m "Read provider diagnostics from the canonical API log

Move serf-doctor API analysis off transcript records and expose attempt identity without bodies. Remove Hub's dependency on failed transcript API-call tails so semantic cold-load paths never inspect private provider data."
```

## Task 8: Remove obsolete raw-log controls and correct current documentation

**Files:**
- Modify: `envvars/envvars.go`
- Modify: `cmd/serf-hub/internal/launchconfig/schema.go`
- Modify: `cmd/serf-hub/internal/launchconfig/types.go`
- Modify: `cmd/serf-hub/internal/launchconfig/merge.go`
- Modify: `cmd/serf-hub/internal/launchconfig/schema_test.go`
- Modify: `cmd/serf-hub/internal/launchconfig/merge_test.go`
- Modify: `cmd/serf-hub/internal/launchconfig/wire_test.go`
- Modify: `docs/environment.md`
- Modify: `docs/tools/transcripts.md`
- Modify: `docs/performance-profiling.md`
- Modify: `fuzz/README.md`
- Modify only if its current prose claims mixed transcript records: `README.md`

**Documentation contract:**

- Transcript JSONL is semantic conversation/lifecycle state only.
- `<sid>.api.jsonl` is the canonical private exact-attempt log, always written when API logging is attached.
- Each completed attempt is appended immediately as `api_attempt`; outer-call finality/count follows as `attempt_group_settlement`, so a crash may leave a readable explicitly unsettled group.
- Exact bodies require explicit attempt/body expansion and credentials are excluded.
- `SERF_LOG_RAW_HTTP`, `raw_http_logging`, and `api-raw.jsonl` no longer exist.
- Doctor and transcript-tool examples use their new canonical sources.
- Historical proof/research/spec/plan documents remain historical artifacts; do not rewrite them to make old observations appear current.

- [ ] **Step 1: Add failing config-surface tests.**

Update launch schema tests to assert `raw_http_logging` is absent from controls, wire types, and provenance. Update the envvar catalog test to assert `SERF_LOG_RAW_HTTP` is absent. This is deliberate deletion under the hard break, not deprecation.

- [ ] **Step 2: Run focused tests and confirm RED.**

```bash
go test ./envvars ./cmd/serf-hub/internal/launchconfig
```

Expected: obsolete environment/config fields are still present.

- [ ] **Step 3: Delete the obsolete controls and update live docs.**

Remove the environment variable from `SERFFuzzRecord`'s override prose as well as its standalone row. Remove `RawHTTPLogging` from launch merge/wire/schema paths. Update live docs and examples; do not add an ignored compatibility field.

- [ ] **Step 4: Prove no current code or live documentation describes the old split.**

Run:

```bash
rg -n 'SERF_LOG_RAW_HTTP|raw_http_logging|api-raw\.jsonl|transcript.*api_call|api_call.*transcript' \
  --glob '!docs/superpowers/proofs/**' \
  --glob '!docs/superpowers/research/**' \
  --glob '!docs/superpowers/specs/**' \
  --glob '!docs/superpowers/plans/**' \
  --glob '!testdata/**' .
```

Expected: no production code or current/live documentation matches. Remaining test fixtures, if any, must be explicit rejection fixtures for legacy mixed transcripts and must say so in their test name.

- [ ] **Step 5: Run focused tests and confirm GREEN.**

```bash
go test ./envvars ./cmd/serf-hub/internal/launchconfig
```

Expected: `ok`.

- [ ] **Step 6: Commit controls and documentation.**

```bash
git status --short
git add envvars/envvars.go cmd/serf-hub/internal/launchconfig/schema.go cmd/serf-hub/internal/launchconfig/types.go cmd/serf-hub/internal/launchconfig/merge.go cmd/serf-hub/internal/launchconfig/schema_test.go cmd/serf-hub/internal/launchconfig/merge_test.go cmd/serf-hub/internal/launchconfig/wire_test.go docs/environment.md docs/tools/transcripts.md docs/performance-profiling.md fuzz/README.md README.md
git commit -m "Remove obsolete mixed and raw API logging controls

Delete the raw-body opt-in and launch setting now that one private API log is canonical and lossless. Correct current user and operator documentation while leaving historical proof artifacts unchanged."
```

If `README.md` did not require a content change, omit it from `git add`; never stage it merely because the template lists it.

## Task 9: Run separation, secrecy, and regression gates

**Files:**
- Modify only tests that fail because they still encode the pre-break mixed-format contract; do not change production behavior during this task without returning to its owning task.
- Likely affected rejection/fixture tests: `agent/transcript_roundtrip_fuzz_test.go`, `agent/transcript_structured_fuzz_test.go`, `agent/doctor/cov_s5_gaps_test.go`, `cmd/serf-hub/cov_final_main_background_fuzz_test.go`, `cmd/serf-hub/cov_thread_data_pass5_fuzz_test.go`, `cmd/serf-hub/internal/hubcore/coverage_edges_test.go`.

- [ ] **Step 1: Run the required structural searches.**

```bash
rg -n 'AppendAPICall|transcript\.APICall|APICalls|apiLines|BuildAPILogRequest|APIRawLogEntry|EnableRawLogging|EnableSessionRawLogging|RawBodyEnabled' --glob '*.go' .
rg -n 'FirstCall|failedAPICallTurn|record\.Kind == "api_call"|case "api_call"' internal/apptranscript cmd/serf-hub --glob '*.go'
rg -n '"kind"\s*:\s*"api_call"' --glob '*.go' --glob '*.md' .
```

Expected: the first two commands have no production matches. Every final-command match is either the committed design/plan/history or a specifically named legacy-format rejection fixture; no writer, accepting reader, cache/index, renderer, doctor, Hub path, or positive round-trip fixture remains.

- [ ] **Step 2: Run focused ownership and secrecy gates.**

```bash
go test ./llm/... -run 'APIAttempt|Settlement|CrashWindow|APILog|WireCapture|Credential|Outcome|TimeoutOwnership|StreamingHappensBefore|CloseWaits|Retry|Fallback|Cancellation|StorageFailure'
go test ./agent/... -run 'Transcript|AttemptGroup|APILogSource|Doctor|ATIF|Budget|Exhaust|Steering|PartialEvidence'
go test ./internal/apptranscript ./cmd/serf-doctor ./cmd/serf-hub/... ./cmdutil -run 'APILog|Transcript|Mixed|TurnCache|TurnIndex|SessionImage|ColdLoad|AttachAPILogger'
```

Expected: all packages report `ok`; no test is skipped for a missing provider credential or environment variable.

- [ ] **Step 3: Run package suites for every touched subsystem.**

```bash
go test ./identifier ./llm/... ./agent/... ./internal/apptranscript ./cmdutil ./cmd/serf-doctor ./cmd/serf-hub/... ./envvars
```

Expected: all packages report `ok`.

- [ ] **Step 4: Run repository-wide deterministic verification.**

```bash
go test ./...
make test
```

Expected: both commands exit 0. No default test performs a live provider request; a credential in the environment does not enable one.

- [ ] **Step 5: Inspect one real deterministic artifact pair.**

Use the scripted session integration test's retained temporary path or add a test helper that logs paths under `-v`. Verify with structured commands, not string regex tests:

```bash
jq -r '.kind' <session>.transcript.jsonl
jq -r 'if .kind == "api_attempt" then [.kind,.attempt_group_id,.attempt_id,(.attempt_index|tostring),.outcome] else [.kind,.attempt_group_id,.final_attempt_id,(.final_attempt_count|tostring),.outcome] end | @tsv' <session>.api.jsonl
stat -f '%Lp %N' <session>.api.jsonl
```

Expected transcript kinds: one `header` followed only by `entry`. Expected API records: one synchronously appended `api_attempt` per scripted transport attempt, ordered 1..N, followed by one `attempt_group_settlement` naming attempt N and final count N. Expected file mode: `600`.

- [ ] **Step 6: Self-review against the committed spec.**

Check every heading in `docs/superpowers/specs/2026-07-15-transcript-api-log-separation-design.md` and record the proving test or implementation path in the commit message or PR notes. Explicitly confirm:

- semantic transcript ownership and v2 rejection boundary;
- exact wire request/response fidelity, including escaped UTF-8 and binary;
- stable group plus unique attempt identity through all retry/fallback modes;
- synchronous attempt append plus settlement-based final-count/finality semantics, including readable unsettled crash tails;
- append-before-retry/fallback/settlement/Close streaming happens-before across success, midstream retry, cancellation, and endpoint fallback;
- all six outcome classes, preserved caller-versus-derived timeout ownership, and non-provider storage-failure ownership;
- provider/config-aware credential exclusion from headers, URL userinfo/query, persisted errors, and warnings while non-secret headers remain exact;
- `0600` API files, `0700` only for logger-created directories, and unchanged pre-existing shared-directory modes;
- default transcript reader isolation and bounded transcript/API expansion;
- range-honest API summary settlement states: `settled`, clean-EOF `unsettled`, and `unknown_outside_range` for bounded/partial ranges;
- shared `apilog.APILogRecord` decoding with caller-specific agent/doctor pagination/rendering;
- doctor canonical-source behavior, `internal/apptranscript` v2 cache/index invalidation, and Hub cold-load/image isolation;
- five-turn steering, typed exhaustion, durable `exhausted` state, partial evidence, and projections remain green from prerequisite Project 1;
- obsolete raw toggle/split file deletion;
- deterministic test boundary from `docs/testing.md`.

- [ ] **Step 7: Check for placeholders and type drift in this implementation.**

```bash
rg -n 'TODO|TBD|FIXME|implement later|compat|legacy fallback' identifier llm agent cmdutil cmd/serf-doctor cmd/serf-hub envvars docs/tools docs/environment.md fuzz/README.md
go vet ./identifier ./llm/... ./agent/... ./internal/apptranscript ./cmdutil ./cmd/serf-doctor ./cmd/serf-hub/... ./envvars
```

Expected: no newly introduced placeholder or compatibility path; `go vet` exits 0. Existing unrelated findings must be reported, not silently edited in this scope.

- [ ] **Step 8: Commit only any necessary test-fixture cleanup.**

```bash
git status --short
# Stage only named tests changed to express the new hard-break contract.
git commit -m "Verify transcript and API log ownership boundaries

Finish deterministic regression coverage for semantic-only transcripts, lossless private API attempts, explicit bounded reads, canonical doctor analysis, and Hub cold-load isolation. Remove only stale positive fixtures for the intentionally unsupported mixed format."
```

If no files changed in this final verification task, do not create an empty commit.
