# Forced note at compaction — design

Builds on the self-compaction tool. Motivated by the **erosion eval**
(`docs/design/2026-06-14-compaction-erosion-results.md`): over 6 successive compactions a
baseline recursive summary lost 2/4 facts by round 3 — specifically the **opaque token and
the exact number** — while a verbatim re-stamped note kept 4/4. So a note that is
**maintained across a long session's compactions** prevents the erosion that auto-compaction
causes for the highest-value, least-recoverable details.

Today a note only exists if the agent proactively calls `compact`. This feature makes the
harness **ensure a note exists and stays current at each compaction**, so erosion-prone facts
survive a long session without relying on the agent's initiative.

## Honest scope (from the evals)

This pays off in **long sessions with many compactions**. On any single early compaction —
especially with clean facts — it adds little (the live single-compaction tests showed no
benefit). So it must fire **late and rarely**, not eagerly.

## Trigger: headroom, not a fixed fraction

Fire the forced-note when **remaining context < a reserve**, where the reserve ≈ **2× the
max response size** (Jesse's idea): late enough that there's room for the note-authoring
interaction *and* the model's reply, and window-size-aware (for serf's large windows a fixed
0.80 fraction is needlessly early). serf has no per-profile max-output value (it's a
per-request `MaxTokens`, usually provider-default), so introduce a configurable
`Manager.CompactReserveTokens` (default ≈ 32k = 2× a typical 16k response). Trigger when
`ContextWindowSize() - usedTokens < CompactReserveTokens`.

## Two capture mechanisms (build both, compare)

**A — force the tool call.** When the trigger fires, inject a mandatory steering turn ("Your
context is about to be compacted; you MUST call `compact` now with a `note_to_self` of what
you'll need") and **defer** the auto-compaction one round. The model authors the note via the
existing tool. **Hard fallback:** if the model doesn't call `compact` (next round still over
the reserve, already mandated), auto-compact normally (one deferral max — no overflow/loop).
- Pro: reuses the tool path; the *agent* chooses what matters.
- Con: relies on the model complying + formatting the call; forces a turn mid-task.
- Testable only **live** (compliance is a model behavior).

**B — harness elicits the note.** When the trigger fires, the harness makes a **side LLM
call** over the current history ("List the facts/decisions/exact values/IDs that must survive
verbatim — tokens, hashes, numbers, names") and **pins the reply as the note**, then compacts
(the note is re-stamped as usual). No reliance on the model's tool use.
- Pro: guaranteed; harness-controlled; the prompt can explicitly target the eroding kinds
  (opaque IDs, exact numbers) the erosion eval identified.
- Con: a side call per compaction; the agent doesn't choose (the elicitor does).
- Testable in a **controlled eval** (does the elicitor capture the eroding facts?).

## Comparison plan (asymmetric, by necessity)

- **B core** is validated controlled: extend the erosion eval — at each round, elicit the
  must-keep note via the side call and check it captures the facts the baseline erodes
  (DEPLOY token, `numShards exceeds 16`). If the elicitor captures them, pinning them prevents
  erosion by construction.
- **A** needs a **live** long session: does the model comply with the mandate and author a
  note that captures the eroding facts? Driven via the serf CLI across several compactions.
- **Decide** on: erosion-resistance (facts kept), reliability (B is deterministic; A depends
  on compliance), and cost (both ≈ one extra LLM interaction per compaction). Keep the winner;
  the other can stay behind a config flag or be dropped.

## Integration points (to pin down in implementation, not asserted here)

- The trigger check lives where pressure is already evaluated (`prepareModelRequest` →
  `ManageContext`), alongside the existing `WarnThreshold` nudge.
- **A** adds: a mandate steering turn + a "deferred one round" flag on the Session + the
  fallback path. Composes with the existing nudge (nudge at 0.75 soft → mandate at the reserve
  hard).
- **B** adds: an elicitation method on the Manager/Session (a side `Complete` call) invoked in
  the compaction flow before summarizing, setting `pinnedNote`.
- Both reuse the existing pinned-note re-stamp (`runPreCompactHook`) and auto-fallback.

## Build order

1. Controlled eval: validate **B's elicitor** captures the eroding facts (cheap, evidence-first).
2. If it does → implement **B** in the session loop + the `CompactReserveTokens` trigger (TDD).
3. Implement **A** (mandate + defer + fallback) (TDD).
4. Live comparison over a multi-compaction session; keep the winner.
