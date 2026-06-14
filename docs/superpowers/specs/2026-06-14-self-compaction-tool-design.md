# Self-Compaction Tool Design

An agent-invoked `compact` tool that lets serf compact its own context **by choice**
at a clean task boundary — supplying the keep/clear judgment itself — layered over
the existing automatic compactor as a safety net.

Prior-art research that motivates this design lives in
[`docs/design/2026-06-14-self-compaction-prior-art.md`](../../design/2026-06-14-self-compaction-prior-art.md).
The subsystem it builds on is documented in
[`docs/design/context.md`](../../design/context.md).

> This spec was revised after an adversarial design review. The review findings and how
> each was resolved are recorded in [Appendix A](#appendix-a-review-findings-addressed).

## Problem

serf already auto-compacts on context pressure (default `compact` strategy:
deterministic checkpoint at 0.80, cheap-model narrative summary at 0.90). The summary
is authored *post-hoc by a cheap model* that did not do the work. This produces the
well-documented failure modes catalogued in the prior-art note: **brevity bias**
(domain insight dropped for concision), **structured-detail loss** (decisions, code
snippets, root causes flattened into prose), and **context collapse** (iterative
rewriting erodes detail over successive compactions).

The agent that did the work is the best-informed party about what matters, when a
clean seam has been reached, and what is safe to drop. Today it has no way to express
any of that. We want to give it one.

## Goals

- **Quality-led (primary):** let the agent author what to keep, so the highest-value
  detail survives compaction verbatim instead of being re-summarized by a cheap model.
- **Seam timing (secondary):** the natural occasion to do this is a clean stopping
  point between tasks, before the context is polluted by the next task.
- Keep the proven automatic compactor as a hard fallback — research found **no
  evidence** that agents self-trigger compaction at good moments, so we must not bet
  the session on the agent's timing.

## Non-goals

- **Not** replacing the automatic compactor. It stays as the fallback.
- **Not** selective mid-history purging. Dropping specific old turns while keeping
  others reintroduces the prompt-cache busting serf deliberately removed. Compaction
  remains whole-prefix replacement.
- **Not** a bet on agent timing calibration. The warning nudge and the raised
  auto-fallback together cover both "compacts too rarely" and "never compacts."

## Design principle: a strategy-independent capability

The compaction *strategy* (`compact`, `recursive-distill`, `obs-mask`, …) owns **how
content is processed at compaction time**. The self-compaction capability is
orthogonal to that and must work with *any* strategy.

The strategy-independent seam is **`Manager.ForceCompact`** — a `Manager` method, so it
is reachable no matter which `Strategy` is active and no matter whether that strategy
routes through `Manager.MaybeCompact` or inlines its own layers. (Four of serf's seven
strategies — `obs-mask`, `session-log`, `ooda`, `checkpoint-pred` — do **not** call
`MaybeCompact`, so a force-flag consumed inside `MaybeCompact` would be a no-op for them;
the `Manager` seam avoids that trap.) Therefore:

- The **trigger** (the tool), the **pinned note**, and the **warning nudge** live at the
  **Session** level and are strategy-independent.
- Agent-triggered compaction is performed by the Session calling **`Manager.ForceCompact`**
  directly, passing the agent's `compaction_instructions` as a call parameter.
- `Manager.ForceCompact` already runs the summarize layer **unconditionally** (it is not
  threshold-gated, unlike `MaybeCompact`), so the agent's instructions are **always**
  consumed — even when the agent compacts at a low-pressure seam where the automatic path
  would have run only the deterministic checkpoint.

**One accepted tradeoff.** `Manager.ForceCompact` runs the *standard* checkpoint +
summarize layers, not a non-default strategy's bespoke layers. For the default `compact`
strategy (what real sessions use) this is identical to its automatic path. For the
*experimental* strategies, agent-triggered compaction will use the standard layers rather
than, say, `recursive-distill`'s micro/macro hierarchy. This is acceptable while those
strategies are experimental; revisit by adding a `Strategy.ForceCompact` interface method
if one graduates to default.

```
 Agent calls compact ─────────────────────────────────┐  (during a tool round)
                                                        ▼
 Session (strategy-independent):  set pinnedNote + instructions + forceRequested  [s.mu]
                                                        │
                         end of tool round ────────────┤  (same activation — not deferred)
                                                        ▼
        Manager.ForceCompact(history, instructions)  ── always checkpoint + summarize
                                                        │   (instructions threaded into
                                                        ▼    summarizeWithLLM)
        re-stamp pinned note as a preserved artifact (ordered before the goal objective)

 Automatic path (unchanged):  prepareModelRequest → Strategy.ManageContext → MaybeCompact
        when a compaction occurs there too, the same note re-stamp runs.
```

## Components

### 1. The `compact` core tool

Registered in `registerCoreTools` (`agent/session_tool_registry.go`) — **not** via
`Strategy.Tools()` — so it is present in every session regardless of the active strategy.
It is the agent-facing sibling of the existing user `/compact` (`Session.Compact` →
`Manager.ForceCompact`) and shares its compaction path.

**Reaching Session state.** A tool `Exec` has signature
`func(ctx, env execenv.ExecutionEnvironment, args map[string]any) (any, error)` — it gets
**no** `*Session` or `*Manager` handle. Like `communicate` (which writes session state via
the injected `setCommunicateResult` closure, `session_tool_registry.go`), `compact` reaches
Session state through new `toolDeps` forwarder closures — `setPinnedNote(string)` and
`requestForceCompact(instructions string)` — captured at registration. These forward to
Session writer methods that take `s.mu`.

**Arguments** (both required, both non-empty):

| Arg | Purpose |
|-----|---------|
| `note_to_self` | Durable text preserved **verbatim** across compactions — the agent's own structured keep (decisions made, the API signature settled on, the failing tests, the next steps). Defeats brevity bias because the summarizer never authors it. |
| `compaction_instructions` | Guidance to the summarizer about what to **preserve** and what is **safe to drop** when condensing everything else ("keep the migration plan steps verbatim; the vendored file dumps are irrelevant, drop them"). |

**Exec behavior:** validate both args non-empty (clear error otherwise); call
`setPinnedNote(note_to_self)` (replacing any prior note) and
`requestForceCompact(compaction_instructions)`; return a confirmation string. The tool does
**not** mutate history itself.

**When the compaction happens.** The forced compaction is applied at the **tail of the
tool round in which `compact` was called** — same agent activation, immediately after the
round's tool results are appended (the seam already used for post-round work like the
content-filter pass in `session_model_call.go`). This avoids deferring to the next
`prepareModelRequest`, which — if the agent ends its turn right after calling `compact` —
would otherwise fire at the *next user turn*, compacting before the user's new message is
read.

**Tool description (the agent's "when" guidance):** instruct the agent to call it at a
clean stopping point — between tasks/subtasks, after extracting results from a large
context, before consuming substantial new input, before a complex multi-step operation —
and explain what belongs in each argument. In **non-persistent** sessions (see
Recoverability) the description must warn that dropped detail is **not** recoverable, so
`note_to_self` is the only durable carry.

### 2. Pinned note (Session state; re-stamped at every compaction)

A single `pinnedNote string` slot on the Session, guarded by `s.mu`. Replaced on each
`compact` call (the "rewrite" model — the agent carries forward what still matters).

**How it survives — correctly stated.** The note survives **because the Session re-stamps a
fresh verbatim copy from `pinnedNote` at every compaction**, *not* because it incidentally
sits in the preserved-recent window. (With `PreserveRecentTurns = 6`, after several rounds
the note can drift *before* the compaction cutoff and be folded into the summary; relying on
"it's a tail turn" is wrong — `summarizeWithLLM` would emit a drifted note as `System: …`.)
Re-stamping from stored state makes any folded stale copy harmless: it is summarized away
while the canonical copy is re-laid intact.

**Re-stamp only at compaction time — never per turn.** The re-stamp runs **only when a
compaction actually occurs** this round (detected via the `EventContextCompaction` emit
through the existing `compactionEmitFunc` wrapper; `ManageContext` itself returns only
`error`). Between compactions, history is left untouched. This matters for the prompt cache:
removing-and-re-appending the note every turn would mutate mid-history and break the prefix
cache for all recent turns — exactly the regression that got observation-masking removed
(`context.md`). At compaction time the whole prefix is being replaced anyway, so re-laying
the note adds no incremental cache cost.

**Ordering vs. the goal objective.** `runPreCompactHook` appends the active goal objective
as the **trailing** steering turn during compaction, and `session_goal.go` documents that
the trailing (strongest-recency) position is load-bearing. The note re-stamp is folded into
that same preserved-artifact path and laid **before** the objective, so the objective
remains trailing-most.

Format: a marked block, e.g. `[NOTE TO SELF]\n…\n[END NOTE TO SELF]`, analogous to the
existing `[DISTILLED MEMORY]` and `[CONTEXT CHECKPOINT]` markers.

### 3. `compaction_instructions` hand-off

The Session passes the agent's instructions **as a call parameter** to
`Manager.ForceCompact(ctx, history, instructions, emitFn)`, which forwards them to
`summarizeWithLLM`. There is **no new mutable instruction state on the `Manager`** — the
string lives on the Session (under `s.mu`) until consumed by the single `ForceCompact` call,
then is cleared. This avoids the hazard of instructions lingering on a shared `Manager` and
leaking into a later, unrelated automatic compaction.

**Placement and precedence in the summarizer prompt.** `summarizeWithLLM`'s fixed prefix
currently instructs the cheap model to "be thorough… include too much"
(`context_manager.go`). The agent's `compaction_instructions` are injected as a
**higher-precedence** block that explicitly overrides the default verbosity directive — e.g.
prefixed with "These caller instructions take precedence over the general guidance below:".
The design's quality thesis depends on the cheap model *obeying* this, which is an
assumption we must test for real (see Testing), not merely assert that the prompt contains
the string.

### 4. Force path (the strategy-independent seam)

1. Agent calls `compact` → Exec sets Session `pinnedNote`, `pendingInstructions`,
   `forceRequested` (all under `s.mu`); returns confirmation.
2. At the end of that tool round, the Session observes `forceRequested` and calls
   `Manager.ForceCompact(ctx, &history, pendingInstructions, emitFn)`, then re-stamps the
   pinned note, then clears `forceRequested`/`pendingInstructions`.
3. `ForceCompact` runs the deterministic checkpoint **and** the LLM summary
   unconditionally; the summary honors the instructions (§3).
4. The next LLM request sees: `[checkpoint + instruction-honoring summary]` + recent turns +
   `[NOTE TO SELF]` + `[goal objective]`.

`MaybeCompact` is **not** modified and carries **no** force flag. The automatic path
(`Strategy.ManageContext` → `MaybeCompact`) is unchanged except that, when it compacts, it
triggers the same note re-stamp (§2).

### 5. Warning nudge (Session-level; strategy-independent)

When context pressure crosses `WarnThreshold` and the agent has not been nudged since the
last compaction, the Session injects a one-time `TurnSteering`:

> "Context is ~N% full. If you are at or near a clean stopping point, call `compact`
> now with a `note_to_self` and `compaction_instructions`."

The nudge latch resets after any compaction. This is MemGPT's proven "memory pressure
warning" shape and directly counters the "agent never calls it" risk.

**Two corrections from review:**
- **Best-effort, not guaranteed-first.** Pressure is evaluated once per round in
  `prepareModelRequest`. A single large tool result can jump pressure from <0.75 to >0.80 in
  one round, in which case the checkpoint fires before the agent ever sees the nudge. The
  nudge gives the agent *an earlier opportunity*, not a guarantee of first crack; the
  checkpoint/summary fallback is the real guarantee.
- **One pressure estimator.** serf already emits a separate "context usage ~80%" warning via
  `maybeWarnContextUsage` (a char/4 estimate in `session_model_call.go`). The new nudge must
  use the same `Manager.Pressure()` signal and the two must be reconciled — either retire the
  old 0.80 warning or make the nudge subsume it — so the agent does not get two uncoordinated
  low-context signals from two different estimators.

### 6. Raised auto-fallback threshold

Give the agent headroom to self-compact at a seam before the automatic narrative summary
fires:

| Layer | Default today | New default | Role |
|-------|--------------|-------------|------|
| `WarnThreshold` (new) | — | 0.75 | Nudge the agent to compact at its next seam |
| `CheckpointThreshold` | 0.80 | 0.80 | Cheap deterministic structured snapshot (unchanged safety net) |
| `SummarizeThreshold` | 0.90 | 0.95 | Automatic cheap-model summary — last-resort fallback |

The deterministic checkpoint stays at 0.80; only the *narrative summary* threshold is raised,
which is what buys the agent room. All three are configurable.

**Accepted risk (documented).** Raising the summary gate to 0.95 narrows the safety margin:
a session whose agent never self-compacts now runs at 0.93–0.95 before Layer 2 fires, where a
single large tool result (output cap ≈ 50K chars ≈ 12.5K tokens) is likelier to approach the
hard context limit (`KindContextLength`) before the summary can run. We accept this in
exchange for agent headroom; the checkpoint at 0.80 remains an earlier structural relief
valve. Worst-case single-turn growth should be sanity-checked against the smallest supported
context window during implementation.

## Changes by file

- `agent/internal/contextmgr/context_manager.go`:
  - `ForceCompact` gains an `instructions string` parameter, forwarded to `summarizeWithLLM`.
  - `summarizeWithLLM` gains an `instructions string` parameter and injects it as a
    higher-precedence block in the prompt (§3). No new mutable `Manager` state.
  - Add `WarnThreshold` config field; change `SummarizeThreshold` default to 0.95.
- `agent/context_strategy_test.go`: the threshold assertion at the scaled-defaults test
  (currently expects `SummarizeThreshold == 0.45`, i.e. `0.90 * 0.5`) becomes `0.475`. Audit
  sibling scaled-threshold assertions for the same breakage.
- `agent/session.go` (+ a new `agent/session_self_compact.go`): `pinnedNote`,
  `pendingInstructions`, `forceRequested`, and the nudge latch (all under `s.mu`); the
  `setPinnedNote`/`requestForceCompact` writer methods; the end-of-tool-round force hook; the
  note re-stamp helper (folded into the `runPreCompactHook` preserved-artifact ordering); the
  nudge emission on `Manager.Pressure()`.
- `agent/session_tool_registry.go`: new `toolDeps` forwarders (`setPinnedNote`,
  `requestForceCompact`); register the `compact` core tool (two-arg schema, marked
  **not** read-only so it serializes rather than batching in the parallel read-only path).
- `docs/design/context.md`: document the tool, the pinned note, the nudge, the revised
  threshold table, and the agent-force-via-`ForceCompact` path.

## Recoverability

Recoverability holds **only for persistent sessions** (`stateDir != ""`): there,
`OnCompactionTurn` persists compaction turns and the full pre-compaction transcript remains
on disk (`context.md` → "Transcript callbacks"), and the checkpoint already points the agent
at `read_session_transcript`. For **non-persistent** sessions and subagents (`stateDir == ""`),
there is **no** transcript and **no** recall surface — cleared detail is gone. The tool
description must reflect this (§1): in ephemeral sessions, `note_to_self` is the only durable
carry, so the agent should be correspondingly conservative about what it instructs the
summarizer to drop.

## Subagents

`compact` is a normal core tool, subject to the usual allowlist/denylist restriction.
Default subagents (which inherit the parent strategy and full core toolset via `NewSession`)
get it. A subagent spawned with `allowedToolNames` gets it **only if** `compact` is in the
allowlist (`RestrictKeepingResultTool` preserves only the result tool), and `deniedToolNames`
can remove it. We do **not** force-preserve `compact` across restriction — a tightly scoped
subagent has little context to compact. This is a deliberate, documented choice.

## Failure modes guarded (traceable to the prior-art note)

| Failure mode | Guard |
|--------------|-------|
| Brevity bias / structured-detail loss | Agent-authored `note_to_self`, re-stamped verbatim at every compaction |
| Context collapse under repeated compaction | The note is re-laid from stored state each compaction, so it never erodes; instructions let the agent steer what the summary keeps |
| Agent compacts too rarely / never (timing uncalibrated) | Best-effort nudge at 0.75 + automatic checkpoint (0.80) and summary (0.95) fallback |
| Mid-history cache busting | Whole-prefix replacement only; the note is re-stamped **only at compaction time**, never per turn |
| Instructions ignored (cheap model) | Instructions injected as higher-precedence over the default "include too much" directive; obedience verified by eval |

## Testing (TDD)

Unit tests, written before implementation:

- **Tool validation:** `compact` rejects empty `note_to_self` or `compaction_instructions`
  with a clear error; on success the Session's `pinnedNote`, `pendingInstructions`, and
  `forceRequested` are set.
- **Force reaches the summary regardless of pressure:** calling `compact` at low pressure
  performs a compaction that runs the LLM summary (so instructions have a consumer), not only
  the deterministic checkpoint.
- **Strategy independence:** the force path compacts under at least one strategy that does
  **not** route through `MaybeCompact` (e.g. `session-log`), proving the `ForceCompact` seam
  works where a `MaybeCompact` flag would not.
- **Note re-stamped verbatim across compaction:** drive enough turns that the note drifts
  before the cutoff, force a compaction, and assert exactly one `[NOTE TO SELF]` turn exists,
  byte-for-byte equal to `pinnedNote`.
- **No per-turn history churn:** across rounds with **no** compaction, history is not mutated
  by note handling (guards the cache regression).
- **Ordering:** after compaction with an active goal, the goal objective is the trailing
  turn and the note immediately precedes it.
- **Instructions obeyed (real, not mocked):** give the summarizer a history containing an
  obviously-droppable block and an instruction to drop it; assert the produced summary
  actually omits it. (String-presence of the instruction in the prompt is necessary but
  **not** sufficient.)
- **Deferred-force timing:** a `compact` that ends the agent's turn compacts within the same
  activation (at the tool-round tail), not at the next user turn.
- **Nudge semantics:** fires once when pressure crosses `WarnThreshold`, does not re-fire
  until after a compaction, resets afterward, and does not double-signal with
  `maybeWarnContextUsage`.
- **Subagent parity + restriction:** an unrestricted subagent exposes `compact`; a subagent
  restricted to an allowlist without `compact` does not.
- **Locking / `-race`:** concurrent `Snapshot`/`Pressure` reads while `compact` writes Session
  state pass `go test -race`.
- **Threshold defaults:** the scaled-defaults test reflects `SummarizeThreshold = 0.95`.
- **Pristine output:** `compact` on a too-short history is a safe no-op with a clear message;
  any error path is captured and asserted (no stray error logs).

## Resolved decisions

1. **Force mechanism:** the Session calls **`Manager.ForceCompact`** (extended with an
   `instructions` parameter) at the tool-round tail. Strategy-independent via the shared
   `Manager` seam; instructions always consumed because `ForceCompact` always summarizes;
   experimental strategies use the standard layers for agent-force (accepted tradeoff). `MaybeCompact`
   is untouched.
2. **Thresholds:** `WarnThreshold` 0.75 / `CheckpointThreshold` 0.80 (unchanged) /
   `SummarizeThreshold` 0.95 (raised). The scaled-defaults test is updated accordingly; the
   narrowed-margin risk is documented and accepted. All three remain configurable.
3. **Tool return value:** confirmation message only — no post-compaction pressure or
   tokens-freed reporting.

## Appendix A: review findings addressed

| # | Finding | Resolution |
|---|---------|-----------|
| Force flag dead for 4/7 strategies | Critical | Route agent-force through `Manager.ForceCompact` (shared seam), not a `MaybeCompact` flag (§Design principle, §4) |
| Instructions discarded when only checkpoint runs / non-summarizing strategies | Critical | `ForceCompact` always summarizes → instructions always have a consumer (§Design principle, §3) |
| Pinned-note re-injection busts prompt cache | Major | Re-stamp **only at compaction time**, never per turn (§2) |
| "Never summarized / verbatim forever" not guaranteed | Major | Survival is by re-stamping from stored state, not by living in the preserved zone (§2) |
| Tool `Exec` has no Session/Manager handle | Major | `toolDeps` forwarder closures `setPinnedNote`/`requestForceCompact` (§1, Changes by file) |
| Nudge can't fire before checkpoint on large jumps | Major | Reframed as best-effort; fallback is the guarantee (§5) |
| Note displaces goal objective from trailing position | Moderate | Note laid before the objective in the shared preserved-artifact path (§2) |
| `SummarizeThreshold` 0.95 breaks scaled-defaults test | Moderate | Test updated to 0.475; siblings audited (Changes by file) |
| `/compact` sibling framing vs. not using `ForceCompact` | Moderate | Now genuinely uses `ForceCompact` (§1, §4) |
| Instructions could leak to a later compaction | Moderate | Instructions live on the Session, passed as a `ForceCompact` arg, consumed once, cleared (§3) |
| New `Manager` fields violate its lock invariant | Moderate | No new mutable `Manager` state; Session state under `s.mu` (§3, §4) |
| Force fires at next user turn if `compact` ends the turn | Moderate | Compaction applied at the tool-round tail, same activation (§1, §4) |
| Nudge collides with existing 0.80 warning | Moderate | Unify on `Manager.Pressure()`; reconcile the old warning (§5) |
| Raising to 0.95 narrows safety margin | Moderate | Risk documented and accepted; checkpoint at 0.80 is earlier relief (§6) |
| Subagent parity breaks under tool restriction | Moderate | Documented: `compact` is restrictable; tests cover both (§Subagents) |
| Cheap-model may ignore instructions; test only checks assembly | Moderate | Higher-precedence placement + a real obedience eval (§3, Testing) |
| Recoverability false for non-persistent sessions | Moderate | Gated on `stateDir != ""`; tool description warns in ephemeral sessions (§Recoverability, §1) |
