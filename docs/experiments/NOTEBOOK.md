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

## Current State (March 27, 2026)

**Model:** gpt-5.4-mini for current eval iteration
**Scoreboard:** `./tools/scoreboard.py` — canonical 89-task matrix
**Score:** 83/89 tested, mean 0.608

### Pending runs

- **full-baseline-2026-03-27** — all 89 tasks × 3 reps, gpt-5.4-mini, main @ 5554bed.
  Clean baseline with all shipped fixes. 3 × c6i.2xlarge, concurrency 8. Running.
  When complete: `./tools/post_run.sh full-baseline-2026-03-27`

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

### Known open problems

1. **Delegation info loss** — coordinator paraphrases specs, losing exact formats.
   No good general fix yet. ~60% of affected tasks pass anyway.
2. **Coordinator bypass (stochastic)** — vision steering content varies per run,
   sometimes priming direct action. Appears stochastic, not systematic.
3. **Self-referential verification** — agent can't detect own schema mismatches
   (e.g., proto field `val` vs `value`). Structural, not prompt-fixable.

### What to do next

1. Collect full-baseline-2026-03-27 results when complete
2. Auto-interrogate every failure: `./tools/interrogate_failures.sh full-baseline-2026-03-27`
3. Build failure inventory from interrogation findings
4. Start hill-climbing from `backlog.md` priority list
5. See `experiment-log.md` for full history of what's been tried

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
