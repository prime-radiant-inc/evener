# Road to world-class Go — `agent` and `llm` as Pike-grade libraries

Date: 2026-05-31 · Synthesis of a 10-reviewer / 8-dimension audit (3 independent panels on architecture-api).
Scope: `agent/` and `llm/` are LIBRARIES; `cmd/serf`, `cmd/serf-tui`, `cmd/serf-hub` are APPLICATIONS.
Read-only audit; every finding is grounded in `file:line` evidence verified against HEAD (`3b5a1af0`).

This document is what Jesse ratifies. Section 2 (the proposed public APIs) is the decision surface;
Sections 3–5 are the supporting findings, roadmap, and standing gate.

---

## 0. Progress (live; updated 2026-05-31)

Ratified: externally-importable libraries; execute to the done-bar. Merges are ff-only to local main (never pushed).

- **Phase 0 — Correctness — ✅ COMPLETE** (all merged to local main @ `94ba4d41`)
  - P0.1 events race + delete `emit` recover() — PRI-1939 (option C: `eventsMu` RWMutex + `eventsClosed`) — merged `3d4bc474`.
  - P0.2 synchronize SetModel/SetReasoningEffort reads on the turn path — PRI-1958 (locked-accessor `currentProfile()` + per-round snapshot) — merged `109b61ad`.
  - P0.3 finish + verify hub_model split — PRI-1956 (4563→232) — verified pure-move, merged.
  - P0.4 delete `chanStream.Send` recover() + document single-producer contract — PRI-1959 — merged `94ba4d41`.
- **Phase 1 — Honest error contracts — ✅ COMPLETE** (all merged to local main @ `7bbae6be`)
  - P1.1 llm error wrapping (E1/E2/E6) — PRI-1960 — merged `34176b0b` (cause-threading + `WrapContextError` populates + 3 behavior-preserving consumer fixes: `retryableError`/`isTurnCancellation`/`queuedInputDrainContext`).
  - P1.2 stream-read surfacing (E3) — PRI-1963 — merged `c6dbd647` (capture `parseErr` → StreamError carrying the cause in all 5 adapters; responses.go fallback sentinel preserved).
  - P1.3 error hygiene (E4/E5/E8/E9) — PRI-1967 — merged `7bbae6be` (EventWarning vs swallow; `errNoCredentials` sentinel + `loginRequiredError` `%v`→`%w` so isUnconfigured is `errors.Is`-based; `errors.As`; apilog Sync symmetry).
- **Phase 2 — Docs + naming gate — ▶ IN PROGRESS** — P2.1 package `doc.go` for `llm` (PRI-1968, merged `c080f0f1`) and `agent` (PRI-1969, merged `95ed966b`) done (both verified against the code, no invented details). P2.1 `llm` runnable Example tests done (PRI-1970, merged `7ab579d2`: `ExampleGenerate`/`ExampleClassify`, `// Output:`-verified, hermetic fake adapter) → **`llm` is now documented + Example-backed**. Remaining P2.1/P2.2: agent Example tests + more llm examples (Stream/GenerateObject), godoc sweep over every export + the identifier/godoc lint gate, the fuller Session concurrency doc (C4), P2.3 kill the naming-ignore pragmas. Parallel-safe with Phase 3.
- **Phase 3 — Library boundary + API surface — ▶ IN PROGRESS** — P3.1 §2.0 keystone DONE: `internal/providerconfig` → public `llm/providercfg` (PRI-1971, merged `345e5b7b`; verified pure-move via multiset diff; `NewFromProviders`/`ResolveProfileFromConfig` now name a public type → libraries externally importable). `WorkspaceInfo` promoted → `agent.WorkspaceInfo` public (PRI-1972, `329b90e4`) — both §2.0 internal-type-in-public-signature leaks now closed; a scan finds no others. **Remaining:** add the "no internal/ types in exported agent/llm signatures" CI gate (pure lock-in — passes now); **CURATE the new public `providercfg` surface** — the promotion exposed serf-app-specific symbols that don't belong in a public config schema (`DefaultStateRoot`, `BaseURLEnvVar`, `Seed`, the `KnownType*` allow-list) → relocate to a serf-internal home (P3.2/P3.3). Then P3.2 zero-risk unexports, P3.3 slim SessionConfig, P3.4 ToolRegistry leak.
- **Phase 4 — Black-box test migration (XL, gates subpackage extraction) — pending.**
- **Phase 5 — Decomposition + dedup — pending** (P5.1 processOneInput god-function; P5.2–P5.6).
- **Phase 6 — Subpackages + actor core (deferred) — pending** (needs PRI-1947 seams + Phase 4).

**Milestone — `go test -race ./...` GREEN module-wide** (2026-05-31): the full race gate now passes. Beyond Phase 0's agent/llm fixes, running the whole-module gate surfaced two PRE-EXISTING `cmd/serf-hub` races the agent/llm-focused audit never looked at — both fixed: PRI-1961 (relay idle-exit test hooks were package globals racing across tests → instance-scoped onto `WebConfig`) and PRI-1962 (codex launcher's single-shot `Exited` channel was peeked via `cmd.ProcessState` racing `cmd.Wait()` → broadcast closed-channel + retired the single-shot fragility).

Standing gate (§5): `-race ./...` now green (milestone above); not yet wired into CI — establish once Phase 1–2 land.

---

## 1. The bar

"World-class" / "Pike would demo it at Google I/O" means, concretely, for `agent` and `llm`:

1. **Correct under `-race`, always.** `go test -race ./...` is green today, but two unexercised races
   and a `recover()`-as-control-flow survive (Section 3.A). A world-class library has no race the test
   suite merely fails to hit, and never uses `recover()` to paper over a send-on-closed-channel.
2. **A small, curated, documented public surface.** A newcomer opening pkg.go.dev sees a package overview,
   a runnable example, and ~50–70 deliberate symbols — not ~350 exported names, 90+ of which no caller uses,
   and not a bare `package X // import …` with no synopsis.
3. **Tests through the public surface.** Behavioral tests are black-box (`agent_test` / `llm_test`),
   proving the contract a consumer actually gets. Internal helpers may be white-box, but white-box coupling
   must not be the thing that *prevents* the API from being tightened or the package from being split.
4. **No exported stutter, correct initialisms, consistent receivers.** Already essentially met mechanically;
   the gap is that nothing *enforces* it.
5. **Honest error contracts.** Exported error types whose `Unwrap()`/`errors.Is` machinery actually works,
   not a `cause` field that no constructor ever populates.
6. **Importable as a standalone module.** No public signature names a type from the application repo's
   `internal/` tree (today two constructors do — they are literally uncallable from outside the module).

### Library vs application split

| Concern | Libraries (`agent`, `llm`) | Applications (`cmd/*`) |
| --- | --- | --- |
| Public API obligation | Yes — curated, documented, stable, stutter-free | None — internal cleanliness only |
| God-file / god-function | Decompose; same bar | Decompose; same bar |
| Package doc + examples | Required | Not required |
| `-race` + `go vet` + gofmt | Required | Required |
| Godoc-completeness lint | Required (scoped to `agent/…`, `llm/…`) | Exempt |
| Test style | Black-box for behavior; white-box only for internal units | Any |

The app findings (hub `app_rpc.go`/`hub_model.go` god-files, the hub relay-manager extraction) are
real but held to the *internal-cleanliness* bar, and `hub_model.go` is already done (PRI-1956 reduced it
from 4,563 → 232 lines). They appear in the roadmap as lower-priority, app-scoped items.

---

## 2. Proposed PUBLIC API (panel-reconciled, decision-ready)

Three independent panels reviewed architecture-api. They **agree** on the diagnosis and on almost every
keep/hide call; the few genuine disagreements are flagged inline and resolved with a recommendation.

### 2.0 The one cross-cutting blocker (all three panels, severity critical)

`agent.ResolveProfileFromConfig(cfg providerconfig.Config, …)` (`agent/resolve.go:26`) and
`llm.NewFromProviders(cfg providerconfig.Config, …)` (`llm/providers_config.go:45`) both name
`primeradiant.com/serf/internal/providerconfig.Config`. Go forbids importing another module's
`internal/`, so **no external consumer can name the argument type** — these config-driven constructors,
the exact entry points a library consumer needs, are uncallable outside the serf module. Separately,
`agent.EnvironmentInfo.Workspace` is typed `workspace.WorkspaceInfo` from `agent/internal/workspace`
(`agent/profile.go:39`) — a public field whose type external code cannot name.

**Resolution (ratify):** promote the config schema (`Config`, `InstanceConfig`, the `Type`/`APIStyle`
enums, `BehaviorTag`) out of `internal/` into a public package owned by the lower layer —
**`llm/providercfg`** — and have `agent` re-use it. Promote `WorkspaceInfo` out of
`agent/internal/workspace` to a package whose type `EnvironmentInfo` can legally expose. This is the
hard precondition for "importable library"; until it lands, neither package is consumable standalone.
(If Jesse decides the libraries stay serf-internal forever, we instead drop the standalone framing for
these signatures — but the default recommendation is to promote.)

### 2.1 `llm` — proposed public API

`llm` is close to library-grade. The model: a **two-tier surface** that the package doc must name
explicitly so a reader knows which symbols are "for callers" vs "for adapter authors."

**Tier A — Caller API (keep exported, ~50 symbols).**
- Construction: `Client`, `NewClient`, `NewFromEnv`, `NewFromProviders` (after §2.0), `EnvOption`,
  `WithStateDir`, `WithAPILogContext`.
- Wire model: `Request`, `Response`, `Message`, `ContentPart` + the `*Data` payload structs;
  `Role`/`ContentKind`/`FinishReason` + their const blocks; `Tool`, `ToolDefinition`, `ToolChoice`,
  `ResponseFormat`; the message constructors `System`/`Developer`/`User`/`Assistant`/`ToolResult`/`ToolResultNamed`.
- Entry points: `Generate`, `GenerateObject`, `Client.Stream`, `StreamEvent`/`StreamEventType`,
  `GenerateOptions`/`GenerateObjectOptions`/`GenerateResult`.
- Telemetry/catalog: `Usage`, `ModelInfo`, `ModelCatalog`, `EmbeddedModelCatalog`,
  `APILogger`/`NewAPILogger`/`APILogRequest`.
- Errors (Tier-A subset): the `Error` interface, `Classify`/`ErrorClass`, `ErrorFromHTTPStatus`,
  `ErrStreamUnsupported`, and the one concrete type a consumer type-asserts —
  `ContentFilterError` (`agent/session_lifecycle.go:612`).

**Tier B — Adapter SPI (stays exported; used only by `llm` + `llm/providers/*`, never by apps).**
- `ProviderAdapter`, the capability interfaces actually used (`ToolChoiceSupporter`, `NonDefaultEligible`,
  and `Warning`/`RateLimitInfo` as `Response` field types), `EnvConfig`,
  `InstanceAdapterFactory`/`EnvAdapterFactory`, `RegisterEnvAdapterFactory`/`RegisterInstanceAdapterFactory`.
- Streaming/transport primitives: `ChanStream`, `StreamAccumulator`, `SSEEvent`/`ParseSSE`/SSE options,
  `RetryPolicy`/`Retry`/`SleepFunc`, the `AdapterTimeout` helpers.
- Normalization helpers: `NormalizeFinishReason`, `ReasoningBudget`, `IntFromAny`, `ValidateToolName`.
- These must stay exported because providers are separate packages. The package doc names them as the SPI;
  consumers ignore them.

**Unexport / delete (zero qualified external refs anywhere — verified):**
- Dead middleware tier (no implementor — YAGNI delete): `Middleware`, `MiddlewareFunc`, `CompleteFunc`,
  `StreamFunc` (each 0 external refs, verified `grep -rhoE '\bllm\.Middleware\b' …` = 0).
- Dead capability interfaces: `Closer`, `Initializer`, `ModelLister` (0 external refs).
- Dead/redundant: `NoObjectGeneratedError`/`NewNoObjectGeneratedError`, `DefaultSleep`, `DefaultPrice`.
- The ~12 concrete HTTP error structs (`RateLimitError`, `AccessDeniedError`, `ContextLengthError`,
  `AuthenticationError`, `QuotaExceededError`, `ServerError`, `NetworkError`, `NotFoundError`,
  `InvalidRequestError`, `UnknownHTTPError`, …): constructed by providers, consumed **only** through the
  `Error` interface + `Classify`/`ErrorClass`. Unexport so they live behind the interface
  (`NetworkError` has 0 module uses at all). Intended usage becomes obvious: *classify, don't type-assert.*
- `StreamGenerate`/`StreamGenerateObject` + `StreamResult`/`StreamObjectResult`/`StepResult`: referenced
  only inside `llm`. **Disagreement (minor):** Panel-A says "unexport with their tests"; Panel-C says
  "unexport OR formalize as a documented streaming entry point + add a black-box test." **Resolution:**
  unexport unless a near-term caller needs streaming generation; `Client.Stream` already covers the
  streaming need. YAGNI — don't keep an undocumented half-public API.
- Generic utilities that don't belong in a curated LLM client namespace: `DataURI`, `ExpandTilde`,
  `InferMimeTypeFromPath`, `IsImageFile`, `IsLocalPath` → move to `internal/mediautil`
  (used by `llm` + providers). (`IntFromAny`/`ReasoningBudget`/`ValidateToolName` stay — they are SPI.)

**New for `llm`:** a `doc.go` (package overview: the Request→Response/Stream flow, the
`Client`+`ProviderAdapter`-Register driver model, an explicit "Adapter SPI" section), and an
`example_test.go` (`package llm_test`): `Example` (one `Generate`), `ExampleGenerate_tools`, `ExampleClient`,
`ExampleStream`.

**`llm` error-contract fixes (also Section 3.E):** give the error constructors a `cause` and populate it
(or delete the dead `cause`/`Unwrap` machinery); wrap the context cause in `WrapContextError`; use `%w`
for **both** arms of `fallbackToChatCompletions`. Document whether `errors.Is/As` traversal is supported.

### 2.2 `agent` — proposed public API

A small caller API over a flat core, with **one public subpackage** (`agent/events`) carved out now and
deeper subpackages deferred behind the seam/test work (per the in-flight PRI-1947 packaging verdict).

**Keep exported — the genuine driver surface (apps reference ~46 top-level decls; the consumed set is the spec):**
- Construction & restore: `NewSession`, `RestoreSessionFromMeta`, `SaveSessionMeta`/`LoadSessionMeta`/
  `ListSessionMetas`, `ForkSession`, `SessionConfig` (slimmed — see below), `ProviderProfile` + the 6
  `New*Profile` constructors + `ResolveProfileFromConfig` (after §2.0).
- Execution environment: `ExecutionEnvironment`, `LocalExecutionEnvironment`, `NewLocalExecutionEnvironment`,
  `ImageAttachment`.
- Live `Session` methods apps call: `Events`, `ProcessInput`, `Enqueue*`, `Steer*`, `DrainAsSteer*`,
  `SetModel`, `SetReasoningEffort`, `SetTimeout`, `Close`, `ID`, `Snapshot`, `Meta`, `State`, `Tasks`,
  `TranscriptPath`, `DetailedStatus`, `RegisterTool`.
- Persistence/transcript schema (serf-hub's entire dependency on `agent`): `SessionMeta`, `EnvironmentInfo`,
  `Turn`/`TurnKind`/`NewTurn`, `TranscriptHeader`/`TranscriptEntry`/`TranscriptWriter`/`NewTranscriptWriter`/
  `ReadTranscript*`, `Task`/`TaskType`/`TaskStatus`, `MCPServerConfig`, `RuntimeDir`.
- The **event contract** — the dominant legitimate surface (`SessionEvent` ~130 refs, the `Event*` consts
  ~157 refs): `SessionEvent`, `EventKind`, the 28 `Event*` constants, the ~26 `*Data` payload structs.
  **→ moves to `agent/events` (below); re-exported or imported by consumers via that package.**

**`agent/events` — the one public subpackage to carve now (all three panels concur it is the clean leaf-cluster).**
- Contents: `SessionEvent`, `EventKind`, `Event*` consts, `*Data` payloads.
- Why now and not deferred: `SessionEvent.Data` is already `any` (`agent/events.go:46`), so the extraction
  is non-breaking to the core type; the cluster carries **no back-edge into `Session`**; `server/bridge.go`
  and serf-hub consume it without ever constructing a `Session`. It carves ~70 symbols (nearly half the
  used surface) into a documented, independently-testable, runtime-free package.
- **Fix while moving:** replace `Data any` with a sealed payload interface
  (`EventData interface { eventKind() EventKind }`, implemented by each `*Data`) or a generic envelope, so
  the Kind↔payload pairing is compile-checked and the `DataMap()` marshal/unmarshal round-trip
  (`agent/events.go:50`) can be retired. `AssistantTextEndData.Usage` becomes `llm.Usage` (it always is one).

**Unexport — over-exposure with zero app refs AND no caller need (verified empty `grep`):**
- 18 zero-everything types: `ATIFAgent`, `CompactionMeta`, `ContextMetrics`, `EvalMetrics`, `HookResult`,
  `PluginInfo`, `PluginManifest`, `PreToolUseResult`, `ProbeResult`, `SessionNameResult`, `SteeringEntry`,
  `StopResult`, `SubAgentStatus`, `ToolInfo`, `ToolMiddleware`, `TranscriptData`, `EnvVarPolicy`,
  `PromptSource`. (Zero-risk; can land immediately — they are internal-only.)
- The prompt-data/tool-mapping internals: `AgentEntry`, `AgentTaskEntry`, `PromptData`, `SectionResolver`,
  `SectionSource`, `MapClaudeToolName`, `MapSerfToolNameToClaude`, `ApplyPatch`, `EstimateTokens`.

**Slim `SessionConfig` (severity high).** It carries test-only and spawn-internal fields no app sets:
`ContextStrategyOverride` ("For testing"), `CompactionThresholdScale` ("for evaluation testing"),
`ParentToolCallID`, `SubagentTask`, `RolePromptOverride`, `ActivatedSkillBodies`, `AllowedToolNames`,
`DeniedToolNames`, `SharedTaskStore`, `LLMSleep`. Move the spawn-internal fields into an unexported struct
`spawnAgent` populates; replace the two "For testing" fields with an internal test option
(`newSessionForTest(...)`). A world-class public config type carries no "For testing" fields.
(`ParentSessionID`/`Depth` stay — serf-hub spawn sets them.)

**Profile-path consolidation (severity high).** Two parallel provider→profile switches exist —
`agent.ResolveProfileFromConfig` (`agent/resolve.go`) and `cmdutil.SelectProfile`
(`cmdutil/cmdutil.go`) — both dispatching the same 6 `New*Profile` constructors (each non-OpenAI
constructor has exactly one call site, all in `cmdutil`). Make `ResolveProfileFromConfig` the single public
entry, delete the duplicate switch, and unexport 5 of the 6 `New*Profile` constructors (keep
`NewOpenAIProfile` only if the OAuth launch path needs a direct constructor).

**Naming polish:** standardize `SubAgent*` → `Subagent*` (the event surface already commits to `Subagent`,
and apps depend on that spelling); `SubAgentResult`/`SubAgentStatus` are app-invisible so the rename is
risk-free.

**New for `agent`:** a `doc.go` (package overview: `NewSession → ProcessInput → Events` arc, the
`ExecutionEnvironment`/tool-registration extension points, the event-stream contract pointer to
`agent/events`), an `agent/README.md` mirroring `llm/README.md`, and an `example_test.go`
(`package agent_test`): `Example` = `NewSession` + `ProcessInput` + range `Events`.

### 2.3 Deferred subpackages (reconciled with in-flight PRI-1940/PRI-1947)

The three panels split on *how aggressively* to extract subpackages. The in-flight specs already settled
this with a `go/types` analysis, and the synthesis adopts their verdict:

- **Do NOT extract `agent/internal/{contextstrategy,tools,subagents}` now.** A `go/types` reference
  analysis (`docs/superpowers/specs/2026-05-30-agent-package-modularization.md`) found 13 of 14
  concern-clusters form one SCC with `Session`, held by four back-cycles. PRI-1947 cuts those cycles with
  in-package seams (`StrategyHost` interface, `toolDeps`/`readGuard`/`taskGuard` structs, `subagentManager`)
  and **stops** — a subpackage cannot import `agent` (for `RegisteredTool`/`Turn`/`NewSession`) while
  `agent` imports it back, and the split would be unobservable to any external caller (zero external refs
  to all seam types). Forcing it first requires sinking the foundational types into a base package (large
  blast radius) for no consumer-visible gain.
- **The strategy/tool/MCP/plugin/eval clusters therefore *unexport in place* now** (their types have zero
  app refs: `ContextManager`, `ContextStrategy`, `StrategyHost`, the 8 `*Strategy` types + their `New*`
  constructors, `ToolRegistry`, `RegisteredTool`, `MCPManager`/`NewMCPManager`, `HookRunner`,
  `LoadedPlugin`, `EvalCollector`/`RunRetentionProbes`, the 8 `ATIF*` types + `ConvertToATIF`). They become
  subpackages **only after** the PRI-1947 seams land AND the test-coupling migration (§2.4) frees them.
  `ProviderProfile.NewToolRegistry() *ToolRegistry` (`agent/profile.go:73`) leaks the to-be-internal
  `ToolRegistry` through the public interface — remove that method from the interface or have it return an
  opaque handle, independent of the subpackage decision.
- **`agent/events` is the exception** — public (not `internal/`), carved now, because it is the one clean
  public leaf-cluster with no back-edge (§2.2).

### 2.4 Test-coupling migration plan — NO coverage loss

This is the single biggest piece of work and the prerequisite for every unexport and every future split.

**Diagnosis (verified):** 85/85 `agent` test files are `package agent`; 0 are `agent_test`. 21/22 `llm`
test files are `package llm` (only `llm/integration_smoke_test.go` is black-box). 167 of 363 unexported
`agent` funcs are reached by tests — **but the breakdown makes this tractable, not scary:**
- **91** of those are **test helpers defined in `_test.go`** (mock-LLM scaffolding: `finalResponse`,
  `toolCallResponse`, `communicateCall`, `testOpenAIProfileWithContextWindow`, `makePluginDir`,
  `initGitRepo`). They move with their test package **for free** — no API change.
- **76** are **production internals**, overwhelmingly single-test pure helpers (`shellEscape`,
  `truncateChars`, `parseExitCode`, `webFetchCacheKey`, `sanitizeToolName`, `safeCutoff`,
  `looksLikeQuestion`).
- Agent tests already build sessions through the **public** surface today: `llm.NewClient()` +
  `c.Register(fakeAdapter)` (fake implements public `llm.ProviderAdapter`) + `NewSession(c,
  NewOpenAIProfile(...), NewLocalExecutionEnvironment(...), SessionConfig{...})` + `sess.ProcessInput(...)`.
  `agent/session_test.go:153` is a working example that would compile unchanged as `package agent_test`.

**Migration in three buckets (coverage preserved at every step):**
1. **Move behavioral tests to black-box.** The ~35 files that drive a whole session through the public API
   become `package agent_test` (and the behavioral `llm` tests become `package llm_test`) — zero logic
   change. Publish the shared mock adapters into a small helper package **`agent/agenttest`** (or expose one
   small, deliberate test seam such as a `Session` responder-injection method — *to be designed in the
   ticket, not invented here*). This *increases* public-contract coverage.
2. **Keep genuine internal-unit tests white-box, riding with their code.** The 76 production-internal unit
   tests stay `package agent`/`package llm` and follow their function into whichever subpackage it lands in
   later (tested there through that subpackage's own black-box tests). Internal unit coverage is preserved.
3. **The 91 test helpers move with their test package** — no action beyond bucket 1/2 placement.

**Ordering rule (hard):** the black-box migration (bucket 1) and the in-place unexports (§2.2) come
**before** any subpackage extraction. Tests must stop coupling to internals so internals can move. Start
with the lowest-coupling files (`tool_registry_test.go` ≈ 6 internal call sites) to validate the pattern.
**No test is ever deleted to make a symbol unexportable** (per CLAUDE.md); if a test genuinely needs an
internal, it stays white-box in bucket 2.

---

## 3. Findings by dimension (consolidated, deduped, severity-ranked)

Severity: **critical** > **high** > **medium** > **low**. Evidence verified at HEAD.

### 3.A Correctness & data races

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| A1 | **PRI-1939 residual: `ProcessInput` `emit()` races `close(s.events)`; `recover()` masks send-on-closed-channel UB.** `emit()` does a best-effort send guarded only by `defer func(){ _ = recover() }()` — recover() is load-bearing control flow. | critical | `agent/session_events.go:38-43`; `close(s.events)` at `agent/session_lifecycle.go:217` joins only `toolEventsWG`/`sendersWG`, not the caller-owned ProcessInput goroutine; race is concrete in `cmd/serf/serve.go:366` (input loop) vs `:419-423` (shutdown → `getSession().Close()`). | **Never close the events channel from the producer.** Add `done chan struct{}` closed once in `closeOnce.Do()` (replacing `close(s.events)`); `emit()` → `select { case s.events<-ev: case <-s.done: default: }`; **delete the recover()**. Add `Done() <-chan struct{}`. Convert the 3 range-consumers to select on `Done()`: `server/bridge.go`, `cmd/serf/run.go` (`drainEventsVerbose`/`Human`), `cmd/serfeval/main.go`. Keep the WaitGroups for in-flight emits. | M |
| A2 | **Unsynchronized write to `s.cfg.ReasoningEffort` in `buildModelRequest`** races the locked full-`s.cfg` copies in `Meta()`/`Snapshot()`. | high/med | Write with no lock at `agent/session_lifecycle.go:1200` (called from the ProcessInput loop at `:587`); every other writer holds `s.mu` (`SetReasoningEffort`, `stuckEscalation`); readers copy `s.cfg` under `s.mu` in `Meta()`/`Snapshot()`. | Make `buildModelRequest` a pure read: resolve `effort` into a local (`req.ReasoningEffort`) without writing back to `s.cfg`. If the override must persist, do it under `s.mu` at a defined point. | S |
| A3 | **`DetailedStatus()` reads ~7 session fields without `s.mu`**, concurrent with `ProcessInput`; `SetModel`→`reapplyProviderSpecificTools` mutates `s.reg` live under `s.mu`, so map iteration can race a map write. | medium | `agent/status.go:47-114` (reads `s.mcpMgr`,`s.reg`,`s.coreToolNames`,`s.skills`,`s.plugins`,`s.hookRunner`,`s.pluginAgents`); wired at `cmd/serf/serve.go:301-303`. | Snapshot the needed refs under a short `s.mu` critical section, then build the struct from locals; or enforce/​document those fields immutable-after-init and route the one live mutator through a concurrency-safe registry. | S |
| A4 | **Hot loop reads `s.profile`/`cachedSystemPrompt`/`cachedToolDefs`/`s.reg` lock-free** while `SetModel` writes them under `s.mu` from another goroutine. | high | Reads at `agent/session_lifecycle.go:536,584,587,860`; writes under `s.mu` at `agent/session.go:252-260`; `ProcessInput` runs in its own goroutine (`serve.go:339,366`) while `SetModelFunc`/`SetReasoningEffort` fire from the request handler (`serve.go:215,299`). | Snapshot profile/sys/toolDefs under one `s.mu` at the top of each round (mirror the history copy at `:544-546`). Strategic fix is the actor core (C5). | S |
| A5 | **Second `recover()`-as-control-flow:** `chanStream.Send` swallows send-on-closed-channel panic. Lower priority — gated by `done`/`closing` selects first, and it is llm-stream-internal (app-internal plumbing). | low | `llm/chan_stream.go:55`; `CloseSend` closes `s.events` at `:44-49`. | Apply the same `Done()` discipline (never close `s.events`; close only `s.done`; `Send` selects on `s.done`) so the recover can be removed. Or accept as documented best-effort v1. | S |
| A6 | **No `-race` regression test covers the ProcessInput-emit-vs-Close path** (only the detached subagent-emit path is tested). | low | `agent/session_close_race_test.go:17-45` tests only the subagent emitter. | Add a looped `-race` test: goroutine A runs `ProcessInput` (fake adapter emitting), goroutine B calls `Close()` after jitter. It flags the race under the current recover()-masked design and passes after the A1 redesign — executable proof recover() is no longer needed. | S |

### 3.B God-files & god-functions

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| B1 | **`processOneInput` is a ~757-line god-function** — the entire turn loop in one body (next top-level func `buildModelRequest` is at line 1165). | critical | `agent/session_lifecycle.go:404-1160`; commented `--- Phase: X ---` markers are the seams. | Function-level decomposition (file is cohesive — do NOT split it): extract `handleContentlessResponse` (the empty/bare-text retry block ~721-842), `execToolBatch` (parallel block + `flushReadBatch` ~859-953), `runModelRound` (~533-603). Target loop body < ~150 lines. | L |
| B2 | **Four provider `Stream` methods each wrap a 180-360 line inline SSE-decode goroutine** (duplicated structural shape). | high | anthropic `adapter.go:438-799`; openai `responses.go:202-488`; openaicompat `adapter.go:298-544`; google `adapter.go:404-586`. Bloat is in the unexported goroutine — public surface unaffected. | Per-provider: keep `Stream` as HTTP-setup + ChanStream entry; extract the goroutine into `decodeStream(resp,s)` (or a per-adapter `streamDecoder` struct with one method per SSE event type). **No cross-provider shared decoder** (wire formats differ). anthropic L, openai/openaicompat M, google S. | L |
| B3 | **anthropic/openaicompat/google `adapter.go` are monolithic single-file adapters** (1341 / 1352 / 1029 lines) bundling client + request + wire-conversion + stream + response structs + ListModels. openai is already split (adapter/chatcompletions/responses/models) — the template. | high | `anthropic/adapter.go`, `openaicompat/adapter.go`, `google/adapter.go`. | Behavior-preserving FILE split mirroring openai: `models.go` (ListModels), `convert.go` / `request.go`+`response.go` (encoders/decoders + wire structs), `quirks.go` (openaicompat), `rescue.go` (openaicompat `rescueClaudeXMLArgs`). Pure cut/paste within one package. anthropic S, openaicompat S, google S. | S (×3) |
| B4 | **`context_manager.go` (1344 lines) mixes compaction with ~280 lines of checkpoint-markdown codec**; `checkpoint()` straddles both (251 lines). | high | `agent/context_manager.go`: codec helpers at `:988-1280`; `checkpoint()` at `:548-798`. | Move the markdown render/extract/parse helpers + their 2 value types into `checkpoint_format.go` (S); function-decompose `checkpoint()` into `collectCheckpointData(...)` + format (M). | M |
| B5 | **`toResponsesInput` is a 224-line conversion** with three levels of nested per-role/per-content-kind switches. | medium | `openai/responses.go:660-883`. | Extract `assistantItemsFromMessage`, `userItemsFromMessage`, `toolItemsFromMessage`; the parent becomes instructions-pass + role dispatch. | M |
| B6 | **`profile.go` (1518) / `session_tools.go` (1552) bundle the abstraction with ~450/~660 lines of `defXxx`/`registerXxxTools` factories.** | medium | `agent/profile.go:1065-1518` (22 `defXxx` factories); `agent/session_tools.go:587-1382` (registration). | FILE split: `tool_definitions.go` (the `defXxx` block), `session_tool_registry.go` (the `registerXxxTools` + dep structs). Pure relocation, same package. (Coordinate with the §2 unexports.) | S (×2) |
| B7 | **App god-files (internal-cleanliness bar):** `newHubAppServer` 485-line constructor with 3 inline closures + 38 handler registrations; `app_rpc.go` 2002 lines spanning 6+ responsibilities. | high/med | `cmd/serf-hub/app_rpc.go:81-565` (constructor), whole file 2002 lines. | Lift `startRelay`/`cleanupRelay`/`startRelayForThread` into a `hubRelayManager`; group registrations into `register*Handlers(...)`. Split `app_rpc.go` by concern (`app_threadlist.go`, `app_transcripts.go`, `app_launch_models.go`) following the existing `app_*.go` convention. | M (×2) |
| — | `hub_model.go` (was 4563) — **already done** (PRI-1956: now 232 lines). | — | git log `3b5a1af0`…; `wc -l` = 232. | None — finish/verify the split lands in Phase 0. | — |

### 3.C Concurrency-model clarity

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| C1 | (= A4) Hot-loop lock-free reads vs `SetModel` writes. | high | see A4 | see A4 | S |
| C2 | (= A2) `buildModelRequest` lock-free write. | high | see A2 | see A2 | S |
| C3 | **`s.mu` is a coarse field-guard over ~25 unrelated fields** (191 lock sites), forcing the lock-free shortcuts and offering no doc of what it protects. | medium | `agent/session.go:28`; 191 `s.mu` sites; no field-grouping, no doc comment on `mu`. | Minimum: document on the `mu` field exactly which fields it guards and that profile/cache reads in the loop MUST hold it. Better: split into intent-named locks or adopt the actor core (C5). | M |
| C4 | **No documented concurrency model** for a library-to-be: which methods are safe concurrently, is `ProcessInput` re-entrant, when does `Events()` close, what is the lock order. | high | No `doc.go`; grep for `concurrency|thread-safe|goroutine-safe|actor` finds only unrelated hits. | Write a Concurrency section (in the `doc.go` from §2): one `ProcessInput` at a time; the safe concurrent control surface (`Steer`/`Enqueue*`/`Close`/`State`/`Snapshot`/`QueueDepth`…); that `SetModel`/`SetReasoningEffort`/`SetTimeout` are safe only after A2/A4 fixed; `Events()`/`Done()` lifecycle; the lock order (`responseSideEffectsMu > s.mu`; `queueEventsMu > s.mu`; `subagentManager.mu > sub.mu`). | S |
| C5 | **Adopt a single-goroutine-owns-state actor core** so `s.mu`/`responseSideEffectsMu`/`queueEventsMu`/`readFilesMu` largely disappear and the A2/A4 races become structurally impossible. | medium | The loop is already effectively single-threaded over history (`agent/session_lifecycle.go:1016-1017`); the 4 mutexes exist mainly to let external control methods poke loop-owned state. | Funnel external mutations into a command mailbox drained by the loop; serve `Snapshot`/`State` from an `atomic.Pointer` snapshot the loop publishes per round; `Close` = send-stop + join. Keep the two WaitGroups (parallel tool emits, detached children). **Stage it** (after C1/C2 land): cache-reads-safe → command mailbox → collapse side-effect locks. Higher risk; depends on PRI-1947 `subagentManager`. | XL |
| C6 | Two-level `subagentManager.mu`/`sub.mu` + parent `sendersWG` is more machinery than the ≤1-depth child count warrants; close paths are idempotent but undocumented. | low | `subagent_manager.go:16`, `subagents.go:50,439-473`, `session_lifecycle.go:154,184-186`. | After the actor core, the manager map can be loop-owned. Short term, document the idempotent close-after-drain. Keep `sendersWG`. | M |

### 3.D Documentation (godoc / stdlib grade)

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| D1 | **Neither library has a package doc comment / `doc.go`** — pkg.go.dev opens with no overview. | high | `grep '^// Package agent' agent/*.go` and `'^// Package llm' llm/*.go` → NONE. Newer providers (openrouter/glm/kimi/minimax/ollama/openaicompat) DO have package docs; the core packages and anthropic/google/openai do not. | Add `doc.go` to each (purpose, overview paragraph, minimal end-to-end snippet; name the SPI tier for `llm`). Backfill anthropic/google/openai package comments. (Covered by §2.) | M |
| D2 | **`llm` primary entry points + central wire types undocumented (27% coverage, 96/362)** — the gap is the API users hit first (`Generate`, `GenerateObject`, `Request`, `Response`, `Message`, `Client`, `ProviderAdapter`, the message constructors, all 13 error types). | high | `llm/generate.go:219`, `generate_object.go:17`, `types.go:34,196,393`, `client.go:10,15,23`, etc. Quality where docs exist is excellent (`Usage` doc at `types.go:291-319`). | Document in priority order: `Generate`/`GenerateObject` → the core types → the enum const blocks. Target 100% of types + top-level funcs + constructors. | L |
| D3 | **`agent` `Session`, `NewSession`, and the entire event vocabulary undocumented** (68% coverage, 238/351) — the on-ramp is bare. | high | `agent/session.go:14` (`Session`), `session_init.go:44` (`NewSession`), `events.go:10-38` (`EventKind` + 27 consts + `SessionEvent` + ~24 `*Data`). | Document `Session` + `NewSession` first; group-document the event system (moves to `agent/events`, §2.2); then `ProviderProfile`/`SessionConfig`/`ExecutionEnvironment` + constructors. | L |
| D4 | **Zero runnable Example tests in either library** (none in the whole repo). | high | `grep -rn 'func Example'` agent/ llm/ → none. | Add `example_test.go` (`package llm_test`/`agent_test`) with compile-checked Examples (per §2.1/§2.2). | L |
| D5 | The three oldest/most-important providers (anthropic 25%, google 25%, openai 33%) are the worst-documented; newer providers are ~100% and keep their adapter type **unexported**. | medium | per-package `go doc` coverage; `openrouter/adapter.go:17` `type adapter struct` (unexported). | Bring the trio to the newer standard (package comment + `Adapter`/`NewFromEnv`/`Config` docs). Consider — as a question for Jesse — unexporting their `Adapter` like the newer providers to shrink surface. | S |
| D6 | **No linter enforces doc comments**, so coverage will regress; existing prose is convention-compliant (1 first-word violation in all of `llm`, 0 in `agent`). | medium | `.golangci.yml` = `default: standard` + `gocritic` only; no revive/godot/stylecheck. `ValidateToolName`'s comment opens "Tool names…". | After backfill, enable revive's exported-comment rule or staticcheck ST1000/ST1020/ST1021 **scoped to `agent/…` and `llm/…`**. Fix the `ValidateToolName` comment. (Part of the standing gate, §5.) | M |

### 3.E Error handling

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| E1 | **`llm` error types declare `cause` + `Unwrap()` but no production constructor ever sets `cause`** — the whole `errors.Is/As` Unwrap chain is dead; two tests pass only because they hand-populate `cause` (a "test of mocked behavior" smell). | high | `llm/errors.go:50,69`, `sdk_errors.go:15,37`; `cause:` assigned only in `errors_test.go:184,195,316`; `ErrorFromHTTPStatus`/the stream/abort/timeout constructors build the base without `cause`. | Decide the contract: give constructors a `cause` and populate it (recommended), OR delete `cause`+`Unwrap()`+the two tests. Document whether `errors.Is/As` traversal is supported. | M |
| E2 | **`WrapContextError` discards the `context.Canceled`/`DeadlineExceeded` cause** (string-only), so `errors.Is(abortErr, context.Canceled)` is FALSE — forcing two agent-side workarounds. | high | `llm/context_errors.go:11-22` (returns `NewAbortError(err.Error())`); workarounds at `agent/session_lifecycle.go:1363-1369`, `agent/session_queue.go:304-305`. | Add `cause`-carrying constructor variants and pass `err` through; the agent helpers simplify to a single `errors.Is`. (Companion to E1.) | S |
| E3 | **Streaming adapters discard the `llm.ParseSSE` error**; 3 of 5 (anthropic, google, openaicompat) then surface nothing for a mid-stream read failure/timeout — the specific cause + any Retry-After are lost (consumer backstop flattens to a generic "stream ended without finish event"). chatcompletions does it correctly, proving inconsistency. | high | `_ = llm.ParseSSE(...)` at anthropic `:495`, google `:434`, openaicompat `:328`, openai/responses `:232`, openai/chatcompletions `:96`; correct epilogue only in chatcompletions `:254-262`; backstop at `stream_generate.go:221-226`. | Capture `parseErr := llm.ParseSSE(...)` in every adapter; in the `if !finished` block emit a `StreamEventError` carrying it (`WrapContextError` for ctx/timeout, else `NewStreamError`), matching chatcompletions. | M |
| E4 | **`subagents.go` silently swallows `PopulateFromTemplates` error** under a comment claiming it logs. | medium | `agent/subagents.go:296-299` (`// Log but don't fail the spawn.` then `_ = err`). | Emit `EventWarning` with the err (stay non-fatal but observable). | S |
| E5 | **`openai isUnconfigured` matches errors by string**, including a sentinel already `%w`-wrapped and reachable via `errors.Is`. | medium | `openai/adapter.go:528-538`; literal produced at `:140`; auth error wrapped with `%w` at `:82`. | Introduce a package sentinel `errNoCredentials`, return it at `:140`, match with `errors.Is`; replace the substring check with `errors.Is(err, authopenai.ErrAuthNotFound)`. | S |
| E6 | **`fallbackToChatCompletions` wraps one arm with `%w` and the other with `%v`**, hiding the chat-completions failure from `errors.As`/`Classify` (a 429 + Retry-After on the cc arm is invisible to the classifier). | medium | `openai/adapter.go:486-490`; classifier relies on `errors.As(err,&Error)` (`classify.go`, `retry_util.go`). | Use `%w` for **both** arms (Go 1.20+ multi-`%w`). | S |
| E7 | **Public error contract under-specified.** `agent`'s control-flow sentinels (`errBareTextWithoutResultTool`, `errEmptyResponseExhausted`, `errStreamUnavailable`) are unexported, so an external consumer can't distinguish terminal outcomes; `llm` has no documented list of matchable errors. | medium | `agent/session_lifecycle.go:20-47`; `llm` error taxonomy is exported but undocumented as a contract. | `llm`: doc the matchable types + `errors.Is/As` support. `agent`: export the sentinels that are legitimate terminal outcomes callers may branch on, or document the surface as intentionally opaque. Keep purely-internal signals unexported. | M |
| E8 | Direct type assertion `err.(*exec.ExitError)` bypasses Unwrap. | low | `agent/plugin_hooks.go:256`. | Use `errors.As`. | S |
| E9 | `APILogger` drops `rawFile` Sync error while capturing the main file's. | low | `llm/apilog.go:191` vs `:202`. | Capture symmetrically or add a one-line "intentionally ignored" comment. | S |

Note: the ~5 panics and the `recover()` at `tool_registry.go:524` (defensive around a third-party jsonschema
lib) and `session_lifecycle.go:411,880` (re-panic after cleanup; bounded panic-as-signal across a goroutine
join) are **legitimate** — not recover-as-flow abuse. The two recover-as-flow sites are A1 and A5 only.

### 3.F Provider-adapter duplication (`llm/providers/`)

The hard shared machinery is already centralized at the `llm` level (retry, error classification, SSE
parsing, ChanStream, timeout, rate-limit, context-error wrapping, media helpers, provider-stamping) — the
right altitude. The remaining duplication sits in the thin layer between raw HTTP/SSE and those helpers.

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| F1 | **`openai/chatcompletions.go` is a 551-line near-clone of the openaicompat Chat Completions adapter** (Responses-API fallback path); `toChatResponseFormat` is byte-identical, the streaming usage-parse block is byte-identical. Two implementations of one wire format → every fix applied twice, silent drift. | high | `openai/chatcompletions.go:20-267,334-405,531-551,180-208` vs `openaicompat/adapter.go:298-541,660-745,1082-1102,424-455`. | Extract the openaicompat Chat-Completions request-codec + streaming-loop into an internal package (`llm/providers/internal/openaichat`) exposing `BuildBody`/`ParseResponse`/`StreamDecoder`; both callers use it. The OpenAI fallback shrinks to ~30 lines. | L |
| F2 | **The "subtract cached_tokens" usage parser is duplicated ~4×** (3 byte-identical, 1 renamed for Responses). Each re-encodes the `llm.Usage` invariant. | medium | openaicompat `:424-455` & `:1239-1267`; openai/chatcompletions `:180-208`; openai/adapter `:583-613`; invariant doc at `llm/types.go:300-319`. | One helper `llm.ParseOpenAIUsage(raw, fieldNames)` (or two thin wrappers); all four delegate. Highest reward-to-effort dedup in the package. | S |
| F3 | **Every direct adapter re-implements the same request/non-2xx/streaming skeleton** — the `ParseRetryAfter`+`ErrorFromHTTPStatus` triad appears 9×; the TeeReader RawBody prologue + `if !finished{…ctxErr…}` epilogue is copy-pasted in all 5 streaming files. | medium | anthropic `:376,429`; google `:319,394`; openai `:320`, `responses.go:192`, `chatcompletions.go:60`; openaicompat `:288`; TeeReader/epilogue in all 5 stream files. | Introduce `llm/providers/internal/transport`: `Executor.Do(...)` (connect-timeout client + marshal + UseNumber decode + non-2xx→`ErrorFromHTTPStatus`) and a `StreamRunner` (ChanStream + TeeReader/RawBody + ParseSSE + the `!finished` epilogue), per-event callback injected. Each adapter keeps only its wire-format handler. (Pairs with E3 — capture `parseErr` once, in the runner.) | L |
| F4 | Header/auth setup is per-adapter copy-paste with the same "DefaultHeaders first" rule (openaicompat 3× inline, google 3× inline). | low | openaicompat `:170-175,268-274,1311-1318`; google `:118-120,302-306,377-381`; anthropic centralizes (`:329-339`). | Transport `Executor` accepts a `HeaderFunc`; shared `llm.SetBearerAuth`/`SetDefaultHeaders`. At minimum give openaicompat/google one private `setHeaders`. | S |
| F5 | **Chat-completions body builders diverged between the two OpenAI implementations** — openaicompat handles quirks/reasoning_details; the openai copy handles web_search + tool-result-image rejection. Same model can produce different wire bodies. | medium | openaicompat `:548-658` vs openai/chatcompletions `:271-331,407-419`. | After F1, a single `BuildChatCompletionsBody(req, BuildOpts{Quirks, WebSearch, AllowToolResultImages, ReasoningStyle})` so the callers cannot diverge. | M |
| F6 | Thin wrappers (glm/kimi/openrouter/ollama/minimax/openrouter_anthropic) duplicate identical Name/Complete/Stream/ListModels + init() forwarding (~80×6 ≈ 480 lines expressing ~4 fields of difference). | low | glm/kimi/openrouter `adapter.go:22-27,62-89`. | Collapse the openaicompat-backed wrappers into one table-driven registration helper; the two anthropic-backed into a second. App-internal — an internal helper is appropriate. | M |
| F7 | ollama hand-rolls a provider-rewrite `Stream` wrapper that already exists in the Client (`providerStampStream`); other wrappers don't rewrite at all → inconsistent behavior. | low | `ollama/adapter.go:81-142` vs `llm/client.go:286-329`. | Drop `ollama.rewriteStream` and rely on the Client's `providerStampStream` + `RewriteErrorProvider` (already called at `:59,65`); confirm the inner stamp doesn't leak before the Client wraps it. | S |

### 3.G Naming & API hygiene

Mechanical quality is near-exemplary: zero `agent.AgentX`/`llm.LLMFoo` stutter, correct initialisms,
consistent receivers, clean `go vet`. The headline "stutter-free public API" goal is essentially met.
Remaining work:

| # | Finding | Sev | Evidence | Recommendation | Size |
| --- | --- | --- | --- | --- | --- |
| G1 | **TUI `serf:naming-ignore` pragma reflects a real structural smell** — a wire-format struct hand-rolled in the app instead of typed in `appwire`. | medium | `cmd/serf-tui/hub_notifications.go:85-91`; sibling `*Params` types exist at `internal/appwire/types.go:625,636`. | Add `appwire.TurnCompletedParams` and unmarshal into it; the pragma disappears and the wire contract lives with the protocol. | S |
| G2 | **3 `mcpServers` pragmas in `agent`** (upstream `.mcp.json` camelCase) could collapse to one path-based linter carve-out. | low | `agent/mcp_config.go:24-25`, `agent/plugin.go:27-28,143-144`; linter already has `isProvidersPath`/`isAppwirePath` (`cmd/serf-namingcheck/main.go:105,124`). | Whitelist the upstream key `mcpServers` in `checkJSONTag`, or move the 3 structs into `mcp_wire.go` + an `isMCPWirePath` carve-out. Removes all 3 pragmas. | S |
| G3 | **lint-naming gate does NOT enforce Go identifier hygiene** — only wire-tag casing; stutter/initialism/receiver correctness is unguarded, so the excellent state is held by manual discipline. | medium | `Makefile:38-39`, `.github/workflows/ci.yml:28-29`; the tool checks struct tags only (`main.go:239,323,344`); `.golangci.yml` has no revive/stylecheck. | Enable `stylecheck` (ST1003/ST1016) in `.golangci.yml`, scoped to `agent/…`+`llm/…`, locking in the stutter-free state. (Folds into the standing gate §5.) | M |
| G4 | **`ATIF*` cluster (8 types + `ConvertToATIF`) exported with zero external consumers** (`ExportATIF` has an internal CLI caller). | low | `agent/atif.go:15-82,212`; verified zero external refs. | Unexport the cluster (tests are in-package, no public-test loss), keeping `ExportATIF` reachable internally; or, if ATIF is a real product capability, move to an `agent/atif` subpackage with docs. (Covered by §2.2.) | M |
| G5 | `SubAgent*` vs `Subagent*` casing inconsistency in exported names. | low | `agent/subagents.go:18`, `status.go:28`, event consts. | Standardize on `Subagent*` (event surface + apps already use it); `SubAgent*` names are app-invisible. (Covered by §2.2.) | S |
| G6 | `Request`/`Response` sibling types use divergent receivers (`req` vs `r`). Each is internally consistent (not ST1016), but two types read side-by-side disagree. | low | `llm` Request `req`, Response `r`. | Cosmetic — pick `req`/`resp`. Very low priority. | S |

### 3.H Package architecture (the three-panel reconciliation — see Section 2)

Production graph is excellent and acyclic (`apps → agent → llm`; `llm/providers/* → llm`; `llm` imports no
provider; the four `Session` back-cycles are being cut by PRI-1947's seams). The problem is **public-surface
size driven by white-box test coupling** plus the `internal/providerconfig` leak. The detailed keep/hide/
move decisions, the two critical blockers (the leak + total white-box coupling), and the genuine panel
disagreements (events-as-public-subpackage — adopted; `StreamGenerate` exported-or-not — unexport; how
aggressively to subpackage — defer per PRI-1947) are all reconciled and decision-ready in **Section 2**.
Headline counts (verified): `agent` exports ~350 top-level symbols, apps use ~46–129 distinct; `llm` exports
~124, apps use ~50 (apps+providers ~115); 85/85 agent tests white-box, 1/22 llm tests black-box.

---

## 4. Prioritized, sized roadmap

One Linear ticket per area. Sizes: **S** < ½ day · **M** ≈ 1 day · **L** 2–3 days · **XL** > 3 days.
Every item is behavior-preserving unless explicitly noted; the existing test suite is the regression harness.

### Phase 0 — Correctness (do first; small, high-value, unblocks the gate)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P0.1 Kill the events race + delete recover()** | A1 (Done-channel teardown, delete `emit` recover, convert 3 consumers, add `Done()`), A6 (the proving `-race` test) | The one residual UB in the codebase; recover() is load-bearing control flow — exactly the anti-pattern the bar forbids. | M | — | Behavior identical except dropped-event semantics stay best-effort; A6 test proves no regression. |
| **P0.2 Fix the unsynchronized session writes/reads** | A2 (`s.cfg.ReasoningEffort` pure-read), A4 (snapshot profile/cache under `s.mu` per round), A3 (`DetailedStatus` snapshot) | Real data races `-race` misses today; cheap, independent of any redesign. | S | — | Pure synchronization; observable behavior unchanged. Add a `SetModel`-concurrent-with-`ProcessInput` `-race` test. |
| **P0.3 Finish + verify the hub_model split** | Confirm PRI-1956 landed; close out | Phase-0 deliverable per brief; already 4563→232 lines. | S | — | Already behavior-preserving relocations. |
| **P0.4 chan_stream recover()** | A5 (Done discipline or documented acceptance) | The second recover-as-flow; small. | S | — | No behavior change if Done-guarded. |

### Phase 1 — Honest error contracts (small, independent, raises library quality)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P1.1 Make `llm` error wrapping real** | E1 (populate `cause` or delete the dead machinery), E2 (`WrapContextError` carries cause), E6 (both arms `%w`), fix the two cause-tests to match | A library that advertises `Unwrap()` it never populates is dishonest; fixing E2 simplifies the agent workarounds. | M | — | `errors.Is/As` *gains* behavior; existing string output preserved. |
| **P1.2 Surface stream-read failures in all adapters** | E3 (capture `parseErr`, emit `StreamEventError`) | 3/5 providers drop the real cause + Retry-After today; consistency with chatcompletions. | M | — | More specific errors surface; happy path unchanged. (Lands cleanly inside the F3 `StreamRunner` if sequenced after it, but doesn't require it.) |
| **P1.3 Small error-hygiene fixes** | E4 (subagent warning), E5 (openai sentinel), E8 (`errors.As`), E9 (apilog symmetry) | Low-risk correctness/clarity. | S | — | Behavior-preserving (E4 adds an observable warning). |

### Phase 2 — Documentation + naming gate (cheap, high pkg.go.dev signal, partially gating)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P2.1 Package docs + examples** | D1, D2, D3, D4, D5, C4 (concurrency doc) | Table-stakes for "Pike demos it"; the concurrency doc also forces the SetModel-safety contract to the surface. | L | P0 (so the concurrency doc tells the truth) | Docs/examples only; examples are compile-checked. |
| **P2.2 Enable godoc + identifier-naming lint (scoped)** | D6, G3, fix `ValidateToolName` comment | Locks in doc coverage and the stutter-free state so neither regresses. | M | P2.1 (backfill first), §2 unexports landing | CI-only; scope to `agent/…`+`llm/…`. |
| **P2.3 Eliminate the naming-ignore pragmas** | G1 (`appwire.TurnCompletedParams`), G2 (mcpServers carve-out), G5 (`Subagent*`), G6 (receivers) | Removes the structural smell (G1) and the manual pragmas. | S | — | App/internal renames; G5 names are app-invisible. |

### Phase 3 — Library boundary + API surface (the decision work; gates the split)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P3.1 Promote config schema out of `internal/`** | §2.0 (`llm/providercfg`, promote `WorkspaceInfo`) | Hard precondition for "importable library"; today two constructors are uncallable externally. | L | — | New public package; existing callers updated to the new import path — no behavior change. **Ratification gate: Jesse confirms standalone-library intent.** |
| **P3.2 Zero-risk unexports** | `agent`: the 18 zero-everything types + prompt-data/tool-mapping internals (§2.2); `llm`: dead middleware/capability symbols + concrete HTTP error structs + utilities→`internal/mediautil` (§2.1) | Immediate surface reduction with no caller and (mostly) no test dependency. | M | the few that tests touch wait for P4 | Internal-only; `go build ./...` confirms. |
| **P3.3 Slim `SessionConfig` + consolidate profile path** | §2.2 SessionConfig split + test-option; §2.2 single `ResolveProfileFromConfig`, delete `cmdutil.SelectProfile` duplicate, unexport 5 `New*Profile` | Removes "For testing" fields from public config and the drift-prone duplicate switch. | M | P3.1 (`ResolveProfileFromConfig` callable) | Behavior identical; spawn-internal fields move to an internal struct. |
| **P3.4 Remove the `ToolRegistry` leak from `ProviderProfile`** | `NewToolRegistry()` off the interface / opaque return (§2.3) | Stops a to-be-internal type leaking through the central public interface. | S | — | Internal callers adjusted; no external caller exists. |

### Phase 4 — Test-coupling migration (the big enabler; must precede any subpackage extraction)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P4.1 Black-box behavioral tests** | §2.4 bucket 1: move ~35 agent files → `agent_test`, the behavioral llm files → `llm_test`; publish fakes to `agent/agenttest` (or a designed `Session` test seam) | Tests must stop coupling to internals so internals can move; *raises* public-contract coverage. | XL | — | **No coverage loss** — behavioral coverage moves to the public surface; internal-unit tests stay white-box (bucket 2). No test deleted. |

### Phase 5 — Decomposition + dedup (internal maintainability; some gated by Phase 4)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P5.1 Decompose `processOneInput`** | B1 (extract 3 phase functions) | The one catastrophic god-function. | L | — (file-internal; safer after P0) | Pure function extraction; suite is the harness. |
| **P5.2 Provider adapter file-splits** | B3 (anthropic/openaicompat/google → openai layout) | Removes 3 monolith adapters; pure cut/paste. | S (×3) | — | Same package, no signature change. |
| **P5.3 Provider Stream-goroutine decomposition** | B2 (per-adapter `decodeStream`/`streamDecoder`), B5 (`toResponsesInput`) | Collapses the giant nested SSE switches. | L | P5.2 (cleaner once split) | Public `Stream` signature unchanged. |
| **P5.4 Provider transport/codec dedup** | F1 (`openaichat` shared codec), F2 (`ParseOpenAIUsage`), F3 (`transport` Executor+StreamRunner), F5 (single body builder), F4/F6/F7 | Kills the 551-line clone + 9× error triad + ~480 lines of wrapper boilerplate; F3 is the natural home for E3's `parseErr` capture. | L (F1,F3) + M (F5,F6) + S (F2,F4,F7) | F1 before F5; E3 may fold into F3 | Behavior-preserving extraction; per-provider tests are the harness. |
| **P5.5 agent library file-splits** | B4 (`checkpoint_format.go` + `collectCheckpointData`), B6 (`tool_definitions.go`, `session_tool_registry.go`) | Breaks up the remaining agent god-files. | M (B4) + S (B6×2) | coordinate with P3.2/P4 unexports | Same package, pure relocation. |
| **P5.6 App god-files** | B7 (`hubRelayManager`, split `app_rpc.go`) | App internal-cleanliness bar. | M (×2) | — | Same package, behavior-preserving. |

### Phase 6 — Subpackages + actor core (deferred; only after the seams + Phase 4)

| Area | Items | Rationale | Size | Depends on | Behavior-preservation |
| --- | --- | --- | --- | --- | --- |
| **P6.1 Carve `agent/events`** | §2.2: move the event contract to a public `agent/events`, fix `Data any` → sealed/generic, type `Usage`, retire `DataMap` round-trip | The one clean public leaf-cluster; carves ~70 symbols; sharpens server/serf-hub imports. | L | P4.1 (event tests black-box), G5 | `SessionEvent.Data` already `any` → core type unaffected; consumers update imports. The typing change is a contract tightening — coordinate the projector (`internal/appprojector`). |
| **P6.2 Internal subpackages (only if PRI-1947 seams have landed)** | strategy → `agent/contextstrategy`; tools → `agent/internal/tools`; plugins/MCP → `agent/internal/plugins`; eval → `agent/eval` | Real boundaries fall out of the seam work; zero external refs so unobservable to callers. | L | **PRI-1947 seams + P4.1** | Pure relocation; the in-place unexports (P3.2) already removed them from the surface. |
| **P6.3 Actor core** | C5 (command mailbox, atomic snapshot, collapse `s.mu`/3 outer locks), C6, C3 | Makes the A2/A4 race class structurally impossible and deletes 4 mutexes. | XL | P0.2 (cache-reads-safe first), PRI-1947 `subagentManager` | Highest-risk; staged; suite + a concurrency test gate it. Behavior-preserving by construction (loop applies commands at existing safe points). |

**Critical-path summary:** Phase 0 → (Phase 1, Phase 2, Phase 3 in parallel) → **Phase 4 (gates)** →
Phase 5 (mostly parallel-safe) → Phase 6. The hard ordering edges: **P3.1 before P3.3**;
**Phase 4 before P6.1/P6.2**; **PRI-1947 seams before P6.2/P6.3**; **P0.2 before P6.3**; **F1 before F5**.

---

## 5. The standing gate (the permanent bar)

A change that lands in `agent/` or `llm/` must pass all of the following. CI enforces them; reviewers
treat any exception as failing the bar (Rule #1 — no exceptions without Jesse's sign-off).

| Gate | Command | Scope | Enforced by |
| --- | --- | --- | --- |
| **Race-free** | `go test -race ./...` | whole module | CI (add `-race` to the existing test step) |
| **Vet-clean** | `go vet ./...` | whole module | CI (already present) |
| **Formatted** | `gofmt -l .` (empty output) | whole module | CI |
| **Wire-tag naming** | `go run ./cmd/serf-namingcheck` | whole module | CI (already present) |
| **Go identifier hygiene** | golangci `stylecheck` ST1003 (initialisms) + ST1016 (receivers) | scoped to `agent/…`, `llm/…` | CI (`.golangci.yml`) — **new (G3)** |
| **Godoc completeness** | golangci revive `exported` (or staticcheck ST1000/ST1020/ST1021): package doc + every exported type/func/interface documented | scoped to `agent/…`, `llm/…` | CI (`.golangci.yml`) — **new (D6)** |
| **No god-file** | a size check (e.g. fail any non-test `.go` > ~800 lines, or any top-level func body > ~200 lines) | `agent/…`, `llm/…` (apps advisory) | CI script / linter — **new** |
| **No `recover()`-as-flow** | grep gate: no new `defer func(){ _ = recover() }()` in `agent/…`/`llm/…` outside the documented legitimate sites | `agent/…`, `llm/…` | CI grep — **new (A1/A5)** |
| **No silent `_ = err`** | grep gate: no `_ = err` assignment on an error value without an adjacent `// intentionally ignored` justification | `agent/…`, `llm/…` | CI grep — **new (E4 class)** |
| **No `internal/` in public signatures** | `go list`-based check: no exported `agent`/`llm` signature names a `…/internal/…` type | `agent/…`, `llm/…` | CI script — **new (§2.0)** |
| **Black-box behavioral tests** | new behavioral tests use `package agent_test`/`package llm_test` | `agent/…`, `llm/…` | review convention (post-Phase-4) |

The two `_ = recover()` sites that remain after A1/A5 (the defensive jsonschema recovery at
`tool_registry.go:524` and the re-panic-after-cleanup sites) are explicitly whitelisted by the grep gate as
documented legitimate uses, not recover-as-flow.

---

## Appendix: ticket map (one per roadmap area)

P0.1 events-race+recover · P0.2 session sync writes/reads · P0.3 hub_model finish · P0.4 chan_stream recover ·
P1.1 llm error wrapping · P1.2 stream-read surfacing · P1.3 error hygiene ·
P2.1 package docs+examples+concurrency doc · P2.2 godoc+naming lint · P2.3 naming-ignore pragmas ·
P3.1 promote config schema · P3.2 zero-risk unexports · P3.3 SessionConfig+profile path · P3.4 ToolRegistry leak ·
P4.1 black-box test migration ·
P5.1 processOneInput · P5.2 adapter file-splits · P5.3 Stream-goroutine decomposition · P5.4 provider transport/codec dedup · P5.5 agent file-splits · P5.6 app god-files ·
P6.1 agent/events subpackage · P6.2 internal subpackages · P6.3 actor core.
