# Serf Optimization Notebook

Current experimental state. Read this first when starting a new session.

**Related docs:**
- `experiment-log.md` — full chronological record of all experiments and results
- `prompt-lessons.md` — synthesized learnings about GPT prompt behavior
- `backlog.md` — prioritized queue of next experiments
- `task-sets.md` — regression set, hard tasks, named sets
- `infrastructure.md` — how to build, deploy, and run evals

**Methodology:** The `benchmark-driven-improvement` skill defines the experimental
process — hill-climbing protocol, root-cause-from-transcripts, commit-before-deploy.

## Current State (March 28, 2026)

**Model:** gpt-5.4-mini for current eval iteration
**Scoreboard:** `./tools/scoreboard.py` — canonical 89-task matrix
**Score:** 88/89 tested, mean 0.504

### Latest baseline: wave-08b8a7f-20260328

First clean 3-rep baseline from a single commit (main @ 08b8a7f, all shipped fixes).
Wave mode: one task per c6i.xlarge instance, 32 concurrent, ~2h total.
264/267 results (filter-js-from-html missing due to wave launcher bug, now fixed).

**33 pass (3/3):** bn-fit-modify, build-pmars, build-pov-ray, cobol-modernization,
code-from-image, constraints-scheduling, count-dataset-tokens, crack-7z-hash,
distribution-search, feal-differential-cryptanalysis, fix-git, git-leak-recovery,
headless-terminal, hf-model-inference, largest-eigenval, log-summary-date-ranges,
merge-diff-arc-agi-task, modernize-scientific-stack, multi-source-data-merger,
nginx-request-logging, openssl-selfsigned-cert, prove-plus-comm, pypi-server,
pytorch-model-cli, pytorch-model-recovery, qemu-startup, regex-log,
reshard-c4-data, schemelike-metacircular-eval, sparql-university,
sqlite-db-truncate, sqlite-with-gcov, tune-mjcf

**55 fail** (see experiment-log.md for full breakdown)

**Regressions vs prior scores:** compile-compcert (1.0→0.0), configure-git-webserver
(1.0→0.0), db-wal-recovery (1.0→0.0), path-tracing (1.0→0.0) are most concerning.
Prior scores were from different runs at different times — this baseline is the most
trustworthy snapshot.

### Shipped fixes (on main)

| Fix | What changed | Task improved |
|-----|-------------|---------------|
| deleg-b | coordinator.md: "quality gate, not the worker" | chess-best-move 3/3 |
| state-b | implementer.md: post-test mutation check | git-multibranch 3/3 |
| v17+v18 | coordinator.md: harmonize HARD GATE + no-tests case | log-summary-date-ranges 3/3 |
| v23-B | reviewer.md: remove "intuit", spec authority | chess-best-move 3/3 |
| ops-task removal | agent/skills/: deleted skill that primed direct implementation | crack-7z-hash 3/3 |
| coordinator inventory | coordinator.md: "listing, not reading" + anti-rationalization | custom-memory-heap-crash 2/3→3/3 |
| v26 task_list | task_reminders.go: neutral phrasing | custom-memory-heap-crash 3/3 |
| v11 positive authority | reviewer.md + workflow: authority ordering, warnings≠failures | chess+polyglot 6/6 |
| v13 tests-first | coordinator.md: tests-first verify, submit gate | fix-code-vuln+git-multi 6/6 |

**Not shipped:** impl-test-a — 3/3 was stochastic (model happened to pick right
field name), not a causal prompt fix.

### Known open problems (from interrogation of 55 failures)

1. **Analysis paralysis** (10 tasks, all 0/3) — implementer spends entire budget
   on research, never produces deliverables. Highest-priority fix.
2. **Shallow coordinator verification** (8 tasks) — coordinator checks existence
   not correctness. "Do NOT re-derive" over-applied as "don't verify."
3. **Never runs test suite** (7 tasks) — test files exist but agent doesn't run them.
4. **Coordinator bypass** (4 tasks) — coordinator implements directly.
5. **Spec/format mismatch** (5 tasks) — wrong field names, output format, API contract.
6. **Fabrication** (3 tasks) — agent invents data when stuck.
7. **Genuine difficulty** (12 tasks) — capability ceiling, not prompt-fixable.

### What to do next

1. Fix interrogation tooling bug (verifier output cross-contamination)
2. Start hill-climbing from `backlog.md` priority list:
   - **Experiment 1:** Anti-analysis-paralysis (10 tasks affected)
   - **Experiment 2:** Run-test-suite instruction (7 tasks)
   - **Experiment 3:** Coordinator verification depth (8 tasks)
   - **Quick fix:** Cleanup instruction refinement (portfolio-optimization)
3. See `experiment-log.md` for full failure inventory and interrogation findings

### Local harnesses

- **Coordinator delegation test** (`tools/coord-repro/`): ~2 min/run, binary
  DELEGATE vs BYPASS signal. Lower bypass rate than AWS — smoke test only.
- **Implementer repro** (`tools/impl-repro/`): ~3 min/run, sends delegation
  message directly, bypasses coordinator.

### Results system

- S3 canonical: `s3://harbor-eval-results-526275945504/runs/RUN_ID/`
- Local cache: `~/.serf-evals/tasks/{task}/{run}/{rep}/`
- Git-tracked: `scoreboard.json`, `runs/*.json`, `tasks/*.json`
- Scoring: mean of reps from last run; same-date tiebreak = highest score
