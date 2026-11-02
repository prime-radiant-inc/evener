# Issue #4923: Google adapter excludes cached content from InputTokens

Status: revised after PAR, 2026-10-25.

## The defect

Gemini's `usageMetadata.promptTokenCount` is the total effective prompt size and
includes cached content. `llm.Usage.InputTokens` is documented as cache-miss
input only (`llm/types.go:472-512`): every adapter must subtract cached tokens
from its raw prompt count so callers can add `InputTokens` and
`CacheReadTokens` without double-counting.

`parseUsage` in `llm/providers/google/response.go` sets `InputTokens` directly
from `promptTokenCount` and separately sets `CacheReadTokens` from
`cachedContentTokenCount`. The same cached tokens therefore land in both fields.
Consumers that add the two — `effectiveRecordedInputTokens` in
`agent/session_model_call.go`, and the ATIF `prompt_wire_tokens` export added in
#4562 — count Gemini cached prompt volume twice.

Both the non-streaming path (`fromGeminiResponse`) and the streaming path
(`decodeGenerateContentStream`) call `parseUsage`, so one parser fix covers
both.

## Chosen change

In `parseUsage`:

1. Read `cachedContentTokenCount` first, keeping only a positive value (the
   existing presence rule: `CacheReadTokens` is nil when the field is absent or
   zero).
2. Set `InputTokens = max(promptTokenCount - cachedContentTokenCount, 0)`. The
   clamp matches `ParseChatUsage` and the Responses parser, and holds the
   package invariant `InputTokens >= 0` under an `invariant.Hold`.
3. Keep `OutputTokens` from `candidatesTokenCount`, `TotalTokens` from
   `totalTokenCount`, `Raw`, and the positive-only `ReasoningTokens` from
   `thoughtsTokenCount` exactly as today.

`TotalTokens` keeps its provider-supplied meaning: it is passed through from
`totalTokenCount` exactly as today, and stays 0 when that field is absent. It is
*not* recomputed from the other normalized fields. Google defines
`totalTokenCount` as prompt + tool-use prompt + thoughts + response candidates,
while this adapter maps only `candidatesTokenCount` to `OutputTokens` and keeps
`thoughtsTokenCount` in `ReasoningTokens`, which `llm/types.go` documents as
metadata never added to `OutputTokens`. `TotalTokens` is therefore already
larger than `InputTokens + CacheReadTokens + OutputTokens` on thinking and
tool-use responses; that pre-existing property is not changed or corrected
here. The cache fix moves tokens out of `InputTokens` into `CacheReadTokens`
without changing `TotalTokens`, so the provider's reported total is preserved.

Zero and absent cache counts leave `InputTokens` equal to `promptTokenCount`
and `CacheReadTokens` nil, so every existing uncached response with a
non-negative prompt count parses exactly as before. A negative
`promptTokenCount` (not a real Gemini payload) now clamps to 0 instead of
remaining negative.

## Tests

Add `TestParseUsageExcludesCachedContent` in
`llm/providers/google/response_test.go`, a table over `usageMetadata` maps:

- `promptTokenCount=100`, `cachedContentTokenCount=40`, candidates `10`,
  total `117`, thoughts `7`: `InputTokens=60`, `CacheReadTokens=40`,
  `OutputTokens=10`, `TotalTokens=117`, `ReasoningTokens=7`. The row also
  asserts the issue's explicit invariant, `InputTokens + CacheReadTokens ==
  promptTokenCount`, so a regression that double-counts cached volume fails.
- Absent cache count: `InputTokens=100`, `CacheReadTokens=nil`.
- Zero cache count: `InputTokens=100`, `CacheReadTokens=nil`. Both uncached
  rows assert `InputTokens == promptTokenCount`.
- Synthetic malformed payload with a cache count larger than the prompt count
  (impossible per Google's subset definition, included only to pin the
  defensive clamp): `InputTokens=0`, `CacheReadTokens=40`, never negative. This
  row does not assert the prompt-sum invariant because the input itself
  violates the premise.

Add `TestStream_UsageExcludesCachedContent` in
`llm/providers/google/usage_finish_chunk_test.go` so the streaming call site is
covered with cached usage too: a finish chunk carrying `promptTokenCount=100`,
`cachedContentTokenCount=40`, candidates `10`, total `117`, thoughts `7` must
accumulate `InputTokens=60`, `CacheReadTokens=40`, `TotalTokens=117`. The
non-streaming call site is covered by `parseUsage`'s direct test; both call
`parseUsage`, so neither test needs a wire-level server.

The test fails on the current parser for the cached and clamped cases
(`InputTokens = 100, want 60` and `InputTokens = 10, want 0`) and passes after
the fix. The existing `TestStream_UsageOnFinishChunk` and difftest goldens use
uncached usage, so they must stay green unchanged.

## Non-goals

- No change to Google request construction, streaming, error handling, or
  response mapping beyond `parseUsage`.
- No change to other providers or to shared `llm.Usage` semantics.
- No change to ATIF, context-pressure accounting, or their tests beyond
  confirming they still pass once `origin/main` (which carries #4562) is
  merged.
- No new `TotalTokens` fallback, no backward-compatibility layer, no labels,
  no merge.

## Verification

1. Red: `go test ./llm/providers/google/ -run
   'TestParseUsageExcludesCachedContent|TestStream_UsageExcludesCachedContent'
   -count=1` fails before the production change with `InputTokens = 100, want
   60`, `InputTokens + CacheReadTokens = 140, want promptTokenCount 100`, the
   stream `in=100 ... want 60/10/117`, and `InputTokens = 10, want 0`; the same
   command passes after it.
2. Green: `go test ./llm/providers/google/...`, `go test
   ./llm/providers/internal/openaichat/... ./llm/providers/responses/...`,
   `go test ./agent/...` for the shared usage/ATIF consumers, and `make build`.
   Also run the red test under `-tags evenerfuzz` so the live
   `invariant.Hold` compiles and holds.
3. PAR-review this spec with two competing reviewers, then the implementation
   diff with two competing reviewers; apply legitimate findings and record
   refuted ones with code-level reasons.
4. Run simplify-code over the diff, commit only the named files, run
   `roborev review --branch --base origin/main --wait`, push, open the PR, and
   send the PR URL, exact head SHA, and the settle command. Do not merge.
