# Experiment Backlog

Prioritized queue of next experiments. Updated March 26, 2026.

## Currently testing

### v7-action-bias (running)

Three prompt changes targeting timeout regressions and research-loop failures:
1. Workflow: "Start building early. Research is not progress."
2. Coordinator: Optional explorer for small workspaces
3. Capabilities: Computational verification for vision tasks

**Test:** 7 regression tasks × 1 rep. If positive, run broader eval.

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
- **A) Code-level enforcement:** Refuse to execute write/edit tools at depth 0.
  The coordinator can only spawn, read, grep, and shell (for verification).
- **B) Steering injection:** If the coordinator's first 3 tool calls don't
  include spawn_agent, inject: "You are the coordinator. Delegate."
- **C) Prompt-only:** Current approach. The reorder fix (Role before Skills)
  helped fix-git delegate. Further prompt tweaks may help chess-best-move.

**Test plan:** chess-best-move × 5 reps with each approach.

### 3. Reviewer quality

**Problem:** Reviewer sometimes destroys correct answers (chess-best-move:
removed g2g4) or introduces bugs (adaptive-rejection-sampler: overly strict
xinit validation).

**Hypotheses:**
- **A) Conservative rejection:** Add "When rejecting, do not suggest changes
  to output format or structure unless the task specification explicitly
  requires a different format."
- **B) Reject-only, no fix suggestions:** Reviewer says what's wrong but
  doesn't suggest how to fix it, reducing the chance of bad advice.

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
- Action bias in workflow — shipped (testing)
- Optional explorer — shipped (testing)
- Capabilities: computational verification — shipped (testing)
- harbor-runner: removed parent-dir binary copy — shipped
- Makefile: `build-linux` target with cache invalidation — shipped
- Session interrogation tool — shipped
- Root-cause prompt template — shipped
