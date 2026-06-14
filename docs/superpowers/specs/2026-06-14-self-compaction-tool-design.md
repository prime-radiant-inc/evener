# Self-Compaction Tool Design

An agent-invoked `compact` tool that lets serf compact its own context **by choice**
at a clean task boundary — supplying the keep/clear judgment itself — layered over
the existing automatic compactor as a safety net.

Prior-art research that motivates this design lives in
[`docs/design/2026-06-14-self-compaction-prior-art.md`](../../design/2026-06-14-self-compaction-prior-art.md).
The subsystem it builds on is documented in
[`docs/design/context.md`](../../design/context.md).

> This spec was hardened across two adversarial design-review rounds. Findings and
> resolutions are in [Appendix A](#appendix-a-review-findings-addressed). Where the spec
> states a *requirement* but defers exact code placement to the implementation plan, that
> is deliberate: items the compiler and TDD verify better than prose are not pinned to
> specific line numbers here.

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

## The two separable parts

The single most important design correction: **pinning the note and condensing the
history are independent operations.** Pinning needs no summarizer; condensing does.

1. **Pin the note (always).** `note_to_self` is stored on the Session and re-stamped
   into history at every compaction. This needs no LLM and no minimum history — it
   always takes effect.
2. **Condense the rest (conditional).** The agent's `compaction_instructions` steer the
   summary of everything else — but only when there is actually enough history to
   compact. At a low-pressure seam with a short history, there may be nothing to
   condense; the instructions then have no work to do, and that is fine and reported.

Conflating these was the v2 error: `ForceCompact`'s summary layer no-ops when
`len(history) ≤ PreserveRecentTurns` or no safe cutoff exists, so "instructions are
*always* consumed" was false precisely at the headline use case. The corrected contract:
**the note is always pinned; the instructions are consumed by whatever compaction this
call actually performs, and the tool reports whether a summary ran.**

## Design principle: a strategy-independent capability

The compaction *strategy* (`compact`, `recursive-distill`, `obs-mask`, …) owns **how
content is processed at compaction time**. The self-compaction capability is orthogonal
and must work with *any* strategy.

The strategy-independent seam is **`Manager.ForceCompact`** — a `Manager` method,
reachable regardless of which `Strategy` is active and regardless of whether that
strategy routes through `Manager.MaybeCompact` or inlines its own layers. (Four of
serf's seven strategies do **not** call `MaybeCompact`, so a force-flag consumed inside
`MaybeCompact` would silently no-op for them; the `Manager` seam avoids that.)

- The **trigger** (the tool), the **pinned note**, and the **warning nudge** live at the
  **Session** level.
- Agent-triggered compaction is the Session calling **`Manager.ForceCompact(instructions)`**.
- `ForceCompact` runs the summary layer whenever the history is compactable (not
  threshold-gated, unlike `MaybeCompact`), so at meaningful pressure the agent's
  instructions reach the summarizer even when the automatic path would have run only the
  deterministic checkpoint.

**Accepted tradeoff.** `Manager.ForceCompact` runs the *standard* checkpoint + summarize
layers, not a non-default strategy's bespoke layers. For the default `compact` strategy
(what real sessions use) this is identical to its automatic path. Experimental strategies
fall back to standard layers for agent-force; revisit with a `Strategy.ForceCompact`
interface method if one graduates to default.

## Components

### 1. The `compact` core tool

Registered in `registerCoreTools` (`agent/session_tool_registry.go`) — **not** via
`Strategy.Tools()` — so it is present in every session regardless of active strategy. It
is the agent-facing sibling of the user `/compact` (`Session.Compact` →
`Manager.ForceCompact`) and shares its compaction path.

**Reaching Session state.** A tool `Exec` (`func(ctx, env, args)`) gets no `*Session`
handle. Like `communicate` (which writes session state via an injected closure),
`compact` reaches state through new `toolDeps` forwarder closures — `setPinnedNote` and
`requestForceCompact` — that forward to Session writer methods taking `s.mu`.

**Arguments** (both required, non-empty):

| Arg | Purpose |
|-----|---------|
| `note_to_self` | Durable text re-stamped **verbatim** into history at every compaction — the agent's structured keep. Always pinned, summarizer never authors it. |
| `compaction_instructions` | Guidance steering the summary of everything else ("keep the migration plan; drop the vendored file dumps"). Consumed when a summary actually runs. |

**Exec behavior:** validate both args non-empty; `setPinnedNote(note_to_self)` (replaces
any prior note); `requestForceCompact(compaction_instructions)`; return a confirmation
that states whether a summary ran or there was nothing to condense yet (the note is
pinned either way). **One `compact` per tool round:** a second `compact` in the same
round returns a clear error rather than silently clobbering the first call's note and
instructions before they are consumed.

**When it happens.** The forced compaction runs at the **tail of the tool round** in
which `compact` was called — same agent activation. *Note:* this is a **new** hook in the
round loop (after tool results are persisted, before the turn can be delivered/ended), not
an existing seam; placing it before turn-delivery is what prevents a `compact`+`communicate`
round from deferring the compaction to the next user turn. Exact insertion point is an
implementation-plan detail; the requirement is "after results are persisted, before the
round can return."

**Tool description (the agent's "when" guidance):** call it at a clean stopping point —
between tasks/subtasks, after extracting results from a large context, before consuming
substantial new input, before a complex multi-step operation. In **non-persistent**
sessions (see Recoverability) warn that dropped detail is unrecoverable, so `note_to_self`
is the only durable carry.

### 2. Pinned note (Session state; re-stamped at every compaction)

A single `pinnedNote string` on the Session, guarded by `s.mu`, replaced on each `compact`
call (the "rewrite" model).

**How it survives — correctly.** The note survives **because the Session re-stamps a fresh
verbatim copy from `pinnedNote` at every compaction** — not because it sits in the
preserved-recent window (with `PreserveRecentTurns = 6` it drifts before the cutoff after a
few rounds and would otherwise be folded into the summary). Re-stamping from stored state
makes any folded stale copy harmless.

**One re-stamp site for all paths.** The re-stamp is folded into **`runPreCompactHook`** —
the existing once-per-compaction callback (already latched to fire once per compaction even
though `ForceCompact` emits two events) that today appends the goal objective. This is the
*only* correct site because:
- It fires for **every** compaction path uniformly — agent-force, automatic `MaybeCompact`,
  and the content-filter recovery compaction — without each call site needing its own logic.
- It is the only place with a real "a compaction is happening" signal (the automatic path's
  `ManageContext` returns only `error`; there is no post-return "did it compact" flag).
- It already routes through the nil-transcript-safe steering-append/flush path, so the note
  turn is persisted on persistent sessions and does not panic on ephemeral ones.
- **Ordering:** the note is appended **before** the goal objective inside this hook, so the
  objective remains the trailing (strongest-recency) turn its invariant requires. (v2's "re-stamp
  after `ForceCompact` returns" was wrong — it landed the note after the objective and
  reintroduced the displacement bug.)

**No per-turn churn.** Because re-stamping happens only inside `runPreCompactHook` (i.e. only
when a compaction occurs, when the whole prefix is being replaced anyway), it never mutates
mid-history on non-compaction turns — so it does not bust the prompt cache.

**Resume.** `pinnedNote` is Session state and is **not** automatically restored on resume,
while a stale `[NOTE TO SELF]` turn does reload with the transcript. So on resume the Session
must **reconstruct `pinnedNote` by scanning the resumed history for the most recent
`[NOTE TO SELF]` block** (mirroring how the goal store is restored). Without this, the next
compaction re-stamps nothing and the note erodes away — silently destroying the agent's
durable carry across a routine restart.

Format: a marked block, e.g. `[NOTE TO SELF]\n…\n[END NOTE TO SELF]`, analogous to the
existing `[DISTILLED MEMORY]` / `[CONTEXT CHECKPOINT]` markers.

### 3. `compaction_instructions` hand-off

The Session passes the agent's instructions **as a call parameter** to
`Manager.ForceCompact(ctx, history, instructions, emitFn)`. There is **no new mutable
instruction state on the `Manager`** — the string lives on the Session under `s.mu` until
consumed by the single `ForceCompact` call, then cleared, so it can never leak into a later
unrelated automatic compaction.

**Minimizing the signature ripple.** Rather than add a parameter to the widely-called
`summarizeWithLLM` (≈17 callsites incl. `MaybeCompact`, two experimental strategies, 13
tests), add a steered inner form (`summarizeWithLLMSteered(…, instructions)`) and keep
`summarizeWithLLM` as a thin wrapper passing `""`. Then **`MaybeCompact`, the experimental
strategies, and all existing tests are unchanged** ("MaybeCompact is not modified" stays
true), and only `ForceCompact` (2 production + 4 test callers) gains the `instructions`
parameter.

**Obedience is a real prompt-engineering problem, not a one-liner.** The fixed summarizer
prompt currently *mandates* seven sections ("Your summary MUST include ALL of the following…")
and closes with "include too much." A prepended "these take precedence" note will not reliably
override that. So when `instructions` are present, `summarizeWithLLMSteered` **structurally
swaps** the fixed prefix for an instruction-led variant (the agent's keep/drop directive
becomes the governing instruction, not an addendum). The feature is **gated on an obedience
eval** (Testing) — if the cheap model won't honor the directive, we do not ship the
instruction path, only the note pin.

### 4. Force path

1. Agent calls `compact` → Exec sets Session `pinnedNote`, `pendingInstructions`,
   `forceRequested` (under `s.mu`); returns confirmation. A second `compact` this round →
   error.
2. At the round-loop tail (the new hook, §1), the Session observes `forceRequested`, calls
   `Manager.ForceCompact(ctx, &history, pendingInstructions, emitFn)`, then clears
   `forceRequested`/`pendingInstructions`.
3. `ForceCompact` runs the deterministic checkpoint and — when history is compactable — the
   steered summary (§3). The note re-stamp and objective re-append happen **inside**
   `ForceCompact` via `runPreCompactHook` (§2), not as a separate post-call step.
4. Resulting trailing order: `[NOTE TO SELF]` then `[goal objective]`.

`MaybeCompact` is **not** modified and carries no force flag. The automatic path is
unchanged except that, because its compaction also fires `runPreCompactHook`, it gets the
same note re-stamp for free.

### 5. Warning nudge (Session-level)

When context pressure crosses `WarnThreshold` and the agent has not been nudged since the
last compaction, the Session injects a one-time `TurnSteering`:

> "Context is ~N% full. If you are at or near a clean stopping point, call `compact`
> now with a `note_to_self` and `compaction_instructions`."

The latch resets after any compaction. MemGPT's proven "memory-pressure warning" shape.

- **Best-effort, not guaranteed-first.** Pressure is evaluated once per round; a single large
  tool result can jump <0.75 → >0.80 in one round, firing the checkpoint before the agent sees
  the nudge. The nudge gives an *earlier opportunity*; the checkpoint/summary fallback is the
  guarantee.
- **Audiences are distinct, sources unified.** serf already emits a *user-facing*
  "context ~80%" warning (`maybeWarnContextUsage`, a char/4 estimate). **Decision:** keep that
  as the user-facing signal but drive it from `Manager.Pressure()` so it and the agent-facing
  nudge cannot diverge; the nudge is the only agent-facing signal. (No two uncoordinated
  agent signals; one shared pressure source.)

### 6. Raised auto-fallback threshold

| Layer | Default today | New default | Role |
|-------|--------------|-------------|------|
| `WarnThreshold` (new) | — | 0.75 | Nudge the agent to compact at its next seam |
| `CheckpointThreshold` | 0.80 | 0.80 | Cheap deterministic structured snapshot (unchanged) |
| `SummarizeThreshold` | 0.90 | 0.95 | Automatic cheap-model summary — last-resort fallback |

Only the narrative-summary gate is raised; all three configurable.

**Knock-on changes the raise requires (do not omit):**
- `agent/context_strategy_test.go` scaled-defaults assertion `0.45` → `0.475` (the clamped
  sibling at the same test stays `0.20`).
- `cmd/serf-tui/statusbar.go` `const compactThreshold = 0.90` (and its `hub_status.go`
  consumer, which renders the user's "tokens-to-compact" figure and color band) must track
  `SummarizeThreshold`. Prefer sourcing the value from config rather than a duplicated
  constant so it cannot drift again.

**Accepted risk.** Raising to 0.95 narrows the margin: a session whose agent never
self-compacts runs at 0.93–0.95 before Layer 2 fires, where a single large tool result
(output cap ≈ 50K chars ≈ 12.5K tokens) is likelier to approach the hard context limit.
Accepted for agent headroom; the checkpoint at 0.80 is earlier relief. Sanity-check
worst-case single-turn growth against the smallest supported window during implementation.

## Changes by file

- `agent/internal/contextmgr/context_manager.go`: add `summarizeWithLLMSteered(…, instructions)`,
  keep `summarizeWithLLM` as a `""` wrapper; `ForceCompact` gains an `instructions` parameter
  forwarded to the steered form; add `WarnThreshold`; change `SummarizeThreshold` default to 0.95.
- `agent/internal/contextmgr/context_manager_test.go` (+ `compaction_kind_test.go`): update the
  4 `ForceCompact` callsites for the new parameter (pass `""` where instructions are irrelevant).
- `agent/context_strategy_test.go`: scaled-defaults assertion `0.45` → `0.475`.
- `agent/session_compaction.go`: fold the pinned-note re-stamp into `runPreCompactHook`,
  appended before the goal objective; ensure it uses the nil-transcript-safe steering path.
- `agent/session.go` (+ new `agent/session_self_compact.go`): `pinnedNote`,
  `pendingInstructions`, `forceRequested`, nudge latch (under `s.mu`); `setPinnedNote` /
  `requestForceCompact` writers; the round-loop force hook; pinned-note reconstruction on
  resume; nudge emission on `Manager.Pressure()`; one-`compact`-per-round guard.
- `agent/session_tool_registry.go`: `toolDeps` forwarders; register the non-read-only `compact`
  core tool.
- `cmd/serf-tui/statusbar.go` (+ `hub_status.go`): track `SummarizeThreshold` (ideally via config).
- `docs/design/context.md`: document the tool, the pinned note, the nudge, the threshold
  table, and the agent-force-via-`ForceCompact` path.
- The user `/compact` path (`Session.Compact`) and the content-filter recovery
  (`session_model_call.go`) call `ForceCompact` with empty instructions — update both call sites.

## Recoverability

Recoverability holds **only for persistent sessions** (`stateDir != ""`): `OnCompactionTurn`
persists compaction turns, the full pre-compaction transcript stays on disk, and the
checkpoint already points the agent at `read_session_transcript`. For **non-persistent**
sessions/subagents there is no transcript and no recall surface — cleared detail is gone, so
the tool description tells the agent to be conservative about what it drops (the `note_to_self`
is the only durable carry).

## Subagents

`compact` is a normal core tool subject to allowlist/denylist restriction. Default subagents
get it; a subagent restricted via `allowedToolNames` gets it only if allow-listed
(`RestrictKeepingResultTool` preserves only the result tool); `deniedToolNames` can remove it.
We do not force-preserve it — a tightly scoped subagent has little to compact.

## Failure modes guarded (traceable to the prior-art note)

| Failure mode | Guard |
|--------------|-------|
| Brevity bias / structured-detail loss | Agent-authored `note_to_self`, always pinned, re-stamped verbatim |
| Context collapse under repeated compaction | Note re-laid from stored state each compaction (incl. after resume reconstruction); instructions steer what the summary keeps |
| Agent compacts too rarely / never | Best-effort nudge at 0.75 + automatic checkpoint (0.80) and summary (0.95) fallback |
| Mid-history cache busting | Note re-stamped only inside `runPreCompactHook` (compaction time), never per turn |
| Instructions ignored (cheap model) | Structural prompt swap when instructions present; gated on an obedience eval |
| Durable note lost on restart | `pinnedNote` reconstructed from history on resume |

## Testing (TDD)

- **Validation:** `compact` rejects empty args; a second `compact` in one round errors.
- **Note pinned even with nothing to condense:** at low pressure / short history, `compact`
  pins the note (re-stamped at the next compaction) and the tool reports "nothing to condense
  yet" rather than claiming a summary ran.
- **Instructions consumed when a summary runs:** at compactable history, the steered summary
  fires and the produced summary reflects the instructions.
- **Instructions obeyed (real, not mocked):** history with an obviously-droppable block +
  "drop it" instruction → the summary omits it. Prompt-contains-the-string is necessary but
  **not** sufficient; this eval gates the instruction feature.
- **Strategy independence:** force compacts under a strategy that does not call `MaybeCompact`
  (e.g. `session-log`).
- **Note re-stamped verbatim across compaction:** drive the note past the cutoff, compact,
  assert exactly one verbatim `[NOTE TO SELF]` turn.
- **Ordering:** after compaction with an active goal, the objective is the trailing turn and
  the note immediately precedes it.
- **No per-turn churn:** across non-compaction rounds, note handling does not mutate history.
- **Resume:** pin a note, compact, resume, compact again → the note survives.
- **Deferred-force timing:** a `compact` that ends the turn compacts within the same activation.
- **Nudge:** fires once past `WarnThreshold`, resets after compaction; user warning and agent
  nudge derive from the same `Manager.Pressure()`.
- **Subagents:** unrestricted subagent has `compact`; an allowlist-restricted one without it
  does not.
- **`-race`:** concurrent `Snapshot`/`Pressure` reads while `compact` writes Session state.
- **Threshold defaults / TUI:** scaled-defaults test reflects 0.95; the TUI threshold tracks it.
- **Pristine output:** no-op and error paths produce clear messages, no stray error logs.

## Resolved decisions

1. **Force mechanism:** Session calls `Manager.ForceCompact(instructions)` at the round-loop
   tail. Strategy-independent via the `Manager` seam; instructions consumed by the summary when
   history is compactable; note pinned unconditionally; experimental strategies use standard
   layers (accepted tradeoff). `MaybeCompact` untouched (via the steered wrapper).
2. **Thresholds:** `WarnThreshold` 0.75 / `CheckpointThreshold` 0.80 / `SummarizeThreshold`
   0.95. Scaled-defaults test and TUI constant updated; margin risk documented and accepted.
3. **Tool return value:** confirmation only — but it does state whether a summary ran vs.
   "note pinned, nothing to condense yet."

## Appendix A: review findings addressed

**Round 1 (force/instructions/note architecture):** force routed through `Manager.ForceCompact`
instead of a `MaybeCompact` flag dead for 4/7 strategies; instructions reach the summarizer via
that path; note re-stamped from stored state (not "lives in preserved zone"); tool reaches state
via `toolDeps` closures; nudge reframed best-effort; note ordered with the goal objective; the
0.95 test break, instruction-leak, Manager-lock, next-user-turn timing, nudge collision,
subagent restriction, and non-persistent recoverability items.

**Round 2 (the v2 revision's own holes):**

| Finding | Resolution |
|---------|-----------|
| "Instructions always consumed" false on short history (no-op path) | Decoupled: note always pinned; instructions consumed only when a summary runs; tool reports which (§"two separable parts", §1, §3) |
| Note re-stamp ordering self-contradictory; "after ForceCompact" displaces objective | Single re-stamp site inside `runPreCompactHook`, before the objective, for all paths (§2, §4) |
| Automatic path has no "did it compact" signal for a post-return re-stamp | Re-stamp lives in the emit callback (`runPreCompactHook`), the only place with that signal (§2) |
| `pinnedNote` lost on resume → durable note destroyed | Reconstruct `pinnedNote` from history on resume (§2, Testing) |
| TUI `compactThreshold = 0.90` desyncs when raising to 0.95 | TUI tracks `SummarizeThreshold`, ideally via config (§6, Changes by file) |
| Signature ripple under-scoped; "MaybeCompact not modified" false | Steered inner form + `""` wrapper keeps `MaybeCompact`/strategies/tests unchanged; only `ForceCompact` callers change, enumerated (§3, Changes by file) |
| "Tool-round tail seam" precedent (content-filter pass) doesn't exist | Described as a **new** round-loop hook with its placement requirement; false precedent removed (§1) |
| Two `compact` calls in one round drop the first silently | One `compact` per round; second errors (§1) |
| Note re-stamp transcript persistence unspecified (panic/loss) | Routed through the nil-transcript-safe steering path in `runPreCompactHook` (§2) |
| Cheap-model obedience vs. mandated 7 sections | Structural prompt swap when instructions present; gated on obedience eval (§3) |
| `maybeWarnContextUsage` reconciliation asserted, not decided | Decided: keep as user-facing, drive from `Manager.Pressure()`; nudge is the sole agent signal (§5) |
