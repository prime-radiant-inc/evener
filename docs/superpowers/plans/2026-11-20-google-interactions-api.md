# Google Interactions API and `google-vertex-express` Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Evener's legacy Google `generateContent` / `streamGenerateContent` adapter under `ProtocolGoogle` with Google's unified Interactions API (`POST /interactions`), scrub pre-cutover Google Grounding from model-facing surfaces, provide cold/live saved-session and delegate model recovery, and retire the built-in `google-vertex-express` preset.

**Architecture:** `llm/providers/google` stays the sole implementation of `registry.ProtocolGoogle` (`"google"`), sending stateless `"store": false` `steps[]` timelines (`user_input`, `thought` with `signature` and `summary`, `function_call`, `function_result`, `model_output`, and opted-in `google_search_*`) by default while supporting stateful `previous_interaction_id` (`"store": true`) continuations and strict `(Instance, WireID)` replay provenance. Ahead of flipping the Google wire format, protocol-independent Hub/CLI/delegate model-recovery paths, `apptranscript` citation/signature-only reasoning readers, shared JSON/SSE settlement and `doctor apilog --recompute`, model-facing Grounding scrubbing, and `*llm.AcceptedInteractionError` terminal failure boundaries land in isolated, green checkpoints so every commit compiles and keeps every production symbol wired to a live caller.

**Tech Stack:** Go 1.24+ (`llm`, `llm/registry`, `llm/providers/google`, `agent`, `internal/apptranscript`, `envvars`, `cmd/evener`, `cmd/evener-hub`, `cmd/llmcall`, `cmd/evener-fuzz-harvest`), TypeScript/React (`cmd/evener-hub/frontend`), VitePress (`make docs-check`).

**Spec:** `docs/superpowers/specs/2026-11-19-google-interactions-api-design.md`

## Global Constraints

- **Clean break, zero legacy shims:** No `generateContent` / `streamGenerateContent` wire fallback, no deprecated camelCase `[providers.<name>.fields]` or `[providers.<name>.body]` shims (`generationConfig.thinkingConfig`, `safetySettings`, `toolConfig`, `cachedContent`), no synthesized `thought` steps from legacy `ToolCallData.ThoughtSignature`, and no deprecation alias for `google-vertex-express`.
- **Supported platforms:** Linux and Darwin only (`AGENTS.md`); keep `GOOS=windows` compile compatibility where tagged files exist, without adding Windows-only features or tests.
- **No dead production symbols (`AGENTS.md`):** Every production type, function, method, or field added in a task MUST have a live production caller in that same commit.
- **`schema.Turn` alphabetical field order:** `agent/schema/turn.go` enforces strict alphabetical JSON-key order (`turn_order_test.go`); `GroundingScrubbed bool` (`json:"grounding_scrubbed,omitempty"`) MUST sit between `GoalContinuation` (`"goal_continuation"`) and `Hook` (`"hook"`), and private correlation keys (`grounding_scrubbed`) are deleted in `publicTranscriptLine` alongside `attention_id`.
- **Copy is not a test oracle (`docs/developing-evener/testing.md` & `internal/devtool/copyonly/errstring_allowlist.tsv`):** Never assert on error-message phrasing in tests (assert typed errors, status codes, fields, and observable state). Whenever a task modifies or deletes a test file listed in `internal/devtool/copyonly/errstring_allowlist.tsv`, run `go test ./internal/devtool/copyonly/...` so exact allowlist counts never drift.
- **Deterministic offline default tests:** Default `go test ./...` runs MUST make zero network calls and never depend on ambient provider API keys; live Google/Vertex verification is gated behind `EVENER_LIVE_TESTS=1` plus explicit credentials.
- **Exact Interactions wire contract:** `Endpoint` and `StreamEndpoint` are `"/interactions"`, `CountTokensEndpoint` is `"-"` (`Protocol.CountTokens` returns `llm.RewriteErrorProvider(llm.ErrInputTokenCountUnsupported, res.Instance)`), `body["model"] = res.WireID` unconditionally, unanchored calls default to `"store": false` (unless `fields.store = false` omits `"store"`), anchored `previous_interaction_id` calls set `"store": true`, `function_result.result` serializes non-image structured values as JSON `string` (and raster `image/*` tool results as `[{"type":"text",...},{"type":"image",...}]`), `thinking_level` uses lowercase `minimal|low|medium|high|xhigh`, combined `google_search` + `function` tools set `generation_config.tool_choice = "validated"`, `[providers.google]` and `[providers."google-vertex"]` default `cheap_model = "gemini-3.5-flash-lite"` and `web_search = false`, and `[providers."google-vertex".fields]` defaults `safety_settings = false`.
- **Grounding non-replay & non-destructive storage:** `buildBodyWithPolicy` rejects any `RoleAssistant` carrying `ContentWebSearch` with `*llm.ConfigurationError`; `Session` removes its legacy Google `web_search` function tool outright; durable `.transcript.jsonl`, `<id>.log.jsonl`, `delegates.jsonl`, `.api.jsonl`, and memory `.md` files remain untouched on disk for human viewing while model-facing projections scrub pre-cutover Google Grounding exposure.
- **Evergreen docs in the same change (`AGENTS.md`):** Each stage updates its owning `docs/product/` guides, `docs/product/subsystems.md` rows, and generated docs (`make generate` / `make docs-check`) in the same task as the code.

## Review Focus

1. **Consecutive `RoleUser` turns separated by a skipped-thinking `RoleAssistant` turn** — when tool results are followed by an assistant turn whose non-Interactions thinking is stripped and then deferred `<job-notification>` steering, `buildBodyWithPolicy` must coalesce adjacent user items into a single `user_input` step after `function_result` rather than emitting adjacent `user_input` steps (pinned in Task 11 & Task 12).
2. **Cold Hub/CLI model switch on a session with a torn `.transcript.jsonl` tail or unsaved crash-window `TurnModelSwitch`** — rejecting an invalid or unsupported replacement model (such as `google-vertex/gemini-2.5-flash`) must leave `.transcript.jsonl` (torn tail included), `SessionMeta`, and queued input byte-for-byte untouched, while a valid cold switch reconciles any post-`DivergenceTurn` unsaved `TurnModelSwitch` under held ownership and records at most one `old -> replacement` marker (pinned in Task 2 & Task 11).
3. **`delegate_send` with `model` override racing `Stop` or daemon restart** — `Stop` before `CommitStart` writes zero journal/child mutations; `Stop` after `CommitStart` before `BeginAdmittedExecution` records the generation in `UnlaunchedStoppedGenerations` without running `SessionStart`/`SubagentStart` hooks or losing the admitted model on a later wake; and a subsequent child-authored switch `B -> C` is preserved (`!descriptorWins`) on later wakes without reverting `C -> B` (pinned in Task 3).
4. **Pre-cutover Grounding leaking through indirect model-facing channels (`TurnCommunicate`, mixed `delegate_send` `EarlierResults`, cross-project/partial-edit memory, OpenAI Responses anchors, or two-hub Manager paging)** — scrubbing must catch exposed follow-up turns across user prompts, strip all `ContentThinking`/`ContentRedThinking` and skip pre-cutover Responses anchors, preserve clean `EarlierResults` siblings while scrubbing grounded ones, match historical memory digests to the exact loaded bytes, and isolate Manager vs human Hub paging caches (pinned in Tasks 6, 7, 8, and 9).
5. **Post-acceptance stream disconnects, adapter timeouts, and non-authorizing terminal statuses (`incomplete`, `budget_exceeded`, `requires_continuation`, `cancelled`, safety `failed`)** — accepted stored calls and `sawQueuedOrTerminal` stateless streams must wrap failures in permanent `*llm.AcceptedInteractionError` (`StatusCode() == 0`, `Retryable() == false`) so neither `RetryStream`, `ModelFallbacks`, summarizer/namer fallbacks, nor `Session` recovery issues a second `POST /interactions`, and `Session` strips non-authorizing tool calls (`zero` tool executions) while retaining surviving `ContentThinking` (`skipHistory = false`) (pinned in Tasks 9, 10, and 11).

---

### Task 1: Shared `llm.StampContinuationIDHash` and `llm/registry` Capability & Constant-Cloning Prerequisites (Stage 1)

**Files:**
- Create: `llm/continuation_hash.go`
- Create: `llm/continuation_hash_test.go`
- Modify: `llm/providers/responses/complete.go`
- Modify: `llm/providers/responses/stream.go`
- Modify: `llm/registry/types.go`
- Modify: `llm/registry/schema.go`
- Modify: `llm/registry/derive.go`
- Modify: `llm/registry/prune.go`
- Test: `llm/registry/derive_test.go`
- Test: `llm/registry/prune_test.go`

**Interfaces:**
- Consumes: `llm.ContinuationHasher`, `llm.ContinuationHasherFromContext` (`llm/continuation.go`), `registry.PresetVertexGemini` (`llm/registry/types.go`).
- Produces:
  - `llm.StampContinuationIDHash(ctx context.Context, resp *llm.Response, fallback llm.ContinuationHasher)` in `llm/continuation_hash.go`
  - `Caps.CombinedSearchAndFunctions *bool` (`toml:"combined_search_and_functions" json:"combined_search_and_functions,omitempty"`) in `llm/registry/types.go` (layered and provenanced in `schema.go` and `resolve.go`)
  - `registry.IsVertexGeminiOmni(preset, wireID string) bool` in `llm/registry/derive.go`
  - Deep-cloning of `map[string]any` and `[]any` constants inside `registry.ApplyBodyConstants(body, constants map[string]any)` in `llm/registry/prune.go`

- [ ] **Step 1: Write failing tests in `llm/continuation_hash_test.go`, `llm/registry/derive_test.go`, and `llm/registry/prune_test.go`**

Test cases to add:
- `TestStampContinuationIDHash_ContextOverridesFallbackAndSkipsEmpty`: verifies context-attached `ContinuationHasher` takes precedence over `fallback`, stamps `resp.Raw["id_hash"]` under handle kind `"response_id"`, initializes nil `resp.Raw`, and is a no-op when `resp == nil`, `resp.ID == ""`, or both hashers are nil.
- `TestDerive_CombinedSearchAndFunctionsAndVertexGeminiOmni` in `llm/registry/derive_test.go`: verifies (a) `CombinedSearchAndFunctions = true` layers from TOML/model config and clamps to `false` (`provDerived`) when `c.Tools != nil && !*c.Tools`, and (b) `IsVertexGeminiOmni(PresetVertexGemini, "gemini-omni-1.2-flash")` is `true` and clamps `c.WebSearch = false` even under a provider-wide `web_search = true`, while `IsVertexGeminiOmni("", "acme-omni-chat")` and `IsVertexGeminiOmni("", "gemini-omni-1.2-flash")` return `false` and preserve `WebSearch`.
- `TestApplyBodyConstants_DeepClonesNestedMapsAcrossCalls` in `llm/registry/prune_test.go`: configures shared `constants = map[string]any{"generation_config": map[string]any{"max_output_tokens": 1000}, "generation_config.seed": 7}`, runs `ApplyBodyConstants` on two request bodies and mutates `body1["generation_config"].(map[string]any)["max_output_tokens"] = 200`, asserting `constants["generation_config"]` and `body2["generation_config"]` still hold `max_output_tokens == 1000` and `seed == 7`.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm -run TestStampContinuationIDHash && go test ./llm/registry -run 'TestDerive_CombinedSearchAndFunctionsAndVertexGeminiOmni|TestApplyBodyConstants_DeepClonesNestedMapsAcrossCalls'`
Expected: FAIL (undefined `StampContinuationIDHash`, `CombinedSearchAndFunctions`, `IsVertexGeminiOmni`, and mutated shared map in `ApplyBodyConstants`).

- [ ] **Step 3: Implement `llm.StampContinuationIDHash`, `Caps.CombinedSearchAndFunctions`, `registry.IsVertexGeminiOmni`, and deep-cloning `ApplyBodyConstants`**

- In `llm/continuation_hash.go`, implement `StampContinuationIDHash(ctx context.Context, resp *Response, fallback ContinuationHasher)` matching `responses.(*Protocol).stampResponseIDHash`, and replace `p.stampResponseIDHash(ctx, ...)` in `llm/providers/responses/complete.go` and `llm/providers/responses/stream.go` with `llm.StampContinuationIDHash(ctx, ..., p.Hasher)` (deleting the private method so there is one shared helper).
- In `llm/registry/types.go`, `schema.go`, `resolve.go`, and `derive.go` (Spec §1 lines 94-96, 132, 328): add `CombinedSearchAndFunctions *bool` onto `Caps` (layered/merged identically to `MultimodalToolResults` and `WebSearch`), thread `TransportPreset string` on `deriveInput`, export `IsVertexGeminiOmni(preset, wireID string) bool`, clamp `CombinedSearchAndFunctions = new(false)` when `c.Tools != nil && !*c.Tools && BoolValue(c.CombinedSearchAndFunctions)`, and clamp `WebSearch = new(false)` when `IsVertexGeminiOmni(in.TransportPreset, m.WireID)` is true.
- In `llm/registry/prune.go` (`ApplyBodyConstants`), deep-clone `map[string]any` and `[]any` values before `setPath` installs them onto `body`.

- [ ] **Step 4: Run `llm` package tests to verify GREEN**

Run: `go test ./llm/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/continuation_hash.go llm/continuation_hash_test.go llm/providers/responses/complete.go llm/providers/responses/stream.go llm/registry/
git commit -m "feat(llm,registry): hoist StampContinuationIDHash, add CombinedSearchAndFunctions and IsVertexGeminiOmni, and clone body constants"
```

---

### Task 2: Cold & Live Model-Switch Target Classification, Lineage-Exact Transcript Reconciliation, and Hub/CLI Recovery (Stage 2a)

**Files:**
- Create: `agent/transcript/reconcile_model.go`
- Create: `agent/transcript/reconcile_model_test.go`
- Modify: `agent/provider/profile.go`
- Modify: `agent/provider/profile_test.go`
- Modify: `agent/session.go`
- Modify: `agent/session_set_model.go`
- Modify: `agent/live_model_metadata.go`
- Modify: `agent/session_init.go`
- Modify: `agent/delegate_restore_idle.go`
- Modify: `cmd/evener/serve.go`
- Modify: `cmd/evener/run.go`
- Modify: `cmd/evener/serve_test.go`
- Modify: `cmd/evener/run_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/types.go`
- Modify: `cmd/evener-hub/spawn.go`
- Modify: `cmd/evener-hub/spawn_test.go`
- Modify: `cmd/evener-hub/app_model.go`
- Modify: `cmd/evener-hub/app_session_resume.go`
- Modify: `cmd/evener-hub/app_threadlifecycle.go`
- Modify: `cmd/evener-hub/e2e_model_switch_test.go`
- Modify: `internal/appwiredoc/protocol.md.tmpl`
- Modify: `docs/appwire-protocol.md`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/product/lifecycles.md`
- Modify: `docs/web-ui/README.md`

**Interfaces:**
- Consumes: `transcript.Scan` (`agent/transcript/scan.go`), `schema.SessionMeta`, `schema.TurnModelSwitch`.
- Produces:
  - `transcript.ScanSessionFile(ctx context.Context, path, expectedSessionID string) ([]transcript.Entry, error)`
  - `transcript.ReconcileMetaModelFromEntries(meta schema.SessionMeta, entries []transcript.Entry) schema.SessionMeta`
  - `transcript.ReconcileMetaModelFromFile(stateDir string, meta schema.SessionMeta) schema.SessionMeta`
  - `provider.ModelSwitchTarget{Provider, Model string}` (`Qualified() string`) and `provider.ClassifyModelSwitchTarget(reg *registry.Registry, savedProvider, explicitProvider, model string) (ModelSwitchTarget, error)` in `agent/provider/profile.go`
  - `RestoreSessionConfig.ModelOverridden bool` and `RestoreSessionConfig.ModelFallbacksExplicit bool` in `agent/session_init.go`
  - `hubcore.ResumeRequest.{ModelOverride, OverrideProvider string}`

- [ ] **Step 1: Write failing unit and integration tests for `ScanSessionFile`, `ReconcileMetaModelFromEntries`, `ClassifyModelSwitchTarget`, restore fallback/`CheapModel` pruning, and cold Hub `thread/model/set`**

Cover the exact Spec §5.3 Stage 2a behaviors (lines 299-312, 330):
- `TestScanSessionFile_SkipsTornTailWithoutTruncatingAndFailsHeaderMismatch` & `TestReconcileMetaModelFromEntries_DivergenceTurnAndFallbackImmunity` in `agent/transcript/reconcile_model_test.go`: verifies read-only torn-tail tolerance (leaving file bytes on disk untouched while rejecting corrupt complete lines or wrong header session ID), 1-based physical `idx + 1 >= meta.DivergenceTurn` filtering skipping `OriginalOrdinal != nil` fold copies (covering forked/aside compacted prefixes), adopting unsaved `TurnModelSwitch` even if a later `TurnAssistant` recorded a `ModelFallbacks` answer `C`, and keeping a newer saved CLI `SessionMeta` (`meta.UpdatedAt` after `switchTurn.Timestamp`) authoritative.
- `TestClassifyModelSwitchTarget` in `agent/provider/profile_test.go`: covers explicit `ModelProvider`, redundant self-prefix stripping (`"openrouter/free"` on `"openrouter"`), facts-depth namespaced model retention (`"anthropic/claude-opus-5"` on `"openrouter"`, never running `api_key_cmd` on `savedProvider`) vs disabled row cross-provider switch, live-only unmatched slashed IDs (`"acme-vendor/live-chat"` kept on `savedProvider`), and unconfigured/retired `savedProvider` requiring `provider/model` where `provider` is configured in `reg`.
- Restore tests in `agent/` (`session_set_model_test.go` / new `session_cold_model_switch_test.go`): (a) rejected cold `ModelOverridden: true` replacement (unrepresentable history or failed live membership) leaves `.transcript.jsonl` (including a torn tail) and `SessionMeta` byte-for-byte untouched; (b) ordinary resume (`ModelFallbacksExplicit: false`) prunes unconfigured saved `ModelFallbacks` and unconfigured saved `meta.CheapModel` with `events.WarningData` and zero `TurnModelSwitch`, whereas explicit `--model-fallback` (`ModelFallbacksExplicit: true`) rejects an invalid fallback strictly; (c) valid cold `ModelOverridden: true` switch writes exactly one `saved -> replacement` `TurnModelSwitch` and is crash-idempotent on re-restore.
- Hub/CLI tests (`cmd/evener-hub/e2e_model_switch_test.go`, `spawn_test.go`, `cmd/evener/serve_test.go`): cold `thread/model/set` on a stopped session saved on an unconfigured/retired provider ID switches cleanly inside `resumeThread`'s existing `active.AcquireOwnership` critical section (`prepareOverride` without double-locking `ResumeLocks`), canonicalizes `"GOOGLE"` -> `"google"`, judges auth on `cmp.Or(req.ModelOverride, req.Resolved.Effective.Model)` while keeping `TestHubSpawnerResumeJudgesThePersistedModel` green, emits one `TurnModelSwitch` and ownership-fenced `thread/model/changed`, retries via ownership+capability-gated `SetThreadModel` when attaching to an already-live daemon, and `serve.go`/`run.go` reload `SessionMeta` + `ReconcileMetaModelFromFile` *after* `reserveSession`.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./agent/transcript/... ./agent/provider/... && go test ./cmd/evener-hub -run 'TestModelSwitch|TestHubSpawnerResume'`
Expected: FAIL (missing symbols and cold unconfigured-provider switch failure).

- [ ] **Step 3: Implement Stage 2a across `agent/transcript`, `agent/provider`, `agent/`, `cmd/evener`, `cmd/evener-hub`, and evergreen docs**

Follow Spec §5.3 lines 299-312 and 330 verbatim:
- Implement `ScanSessionFile`, `ReconcileMetaModelFromEntries`, and `ReconcileMetaModelFromFile` in `agent/transcript/reconcile_model.go`.
- Implement `ClassifyModelSwitchTarget` in `agent/provider/profile.go` and wire both `Session.resolveModelSwitchProfile` (`agent/session.go`) and Hub cold `setThreadModelWithResume` (`cmd/evener-hub/app_model.go`, `app_session_resume.go`, `app_threadlifecycle.go`).
- Update `RestoreSessionFromMetaWithConfig` (`agent/session_init.go`) and `delegateRuntime.restoreIdle` (`agent/delegate_restore_idle.go`): run read-only `ScanSessionFile` + `unrepresentableHistoryKinds` + `resolveStartupModelProfile` (when `ModelOverridden`) *before* `transcript.OpenWriterForSession`; prune unservable saved `meta.CheapModel` and `!ModelFallbacksExplicit` `ModelFallbacks` with `events.WarningData`; reconcile unsaved `TurnModelSwitch` entries; and gate new `TurnModelSwitch` emission on `restoreCfg.ModelOverridden`.
- Update `cmd/evener/serve.go` and `cmd/evener/run.go` to set `ModelOverridden` (`*lf.model != ""`) and `ModelFallbacksExplicit` (`len(modelFallbacks) > 0` from `--model-fallback`), and defer resume `modelRef` / `buildProfile` until after `reserveSession` reloads + reconciles `SessionMeta`.
- Update `internal/appwiredoc/protocol.md.tmpl` + run `make generate` (`docs/appwire-protocol.md`), `docs/product/subsystems.md` (`S06`, `S08`, `S09`), `docs/product/lifecycles.md`, and `docs/web-ui/README.md`.

- [ ] **Step 4: Verify `agent`, `cmd/evener`, `cmd/evener-hub`, copyonly audit, and `make docs-check`**

Run: `go test ./agent/transcript/... ./agent/provider/... ./cmd/evener/... ./cmd/evener-hub/... ./internal/devtool/copyonly/... && go test ./agent -run 'Test.*ModelSwitch|TestRestoreSession|TestSetModel' && make docs-check`
Expected: PASS with pristine output and zero `errstring_allowlist.tsv` drift.

- [ ] **Step 5: Commit**

```bash
git add agent/ cmd/evener/ cmd/evener-hub/ internal/appwiredoc/ docs/
git commit -m "feat(session,hub): unify cold and live model-switch target classification, transcript model reconciliation, and saved-route recovery"
```

---

### Task 3: Durable `delegate_send` `model` Override Admission, Stop Fencing, and Restart Recovery (Stage 2b)

**Files:**
- Modify: `llm/api_attempt.go`
- Modify: `llm/client_test.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/internal/delegatestore/events.go`
- Modify: `agent/internal/delegatestore/fold.go`
- Modify: `agent/internal/delegatestore/fold_test.go`
- Modify: `agent/session_set_model.go`
- Modify: `agent/delegate_send.go`
- Modify: `agent/delegate_tree_start.go`
- Modify: `agent/delegate_restore_idle.go`
- Modify: `agent/delegate_attention_drive.go`
- Modify: `agent/subagents.go`
- Modify: `agent/delegate_tree_restore.go`
- Modify: `agent/delegate_owed_bootstrap.go`
- Modify: `agent/delegate_tree_controller_test.go`
- Create: `agent/delegate_send_model_override_test.go`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/product/lifecycles.md`
- Modify: `docs/subagent-runtime-contracts.md`

**Interfaces:**
- Consumes: `provider.ClassifyModelSwitchTarget`, `transcript.ScanSessionFile`, `transcript.ReconcileMetaModelFromEntries` (Task 2).
- Produces:
  - `llm.DiscardAPIAttemptSink() llm.APIAttemptSink` in `llm/api_attempt.go`
  - Optional `"model"` string parameter on `DefDelegateSend` (`agent/internal/tool/definitions.go`)
  - `delegatestore.EventDelegateStartAdmitted` & `EventDelegateStartLaunched`, `Aggregate.{PendingAdmittedStart, UnlaunchedStoppedGenerations, LastAdmittedModelAt}`
  - `RestoreSessionConfig.{ReconcileAdmittedSwitch bool, UnlaunchedStoppedGenerations map[uint64]bool}`
  - `selectAuthoritativeDelegateModel(descriptor, lastAdmittedModelAt, reconciledMeta, latestChildSwitchAt)` shared across Phase 1 preflight, cold `restoreIdle`, and resident `reconcileBoundDelegateRuntime`

- [ ] **Step 1: Write failing tests in `llm/client_test.go`, `agent/internal/delegatestore/fold_test.go`, and `agent/delegate_send_model_override_test.go`**

Pin every Spec §5.3 Stage 2b invariant (lines 310-321, 331):
- `TestDiscardAPIAttemptSink_SuppressesMiddlewareRecording`: verifies `llm.WithAPIAttemptSink(ctx, llm.DiscardAPIAttemptSink())` records zero entries to an installed middleware API-attempt logger.
- `delegatestore` fold tests: atomic `[EventDelegateRunStarted, EventDelegateStartAdmitted]` updates `(ResolvedProfileID, ResolvedModel, ModelFallbacks)`, `LastAdmittedModelAt`, and `PendingAdmittedStart`; `EventDelegateStartLaunched` clears `PendingAdmittedStart`; stopping before launch records `UnlaunchedStoppedGenerations`.
- `agent/delegate_send_model_override_test.go`:
  1. `model != ""` on a running delegate bypasses `Steer` (`maxWaitMS == 0`) and returns `target_busy` without steering the running generation.
  2. Rejected Phase 1 `model` override (resident and cold, including torn `.transcript.jsonl` tail) makes zero durable writes (`SessionMeta`, `.transcript.jsonl`, `delegates.jsonl`, `.api.jsonl`) and zero `AcquireSessionOwnership` calls.
  3. Valid cold & resident `delegate_send(model=...)` wakes a dormant delegate pinned to an unconfigured provider, reuses Phase 1's primed `StartupModelListing` (single `ListModels` call), records one `old -> replacement` `TurnModelSwitch`, and delivers the reply.
  4. `Stop` racing before `CommitStart` writes nothing; `Stop` winning after `CommitStart` before `BeginAdmittedExecution` runs neither `SessionStart` nor `SubagentStart` hooks, excludes that generation's `ClientMutationID` input on later wakes, and preserves the admitted model `B` (`descriptorWins == true` via `LastAdmittedModelAt` vs `TurnModelSwitch` timestamps, unaffected by `SessionMeta.UpdatedAt` note saves).
  5. When the child later switches `B -> C` on its own (`latestChildSwitchAt.After(lastAdmittedModelAt)` -> `descriptorWins == false`), subsequent resident and cold wakes preserve `C` without recording an unrequested `C -> B` switch.
  6. Restart bootstrap recovery (`reconcileRuntimeLostFromEvidenceLocked` + `admitOwedDelegateAttentionStarts`) drives an admitted unlaunched start once without duplicating `TurnUserInput`.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm -run TestDiscardAPIAttemptSink && go test ./agent/internal/delegatestore/... && go test ./agent -run TestDelegateSendModelOverride`
Expected: FAIL (missing `DiscardAPIAttemptSink`, `EventDelegateStartAdmitted`, and `DefDelegateSend` `"model"` parameter).

- [ ] **Step 3: Implement Stage 2b across `llm`, `delegatestore`, `agent`, and `S12` docs**

Follow Spec §5.3 lines 310-321 and 331:
- Export `DiscardAPIAttemptSink()` in `llm/api_attempt.go` (`sink != nil` with `sinkPersistsRecords(sink) == false`).
- Add `"model"` schema property onto `DefDelegateSend` (`agent/internal/tool/definitions.go`).
- Add `EventDelegateStartAdmitted`, `EventDelegateStartLaunched`, `PendingAdmittedStart`, `UnlaunchedStoppedGenerations`, and `LastAdmittedModelAt` in `agent/internal/delegatestore`.
- Implement `resolveModelSwitchTargetWithContext`, `selectAuthoritativeDelegateModel`, Phase 1 non-recording preflight (`delegate_send.go`), `StageStartModelOverride` + post-append `c.durable` reload in `CommitStart` (`delegate_tree_start.go`), cold `ReconcileAdmittedSwitch` (`delegate_restore_idle.go`), resident `reconcileBoundDelegateRuntime` (`delegate_send.go` + `delegate_attention_drive.go`), `finishAdmittedOwnerInputStart` + `BeginAdmittedExecution` ahead of both resume `SessionStart` and `SubagentStart` hooks (`subagents.go`), and restart recovery (`delegate_tree_restore.go`, `delegate_owed_bootstrap.go`), updating `delegate_tree_controller_test.go` method-call inventory if needed.
- Update `docs/product/subsystems.md` (`S12`), `docs/product/lifecycles.md`, and `docs/subagent-runtime-contracts.md`.

- [ ] **Step 4: Run tests and `make docs-check` to verify GREEN**

Run: `go test ./llm -run TestDiscardAPIAttemptSink && go test ./agent/internal/delegatestore/... && go test ./agent -run 'TestDelegate|TestSubagent' && make docs-check`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/api_attempt.go llm/client_test.go agent/ docs/
git commit -m "feat(delegates): add durable delegate_send model override admission, Stop fencing, and restart recovery"
```

---

### Task 4: `WebSearchSource`, `ThinkingData.GoogleInteraction`, and `apptranscript` Readers (Stage 2c)

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

Add tests verifying Spec §5.2 (lines 271-273, 332):
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
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add llm/types.go internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go
git commit -m "feat(apptranscript,llm): project WebSearchData.Sources citations and hide signature-only Google thoughts"
```

---

### Task 5: Interactions JSON/SSE Settlement, Usage Reconciliation, Cross-Protocol `!GoogleInteraction` Destination Guards, and `doctor/apilog` Recompute (Stage 3a)

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
- Consumes: `llm.StampContinuationIDHash` (Task 1), `llm.WebSearchSource`, `ThinkingData.GoogleInteraction` (Task 4).
- Produces:
  - `ContentPart.GoogleStep int` (`json:"google_step,omitempty"`), `ThinkingData.{GoogleInstance, GoogleModel string}` (`ChatReplayText`, `ReplayableResponsesPlaintext` `!GoogleInteraction` guards), `ToolCallData.{GoogleInteraction bool, GoogleInstance, GoogleModel string}` in `llm/types.go`
  - Shared settlement helpers in `llm/providers/google`: `fromInteraction`, `appendInteractionSteps`, `extractThoughtSummaryItems`, `extractCitationAnnotations`, `normalizeGoogleSearchResultStep`, `settleInteractionStatus`, `classifyStepRPCStatus`, `classifyInteractionError`, `parseInteractionUsage`, `interactionStreamAccumulator`, `ErrPendingInteractionStatus`, `ErrTerminalStatusStepFailure`, `ErrUnexpectedInteractionStatus`, and `google.ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error)`
  - `"google_interactions"` and `"google_interactions_stream"` registered in `recomputeExtractors` (`agent/doctor/apilog.go`)

- [ ] **Step 1: Write failing tests for JSON/SSE settlement, 8-way usage reconciliation, SSE slot ordering, `doctor/apilog` recompute, and cross-protocol `!GoogleInteraction` guards**

Cover Spec §3, §4.1-4.3, §5.1, and Stage 3a (lines 206-270, 276, 333, 338-345):
- `llm/providers/google/interactions_settlement_test.go`:
  - `fromInteraction` & `interactionStreamAccumulator` parity across Vertex singleton & Gemini array `thought.summary` (`extractThoughtSummaryItems`), `model_output` `GoogleStep: stepIdx + 1`, mixed `text -> image -> text` boundaries (`[]*strings.Builder` + `textNeedsNewBuilder`), Vertex singleton & Gemini array `google_search_result` (`normalizeGoogleSearchResultStep`) and `url_citation` (`extractCitationAnnotations` attaching `Sources` or inserting standalone `ContentWebSearch` carrier), `function_call` raw argument preservation (`captureInteractionArgsRaw` + `NormalizeToolArguments`, preserving invalid streamed JSON verbatim with `RawArguments == ""`).
  - SSE per-slot `Start -> Delta(s) -> End` event queue (`acc.nextEmitIndex` + `slot.deferredEvents`): (a) `step.start` opening `"arguments": {}` placeholder with zero deltas defers `StreamEventToolCallEnd` until `interaction.completed.steps` supplies `arguments`, whereas delta-confirmed `"{}"` or non-empty `step.start` `arguments` emits `StreamEventToolCallEnd` immediately at `step.stop`; (b) open unstopped `function_call` slot with incomplete fragments adopts complete terminal `interaction.completed.steps` arguments while preserving malformed `argrepair` bytes when no complete terminal object exists; (c) mixed stream with unresolved `step 0` + live `step 1` drains `slot.deferredEvents` before terminal backfills so `StreamResult.TextStream()` yields `step 0` then `step 1` once and deferred `step 1` tool calls emit `ToolCallStart -> ToolCallDelta -> ToolCallEnd` in exact order; (d) `thinking_summaries: "none"` (`summariesDisabled`) advances `acc.nextEmitIndex` immediately at `step.stop` while summary-enabled signed thoughts hold until `interaction.completed`; (e) started text/reasoning spans without `step.stop` emit `TextEnd`/`ReasoningEnd` before `Finish`.
  - Status & error classification: `"completed"`, `"requires_action"`, `"incomplete"`/`"budget_exceeded"`/`"requires_continuation"` (`FinishReasonLength` + `continuation_token` retention + `ErrTerminalStatusStepFailure` on empty gRPC code `4` step error), `"cancelled"`, `"queued"`/`"in_progress"` (`ErrPendingInteractionStatus` with `StatusCode() == 0 && !Retryable()`), `"failed"` (`not_found` -> 404 `KindNotFound`, `gateway_timeout`/`deadline_exceeded` -> 408 `KindTimeout`, safety codes -> `FinishReasonContentFilter`), missing/unknown status -> `ErrUnexpectedInteractionStatus`, media-only output -> `ConfigurationError`.
  - All 8 `parseInteractionUsage` billing cases (`100/50/25/125`, `10/20/25/20/55`, omitted `total` `10/20/25`, `thought > rawOut` `10/10/20/30 -> Output=30, Total=40`, additive cache `100/25/5/10/135`, cached subtraction, `StepStop.usage` retention).
- `agent/doctor/apilog_recompute_test.go`: `"google_interactions"` JSON and `"google_interactions_stream"` SSE recompute accurately while historical `"google_generate_content"` / `"google_stream_generate_content"` rows are skipped cleanly (`false`) in mixed logs, and mis-filed `choices`/`candidates` JSON bodies fail recompute.
- Cross-protocol `!GoogleInteraction` tests in `anthropic/request_test.go`, `chatcompletions/messages_test.go`, and `llm/token_count_test.go`: Google `ThinkingData{GoogleInteraction: true, Summary: ..., Signature: ...}` and `ToolCallData{GoogleInteraction: true, ThoughtSignature: ...}` never emit Anthropic `thinking`, Chat `reasoning_content` (even with `thinking_as_text`), or `extra_content.google.thought_signature`, and add **zero** estimated input tokens in `token_count.go` for non-Google targets while native Anthropic/Chat/Responses reasoning still bills and replays unchanged.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm/... -run 'TestInteraction|TestExtractRecordedResponse|TestGoogleInteraction' && go test ./agent/doctor -run TestRecompute`
Expected: FAIL (missing Interactions settlement files, types, and recompute registrations).

- [ ] **Step 3: Implement Stage 3a helpers, `!GoogleInteraction` guards, and `doctor/apilog` wiring**

- Update `llm/types.go`, `llm/token_count.go` (non-Google upfront `!GoogleInteraction` guard in `estimateMessageInputParts`, `chatReasoningEstimate.add`, and `thinkingReplayChars`), `llm/providers/anthropic/request.go`, and `llm/providers/chatcompletions/messages.go` per Spec §5.1 & §5.3 lines 270, 276.
- Implement `llm/providers/google/interactions_response.go`, `llm/providers/google/interactions_stream.go`, and `llm/providers/google/recompute.go` per Spec §3 and §4 (keeping legacy `response.go`/`stream.go` intact until Task 11's atomic cutover, while `ExtractRecordedResponse` in `recompute.go` exercises every new helper in production via `agent/doctor/apilog.go`).
- Register `"google_interactions"` and `"google_interactions_stream"` -> `google.ExtractRecordedResponse` in `agent/doctor/apilog.go`.

- [ ] **Step 4: Run `llm` and `agent/doctor` suites to verify GREEN**

Run: `go test ./llm/... ./agent/doctor/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/ agent/doctor/
git commit -m "feat(google,doctor): add Interactions JSON/SSE settlement, usage reconciliation, cross-protocol provenance guards, and apilog recompute"
```

---

### Task 6: Shared Grounding Provenance & Transcript/Compaction/Notes/Delegate/`TurnCommunicate` Projection + Local Model-Facing Readers (Stage 3b-1)

**Files:**
- Modify: `agent/schema/turn.go`
- Modify: `agent/schema/turn_order_test.go`
- Modify: `agent/schema/snapshot.go`
- Modify: `agent/schema/skill_lifecycle.go`
- Modify: `agent/internal/sessionlog/sessionlog.go`
- Modify: `llm/apilog/record.go`
- Create: `agent/grounding_scrub.go`
- Create: `agent/grounding_scrub_test.go`
- Modify: `agent/session_tools_transcript.go`
- Modify: `agent/transcript_render.go`
- Modify: `agent/apilog_read.go`
- Modify: `agent/session_tools_find.go`
- Modify: `agent/session_tools_doctor.go`
- Modify: `agent/doctor/transcript.go`
- Modify: `agent/doctor/sessions.go`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/product/history-content.md`

**Interfaces:**
- Consumes: `schema.SessionDelegateJournalPath`, `transcript.ScanSessionFile` (Task 2), `llm.ContentWebSearch`.
- Produces:
  - `Turn.GroundingScrubbed bool` (`json:"grounding_scrubbed,omitempty"`, placed alphabetically between `GoalContinuation` and `Hook` in `agent/schema/turn.go`), `SessionMeta.{AgentNoteGroundingScrubbed, PinnedNoteGroundingScrubbed, NameGroundingScrubbed bool}`, `SessionURL.LabelGroundingScrubbed bool`, `SkillCompactionOperation.GroundingScrubbed bool`, `sessionlog.SessionLogEntry.GroundingScrubbed bool`, `apilog.APIAttemptRequest.GroundingScrubbed bool`
  - `agent.ScrubGoogleGroundingTranscriptEntries(...) ([]transcript.Entry, []bool)` and `scrubGoogleGroundingTurns(...)` in `agent/grounding_scrub.go`, wired immediately into `read_transcript` (`outline`, `markdown`, `jsonl`, `expand_turn`, `source="api_log"`), `find_session_transcripts`, and in-process `doctor_evener` (`command="transcript"|"sessions"`)

- [ ] **Step 1: Write failing tests in `agent/grounding_scrub_test.go` (plus `turn_order_test.go` check)**

Cover every Spec §5.3 Stage 3b-1 rule (lines 281-300, 336, 346):
- `Turn` alphabetical JSON-key order passes `go test ./agent/schema/...`.
- `groundedHistoryPresented` carries across pre-cutover follow-up `TurnUserInput` prompts once Grounding entered the active window (`ProtocolGoogle` `ContentWebSearch`, unprefixed `"web_search"` when `resultToolName != "web_search"`, grounded fold, grounded `notes_read`/`TurnNotesContext`, grounded cross-session `read_transcript`/`find_session_transcripts`/`doctor_evener`, or grounded delegate delivery), while preserving pre-Grounding turns, human prompts/steering, `ResultToolName == "web_search"` overrides, and post-cutover `Turn.GroundingScrubbed == true` turns verbatim.
- Default-deny scrub on exposed pre-cutover turns: strips `ContentWebSearch`, replaces `ContentText`, strips **all** `ContentThinking` and `ContentRedThinking` parts (Responses `encrypted_content`/summary, Anthropic signed/redacted, display thinking), clears `ResponseID`/`ResponseIDHash`, scrubs `ContentToolCall` (`Arguments`, `ArgumentsSpelling = ""`, `RawArguments = ""`, preserving structural allowlist keys and `apply_patch` file headers), scrubs `TurnCommunicate` (`deliverCommunicate` `Communicate.Message` while keeping `CallID`, `EndTurn`, `EndReason`), scrubs paired `ContentToolResult`s + `ToolState`, rewrites historical `TurnNotesContext`, tracks chronological `notes_agent_set`/`urls_add`/`urls_remove` (with conservative interrupted-batch handling), and walks `ParentSessionID` ancestry `schema.SessionDelegateJournalPath` (`sessions/<id>/delegates.jsonl`) to scrub `<delegate-notification>` and per-delivery `Turn.DelegateDeliveryCommits` `EarlierResults` + latest `delegate_send` results (preserving clean siblings and their numbering even in an already-exposed parent turn or forked session!).
- Pre-cutover fold reconstruction/withholding (`index 0` imported baseline, survivor provenance, `<id>.log.jsonl` `session-log` full-history-first 10-turn mask & `ooda` sanitization), while `GroundingScrubbed == true` folds/entries survive untouched.
- Local model-facing readers: `read_transcript` (`outline`, `markdown`, `format=jsonl`, and multi-page `expand_turn` rewriting `rawTurn["message"]`, `rawTurn["skill_state"]`, `rawTurn["communicate"]`, deleting cleared `response_id`/`response_id_hash` and `grounding_scrubbed` while leaving unchanged lines byte-identical), `read_transcript(source="api_log")` (`hasGoogleGroundingWirePayload` scoped strictly to Google attempts so clean OpenAI Responses `url_citation` bodies remain readable while Google citation-only and `groundedAttemptGroupIDs`/`groundedWindows` bodies are withheld), `find_session_transcripts` (title + snippet withholding), and in-process `doctor_evener` (leaving `cmd/evener` CLI `doctor` untouched).

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./agent/schema/... && go test ./agent -run TestGroundingScrub`
Expected: FAIL (`TestGroundingScrub*` undefined).

- [ ] **Step 3: Implement Stage 3b-1 schema fields, `agent/grounding_scrub.go`, local reader wiring, and `S09`/`S10`/`history-content.md` docs**

Implement all schema fields, `agent/grounding_scrub.go`, `read_transcript` / `api_log` / `find_session_transcripts` / `doctor_evener` hooks, and evergreen updates to `docs/product/subsystems.md` (`S09`, `S10`) and `docs/product/history-content.md` per Spec §5.3 lines 281-300 and 336.

- [ ] **Step 4: Run `agent/schema`, `llm/apilog`, `agent/doctor`, `agent` transcript/doctor/scrub tests, copyonly audit, and `make docs-check`**

Run: `go test ./agent/schema/... ./llm/apilog/... ./agent/doctor/... ./internal/devtool/copyonly/... && go test ./agent -run 'TestGroundingScrub|TestReadSessionTranscript|TestFindSessionTranscripts|TestDoctorEvener|TestAPILog' && make docs-check`
Expected: PASS with pristine output (including `TestReadSessionTranscriptExpansionLosslesslyReturnsEverySemanticTurn`).

- [ ] **Step 5: Commit**

```bash
git add agent/ llm/apilog/ docs/product/subsystems.md docs/product/history-content.md
git commit -m "feat(agent): add pre-cutover Google Grounding transcript projection and wire local model-facing readers"
```

---

### Task 7: Read-Side Memory Mutation Provenance, Byte-Matched Historical Lookup, Byte-Bound Memory Tools, and `S24` Docs (Stage 3b-2)

**Files:**
- Modify: `agent/memorypages/memory_page.go`
- Modify: `agent/memorypages/memory_page_test.go`
- Modify: `agent/execenv/local_files.go`
- Modify: `agent/execenv/confined_files.go`
- Modify: `agent/execenv/local_test.go`
- Create: `agent/session_memory_grounding.go`
- Create: `agent/session_memory_grounding_test.go`
- Modify: `agent/grounding_scrub.go`
- Modify: `agent/session_memory.go`
- Modify: `agent/session_tools_memory.go`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/product/memory.md`

**Interfaces:**
- Consumes: `agent.ScrubGoogleGroundingTranscriptEntries` / `groundedHistoryPresented` (Task 6).
- Produces:
  - `memorypages.Page.{GroundingCleanDigest, ContentDigest string, GroundingWithheld bool}` and `memorypages.MemoryPageContentDigest(raw []byte) string` in `agent/memorypages/memory_page.go`
  - `(*LocalExecutionEnvironment).EditFileCheckedWithCtx(..., checkOriginal func(raw []byte) error, finish func(edited []byte) []byte)` and `execenv.ApplyLiteralOrFuzzyEdit(content, oldString, newString string, replaceAll bool) (string, bool)` in `agent/execenv/local_files.go`
  - `(*LocalExecutionEnvironment).GrepConfinedFilesWithContentFilter(..., allowContent func(rel string, raw []byte) bool)` in `agent/execenv/confined_files.go`
  - `isMemoryPageGrounded`, `memoryHistoryBuckets`, and `TurnMemoryContext` Grounding line filtering in `agent/session_memory_grounding.go` (keeping `stampMemoryPage` `grounding_clean_digest` writer emission gated off until Task 9 / Stage 3b-4!)

- [ ] **Step 1: Write failing tests in `agent/memorypages/memory_page_test.go` and `agent/session_memory_grounding_test.go`**

Pin every Spec §5.3 Stage 3b-2 requirement (lines 291-294, 337, 346):
- `TestMemoryPageContentDigest_IncludesFrontmatterExceptBookkeeping`: changing `tags`, `description`, `when`, custom frontmatter, or body changes `ContentDigest` and invalidates `GroundingCleanDigest`, while changing `updated`, `by`, `grounding_clean_digest`, or `grounding_withheld` leaves `ContentDigest` unchanged.
- `agent/session_memory_grounding_test.go`:
  1. Grounded pre-cutover `by:` page, clean `"personal"` page written in Project A and read from Project B, and unstamped `by == ""` human wiki page coexist: clean cross-project and human pages appear in regenerated `TurnMemoryContext` and `"MEMORY.md"`, while grounded page's `when:` / `tags` stay excluded and all `.md` files remain untouched on disk.
  2. Cross-session `A (clean write) -> B (grounded partial memory_edit) -> A (clean partial memory_edit)` stays withheld across regenerated indexes, `memory_read`, and `memory_search`; a clean legacy page edited via whitespace-normalized fuzzy matching reproduces `versionDigest == page.ContentDigest` and stays readable even if the writer later encounters unrelated Grounding.
  3. Interleaving race: reader loads older Grounding-derived bytes before the same writer commits a clean full `memory_write` replacement -> `versionDigest == page.ContentDigest` keeps the already-loaded older bytes withheld while a subsequent read of the new clean bytes succeeds.
  4. Byte-bound race protection on `memory_read` (`ReadFileAndBytesCtx`), `memory_search` (`GrepConfinedFilesWithContentFilter` across `content`, `files_with_matches`, and `count`), and `EditFileCheckedWithCtx` (`memory_edit` failing with a path-only message and **zero** `nearestFileRegion` stored excerpts when a page is withheld or replaced right before read, while clean pages retain full diagnostics).
  5. Pre-cutover `TurnMemoryContext` snapshots strip grounded index lines and latch `groundedHistoryPresented` while preserving clean sibling lines.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./agent/memorypages/... && go test ./agent -run TestMemoryGrounding`
Expected: FAIL (missing `MemoryPageContentDigest`, `EditFileCheckedWithCtx`, `GrepConfinedFilesWithContentFilter`, and `session_memory_grounding.go`).

- [ ] **Step 3: Implement Stage 3b-2 across `memorypages`, `execenv`, `agent`, and `S24` / `memory.md` docs**

Implement `MemoryPageContentDigest`, `EditFileCheckedWithCtx`, `ApplyLiteralOrFuzzyEdit`, `GrepConfinedFilesWithContentFilter`, `session_memory_grounding.go`, `renderMemoryScope` / `execMemoryRead` / `execMemorySearch` / `execMemoryEdit` wiring (keeping `stampMemoryPage` `grounding_clean_digest` emission gated on a `Session` flag activated in Task 9), `TurnMemoryContext` scrubbing in `grounding_scrub.go`, and evergreen updates to `docs/product/subsystems.md` (`S24`) and `docs/product/memory.md`.

- [ ] **Step 4: Run `memorypages`, `execenv`, `agent` memory/scrub tests, copyonly audit, and `make docs-check`**

Run: `go test ./agent/memorypages/... ./agent/execenv/... ./internal/devtool/copyonly/... && go test ./agent -run 'TestMemory|TestGroundingScrub' && make docs-check`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add agent/ docs/product/subsystems.md docs/product/memory.md
git commit -m "feat(memory): filter Grounding-derived memory pages with byte-matched cross-session provenance and byte-bound tool checks"
```

---

### Task 8: Local and Cross-Hub Manager `read_transcript(hub=...)` / `Discover` / `Read` Projection & Cursor Isolation (Stage 3b-3)

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_source.go`
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_source_paging_test.go`
- Modify: `cmd/evener-hub/session_tools.go`
- Modify: `cmd/evener-hub/session_reads.go`
- Modify: `cmd/evener-hub/app_threadread.go`
- Create: `cmd/evener-hub/manager_grounding_projection_test.go`

**Interfaces:**
- Consumes: `agent.ScrubGoogleGroundingTranscriptEntries` (Tasks 6 & 7), `appsource.WithManagerInputCaller` (`remote_hub_source.go:261`).
- Produces:
  - `appsource.ManagerInputCaller(ctx context.Context) string` in `cmd/evener-hub/internal/appsource/remote_hub_source.go`
  - Projection-keyed `(threadRef, managerProjection bool)` paging cache and `CursorIdentity` in `RemoteHubSource.ListItemCandidates`
  - Authenticated `MethodHubSessionToolsInput` routing for Manager read methods (`MethodThreadList`, `MethodThreadRead`, `MethodThreadHistoryRead`, `MethodThreadTurnsList`, `MethodThreadHistoryTurnsList`, `MethodEvenerThreadRetainedItemFetch`) + `MethodNotFound` direct-and-indirect full-snapshot legacy-peer provenance gate
  - Isolated `o.managerCanonicalItems *transcriptCanonicalStore` and owning-Hub Manager `ScrubGoogleGroundingTranscriptEntries` projection in `cmd/evener-hub`

- [ ] **Step 1: Write failing tests in `cmd/evener-hub/manager_grounding_projection_test.go` and `remote_hub_source_paging_test.go`**

Cover every Spec §5.3 Stage 3b-3 case (lines 300, 338, 346):
- Local Manager `read_transcript(hub=...)` initial `Turns` window/page and subsequent `fetchCursor` `Fetch` withhold pre-cutover Grounding turns, exposed follow-up turns with no search marker, and dedicated `deliverCommunicate` `TurnCommunicate` items while preserving clean sibling turns, keeping human UI `o.Read` / `handleAppThreadTurnsList` unredacted, and refusing Manager `o.fetch` on a human-UI `fetchCursor` token.
- Two-hub Manager `Turns` + `fetchCursor` `Fetch` over `MethodHubSessionToolsInput`: owning remote Hub runs `ScrubGoogleGroundingTranscriptEntries` so exposed follow-up turns with no search marker are withheld while clean siblings survive; a human read populating an older range containing Grounding on `RemoteHubSource.ListItemCandidates` never leaks unredacted candidates when a Manager pages into that range, and alternating human and Manager cursors keeps both cursors valid.
- Older un-upgraded remote peer returning `appwire.MethodNotFound`: refused with typed `appwire.SessionUnavailable` both when Grounding sits on an earlier page outside the requested `TurnWindow` and when a marker-free parent transcript contains an indirect carrier (such as a `<delegate-notification>` report), while verified Grounding-free multi-page sessions on the older peer and all human UI reads succeed.

- [ ] **Step 2: Run targeted Hub tests to verify RED failure**

Run: `go test ./cmd/evener-hub/... -run 'TestManagerGrounding|TestRemoteHubSource.*Manager'`
Expected: FAIL.

- [ ] **Step 3: Implement Stage 3b-3 across `cmd/evener-hub/internal/appsource` and `cmd/evener-hub`**

Follow Spec §5.3 lines 300 and 338 verbatim: isolate `o.managerCanonicalItems`, project local Manager reads/discoveries through `agent.ScrubGoogleGroundingTranscriptEntries` when `appsource.ManagerInputCaller(ctx) != ""`, key `RemoteHubSource.ListItemCandidates` paging state and `CursorIdentity` by `(threadRef, managerProjection)`, route Manager read RPCs through `MethodHubSessionToolsInput` + `registerRemoteInput`, and enforce the `MethodNotFound` direct-and-indirect full-snapshot legacy-peer gate.

- [ ] **Step 4: Run `cmd/evener-hub` and `agent` Manager hub tests to verify GREEN**

Run: `go test ./cmd/evener-hub/... && go test ./agent -run 'TestManager|TestHub'`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/
git commit -m "feat(hub): scrub Manager transcript projections across local and remote hubs with isolated paging caches"
```

---

### Task 9: Remove Legacy `Session` Google `web_search`, Activate Live `Session` Grounding Scrub / Responses Anchor Skip / Clean Memory Stamps, and Suppress Non-Authorizing `ProtocolGoogle` Tool Calls (Stage 3b-4)

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
- Modify: `agent/session_init.go`
- Modify: `agent/session_compaction.go`
- Modify: `agent/session_self_compact.go`
- Modify: `agent/session_notes_projection.go`
- Modify: `agent/session_notes_context.go`
- Modify: `agent/session_tools_notes.go`
- Modify: `agent/session_namer.go`
- Modify: `agent/session_execution.go`
- Modify: `agent/session_tools_memory.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/internal/contextmgr/elicit.go`
- Modify: `agent/internal/contextmgr/checkpoint.go`
- Modify: `agent/internal/contextmgr/summarize.go`
- Modify: `agent/internal/contextmgr/strategy_session_log.go`
- Modify: `agent/internal/contextmgr/strategy_ooda.go`
- Modify: `agent/internal/contextmgr/fork_summarize.go`
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
- Consumes: `agent.ScrubGoogleGroundingTranscriptEntries` / `scrubGoogleGroundingTurns` (Task 6), `memorypages.MemoryPageContentDigest` (Task 7).
- Produces:
  - Complete removal of `Session`'s client-side `web_search` function tool (`DefWebSearch`, `tool_web_search.go`, `capabilityWebSearch`, `reapplyProviderSpecificTools`)
  - Live `Session` restore/outbound/compaction/notes/namer/`stampMemoryPage` Grounding scrub & `Turn.GroundingScrubbed = true` + `APIAttemptRequest.GroundingScrubbed = true` stamping + `selectResponsesContinuationAnchorCandidate` pre-cutover grounded-anchor skip
  - Storage-independent `ProtocolGoogle` non-authorizing status tool-call suppression + `skipHistory = false` surviving-thinking persistence + single-call failure settlement in `agent/session_lifecycle.go`

- [ ] **Step 1: Write failing tests in `agent/tool_web_search_test.go`, `agent/grounding_scrub_test.go`, and `agent/session_google_settlement_test.go`**

Cover Spec §5.3 Stage 3b-4 (lines 275, 277-280, 339, 345-346):
- `tool_web_search_test.go`: `Session` registers no `web_search` function tool and dispatches zero side Grounding calls on `ProtocolGoogle` (even when `providers.toml` sets `web_search = true`).
- Live `Session` Grounding scrub + OpenAI Responses anchor skip + `grounding_clean_digest` writer: restoring a pre-cutover session that ran Grounding and switched to OpenAI Responses rejects the pre-cutover `ResponseID` anchor, sends scrubbed full history (with exposed text, tool calls/results, and `encrypted_content`/summary stripped), establishes a clean post-cutover `ResponseID` (`Turn.GroundingScrubbed == true`), and resumes `responses_delta` continuation on the next turn; live `ElicitNote` / `checkpointFold` / `summarizeFold` / `SessionLogStrategy.AfterAction` scrub inputs before summarizing and stamp `GroundingScrubbed = true`; `stampMemoryPage` stamps `grounding_clean_digest` when `s.activeRoundGroundingScrubbed()` is true, restoring a previously withheld page on full `memory_write`.
- `session_google_settlement_test.go`: across `"incomplete"`, `"budget_exceeded"`, `"requires_continuation"`, `"cancelled"`, and `FinishReasonContentFilter` (`google_accepted_stored: true` vs `false`), `Session` strips all `ContentToolCall` parts and executes **zero** tool handlers; `"incomplete"`, `"budget_exceeded"`, `"requires_continuation"`, `"cancelled"`, and accepted-stored safety settle immediately at a failure boundary after **one** model call (while stateless safety without `google_accepted_stored` routes through `handleNoToolCalls` with `len(calls) == 0`); and a response carrying `ContentThinking` + `ContentToolCall` with empty text sets `skipHistory = false`, persisting only the surviving `ContentThinking` on `.transcript.jsonl` reload.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./agent -run 'TestSessionGoogleSettlement|TestWebSearch'`
Expected: FAIL.

- [ ] **Step 3: Implement `web_search` deletion, live `Session` Grounding scrub activation, and `session_lifecycle.go` `ProtocolGoogle` settlement**

Follow Spec §5.3 lines 275, 277-299, and 339:
- Delete `agent/tool_web_search.go` and remove `DefWebSearch`, `deps.webSearchEnabled`, `reapplyProviderSpecificTools`, and `capabilityWebSearch`, updating every listed `agent/` test file in the same commit (check `internal/devtool/copyonly/errstring_allowlist.tsv` if any allowlisted test file's assertion count changes!).
- Activate restore-time and outbound `scrubGoogleGroundingTurns` (`session_init.go`, `session_model_call.go`, `contextmgr`, `session_compaction.go`, `session_self_compact.go`, `session_notes_*.go`, `session_namer.go`, `session_execution.go`, `session_tools_memory.go`), `selectResponsesContinuationAnchorCandidate` grounded-anchor skip, and `Turn.GroundingScrubbed = true` / `llm.WithAPIGroundingScrubbed(ctx)` stamping.
- Implement `ProtocolGoogle` non-authorizing status tool-call suppression, `skipHistory = false` when `len(resp.Message.Content) > 0`, and pre-`handleNoToolCalls` failure-boundary settlement in `agent/session_lifecycle.go`.

- [ ] **Step 4: Run full `agent` and `copyonly` test suites to verify GREEN**

Run: `go test ./agent/... ./internal/devtool/copyonly/...`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add -A agent/ internal/devtool/copyonly/
git commit -m "feat(agent): remove legacy Google web_search tool, activate live Grounding scrub, and gate ProtocolGoogle tool dispatch on authorizing statuses"
```

---

### Task 10: `*llm.AcceptedInteractionError`, Opt-In `StreamRunner.DeadlineError` & `Call.BodyTimeoutError`, and `Session`/Summarizer/CheapModel/Namer Terminal Guards

**Files:**
- Modify: `llm/errors.go`
- Modify: `llm/context_errors.go`
- Modify: `llm/retry.go`
- Modify: `llm/stream_retry.go`
- Modify: `llm/internal/transport/runner.go`
- Modify: `llm/internal/transport/runner_test.go`
- Modify: `llm/providers/internal/protocolhttp/exchange.go`
- Modify: `llm/providers/internal/protocolhttp/exchange_test.go`
- Create: `llm/accepted_interaction_error_test.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/internal/contextmgr/summarize_route.go`
- Modify: `agent/internal/contextmgr/fuzz_fc1_summarization_routes_test.go`
- Modify: `agent/internal/cheapmodel/caller.go`
- Modify: `agent/session_namer.go`
- Modify: `agent/session_google_settlement_test.go`

**Interfaces:**
- Consumes: `llm.Classify`, `llm.ErrorClassPermanent` (`llm/classify.go`), `transport.StreamRunner`, `protocolhttp.Call`.
- Produces:
  - `*llm.AcceptedInteractionError` and `llm.NewAcceptedInteractionError(provider, interactionID string, cause error, raw map[string]any) *AcceptedInteractionError` (`StatusCode() == 0`, `Retryable() == false`, `Unwrap() error`, `Raw() map[string]any`, `RewriteErrorProvider` support) in `llm/errors.go`
  - Opt-in `ParentCtx context.Context` and `DeadlineError func(ctxErr error) error` on `transport.StreamRunner` (`llm/internal/transport/runner.go`, leaving `r.DeadlineError == nil` precedence unchanged for `responses` and all other adapters)
  - Opt-in `BodyTimeoutError func(status int, raw map[string]any, readErr error) error` on `protocolhttp.Call` (`llm/providers/internal/protocolhttp/exchange.go`)
  - Terminal `*llm.AcceptedInteractionError` short-circuiting across `WrapContextError`, `Retry`, `RetryStream`, `modelFallbackEligible`, `Session` fallback loop / `handleModelError` / quota-wait / context-length / document / content-filter recovery, `shouldFallbackSummarizationModel`, `cheapmodel.refusesModel`, and `isTemperatureUnsupported`

- [ ] **Step 1: Write failing tests in `llm/accepted_interaction_error_test.go`, `runner_test.go`, `exchange_test.go`, and `agent/session_google_settlement_test.go`**

Cover Spec §4.2 (lines 258-262) and Stage 3c error-boundary requirements (lines 340, 347):
- `*AcceptedInteractionError` satisfies `StatusCode() == 0`, `Retryable() == false`, `llm.Classify(err) == llm.ErrorClassPermanent`, preserves `InteractionID`, `Unwrap()`, `Raw()`, and `RewriteErrorProvider`.
- `WrapContextError`, `Retry`, and `RetryStream` preserve `*AcceptedInteractionError` on `context.DeadlineExceeded` (`TimeoutPerStep` / `TimeoutTotal` expiry) while honoring explicit `context.Canceled`.
- `StreamRunner.Run`: when `DeadlineError == nil`, preserves existing `ctx.Err() -> *FatalStreamError -> incomplete` precedence (`responses` `KindTimeout` unaffected); when `DeadlineError != nil`, enforces `(i) ParentCtx/ctx Canceled -> (ii) *FatalStreamError -> (iii) DeadlineError(ctx.Err()) -> (iv) incomplete`.
- `protocolhttp.Do`: when `BodyTimeoutError != nil`, HTTP 2xx body-read timeout after decoding a valid JSON object with non-empty `"id"` returns `BodyTimeoutError` (`*AcceptedInteractionError`), leaving `context.Canceled`, invalid JSON, and non-2xx untouched.
- `Session`, summarizer (`shouldFallbackSummarizationModel`), `cheapmodel.refusesModel`, and `session_namer` (`isTemperatureUnsupported`, even on an error naming `"generation_config.temperature"`): `*AcceptedInteractionError` makes **one** submission, skips all fallbacks/retries/compaction/quota-waits, and surfaces `InteractionID` and `cause`.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm/... -run TestAcceptedInteractionError && go test ./agent/... -run TestAcceptedInteractionError`
Expected: FAIL (`AcceptedInteractionError` undefined).

- [ ] **Step 3: Implement `*llm.AcceptedInteractionError`, `StreamRunner`/`protocolhttp.Call` hooks, and `Session`/auxiliary terminal guards**

Implement all listed `llm`, `transport`, `protocolhttp`, `agent`, `contextmgr`, `cheapmodel`, and `session_namer` changes per Spec §4.2 lines 258-262 and §5.3 line 322 (also updating `isTemperatureUnsupported` in `session_namer.go` to check `registry.TemperaturePath(registry.ProtocolGoogle)`).

- [ ] **Step 4: Run `llm` and `agent` tests to verify GREEN**

Run: `go test ./llm/... ./agent/internal/contextmgr/... ./agent/internal/cheapmodel/... && go test ./agent -run 'TestAcceptedInteractionError|TestSessionGoogleSettlement|TestSessionNamer'`
Expected: PASS with pristine output.

- [ ] **Step 5: Commit**

```bash
git add llm/ agent/
git commit -m "feat(llm,agent): add permanent AcceptedInteractionError and post-acceptance timeout/fallback guards"
```

---

### Task 11: Atomic `ProtocolGoogle` `/interactions` Cutover, `llm/registry` Overlay & `VertexSupportsGeminiModel`, `(Instance, WireID)` Provenance, `evener-fuzz-harvest`, and `google-vertex-express` Retirement (Stage 3c)

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
- Modify: `llm/registry/resolve_test.go`, `golden_test.go`, `instances_test.go`, `overlay_test.go`, `prune_test.go`, `registry_shape_test.go`
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
- Modify: `cmd/evener-hub/app_auth.go`, `cmd/evener-hub/e2e_model_switch_test.go`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceDialogs.test.tsx`
- Modify: `docs/llm-providers.md`, `docs/environment-variables.md` (via `make generate`), `docs/product/subsystems.md` (`S15`, `S16`), `docs/developing-evener/environment.md`, `docs/developing-evener/config-knobs.md`

**Interfaces:**
- Consumes: Tasks 1-10 (`StampContinuationIDHash`, `CombinedSearchAndFunctions`, `IsVertexGeminiOmni`, `interactions_response.go`, `interactions_stream.go`, `*AcceptedInteractionError`, `DeadlineError`, `BodyTimeoutError`, cold/live/delegate model recovery).
- Produces:
  - `ThinkingData.ReplayableGoogleThoughtFor(instance, wireModel string) bool` in `llm/types.go`
  - `registry.VertexSupportsGeminiModel(id string) bool`, `registry.FieldGoogleThinkingLevel`, `registry.FieldGoogleThinkingSummaries`
  - Full `ProtocolGoogle` `/interactions` wire implementation (`BuildBody`, `Complete`, `Stream`, `CountTokens`) + 1-shot unsigned legacy tool-call `400` text-projection fallback + `FinalizeBody` policy locks
  - Complete retirement of `google-vertex-express`, `GOOGLE_VERTEX_API_KEY`, and `GOOGLE_VERTEX_EXPRESS_BASE_URL`

- [ ] **Step 1: Write/update failing tests across `llm/registry`, `llm/providers/google`, `llm`, `agent`, `envvars`, `cmd/llmcall`, `cmd/evener-fuzz-harvest`, and `frontend`**

Cover every Spec §1, §2, §4.2, §5.1, §5.3, and Stage 3c bullet (lines 82-205, 257-262, 270, 276, 296, 323, 340, 342-348):
- `llm/providers/google/*_test.go` & 5 fuzz harnesses: `POST .../interactions`, `body["model"] = res.WireID`, unanchored `"store": false` default vs anchored `previous_interaction_id` (`"store": true`) vs `fields.store = false` omission, `FinalizeBody` locking `"stream"`, `"model"`, `"input"`, `"tools"`, `"response_format"`, `"previous_interaction_id"`, `"store"`, `"agent"`, `"background"`, `"continuation_token"`, `"cached_content"`, `"service_tier": "deferred"`, retired camelCase constants, `reasoning = false` stripping, `tool_choice: "validated"` vs `"auto"`, `reconcileOutputField`, Unicode code-point `"labels"` filtering (`"équipe"` kept, `"team/id"` skipped on `req.Metadata`, invalid explicit `ProviderOptions`/`Transport.Body` labels rejected), `ProviderOptions["google"]` deep-cloning, lazy `user_input` coalescing across skipped-thinking assistant turns, `RoleAssistant` `ContentImage` + `GoogleStep` boundaries, `(Instance, WireID)` `ReplayableGoogleThoughtFor` and `function_call.signature` replay, `ContentWebSearch` replay `ConfigurationError`, JSON-string `function_result.result` + raster-image subcontent + PDF side channel, cyclic tool/response schemas (`openaichat.CyclicSchema`), `sanitizeInteractionResponseSchema`, `RequiresStreamingCompleteContext` -> `CompleteViaStream`, sticky `acc.sawQueuedOrTerminal` (`queued -> in_progress -> EOF/deadline/error` -> `*AcceptedInteractionError`), `AcceptedInteractionError` precedence over 1-shot unsigned-legacy-tool-call `400` fallback + zero-published-output `Stream` retry with single `StreamEventStreamStart`, Vertex regional (`us`/`eu`/cross-border residency disclosure) warning & 404 remedy, `:generateContent` endpoint-override `ConfigurationError`, and `VertexSupportsGeminiModel` filtering (`gemini-1*`, `gemini-2*`, `gemini-3-flash-preview`, `gemini-3-pro-preview`).
- `agent/session_replay_provenance_test.go`: primary `replayScope.WireID` + `ModelFallbacks` `scope.WireID` rebinding (`Model A -> Model B` keeps B's prior thoughts/signatures while omitting A's; same-`WireID` aliases retain signatures; `TestExpandHistory_InFlightTurn_NotFiltered` stays green).
- `llm/generate.go` & `cmd/llmcall`: active-`Execute` loop preflight rejection on anchored/combined `ProtocolGoogle` requests; `cmd/llmcall --web-search` pre-HTTP rejection on `ProtocolGoogle`.
- `cmd/evener-fuzz-harvest`: `"google_interactions_stream"` -> `dirGeminiStream`, `"google_interactions"` / `"google_generate_content"` / `"google_stream_generate_content"` terminal no-target matches, `isGoogleInteractionsStream`.
- `envvars`, `llm/registry`, `wirecapture`, `e2e_model_switch_test.go`, `instanceDialogs.test.tsx`: `google-vertex-express` retired cleanly and cold/delegate recovery from retired `"google-vertex-express"` / `"google-vertex/gemini-2.5-flash"` verified end-to-end.

- [ ] **Step 2: Run targeted tests to verify RED failure**

Run: `go test ./llm/registry/... ./envvars/... ./cmd/llmcall/... ./cmd/evener-fuzz-harvest/...`
Expected: FAIL before cutover implementation.

- [ ] **Step 3: Implement the atomic Stage 3c cutover across `llm`, `llm/registry`, `llm/providers/google`, `envvars`, `agent`, `cmd`, `frontend`, goldens, and docs**

Implement every Stage 3c file change per Spec §1, §2, §4.2, §5.1, §5.3, and line 340, run `go test ./llm/registry -update` (and `wirecapture` fixture update if applicable) to refresh surviving Google/Vertex goldens, run `make generate` to regenerate `docs/environment-variables.md`, and update `docs/llm-providers.md`, `docs/product/subsystems.md` (`S15`, `S16`), `docs/developing-evener/environment.md`, and `docs/developing-evener/config-knobs.md` (run `npx biome check --write` from `cmd/evener-hub/frontend` on touched TS files).

- [ ] **Step 4: Run Go suites, `copyonly`, frontend typecheck/unit test, and `make docs-check` sequentially**

Run:
1. `go test ./llm/... ./envvars/... ./internal/apptranscript/... ./cmd/llmcall/... ./cmd/evener-fuzz-harvest/... ./internal/devtool/copyonly/...`
2. `go test ./agent/... ./cmd/evener/... ./cmd/evener-hub/...`
3. `make docs-check`
4. `make test-web`
Expected: PASS across every suite with pristine output.

- [ ] **Step 5: Commit**

```bash
git add -A llm/ envvars/ agent/ cmd/ docs/
git commit -m "feat(google,registry): switch ProtocolGoogle to Interactions API and retire google-vertex-express"
```

---

### Task 12: End-to-End Session Rollover & Legacy-Signature Restore Regressions + Opt-In Live Verification Suite (Stage 4)

**Files:**
- Create: `agent/google_interactions_rollover_e2e_test.go`
- Create: `llm/providers/google/live_interactions_test.go`
- Create: `agent/google_interactions_live_test.go`
- Modify: `docs/llm-providers.md` (if live `EVENER_LIVE_TESTS=1` probe surfaces any surface-specific note)

**Interfaces:**
- Consumes: Full Interactions `ProtocolGoogle` adapter, `Session` lifecycle, and restore pipeline (Tasks 1-11).
- Produces:
  - Deterministic `Session` regression suite (`TestSessionGoogleInteractions_DeepMultiTurnNotificationRolloverRegression` & `TestSessionGoogleInteractions_PreCutoverSignedToolCallRestoreAndFallback`) in `agent/google_interactions_rollover_e2e_test.go`
  - Opt-in `EVENER_LIVE_TESTS=1` verification suite (`llm/providers/google/live_interactions_test.go` & `agent/google_interactions_live_test.go`) skipped cleanly by default offline runs

- [ ] **Step 1: Write deterministic end-to-end `Session` regression tests and opt-in `EVENER_LIVE_TESTS=1` live tests**

Per Spec Stage 4 (line 341):
1. `TestSessionGoogleInteractions_DeepMultiTurnNotificationRolloverRegression` (`agent/google_interactions_rollover_e2e_test.go`): seeds a `Session` on `google/gemini-4-carbon` against a scripted `/interactions` HTTP server with a multi-turn history representative of session `03507HVKeWCiXCzGHXBqyK` (prior native `thought` + `function_call` + `function_result` + deferred `<job-notification>` / `<delegate-notification>` steering + new user turn), asserting exact outbound `steps[]` order, `"store": false`, `call_id`/`name` `function_result` pairing ahead of a **single** coalesced `user_input` step (zero adjacent `user_input` steps), native `thought` `signature`+`summary` round-trip, and clean next-turn settlement.
2. `TestSessionGoogleInteractions_PreCutoverSignedToolCallRestoreAndFallback` (`agent/google_interactions_rollover_e2e_test.go`): restores a representative pre-cutover `generateContent` `.transcript.jsonl` carrying `ToolCallData{ThoughtSignature: "legacy_sig", GoogleInteraction: false}` and historical Grounding on `google/gemini-3.8-flash`, verifying (a) historical Grounding is scrubbed while completed tool results survive, (b) Attempt 1 omits `legacy_sig` on `function_call` steps, and (c) when the scripted endpoint returns HTTP `400` `INVALID_ARGUMENT` referencing `thought_signature`, `Complete` and zero-output `Stream` each recover transparently in 1 retry by rendering only unsigned legacy tool pairs into structured `model_output` / `user_input` blocks (with a single `StreamEventStreamStart` on `Stream`).
3. Opt-in `EVENER_LIVE_TESTS=1` tests (`llm/providers/google/live_interactions_test.go` and `agent/google_interactions_live_test.go`, immediately calling `t.Skip` unless `os.Getenv("EVENER_LIVE_TESTS") == "1"` and the respective `GEMINI_API_KEY`/`GOOGLE_API_KEY` or `GOOGLE_VERTEX_PROJECT` credentials are present): exercises on both `google` and `google-vertex` (`GOOGLE_VERTEX_LOCATION=global`) (a) a real `Session` turn with default `Profile.ProviderOptions()` (`safety_settings` wire check) plus direct `Complete` + `Stream` 2-turn stateless function round, (b) settling a post-upgrade turn on a pre-cutover legacy-signed tool-call history, and (c) a 2-turn stateful combined `google_search` + `function` round (`web_search = true` test config layer, requiring Turn 1 to emit both paired `google_search_call`/`google_search_result` and `function_call` before sending `previous_interaction_id` + `[function_result]` + active `Tools` + `WebSearch: true`).

- [ ] **Step 2: Run default offline tests (and opt-in live tests if credentials exist in environment) to verify GREEN**

Run: `go test -v ./agent -run 'TestSessionGoogleInteractions_' && go test -v ./llm/providers/google -run 'TestLiveInteractions'`
Expected: Deterministic E2E regressions PASS; live tests skip cleanly under default offline env (and run if explicitly invoked with `EVENER_LIVE_TESTS=1` and configured credentials).

- [ ] **Step 3: Commit**

```bash
git add agent/google_interactions_rollover_e2e_test.go agent/google_interactions_live_test.go llm/providers/google/live_interactions_test.go docs/llm-providers.md
git commit -m "test(google,agent): add multi-turn Interactions rollover, legacy-signature restore, and opt-in live verification suites"
```
