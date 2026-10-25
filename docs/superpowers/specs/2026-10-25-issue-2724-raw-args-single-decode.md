# Issue #2724: decode raw tool-call arguments once per response

## Scope and authority

Issue #2724 authorizes this performance correction in the provider adapters. The
raw-argument capture helpers preserve the model's exact argument bytes for the
durable transcript and for `evener-doctor` post-mortems. Their current shape
re-decodes the whole response body once per tool call. This change indexes the
raw argument tokens once per response body (or streamed event body) and
preserves every captured byte, fallback, and provider response field.

Only these paths change:

- `llm/providers/chatcompletions/response.go` and
  `llm/providers/chatcompletions/stream.go`.
- `llm/providers/google/response.go` and `llm/providers/google/stream.go`.
- `llm/providers/anthropic/response.go`.
- `llm/providers/responses/response.go`; its callers in `complete.go`,
  `recompute.go`, and `stream.go` keep their existing signatures.
- The four packages' raw-args tests (`rawargs_test.go`), the existing
  `recompute_test.go` files where a recorded-path case belongs, plus this
  specification.

No provider request shape, response field, raw-argument output, transcript
schema, fallback, or client surface changes.

The issue's helpers are not on `origin/main`: they exist only on open PR #2640's
branch `provider-rawargs` at `95335cc068`. Both issue comments record that #2724
waits for #2640 to land. This lane merges `origin/main`
`f6900f5d8ff3e436a474054a7211fff746d1f451` and then `95335cc068` (resolving the
two conflicts in `chatcompletions/complete.go` and `chatcompletions/rescue.go`
against main's #4937 rescue rewrite) so the fix is implemented and tested against
the code the issue names. The PR is stacked on #2640 until #2640 lands on main.
Landing #2640 is not part of this lane.

## Cause and measurement

`json.Unmarshal` of the full body is repeated for each call or item. For K
tool calls in a body of B bytes the capture work is O(K·B). The five provider
paths and their six focused body-decode call sites:

| Provider path | Helper (before) | Body unit | Index space | Call sites |
| --- | --- | --- | --- | --- |
| Chat Completions non-streaming | `captureChatArgsRaw(body, partsBefore, tcIdx)` | response | `choices[0].message.tool_calls` | `response.go` (live `Complete`, JSON branch of `ExtractRecordedResponse`) |
| Chat Completions streaming | `captureChunkArgsRaw(chunkData, tcArrIdx)` | chunk | `choices[0].delta.tool_calls` | `stream.go` |
| Gemini | `captureGeminiArgsRaw(body, partIdx)` | response or SSE event | `candidates[0].content.parts` | `google/response.go`, `google/stream.go` |
| Anthropic Messages | `captureAnthropicInputRaw(body, contentIdx)` | response | `content` | `anthropic/response.go` |
| Responses API | `captureResponsesArgsRaw(body, itemIdx)` | response or terminal payload | `output` | `responses/response.go` (live `Complete`, JSON branch of `ExtractRecordedResponse`, streaming `response.completed`) |

The five provider paths are the five rows. Google and Responses each use the
same helper for a non-streaming body and a streaming body unit; Chat
Completions has a separate chunk helper for its stream. The chatcompletions
`Complete` route that `RequiresStreamingComplete` sends through
`CompleteViaStream`, and recorded SSE replay, use `decodeStream` and do not call
`captureChatArgsRaw`. The responses `rawItems` path remains a per-event item
decode of an already-aligned `json.RawMessage`, not a body decode.

Each package gets one package-local `var rawArgsBodyDecode = json.Unmarshal`
seam used only by its capture helpers. Tests replace it with a counting wrapper
and restore it with `t.Cleanup`. The seam counts focused raw-args body decodes;
it does not count the existing map decode, the existing
`json.Marshal`/`json.Unmarshal` round trip in `fromChatCompletionResponse`, or
the per-item `captureResponsesItemArgsRaw` token decode. Tests that swap the
seam stay serial and say why.

## Per-response index

Each helper returns a slice of `json.RawMessage` aligned with the loop index
space above, after one focused decode:

- `captureChatArgsRaw(body []byte) []json.RawMessage` returns one token per
  `choices[0].message.tool_calls` entry. The unused `partsBefore` parameter is
  removed.
- `captureChunkArgsRaw(chunkData []byte) []json.RawMessage` returns one token
  per `choices[0].delta.tool_calls` entry.
- `captureGeminiArgsRaw(body []byte) []json.RawMessage` returns one entry per
  `candidates[0].content.parts` part; non-`functionCall` parts hold nil.
- `captureAnthropicInputRaw(body []byte) []json.RawMessage` returns one entry
  per `content` block; blocks whose type is not `tool_use` hold nil.
- `captureResponsesArgsRaw(body []byte) []json.RawMessage` decodes `output`
  once into raw item tokens and returns one token per output item. The walk
  then indexes the selected item token and passes it to the existing
  `captureResponsesItemArgsRaw`, which type-decodes that one item. That keeps
  the old tolerant behavior: a non-object sibling item does not make the
  whole capture fail, and the per-item decode is bounded by the item token,
  not the body.

The capture is lazy: a call site runs the focused decode only when its walk
first reaches a tool call/item, and at most once per body. A call site with
`len(body) == 0` performs no focused decode. For the Chat
Completions chunk stream the trigger is the first tool-call delta whose
arguments fragment is non-empty, matching the existing guard. `rawCaptured`
tracks a failed decode so it is not retried per call. A response or event with
no tool calls performs zero focused decodes, exactly as before. A nil capture
means the focused decode failed; every call in that body then keeps the
existing map-decoded or string-form arguments, so all calls degrade together.
That is the same outcome as the old per-call helper, because every call
decoded the same body. Each call site bounds its lookup by the slice length.
`chatcompletions` still applies `rescueClaudeXMLArgs` and
`protocolhttp.RawStringContent` to the indexed string token; google and
anthropic still use the object token directly; responses still prefers
`rawItems[itemIdx]` when that entry is non-empty, else the body index for that
item.

## Byte preservation

`json.Unmarshal` copies each `json.RawMessage` token, so the index holds
independent exact wire bytes rather than aliasing the input body. Chat
Completions' index holds JSON string tokens and `protocolhttp.RawStringContent`
unescapes them byte-faithfully. The Responses body index holds output-item
object tokens; `RawStringContent` applies only to the argument string token
returned by the per-item `captureResponsesItemArgsRaw`. Gemini and Anthropic
index the object's own bytes. Invalid UTF-8, non-canonical spacing, key order,
and escape spellings survive exactly as before. The body and the map walked by
each adapter come from the same response (or recorded body), and
`runImageRecovery` mutates only the outbound request, so the index cannot shift
relative to the walk. No call receives another call's token.

## Non-goals

- No changes to the streaming delta/accumulator helpers
  `captureAnthropicPartialJSONRaw`, `captureResponsesDeltaRaw`,
  `captureResponsesArgumentsDoneRaw`, `captureResponsesOutputItemRaw`, or the
  `rawItems` item decode. The discriminator for this change is a body unit that
  can carry K calls/item entries re-decoded once per entry; those helpers
  decode an event or item that carries one call.
- No change to `rescueClaudeXMLArgs`, request building, finish reasons, usage,
  warnings, tool-call IDs, or any provider response semantics.
- No shared decode framework or cross-package refactor beyond the
  package-local seams and helpers.
- No landing, rebasing, or modification of PR #2640 in this lane.
- No full local suite; CI remains the full-suite source of truth.
- No labels. Unrelated findings become separate GitHub issues.

## Verification

1. Red first: add the counting seam to each existing per-call helper, then add
   multi-call tests that assert one focused body decode and exact per-call
   bytes. Run them against the per-call implementation and record the failing
   counts (one decode per call). Only then change the helpers to the
   per-response index.
2. The same tests pass with exactly one focused body decode per body, and a
   response/event with no tool calls performs zero. A tool-free case per
   provider (`TestRawArgs_NonStream_NoToolCallsDecodesNothing` or
   `..._NoFunctionCallsDecodesNothing`) asserts the zero. They cover:
   Chat Completions non-streaming `Complete` and the JSON branch of
   `ExtractRecordedResponse`; Chat Completions one streamed chunk with several
   tool-call deltas; Gemini non-streaming `Complete` and one streamed event
   with several `functionCall` parts; Anthropic non-streaming `Complete`;
   Responses non-streaming `Complete`, the JSON branch of
   `ExtractRecordedResponse`, and one streaming `response.completed` terminal
   payload. Bodies include text/thinking/message items before the calls so the
   index alignment is exercised, and each call's arguments use a distinct
   invalid-UTF-8 byte and non-canonical spacing. The responses accumulated
   settlement path (`body == nil`, `rawItems` set) asserts zero body decodes.
3. Existing `TestRawArgs_*` tests keep passing unchanged, including the
   single-call invalid-UTF-8, non-canonical JSON, null-fallback, stream-index,
   and multi-call tests from #2640.
4. Run `go test ./llm/providers/chatcompletions ./llm/providers/google
   ./llm/providers/anthropic ./llm/providers/responses
   ./llm/providers/internal/protocolhttp -count=1` and `make build`. Read the
   scripts and report what actually ran. Do not run `go test ./...` or
   `make test`.
5. Freeze this specification for two independent competing PAR reviewers before
   implementation, resolve legitimate findings, and re-review affected
   requirements. Repeat PAR over the implementation diff, then run
   simplify-code.
6. Commit intended paths and run branch RoboRev. Before the final push, fetch
   `origin/main` again and merge if it moved, renewing affected checks and
   reviews. Open a PR against main that closes #2724 and names its #2640
   dependency, then send the settle command. Do not merge.

## PAR amendments (2026-10-25)

The two competing reviewers of the first freeze found:

1. `google/stream.go` also calls `captureGeminiArgsRaw(ev.Data, partIdx)`; the
   first spec changed the helper signature without that call site. Resolved by
   adding the Gemini streaming path to scope, the call-path table, and the
   red/green matrix.
2. A typed `{type, arguments}` decode of the responses `output` array would
   fail on a malformed sibling item and lose capture for well-formed function
   calls, narrowing the old tolerant `[]json.RawMessage` decode. Resolved by
   indexing raw item tokens from one body decode and keeping the existing
   per-item token decode.
3. The first spec did not pin the capture placement relative to `len(body) > 0`,
   the per-item `rawItems` preference, or the `body == nil` accumulated path.
   Resolved by specifying lazy first-use capture, zero decodes with no tool
   calls, and a zero-decode assertion for accumulated settlement.
4. The first spec named chatcompletions `ExtractRecordedResponse` and live
   `Complete` without pinning the branches that reach `captureChatArgsRaw`.
   Resolved by naming the JSON branch and the non-streaming route.
5. The first spec said five call paths but listed seven entry points. Resolved
   by stating five provider paths and six focused body-decode call sites, with
   each site's body unit and index space in the table.
6. The first spec's test-file scope excluded `recompute_test.go`, the existing
   home of recorded-response cases. Resolved by allowing the packages'
   raw-args and recompute test files.

During amendment, the same per-call shape was confirmed in
`chatcompletions/stream.go`'s `captureChunkArgsRaw`, a chunk-body decode per
tool-call delta. It is included as the Chat Completions streaming path so the
issue's title holds for both body units.
