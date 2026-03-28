# Experiment Backlog

Prioritized queue of next experiments. Updated March 27, 2026.

## Currently running

### full-baseline-2026-03-27 — all 89 tasks × 3 reps

Clean baseline with all shipped fixes on main @ 5554bed. gpt-5.4-mini,
3 × c6i.2xlarge, concurrency 8. Results will establish the current state
and identify which failures to target next.

**After this completes:** collect with `post_run.sh`, interrogate all failures,
build the failure inventory, then start hill-climbing from the top of the
"Next up" list below.

## Next up

Priority order for hill-climbing after the baseline establishes current state.
Re-prioritize based on what the baseline failure inventory reveals.

### 1. Delegation info loss — structural solution

**Problem:** Coordinator paraphrases task specs when delegating, losing exact
format strings, schemas, and constraints. Prompt-level "forward verbatim"
instruction doesn't work — the model rewrites regardless. No good general fix
found yet.

**Hypotheses to test:**
- **A) Structured delegation field:** Add `context` parameter to spawn_agent
  that auto-populates with the original task. Coordinator writes its own `task`,
  implementer sees both.
- **B) Read-back verification:** Post-delegation steering: "Verify your
  delegation includes ALL format specifications. If you summarized, revise."
- **C) Accept the loss:** ~60% of info-loss tasks pass anyway. Focus elsewhere.

**Test plan:** Tasks with known info-loss failures from baseline × 3 reps each.

### 2. Coordinator bypass (stochastic)

**Problem:** Coordinator sometimes handles tasks directly instead of delegating.
Most visible on chess-best-move. Root cause: vision steering content varies per
run, sometimes priming the coordinator to act directly.

**Current state:** ops-task removal + "quality gate" framing shipped. Baseline
retest went 3/3, but v27 experiments showed 2/3 bypass rate on same task.
Appears stochastic, not systematic. **Deprioritize if baseline shows 2/3+.**

**Hypotheses if still failing:**
- **A) Steering injection:** If first 3 tool calls don't include spawn_agent,
  inject: "You are the coordinator. Delegate."
- **B) Vision steering suppression:** Don't inject image descriptions into
  coordinator context for tasks requiring delegation.

### 3. Reviewer quality

**Problem:** Reviewer destroys correct answers when it lacks domain tools.
Root cause: reviewer re-derives from primary sources (vision, raw data)
instead of reviewing implementer's methodology.

**Shipped fixes:** v23-B removed "intuit", added spec authority for implementer.
v25 reviewer experiments all made things worse (0/3, 0/3, 1/3) — do not revisit
the vision/reviewer path. The shipped v23-B is the current best.

**Remaining hypotheses (only if baseline shows reviewer-caused failures):**
- **A) Conservative rejection:** Reviewer says what's wrong but doesn't suggest
  how to fix it.
- **B) Tool parity:** Give reviewer the same tools implementer had (risky —
  may cause reviewer to redo all work).

### 4. Budget awareness

**Problem:** Neither coordinator nor implementer knows the 900s wall-clock
budget. Implementers research for 10 minutes then timeout.

**Hypothesis:** Inject approximate budget info ("~12 minutes wall time").

**Risk:** Premature submission. Test carefully on tasks that currently pass.

### 5. Verification depth (structural)

**Problem:** Self-referential testing can't detect external contract mismatches
(e.g., kv-store-grpc proto field `val` vs expected `value`). No prompt fix can
address this — the agent's test agrees with the agent's implementation.

**Hypotheses:**
- **A) Cross-check instruction:** "Compare your proto/schema field names against
  the exact words in the task description" (fragile, may help some tasks)
- **B) Accept nondeterministic:** ~50-70% pass rate on these tasks. Focus on
  tasks with fixable failure modes instead.

impl-test-a was NOT shipped — its 3/3 was stochastic, not causal.

## Completed / shipped

### Effective fixes (validated with improved scores)
- **v19-deleg-b** — "quality gate, not worker" framing → chess-best-move 3/3
- **v19-state-b** — post-test mutation check → git-multibranch 3/3
- **v17+v18** — harmonize HARD GATE + no-tests case → log-summary-date-ranges 3/3
- **v23-B** — reviewer: remove "intuit", spec authority → chess-best-move 3/3
- **v26** — neutral task_list phrasing → custom-memory-heap-crash 3/3
- **ops-task removal** — deleted embedded skill that primed direct implementation
- **coordinator inventory fix** — "listing, not reading" + anti-rationalization
- **v11** — reviewer positive authority ordering + warnings-are-not-failures → 6/6
- **v13** — tests-first verification + submit gate → fix-code-vuln 3/3, git-multi 3/3

### Infrastructure shipped
- Template section ordering (Role before Skills)
- Verification language revert (artifact-only → run tests)
- Workspace cleanup in shared values
- harbor-runner: removed parent-dir binary copy
- Makefile: `build-linux` target with cache invalidation
- Session interrogation tool (real session resume, subagent support)
- Root-cause prompt template
- Eval tooling: run_eval.sh (unified launcher with wave mode), run_status.sh, post_run.sh, interrogate_failures.sh

### Tried and superseded (no longer active)
- Scratch directory verification — v9/v10: still violated despite instruction
- Active pre-submit workspace check — v9/v10: still violated
- Anti-gold-plating — v10: overridden by competing instructions (resolved differently in v11)
- Reviewer consistency check — v9: 2/3, v10: 0/3 (superseded by v11 positive authority)
- v25 reviewer experiments — all made things worse, do not revisit
- impl-test-a — 3/3 was stochastic, not shipped
