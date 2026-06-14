# Self-Compaction Tool Design

An agent-invoked `compact` tool that lets serf compact its own context **by choice**
at a clean task boundary — supplying the keep/clear judgment itself — layered over
the existing automatic compactor as a safety net.

Prior-art research that motivates this design lives in
[`docs/design/2026-06-14-self-compaction-prior-art.md`](../../design/2026-06-14-self-compaction-prior-art.md).
The subsystem it builds on is documented in
[`docs/design/context.md`](../../design/context.md).

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
orthogonal to that and must work with *any* strategy. Therefore:

- The **trigger** (the tool), the **pinned note**, the **warning nudge**, and the
  **raised threshold** all live at the **Session / Manager** level and are
  strategy-independent.
- The agent's **`compaction_instructions`** are the *only* thing handed into the
  strategy's content processing — and they ride a path (`Manager.summarizeWithLLM`)
  that every strategy already routes through, so no per-strategy code changes.

```
┌─────────────────────────── Session (strategy-independent) ───────────────────────────┐
│  compact tool (core)   pinned note (survives all compactions)   warning nudge         │
│         │                      │                                      │               │
│         ▼                      ▼                                      ▼               │
│  sets force + instructions + note      re-inject note at tail        emit steering    │
└─────────┬──────────────────────────────────────────────────────────────────────────┘
          │ (next request boundary)
          ▼
   Manager.MaybeCompact ──► active Strategy processing ──► Manager.summarizeWithLLM
                              (varies per strategy)         (honors compaction_instructions)
```

## Components

### 1. The `compact` core tool

Registered in `registerCoreTools` (`agent/session_tool_registry.go`) — **not** via
`Strategy.Tools()` — so it is present in every session and subagent regardless of the
active strategy. It is the agent-facing sibling of the existing user `/compact`
(`Manager.ForceCompact`).

**Arguments** (both required, both non-empty):

| Arg | Purpose |
|-----|---------|
| `note_to_self` | Durable text preserved **verbatim** into the next context — the agent's own structured keep (decisions made, the API signature settled on, the failing tests, the next steps). Defeats brevity bias because the summarizer can never touch it. |
| `compaction_instructions` | Guidance to the active strategy's summarizer about what to **preserve** and what is **safe to drop** when condensing everything else ("keep the migration plan steps verbatim; the vendored file dumps are irrelevant, drop them"). |

**Exec behavior:** validate both args non-empty; set the Session's pinned note to
`note_to_self` (replacing any prior note); stash `compaction_instructions` as pending;
set the force-compaction flag; return a confirmation. The compaction itself is applied
at the next request boundary (see Trigger flow) — the tool does not mutate history
directly, keeping all history mutation at the single `ManageContext` site.

**Tool description (the agent's "when" guidance):** instruct the agent to call it at a
clean stopping point — between tasks/subtasks, after extracting results from a large
context, before consuming substantial new input, before a complex multi-step
operation — and explain what belongs in each argument.

### 2. Pinned note (Session state; survives every compaction)

A single `pinnedNote string` slot on the Session. Replaced on each `compact` call (the
"rewrite" model — the agent carries forward what still matters).

After **any** compaction — agent-triggered or automatic — the Session re-injects the
pinned note as a tail `TurnSteering` turn (remove the prior note turn, append a fresh
one). Because compaction only ever folds turns *before* the preserved-recent cutoff,
a tail turn always sits in the preserved zone and is therefore **never summarized**.
This lifts the proven `RecursiveDistillStrategy` tail-reinjection trick
(`agent/internal/contextmgr/strategy_recursive_distill.go`) up to Session level so it
holds for every strategy.

Format: a marked block, e.g. `[NOTE TO SELF]\n…\n[END NOTE TO SELF]`, analogous to the
existing `[DISTILLED MEMORY]` and `[CONTEXT CHECKPOINT]` markers.

### 3. `compaction_instructions` hand-off (into the strategy's processing)

Carried transiently on the `Manager`: the Session sets
`Manager.pendingCompactionInstructions` before the next compaction runs;
`summarizeWithLLM` injects it into its prompt prefix (e.g. *"Follow these instructions
about what to preserve and what is safe to drop: <instructions>"*); the field is
cleared after the compaction completes.

Every strategy that routes through the Manager's summarize path honors this with **zero
per-strategy change**. A strategy with a bespoke summarizer (e.g. `recursive-distill`'s
`microSummarize`) may choose to honor it later; that is out of scope here.

### 4. Trigger flow & the force path

1. Agent calls `compact` → Exec sets `pinnedNote`, `pendingCompactionInstructions`,
   and `forceCompactRequested = true`; returns confirmation.
2. The tool result is appended; the loop proceeds to the next `prepareModelRequest`.
3. `prepareModelRequest` → `Strategy.ManageContext` → `Manager.MaybeCompact`. When
   `forceCompactRequested` is set, `MaybeCompact` compacts **regardless of pressure**,
   consuming and clearing the force flag and the pending instructions.
4. After `ManageContext` returns, the Session re-injects the pinned note at the tail.
5. The next LLM request sees: `[checkpoint/summary authored under the agent's
   instructions]` + recent turns + `[NOTE TO SELF]`.

**Force mechanism (recommended):** transient `Manager` fields
(`forceCompactRequested`, `pendingCompactionInstructions`) consumed by `MaybeCompact`/
`summarizeWithLLM`. This needs **no `Strategy` interface change** and works for every
Manager-backed strategy. (Alternative, if we dislike transient Manager state: add an
explicit method to the `Strategy` interface. Deferred — see Open implementation
choices.)

### 5. Warning nudge (Session-level; strategy-independent)

When `Manager.Pressure()` crosses `WarnThreshold` and the agent has not been nudged
since the last compaction, the Session injects a one-time `TurnSteering`:

> "Context is ~N% full. If you are at or near a clean stopping point, call `compact`
> now with a `note_to_self` and `compaction_instructions`."

The nudge latch resets after any compaction. This is MemGPT's proven "memory pressure
warning" shape and directly counters the "agent never calls it" risk.

### 6. Raised auto-fallback threshold

Give the agent headroom to self-compact at a seam before the automatic narrative
summary fires:

| Layer | Default today | New default | Role |
|-------|--------------|-------------|------|
| `WarnThreshold` (new) | — | 0.75 | Nudge the agent to compact at its next seam |
| `CheckpointThreshold` | 0.80 | 0.80 | Cheap deterministic structured snapshot (unchanged safety net) |
| `SummarizeThreshold` | 0.90 | 0.95 | Automatic cheap-model summary — last-resort fallback |

The deterministic checkpoint is cheap and non-destructive (structured, preserves
recent turns + the pinned note), so leaving it at 0.80 is fine. Raising only the
*narrative summary* threshold is what buys the agent room. All three are configurable.

## Changes by file (sketch)

- `agent/internal/contextmgr/context_manager.go`: add transient
  `forceCompactRequested` / `pendingCompactionInstructions`; have `MaybeCompact` honor
  the force flag; have `summarizeWithLLM` inject the instructions into its prompt
  prefix; add `WarnThreshold` config; change `SummarizeThreshold` default to 0.95.
- `agent/session.go` (+ a new `agent/session_self_compact.go`): `pinnedNote` and
  nudge-latch state; pinned-note re-injection after compaction; nudge emission keyed
  on `Manager.Pressure()`.
- `agent/session_tool_registry.go` (+ tool impl file): register the `compact` core
  tool with the two-arg schema and Exec described above.
- `docs/design/context.md`: document the new tool, the pinned note, the nudge, and the
  revised threshold table.

## Recoverability

No new mechanism needed. Cleared detail is already recoverable: `OnCompactionTurn`
persists compaction turns and the full pre-compaction transcript remains on disk
(see `context.md` → "Transcript callbacks"). A future recall/search surface over that
transcript is out of scope.

## Failure modes guarded (traceable to the prior-art note)

| Failure mode | Guard |
|--------------|-------|
| Brevity bias / structured-detail loss | Agent-authored `note_to_self`, preserved verbatim |
| Context collapse under repeated compaction | Pinned note lives in the preserved zone → never summarized → never erodes |
| Agent compacts too rarely / never (timing uncalibrated) | Warning nudge at 0.75 + automatic checkpoint (0.80) and summary (0.95) fallback |
| Mid-history cache busting | Whole-prefix replacement only; the note is a tail turn |

## Testing (TDD)

Unit tests, written before implementation:

- **Tool validation:** `compact` rejects empty `note_to_self` or
  `compaction_instructions` with a clear error; on success sets pinned note + pending
  instructions + force flag.
- **Note survives auto-compaction:** after an automatic checkpoint, then after an
  automatic summary, the `[NOTE TO SELF]` turn is present and byte-for-byte verbatim.
- **Note is replaced, not duplicated:** a second `compact` call leaves exactly one note
  turn, with the new content.
- **Instructions reach the summarizer:** `summarizeWithLLM`'s prompt contains the
  agent's `compaction_instructions`.
- **Force path:** with `forceCompactRequested` set, `MaybeCompact` compacts below the
  checkpoint threshold on the next `ManageContext`, and clears the flag + instructions.
- **Nudge semantics:** fires once when pressure crosses `WarnThreshold`, does not
  re-fire until after a compaction, and resets afterward.
- **Subagent parity:** a subagent session exposes the `compact` tool.
- **Pristine output:** `compact` on a too-short history is a safe no-op with a clear
  message; any error path is captured and asserted (no stray error logs).

## Resolved decisions

1. **Force mechanism:** transient `Manager` fields (`forceCompactRequested`,
   `pendingCompactionInstructions`) consumed by `MaybeCompact`/`summarizeWithLLM`. **No
   `Strategy` interface change** — the existing `ManageContext` → `MaybeCompact` path
   honors the flag on the next request boundary, so the capability works for every
   Manager-backed strategy.
2. **Thresholds:** `WarnThreshold` 0.75 / `CheckpointThreshold` 0.80 (unchanged) /
   `SummarizeThreshold` 0.95. The nudge fires *before* the deterministic checkpoint so
   the agent gets first crack at the seam; only the narrative-summary fallback is raised.
   All three remain configurable.
3. **Tool return value:** confirmation message only — no post-compaction pressure or
   tokens-freed reporting. (Compaction is deferred to the next `ManageContext`, so those
   numbers aren't available at Exec time anyway; the agent simply sees a smaller context
   with its `[NOTE TO SELF]` on its next turn.)
