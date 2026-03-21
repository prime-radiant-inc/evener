# Serf Optimization Notebook

Living document tracking the current experimental state. Read this first when starting
a new session.

## Current State (March 21, 2026)

**Shipped code:** commit 5921528 on main, pushed to GitHub
**Model:** gpt-5.4 for evals
**Baseline:** 56/88 = 64% on full 89-task terminal-bench (job: `full-89-ef120d4`)
**High water mark:** 75/89 (84%) tasks ever passed across all runs
**Skill:** `~/.claude/skills/benchmark-driven-improvement/SKILL.md`

## What's Been Done

### Phase 1-4 (March 13-20)
Shipped: tightened core.md, typed tasks + auto-verify, tool-level read-only verify,
reasoning escalation, coordinator delegation (R2 variant). 52% → 89% on 3 hard tasks.
Full writeup: `docs/experiments/2026-03-17-gepa-prompt-optimization.md`

### Phase 5 (March 20-21)
- **Delegation enforcement:** Tested tool restriction, graphviz flowcharts, prose framing.
  Result: prose with "CRITICAL" markers works (3/3), graphviz doesn't work with GPT (0/7).
- **Model upgrade:** gpt-5.3-codex → gpt-5.4. Significant improvement.
- **Vision capability:** Added vision section to core.md. Ran 12 prompt variants for
  chess-best-move. Overlay experiments (system_prompt_append) passed 3/5; core.md versions
  passed ~1/5. Vision works but prompt placement matters.
- **Full eval:** 56/88 = 64% on all 89 tasks.
- **Root cause analysis:** All 22 non-too-hard failures root-caused from actual transcripts.
  Found 3 systemic patterns: write-last antipattern, workspace contamination, confirmation bias.
- **Tools built:** iterate_task.py for single-task iteration with diagnostic reports.

## What's Next

**Active work plan:** `docs/experiments/2026-03-21-failure-inventory.md`

Execute in this order:

### 1. Chess-best-move regression investigation
The overlay experiments (prompt only on coordinator) passed 3/5, but core.md versions
pass ~1/5. Need to understand why and get a reliable solution.

**Hypothesis:** Coordinator describing the image and passing text to implementer works
better than implementer seeing the image directly.

**Test plan:** Revert core.md vision to minimal, add description guidance to coordinator.md
only. Run 5 reps.

### 2. Write-early fix (3 tasks)
chess-best-move, gcode-to-text, query-optimize all had answers but never wrote files.

**Fix:** Add to core.md: "Write your best answer to the output file as soon as you have
one. You can always improve it later."

**Test:** Run all 3 tasks × 3 reps.

### 3. Workspace contamination fix (4 tasks)
db-wal-recovery, configure-git-webserver, git-multibranch, polyglot-c-py.

**Fix:** Add to coordinator.md: "Your verification must not leave permanent changes."

**Test:** Run all 4 tasks × 3 reps.

### 4. Read-tests fix (3 tasks)
polyglot-c-py, sqlite-with-gcov, install-windows-3.11.

**Fix:** Strengthen coordinator step 2 to explicitly check /tests/.

**Test:** Run all 3 tasks × 3 reps.

### 5. Escalate-contradictions fix (1 task)
fix-code-vulnerability (390/391 pass, wrong CWE).

**Fix:** Add to subagent.md: report contradictory evidence.

**Test:** Run × 3 reps.

### 6. Full validation
After all individual fixes validated, run full discriminator set.

## Key Learnings (for future sessions)

- **Graphviz doesn't work with GPT.** 0/7 compliance. Use prose with CRITICAL markers.
- **Prohibitions don't work with GPT.** "NEVER do X" → model does X via workaround.
  Use positive framing: "Before X, try Y first."
- **Root cause from transcripts, not error messages.** Error messages mislead.
  Read what the agent actually did step by step.
- **Test in isolation before full eval.** 3+ reps on affected tasks only.
- **Never reuse job names.** The launch script rm -rf's the job directory.
- **system_prompt_append only reaches root session.** For prompts that need to reach
  subagents, change core.md.
- **The coordinator's verification can contaminate the workspace.** sqlite3, git push,
  test scripts can all leave permanent changes.

## Experiment Log

| Date | Experiment | Tasks | Result | Notes |
|------|-----------|-------|--------|-------|
| 3/20 | H1: tool restriction | polyglot, ars ×2 | 0/2 | Coordinator bypassed via shell heredocs |
| 3/20 | H1+H6: restriction + shell ban | polyglot, ars ×2 | 0/3 | Model hallucinated |
| 3/20 | H8: dispatcher framing | polyglot, ars ×2 | 1/3 delegated | Weaker framing |
| 3/20 | H9: CRITICAL implementer | polyglot, ars ×2 | 3/3 delegated | Best delegation |
| 3/20 | Graphviz complex | polyglot, ars ×2 | 0/3 delegated | GPT ignores flowcharts |
| 3/20 | Graphviz simple | polyglot, ars ×2 | 0/4 delegated | Same |
| 3/20 | Prose + verify-fix | 3 tasks ×2 | 0/4 pass | Delegation OK, impl quality low |
| 3/21 | Full discriminator gpt-5.4 | 56 tasks ×1 | 35/53 (66%) | Model upgrade helps |
| 3/21 | Vision v1: say out loud | chess ×1 | PASS | (overlay, timeout) |
| 3/21 | Vision v2: describe before code | chess ×1 | PASS | Best efficiency, 11m |
| 3/21 | Vision v3: LOOK THINK CODE | chess ×1 | FAIL | Too prescriptive |
| 3/21 | Vision v4: no pixel code | chess ×1 | FAIL | Prohibition ignored |
| 3/21 | Vision v5: write early | chess ×1 | PASS | 12m |
| 3/21 | Vision v6: describe+connect | chess ×1 | FAIL | |
| 3/21 | Vision v7: describe fully | chess ×1 | FAIL | Most efficient (5 reads) but wrong |
| 3/21 | Vision v8: intention first | chess ×1 | FAIL | |
| 3/21 | Vision v9: combined 1+2+5+7 | chess ×1 | FAIL | |
| 3/21 | Vision v10: combined no early | chess ×1 | FAIL | Wrong move |
| 3/21 | Vision core-md negative | chess ×2 | 0/2 | Wrong moves |
| 3/21 | Vision core-md positive | chess ×2 | 0/2 | Wrong moves |
| 3/21 | Vision describe+verify (core.md) | chess ×2 | 1/2 | Correct answer on rep 2 |
| 3/21 | Full 89-task eval | 89 tasks ×1 | 56/88 (64%) | Baseline for fix work |
