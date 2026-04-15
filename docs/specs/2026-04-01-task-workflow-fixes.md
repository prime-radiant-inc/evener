# Task-Driven Workflow Fixes — Session 10 Findings & Plan

## What we learned

The task-driven workflow (structured YAML tasks with per-task reasoning effort)
is a net positive: 22 tasks improved vs 10 regressed, with 28 perfect scores.
But root-cause analysis of every regression revealed three design problems
that are causing previously-passing tasks to fail.

### Problem 1: Verification instruction reordering (SYSTEMIC)

When the coordinator workflow moved from inline prose to YAML tasks, the Verify
step's text was rewritten to lead with the prohibition ("MUST NOT reproduce")
before the exception ("running deliverable IS allowed"). The old inline version
led with the exception.

**Impact:** large-scale-text-editing (0→1.0), financial-document-processor
(0→0.67), sparql-university, sqlite-db-truncate, sqlite-with-gcov,
constraints-scheduling, cobol-modernization — coordinators cite the prohibition
as why they didn't run deliverables against actual data.

**Fix applied:** Commit 1e2d79a rewrote the Verify task to lead with "You MUST
check deliverable correctness against acceptance criteria." Tested on 6 tasks —
large-scale-text-editing recovered 1/3 (was 0/3), qemu-startup went 3/3.
Partially effective but not sufficient alone.

### Problem 2: xhigh Plan causes coordinator to do implementer's work (SYSTEMIC)

With `reasoning_effort: xhigh` on the Plan step, the coordinator runs at
maximum thinking budget and starts solving the problem itself instead of
writing a delegation prompt.

**Evidence (tune-mjcf):** Coordinator spent 71,562 reasoning tokens (rounds 2-15)
reading files, running eval.py, testing solver configurations. Then spawned
ONE implementer with a 92-character delegation. Baseline coordinator: 3,298
tokens, 1,506-character delegation. The implementer failed because it had
no context.

**Evidence (schemelike):** Coordinator spawned implementer during Inventory
(before Plan), then spawned 2 more during Plan and Fix. 3 implementers vs
baseline's 1.

**Impact:** tune-mjcf (0.67←1.0), schemelike-metacircular-eval (0.33←1.0),
sqlite-db-truncate, sqlite-with-gcov. Coordinator burns tokens on self-solving
instead of producing good delegations.

### Problem 3: Monolithic delegation vs narrow-iterative (DESIGN)

The "ONE implementer gets the whole problem" rule + parent_tasks gives the
implementer a single long session with many tasks. The baseline's pattern
was 3 narrow implementer sessions with coordinator verification between each.

**Evidence (tune-mjcf):** Baseline used 3 implementers (1 safe change each,
coordinator verified between). Task-driven used 1 implementer with 33 turns
who changed 6+ parameters and broke correctness. The implementer treated the
large task list as "license to experiment."

**Impact:** Concentrated on optimization/iteration tasks where the baseline's
narrow-iterative pattern was the success factor.

### Problem 4: Forced 6-step workflow overhead (OVERHEAD)

The YAML tasks force coordinators through 6 mandatory steps with task_list
bookkeeping at each transition, adding 5-12 coordinator rounds of overhead.

| Task | Baseline rounds | Task-driven rounds | task_list calls |
|------|----------------|-------------------|-----------------|
| schemelike | 8 | 15-21 | 6 |
| sqlite-truncate | 5 | 15 | 6 |
| sqlite-gcov | 5 | 13-17 | 7 |
| tune-mjcf | 8 | 16 | 3 |

## Experiment plan

### Experiment A: Effort level fix (h/l/l/l/l/l + "don't do the work")

**Change:**
- Plan: high (was xhigh) — enough for thorough delegation, not enough to self-solve
- Verify: low (was medium) — quick acceptance checks, not deep investigation
- Fix: low (was medium) — dispatch fix agent quickly
- Add to Plan task: "DO NOT read source files, run code, or test solutions
  during planning. Your job is to write the delegation prompt, not to do the
  implementer's work. Write the delegation and move to Delegate."

**Rationale:** Directly addresses Problem 2 (xhigh self-solving) and Problem 4
(medium Verify over-investigation). Preserves the task-driven structure.

**Target tasks (12 tasks × 3 reps):**
- Regressed tasks: tune-mjcf, schemelike-metacircular-eval, sparql-university,
  sqlite-with-gcov, sqlite-db-truncate
- Regression set controls: build-pov-ray, feal-linear-cryptanalysis, pypi-server,
  distribution-search, regex-log, portfolio-optimization, kv-store-grpc

**Success criteria:** Regressed tasks recover to ≥0.67 AND regression set holds.

### Experiment B: Allow narrow-iterative delegation

**Change:** Soften the "ONE implementer gets the whole problem" rule:
"Start with ONE implementer for the full task. If verification finds specific
failures, you may spawn focused fix agents with narrow scope. Each fix agent
should address ONE specific failure, not re-attempt the whole task."

This is already partially how the Fix step works, but the current rule discourages
it for the initial implementation. The change makes the coordinator's iteration
pattern closer to the baseline's narrow-iterative approach.

**Target tasks (6 tasks × 3 reps):**
- tune-mjcf, compile-compcert, cobol-modernization (tasks where baseline used
  narrow iterative spawns)
- build-pov-ray, regex-log, pypi-server (regression controls)

**Success criteria:** tune-mjcf recovers to 1.0, no regression set losses.

### Experiment C: Combined A+B

If A and B both show improvement independently, combine them and run:
- All 23 regressed tasks × 3 reps

**Success criteria:** Mean across regressed tasks improves vs current wave.

### Experiment D: Full 89-task baseline (if C validates)

Full 89 × 3 run with the combined fixes. Compare against:
- wave-da201c0 (HEAD baseline, 0.452)
- wave-6b5c963 + wave-b772fb6 (current task-driven, 0.455)

**Goal:** Beat both baselines with no net regressions.

## Execution order

1. Implement A → build → launch A's 12 target tasks
2. Implement B → build on separate branch → launch B's 6 target tasks
3. While A+B run: document all findings in experiment-log.md
4. Collect A results → if regression set holds, proceed
5. Collect B results → if target tasks improve, proceed
6. If both positive: implement C (A+B combined) → launch 23 regressed tasks
7. If C positive: launch D (full 89-task baseline)

## Verification checklist

- [ ] All Go tests pass after each change
- [ ] Binary SHA matches HEAD
- [ ] Regression set tasks pass at ≥ current rate
- [ ] No new 0/3 regressions on tasks that currently pass

## Classification of all 23 regressed tasks

### Likely recoverable with Experiment A (effort + verification fix)
- large-scale-text-editing (0.00←1.0) — verification instruction
- financial-document-processor (0.00←0.67) — verification instruction
- sparql-university (0.33←1.0) — verification instruction
- sqlite-with-gcov (0.33←1.0) — verification + coordinator over-investigation
- sqlite-db-truncate (0.67←1.0) — verification instruction
- constraints-scheduling (0.67←1.0) — coordinator couldn't verify answer
- cobol-modernization (0.33←1.0) — only one code path tested
- configure-git-webserver (0.67←1.0) — verification of running services

### Likely recoverable with Experiment B (narrow-iterative)
- tune-mjcf (0.67←1.0) — baseline used 3 narrow implementers
- compile-compcert (0.50←1.0) — baseline used iterative approach

### Stochastic (may improve with A/B but fundamentally variable)
- schemelike-metacircular-eval (0.33←1.0) — capability boundary + overhead
- count-dataset-tokens (0.67←1.0) — dataset interpretation variance
- extract-elf (0.33←1.0) — capability boundary
- feal-differential-cryptanalysis (0.33←1.0) — introspection shortcut
- kv-store-grpc (0.67←1.0) — field name normalization
- polyglot-c-py (0.67←1.0) — build artifact cleanup
- merge-diff-arc-agi-task (0.67←1.0) — ARC pattern generalization

### Stochastic / task difficulty (unlikely to fix with prompt changes)
- chess-best-move (0.00←1.0) — vision model unreliable + "enumerate all" missed
- winning-avg-corewars (0.00←1.0) — genuine task difficulty
- caffe-cifar-10 (0.33←1.0) — timing/infrastructure
- bn-fit-modify (0.33←1.0) — DAG recovery probabilistic
- password-recovery (0.67←1.0) — ambiguous spec interpretation
- qemu-startup (0.67←1.0) — QEMU config highly variable

### Infrastructure
- mcmc-sampling-stan (missing) — Docker CPU limit, needs larger instance
- sanitize-git-repo (0.67) — historically flaky, not a regression
