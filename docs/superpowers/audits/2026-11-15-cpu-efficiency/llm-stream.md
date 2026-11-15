# LLM stream and request CPU audit

## Verdict

The largest confirmed waste is `StreamGenerate` rebuilding and copying the complete partial response after every text delta, which makes snapshot work quadratic in output length. The next largest is live `communicate` preview reparsing the entire growing tool-argument buffer on every argument delta; API-log appends also force a synchronous filesystem sync for each provider attempt and settlement. SSE parsing uses buffered reads and a reusable event buffer, while request serialization occurs once per prepared HTTP attempt; I found no per-byte JSON decode or repeated request marshal within a single attempt.

## Findings, ranked

### 1. **High: `StreamGenerate` copies the growing response once per text delta**

**Path:** `llm.StreamGenerate` → `openAndConsumeStream` → `StreamAccumulator.Process` → `StreamAccumulator.PartialResponse` → `buildResponse` (`llm/stream_generate.go:210-229`; `llm/stream_accumulator.go:49-68, 176-214`).

**Mechanism:** Every text delta marks the accumulator dirty, then `openAndConsumeStream` calls `PartialResponse`. That rebuilds a `Response`, copies every accumulated text builder into another builder, and materializes a new string. The cost is proportional to all prior output for each delta, not the new delta.

**Evidence, executed:** `go test ./llm -run '^$' -bench '^BenchmarkAuditStreamAccumulator$' -benchmem -benchtime=1x` ran a temporary benchmark that fed fixed 16-byte deltas through `StreamAccumulator.Process`, calling `PartialResponse` after each delta, and compared append-only processing. Output: 100 deltas, partial `97,750 ns/op, 159,376 B/op, 414 allocs/op`, append-only `8,417 ns/op, 5,712 B/op, 14 allocs/op`; 500 deltas, partial `210,209 ns/op, 2,515,600 B/op, 2,019 allocs/op`, append-only `17,625 ns/op, 34,640 B/op, 19 allocs/op`; 1000 deltas, partial `789,417 ns/op, 9,221,712 B/op, 4,021 allocs/op`, append-only `24,375 ns/op, 63,312 B/op, 21 allocs/op`. The call path is visible at `llm/stream_generate.go:222-229`; `StreamAccumulator.buildResponse` traverses `textOrder` and appends each complete `tb.String()` at `llm/stream_accumulator.go:208-215`. This synthetic benchmark isolates accumulator behavior, not the full provider/network stream.

**Scale:** For *D* text deltas and final output *N* bytes, the snapshot path copies approximately the sum of every intermediate response length. With evenly sized deltas, that is about `N*(D+1)/2` bytes, or O(D·N), approaching quadratic growth when delta sizes remain bounded. At 500 deltas, this is roughly 250 full-response equivalents.

**Fix design:** In `openAndConsumeStream`, do not call `PartialResponse` for every delta. If partial snapshots are required, publish them at a bounded cadence or expose an append-only delta snapshot contract; preserve the current final `Response` behavior. A cadence changes snapshot freshness, so this requires an explicit API decision and tests that pin final response correctness, partial freshness, and cancellation behavior. A cheap initial design is to publish at a fixed byte/event interval and once at stream finish.

**How to verify the win:** Add a benchmark that drives the real `StreamGenerate` path with 100/500/1000 text deltas of fixed size and reports `-benchmem`; compare `ns/op`, `B/op`, and `allocs/op` before and after. Use `go test ./llm -run '^$' -bench 'StreamGenerate.*Delta' -benchmem -benchtime=1x` and verify growth is near-linear in total bytes rather than delta-count times response size.

### 2. **High: live communicate preview reparses growing tool arguments per delta**

**Path:** agent model round → `Session.consumeModelStream` → `StreamEventToolCallDelta` case → `emitCommunicatePreview` → `partialJSONStringField` / `scanPartialJSONStringBody` (`agent/session_stream.go:401-420, 450-467, 531-555`).

**Mechanism:** For every argument delta of the result/communicate tool, the consumer calls `Builder.String()` on all accumulated arguments, searches for the `message` field, and scans its current partial JSON string from its beginning. It then compares the reconstructed full message with the previous message and emits only the suffix. Output emission is incremental, but extraction is not.

**Evidence, read:** The tool-argument builder is appended at `agent/session_stream.go:461-463`; the `resultToolName` path immediately calls `emitCommunicatePreview` at `:465-467`. That function obtains the full builder string at `:403-406`, then rescans it through `partialJSONStringField` at `:539-554` on every call. The prefix check at `:410-420` prevents duplicate output but does not avoid the repeated scan.

**Scale:** For *D* deltas forming a message of *N* bytes, the parser visits the growing prefix per delta: O(D·N), quadratic for fixed-size deltas. This is limited to streamed result/communicate tool calls, not ordinary text or all tool calls.

**Fix design:** Keep per-call preview parser state (current JSON scan offset, string escape state, and decoded message byte offset) beside `toolArgs`; feed only the new argument bytes to it. Emit newly completed message text as it becomes safe to decode. Preserve the existing malformed/truncated-JSON and unicode-escape behavior in `scanPartialJSONStringBody`; tests must cover escapes split across deltas, repeated/non-prefix fields, invalid JSON, and final tool arguments.

**How to verify the win:** Benchmark `partialJSONStringField`-equivalent processing through `consumeModelStream` with fixed total JSON split into 10/100/500 chunks. Compare allocations and CPU; the updated parser should scale with total bytes and number of deltas, not repeated prefix length. Exercise Unicode and escape-boundary cases in tests.

### 3. **Medium: Chat Completions tool-argument chunks decode the same event JSON twice**

**Path:** provider round → `chatcompletions.Protocol.Stream` SSE callback → decode `chatCompletionChunk` → if tool arguments exist, `captureChunkArgsRaw` (`llm/providers/chatcompletions/stream.go:173-183, 266-324, 333-370`).

**Mechanism:** Each SSE `data` payload is unmarshaled into `chatCompletionChunk`. For an argument-bearing tool call, `captureChunkArgsRaw` unmarshals the same payload a second time into a focused raw-message shape, then creates a fragments slice. The second pass preserves raw JSON string bytes and is only needed on tool-argument chunks.

**Evidence, read:** The main `json.Unmarshal([]byte(data), &chunk)` is at `:173-174`; the first tool argument triggers `captureChunkArgsRaw(ev.Data)` at `:296-303`, whose focused decode is `rawArgsBodyDecode(chunkData, &focused)` at `:358-360`. The output path is reached only for argument-bearing chunks, not all SSE events.

**Scale:** One full decode per SSE event, plus one additional full decode and a slice allocation per event with tool arguments. Tool argument streaming commonly splits one call across many chunks, so this second parse multiplies with argument-delta count. No timing/allocation benchmark was available for this exact path.

**Fix design:** Decode the main event into a structure that retains `json.RawMessage` for `function.arguments`, or index raw argument tokens during the initial decode; pass those bytes alongside the typed fields. Keep `protocolhttp.RawStringContent` for exact string-content recovery. Verify providers that send noncanonical escapes and multiple tool calls in one chunk, plus the string fallback when raw extraction fails.

**How to verify the win:** Use recorded Chat Completions tool-call SSE fixtures or a focused parser benchmark with 100/500 argument chunks. Confirm decode-call count falls from two to one for argument chunks, while raw argument bytes and emitted `StreamEventToolCallDelta` values remain byte-identical.

### 4. **Medium: SSE reader allocates a string per line and copies event data at flush**

**Path:** provider `Stream` → `transport.StreamRunner.Run` → `llm.ParseSSE` → `parseSSEBlocking` / `sseParser.processLine` / `flush` (`llm/sse.go:77-107, 130-153`; `llm/providers/internal/transport/runner.go:102`).

**Mechanism:** `bufio.Reader.ReadString('\n')` returns a line string for each line. `sseParser` does reuse its `bytes.Buffer` across events with `Reset`, but `flush` makes another owned byte copy with `append([]byte{}, b...)` for each event. It does not read the complete response body into memory; bytes are consumed from `Resp.Body` through the buffered reader. With a configured timeout, the alternate parser also launches a line-reading goroutine and sends each line through a channel (`llm/sse.go:156-179`).

**Evidence, read:** The blocking path uses `ReadString` at `:140` and flush copies data at `:82-86`. The timeout path uses the same `ReadString` at `:169`. `StreamRunner.Run` passes `r.Resp.Body` directly to `ParseSSE`, at `llm/providers/internal/transport/runner.go:102`. Provider error transports use `io.ReadAll`, but those paths are non-success/error classification, not normal streamed response parsing (`llm/providers/internal/protocolhttp/stream.go:44`; `exchange.go:76`).

**Scale:** At least one line-string allocation per parsed line and one event-data byte copy per SSE event, with additional growth allocations when line/event buffers outgrow capacity. The count scales linearly with stream line/event count and payload bytes. I could not measure bytes/op for a recorded provider stream.

**Fix design:** Consider a reusable byte-oriented line parser that scans a fixed read buffer and appends only `data:` bytes to the event buffer, transferring an owned event slice only at callback boundaries. Keep `SSEEvent.Data` ownership unchanged and maintain UTF-8, CRLF, multi-data-line, EOF-flush, cancellation, and timeout semantics. Avoid a zero-copy API unless callback lifetime is explicitly constrained.

**How to verify the win:** Benchmark `ParseSSE` over a fixture with fixed payload bytes but many small events and another with fewer large events, reporting `allocs/op`, `B/op`, and `ns/op`; retain the existing SSE fuzz and ownership tests. Expected improvement is fewer allocations per line and no duplicate event-data copy.

### 5. **Medium: API-log durability sync runs once per attempt and once per logical settlement**

**Path:** transport starts attempt → `APIAttempt.Complete` → `APILogger.AppendAttempt` → `appendCanonicalRecord`; after attempt stream completion, group settlement follows the same append path (`llm/api_attempt.go:315-348, 451-490`; `llm/apilog.go:353-375, 408-470`).

**Mechanism:** Every canonical record is marshaled once by `apiLogMarshalRecord`, copied into a newline-terminated slice, written, then `apiLogFileSync` is called before unlocking. A typical successful one-attempt call therefore synchronously syncs the attempt record and settlement record. This is durable logging cost on the per-request boundary, not per streamed delta. The read API is not involved.

**Evidence, read:** `appendCanonicalRecord` calls `apiLogMarshalRecord` once at `llm/apilog.go:426`; it copies the encoded line at `:451-452`, writes at `:453`, and syncs at `:464-468`. `APIAttempt.Complete` appends one attempt at `llm/api_attempt.go:332-339`; group settlement appends its terminal record after waiting for prior attempts at `:465` onward (continued in file after line 465). The benchmark command does not isolate filesystem sync latency.

**Scale:** Two syncs per successful logical request at minimum (one attempt plus one settlement), more for retries; work is O(number of attempts + one settlement) and filesystem-latency dominated. It does not multiply by response token count, though each attempt record includes request/response evidence.

**Fix design:** Do not remove durability silently. First consider one sync for the attempt plus settlement records, or a configurable durability mode/batch, only if the project accepts a bounded crash-loss window. A group-level append batch could marshal each canonical record once but write both records before one sync; preserve record ordering, quarantine-on-write-failure, close barriers, and settlement-after-attempt guarantees. This trades durability granularity and needs Jesse's ruling.

**How to verify the win:** Add an API-log benchmark using a temporary session directory, compare `AppendAttempt`+`AppendSettlement` wall time and injected `apiLogFileSync` call counts. The expected count is currently 2 for one attempt; demonstrate one sync only after a deliberate batching policy, and test crash/reopen, ordering, and write-failure quarantine behavior.

### 6. **Low: error-path classification can decode and re-encode one in-band Responses error several times**

**Path:** Responses stream callback → `responsesInbandError` (`llm/providers/responses/stream.go:277-322`).

**Mechanism:** On an error event, code decodes the envelope, may decode its nested `error`, decodes the full body into `map[string]any`, marshals the normalized error object, and unmarshals that object back into a map. This is limited to in-band error events, not normal output deltas.

**Evidence, read:** The envelope decode is at `:286`; nested decode at `:295`; raw map decode at `:310`; and marshal-then-unmarshal at `:314-315`. In successful response data these branches are not reached.

**Scale:** Up to three decodes and one encode per in-band error event, with allocations proportional to that event's JSON size. Error events are rare relative to deltas; this ranks below steady-state stream work.

**Fix design:** Decode the raw event map once and inspect the relevant nested fields from it, then assign the typed normalized error map directly rather than marshal/unmarshal round-tripping `inband`. Keep malformed and string-valued `error` behavior and the full raw forensic payload. Add tests for flat, nested, wrapped, and malformed errors.

**How to verify the win:** Benchmark `responsesInbandError` on representative flat and nested payloads with `-benchmem`; instrument decode/encode calls in focused tests to confirm the normal stream callback is unchanged and error-path JSON passes are reduced.

## Clean

- **SSE does not read the whole successful response body:** `transport.StreamRunner.Run` passes `Resp.Body` to `llm.ParseSSE` (`llm/providers/internal/transport/runner.go:102`), which uses `bufio.Reader`; whole-body `io.ReadAll` is confined to protocol HTTP non-stream/error responses (`llm/providers/internal/protocolhttp/stream.go:44`, `exchange.go:76`).
- **Accumulator append itself is not quadratic:** `StreamAccumulator.Process` appends text and reasoning deltas to `strings.Builder` (`llm/stream_accumulator.go:60-68`), and tool arguments to a byte slice (`:108-141`). The quadratic behavior is the snapshot rebuild, not the append operation.
- **Request JSON is marshaled once per prepared attempt:** `protocolhttp.Prepare` applies pruning/constants/auth/preparation and then calls `json.Marshal(c.Body)` once (`llm/providers/internal/protocolhttp/call.go:66-122`). Retrying intentionally rebuilds and marshals because `Prepare` mutates the body in place and provider preparation/authentication may be attempt-specific. Do not cache across attempts without proving those transformations are repeatable and safe.
- **Usage aggregation is per completed step, not per delta:** `StreamGenerate` adds `stepResp.Usage` only when finalizing each tool/model step (`llm/stream_generate.go:310-341`). Chat stream usage fields are parsed when present in a chunk (`llm/providers/chatcompletions/stream.go:207-223`), but no pricing/token recount loop over streamed text was found.
- **API log JSON is not re-parsed after being encoded on the write path:** `apilog.MarshalRecord` validates then marshals once (`llm/apilog/codec.go:282-300`); the append path writes that byte slice directly. It does make an additional copy to append the newline, so there are two transient encoded-body buffers, not two marshals.

## Open questions

- May partial responses in `StreamGenerate` become less frequent (for example, every fixed byte/event interval) to avoid an O(D·N) copy while preserving final response behavior?
- What durability window, if any, is acceptable for API-log attempt and settlement records? Batching their fsyncs could reduce per-request latency but changes crash-loss semantics.
- Should parser-owned `SSEEvent.Data` remain independently owned for callbacks, or can a future API define a borrowed-buffer lifetime to avoid the event copy?

## Measurement notes

- **Measurement status:** The first partial-snapshot benchmark attempt was killed before producing output under host contention; its retry completed and produced the measurements in finding 1. The temporary benchmark file was deleted.
- The requested multi-package benchmark command `go test ./llm/... -run '^$' -bench . -benchmem -benchtime=1x -memprofile "$EVENER_SCRATCH_DIR/llm.mem"` exited 1 because Go rejects `-memprofile` with multiple packages. The first attempt from `llm/` also failed because the package pattern was rooted incorrectly (`lstat ./llm/: no such file or directory`). The valid single-package rerun `go test ./llm -run '^$' -bench . -benchmem -benchtime=1x -memprofile "$EVENER_SCRATCH_DIR/llm.mem"` exited 0 and reported only the unrelated existing `BenchmarkDataURI-18`: `431625 ns/op`, `1402016 B/op`, `3 allocs/op`. The targeted temporary benchmark and its numbers are recorded in finding 1; `llm/zz_audit_streamacc_test.go` was deleted after the run.
- No temporary benchmark file was created. No recorded provider stream suitable for an end-to-end benchmark was found under `llm/testdata`; the existing data is SSE fuzz corpus input.
