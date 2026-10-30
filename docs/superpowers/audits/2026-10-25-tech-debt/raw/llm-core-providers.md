# Audit: llm-core-providers

Slice: `llm/*.go` (root package), `llm/providers/**`, `llm/apilog`, `llm/internal`. Registry (`llm/registry`) is out of scope.
Worktree: `.worktrees/debt-audit` at origin/main `a6c0b9c561`. Read-only audit; no tests were run.

## Overview and inventory

| Area | Non-test Go lines | Test Go lines | Notes |
|---|---|---|---|
| `llm/*.go` root | 10,777 (about 62 files) | 30,288 (132 files) | 2.8x more test than production |
| `llm/providers/**` | about 9,800 | 20,556 (92 files) | anthropic, chatcompletions, responses, google, tokenauth, internal/{transport,protocolhttp,requestutil,openaichat} |
| `llm/apilog` | 864 | 2,089 | record/codec/body/header |
| `llm/internal/imagerepair` | 740 | n/a | JPEG/ICC repair, single consumer (chatcompletions) |

Largest production files: `client.go` 902, `token_count.go` 891, `types.go` 828, `api_attempt.go` 692, `google/request.go` 600, `anthropic/request.go` 586, `transport/http_attempts.go` 565, `responses/stream.go` 561.

Functions of 100+ lines (AST measured): `decodeMessagesStream` 413, `chatcompletions.decodeStream` 293, `StreamGenerate` 291, `responses.decodeStream` 233, `toResponsesInput` 233, `decodeGenerateContentStream` 203, `toAnthropicMessages` 193, `toGeminiContents` 181, `toChatMessages` 168, `rescueClaudeXMLArgs` 134, `fromAnthropicResponse` 129, `responses.buildBody` 127, `chatcompletions.buildBody` 112, `StreamAccumulator.Process` 111, `Generate` 107.

Test-file smells: 46 `*fuzz*_test.go`, 28 `*cov*`/`*coverage*` test files, 118 test comments citing production line numbers (for example "covers lines 229-231"), one file with an "anti-collision token" header written for parallel agent lanes.

Overall shape: the provider layer was consolidated into four `Protocol` implementations plus shared `protocolhttp`/`transport`, which is good. What is left is (a) a large amount of production API with no production caller (the high-level `Generate` family streaming half, the SSE read-timeout stack, a list of exported helpers), (b) four near-parallel protocol packages that still copy-paste the same finishing, listing, usage, image and boilerplate code, with visible drift between the copies, and (c) the API-log attempt machinery living in the root `llm` package (about 2,400 lines, 75 exported declarations) even though its only consumers are `llm/providers/internal/*` and `cmdutil`.

Product-guide cross-check: `docs/product/subsystems.md` S15/S16 name `llm/providers` and `llm/registry` and the Chat Completions image recovery contract. None of the fixes below changes those contracts; fixes that touch behavior are marked and need TDD.

---

## Findings

### llm-core-providers-1 (high) Streaming half of the Generate family has no production caller

Locations
- `llm/stream_generate.go:135` `StreamGenerate` (291-line function, file is 425 lines), `StreamResult`, `TextStream` (`:46`)
- `llm/generate_object.go:117` `StreamGenerateObject`, `:204` `tryParsePartialJSON`, `:22-23` `streamGenerateObjectStartMu`/`streamGenerateObjectStart`
- `llm/stream_generate.go:175` and `llm/generate.go:255` duplicate a 31-field `Request{...}` literal built from `GenerateOptions`

Evidence
- `git grep` for `StreamGenerate|StreamObjectResult|TextStream` outside `llm/` finds nothing (only tests inside `llm/`). Production callers of the family are `cmd/llmcall/main.go:299,319` (Generate, GenerateObject), `agent/session_namer.go:101`, `tools/tool-fluency/cmd/evener-fluency/respond.go:240` (GenerateObject). The agent session streams through `client.Stream` plus `llm.RetryStream` directly (`agent/session_stream.go:178`).
- `streamGenerateObjectStart = StreamGenerate` is a package-level mutable seam behind an RWMutex that exists only so tests can swap it.
- `generate_object.go:137` `_, cancelOut := context.WithCancel(ctx)` discards the derived context, so `cancelOut` cancels something nobody uses.
- Tests carrying this: `stream_generate_test.go` 1,448 lines, `stream_generate_coverage_fuzz_test.go` 142, `generate_object_residual_fuzz_test.go` 129 (about 1,700 lines), plus example tests.
- `doc.go` advertises only `Generate`, `GenerateObject` and `Client.Stream` for incremental output, so the doc does not promise the deleted functions.

Impact: about 610 production lines plus about 1,700 test lines that nobody runs in production but everybody maintains; the duplicated Request literal means any new `GenerateOptions` field must be added in two places and silently drifts.

Fix plan
1. Confirm with Jesse (deleting existing implementations needs explicit permission).
2. Delete `stream_generate.go`, `StreamGenerateObject`, `StreamObjectResult`, `tryParsePartialJSON`, the `streamGenerateObjectStart*` seam, and their tests. Keep `GenerateObject`, `stripMarkdownCodeFence`, `tryUnmarshal`.
3. Extract the remaining single `GenerateOptions -> Request` conversion into one method used by `Generate`.
4. Update `llm/README.md`/`doc.go` only if they mention the removed names (grep showed they do not).
Size: about -2,300 LOC (610 prod, 1,700 test). Risk: low (no production caller); Jesse approval required to delete.

### llm-core-providers-2 (medium) Multi-round tool loop in `Generate` is only exercised by tests

Locations: `llm/generate.go:46-110` (`GenerateOptions.Tools/MaxToolRounds/StopWhen/RepairToolCall`), `:240-347` loop, `:403-515` `executeSingleToolCall`/`executeToolCalls`/`parseAndValidateArgs`, `:30-41` `ToolCallContext`/`ToolCallContextFromCtx`.

Evidence
- Every production caller of `Generate`/`GenerateObject` passes no tools: `cmd/llmcall/main.go:256` sets `Tools: nil` and `MaxToolRounds: &maxToolRounds` (0); `session_namer.go:92-99` and `respond.go:240-248` pass none.
- `ToolCallContextFromCtx` has zero callers outside its file. `llm.Tool` is only embedded in `agent/internal/tool/registry.go:440`, which the agent session loop executes itself.
- `doc.go:28` still advertises "including any tool-call rounds".
- Tests: `generate_test.go` 1,741 lines, `generate_core_fuzz_test.go` 471, `generate_edges_fuzz_test.go` 136.

Impact: roughly 300 production lines of a second tool-execution loop that competes conceptually with the agent session loop. It will drift from the real loop (which owns permissions, hooks, delegate semantics).

Fix plan: this is a product-adjacent API choice, so raise with Jesse before acting. Option A: keep `Generate` as a single-shot completion helper (delete tool execution, `ToolCallContext`, `StopWhen`, `RepairToolCall`, `executeToolCalls`, `jsonschema` tool arg validation; keep `compileSchema` for GenerateObject). Option B: leave as library API but document as test/library-only. Recommend A. Size about -1,500 LOC including tests. Risk: medium (public API shrink; verify `llm.Tool` embedding in `agent/internal/tool` still compiles, keep `Tool`/`ToolDefinition` types).

### llm-core-providers-3 (medium) SSE read-timeout stack is dead; `SSEOpts` is never set

Locations
- `llm/sse.go:58-66` `WithStreamReadTimeout`, `:116-130` `ParseSSE` option handling, `:156-209` `parseSSEWithTimeout`, `:211-239` `sseActivityReader`/`sseReadTimer`, `:18-45` `sseReadTimeoutError`, `APITimeoutSourceForSSE`
- `llm/adapter_timeout.go:264-271` `StreamReadSSEOptions`
- `llm/providers/internal/transport/runner.go:50-51,86,113` `StreamRunner.SSEOpts`

Evidence
- `git grep` shows `StreamReadSSEOptions` has no caller (even the 4 decoders construct `StreamRunner` with no `SSEOpts`); the decoders carry the comment "HTTP response-byte idle is owned by ClientWithAdapterTimeout, below gzip decoding" (`google/stream.go:50`), i.e. `response_idle.go` superseded this.
- `APITimeoutSourceForSSE` is called in `runner.go:113`, `exchange.go:57`, `stream.go:52`, but with `SSEOpts` never set it can never see an `*sseReadTimeoutError`, so those three branches are unreachable in production.
- `ParseSSE` doc still warns about a background read goroutine that is never started in production.

Impact: about 150 lines of subtle concurrent code (goroutine, timer, channel) plus three dead branches in the transport layer that look load-bearing.

Fix plan: delete the timeout path from `ParseSSE` (keep `parseSSEBlocking` as the only path, rename), delete `sseActivityReader`/`sseReadTimer`/`sseReadTimeoutError`/`StreamReadSSEOptions`/`SSEOpts`, and the three `APITimeoutSourceForSSE` branches; keep `StreamRead` in `AdapterTimeout` only if `response_idle.go` reads it (verify). TDD: existing idle-timeout tests in `provider_idle_*_test.go` stay as the behavior guard. Size about -250 LOC incl. tests. Risk: low-medium; run `provider_idle*` and `sse_test.go` on CI.

### llm-core-providers-4 (medium) Exported helpers with zero production callers, plus a source-compat parameter

Locations and evidence (all confirmed by `git grep -w`, no non-test caller, and no mention beyond comments)
- `llm/headers.go` whole file: `MergeHeaders` (`:34`) and `sortedHeaderKeys` (`:10`, only referenced by `core_contracts_fuzz_test.go:105`). Its comment says it reports "missing-variable error deterministically", but nothing calls it.
- `llm/media_utils.go:66` `IsImageFile`
- `llm/types.go:279` `OrderedEffortLevels` (doc claims "one definition so those surfaces cannot drift", no surface uses it)
- `llm/api_attempt.go:257` `(*APIAttempt).SetWireRequestMetadata`
- `llm/classify_http.go:72` `ErrorHint`
- `llm/adapter_timeout.go:143` `AdapterTransport`
- `llm/api_attempt_sanitize.go:42` `BuildAPILogCredentialMaterial` (no test either; its doc says "used by all provider adapters" and `protocolhttp/call.go:159` still cites it though it reimplements it, see finding 11)
- `llm/providers/anthropic/response.go:246` `clampEffort`: a one-line pass-through to `llm.ClampReasoningEffort` whose only caller is `areq_targets_fuzz_test.go`; its comment claims it avoids "maintaining its own narrower hierarchy".
- `llm/providers/internal/protocolhttp/call.go:43-46` `Call.Auth` field ("Nil keeps the historical global lookup"): no `Call{... Auth: ...}` exists anywhere; authenticators come from `AuthenticatorForContext`.
- `llm/adapter_timeout.go:128` `ApplyAdapterTimeout(ctx, at, _ bool)`: the bool is ignored ("retained for source compatibility with adapters that distinguish request phases elsewhere"); the three callers (`client.go:558`, `exchange.go:37`, `stream.go:24`) pass `false/false/true`. That is a compat shim with no outside consumer.

Impact: dead public surface that readers must reason about; the stale comments mislead.

Fix plan: delete each symbol and its direct tests in one PR (grep each name before deleting), drop the bool parameter and update the three callers. No behavior change. Size about -200 LOC (prod + tests). Risk: low. Per Jesse rule, removing the compat parameter needs no new compat; it is the removal of one.

### llm-core-providers-5 (high) The four stream decoders copy the same finishing and bookkeeping code

Locations
- `llm/providers/anthropic/stream.go:22-435` (413-line function), `chatcompletions/stream.go:21-313` (293), `responses/stream.go:329-561` (233), `google/stream.go:22-224` (203)
- Repeated finish tail: `anthropic/stream.go:410-418`, `chatcompletions/stream.go:162-173`, `responses/stream.go:513-523`, `google/stream.go:196-210`: build `StreamEventFinish{FinishReason:&r.Finish, Usage:&r.Usage, Response:&rp}`, then `if attempt.Active() { finalEvent = &event } else { s.Send(event) }`, then `finished = true; cancel()`.
- Repeated: `defer func(){ resp.Body.Close(); s.CloseSend() }()`, `llm.StampEndpointURL(&r, ...)`, "undecodable event: forward raw ProviderEvent" passthrough (anthropic `:106`, google `:63`, chat, responses `:404`), in-band error wrapped as `&transport.FatalStreamError{Err: llm.RewriteErrorProvider(...)}` (anthropic `:426`, google `:78`, responses `:534,:552`), `ReasoningTokensEstimated` fallback (anthropic `:403`, chat `:155`).
- Signature drift: google/anthropic decoders take `(…, req, provider, endpointURL, material, attempt)`; chat/responses take `(…, req, res, r, attempt)`.

Impact: any fix to finish semantics (the attempt-capture handoff is subtle) needs four coordinated edits; the transport package was created to stop exactly this drift ("so the protocols cannot drift on these mechanics", `runner.go:1-9`) but stops short of the tail.

Fix plan
1. Add `(*StreamRunner).Finish(resp llm.Response)` that does the Active/Send branching, sets `*Finished`, and is the one place that stamps the endpoint URL; also `(*StreamRunner).DecodeObject(data []byte) (map[string]any, bool)` that does UseNumber decode and the raw-passthrough event.
2. Make all four decoders take `(sctx, cancel, resp, s, req, res, r, attempt)` (they all have `res.Instance`; google and anthropic get `r.EndpointURL/r.Material` already).
3. Migrate one provider per commit, guarded by the existing `providers/difftest` differential tests and each package's stream tests (CI only).
Size: about -250 LOC net. Risk: medium (streaming bookkeeping), mitigated by difftest and recorded-wire tests.

### llm-core-providers-6 (medium) Giant conversion and decode functions

Locations: `responses/input.go:99` `toResponsesInput` (233 lines), `anthropic/request.go:376` `toAnthropicMessages` (193), `google/request.go:405` `toGeminiContents` (181), `chatcompletions/messages.go:28` `toChatMessages` (168), `anthropic/response.go:10` `fromAnthropicResponse` (129), plus the four stream decoders of finding 5.

Evidence: AST-measured lengths above; each is a single `switch p.Kind` inside nested loops with inline closures, which is where findings 8 and 16 hide.

Impact: review and drift risk; the image/document handling in these functions is exactly where the copies diverged.

Fix plan: after findings 5 and 8 land, split each into per-role helpers (`userMessage`, `assistantMessage`, `toolResult`), one file-private function per `ContentKind`. Pure refactor under the existing golden wire tests (`wirecapture`, `difftest`). Size: neutral LOC, large readability win. Risk: low if done mechanically, one function per commit.

### llm-core-providers-7 (medium) Per-protocol boilerplate repeated four times, with drift

Locations
- Registration skeleton: `Protocol{Client}`, `DefaultProtocol`, `init(){RegisterProtocol}`, `ID()` in `anthropic/protocol.go:11-27`, `google/protocol.go`, `responses/protocol.go`, `chatcompletions/protocol.go`.
- `prunablePaths` + `PrunablePaths()` clone-then-sort: `anthropic/protocol.go:28-35`, `google/protocol.go:27-33`, `responses/protocol.go:36-48`, `chatcompletions/protocol.go:35-46`. The interface comment (`registry_shape.go:22`) says the result "must equal registry.PrunablePaths(ID()); a test asserts it", i.e. the same table already lives in `registry`.
- `Complete` preamble `if protocolhttp.RequiresStreamingComplete(res) { return CompleteViaStream(...) }` and `completionCall` wrappers: `anthropic/protocol_transport.go:26,40`, `chatcompletions/complete.go:31,42`, `google/protocol_transport.go:22,87`, `responses/complete.go:17,29`.
- Endpoint-unsupported guards: 8 sites (`anthropic/protocol_transport.go:70,111`, `chatcompletions/models.go:99`, `chatcompletions/complete.go:67`, `google/protocol_transport.go:116,164`, `responses/models.go:137`, `responses/tokens.go:23`).
- `ListModels` Call literal with `llm.Request{Model: "*", AdapterTimeout: llm.ModelListingTimeout(ctx)}` plus unmarshal, `fmt.Errorf("models.list: %w")` and `sort.Slice`: `chatcompletions/models.go:102`, `responses/models.go:140`, `anthropic/protocol_transport.go:80`, `google/protocol_transport.go:129`.
- Drift caused by the copies: `google/vertex_models.go:110` builds `llm.Request{Model: "*"}` with no `AdapterTimeout`, so Vertex model listing ignores the model-listing timeout every other listing honors.
- Pass-through chain: anthropic `BuildBody` (`protocol.go:34`) -> `buildProtocolBody` -> `buildProtocolBodyForOperation(…, true)` (`protocol_request.go:16-20`).
- File naming drift: anthropic/google use `protocol_request.go`/`protocol_transport.go`, chat/responses use `complete.go`/`models.go`/`tokens.go`.

Impact: new protocol or new capability means four edits; the Vertex bug is the proof.

Fix plan
1. In `protocolhttp`, add `ListModels(ctx, p.Client, res, family, parse func(*Result) error)` that owns the unsupported guard, the Call literal, timeout, and the `models.list:` wrapping; sort in one place.
2. Add `llm.PrunablePathsFor(protocolID)` forwarding to `registry.PrunablePaths` and drop the four tables (keep the equality test only if a table remains).
3. Add `protocolhttp.CompleteJSON(...)` or embed a small `base` struct providing `Complete` preamble and `completionCall`.
4. Delete the `buildProtocolBody` wrapper; make the enforce flag explicit at the two call sites.
TDD: first a failing test that Vertex `ListModels` honors `ModelListingTimeout` (behavior change), then refactor. Size about -200 LOC. Risk: low-medium.

### llm-core-providers-8 (medium) Image part resolution implemented per provider, with behavioral drift

Locations
- `anthropic/request.go:259-290` (Data | local path | URL; default `image/png`; normalize bytes)
- `google/request.go:341-379` (same resolve, no normalization, inlineData/fileData)
- `responses/input.go:166-194` (Data | local path; normalize via `normalizeImageInput`)
- `chatcompletions/messages.go:291-310` (Data -> data URI default `image/png`; URL passed through)
- `token_count.go:749` `imageDimensions` (a fifth local/Data resolver)

Evidence: all call the same `llm.IsLocalPath`/`ExpandTilde`/`os.ReadFile`/`InferMimeTypeFromPath` sequence by hand. Behavior diverges: Chat passes a local path through as the `url` (never reads the file), Google sends unnormalized BMP/TIFF bytes that Anthropic and Responses re-encode, only Anthropic refuses undecodable bytes loudly.

Impact: the same image attachment succeeds on one provider and fails on another for avoidable reasons; fixes (such as #4464 normalization) land in one copy.

Fix plan: add `llm.ResolveImage(img *ImageData) (data []byte, mediaType, url string, err error)` in `media_utils.go` (read path, infer type, default PNG); each builder keeps only its wire shape. Decide per provider whether to normalize (Jesse call; the registry already has per-row `ImageDetail`). TDD: write the Chat local-path and Google BMP cases red first. Size about -80 LOC. Risk: medium (behavior change for chat/google needs sign-off).

### llm-core-providers-9 (medium) Non-streaming and streaming decode into different number types; four int-coercion helpers

Locations
- `protocolhttp/exchange.go:79` `json.Unmarshal(rawBytes, &raw)` (numbers become `float64`); stream decoders use `dec.UseNumber()` (`anthropic/stream.go:106`, `google/stream.go:63`, `responses/stream.go:404`, `chatcompletions/recompute.go:35`, `responses/recompute.go:138,150`; 11 sites in all).
- Coercion helpers: `llm/types.go:816` `IntFromAny` (json.Number, float64, int), `anthropic/stream.go:436` `intFromAny` (adds int64), `google/stream.go:226` `tokenCountInt` (byte-identical body to anthropic's), `requestutil/numbers.go:55` `PositiveInt`, plus `responses/tokens.go:29` `r.Raw["input_tokens"].(float64)` that only works because the non-stream path is float64.
- `google/response.go:30` `normalizeJSONNumbers` exists to repair `json.Number` leakage from the stream path.

Impact: the same field parses differently depending on transport; large integers lose precision on the non-stream path; two identical helpers exist.

Fix plan: add one `protocolhttp.DecodeObject([]byte) (map[string]any, error)` (UseNumber) used by `Do`, the stream decoders (via finding 5's `DecodeObject`), and the recompute helpers; make `llm.IntFromAny` handle `int64` and delete `intFromAny` and `tokenCountInt`; make `responses.CountTokens` use `IntFromAny`. TDD: red test that a >2^53 integer in a non-stream `Raw` round-trips. Size about -60 LOC. Risk: low-medium (Raw consumers: check apilog/doctor for float64 assumptions).

### llm-core-providers-10 (low) Usage derivation fragments and reasoning-token estimate copied four times

Locations: estimate block at `anthropic/response.go:130-135`, `anthropic/stream.go:403-408`, `chatcompletions/response.go:305-316`, `chatcompletions/stream.go:155-160`; helpers `anthropic/request.go:574` `estimateThinkingTokens` and `chatcompletions/response.go:324` `estimateThinkingFromBuf` (both `max(chars/4,1)`; `token_count.go:84,88` has another `len/4`). Three `invariant.Hold(usage.InputTokens >= 0 …)` copies in `google/response.go`, `responses/response.go`, `openaichat.go`.

Fix plan: add `llm.EstimateReasoningTokens(chars int) int` and `(*Usage).FillEstimatedReasoning(chars int)`; the per-provider `parseUsage` functions stay (wire shapes differ). Size about -40 LOC. Risk: low.

### llm-core-providers-11 (medium) Credential-material merge and construction implemented three times

Locations
- `llm/api_attempt.go:449-468` `mergeAPILogCredentialMaterial` and `providers/internal/transport/http_attempts.go:215-233` `mergeTransportCredentialMaterial` are line-for-line identical (header names, query names, values, then `NewAPILogCredentialMaterial`).
- `protocolhttp/call.go:157-190` `credentialMaterial` re-implements `llm/api_attempt_sanitize.go:42` `BuildAPILogCredentialMaterial` (same header/values/userinfo steps) and cites it in its comment.
- `(*APIAttempt).MergeCredentialMaterial` (`api_attempt.go:240`) is the third merge entry.

Impact: this is security-relevant redaction code (a missed header leaks a credential into the on-disk API log); three diverging copies is the wrong place to have drift.

Fix plan: add `func (m APILogCredentialMaterial) Merge(o APILogCredentialMaterial) APILogCredentialMaterial` in `llm`, export it, delete both private copies; have `protocolhttp.credentialMaterial` call `BuildAPILogCredentialMaterial` (after deleting finding 4's "unused" claim, it becomes used) or delete the latter. Existing `api_attempt_sanitize*_test.go` and credential admission fuzz tests are the guard. Size about -45 LOC. Risk: low.

### llm-core-providers-12 (medium) HTTP status to error kind table and kind taxonomy defined in parallel places

Locations
- `llm/errors.go:431-487` `errorFromHTTPStatus` (400/422, 401, 403, 404, 408, 413, 429, 5xx) vs `providers/internal/transport/api_attempt.go:177-197` `explicitAPIAttemptErrorClass` repeating the same status switch to produce `Kind*` strings.
- `llm/errorkind.go:83-114` `Kind()` (an `errorIs[...]` ladder) vs `:152-164` 13 `declaredKind()` methods: two tables mapping concrete type to `ErrorKind` that must agree (`ContextBudgetError` is in `Kind` and also has its own method at `errors.go:101`).
- `llm/classify.go:57-106` `Classify` re-derives retry disposition from status codes (`case 400, 401, 403, 404, 413, 422` / `408, 429, 500…`) while every error already carries `Retryable()`; `retry_util.go:31` `retryableError` wraps `Classify` ("Delegate to the classifier so retry decisions are made in exactly one place") but adds its own context-sentinel pre-check.

Impact: adding a status or type needs edits in up to four places; `explicitAPIAttemptErrorClass` ignores the quota/usage-limit refinement that `errorFromHTTPStatus` performs on 403 (it relies on `DeclaredKind` first, which is why it works only for typed errors).

Fix plan: derive the fallback with `llm.Kind(llm.ErrorFromHTTPStatus("", status, "", nil, nil))`, deleting the switch; implement `Kind` via a loop `errors.As`-ing to a `kindBearer` (or reduce `declaredKind` to the single source and make `Kind` walk the chain asking it); make `Classify` trust `Retryable()` and keep only the cancel/budget special cases. TDD: table test over all statuses asserting `explicitAPIAttemptErrorClass == Kind(ErrorFromHTTPStatus)` first. Size about -120 LOC. Risk: medium (retry behavior; the existing `classify*_test`/fuzz targets pin it).

### llm-core-providers-13 (medium) Request shaping is done in `ShapeRequest` and then again by every builder; `ShapeRequest` itself runs twice

Locations
- `llm/client.go:343-346` and `:400-402`: `ShapeRequest` before middleware, then again inside `shapeAndApplyTokenBudget` (`:405-408`) in `base`.
- Re-derivations after the "single place request-level shaping happens" (`registry_shape.go:57`): `google/protocol.go:48-53` (clears effort when `caps.Reasoning` is false and when effort is `none`), `responses/request.go:161,203` and `chatcompletions/reasoning.go:16` (`caps.Reasoning != nil && !*caps.Reasoning` re-tested), `chatcompletions/request.go:75` and `responses/request.go:73` (identical `ToolChoiceForcing` downgrade, copied), `chatcompletions/request.go:94` and `responses/request.go:122` (StructuredOutput downgrade, copied).
- Output-limit reconcile runs twice: end of `chatcompletions/request.go:127` and `responses/request.go:141` (`ReconcileOutputField`) and again in `FinalizeBody` (`reconcilePreparedOutput`, `chatcompletions/request.go:170`, `responses/request.go:147`); `chatcompletions/request.go:155` also keeps a private `reconcileOutputField` variant that duplicates `requestutil.ReconcileOutputField` with an alternate-name twist.

Impact: the build-time reconcile is redundant when `FinalizeBody` recomputes from the same inputs (verify before deleting; ProviderOptions can set the field between the two); the "single place" invariant is false, so a change in `ShapeRequest` can be masked by a later transform.

Fix plan: (1) move `ToolChoiceForcing` and `StructuredOutput` downgrades into `ShapeRequest` (they depend only on `caps` and `req`); (2) drop `google/protocol.go` effort resets and keep the `none` handling in one place; (3) delete the build-time `ReconcileOutputField` calls if a test proves `FinalizeBody` covers them; (4) fold chat's private variant into `requestutil`. TDD: golden wire tests (`wirecapture`) stay green; add red tests for each moved rule at the `ShapeRequest` level. Size about -60 LOC. Risk: medium; do per rule.

### llm-core-providers-14 (low) `Client.Complete`/`Stream` twins and a dual dispatch path

Locations: `llm/client.go:325-362` vs `:365-403` (same Validate, AdapterTimeout default, dispatchTarget, ShapeRequest, middleware wrap, provider stamp); `ProviderAdapter` override machinery `:18-24,31-63,194-280,822-833` (about 47 references to overrides) with no production implementer (`git grep` finds `Register` called only from tests; `SetDefaultProvider` has no non-test caller), though `agenttest` fakes and AGENTS.md's "scripted provider at the LLM boundary" use it.

Fix plan: factor the shared preamble into `prepareDispatch(ctx, req) (ctx, req, target, err)`; leave the override seam alone (it is the sanctioned test boundary) but delete `SetDefaultProvider` if Jesse agrees it is unused. Size about -50 LOC. Risk: low.

### llm-core-providers-15 (low) Three hand-written goroutine "pump" stream wrappers

Locations: `llm/client.go:835-898` `providerStampStream`, `llm/apilog.go:289-352` `apiAttemptSettlementStream`, `llm/chan_stream.go` `ChanStream` (`type ChanStream struct{ chanStream }` embeds an unexported twin for no visible reason). Each repeats `out chan`, `done`, `closing`, `sync.Once`, a `select` on closing/inner/out.

Fix plan: one `mapStream(inner Stream, onEvent func(*StreamEvent), onEnd func(clean bool)) Stream` helper; both wrappers become 10-line callbacks. Collapse `chanStream`/`ChanStream`. TDD: existing `chan_stream_test.go`, close-without-drain tests. Size about -90 LOC. Risk: medium (goroutine lifecycle); keep the leak-on-Close tests.

### llm-core-providers-16 (medium) Stream and non-stream chat decoders disagree on reasoning precedence

Locations: `chatcompletions/response.go:171` `reasoningFromDelta` (stream) vs `:204` `extractReasoning` (non-stream).

Evidence: for the same fields, non-stream checks `reasoning_details` first and joins pieces with `"\n"`; the stream version checks `reasoning_content/reasoning/reasoning_text` first, falls back to details, and joins details without a separator. A provider that sends both yields different text and different replay field depending on how the call was made. The `difftest` differential suite exists to catch this class but evidently does not cover `reasoning_details`.

Fix plan: red test in `difftest` (same logical response, both paths) then have both call one `reasoningFromFields(content, reasoning, text string, details []detail)` helper. Size about -30 LOC. Risk: medium (user-visible reasoning text on OpenRouter MiniMax rows); needs Jesse sign-off on which precedence wins.

### llm-core-providers-17 (medium) `protocolhttp.Do` and `Stream` duplicate exchange/error handling; Do special-cases an operation string

Locations: `protocolhttp/exchange.go:46-103` `Do` and `protocolhttp/stream.go:25-65` `Stream`: both do prepare, `ClientWithAdapterTimeout`, `DoWithAPIAttempts`, `WrapContextError` on transport error, `ReadAll`, `classify`, idle-timeout detection (`errors.Is(readErr, llm.ErrResponseIdleTimeout)`), `attempt.Complete` with a hand-built timeout source. `exchange.go:91` `if readErr != nil && c.Operation == "models.list"` matches a free-text label to change control flow.

Impact: a bug fix to non-2xx handling (for example Retry-After headers or idle timeout) has to be made twice; string-keyed behavior is fragile (a renamed Operation silently changes semantics).

Fix plan: extract `handleNon2xx(...)` returning the typed error and the attempt result, used by both; replace the Operation check with an explicit `Call.TolerateBodyReadError bool` (or drop the special case if no test depends on it). Size about -50 LOC. Risk: medium; CI wire tests.

### llm-core-providers-18 (medium) Token estimator re-encodes each provider's wire behavior by model and provider name

Locations: `llm/token_count.go` (891 lines): `targetInfo` (`:400`), `mediaFamily` (`:457`), `mediaFamilyFromModelFamily` (`:502`), `providerTokenFamily` (`:773`), `unsignedThinkingReplayedByName` (`:696`), `oSeriesModelName` (`:842`), `dropsToolResultImages` (`:568`), `billsToolResultMedia`/`DeliversToolResultMedia`.

Evidence: doc comments cite other packages' files by name ("chatcompletions/messages.go", "google/request.go") as the thing being mirrored; commit `36d50ce494 "fix(google): deliver PDF documents without duplicate tool media"` had to edit both `google/request.go` and `token_count.go` in lockstep (the worktree diff against the checked-out branch shows `billsToolResultMedia` gaining `|| t.protocol == registry.ProtocolGoogle`). `strings.Contains(p, "anthropic")` ladders appear in three functions, parallel to `registry/modelsdev.go:157` `surfaceForFamily`.

Impact: two sources of truth for what a provider puts on the wire; drift lands as silent over/under-estimation.

Fix plan: expose the delivery decisions as `Caps`/`Resolved` facts computed once next to each builder (`registry` already has the family/surface rule), and have both the builders and the estimator read them; delete the name-based ladder in favor of the resolved row, keeping a single minimal fallback for name-only callers. This is design work: discuss with Jesse first. Size about -250 LOC. Risk: medium-high.

### llm-core-providers-19 (low) Continuation scaffolding with rollout flags and unused fields

Locations: `llm/responses_continuation.go:59-67,212-224` `ResponsesContinuationSupport{StorageShapeProven, ProductionPathProven, StorageShapeProofID, ProductionPathProofID}`; `:125-130` `ContinuationStoreOverride.ProviderOptionKeysByContinuation`; `:161` package-level `PlanResponsesContinuation(input)` ("does not yet compute the request fingerprint…"); `:196` `ClearResponsesContinuationStoreOverride`.

Evidence: `ProductionPathProven`, both `*ProofID` strings (`"2026-06-24-responses-continuation-phase-0b"`, `"…phase-12a-public"`) and `ProviderOptionKeysByContinuation` are never read in production (only `StorageShapeProven` is, at `agent/session_model_call.go:610`); `PlanResponsesContinuation(input)` and `Clear…` are only called from tests. Dated rollout narrative lives in code.

Fix plan: delete the unread fields and IDs, the package-level planner function, and `Clear…`; if only one family is ever enabled, collapse the registry map to a function. Update tests in `responses_continuation*_test.go`. Size about -120 LOC. Risk: low.

### llm-core-providers-20 (low) Stale package docs and history-narrating comments

Evidence (48 hits of history language in non-registry production files, a sample):
- `providers/internal/openaichat/openaichat.go:2` "shared by … the openai and openaicompat adapters" (neither exists).
- `chatcompletions/protocol.go:3-6` "consolidates the two Chat Completions builders that existed before the registry"; `responses/protocol.go:3-4` "Responses half of the pre-registry openai adapter"; `chatcompletions/reasoning.go:9` "table kept verbatim from openaicompat's applyThinkingFormat"; `responses_continuation_plan.go:29` "computed from the pre-registry builder".
- `providers/wirecapture/doc.go` and `providers/difftest/doc.go` exist only so `go build ./...` has a non-test file (acceptable, but note).
- 118 test comments cite production line numbers.

Fix plan: rewrite each comment to say what the code does now; history belongs in git. Strip line-number references from tests (they rot on the next edit). Size about 60 lines touched. Risk: none.

### llm-core-providers-21 (medium) Test suite weight: coverage-chasing files, copy assertions, dead code kept alive by fuzz targets

Evidence
- Root package tests 30,288 lines vs 10,777 production. 28 files named `*cov*`/`*coverage*` plus `cov_llm_*`, `cov_tt_llm_test.go`, `misc_covtest_test.go`, `remaining_coverage_test.go` (111), `adapter_timeout_covtest`, `api_attempt_covtest`, etc.
- Their stated purpose is line coverage ("covers lines 229-231", `remaining_coverage_test.go`; "covers the declaredKind method … otherwise never called", `cov_tt_llm_test.go`).
- Copy assertions, against the AGENTS.md rule: `remaining_coverage_test.go` `stringContains(msg, "usage limit reached")`; `cov_tt_llm_test.go` `e.Error() != "configuration error: bad setup"`; `usagelimit`-related message checks.
- `providers/anthropic/areq_targets_fuzz_test.go` (504 lines): header says its `areq` prefix is an "anti-collision token" so "parallel lanes editing package anthropic cannot collide"; one of its four targets, `FuzzAreqClampEffort`, only exists to fuzz the dead `clampEffort` (finding 4).
- 46 fuzz files; several target pure pass-through functions.

Fix plan: do not delete blindly. Per file: (1) keep tests that assert behavior; (2) fold survivors into the topical test file (`errors_test.go`, `usagelimit_test.go`); (3) drop tests that exist only to execute a getter or an unexported method; (4) rewrite copy assertions to assert on `Kind`/status/type. Do after findings 1-4 so deleted code takes its tests with it. Size about -3,000 test LOC. Risk: low if coverage gates are not tied to those files (check `docs/developing-evener/coverage.md` first).

### llm-core-providers-22 (medium) API-log attempt machinery lives in the root `llm` package

Locations: `llm/api_attempt.go` (692), `api_attempt_sanitize.go` (420), `api_attempt_scope.go` (38), `apilog.go` (534), `apilog_open_{unix,other}.go` (104), `adapter_timeout.go` (271), `response_idle.go` (171), `endpoint_url.go` (31): 2,390 lines and 75 exported declarations in a package whose job is the LLM client; tests for this area are 6,592 lines.

Evidence: the only non-test importers of these symbols are `llm/providers/internal/{transport,protocolhttp}` and `cmdutil/api_logging.go` (`NewSessionAPILogger`), per `git grep`. `llm/apilog` already exists for the record schema, so the split between `llm`, `llm/apilog` and `providers/internal/transport` is by accident of history (the middleware needs `Client`'s context hooks; everything else does not).

Impact: `llm` exports a large surface that is really internal plumbing, and `transport.APIAttemptCapture` wraps `llm.APIAttempt` wraps `apilog.APIAttemptRecord`: three layers for one record.

Fix plan: discuss first (architecture). Proposed: move the attempt group, sink, sanitize, and logger into `llm/apilog` (or `llm/attempt`) with the context keys; keep in `llm` only the `Middleware` adapter that binds the group. Collapse `APIAttemptCapture` and `APIAttempt`. Mechanical but large; do it after findings 3, 4 and 11 shrink it. Size about 2,400 LOC moved, -300 net. Risk: medium-high, wide import churn, but compile-checked.

### llm-core-providers-23 (low) 18 copy-with-provider methods and provider stamping at about 14 sites

Locations: `errors.go:296-343` (13 one-line `copyWithProvider`), `sdk_errors.go:93-118` (6 more); `RewriteErrorProvider` call sites: `client.go:359,400,564,583,874`, `token_count.go:180`, `google/protocol.go:42`, `anthropic/protocol_request.go:25`, `chatcompletions/request.go:23`, `responses/request.go:24`, four in-band error wraps (`responses/stream.go:534,552`, `google/stream.go:78`, `anthropic/stream.go:426`), `google/protocol_transport.go:42`.

Evidence: `Client.Complete`/`Stream` already stamp the instance on every error and response (`client.go:359,400,874`), so the provider-level stamps matter only for direct `BuildBody`/`Protocol` use. The four `BuildBody` copies even carry the same 4-line comment, pasted.

Fix plan: collapse the copy methods with a small generic `withProvider[T httpError](e T, name string) T` if it stays readable (otherwise leave, the test `TestEveryProviderAttributedErrorCopies` guards it); delete the per-builder `defer RewriteErrorProvider` and the pasted comment if `Client` is the only entry point (check `planContinuation`'s direct `BuildBody` call first). Size about -40 LOC. Risk: low.

### llm-core-providers-24 (low) Model-specific XML argument rescue inside the chat protocol package

Locations: `chatcompletions/rescue.go` (346 lines), called from `response.go:266` and `stream.go:91`.

Evidence: written to repair MiniMax M2.7 output that mixes `<parameter name=...>` XML into JSON tool arguments; two commits (`7390e38017`, `f68653aead`) hardened it for byte faithfulness. It is a quirk handler with its own regex, UTF-16 surrogate and raw-byte logic, not a protocol rule.

Fix plan: not a deletion candidate; make it a registry quirk (`Caps.RescueXMLToolArgs`) so only rows that need it pay for it, and document the owner in `docs/llm-providers.md`. Defer unless Jesse wants it. Size neutral. Risk: low.

---

## Recommended fix sequence

1. Dead-code sweep (no behavior change, shrinks everything after it): findings 4, 3, 19, then 1 (with Jesse's go-ahead to delete), then decide 2.
2. Security-sensitive de-duplication: finding 11 (credential material merge), then 12 (status/kind tables) behind new table-driven tests.
3. Shared protocol plumbing: findings 7 and 17 (listing, boilerplate, exchange handling), writing the red Vertex-timeout test first.
4. Decoder consolidation: finding 5 (runner `Finish`/`DecodeObject`), with 9 (single number decode) folded in, one provider per commit under `difftest`.
5. Behavior-parity fixes found along the way, each TDD with Jesse's decision on precedence: findings 8 (image resolve) and 16 (reasoning precedence).
6. Shaping cleanup: finding 13, then 6 (split the long converters) and 10 (usage estimate helper).
7. Small structural items: 14, 15, 23.
8. Judgment calls to discuss with Jesse before starting: 2 (tool loop), 18 (estimator vs wire truth), 22 (API-log package move), 24 (rescue as quirk).
9. Test cleanup last (finding 21), so deleted production code takes its tests with it, and update `llm/README.md`/`doc.go` and the `docs/llm-providers.md` references in the same PRs as the code they describe (finding 20 comments rewritten as each file is touched).
