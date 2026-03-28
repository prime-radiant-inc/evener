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
**Score:** 88/89 tested, mean 0.504 (33 perfect, 23 partial, 32 zero)
**Baseline run:** wave-08b8a7f-20260328 (main @ 0d224b5, 88 tasks × 3 reps)

### Failure inventory summary

All 55 failing reps interrogated with corrected tooling. Full inventory in
`experiment-log.md` under wave-08b8a7f-20260328.

| Pattern | Tasks | % | Fixable? |
|---------|-------|---|----------|
| Shallow verification | 20 | 36% | Yes — coordinator test gate |
| Genuine difficulty | 9 | 16% | Unlikely by prompt alone |
| Analysis paralysis | 8 | 15% | Yes — deliverable-first checkpoint |
| Spec mismatch | 7 | 13% | Yes — spec-literal naming |
| Fabrication | 3 | 5% | Yes — anti-fabrication strengthening |
| Coordinator bypass | 3 | 5% | Partially addressed |
| Environment issue | 2 | 4% | Infrastructure fix |
| Other | 3 | 5% | Case-by-case |

**Key insight:** 36% of failures share one root cause — the coordinator does not
run the verifier tests before submitting. This is the single highest-leverage fix.

### Shipped fixes (on main)

| Fix | What changed | Task improved |
|-----|-------------|---------------|
| deleg-b | coordinator.md: "quality gate, not the worker" | chess-best-move 3/3 |
| state-b | implementer.md: post-test mutation check | git-multibranch 3/3 |
| v17+v18 | coordinator.md: harmonize HARD GATE + no-tests case | log-summary-date-ranges 3/3 |
| v23-B | reviewer.md: remove "intuit", spec authority | chess-best-move 3/3 |
| ops-task removal | agent/skills/: deleted skill that primed direct implementation | crack-7z-hash 3/3 |
| coordinator inventory | coordinator.md: "listing, not reading" + anti-rationalization | custom-memory-heap-crash 2/3->3/3 |
| v26 task_list | task_reminders.go: neutral phrasing | custom-memory-heap-crash 3/3 |
| v11 positive authority | reviewer.md + workflow: authority ordering, warnings!=failures | chess+polyglot 6/6 |
| v13 tests-first | coordinator.md: tests-first verify, submit gate | fix-code-vuln+git-multi 6/6 |

**Not shipped:** impl-test-a — 3/3 was stochastic (model happened to pick right
field name), not a causal prompt fix.

### What to do next

1. **Experiment 1: Coordinator test gate** — highest priority, targets 20 tasks.
   Require coordinator to run `run_tests` as final step before `communicate`.
   See `backlog.md` for test plan.
2. **Experiment 2: Deliverable-first checkpoint** — targets 8 tasks.
   Hard limit on analysis-only tool calls before first deliverable write.
3. **Experiment 3: Spec-literal naming** — targets 7 tasks.
   Instruction to use exact spec wording for field names/formats.
4. See `backlog.md` for experiments 4-6.

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
