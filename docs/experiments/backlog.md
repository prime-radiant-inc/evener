# Experiment Backlog

Prioritized queue of next experiments. Updated March 26, 2026.

## Currently testing

### v11-positive-framing (ready to launch)

Two prompt fixes based on real session interrogation of v10 failures:
1. **Reviewer positive authority ordering**: "Treat domain-tool results as
   authoritative" + "Computational proof outranks visual inspection, manual
   reasoning, and heuristic judgment." Replaces prohibition framing.
2. **Warnings are not failures**: workflow.md adds "A command that exits 0
   succeeded — warnings are informational, not failures." Resolves the
   competing-instruction conflict where "never ignore output" overrode the
   anti-gold-plating instruction.

**Test:** chess-best-move × 3, polyglot-c-py × 3.
**Build:** commit 1e0ddd1.

## Next up

### 1. Delegation info loss — structural solution

**Problem:** Coordinator consistently paraphrases task specs when delegating,
losing exact format strings, schemas, and constraints. Prompt-level "forward
verbatim" instruction doesn't work — the model rewrites regardless.

**Hypotheses to test:**
- **A) Structured delegation field:** Add an optional `context` parameter to
  spawn_agent that the system auto-populates with the original task. The
  coordinator still writes its own `task`, but the implementer sees both.
- **B) Read-back verification:** After the coordinator delegates, inject a
  steering message: "Verify your delegation includes ALL format specifications
  from the original task. If you summarized instead of copying, revise."
- **C) Accept the loss:** ~60% of delegation-info-loss tasks pass anyway.
  Focus engineering time elsewhere.

**Test plan:** Pick log-summary-date-ranges and multi-source-data-merger (both
~1/3 pass rate due to info loss). Test each hypothesis × 3 reps.

### 2. Non-delegation enforcement

**Problem:** Coordinator sometimes handles tasks directly instead of delegating.
Most visible on chess-best-move (reads image, writes wrong answer immediately)
and pytorch-model-cli (codes in C++ for 35 rounds instead of delegating).

**Hypotheses:**
- **A) Steering injection:** If the coordinator's first 3 tool calls don't
  include spawn_agent, inject: "You are the coordinator. Delegate."
- **B) Prompt-only:** Current approach. The reorder fix (Role before Skills)
  helped fix-git delegate. Further prompt tweaks may help chess-best-move.

**Test plan:** chess-best-move × 5 reps with each approach.

### 3. Reviewer quality — partially addressed

**Problem:** Reviewer sometimes destroys correct answers (chess-best-move:
removed g2g4) or introduces bugs (adaptive-rejection-sampler: overly strict
xinit validation). Root cause identified in v8: reviewer without equivalent
domain tools (chess engine) fell back on vision hallucination, overriding the
implementer's correct computational proof.

**Fix in testing (v9):** Reviewer prompt changed to "review consistency, not
re-derive." If implementer validated with a domain tool, reviewer checks
methodology consistency rather than substituting its own analysis.

**Remaining hypotheses (if v9 doesn't fully fix):**
- **A) Conservative rejection:** "Do not suggest changes to output format or
  structure unless the task specification explicitly requires a different format."
- **B) Reject-only, no fix suggestions:** Reviewer says what's wrong but
  doesn't suggest how to fix it.

### 4. Post-rejection coordinator behavior

**Problem:** After reviewer rejection, coordinator sometimes fixes code itself
instead of spawning a new implementer (violating "NEVER write files yourself").

**Fix:** Strengthen step 5 in coordinator.md: "If the reviewer rejects, spawn
a new implementer with the reviewer's feedback. Do NOT fix the code yourself."

### 5. Full 89-task eval with all fixes

After validating v7 changes, run the full 89-task × 3-rep eval to measure
aggregate impact. Compare against:
- disc-3rep-v6 (unfixed 3-rep baseline): 40.7%
- disc-3rep-v6-fixed (first fixed eval): 42.9%
- Combined baseline (gpt-5.4 + mini): 82%

### 6. Budget awareness

**Problem:** Neither coordinator nor implementer knows the 900s wall-clock
budget. Implementers research for 10 minutes then timeout.

**Hypothesis:** Injecting approximate budget info ("You have approximately
12 minutes of wall time") would encourage decisive action.

**Risk:** May cause premature submission ("I'm running out of time, submit
what I have").

## Completed / shipped

- Template section ordering (Role before Skills) — shipped
- Verification language revert (artifact-only → run tests) — shipped
- Workspace cleanup in shared values — shipped
- Verification cleanup after step 4 — shipped
- Action bias in workflow — shipped (v7: 3/7 tasks improved)
- Optional explorer — shipped (v7: included in action-bias run)
- Capabilities: computational verification — shipped (v7: included)
- Input pre-processing ban — shipped (coordinator must not analyze task inputs)
- Coordinator/reviewer rename — shipped (inventory not scout, coordinator not dispatcher)
- Reviewer consistency check — shipped (v9: 2/3 chess, v10: 0/3 — fix insufficient)
- Scratch directory verification — shipped (v9: 2/3 polyglot, v10: still violated)
- Active pre-submit workspace check — shipped (v9/v10: still violated)
- Delegation guidelines to reviewer — shipped (v10: included)
- Anti-gold-plating — shipped (v10: overridden by competing instructions)
- Reviewer positive authority ordering — testing (v11: replaces prohibition framing)
- Warnings are not failures — testing (v11: resolves competing-instruction conflict)
- harbor-runner: removed parent-dir binary copy — shipped
- Makefile: `build-linux` target with cache invalidation — shipped
- Session interrogation tool — shipped (rewritten: real session resume, subagent support)
- Root-cause prompt template — shipped
