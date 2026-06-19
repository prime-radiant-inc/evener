# Serf Doctoring System — Unified Design

Date: 2026-06-19
Status: design spec for Jesse review (UNCOMMITTED)
Supersedes (folds in): `docs/superpowers/specs/2026-06-19-serf-doctoring-tools.md` (the TOOLS half) and `docs/superpowers/specs/2026-06-19-serf-debugging-docs.md` (the DOCS half).
Builds on: `docs/superpowers/specs/2026-06-19-doctoring-skill-doc-adoption.md` (the ADOPTION audit), meta-doctor's `scaffold-doctor` reference contracts (`/home/jesse/git/meta-doctor/plugins/meta-doctor/skills/scaffold-doctor/references/`).

This is a **design spec, not an implementation**. It defines the architecture, the contracts, and the build plan for serf's on-demand doctoring system. The doctor is a **skill plus a small set of compiled tools**: its data plane is a standalone **`serf-doctor` binary** (a thin `cmd/serf-doctor` main over a focused `agent/doctor` package) — Go that imports serf's own canonical types and folds — invoked as `serf-doctor <subcommand>`; the `doctoring-serf` skill carries the knowledge, contracts, and runbooks and *invokes* those tools. All load-bearing structural facts were re-verified against the Go source (see §8); citations are to Go **symbols**, never `file:line`.

---

## 1. Motivation — and the load-bearing insight

### The pain

We shipped observer-watch causal provenance and ran live E2E tests. Verifying what actually happened meant hand-parsing serf's on-disk JSONL, and it broke in recurring ways — each a real failure from that session, not a hypothetical:

- A hand-written transcript parser returned `0 communicate calls` / `0 steering entries` because it guessed the JSONL shape wrong (looked for top-level keys; a `communicate` call is a *tool call* nested at `entry.Turn.Message.Content[].ToolCall.Name`, and a steering turn's kind is `STEERING`, one level deeper than the parser looked).
- `grep -c watch_send_pending` over `jobs.jsonl` overcounts deliveries, because pending frames coalesce latest-wins by `WatchSendKey` + `UpdateSeq` (this is what `jobstore.FoldWatchSends` replays). A `pending != delivered` gap *looks* like a dropped delivery but is usually expected coalescing.
- "Was this watch delivery caused by the caller (legit) or by the session it feeds (a feedback loop)?" required reading the provenance chain by hand and comparing session IDs.
- `grep -c delegate_send` returned 5 where the real *tool-invocation* count was 0 (the other hits were a tool list in an `api_call` log and a "do not call delegate_send" instruction in assistant text).
- Parent/observer/delegate sub-sessions are different SIDs in *different* project-hash buckets; linking them was manual cross-referencing.
- Every inspection started with `find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`.

The throughline: **serf already has the typed structs and fold logic that produce the exact answers we were reconstructing by hand** — `jobstore.FoldWatchSends`, `provenance.ContainsWatch`, the `transcript.Entry` grammar. The same rot is replicated across ~74 `test/scenarios/*.md` plus several docs — each an independent copy of serf's on-disk shape, none of which fails loudly when the schema moves. And there is no failure-mode taxonomy and no conceptual data-model reference: the load-bearing knowledge an ad-hoc parser lacks. (The doctoring system's data plane **calls that Go logic directly** — it `import`s those types and folds, so a schema change flows through automatically or fails to compile; the type coupling is the drift guard, §2/§5.)

### The insight: adopt meta-doctor's *contracts*, not its *engine*

meta-doctor (`scaffold-doctor`) generates a complete scheduled "doctor": an `index` mode-router + remediation orchestrator, a `run-skill` agentic-loop driver (model fallback, anti-loop guard, tool dispatch, per-tool error isolation, token/iteration budgets), a `dedup` filter, an `error-classifier`, a `summary` health roll-up, a Slack digest, and a `repair-pipeline`/`repair-ci`. That apparatus exists because meta-doctor's target is a *plain repo with no agent runtime* — it must synthesize the loop.

**Serf is already a full agent.** It already provides the agentic loop, tool dispatch, model fallback, error isolation, the spawn/delegate/observe fabric, the durable transcript, and a persona/skill system. So almost all of meta-doctor's apparatus **collapses into "serf + a persona + skills."** What survives the collapse is not the engine — it is the *contracts*: the Finding schema and its stable dedup signature, the "healthy run emits zero findings" discipline, the runbook structure (HEALTHY / INSPECT / CLASSIFY, pull-live-config), the self-consistency winner-selection for load-bearing repairs, and the propose-validate-never-silently-apply conservatism. **This is the load-bearing design decision: we take meta-doctor's contracts and conservatism wholesale, and we throw its engine away because we already have a better one.**

The doctoring system is therefore three thin layers over serf's existing runtime, not a new subsystem.

---

## 2. Architecture: three layers, clean seams

**On-demand only. No scheduling.** (meta-doctor's daily/weekly/repair-ci modes and EventBridge/cron triggers are part of the discarded engine. A serf session — or a human — invokes the doctor when they want it.)

```
   ┌─────────────────────────────────────────────────────────────┐
   │  doctor AGENT TYPE  (the persona)                            │
   │  loads the skill · runs `serf-doctor …` · carries write/heal │
   │  spawnable + delegatable (a session can delegate→doctor)     │
   └──────────────────────────────┬──────────────────────────────┘
                                  │ loads                 │ invokes
                                  ▼                        ▼
   ┌───────────────────────────────────┐  ┌──────────────────────────┐
   │  doctoring-serf SKILL             │  │  serf-doctor  (Go binary) │
   │  SKILL.md + references/           │  │  cmd/serf-doctor main over│
   │       + runbooks/   (contracts)   │  │  agent/doctor package     │
   │  knowledge + contracts + runbooks │  │  subcommands:             │
   │  data-model.md cites the Go       │  │   locate · transcript     │
   │  types as the source of truth;    │  │   watches · tree          │
   │  runbooks INSPECT via             │  │  imports jobstore folds / │
   │  `serf-doctor <cmd>`             │  │  provenance / transcript  │
   │  portable · progressive disclosure│  │  types — compile-coupled  │
   └───────────────────────────────────┘  └──────────────────────────┘
```

### Layer 1 — the `doctoring-serf` skill (knowledge + contracts + runbooks)

Lives at `internal/bundled/skills/doctoring-serf/` and is embedded into Serf. It is **portable** inside Serf: any Serf agent can load it by name through the skill registry. It carries the doctor's *knowledge* — not its data plane: the data plane is the compiled `serf-doctor` tools (Layer 2), which the skill *references*. Structure: a small always-loaded `SKILL.md` (the diagnose→findings loop, the Finding contract in brief, the `serf-doctor` subcommand list, and *when* to pull each reference) + `references/` (the heavy grammar/recipes, pulled only on demand) + `runbooks/` (audit definitions that INSPECT via `serf-doctor <cmd>` invocations). The craft is the **progressive-disclosure boundary**: `SKILL.md` stays small; the data-model grammar, the failure taxonomy, the finding schema, the runbook-authoring craft, and the repair guardrails live in references that are pulled only when the task needs them. Detailed in §3.

### Layer 2 — the `serf-doctor` binary (data plane)

**The doctor's data plane is compiled Go that uses serf's core, shipped as a standalone `serf-doctor` binary invoked as `serf-doctor <subcommand>`.** The forensic logic lives in a focused `agent/doctor` package (under `agent/`, so it CAN import `agent/internal/jobstore`); `cmd/serf-doctor` is a thin `main` over it (parallel to the existing `cmd/serf-hub` / `cmd/serf-tui` binaries). Because `agent/doctor` lives under `agent/` it `import`s serf's own canonical types and folds — `jobstore.FoldWatches`/`Fold`/`FoldWatchSends` (and the `Load*` accessors), `provenance.Causal`/`ContainsWatch`, `transcript.Entry`, `schema.Turn`, `effectiveResultToolName` — and answers questions *with the same code the runtime uses*. **Design goal:** `agent/doctor` imports ONLY the durable-format packages it reads — `agent/internal/jobstore`, `agent/provenance`, `agent/transcript`, `agent/schema` — and **not** the agent session/runtime, keeping `serf-doctor` a lean, type-coupled forensic reader decoupled from the agent runtime (the implementer keeps `agent/doctor`'s import graph minimal). The four subcommands cover four capabilities:

- `serf-doctor locate <selector>` — resolve a selector to absolute on-disk paths (kills the `find`-tax).
- `serf-doctor transcript <selector>` (`--count <tool>`) — render a session's logical turns, and answer "how many real X calls" structurally (the structural tool-call counts).
- `serf-doctor watches <selector>` — distinct deliveries (collapsing coalescing) + provenance + lifecycle + self-loop verdict.
- `serf-doctor tree <selector>` — parent↔delegate/observer linkage across buckets.

Detailed in §5.

**Why a standalone binary, not a `serf` subcommand (honest tradeoffs).** Both costs are minor. `serf-doctor` is a separate binary that must ship alongside `serf` and be on PATH (the doctor agent type shells out to it). And it is slightly less discoverable than a `serf --help` subcommand. The upside is better DX: a dedicated `serf-doctor watches <sid>` tool that humans and `test/scenarios` reach for directly, decoupled from the main `serf` CLI surface.

**Why compiled Go, not in-skill scripts (the drift guard).** An external re-parser — a script that re-implements the on-disk shape — rots the instant the schema moves: nothing forces it to track a format change, and a frozen fixture only catches *parser regressions against a stale sample*, never drift between that sample and live serf output. The real drift guard is **compile-time type coupling**: the `agent/doctor` package `import`s serf's own types and folds, so a schema change either flows through automatically or fails to compile. `jobs.jsonl` coalescing is `jobstore.FoldWatchSends`, not a re-derived latest-wins; the transcript unwrap is `transcript.Entry`, not a hand-rolled struct; the provenance semantics are `provenance.ContainsWatch` over `Causal.WatchKeys`, not a re-implemented predicate. There is no re-implementation to drift. (Ordinary behavior tests — fixtures/goldens over sample artifacts — are still worth shipping to catch *behavioral* regressions in the tools, but they are not the drift guard; the compiler is.)

### Layer 3 — the `doctor` agent type (the persona)

The persona that loads the skill, runs the `serf-doctor` tools (via the shell tool), and carries the write/manage/repair skills. **Spawnable + delegatable**: a session can `delegate → doctor` to diagnose itself, another session, or the fleet. Detailed in §4.

### The doctor's three on-demand capabilities

1. **Diagnose** — run runbooks (invoking `serf-doctor <cmd>`) → emit Findings.
2. **Extend** — write/manage runbooks (also how the runbook/skill corpus stays evergreen).
3. **Heal** — repair runbooks + the doctor's Go tools + core skills (under graduated guardrails, §6).

### meta-doctor → serf mapping (what collapses, what survives)

| meta-doctor module / concept | In serf | Disposition |
|---|---|---|
| `index` mode-router + remediation orchestrator | the agent runtime + the `doctor` persona's loop | **collapses** — serf already orchestrates |
| three run modes (daily / weekly / repair-ci) + EventBridge/cron | on-demand spawn/delegate of the `doctor` agent type | **discarded** — no scheduling |
| `run-skill` agentic-loop driver | serf's own agentic loop | **collapses** — serf is the loop |
| model fallback (`createMessage` retry) | serf's provider fallback | **collapses** |
| per-tool error isolation (`is_error` tool results) | serf's tool dispatch | **collapses** |
| anti-loop guard (last-3-calls nudge), token/iteration budgets | serf's loop controls / harness | **collapses** |
| `tools/` data-plane (Athena/CloudWatch/GitHub/…) | **the standalone `serf-doctor` binary** (`cmd/serf-doctor` over the `agent/doctor` package, subcommands `locate`/`transcript`/`watches`/`tree`), run via the shell tool | **becomes the `serf-doctor` binary** — serf-specific, imports serf's own types/folds (compile-coupled, no re-parser) |
| `emit_finding` / `skill_done` control tools | serf's result tool (`communicate`) + the Finding JSON envelope | **adapt** — serf already has a result tool; Findings are structured output, not new control tools (§6) |
| **Finding schema, signature formats, routing, zero-on-healthy, dedup** | the **finding-contract** reference + the agent persona | **ADOPT (contract)** — serf-adapted, §6. *Note:* meta-doctor's `code` route repairs the **target system** under audit (opens a PR against that repo); serf's doctor has no such route — a finding that's a bug in serf itself can only route to report-only `diagnosis` (§6). |
| **runbook structure** (HEALTHY/INSPECT/CLASSIFY, pull-live-config, authoring checklist) | the **writing-runbooks** reference + `runbooks/` | **ADOPT (contract)** — §3, §6 |
| `repair-pipeline` five-phase localize→repair→summarize | the **Heal** capability + repair-guardrails | **ADOPT the contract, not the TS engine** — serf's edit tools do the localize/patch; we keep the *discipline* (§6) |
| **self-consistency winner-selection** (N candidates, majority ≥2 by byte-identical normalized source) | repair-guardrails for **code/tool** repairs | **ADOPT (contract)** — §6. Byte-majority only works for code (the doctor's Go tools); prose/markdown repairs use review + the validation gate, not byte-majority. |
| `repair-ci` CI babysitter + attempt-counter persistence | — | **discarded** — out of scope (no CI auto-repair loop) |
| `dedup` (open PR/ticket suppression), `error-classifier`, `summary` health roll-up | folded into the Finding contract's signature/dedup + severity/category | **collapses into the contract** |
| Slack digest (`postDigest`, always-runs) | the doctor's natural-language report to its caller | **collapses** — the agent reports back; no separate digest channel |

The surviving column is small and entirely *contract*. That is the whole point.

---

## 3. The `doctoring-serf` skill

Greenfield at `internal/bundled/skills/doctoring-serf/`. Shape cloned from `docs/skills/benchmark-driven-improvement/` (a `SKILL.md` + reference siblings), with an added `runbooks/` directory.

```
internal/bundled/skills/doctoring-serf/
├── SKILL.md                       # always-loaded: the loop + contracts-in-brief + `serf-doctor` subcommand list + when-to-pull
├── references/
│   ├── data-model.md              # what is on disk (conceptual), each artifact → Go type (the source of truth) + the `serf-doctor` reader
│   ├── failure-modes.md           # the taxonomy: symptom → what it is → confirm-with → mechanics link
│   ├── finding-contract.md        # the serf-adapted Finding schema + signatures + zero-on-healthy
│   ├── writing-runbooks.md        # runbook authoring craft (HEALTHY/INSPECT/CLASSIFY, checklist)
│   └── repair-guardrails.md       # graduated repair policy by blast radius
└── runbooks/
    ├── observer-self-loop.md      # seed audit
    └── watch-delivery-health.md   # seed audit
```

The data plane is **not** in the skill — it is the compiled `serf-doctor` tools (`cmd/serf-doctor` over the `agent/doctor` package, §5). The skill references them.

### `SKILL.md` outline (small, always loaded)

Following the in-repo precedent (HARD-GATE framing, a tool table, a "see X.md in this skill directory" progressive-disclosure pattern, an anti-pattern table):

1. **Identity + core principle.** "You diagnose serf sessions/health by reading canonical state through the `serf-doctor` tools, never by hand-parsing JSONL. A healthy run emits zero findings."
2. **The diagnose→findings loop** (compressed): pick/load a runbook → INSPECT by running `serf-doctor <cmd>` (via the shell tool) → CLASSIFY → `emit` a structured Finding per confirmed, actionable problem → report. Mirrors meta-doctor's runbook-runner contract, expressed as agent steps (serf *is* the loop).
3. **The Finding contract in brief** — the required fields and the "every finding actionable or unemitted; healthy ⇒ zero findings" rule. Full schema deferred to `references/finding-contract.md`.
4. **The `serf-doctor` subcommand list** — one line each (`locate`/`transcript`/`watches`/`tree`), with the one-sentence "what it answers" and the invocation (`serf-doctor <cmd> <selector> …`). Flag-level detail lives in each subcommand's `--help`, not here.
5. **HARD GATE: read `data-model.md` first, inspect through `serf-doctor`, never hand-parse.** Two coupled rules: (a) before reading any artifact, read `references/data-model.md` — the consult-don't-guess contract that kills the "guessed the JSONL shape → `0`s" failure (the agent still reads `serf-doctor` *output*, but should understand the model behind it); (b) inspect through the `serf-doctor` tools, never an ad-hoc one-off parser. Mirrors `agent/prompts/sections/transcripts.md`'s "use the transcript tools, do not read raw transcript files directly" rule. State it as a gate.
6. **When to pull each reference** (the progressive-disclosure index):
   - "What is on disk / what does this field mean?" → `references/data-model.md` (**always** before reading)
   - "I see weird behavior X — what is it?" → `references/failure-modes.md`
   - "I'm about to emit a finding" → `references/finding-contract.md`
   - "I'm writing/registering a runbook" → `references/writing-runbooks.md`
   - "I want to repair a runbook, a doctor tool, or a core skill" → `references/repair-guardrails.md` (**always** before any repair)
7. **Anti-pattern table** (a handful): hand-parsing JSONL instead of running `serf-doctor`; reading an artifact before reading `data-model.md`; emitting FYI/PASS findings; counting `watch_send_pending` lines as deliveries; using `ContainsWatch`/`WatchKeys` as a self-loop verdict on a recorded delivery (it's the runtime suppression predicate — vacuously true post-stamp; read the `Chain` for a same-`watch_id` prior hop instead); silently applying a core-skill (or doctor-tool) repair.

**The progressive-disclosure boundary**: `SKILL.md` carries the loop, the gate, the brief contract, the subcommand names + invocations, and the pull-index — nothing heavy. Every grammar table, every failure recipe, the full Finding JSON schema, the runbook skeleton + checklist, and the entire repair policy live in `references/` and are read only on demand. The `serf-doctor` subcommands are tools the skill references, not prose to load — their flag-level detail lives in `--help`. This is the same craft the in-repo model skill uses ("see `root-cause-task-failure.md` in this skill directory").

### The `references/` set — adopt / extract-from / write-fresh map

Sources keyed from the adoption audit (`2026-06-19-doctoring-skill-doc-adoption.md` §4) and corrected per §8.

| Reference | Mode | Source(s) | New work |
|---|---|---|---|
| `data-model.md` | **assemble + correct** (the conceptual reference; the Go types are the source of truth) | **adopt** `docs/tools/transcripts.md` (transcript grammar, refs, buckets, scope, result-tool, kind=root/subagent/fork) wholesale. **cite** the Go types as the source of truth (`serf-doctor` imports these; symbols, never `file:line`): `transcript.Entry`/`schema.Turn` (transcript), `schema.SessionMeta`/`schema.ListSessionMetas` (meta + lineage), `jobstore` folds + `WatchSendState`/`WatchSendKey`/`WatchRecord`/`DelegateRecord` (jobs), `provenance.Causal`/`WatchKey`/`Entry` (provenance), `RuntimeDir`/`hexHash` (state dir). **cite** `docs/architecture.md` (watch-outbox + level-by-level-coordinator mechanics + module shape), `docs/job-control.md` (job-record fields), `docs/conventions/naming.md` (snake_case on disk). **extract** the field model for the **in-transcript `api_call` lines** (`transcript.APICall`, kind `api_call`) and the separate per-call latency log `<state-dir>/api.jsonl` (`llm.APILogger`/`APILogEntry`) from `docs/performance-profiling.md` (the two are distinct sources — keep them apart; the field model only, never a parser). | thin glue prose tying state-dir layout → transcript grammar → jobs-is-a-folded-event-log → meta → durable-vs-live appwire, each artifact ending "read it via `serf-doctor <cmd>`". This doc is the conceptual reference the consult-don't-guess gate points at; the Go types it cites are the real source of truth (the tools import them), and it should track the on-disk format. Apply **all** §8 corrections. |
| `failure-modes.md` | **extract + write taxonomy** | **cite** `docs/architecture.md` (watch loops / watch-outbox drain / level-by-level-coordinator / single-hop-forwarding mechanics) and `docs/job-control.md` (status×reason, dropped-vs-coalesced incl. `evicted`, `runtime_lost`/`supervision_lost`). The "stuck turns" / "deaf coordinator" *framing* is from the debugging-docs spec (`2026-06-19-serf-debugging-docs.md`), built on those architecture mechanics. **extract** the provenance model + same-watch suppression from `2026-06-18-observer-watch-origin-loop-design.md` (with §8 type-name + WatchKeys-vs-Chain corrections); the watch-self-loop worked example + "Resolution" from `2026-06-12-job-control-watch-deadlock-design.md`; the 6-category missing-support taxonomy from `docs/skills/benchmark-driven-improvement/root-cause-task-failure.md` (the eval-failure axis); hook-blocked/failed from `docs/hooks.md`; provider-error from `docs/llm-providers.md`; the provenance-rail probe checklist from `2026-06-19-observer-provenance-remediation-spec.md`. **adopt the ENTRY SHAPE** from `docs/ollama.md`'s symptom→cause section. | the taxonomy STRUCTURE — one entry per shape: *symptom → what it actually is → confirm with (`serf-doctor <cmd> …` / which durable record) → mechanics link*. Raw material is fully present but scattered; nobody has assembled the recognize/diagnose table. Highest-leverage new authorship of the three "strong-source" refs. |
| `finding-contract.md` | **extract + write doctoring layer** | **extract** `docs/subagent-management/10-runtime-contracts.md` Contract 3 (the Diagnostic DTO: `source`/`kind`/`severity`/`field`/`message`; the `validation\|policy_denied\|unavailable\|timeout\|cancellation\|provider_error\|hook_blocked\|hook_failed\|transcript_unavailable` category set; secret redaction; "warnings only where displayed"; structured-fields-over-string-matching) — that is most of the schema. **cite** the Step-5 report schema in `root-cause-task-failure.md` (a worked Finding shape: DECISION POINT / REASONING / EVIDENCE). **adopt** meta-doctor `finding-contract.md`'s signature formats + routing-or-nothing rule + bucketing guidance. | the doctoring-specific layer (§6): stable signatures keyed to serf identifiers, the "healthy run emits zero findings" invariant, the `suggestedFix` *routing* (which collapses meta-doctor's PR/ticket/heal-script triage into serf's diagnose/extend/heal — with the accepted gap that meta-doctor's "repair the target system" route has no serf equivalent, §6), and the JSON envelope the `doctor` agent emits. Built ON Contract 3, not from scratch. |
| `writing-runbooks.md` | **MUST-WRITE-FRESH** (thinnest source) | **adopt** meta-doctor `writing-runbooks.md`'s skeleton + authoring checklist + key principle (HEALTHY/INSPECT/CLASSIFY; pull-live-config; parameterize-off-config; separate-violations-from-visibility; stable-signature; zero-on-healthy; registered). **extract** the escalation-ladder + rebuild-map METHOD from `docs/agentic-testing.md` (drive-the-UI → read-the-transcript → add-a-probe), stripped of stale paths/recipes. **optional seed**: observer audit families from `2026-06-18-observer-sidecar-use-cases.md` (49 cards → ~8 families). | the serf **runbook/audit-definition concept**: what a runbook IS in this skill, its schema, how a `doctor` run consumes one, registration (a `runbooks/` file the persona is pointed at — no `skills/index.ts` analogue; serf loads markdown by path). No existing serf doc describes runbook authoring; treat as primarily new (adapting meta-doctor's craft). |
| `repair-guardrails.md` | **MUST-WRITE-FRESH** (zero precedent) | **cite (boundary anchors only)** the doctoring-tools spec (`2026-06-19-serf-doctoring-tools.md`) Non-goals ("Not a mutator" / read-only forensics; mutation is the hub surface), `docs/serf-hub-remote-operations.md` (ops-repair boundary). **adopt** meta-doctor `repair-pipeline.md`'s self-consistency winner-selection (N candidates, majority ≥2 — code only) + propose/validate conservatism as the discipline for load-bearing repairs. | the entire graduated-repair model (§6). The repo has rich *diagnosis* material and essentially **zero *repair-policy* material** (everything shipped is read-only forensics), so the guardrails are net-new design. |

**Net:** `data-model` / `failure-modes` / `finding-contract` have strong existing sources (assembly + correction + a new framing layer); `writing-runbooks` / `repair-guardrails` are must-write-fresh, with `repair-guardrails` having literally no precedent.

### The `runbooks/` contract (brief; full craft in `writing-runbooks.md`)

A serf runbook is a self-contained markdown audit definition the `doctor` persona executes. It encodes the three meta-doctor questions, adapted to serf forensics:

1. **HEALTHY** — what steady state looks like (e.g. "no settled watch delivery's `Chain` contains a prior hop of the same `watch_id`; `serf-doctor watches` reports no self-loop verdict"). *Note:* a recorded delivery's `provenance.WatchKeys` **always** contains its own `(watch_id, watch_generation)` — that stamp is not the health signal (§5/§8.2); the absence of a same-`watch_id` prior hop in the `Chain` is.
2. **INSPECT** — the concrete `serf-doctor <cmd>` invocations to run (read live state first; never hardcode SIDs/thresholds — take them from the target selector / config).
3. **CLASSIFY** — which results are PASS-with-a-note vs which `emit` a Finding. A healthy run emits **zero** findings.

Registration is by path (the persona is told which `runbooks/` to run), not a code index. Seeds ship as `observer-self-loop.md` and `watch-delivery-health.md`.

---

## 4. The `doctor` agent type

A serf agent type (persona/system prompt) alongside the existing built-in agent personas.

### Persona / system-prompt shape

Structurally mirrors meta-doctor's four-section system prompt (`agentic-loop.md` §8), re-expressed for serf:

1. **Identity** — "You are the serf doctor: an on-demand forensic auditor for serf sessions, jobs, watches, and the session tree. You read canonical durable state through the `serf-doctor` tools (compiled Go that imports serf's own types/folds) and emit structured Findings."
2. **Core behavioral contract** — read `data-model.md` before reading an artifact, inspect through the `serf-doctor` tools, never hand-parse JSONL (the HARD GATE); one runbook's discipline per audit; **healthy ⇒ zero findings**; every finding actionable-or-unemitted (no FYI noise); stable signatures for dedup; structured fields over string-matching; secret redaction. (Adopts the meta-doctor behavioral contract minus the PR/ticket-routing specifics, which become the serf routing of §6.)
3. **Known gotchas** — serf-specific, the extension point: `watch_send_pending` lines coalesce (count distinct deliveries, not lines); a recorded delivery's `WatchKeys` always contains its own watch key (the delivery-time `WithWatch` stamp), so `ContainsWatch` is vacuously true on it — the post-hoc self-loop signal is the `Chain` (a same-`watch_id` prior hop, truncatable at `maxDiagnosticChain = 16`), not `ContainsWatch`/`WatchKeys`; note the Chain-hop check keys on `watch_id` while runtime suppression (`shouldSuppressWatch`) keys on `watch_id`+`watch_generation`, so a re-arm / generation bump is exactly the escape case the Chain catches; a `delegate_send` text mention is not an invocation; sub-sessions live in different buckets. Seeded from `failure-modes.md`.
4. **Runtime context** — the target selector(s), the state dir in effect, today's date. Injected at invocation.

### Its tools + skills

- **Tools**: the shell tool, used to run the `serf-doctor` subcommands (`locate`/`transcript`/`watches`/`tree`) as its primary data plane, plus serf's standard read tools (`find_session_transcripts`/`read_session_transcript`, file reads). For Extend/Heal (including repairing the doctor's own Go tools): serf's edit tools, gated by `repair-guardrails.md`.
- **Skills it carries**: `doctoring-serf` (always). Vocabulary anchors cited by the persona: `docs/tools/transcripts.md` + `agent/prompts/sections/transcripts.md` (transcript reading), `docs/hooks.md` (hook authoring), `docs/job-control.md` (status/reason vocabulary).

### Delegation

Spawnable directly and **delegatable** from any session via serf's existing delegate fabric:

- **Self-diagnosis** — a session delegates → `doctor` with its own SID; the doctor reads that session's durable transcript/jobs and reports (the durable state is already on disk, so the doctor reads settled state, not the live loop).
- **Cross-session** — diagnose another session by selector.
- **Fleet** — a coordinator delegates → `doctor` to sweep a set of sessions (each a `serf-doctor`-driven pass).

Because it is an ordinary agent type, the doctor inherits serf's spawn/observe/error-isolation for free — the collapse from §1 in practice.

---

## 5. The `serf-doctor` data plane

The standalone `serf-doctor` binary — a thin `cmd/serf-doctor` main over the focused `agent/doctor` package — invoked as `serf-doctor <subcommand>`, run by the doctor via the shell tool. Folds in the tools spec, with all §8 corrections applied. Read-only forensics over settled on-disk state. Because `agent/doctor` is under `agent/`, it `import`s serf's own canonical types and folds and answers with the same code the runtime uses — the four subcommands below are thin presenters over `jobstore`/`provenance`/`transcript`/`schema`. The cited Go symbols are the code the package *calls*, not a contract a re-parser must match.

### The drift guard: compile-time type coupling

The `agent/doctor` package does not re-parse anything — it imports the real folds and types, so a schema change either flows through automatically or fails to compile. `jobs.jsonl` coalescing is `jobstore.FoldWatchSends`; the transcript unwrap is `transcript.Entry`; the provenance predicate is `provenance.ContainsWatch` over `Causal.WatchKeys`; the effective result-tool name is `effectiveResultToolName`. There is no second implementation to drift. (Behavior tests — goldens/fixtures over sample artifacts — are still worth shipping for the tools' own presentation logic, but the compiler, not a fixture, is what keeps the data plane honest as the format moves.)

### Shared conventions

- First positional arg is a **session selector** in the form the running agent's `read_session_transcript` accepts: `""`/`current`, `local:<SID>`, `proj:<hash>:<SID>`, or a bare `<SID>` (searched across buckets; ambiguity reported with candidate refs). `serf-doctor locate` is the shared resolver the other three reuse (same in-process resolution) so there is no second selector dialect.
- `--json` emits the underlying struct as JSON for machine consumers; default is a human summary.
- `--state-dir` overrides the resolved root (mirrors `serf --state-dir`), so the tools work against an E2E scratch root. Base precedence matches serf (§8.4): `--state-dir` › `SERF_STATE_DIR` › `$XDG_STATE_HOME` › `~/.local/state`.

### 1. `serf-doctor locate <selector>`

- **Purpose**: resolve a selector to absolute on-disk paths. No more `find`.
- **Inputs**: selector; `--all-buckets` to list every bucket the SID appears in.
- **Output**: resolved `transcript_path`, `meta_path`, `jobs_path`, and `bucket_hash`. `--json` → `{transcript_ref, transcript_path, meta_path, jobs_path, bucket_hash}`.
- **CORRECTED path semantics (verified against `agent/jobs.go` + `agent/transcript_lookup.go`; `agent/doctor` builds paths with these helpers):**
  - transcript + meta are **flat, SID-prefixed** files: `<bucket>/sessions/<SID>.transcript.jsonl` (and the matching `.meta.json`) — matching `transcriptPath`'s join of `sessions/` + `<SID>.transcript.jsonl`.
  - **`jobs.jsonl` is in a per-session SUBDIR**: `jobsDir(stateDir, sid)` = `<stateDir>/sessions/<sid>`, opened as `filepath.Join(dir, "jobs.jsonl")` → **`<bucket>/sessions/<SID>/jobs.jsonl`**. `jobs_path` MUST be built as the `jobsDir` subdir form, **not** by appending `.jobs.jsonl` to the transcript path. (The tools spec's "beside the transcript" / `…/<SID>.jobs.jsonl` claim was wrong — see §8.)
- **State-dir base (CORRECTED, verified):** `--state-dir` › `SERF_STATE_DIR` env › `$XDG_STATE_HOME` › `~/.local/state` (the precedence `cmdutil/statedir.go` resolves at the cmd layer; `agent/doctor` takes the resolved root). `SERF_STATE_HOME` **does not exist** (read nowhere). The bucket is `hexHash(key)` where `key := originURL; if key == "" { key = workDir }` (one OR the other, not concatenated) and `hexHash = hex.EncodeToString(sha256(key)[:8])` = **16 hex chars**.
- **Resolve, don't recompute.** The locator resolves via selector parsing + a cross-bucket glob of `<root>/*/sessions/`; it must **not** recompute the bucket hash itself (a running session already knows its bucket; recomputing would mean reproducing the exact origin/cwd — resolving by glob avoids that whole failure mode).
- **Uses**: the `resolveTranscript` / `transcriptPath` / `enumerateBuckets` / `jobsDir` path logic directly.

### 2. `serf-doctor transcript <selector>`

- **Purpose**: render a session's logical turns instead of raw JSONL, and answer "how many real X calls" structurally. The structural counting is the load-bearing capability; full conversation rendering is secondary (the agent also has `read_session_transcript`).
- **Inputs**: selector; `--range last:N|start:N|A-B` (mirroring serf's `parseRange` grammar); `--format outline|markdown` (default `markdown`); `--count <tool>` to print the structural invocation count of a tool name and exit.
- **Output (default)**: a conversation-grouped render — tool calls condensed into ID-paired cards, the result tool surfaced as assistant text, results head+tail truncated — with the honest provenance footer (`turns_total`/`turns_rendered`/`elided`). `--format outline` prints the turn map. `--count delegate_send` prints e.g. `delegate_send: 0 calls (5 textual mentions in api_call lines / instructions)` — the exact disambiguation the pain needed.
- **Counting predicate (verified)**: walk `entry.Turn.Message.Content` for parts whose kind is `ContentToolCall` with a non-nil tool-call whose `Name == <tool>` — the `.Name ==` comparison in `writeAssistantContent` (note: `toolCallIDs` does **not** filter by name; it collects *all* tool-call IDs by ID, so it is the wrong symbol for this predicate). For the "mentions" number, count substring hits in the **in-transcript `api_call` lines** (`transcript.APICall`, which carry the request payload) and in assistant text separately so the two are never conflated. The effective result-tool name follows `effectiveResultToolName`'s order (`opt.resultToolName` → `meta.Config.ResultToolName` → `"communicate"`), resolved from `meta.json` rather than hard-coded, so `--count` on an aliased result tool still counts correctly.
- **`api_call` is in the transcript, not `api.jsonl`.** The "mentions" scan reads the in-transcript `api_call` lines (`transcript.APICall`, kind `api_call`), **not** the separate per-LLM-call latency log at `<state-dir>/api.jsonl` (`llm.APILogger`/`APILogEntry`) — that file is a different source (latency/duration), out of scope for `--count`.
- **Turn-kind facts (verified, for `--format outline` legibility):** `USER_INPUT`, `STEERING`, `ASSISTANT`, `TOOL_RESULTS` (current aggregated-results kind), `SUMMARY`; **`TOOL` is the deprecated kind** (`TurnTool` is marked Deprecated; `TurnToolResults` is current). Results pair to calls **by `ToolResult.ToolCallID`, never by adjacency**.
- **Uses**: the `transcript.Entry{Kind,Seq,Turn}` unwrap, the content-part walk above, and the effective-result-tool resolution. Presentation (the conversation/outline render) is `agent/doctor`'s own logic over the unwrapped turns — keep it lean: the structural counts and the turn map are the value, not a byte-faithful reproduction of serf's interactive renderers.

### 3. `serf-doctor watches <selector>`

- **Purpose**: the watch/job inspector. Collapse coalescing, show distinct deliveries with provenance + lifecycle, flag self-loops. This is the highest-leverage subcommand — it produced the *wrong numbers* this session.
- **Inputs**: selector; `--watch <watch_id>` to scope; `--self-loops` to print only deliveries whose provenance implicates the delivery target.
- **Output (default), per watch (folding the `WatchRecord` event log with `FoldWatches`):**
  - registration off `WatchRecord` (via `FoldWatches`): `WatchID`, `Generation`, `OwnerSessionID`, `VisibleSessionID`, `Target`, `SendTo`, `Condition`, `Active`, `EndReason`.
  - **distinct deliveries** — the count of *settled* `WatchSendKey`s (delivered/dropped/**evicted** — four terminals counting pending; **`evicted` is a real fourth terminal**, `EventWatchSendEvicted = "watch_send_evicted"`), **NOT** the raw `watch_send_pending` line count — with a one-line note when they differ (`8 pending lines → 4 deliveries (latest-wins coalescing — expected)`).
  - per settled delivery: `DeliveryID`, `TriggerIdentity`/`TriggerReason`, terminal kind + `DiagnosticReason` if dropped, `CoalescedCount`, and the provenance — read from the raw `watch_send_delivered`/`watch_send_dropped`/`watch_send_evicted` events (see the counting note below), not from `FoldWatchSends`.
  - **self-loop verdict (CORRECTED — see §8.2):** A recorded delivery's `WatchSendState.Provenance` **always** contains its own `(watch_id, watch_generation)`: `watchSendSnapshot` stamps it via `WithWatch(triggering_provenance, watch_id, generation, …)` at delivery time. So `ContainsWatch` over a *recorded* delivery's `WatchKeys` is **vacuously true for every delivery** and cannot be the verdict. `ContainsWatch`/`WatchKeys` is the **runtime suppression** predicate (`shouldSuppressWatch`), applied to an *incoming event's* provenance pre-stamp — if the triggering event already carries the key, the delivery is suppressed (`shouldSuppressWatch` → `ContainsWatch`) and **never recorded**. Hence a healthy watch records **zero** self-loop deliveries by construction. The forensic verdict is the diagnostic **`Chain`**: a delivery whose hop trail contains a **prior hop of the same `watch_id`** was caused (transitively) by an earlier delivery of this watch — a loop. Honest caveat: `Chain` is truncatable (`maxDiagnosticChain = 16`, `ChainTruncated` when over), so a deep loop can lose middle hops — a positive signal, not a completeness guarantee. A real nuance falls out of this: the Chain-hop check keys on **`watch_id`** alone, while suppression keys on **`watch_id`+`watch_generation`** — so a re-arm / generation bump (which escapes suppression, because `ContainsWatch` requires both fields to match) is precisely the loop the `Chain` still catches. `watches` reads the verdict off the `Chain` (same-`watch_id` prior hop) and presents `WatchKeys`/`ContainsWatch` only as the (always-present) delivery stamp, never as the discriminator. (This is the evidence the observer e2e relied on: no same-`watch_id` prior hop in any chain.)
- **Distinct-delivery counting + settled-delivery payloads (verified)**: count distinct deliveries with `FoldWatchSends`' settle rule — latest-wins coalescing keyed on `WatchSendKey` + `UpdateSeq`, terminal tracking (`terminalSeq` per key). But note `FoldWatchSends` returns only the **pending** `WatchSendState`s (its `WatchSendRecord.Pending`) and *discards* terminal payloads (it tracks terminals only in a local `terminalSeq` to evict pending). So to list settled deliveries with their terminal kind + `DiagnosticReason` + provenance, `watches` **scans the raw `watch_send_delivered`/`watch_send_dropped`/`watch_send_evicted` events directly** (and uses `FoldWatches` for the watch registration). `WatchSendKey` = `{VisibleSessionID, WatchID, WatchTarget, ResolvedWatchedIdentity, ResolvedSendTo, WatchGeneration}` (§8.6). Provenance rides `WatchSendState.Provenance` (a `*provenance.Causal` on disk).
- **Uses**: `FoldWatches` (registration) + a raw scan of the settled `watch_send_*` events (terminal payloads + provenance) + `FoldWatchSends` (the distinct-count settle rule), all over `jobs.jsonl`; the self-loop verdict is a `Chain` walk for a same-`watch_id` prior hop (the recorded `WatchKeys`/`ContainsWatch` stamp is shown but is not the discriminator, §8.2). This is the subcommand whose behavior tests matter most: a golden exercises the pending→delivered coalescing collapse, a `dropped`/`evicted` terminal, and a self-loop `Chain`.

### 4. `serf-doctor tree <selector>`

- **Purpose**: the session-tree view — parent ↔ delegates/observers across buckets.
- **Inputs**: selector (any node); `--depth N`; `--observers` to include observer edges, not just delegate edges.
- **Output**: an indented tree; each node `SID (agent_type) status → transcript_ref`. Delegate edges from `DelegateRecord` (via `FoldDelegates`); observer edges from the worker's `SessionMeta.ObservedBy[]` (across `meta.json` files, as `ListSessionMetas` enumerates) and the watch-grant fold (`FoldGrants`: observer-session → watched-job). Each child reached via its `transcript_ref` so you can pivot straight into `serf-doctor transcript <ref>`.
- **Uses**: the `FoldDelegates` / `FoldGrants` folds over `jobs.jsonl`, the `ObservedBy[]` read across `meta.json` files (`ListSessionMetas`), and the same cross-bucket enumeration `serf-doctor locate` provides.

### Output discipline

Summary-by-default, detail-on-demand (serf's automation philosophy): summaries print headers + distinct counts + verdicts, not every coalesced frame; `serf-doctor transcript` applies its own render budgets (it cannot dump a 10k-line transcript by accident); `--watch`/`--range`/`--format`/`--depth`/`--self-loops` narrow; `--json` emits the struct; every elision is reported (the `turns_rendered + elided == turns_total` invariant; the pending-lines-vs-distinct-deliveries note). A dropped delivery and a coalesced one are visibly different.

### Non-goals

Not a live TUI/monitor (these read settled state; live observation is appwire + tui + hub). Not a replacement for the appwire projection (different source: durable transcript/jobs vs the live `events.SessionEvent` stream). Not test infrastructure (no assertion DSL; the inspectors *expose numbers* that `test/scenarios` assert with `jq`/grep). Not a mutator (read-only; mutation is the hub REST surface). Not a hub client (works directly against the state dir, with the hub down, against an E2E scratch root).

---

## 6. The Finding contract + runbook contract + graduated repair guardrails

### Finding contract (serf-adapted from meta-doctor)

A Finding is the atomic output of a `doctor` audit — structured, emitted as JSON. Built on meta-doctor's `finding-contract.md` and `subagent-management/10`'s Contract 3.

| Field | Type | Req | Notes |
|---|---|---|---|
| `signature` | string | yes | **stable dedup key** — deterministic for a given root cause across runs (see formats below). |
| `severity` | `low`\|`medium`\|`high` | yes | impact level. |
| `category` | enum | yes | serf-adapted category bucket (below). |
| `title` | string | yes | one-line label. |
| `description` | string | yes | what was observed + why it is a problem. |
| `evidence` | object | yes | at least one sub-field populated: `sessionRefs[]`, `watchIds[]`, `deliveryIds[]`, `transcriptTurns[]`, `doctorCommand` (the `serf-doctor <cmd> …` invocation that surfaces it), `logSnippets[]`. |
| `suggestedFix` | object | yes | the **routing** directive (below). |

- **Category set** — adopt Contract 3's diagnostic categories where they apply (`validation`, `policy_denied`, `unavailable`, `timeout`, `cancellation`, `provider_error`, `hook_blocked`, `hook_failed`, `transcript_unavailable`) plus the forensic shapes the taxonomy adds (`watch_self_loop`, `dropped_delivery`, `provenance_gap`, `stuck_processing`, `orphaned_runtime`). The category routes and groups.
- **Signature formats** (adopt meta-doctor, re-keyed to serf identifiers): structural defect → `{shape}:{sessionID}:{watchID|turn}`; recurring audit finding → `{runbook}:{category}:{bucket}` (ISO week/date/month). When unsure, **bucket broader** — false dedups are cheaper than duplicate findings.
- **`suggestedFix` routes** (this is where meta-doctor's PR/ticket/heal-script triage collapses into serf's three capabilities): `type: "diagnosis"` (report-only — the durable record explains it, no change), `type: "runbook"` (propose/extend a runbook to catch this class henceforth — the Extend capability), `type: "skill"` (propose a core-skill/persona/doctor-tool repair — the Heal capability, gated). `fileHint`/`symbolHint` optional.
- **Accepted scope limit (not a clean equivalence).** meta-doctor has a fourth route the collapse silently drops: "repair the **target system** under audit" (its `code` route opens a PR against the audited repo). serf's doctor has no such route — a finding that's a genuine bug in **serf itself** can only route to `diagnosis` (report-only), because the doctor's Heal authority covers its *own* machinery (runbooks / its Go tools / core skills), not serf's product code. That is an honest limit of an on-demand forensic doctor, not an equivalence.
- **Healthy run emits ZERO findings.** No PASS findings, no informational/visibility metrics, no clean-scorecard findings. Every finding is actionable or it is not emitted (no FYI noise). When in doubt, do not emit. (Adopted verbatim in spirit from meta-doctor's emit/don't-emit rule.)
- **Dedup**: by `signature` (stem-matched), applied per `suggestedFix.type`; within a single run, the first occurrence of a signature stem wins.
- **Structured fields over string-matching; secret redaction** (from Contract 3): consumers key on `category`/`signature`, never on parsing `description`; evidence is redacted of secrets.

### Runbook contract

(Summarized in §3; full craft in `writing-runbooks.md`.) HEALTHY / INSPECT / CLASSIFY; pull live state first (never hardcode SIDs or thresholds); parameterize off the target; separate violations from visibility; stable signature per `emit`; **healthy ⇒ zero findings**; registered by path. A runbook that fires on every healthy run is miscalibrated, not thorough.

### Graduated repair guardrails (by blast radius)

The doctor is itself made of skills, a persona, and its own Go tools — repairing them is a **bootstrapping hazard**. So repair authority is graduated by blast radius, adopting meta-doctor's propose/validate conservatism and self-consistency:

| Capability | Blast radius | Authority |
|---|---|---|
| **Diagnose** (run runbooks → findings) | read-only | **autonomous.** No mutation; emits findings. |
| **Extend** (author a new runbook / edit an audit definition) | adds/changes a *runbook*, not the doctor's own machinery | **autonomous** authoring permitted (this is also how the corpus stays evergreen), subject to the runbook contract (zero-on-healthy, stable signature). |
| **Heal — runbook repair** (fix an existing runbook) | a runbook | **propose + light validation.** Re-run the runbook against a known-healthy and a known-broken target; the repair must produce zero findings on healthy and catch the broken case before it lands. |
| **Heal — core-skill / doctor-tool repair** (the `doctoring-serf` skill itself, the persona, a reference, **or the `serf-doctor` Go tools** in serf's tree) | the doctor's own foundation — its knowledge, persona, and data plane | **propose-only + review + a validation gate — NEVER silently applied.** A human (or an explicitly-authorized higher-authority agent) approves before it is written. For a **Go tool** repair (the `agent/doctor` package + the `cmd/serf-doctor` main): use self-consistency — generate N candidate edits and require a majority (≥2 agreeing by byte-identical normalized source, per meta-doctor's winner-selection) before the proposal is even surfaced — then the validation gate is concrete: it must compile, `go test` (the tools' goldens) must pass, and `data-model.md` is updated in the same change so the conceptual reference tracks any format note. For a **prose** repair (a reference, the persona, the skill): the ≥2-agree byte-majority does **not** apply (independent prose rewrites are essentially never byte-identical) — these go through review + the consult/validation gate instead, not byte-majority. |

The rule: **diagnosis and runbook authoring may be autonomous; runbook repair is propose-plus-validate; core-skill and doctor-tool repair are propose-only behind a review + validation gate.** This mirrors meta-doctor's "every load-bearing patch goes through N-candidate self-consistency and is validated before it lands," tightened at the core-skill/doctor-tool tier because that tier — including the Go tools the data plane is built from — can corrupt the doctor itself. The byte-majority self-consistency is meta-doctor's winner-selection (`selectWinner`, which groups by normalized source), so it bites on **code** (the Go tools); prose repairs use review, not the vote.

---

## 7. Phased build plan

### First cut (the high-leverage minimum)

1. **The `serf-doctor` binary — `watches` + `transcript` (incl. `--count`) + `locate`** — the data plane that produced the *wrong numbers* (coalescing collapse + provenance/self-loop verdict; structural tool-call counting; the `find`-tax killer). Build the forensic logic as the `agent/doctor` package (under `agent/`, so it can import `internal/jobstore`) with a thin `cmd/serf-doctor` main over it (parallel to `cmd/serf-hub`/`cmd/serf-tui`). Order: `locate` first (the shared resolver the other two reuse, and trivial — selector parse + cross-bucket glob), then `watches` (riskiest by eye — the `FoldWatchSends` coalescing collapse, the raw settled-event scan for terminal payloads, and the `Chain` self-loop verdict, the part that was *wrong*), then `transcript --count` (the `writeAssistantContent` name-predicate + effective-result-tool resolution; full rendering can stay minimal). The tools import the real folds/types — no re-parser to drift.
2. **`data-model.md` + `finding-contract.md`** — the two references with the strongest sources and the highest leverage: the conceptual data model the ad-hoc parser lacked (and which the tools' cited Go types are the source of truth for — assembly + §8 corrections) and the serf-adapted Finding schema (Contract 3 + the doctoring layer). Ship them together, each referencing the other; `data-model.md` is the conceptual reference the consult-don't-guess gate points at.
3. **1–2 seed runbooks** — `observer-self-loop.md` and `watch-delivery-health.md`, exercising `serf-doctor watches` end-to-end and proving the runbook contract.
4. **The `doctor` agent type** + a small **`SKILL.md`** — the persona that loads the skill and runs the `serf-doctor` tools, with the always-loaded loop/gate (consult `data-model.md` + run `serf-doctor`)/subcommand-list/pull-index. This makes diagnose-by-delegation real.

### Later

5. **`serf-doctor tree`** — cross-session linking; valuable but it was annoying, not *wrong*-number-producing. Build once 1–4 prove the binary's shape (selector resolution + JSONL fold + golden).
6. **`failure-modes.md`** — the full taxonomy (the new-authorship reference); seed the persona's "known gotchas" from it as it lands.
7. **`writing-runbooks.md`** — the runbook-authoring craft reference (must-write-fresh), once the seed runbooks have shaken out the schema.
8. **`repair-guardrails.md` + the Heal/Extend capabilities** — the repair phase (this is where the doctor gains authority to maintain its own runbooks, references, and Go tools). Author the guardrails reference first (zero precedent), then wire Extend (autonomous runbook authoring) and Heal (propose+validate runbook repair; propose-only gated core-skill/doctor-tool repair — Go-tool repairs compile-and-test-gated, prose repairs review-gated). This is deliberately last: diagnosis is the value, repair is the hazard.

---

## 8. Corrections to prior specs (verified against code)

Every load-bearing structural fact below was re-verified against the Go source this session (symbols, not `file:line`). Three separate **false** "this symbol/path doesn't exist" grep-claims were produced earlier in the session; these are the corrected, code-verified facts.

1. **`jobs.jsonl` path was WRONG in the tools spec.** It claimed jobs.jsonl "lives beside the transcript" at `…/sessions/<SID>.jobs.jsonl`. **Verified against `agent/jobs.go`:** `jobsDir(stateDir, sid)` = `<stateDir>/sessions/<sid>` (a **subdir**), and `jobstore.Open(filepath.Join(dir, "jobs.jsonl"))` → **`<bucket>/sessions/<SID>/jobs.jsonl`**. Transcript/meta ARE flat SID-prefixed files (`transcriptPath` → `<bucket>/sessions/<SID>.transcript.jsonl`). `serf-doctor locate`'s `jobs_path` must build the `jobsDir` subdir form, not suffix the transcript path; `serf-doctor watches` reads jobs from that subdir. (The scenario files that `find … -path "*sessions/$SID/jobs.jsonl"` were right all along.)

2. **Provenance type names — use the shipped package, not the 06-18 design names.** The design doc's `CausalProvenance` / `WatchProvenanceKey` / `ProvenanceEntry` **do not exist** (verified absent). The shipped package `agent/provenance` has `Causal{WatchKeys []WatchKey, Chain []Entry, ChainTruncated bool}`, `WatchKey{WatchID, WatchGeneration}`, `Entry{Kind, WatchID, WatchGeneration, DeliveryID, SessionID, JobID}`, and `ContainsWatch(p, watchID, watchGeneration) bool`. The **load-bearing** structure for *runtime suppression* is the deduped `WatchKeys` set + `ContainsWatch`; the `Chain` is **diagnostic-only and truncatable** (`maxDiagnosticChain = 16`; truncation drops `Chain` entries but never `WatchKeys`). **Self-loop verdict (re-corrected):** `ContainsWatch`/`WatchKeys` is the *runtime suppression* predicate — `shouldSuppressWatch(cfg, p)` → `ContainsWatch(p, cfg.watchID, cfg.generation)`, applied to an **incoming event's** provenance pre-stamp (in `onSessionEvent`/`feedJobOutput`) — not a post-hoc verdict on a *recorded* delivery, whose `WatchKeys` always contains the watch's own key (the `watchSendSnapshot` → `WithWatch` stamp at delivery) and is therefore vacuously `ContainsWatch`-true. Because suppression drops self-loop triggers before they become deliveries, a healthy watch records **zero** self-loop deliveries. The post-hoc forensic verdict is the diagnostic `Chain`: a same-`watch_id` prior hop = a loop, with the `maxDiagnosticChain` truncation caveat — **not** `ContainsWatch` over the recorded `WatchKeys`. There is **no** independent "count of legitimate external triggers" to compare against: the triggering `events.SessionEvent`s are ephemeral and never persisted (there is no durable event log, and for `output_match`/progress-tick watches there is no per-trigger record at all), so a "distinct deliveries ≤ external triggers" count invariant is **uncomputable from durable state** and is dropped. Nuance worth recording: the Chain-hop check keys on `watch_id` only, while suppression keys on `watch_id`+`watch_generation`, so a re-arm / generation bump escapes suppression but is exactly the loop the `Chain` still catches. (The unified-spec draft's "use `WatchKeys`, not `Chain`" framing over-corrected: `WatchKeys` is load-bearing for *runtime suppression*, but for *post-hoc self-loop forensics* the `Chain` is the usable signal — the tools spec's original "Chain is the self-loop read" was closer to right; it just lacked the truncation caveat.)

3. **Transcript grammar (verified `agent/transcript/transcript.go` + `agent/schema/turn.go`).** Each on-disk line wraps the turn in `transcript.Entry{Kind:"entry", Seq, Turn schema.Turn}` (header line `Kind:"header"`, api-call lines `Kind:"api_call"`). A tool call is a content part at `entry.Turn.Message.Content[]` with `Kind == llm.ContentToolCall` and the name at `ToolCall.Name` (the result-tool call's name == the session's effective result-tool name). The **deprecated** turn kind is `TOOL` (`TurnTool`, marked Deprecated); the current aggregated-results kind is `TOOL_RESULTS` (`TurnToolResults`). Also note the user-input kind constant is `USER_INPUT` (`TurnUserInput`), not `USER`. Results pair to calls by `ToolResult.ToolCallID`, never by adjacency.

4. **State-dir resolution (verified `cmdutil/statedir.go` + `agent/runtime_dir.go`).** `SERF_STATE_HOME` **does not exist** (read nowhere; the tools spec invented it). Real precedence: `--state-dir` flag › `SERF_STATE_DIR` env › `$XDG_STATE_HOME` › `~/.local/state`, with `SERF_STATE_DIR`+the flag resolved at the **cmd layer** and passed down; `RuntimeDirWithStateHome` knows only `XDG_STATE_HOME` (plus an explicit override). The bucket is `hexHash(key)` where `key := originURL; if key == "" { key = workDir }` (one OR the other, **not** a concatenation — the spec's `sha256(originURL|workDir)` shorthand reads as concat), and `hexHash = hex.EncodeToString(sha256(key)[:8])` = **16 hex chars**.

5. **Watch-send terminals — `evicted` is a real fourth kind (verified `agent/internal/jobstore/event.go`).** `watch_send_pending` (not terminal) + `watch_send_delivered` + `watch_send_dropped` + `watch_send_evicted` (`EventWatchSendEvicted`). Distinct deliveries mirror `FoldWatchSends` (latest-wins coalescing keyed on `WatchSendKey` + `UpdateSeq`, with `terminalSeq` per key), **not** a count of `watch_send_pending` lines.

6. **The fold semantics the `agent/doctor` package calls are CORRECT (recorded so they are not re-flagged).** Re-verified `agent/internal/jobstore` (`agent/doctor` `import`s and calls these — a package under `agent/` can reach `internal/jobstore`): `Fold` (returns `map[string]*JobRecord`), `FoldOrdered`, `FoldWatches`, `FoldWatchSends`, `FoldDelegates`, `FoldGrants` and the matching `Store.Load`/`LoadOrdered`/`LoadWatches`/`LoadWatchSends`/`LoadDelegates`/`LoadGrants` all exist; `WatchSendState.Provenance` is a `*provenance.Causal`; `WatchSendKey` = `{VisibleSessionID, WatchID, WatchTarget, ResolvedWatchedIdentity, ResolvedSendTo, WatchGeneration}`. No missing/renamed fold. **One fold subtlety the `watches` tool must respect (verified):** `FoldWatchSends` surfaces only **pending** sends — its result carries `WatchSendRecord.Pending` and *discards* terminal (`delivered`/`dropped`/`evicted`) payloads, tracking terminals only in a local `terminalSeq` to evict pending. So `FoldWatchSends` gives the distinct-delivery *settle rule* (latest-wins keyed on `WatchSendKey`+`UpdateSeq`) but **not** the settled deliveries themselves; to list those with terminal kind + `DiagnosticReason` + provenance, `agent/doctor` scans the raw `watch_send_delivered`/`watch_send_dropped`/`watch_send_evicted` events (and uses `FoldWatches` for registration). These are the behaviors `serf-doctor watches`/`tree` are built on, and what the goldens cover.

7. **The `internal` wall is REAL and load-bearing — it is the architectural spine (verified).** `agent/internal/jobstore` is an `internal/` package; no file under `cmd/` or `server/` imports it (verified: only package `agent` and its subpackages reach it — e.g. `agent/jobs.go` imports it; `cmd/serf` and `server/` do not). Go's internal-package rule means `cmd/serf-doctor` (rooted at the module root under `cmd/`, like `cmd/serf` — a sibling of `agent/`, not under it) **cannot** import `jobstore`, while a package under `agent/` (the new `agent/doctor` package) **can**. That boundary is exactly what *forces* the exported `agent/doctor` package as the facade and **structurally forbids an external re-parser**: any tool that wants the real folds must live inside `agent/`, and the thin `cmd/serf-doctor` main reaches them only through that exported package. This is the spine of the design — the compiler will not let the data plane be anything other than serf's own folds behind an `agent/` package.

8. **No `cmd/serf` doctor dispatch + result-tool resolution (verified).** `serf-doctor` is its own binary, so it does **not** slot into `cmd/serf`'s `dispatchCLICommand` — there is no `doctor` case there. (Verified: `cmd/serf`'s `dispatchCLICommand` exists with current cases `serve`/`launch-check`/`openai` only; `serf-doctor` routes through its own `cmd/serf-doctor` main into the `agent/doctor` package, parallel to how `cmd/serf-hub`/`cmd/serf-tui` are their own binaries, not `serf` subcommands.) The effective result-tool name follows `effectiveResultToolName`'s order (`opt.resultToolName` → `meta.Config.ResultToolName` → `"communicate"`), backed by `SessionConfig.ResultToolName` — so `serf-doctor transcript --count` resolves the effective name from `meta.json` (by calling `effectiveResultToolName`), never hard-codes `"communicate"`.
