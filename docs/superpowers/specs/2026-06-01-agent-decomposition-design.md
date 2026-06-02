# Agent package decomposition — design v2 (post adversarial review)

**Status:** PROPOSAL v2. v1 was sunk by a two-reviewer adversarial pass (the
`agent/schema` keystone created a hard import cycle; the carve was mis-sold as a
surface-shrink). This version takes the findings seriously. No code until signed
off + the chunk-1 plan is written.

## 1. Goal — stated honestly (two separable concerns)

The flat `package agent` is 71 non-test files / ~20,471 LOC / **52 exported types
+ 47 exported funcs** in one namespace (`agent/events` + 5 `agent/internal/*`
packages already exist — the carve pattern is established, not new).

Two distinct wins, **separated** because the review proved they are:

- **Legibility / organization (the win you want):** break the monolith into ~10
  cohesive packages, each understandable in isolation with explicit downward
  dependencies. This is delivered by the **structural carve**. It is *not* a
  public-surface reduction (most engine machinery — `toolRegistry`,
  `contextManager`, `mcpManager`, the strategies — is **already unexported**, so
  relocating it removes nothing from `go doc agent`).
- **Surface area (cheap warm-up):** shrink the *exported* surface by **unexporting
  in place** the exported types with no external consumers. No package move, no
  cycle, no test migration. Done first (Phase 0) — it also settles each type's
  public/private status *before* the structural carve, de-risking it.

## 2. What the adversarial review changed (load-bearing)

1. **The `SessionConfig` cycle (both reviewers, Critical).** `SessionMeta.Config`
   and `SessionSnapshot.Config` embed `SessionConfig` by value
   (`snapshot.go:22,39`), and `SessionConfig` references engine types
   (`ResolveProfile func`→`ProviderProfile`, `contextStrategyOverride
   contextStrategy`, `sharedTaskStore *TaskStore` — `session_config.go:144,164,196`).
   So `SessionMeta`/`SessionSnapshot` **cannot** move to a Layer-0 `agent/schema`
   without `schema`→`agent` cycling. → **They do not move in chunk 1.** The
   persistence split is its own designed chunk (§5).
2. **Carve ≠ surface-shrink (B-M2, verified).** Reframed as §1.
3. **`strategyHost.Snapshot()` hides a cycle (B-M1).** It returns
   `SessionSnapshot` (→`SessionConfig`, engine-bound), so the strategies can't
   move to `agent/internal/context` until the seam is narrowed (§4).
4. **Breaking is acknowledged and accepted.** Moving any moved type is a breaking
   import-path change for the root-module consumers (`cmd/`, `server/`, `internal/`,
   `cmdutil/`, `hubapi/` — `agent` is a separate module to them). Jesse has OK'd
   reshaping published surfaces when genuinely better; edit volume is compiler-
   verified, so it is acceptable. Every move is a **real move** (rewrite
   `agent.X`→`pkg.X` at all sites), never a `type X = pkg.X` alias (an alias keeps
   `agent.X` alive and defeats the point).
5. **CI gates are hardcoded lists (A-M4/B-M3).** `serf-docscheck` and
   `serf-internalcheck` enumerate `libraryPackages` literally
   (`cmd/serf-docscheck/main.go:36`, `cmd/serf-internalcheck/main.go:35`); they do
   **not** auto-cover new packages. → Every chunk that adds a public package edits
   both lists, and we add a test asserting the lists cover all `agent/**` public
   packages.

## 3. Target architecture (layered; deps point down)

```
Layer 2  package agent  (engine / facade — import path UNCHANGED)
   Session, ProcessInput, turn loop, queue/steering, lifecycle, NewSession/
   RestoreSession/ForkSession, SessionConfig, the 5 *Session-touching helpers
   (status, context_metrics, history_repair, tool_web_*), subagents (until §4)
        │  composes Layer-1 via seams
Layer 1  subsystems (no live-Session dependency)
   PUBLIC:   agent/provider   ProviderProfile (+ its redesign, later chunk)
   INTERNAL: agent/internal/tool      registry + builtin tools  (seam toolDeps ✓)
             agent/internal/context   contextManager+strategies (seam strategyHost,
                                       after §4 narrowing)
             agent/internal/mcp       mcpManager
             agent/internal/plugin    hookRunner + loader internals
   SPLIT public-contract vs internal-machinery (per verified consumer set):
             agent/task        Task (public, in schema) ; TaskStore machinery
             agent/transcript  header/entry types (schema) ; writer/reader
Layer 0  foundation (imports only llm + stdlib)
   agent/schema   Turn, TurnKind  (chunk 1) ; later: SessionMeta/Snapshot via §5
   agent/execenv  ExecutionEnvironment, LocalExecutionEnvironment, ExecResult,
                  DirEntry, EnvVarPolicy  (full type closure)  (chunk 1)
   agent/events   (already carved ✓)
```

**Public vs internal is decided per type by verified external use** (the rule:
referenced by name OR returned by an externally-called exported func ⇒ public).
Confirmed-public (stay/become public): `LoadedPlugin`, `SkillMeta`, `HookEvent`,
`RegisteredHook`, `MCPServerConfig`, `ContextMetrics`, the transcript header types,
`ProviderProfile`. Confirmed-internalizable: `toolRegistry`, `contextManager`,
`mcpManager`, the strategies (already unexported), plus the Phase-0 set.

## 4. Seam prep (must precede the entangled carves)

**Corrected per the v2 adversarial review — the v2 seam claims here were wrong.**

- **tools** → `toolDeps` (`session_tool_registry.go:23`) decouples tools from the
  `*Session` *receiver* (good), but the struct still embeds `steeringMessage`
  (`session_queue.go:43`, an engine type) and a `skill func(...) SkillMeta`.
  **Prep:** relocate the trivial `steeringMessage` value type (and depend on
  `skill`/`task` packages first). Not "zero prep."
- **context** → `*Session` *is* genuinely decoupled (`strategy_*`/`context_*` hold
  `strategyHost`, zero `*Session` refs), BUT context does **not** depend only on
  `agent/schema`. After narrowing `Snapshot()`, it still hard-depends on:
  - **provider:** `strategyHost.Profile() ProviderProfile` (`context_strategy.go:19`,
    used `strategy_recall.go:100`), `contextManager.profile` (`context_manager.go:26`),
    `ForkSummarize(... ProviderProfile ...)` (`fork_summarize.go:16`).
  - **tool:** `contextStrategy.Tools() []registeredTool` (`context_strategy.go:36`),
    constructed in `strategy_recall.go:46,64`, `strategy_session_log.go:43`.
  - **transcript/persistence:** `SaveSession`/`loadSnapshotFromPath`→`SessionSnapshot`
    (`strategy_recall.go:95,120`, `transcript_tools.go:125`).
  **So context is carvable only AFTER provider + tool + transcript**, and either
  (a) provider/tool are carved first, or (b) a `schema.profileView` value-snapshot
  (id/model/contextWindow) replaces `Profile()` in the seam. Context may, like
  subagent, end up staying in `package agent` if the prep is too costly. **Two
  more seam-preps than v2 budgeted.**
- **subagent** → no seam; `subagents.go` reaches ~18 parent + ~10 child unexported
  members and constructs child `*Session`s (it holds a `sess *Session` field —
  *not* embedding). A faithful `subagentHost` would be ~20 members ≈ exposing the
  engine. **Decision deferred to its chunk:** prototype `subagentHost`; if it is
  that wide, **subagent simply stays in `package agent`** (cheaper and honest).
  Verified: only `subagents.go:284` constructs a `*Session` outside the
  constructors, so subagent-stays is genuinely cycle-free.

## 5. The `SessionMeta`/`SessionSnapshot`/`SessionConfig` persistence chunk

Carving these requires breaking the §2.1 cycle. Design direction: introduce
`schema.ConfigSnapshot` holding only the **json-serializable wire fields** of
`SessionConfig`, **with identical json tags** so existing `meta.json` round-trips
unchanged. `SessionMeta.Config` becomes `schema.ConfigSnapshot`; the live engine
`SessionConfig` (with the `json:"-"` engine fields `spawn`, `testOnly`,
`contextStrategyOverride`, `ResolveProfile`, `sharedTaskStore`) stays in
`package agent`. The public Go type of `SessionMeta.Config` changes — an accepted
breaking change.

**Corrected per the v2 review — the wire subset is NOT self-contained.** One
serialized field leaks an engine type: `ToolOutputLimits map[string]ToolOutputLimit`
(`json:"tool_output_limits"`, `session_config.go:34`), and `ToolOutputLimit`
(`tool_registry.go:92`) → `TruncationStrategy` (`tool_registry.go:20`) live in the
tool layer. So this chunk must **first relocate `ToolOutputLimit` +
`TruncationStrategy` down to `agent/schema`** (both are clean value types — a
`{int,int,TruncationStrategy}` struct and a string enum), then `ConfigSnapshot`
can embed `schema.ToolOutputLimit` with identical tags. The tool registry
(`registeredTool.Limit`, `truncateChars`) then consumes the schema type.

## 6. Chunking

- **Phase 0 — in-place unexport** (surface warm-up): unexport the exported types
  with no external type-reference **AND not returned by ANY exported func/method on
  an exported receiver** — because revive `unexported-return` (`.golangci.yml:41`)
  fires on the *declaration* regardless of whether the returner is ever called
  (empirically verified). So unexporting such a type **cascades**: its exported
  constructors/accessors (e.g. `NewSessionLog → newSessionLog`) must be unexported
  in lockstep and their callers updated. Not a pure type-only rename; budget the
  cascade. Gated.
- **Chunk 1 — the two genuinely-clean foundation carves (BOTH v2 reviewers
  independently verified these are clean):** `agent/schema` = `Turn` + `TurnKind`
  only (pure atom: `turns.go` imports only `time`+`llm`; ~53 in-module files +
  ~9 cross-module consumer files rewrite `Turn`→`schema.Turn`, compiler-driven —
  `Turn` is widely externally consumed, so this is a real downstream break) **+**
  `agent/execenv` (full type closure). **Ends with: (a) reflect on chunk 1,
  (b) write the plan for the rest.**
- **Later chunks — CORRECTED topological order** (the v2 §6 order was wrong: it
  invented `skill→mcp` and `plugin→context` edges and put provider *after*
  context, which cycles). Real verified edges: `provider→tool` (`profile.go:349`),
  `context→provider` + `context→tool` (§4), `tool→skill` + `tool→task`
  (`session_tool_registry.go:59,91`), `plugin→skill` + `plugin→mcpconfig`
  (`plugin.go:70,73`), `mcp→tool` (`mcp_manager.go:111`), `context→transcript`
  (`strategy_recall.go:120`). Valid order:
  **`skill` · `task` · `transcript` → `tool` (promote `toolRegistry`→`tool.Registry`
  for mcp) · `mcpconfig` → `mcp` · `provider` (+ ProviderProfile redesign) ·
  `plugin` → `context`.** Provider and tool MUST precede context.
- **Persistence chunk (§5):** relocate `ToolOutputLimit`+`TruncationStrategy`, then
  `SessionMeta`/`SessionSnapshot`/`ConfigSnapshot` → schema. Must precede the
  transcript-reader carve (it returns `SessionSnapshot`).
- **Last:** subagent (or it stays in `package agent` per §4). Context, too, may
  prove uncarvable without the provider/tool prep and could stay put.

## 7. Mechanical procedure (per carve)

1. Create the package dir; move the declarations (struct-by-struct surgery where a
   type shares a file with engine code — e.g. `Task` in `task_store.go`).
2. `go build ./...` enumerates every broken reference (zero silent misses).
3. Qualify each `agent.X`→`pkg.X`; `goimports -w` fixes imports. **Never
   `gofmt -r` on type names** (collides with field names like `ForkSummaryData{Turn}`).
4. Move/split the white-box tests (per-chunk task — several mix moved types with
   engine-only internals like `spawnConfig`; budget the surgery, don't assume
   "tests come free").
5. Update `serf-docscheck` + `serf-internalcheck` lists if a public package was
   added.
6. Gate: build + vet + `-race` + golangci across all four go.work modules; commit.

## 8. What does NOT change

`agent.NewSession`/`Session`/`SessionConfig` and the import path
`primeradiant.com/serf/agent`. The module boundary (one Go module). Behavior
(`-race` + full suite prove equivalence per chunk).

## 9. Residual risks

- The carve is **internal-organization value, not surface-shrink** — judged worth
  it because the monolith's illegibility is the stated problem.
- `subagent` may be uncarvable as a narrow seam (§4) — accepted; it stays put.
- Intra-Layer-1 edges force unexported→exported promotions (e.g. `tool.Registry`)
  that slightly *widen* some internal APIs — a real cost of the boundaries.
- Per-chunk test surgery is non-trivial (B-M5); each chunk budgets it explicitly.
