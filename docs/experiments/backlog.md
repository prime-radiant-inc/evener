# Experiment Backlog

Prioritized queue of next experiments. Updated March 28, 2026.

## Baseline

**wave-08b8a7f-20260328** — 88/89 tasks × 3 reps, mean 0.504 (33 pass, 55 fail).
See experiment-log.md for full failure inventory from interrogation of all 55 failures.

## Next up

Priority order based on failure inventory. Ordered by number of affected tasks
and likelihood of prompt-level fix (vs genuine capability ceiling).

### 1. Anti-analysis-paralysis — force early deliverables

**Pattern A: 10 tasks, all 0/3.** Highest-impact fix by task count.

**Problem:** Implementer spends entire turn budget on research/reading and never
produces deliverable files. Existing "start building early" instruction is ignored.

**Hypotheses:**
- **A) Hard gate:** "You MUST write your first deliverable file within your first
  10 tool calls. Iterate on it afterward. A partial deliverable is infinitely
  better than no deliverable."
- **B) Progress checkpoint:** Detect when implementer has done >20 rounds of
  read/grep without any write/apply_patch, inject a warning message.
- **C) Deliverable-first framing:** Rewrite implementer intro: "Your job is to
  produce deliverable files. Research is only valuable insofar as it helps you
  write better files. Start writing immediately, then refine."

**Test tasks (3 reps each):** mailman, make-doom-for-mips, path-tracing-reverse,
dna-assembly, train-fasttext
**Regression set:** build-pmars, constraints-scheduling, crack-7z-hash,
prove-plus-comm, pytorch-model-cli

### 2. Coordinator verification depth

**Pattern B: 8 tasks.** Second-highest impact.

**Problem:** Coordinator checks artifact existence but not correctness. The
"Do NOT re-derive the answer independently" instruction is over-applied —
coordinators interpret it as "don't verify at all."

**Hypotheses:**
- **A) Bounded verification:** "You must not re-derive the full answer, but you
  MUST verify deliverables against specific acceptance criteria: run tests if
  available, check output format matches spec, sanity-check numerical values."
- **B) Verifier-first:** "Before accepting implementer output, run any test
  scripts in /tests/ or the workspace. If they fail, reject and re-delegate."

**Test tasks:** compile-compcert, raman-fitting, query-optimize,
model-extraction-relu-logits, mcmc-sampling-stan
**Regression set:** cobol-modernization, fix-git, hf-model-inference,
largest-eigenval, sqlite-with-gcov

### 3. Run test suite instruction

**Pattern C: 7 tasks.** Overlaps with pattern B but distinct — the issue is
the implementer not running available tests, not just the coordinator.

**Hypotheses:**
- **A) Test-first instruction for implementer:** "Before communicating
  completion, if test files exist in /tests/ or the workspace, run them. Do not
  declare success until tests pass."
- **B) Read tests before building:** "If test files exist, read them FIRST to
  understand exact acceptance criteria (field names, paths, formats)."

**Test tasks:** install-windows-3.11, dna-insert, sanitize-git-repo,
portfolio-optimization, polyglot-c-py
**Regression set:** bn-fit-modify, code-from-image, distribution-search,
openssl-selfsigned-cert, regex-log

### 4. Anti-bypass reinforcement

**Pattern D: 4 tasks.** Known problem, existing fixes partially effective.

**Problem:** Coordinator implements directly. ops-task removal helped but
some tasks still trigger bypass (chess-best-move, custom-memory-heap-crash,
fix-ocaml-gc, password-recovery).

**Hypotheses:**
- **A) Tool-gating:** If coordinator's first 3 tool calls don't include
  spawn_agent, inject: "REMINDER: You are the coordinator. Delegate to an
  implementer."
- **B) Structural:** Remove write_file/apply_patch from coordinator's tool
  palette entirely (risky — may break legitimate coordinator file operations).

**Test tasks:** chess-best-move, custom-memory-heap-crash, fix-ocaml-gc,
password-recovery
**Regression set:** crack-7z-hash, git-leak-recovery, headless-terminal,
qemu-startup, sparql-university

### 5. Anti-fabrication instruction

**Pattern F: 3 tasks.** Small count but concerning behavior.

**Problem:** Agent fabricates data or takes shortcuts (calling original binary)
when stuck, instead of reporting inability.

**Hypotheses:**
- **A) Honesty gate:** "If you cannot complete a task or verify your output
  from primary sources, communicate that you were unable to complete it. NEVER
  fabricate data, guess at outputs, or delegate to the original binary."
- **B) Verification requirement:** "Every value in your output must be traceable
  to a specific computation or source. If you cannot cite the source, do not
  include the value."

**Test tasks:** db-wal-recovery, extract-moves-from-video, path-tracing
**Regression set:** count-dataset-tokens, merge-diff-arc-agi-task,
multi-source-data-merger, reshard-c4-data

### 6. Cleanup instruction refinement

**Pattern G, 1 clear case.** Low effort, high certainty.

**Problem:** portfolio-optimization deleted its own compiled .so during cleanup.

**Fix (no experiment needed):** Change cleanup instruction from "only
deliverable files should remain" to "only deliverable files and their build
artifacts should remain. Compiled libraries (.so, .dll) that your solution
depends on are deliverables, not build artifacts."

### 7. Spec precision instruction

**Pattern E: 5 tasks.** Hard to fix generically — each mismatch is different.

**Hypotheses:**
- **A) Exact-match framing:** "When the task specifies field names, paths,
  formats, or schemas, match them EXACTLY. Do not use synonyms or abbreviations
  for API fields (e.g., if spec says 'value', use 'value', not 'val')."
- **B) Read tests first (overlaps with #3):** Test files often reveal exact
  expected formats.

**Test tasks:** kv-store-grpc, extract-elf, adaptive-rejection-sampler,
overfull-hbox
**Regression set:** pytorch-model-recovery, sqlite-db-truncate, tune-mjcf

## Execution order

1. **Anti-analysis-paralysis** — highest impact (10 tasks), clear intervention
2. **Run test suite** — high impact, overlaps with coordinator verification
3. **Coordinator verification depth** — combine with #2 if both are prompt changes
4. **Cleanup refinement** — trivial fix, ship immediately
5. **Anti-bypass reinforcement** — known problem, incremental improvement
6. **Anti-fabrication** — important but few affected tasks
7. **Spec precision** — hardest to fix generically

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
- Eval tooling: run_eval.sh (unified launcher with wave mode), run_status.sh, post_run.sh
- Wave launcher: wave_launcher.py orchestrator

### Tried and superseded (no longer active)
- Scratch directory verification — v9/v10: still violated despite instruction
- Active pre-submit workspace check — v9/v10: still violated
- Anti-gold-plating — v10: overridden by competing instructions (resolved differently in v11)
- Reviewer consistency check — v9: 2/3, v10: 0/3 (superseded by v11 positive authority)
- v25 reviewer experiments — all made things worse, do not revisit
- impl-test-a — 3/3 was stochastic, not shipped
