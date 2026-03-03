# Prompt Experiment Backlog

Hypotheses to test against terminal-bench. Each experiment changes ONE thing.
See `~/.claude/skills/prompt-experiment-protocol/SKILL.md` for execution methodology.

## Current Baseline

Best run: `serf_gpt-5.3-codex_high_e6756a2_20260302_1` — 58/89 cumulative (65.2%)
Reverted baseline: `revert-e6756a2-high` — 9/14 (64%) on 5 tasks x ~3 reps
Critical parameter: `--ak reasoning_effort=high` (without it everything scores ~0%)

---

## Experiment 1: Delegation Framing (delegation-framing-v1)

**Status:** In progress — prompt written, tests being fixed

**Hypothesis:** Framing delegation as a speed optimization ("Sub-agents make you faster")
rather than a mandate produces better delegation behavior — more parallelism, less
analysis paralysis at the coordinator level.

**Change:** Rewrote `## Role: Coordinator` and `## Sub-agents` sections in base.md.
Adopted codex-rs style: motivation-first framing, explicit parallelism encouragement,
simplified 5-step workflow.

**Tasks:** cancel-async-tasks (MODERATE-HARD), build-pmars (MODERATE-HARD),
overfull-hbox (MODERATE-HARD) for Phase 1. Expand to 8 tasks for Phase 2.

**What to look for:** Does the coordinator delegate sooner? Does it spawn parallel
agents? Does it avoid doing implementation work itself?

---

## Experiment 2: Inline Role Descriptions in spawn_agent

**Status:** Not started

**Hypothesis:** Embedding role descriptions directly in spawn_agent's `agent_type`
parameter description (like codex-rs does) helps the model choose the right agent type
and understand what each type can do — without reading separate docs.

**Change:** Modify `defSpawnAgent()` in `agent/profile.go` to include role descriptions
in the `agent_type` parameter's enum description, e.g.:
```
"implementer" — Writes code, runs tests, creates files. Best for concrete coding tasks.
"researcher" — Reads files, searches code, explores. Best for understanding codebases.
```

**Risk:** Token cost per tool call increases. May confuse simpler models.

**What to look for:** Does the model choose agent types more appropriately? Fewer
mismatches between task complexity and agent type?

---

## Experiment 3: Skills-in-System-Prompt Rollback

**Status:** Not started

**Hypothesis:** The skills-in-system-prompt change (rendering skill content directly into
the system prompt) may be hurting more than helping by bloating context. The old approach
(skills as separate files the agent can read) was leaner.

**Change:** Revert the skills-in-system-prompt behavior so skills are NOT rendered inline.
Keep the skill metadata available for the agent to request.

**What to look for:** Does the agent use fewer rounds on setup? Does it still follow
TDD and verification patterns? Does pass rate change?

**Note:** This was the change between e6756a2 and the reverted baseline. The reverted
baseline (which KEPT this change) scored 64%, so it may not be hurting. But worth isolating.

---

## Experiment 4: task_list Simplification

**Status:** Not started

**Hypothesis:** The detailed `## task_list` section in base.md may conflict with the
parallelism framing. The current text encourages sequential work ("dispatching one
implementer per subtask and verifying each before moving to the next") which contradicts
"parallelize independent work."

**Change:** Simplify task_list section to:
- Keep decomposition guidance
- Remove the sequential "verify each before moving to next" language
- Add: "For independent subtasks, dispatch agents in parallel"

**Risk:** May reduce verification quality if the model skips checking between subtasks.

**What to look for:** Does the agent decompose AND parallelize? Or does it skip
decomposition entirely?

---

## Experiment 5: Subagent Base Prompt Enrichment

**Status:** Not started

**Hypothesis:** subagent_base.md is very generic. Adding task-type-specific guidance
(e.g., "if writing code, write tests first" directly in the base) may improve subagent
quality without requiring skill loading.

**Change:** Add 2-3 critical directives to subagent_base.md:
- "Run existing test suites before reporting done"
- "If the task involves writing code, write a test first"
- "If the task involves configuring a service, verify it's running before reporting done"

**Risk:** Subagent prompt bloat. May conflict with skills that are loaded.

**What to look for:** Do subagents produce higher-quality work on first attempt?
Fewer fix-up rounds at the coordinator level?

---

## Experiment 6: Coordinator Tool Restriction

**Status:** Not started (structural, not prompt)

**Hypothesis:** The coordinator has write_file/edit_file/exec_command available, which
tempts it to do implementation work directly. Removing write/edit tools from the
coordinator's tool set at depth 0 would FORCE delegation.

**Change:** In `agent/profile.go` or session setup, filter out write/edit tools when
depth == 0.

**Risk:** Coordinator can't write scratch notes, can't do quick fixes. May need to
keep exec_command for running tests/verification.

**What to look for:** Does 100% delegation actually improve results? Or does the
overhead of spawning agents for trivial tasks hurt?

**Note:** This is a CODE change, not a prompt change. Higher risk, higher potential.

---

## Experiment 7: Reasoning Effort Tuning

**Status:** Not started

**Hypothesis:** reasoning_effort=high may be overkill for subagents doing simple tasks.
Using "high" for the coordinator and "medium" for subagents could save tokens/time
without hurting quality.

**Change:** Pass reasoning_effort per-spawn based on agent type or task complexity.

**Risk:** Subagent quality may drop below acceptable threshold.

**What to look for:** Wall time reduction. Token savings. Pass rate comparison.

---

## Priority Order

1. **Delegation Framing** (in progress) — low risk, tests codex-rs insight
2. **Inline Role Descriptions** — low risk, complements delegation framing
3. **task_list Simplification** — low risk, fixes internal contradiction
4. **Skills Rollback** — medium risk, isolates a known variable
5. **Subagent Base Enrichment** — medium risk, cross-cutting
6. **Coordinator Tool Restriction** — high risk, structural change
7. **Reasoning Effort Tuning** — optimization, do after finding best prompt

---

## Experiment Log

(Results go here as experiments complete)

### delegation-framing-v1

**Hypothesis:** Framing delegation as speed optimization (codex-rs style) improves
delegation behavior — more parallelism, better coordinator discipline.

**Commit:** 1d3b03f (main)

#### Phase 1 Results (delegation-framing-v1-p1)
- **Job:** delegation-framing-v1-p1
- **Tasks:** cancel-async-tasks, build-pmars, overfull-hbox (1 rep each)
- **Pass rate:** 1/2 valid tasks (build-pmars was setup failure — install script bug)
  - overfull-hbox: PASS (verifier 4/4 tests, despite agent timeout at 750s)
  - cancel-async-tasks: FAIL (verifier 5/6 tests, SIGINT cleanup bug)
  - build-pmars: SETUP FAILURE (libatlas-base-dev unavailable, git not installed)
- **Behavioral observations:**
  - Both coordinators followed full delegation workflow (plan → implement → verify → submit)
  - Neither coordinator tried to write code directly — the prompt is working
  - cancel-async-tasks used parallelism (test-engineer + reviewer in parallel)
  - overfull-hbox did NOT use parallelism (all sequential blocking calls)
  - cancel-async-tasks spawned 6 subagents (2 were wasteful cleanup agents)
  - overfull-hbox spawned 3 subagents (planner, implementer, reviewer)
  - overfull-hbox reviewer was pathologically thorough (downloaded Gutenberg text, ran out of budget)
  - cancel-async-tasks needed STEERING correction (text instead of communicate)
  - cancel-async-tasks failure was validation gap: all 3 validators tested programmatic
    cancel, not SIGINT from external process. Implementation bug is real but all validators missed it.
- **Decision:** Proceed to Phase 2 — delegation behavior is correct, but need more data points
  for pass rate signal. Swap build-pmars for a task without setup issues. Consider:
  replace build-pmars with largest-eigenval (MODERATE tier, no special env needs)

#### Phase 2 Results (delegation-framing-v1-p2)
- **Job:** delegation-framing-v1-p2
- **Tasks:** 8 tasks x 2 reps = 16 trials
- **Pass rate:** 10/16 (62.5%)
- **Baseline comparison:** 6/30 (20%) across all prior runs on these same 8 tasks

| Task | Tier | Baseline | P2 | Change |
|------|------|----------|----|----|
| cancel-async-tasks | MOD-HARD | 0/3 | 1/2 | **NEW PASS** |
| overfull-hbox | MOD-HARD | 0/4 | 1/2 | **NEW PASS** |
| largest-eigenval | MODERATE | 2/5 | 1/2 | ~same |
| path-tracing | VERY HARD | 0/5 | 1/2 | **NEW PASS** |
| winning-avg-corewars | HARD | 0/5 | 2/2 | **NEW PASS** |
| qemu-startup | HARD | 2/2 | 2/2 | no regression |
| multi-source-data-merger | MOD-HARD | 2/3 | 2/2 | ~same |
| filter-js-from-html | MODERATE | 0/3 | 0/2 | still failing |

- **Failure modes:**
  - 2 timeouts (overfull-hbox rep 2, path-tracing rep 2)
  - filter-js-from-html 0/2 (persistent, likely needs different approach)
  - cancel-async-tasks, largest-eigenval each 1/2 (nondeterministic)
- **No regressions** on tasks that already passed (qemu-startup, multi-source-data-merger)
- **4 previously-impossible tasks now passing** at >=50% rate
- **Caveats:** Baseline spans multiple configs (some without reasoning_effort=high).
  But even tasks that were 0/4+ across ALL configs are now passing.
- **Decision:** Strong signal. This prompt change is a clear improvement. Merge to main
  and proceed with next experiment (inline role descriptions). Consider Phase 3
  (full 89-task run) to validate no regressions on broader task set.
