# Google Interactions API and `google-vertex-express` Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Evener's legacy Google `generateContent` / `streamGenerateContent` adapter under `ProtocolGoogle` with Google's unified Interactions API (`POST /interactions`), omit direct historical Google Grounding outputs on model replay, and retire the built-in `google-vertex-express` preset (Jesse's 2026-11-20 Lean Core scope ruling).

**Architecture:** `llm/providers/google` stays the sole implementation of `registry.ProtocolGoogle` (`"google"`), sending stateless `"store": false` `steps[]` timelines (`user_input`, `thought` with `signature` and `summary`, `function_call`, `function_result`, `model_output`, and opted-in `google_search_*`) by default while supporting stateful `previous_interaction_id` (`"store": true`) continuations and strict `(Instance, WireID)` replay provenance. Ahead of flipping the Google wire format in Task 5, `apptranscript` citation/signature-only reasoning readers (Task 2), shared JSON/SSE settlement + 8-way usage reconciliation + `doctor apilog --recompute` (Task 3), and `Session` legacy `web_search` removal + ~40-LoC direct `expandHistory` Grounding omission + non-authorizing `ProtocolGoogle` tool-call suppression (Task 4) land in isolated, green checkpoints so every commit compiles and keeps every production symbol wired to a live caller.

**Tech Stack:** Go 1.24+ (`llm`, `llm/registry`, `llm/providers/google`, `agent`, `internal/apptranscript`, `envvars`, `cmd/evener`, `cmd/evener-hub`, `cmd/llmcall`, `cmd/evener-fuzz-harvest`), TypeScript/React (`cmd/evener-hub/frontend`), VitePress (`make docs-check`).

**Spec:** `docs/superpowers/specs/2026-11-19-google-interactions-api-design.md`

## Global Constraints

- **Clean break, zero legacy shims:** No `generateContent` / `streamGenerateContent` wire fallback, no deprecated camelCase `[providers.<name>.fields]` or `[providers.<name>.body]` shims (`generationConfig.thinkingConfig`, `safetySettings`, `toolConfig`, `cachedContent`), no synthesized `thought` steps from legacy `ToolCallData.ThoughtSignature`, and no deprecation alias for `google-vertex-express`.
- **Lean protocol core only (Jesse's 2026-11-20 scope ruling):** Do NOT build transitive historical Grounding taint-tracking across follow-up turns, memory frontmatter digests (`MemoryPageContentDigest`), `<shared-notes>`, `delegates.jsonl`, `doctor_evener`, `read_transcript`, `find_session_transcripts`, `api_log`, or Manager Hub RPCs; do NOT add cold Hub `resumeThread` model-override plumbing or a `delegate_send(model=...)` write-ahead journal state machine; and do NOT add a cross-transport `*llm.AcceptedInteractionError` / `StreamRunner.DeadlineError` / `Call.BodyTimeoutError` hierarchy.
- **Supported platforms:** Linux and Darwin only (`AGENTS.md`); keep `GOOS=windows` compile compatibility where tagged files exist, without adding Windows-only features or tests.
- **No dead production symbols (`AGENTS.md`):** Every production type, function, method, or field added in a task MUST have a live production caller in that same commit.
- **Copy is not a test oracle (`docs/developing-evener/testing.md` & `internal/devtool/copyonly/errstring_allowlist.tsv`):** Never assert on error-message phrasing in tests (assert typed errors, status codes, fields, and observable state). Whenever a task modifies or deletes a test file listed in `internal/devtool/copyonly/errstring_allowlist.tsv`, run `go test ./internal/devtool/copyonly/...` so exact allowlist counts never drift.
- **Deterministic offline default tests:** Default `go test ./...` runs MUST make zero network calls and never depend on ambient provider API keys; live Google/Vertex verification is gated behind `EVENER_LIVE_TESTS=1` plus explicit credentials.
- **Exact Interactions wire contract:** `Endpoint` and `StreamEndpoint` are `"/interactions"`, `CountTokensEndpoint` is `"-"` (`Protocol.CountTokens` returns `llm.RewriteErrorProvider(llm.ErrInputTokenCountUnsupported, res.Instance)`), `body["model"] = res.WireID` unconditionally, unanchored calls default to `"store": false` (unless `fields.store = false` omits `"store"`), anchored `previous_interaction_id` calls set `"store": true`, `function_result.result` serializes non-image structured values as JSON `string` (and raster `image/*` tool results as `[{"type":"text",...},{"type":"image",...}]`), `thinking_level` uses lowercase `minimal|low|medium|high|xhigh`, combined `google_search` + `function` tools set `generation_config.tool_choice = "validated"`, `[providers.google]` and `[providers."google-vertex"]` default `cheap_model = "gemini-3.5-flash-lite"` and `web_search = false`, and `[providers."google-vertex".fields]` defaults `safety_settings = false`.
- **Direct Grounding non-replay & non-destructive storage:** `buildBodyWithPolicy` rejects any `RoleAssistant` carrying `ContentWebSearch` with `*llm.ConfigurationError`; `Session` removes its legacy Google `web_search` function tool outright and omits `ContentWebSearch` + legacy unprefixed `"web_search"` tool pairs directly in `expandHistory` while leaving `.transcript.jsonl` untouched on disk.
- **Evergreen docs in the same change (`AGENTS.md`):** Task 5 updates `docs/llm-providers.md`, `docs/environment-variables.md` (`make generate`), `docs/product/subsystems.md` (`S15`, `S16`), `docs/developing-evener/environment.md`, and `docs/developing-evener/config-knobs.md` (`make docs-check`).

## Review Focus

1. **Consecutive `RoleUser` turns separated by a skipped-thinking `RoleAssistant` turn** — when tool results are followed by an assistant turn whose non-Interactions thinking is stripped and then deferred `<job-notification>` steering, `buildBodyWithPolicy` must coalesce adjacent user items into a single `user_input` step after `function_result` rather than emitting adjacent `user_input` steps (pinned in Tasks 5 and 6).
2. **SSE per-slot `Start -> Delta(s) -> End` event ordering when `step.start` carries an opening `"arguments": {}` placeholder or `summariesDisabled` is active** — `step.start` `{}` with zero deltas must wait for `interaction.completed.steps` before emitting a single `StreamEventToolCallEnd`, delta-confirmed `"{}"` finalizes at `step.stop`, and a live-streamed `step 1` queued behind an unresolved `step 0` drains `slot.deferredEvents` before terminal backfills so `TextStream()` and tool-call spans match settled `resp.Message` (pinned in Task 3).
3. **Cross-protocol and cross-model `(Instance, WireID)` signature isolation** — Google `ThinkingData` and `ToolCallData.ThoughtSignature` must replay only onto the exact originating `(Instance, WireID)` (including rebinding `scope.WireID` across `Session` `ModelFallbacks`), never onto Anthropic `thinking`, Chat `reasoning_content` / `extra_content.google.thought_signature`, Responses plaintext, or non-Google `token_count.go` estimates (pinned in Tasks 3 and 5).
4. **Non-authorizing `ProtocolGoogle` terminal statuses (`incomplete`, `budget_exceeded`, `requires_continuation`, `cancelled`, and `FinishReasonContentFilter`)** — `Session` must strip `ContentToolCall` parts and execute **zero** tool handlers across all statuses outside `{"completed", "requires_action"}`, set `skipHistory = false` when surviving `ContentThinking` remains without text, and settle `incomplete`/`budget_exceeded`/`requires_continuation`/`cancelled` at a failure boundary after **one** model call (pinned in Task 4).
5. **Restoring pre-cutover sessions with saved `google-vertex-express` / `google-vertex/gemini-2.5-*` fallbacks, `CheapModel` overrides, legacy `web_search` tool turns, or legacy signed tool calls** — restore prunes unservable saved `ModelFallbacks` and `CheapModel` overrides with a warning (keeping explicit `--model-fallback` strict), `expandHistory` omits `ContentWebSearch` and legacy unprefixed `"web_search"` tool payloads without touching `ResultToolName: "web_search"` or `.transcript.jsonl`, and `Complete` / zero-output `Stream` recover transparently in 1 retry when an endpoint rejects unsigned legacy `function_call` steps with HTTP `400` (pinned in Tasks 4, 5, and 6).

---

### Task 1: Shared `llm.StampContinuationIDHash` and `llm/registry` Capability & Constant-Cloning Prerequisites (COMPLETE at `9163f99c06`)

**Files:**
- Created: `llm/continuation_hash.go`, `llm/continuation_hash_test.go`
- Modified: `llm/providers/responses/complete.go`, `llm/providers/responses/stream.go`, `llm/registry/types.go`, `llm/registry/schema.go`, `llm/registry/derive.go`, `llm/registry/prune.go`, `llm/registry/derive_test.go`, `llm/registry/prune_test.go`

**Interfaces:**
- Produces:
  - `llm.StampContinuationIDHash(ctx context.Context, resp *llm.Response, fallback *llm.ContinuationHasher)` in `llm/continuation_hash.go`
  - `Caps.CombinedSearchAndFunctions *bool` (`toml:"combined_search_and_functions" json:"combined_search_and_functions,omitempty"`) in `llm/registry/types.go`
  - `registry.IsVertexGeminiOmni(preset, wireID string) bool` in `llm/registry/derive.go`
  - Deep-cloning of `map[string]any` and `[]any` constants inside `registry.ApplyBodyConstants(body, constants map[string]any)` in `llm/registry/prune.go`

- [x] **Steps 1-5: Implemented, verified (`go test ./llm/...`), reviewed clean, and committed at `9163f99c06`.**

---

### Task 2: `WebSearchSource`, `ThinkingData.GoogleInteraction`, and `apptranscript` Readers

**Files:**
- Modify: `llm/types.go`
- Modify: `internal/apptranscript/apptranscript.go`
- Modify: `internal/apptranscript/apptranscript_test.go`

**Interfaces:**
- Consumes: `llm.WebSearchData`, `llm.ThinkingData` (`llm/types.go`).
- Produces:
  - `llm.WebSearchSource{Title string, URL string}` (`json:"title,omitempty"`, `json:"url,omitempty"`) and `WebSearchData.Sources []WebSearchSource` (`json:"sources,omitempty"`) in `llm/types.go`
  - `ThinkingData.GoogleInteraction bool` (`json:"google_interaction,omitempty"`) in `llm/types.go`
  - Updated `apptranscript.MetadataOnlyResponsesReasoning` and `apptranscript.WebSearchProjection` in `internal/apptranscript/apptranscript.go`

- [ ] **Step 1: Write failing tests in `internal/apptranscript/apptranscript_test.go`**

Add tests verifying Spec §5.2:
- `TestMetadataOnlyResponsesReasoning_GoogleSignatureOnlyThought`: verifies `ThinkingData{GoogleInteraction: true, Signature: "sig_abc"}` (empty `Text`, empty `Summary`, empty `EncryptedContent`) returns `true` and does not split adjacent assistant text items, whereas `ThinkingData{GoogleInteraction: true, Text: "plan", Summary: []string{"plan"}, Signature: "sig_abc"}` returns `false` and renders reasoning text.
- `TestWebSearchProjection_PrefersSourcesAndFallsBackToLegacyGroundingChunks`: verifies `WebSearchData{Query: "go 1.24", Sources: []llm.WebSearchSource{{Title: "Go", URL: "https://go.dev"}}, Raw: ...search_suggestions HTML...}` renders markdown source links from `ws.Sources` without injecting raw `search_suggestions` HTML, while legacy `WebSearchData{Sources: nil, Raw: legacyGroundingChunks}` continues rendering via the `ws.Raw` fallback.

- [ ] **Step 2: Run `apptranscript` tests to verify RED failure**

Run: `go test ./internal/apptranscript/...`
Expected: FAIL (missing `WebSearchSource` / `Sources` / `GoogleInteraction`).

- [ ] **Step 3: Implement types in `llm/types.go` and projections in `internal/apptranscript/apptranscript.go`**

- Add `WebSearchSource`, `WebSearchData.Sources`, and `ThinkingData.GoogleInteraction` in `llm/types.go`.
- Extend `MetadataOnlyResponsesReasoning` in `internal/apptranscript/apptranscript.go` to return `true` when `t.Text == "" && len(t.Summary) == 0 && t.EncryptedContent == "" && (t.GoogleInteraction || t.Signature != "")`.
- Update `WebSearchProjection` in `internal/apptranscript/apptranscript.go` to format `ws.Sources` via `webSearchResultLine(s.Title, s.URL)` when `len(ws.Sources) > 0`, falling back to `ws.Raw` otherwise.

- [ ] **Step 4: Run tests to verify GREEN**

Run: `go test ./llm/... ./internal/apptranscript/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/types.go internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go
git commit -m "feat(apptranscript,llm): project WebSearchData.Sources citations and hide signature-only Google thoughts"
```

---

### Task 3: Interactions JSON/SSE Settlement, 8-Way Usage Reconciliation, Cross-Protocol `!GoogleInteraction` Destination Guards, and `doctor/apilog` Recompute

**Files:**
- Modify: `llm/types.go`
- Modify: `llm/token_count.go`
- Modify: `llm/token_count_test.go`
- Modify: `llm/providers/anthropic/request.go`
- Modify: `llm/providers/anthropic/request_test.go`
- Modify: `llm/providers/chatcompletions/messages.go`
- Modify: `llm/providers/chatcompletions/messages_test.go`
- Create: `llm/providers/google/interactions_response.go`
- Create: `llm/providers/google/interactions_stream.go`
- Create: `llm/providers/google/recompute.go`
- Create: `llm/providers/google/interactions_settlement_test.go`
- Modify: `agent/doctor/apilog.go`
- Modify: `agent/doctor/apilog_recompute_test.go`

**Interfaces:**
- Consumes: `llm.StampContinuationIDHash` (Task 1), `llm.WebSearchSource`, `ThinkingData.GoogleInteraction` (Task 2).
- Produces:
  - `ContentPart.GoogleStep int` (`json:"google_step,omitempty"`), `ThinkingData.{GoogleInstance, GoogleModel string}` (`ChatReplayText` and `ReplayableResponsesPlaintext` `!GoogleInteraction` guards), `ToolCallData.{GoogleInteraction bool, GoogleInstance, GoogleModel string}` in `llm/types.go`
  - Shared settlement helpers in `llm/providers/google`: `fromInteraction`, `appendInteractionSteps`, `extractThoughtSummaryItems`, `extractCitationAnnotations`, `normalizeGoogleSearchResultStep`, `settleInteractionStatus`, `classifyStepRPCStatus`, `classifyInteractionError`, `parseInteractionUsage`, `interactionStreamAccumulator`, `ErrPendingInteractionStatus`, `ErrTerminalStatusStepFailure`, `ErrUnexpectedInteractionStatus`, and `google.ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error)`
  - `"google_interactions"` and `"google_interactions_stream"` registered in `recomputeExtractors` (`agent/doctor/apilog.go`)

- [ ] **Step 1: Write failing tests for JSON/SSE settlement, 8-way usage reconciliation, SSE slot ordering, `doctor/apilog` recompute, and cross-protocol `!GoogleInteraction` guards**

Cover Spec §3, §4, and §5.1:
- `llm/providers/google/interactions_settlement_test.go`:
  - `fromInteraction` & `interactionStreamAccumulator` parity across Vertex singleton & Gemini array `thought.summary` (`extractThoughtSummaryItems`), `model_output` `GoogleStep: stepIdx + 1`, mixed `text -> image -> text` boundaries (`[]*strings.Builder` + `textNeedsNewBuilder`), Vertex singleton & Gemini array `google_search_result` (`normalizeGoogleSearchResultStep`) and `url_citation` (`extractCitationAnnotations` attaching `Sources` or inserting standalone `ContentWebSearch` carrier), `function_call` raw argument preservation (`captureInteractionArgsRaw` + `NormalizeToolArguments`, preserving invalid streamed JSON verbatim with `RawArguments == ""`).
  - SSE per-slot `Start -> Delta(s) -> End` event queue (`acc.nextEmitIndex` + `slot.deferredEvents`): (a) `step.start` opening `"arguments": {}` placeholder with zero deltas defers `StreamEventToolCallEnd` until `interaction.completed.steps` supplies `arguments`, whereas delta-confirmed `"{}"` or non-empty `step.start` `arguments` emits `StreamEventToolCallEnd` immediately at `step.stop`; (b) open unstopped `function_call` slot with incomplete fragments adopts complete terminal `interaction.completed.steps` arguments while preserving malformed `argrepair` bytes when no complete terminal object exists; (c) mixed stream with unresolved `step 0` + live `step 1` drains `slot.deferredEvents` before terminal backfills so `StreamResult.TextStream()` yields `step 0` then `step 1` once and deferred `step 1` tool calls emit `ToolCallStart -> ToolCallDelta -> ToolCallEnd` in exact order; (d) `thinking_summaries: "none"` (`summariesDisabled`) advances `acc.nextEmitIndex` immediately at `step.stop` while summary-enabled signed thoughts hold until `interaction.completed`; (e) started text/reasoning spans without `step.stop` emit `TextEnd`/`ReasoningEnd` before `Finish`.
  - Status & error classification: `"completed"`, `"requires_action"`, `"incomplete"`/`"budget_exceeded"`/`"requires_continuation"` (`FinishReasonLength` + `continuation_token` retention + permanent `ErrTerminalStatusStepFailure` on empty gRPC code `4` step error), `"cancelled"`, `"queued"`/`"in_progress"` (`ErrPendingInteractionStatus` with `StatusCode() == 0 && !Retryable()`), `"failed"` (`not_found` -> 404 `KindNotFound`, `gateway_timeout`/`deadline_exceeded` -> 408 `KindTimeout`, safety codes -> `FinishReasonContentFilter`), missing/unknown status -> `ErrUnexpectedInteractionStatus`, media-only output -> `ConfigurationError`.
  - All 8 `parseInteractionUsage` billing cases (`100/50/25/125`, `10/20/25/20/55`, omitted `total` `10/20/25`, `thought > rawOut` `10/10/20/30 -> Output=30, Total=40`, additive cache `100/25/5/10/135`, cached subtraction, `StepStop.usage` retention).
- `agent/doctor/apilog_recompute_test.go`: `"google_interactions"` JSON and `"google_interactions_stream"` SSE recompute accurately while historical `"google_generate_content"` / `"google_stream_generate_content"` rows are skipped cleanly (`false`) in mixed logs, and mis-filed `choices`/`candidates` JSON bodies fail recompute.
- Cross-protocol `!GoogleInteraction` tests in `anthropic/request_test.go`, `chatcompletions/messages_test.go`, and `llm/token_count_test.go`: Google `ThinkingData{GoogleInteraction: true, Summary: ..., Signature: ...}` and `ToolCallData{GoogleInteraction: true, ThoughtSignature: ...}` never emit Anthropic `thinking`, Chat `reasoning_content` (even with `thinking_as_text`), or `extra_content.google.thought_signature`, and add **zero** estimated input tokens in `token_count.go` for non-Google targets while native Anthropic/Chat/Responses reasoning still bills and replays unchanged.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm/... -run 'TestInteraction|TestExtractRecordedResponse|TestGoogleInteraction' && go test ./agent/doctor -run TestRecompute`
Expected: FAIL (missing Interactions settlement files, types, and recompute registrations).

- [ ] **Step 3: Implement settlement helpers, `!GoogleInteraction` guards, and `doctor/apilog` wiring**

- Update `llm/types.go`, `llm/token_count.go` (non-Google upfront `!GoogleInteraction` guard in `estimateMessageInputParts`, `chatReasoningEstimate.add`, and `thinkingReplayChars`), `llm/providers/anthropic/request.go`, and `llm/providers/chatcompletions/messages.go` per Spec §5.1 & §5.3.
- Implement `llm/providers/google/interactions_response.go`, `llm/providers/google/interactions_stream.go`, and `llm/providers/google/recompute.go` per Spec §3 and §4 (keeping legacy `response.go`/`stream.go` intact until Task 5's atomic cutover, while `ExtractRecordedResponse` in `recompute.go` exercises every new helper in production via `agent/doctor/apilog.go`).
- Register `"google_interactions"` and `"google_interactions_stream"` -> `google.ExtractRecordedResponse` in `agent/doctor/apilog.go`.

- [ ] **Step 4: Run `llm` and `agent/doctor` suites to verify GREEN**

Run: `go test ./llm/... ./agent/doctor/... ./internal/devtool/copyonly/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/ agent/doctor/
git commit -m "feat(google,doctor): add Interactions JSON/SSE settlement, usage reconciliation, cross-protocol provenance guards, and apilog recompute"
```

---

### Task 4: Remove Legacy `Session` Google `web_search` Tool, Direct `expandHistory` Grounding Omission, and `ProtocolGoogle` Non-Authorizing Tool-Call Suppression

**Files:**
- Delete: `agent/tool_web_search.go`
- Modify: `agent/session_tools_web.go`
- Modify: `agent/session_tool_registry.go`
- Modify: `agent/session.go`
- Modify: `agent/provider/profile.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/internal/tool/definitions_program_fuzz_test.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_namer.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/tool_web_search_test.go`
- Modify: `agent/provider/profile_test.go`
- Modify: `agent/profile_test.go`
- Modify: `agent/result_tool_name_test.go`
- Modify: `agent/cov_stweb_fuzz_test.go`
- Modify: `agent/sandbox_net_test.go`
- Modify: `agent/session_go_tail_coverage_fuzz_test.go`
- Modify: `agent/session_model_switch_queue_test.go`
- Modify: `agent/session_surface_behavior_test.go`
- Modify: `agent/provider_instance_integration_test.go`
- Modify: `agent/session_resolve_profile_test.go`
- Create: `agent/session_google_settlement_test.go`

**Interfaces:**
- Consumes: `llm.ContentWebSearch`, `registry.ProtocolGoogle`, `registry.TemperaturePath`.
- Produces:
  - Complete removal of `Session`'s client-side `web_search` function tool (`DefWebSearch`, `tool_web_search.go`, `capabilityWebSearch`, `reapplyProviderSpecificTools`)
  - Direct ~40-LoC outbound Grounding non-replay in `agent/session_model_call.go`: `webSearchReplayEligible` returns `false` for `ProtocolGoogle`; `projectTurnMessage` strips `ContentWebSearch` (replacing grounded `ProtocolGoogle` `TurnAssistant` non-empty text with `"[historical grounded output omitted from model replay]"`) and replaces historical unprefixed `"web_search"` `ContentToolCall` arguments + paired `ContentToolResult.Content` when `s.resultToolName() != "web_search"`, leaving `.transcript.jsonl` untouched on disk
  - `ProtocolGoogle` non-authorizing status tool-call suppression + `skipHistory = false` surviving-thinking persistence + single-call failure settlement in `agent/session_lifecycle.go`, and `registry.TemperaturePath(registry.ProtocolGoogle)` check in `agent/session_namer.go`

- [ ] **Step 1: Write failing tests in `agent/tool_web_search_test.go` and `agent/session_google_settlement_test.go`**

Cover Spec §5.3:
- `tool_web_search_test.go`: (a) `Session` registers no `web_search` function tool and dispatches zero side Grounding calls on `ProtocolGoogle` (even when `providers.toml` sets `web_search = true`); (b) restoring a session whose `.transcript.jsonl` contains a `ProtocolGoogle` `TurnAssistant` with `ContentWebSearch` and a legacy `"web_search"` tool call/result omits `ContentWebSearch` and replaces grounded assistant text + `"web_search"` tool payloads on outbound model replay while keeping `.transcript.jsonl` untouched on disk and keeping `SessionConfig{ResultToolName: "web_search"}` intact.
- `session_google_settlement_test.go`: across `"incomplete"`, `"budget_exceeded"`, `"requires_continuation"`, `"cancelled"`, and `FinishReasonContentFilter`, `Session` strips all `ContentToolCall` parts and executes **zero** tool handlers; `"incomplete"`, `"budget_exceeded"`, `"requires_continuation"`, and `"cancelled"` settle immediately at a failure boundary after **one** model call (while `FinishReasonContentFilter` routes through `handleNoToolCalls` with `len(calls) == 0`); and a response carrying `ContentThinking` + `ContentToolCall` with empty text sets `skipHistory = false`, persisting only the surviving `ContentThinking` on `.transcript.jsonl` reload.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./agent -run 'TestSessionGoogleSettlement|TestWebSearch'`
Expected: FAIL.

- [ ] **Step 3: Implement `web_search` deletion, direct `expandHistory` Grounding omission, `isTemperatureUnsupported`, and `session_lifecycle.go` `ProtocolGoogle` settlement**

Follow Spec §5.3:
- Delete `agent/tool_web_search.go` and remove `DefWebSearch`, `deps.webSearchEnabled`, `reapplyProviderSpecificTools`, and `capabilityWebSearch`, updating every listed `agent/` test file in the same commit (check `internal/devtool/copyonly/errstring_allowlist.tsv` if any allowlisted test file's assertion count changes).
- Gate `req.WebSearch` in `buildModelRequest` on `s.providerWebSearchEnabled(profile) && protocolInjectsNativeWebSearch(profile.Protocol())`, return `false` for `ProtocolGoogle` in `webSearchReplayEligible`, and strip `ContentWebSearch` + replace grounded `ProtocolGoogle` `TurnAssistant` text and historical unprefixed `"web_search"` tool call/result payloads directly in `projectTurnMessage` (`agent/session_model_call.go`).
- Update `isTemperatureUnsupported` (`agent/session_namer.go`) to check `registry.TemperaturePath(registry.ProtocolGoogle)`.
- Implement `ProtocolGoogle` non-authorizing status tool-call suppression, `skipHistory = false` when `len(resp.Message.Content) > 0`, and pre-`handleNoToolCalls` failure-boundary settlement in `agent/session_lifecycle.go`.

- [ ] **Step 4: Run full `agent` and `copyonly` test suites to verify GREEN**

Run: `go test ./agent/... ./internal/devtool/copyonly/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git status
git add -A agent/ internal/devtool/copyonly/
git commit -m "feat(agent): remove legacy Google web_search tool, omit Grounding on replay, and gate ProtocolGoogle tool dispatch"
```

---

### Task 5: Atomic `ProtocolGoogle` `/interactions` Cutover, `llm/registry` Overlay & `VertexSupportsGeminiModel`, `(Instance, WireID)` Provenance, `evener-fuzz-harvest`, and `google-vertex-express` Retirement

**Files:**
- Modify: `llm/types.go`
- Modify: `llm/token_count.go`
- Modify: `llm/generate.go`
- Modify: `llm/registry_shape.go`
- Modify: `llm/registry/resolve.go`
- Modify: `llm/registry/prune.go`
- Modify: `llm/registry/derive.go`
- Modify: `llm/registry/modelsdev.go`
- Modify: `llm/registry/data/providers_overlay.toml`
- Delete: `llm/registry/testdata/golden/google-vertex-express-gemini-2.5-flash.json`
- Modify: `llm/registry/testdata/golden/*.json` (via `go test ./llm/registry -update`)
- Modify: `llm/registry/resolve_test.go`, `golden_test.go`, `instances_test.go`, `overlay_test.go`, `prune_test.go`, `derive_test.go`, `registry_shape_test.go`
- Modify/Replace: `llm/providers/google/protocol.go`, `protocol_transport.go`, `request.go`, `response.go`, `stream.go`, `vertex_models.go`, and all `llm/providers/google/*_test.go` + `*_fuzz_test.go` + `testdata/`
- Modify: `llm/providers/wirecapture/` + `llm/providers/difftest/` + `llm/provider_wire_testkit_test.go` + `llm/provider_operations_apilog_test.go`
- Modify: `envvars/envvars.go`, `envvars/envvars_test.go`
- Modify: `agent/provider/profile.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_set_model.go`
- Modify: `agent/live_model_metadata.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_dod_definition_test.go`, `agent/session_effort_default_test.go`, `agent/session_namer_temperature_test.go`, `agent/session_vision_google_pdf_test.go`, `agent/provider/profile_test.go`
- Create: `agent/session_replay_provenance_test.go`
- Modify: `cmd/llmcall/main.go`, `cmd/llmcall/main_test.go`
- Modify: `cmd/evener-fuzz-harvest/raw.go`, `cmd/evener-fuzz-harvest/raw_test.go`, `cmd/evener-fuzz-harvest/coverage_test.go`
- Modify: `cmd/evener/models_live_test.go`
- Modify: `cmd/evener-hub/app_auth.go`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.test.tsx`
- Modify: `docs/llm-providers.md`, `docs/environment-variables.md` (via `make generate`), `docs/product/subsystems.md` (`S15`, `S16`), `docs/developing-evener/environment.md`, `docs/developing-evener/config-knobs.md`

**Interfaces:**
- Consumes: Tasks 1-4 (`StampContinuationIDHash`, `CombinedSearchAndFunctions`, `IsVertexGeminiOmni`, `interactions_response.go`, `interactions_stream.go`, `Session` settlement & Grounding omission).
- Produces:
  - `ThinkingData.ReplayableGoogleThoughtFor(instance, wireModel string) bool` in `llm/types.go`
  - `registry.VertexSupportsGeminiModel(id string) bool`, `registry.FieldGoogleThinkingLevel`, `registry.FieldGoogleThinkingSummaries`
  - Full `ProtocolGoogle` `/interactions` wire implementation (`BuildBody`, `Complete`, `Stream`, `CountTokens`) + 1-shot unsigned legacy tool-call `400` text-projection fallback + `FinalizeBody` policy locks
  - Saved `meta.CheapModel` and `meta.Config.ModelFallbacks` restore pruning on retired/unsupported targets in `agent/session_init.go`
  - Complete retirement of `google-vertex-express`, `GOOGLE_VERTEX_API_KEY`, and `GOOGLE_VERTEX_EXPRESS_BASE_URL`

- [ ] **Step 1: Write/update failing tests across `llm/registry`, `llm/providers/google`, `llm`, `agent`, `envvars`, `cmd/llmcall`, `cmd/evener-fuzz-harvest`, and `frontend`**

Cover every Spec §1, §2, §4.2, §5.1, and §5.3 bullet (plus Task 1's two minor test additions: `IsVertexGeminiOmni(PresetVertexGemini, " Gemini-Omni-1.2-Flash ")` in `derive_test.go` and a `[]any` slice mutation case in `prune_test.go`):
- `llm/providers/google/*_test.go` & 5 fuzz harnesses: `POST .../interactions`, `body["model"] = res.WireID`, unanchored `"store": false` default vs anchored `previous_interaction_id` (`"store": true`) vs `fields.store = false` omission, `FinalizeBody` locking `"stream"`, `"model"`, `"input"`, `"tools"`, `"response_format"`, `"previous_interaction_id"`, `"store"`, `"agent"`, `"background"`, `"continuation_token"`, `"cached_content"`, `"service_tier": "deferred"`, retired camelCase constants, `reasoning = false` stripping, `tool_choice: "validated"` vs `"auto"`, `reconcileOutputField`, Unicode code-point `"labels"` filtering (`"équipe"` kept, `"team/id"` skipped on `req.Metadata`, invalid explicit `ProviderOptions`/`Transport.Body` labels rejected), `ProviderOptions["google"]` deep-cloning, lazy `user_input` coalescing across skipped-thinking assistant turns, `RoleAssistant` `ContentImage` + `GoogleStep` boundaries, `(Instance, WireID)` `ReplayableGoogleThoughtFor` and `function_call.signature` replay, `ContentWebSearch` replay `ConfigurationError`, JSON-string `function_result.result` + raster-image subcontent + PDF side channel, cyclic tool/response schemas (`openaichat.CyclicSchema`), `sanitizeInteractionResponseSchema`, `RequiresStreamingCompleteContext` -> `CompleteViaStream`, premature `"queued"` stream EOF -> permanent `ErrPendingInteractionStatus` vs `"in_progress"` EOF -> retryable `StreamError`, 1-shot unsigned-legacy-tool-call `400` structured-text fallback on `Complete` and zero-output `Stream` (with single `StreamEventStreamStart`, and no adapter-internal retry once any output event was published), Vertex regional (`us`/`eu`/cross-border residency disclosure) warning & 404 remedy, `:generateContent` endpoint-override `ConfigurationError`, and `VertexSupportsGeminiModel` filtering (`gemini-1*`, `gemini-2*`, `gemini-3-flash-preview`, `gemini-3-pro-preview`).
- `agent/session_replay_provenance_test.go` & restore tests: primary `replayScope.WireID` + `ModelFallbacks` `scope.WireID` rebinding (`Model A -> Model B` keeps B's prior thoughts/signatures while omitting A's; same-`WireID` aliases retain signatures; `TestExpandHistory_InFlightTurn_NotFiltered` stays green); `VertexSupportsGeminiModel` live/cold switch rejection; restore pruning of saved `ModelFallbacks` and saved `SessionMeta.CheapModel` targeting retired `google-vertex-express` or `google-vertex/gemini-2.5-*` with a warning while keeping explicit `--model-fallback` strict.
- `llm/generate.go` & `cmd/llmcall`: active-`Execute` loop preflight rejection on anchored/combined `ProtocolGoogle` requests; `cmd/llmcall --web-search` pre-HTTP rejection on `ProtocolGoogle`.
- `cmd/evener-fuzz-harvest`: `"google_interactions_stream"` -> `dirGeminiStream`, `"google_interactions"` / `"google_generate_content"` / `"google_stream_generate_content"` terminal no-target matches, `isGoogleInteractionsStream`.
- `envvars`, `llm/registry`, `wirecapture`, `instanceDialogs.test.tsx`: `google-vertex-express` retired cleanly.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm/registry/... ./envvars/... ./cmd/llmcall/... ./cmd/evener-fuzz-harvest/...`
Expected: FAIL before cutover implementation.

- [ ] **Step 3: Implement the atomic cutover across `llm`, `llm/registry`, `llm/providers/google`, `envvars`, `agent`, `cmd`, `frontend`, goldens, and docs**

Implement every Task 5 file change per Spec §1, §2, §4.2, §5.1, and §5.3, run `go test ./llm/registry -update` (and `wirecapture` fixture update if applicable) to refresh surviving Google/Vertex goldens, run `make generate` to regenerate `docs/environment-variables.md`, update `docs/llm-providers.md`, `docs/product/subsystems.md` (`S15`, `S16`), `docs/developing-evener/environment.md`, and `docs/developing-evener/config-knobs.md`, and run `npx biome check --write` from `cmd/evener-hub/frontend` on touched TS files (also check `internal/devtool/copyonly/errstring_allowlist.tsv` if any allowlisted test file's assertion count changed).

- [ ] **Step 4: Run Go suites, `copyonly`, `make docs-check`, and `make test-web` sequentially (one heavy gate at a time)**

Run sequentially:
1. `go test ./llm/... ./envvars/... ./internal/apptranscript/... ./cmd/llmcall/... ./cmd/evener-fuzz-harvest/... ./internal/devtool/copyonly/...`
2. `go test ./agent/... ./cmd/evener/... ./cmd/evener-hub/...`
3. `make docs-check`
4. `make test-web`
Expected: PASS across every suite with pristine output.

- [ ] **Step 5: Commit**

```bash
git status
git add -A llm/ envvars/ agent/ cmd/ docs/ internal/devtool/copyonly/
git commit -m "feat(google,registry): switch ProtocolGoogle to Interactions API and retire google-vertex-express"
```

---

### Task 6: End-to-End Session Rollover & Legacy-Signature Restore Regressions + Opt-In Live Verification Suite

**Files:**
- Create: `agent/google_interactions_rollover_e2e_test.go`
- Create: `llm/providers/google/live_interactions_test.go`
- Create: `agent/google_interactions_live_test.go`
- Modify: `docs/llm-providers.md` (if live `EVENER_LIVE_TESTS=1` probe surfaces any surface-specific note)

**Interfaces:**
- Consumes: Full Interactions `ProtocolGoogle` adapter, `Session` lifecycle, and restore pipeline (Tasks 1-5).
- Produces:
  - Deterministic `Session` regression suite (`TestSessionGoogleInteractions_DeepMultiTurnNotificationRolloverRegression` & `TestSessionGoogleInteractions_PreCutoverSignedToolCallRestoreAndFallback`) in `agent/google_interactions_rollover_e2e_test.go`
  - Opt-in `EVENER_LIVE_TESTS=1` verification suite (`llm/providers/google/live_interactions_test.go` & `agent/google_interactions_live_test.go`) skipped cleanly by default offline runs

- [ ] **Step 1: Write deterministic end-to-end `Session` regression tests and opt-in `EVENER_LIVE_TESTS=1` live tests**

Per Spec Verification:
1. `TestSessionGoogleInteractions_DeepMultiTurnNotificationRolloverRegression` (`agent/google_interactions_rollover_e2e_test.go`): seeds a `Session` on `google/gemini-4-carbon` against a scripted `/interactions` HTTP server with a multi-turn history representative of session `03507HVKeWCiXCzGHXBqyK` (prior native `thought` + `function_call` + `function_result` + deferred `<job-notification>` / `<delegate-notification>` steering + new user turn), asserting exact outbound `steps[]` order, `"store": false`, `call_id`/`name` `function_result` pairing ahead of a **single** coalesced `user_input` step (zero adjacent `user_input` steps), native `thought` `signature`+`summary` round-trip, and clean next-turn settlement.
2. `TestSessionGoogleInteractions_PreCutoverSignedToolCallRestoreAndFallback` (`agent/google_interactions_rollover_e2e_test.go`): restores a representative pre-cutover `generateContent` `.transcript.jsonl` carrying `ToolCallData{ThoughtSignature: "legacy_sig", GoogleInteraction: false}` and historical Grounding on `google/gemini-3.8-flash`, verifying (a) historical Grounding (`ContentWebSearch` + `"web_search"` tool payloads) is omitted on outbound model replay while completed non-search tool results survive and `.transcript.jsonl` stays untouched on disk, (b) Attempt 1 omits `legacy_sig` on `function_call` steps, and (c) when the scripted endpoint returns HTTP `400` `INVALID_ARGUMENT` referencing `thought_signature`, `Complete` and zero-output `Stream` each recover transparently in 1 retry by rendering only unsigned legacy tool pairs into structured `model_output` / `user_input` blocks (with a single `StreamEventStreamStart` on `Stream`).
3. Opt-in `EVENER_LIVE_TESTS=1` tests (`llm/providers/google/live_interactions_test.go` and `agent/google_interactions_live_test.go`, immediately calling `t.Skip` unless `os.Getenv("EVENER_LIVE_TESTS") == "1"` and the respective `GEMINI_API_KEY`/`GOOGLE_API_KEY` or `GOOGLE_VERTEX_PROJECT` credentials are present): exercises on both `google` and `google-vertex` (`GOOGLE_VERTEX_LOCATION=global`) (a) a real `Session` turn with default `Profile.ProviderOptions()` (`safety_settings` wire check) plus direct `Complete` + `Stream` 2-turn stateless function round, (b) settling a post-upgrade turn on a pre-cutover legacy-signed tool-call history, and (c) a 2-turn stateful combined `google_search` + `function` round (`web_search = true` test config layer, requiring Turn 1 to emit both paired `google_search_call`/`google_search_result` and `function_call` before sending `previous_interaction_id` + `[function_result]` + active `Tools` + `WebSearch: true`).

- [ ] **Step 2: Run default offline tests (and opt-in live tests if credentials exist in environment) to verify GREEN**

Run: `go test -v ./agent -run 'TestSessionGoogleInteractions_' && go test -v ./llm/providers/google -run 'TestLiveInteractions'`
Expected: Deterministic E2E regressions PASS; live tests skip cleanly under default offline env (and run if explicitly invoked with `EVENER_LIVE_TESTS=1` and configured credentials).

- [ ] **Step 3: Commit**

```bash
git add agent/google_interactions_rollover_e2e_test.go agent/google_interactions_live_test.go llm/providers/google/live_interactions_test.go
git commit -m "test(google,agent): add multi-turn Interactions rollover, legacy-signature restore, and opt-in live verification suites"
```
