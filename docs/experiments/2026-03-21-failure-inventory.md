# Failure Inventory and Fix Plan

**Source:** full-89-ef120d4 eval, gpt-5.4, March 21 2026
**Baseline:** 56/88 pass (64%). High water mark across all runs: 75/89 (84%).
**Goal:** Systematically fix failures, test in isolation, document results.

## Process

For each failure:
1. Root cause is documented (from transcript analysis)
2. Proposed fix is specific and testable
3. Test the fix on ONLY the affected task, 3 reps, using iterate_task.py
4. Record pass rate before and after
5. If fix works (2/3+), commit it
6. If fix regresses other tasks, revert
7. After all fixes: full eval to measure aggregate impact

**Rules:**
- Never reuse job names. Always use auto-generated names or iterate_task.py.
- Always collect results before launching new jobs.
- Don't run full evals until individual fixes are validated.
- Document every experiment outcome, pass or fail.

## Failure Categories

### Category 1: Write-Last Antipattern (3 tasks)
Agent has the answer but never writes the deliverable file.

| Task | Pass rate | Root cause | Proposed fix |
|------|-----------|------------|--------------|
| chess-best-move | 0/1 (but 3/5 in overlay experiments) | Implementer spent 120 turns on template matching, never wrote move.txt | See "chess-best-move investigation" below |
| gcode-to-text | 0/1 | OCR got answer at turn 123, started another preprocessing pass | Strengthen "write EARLY" in core.md or subagent.md |
| query-optimize | 0/1 (but 1/3 in earlier eval) | Had 0.4s query at turn 7, stuck verifying equivalence | Same write-early fix |

**Proposed fix:** Strengthen the "write deliverable files EARLY" instruction. Currently in coordinator.md delegation guidelines. Needs to be in core.md so implementers see it. Specific language: "Write your best answer to the output file as soon as you have one. You can always improve it later. An imperfect deliverable is infinitely better than no deliverable."

**Test plan:** Run chess-best-move, gcode-to-text, query-optimize × 3 reps each after the fix.

### Category 2: Coordinator Contaminates Workspace (4 tasks)
The coordinator's own actions change workspace state, breaking the verifier.

| Task | Pass rate | Root cause | Proposed fix |
|------|-----------|------------|--------------|
| db-wal-recovery | 0/1 (but 5/7 verifier) | `sqlite3` opened DB, SQLite deleted obfuscated WAL | Warn coordinator about destructive reads |
| configure-git-webserver | 0/1 | Coordinator pushed test data during verification | Tell coordinator to reset state after testing |
| git-multibranch | 0/1 | Coordinator's verify.sh left stale branches | Same reset-state fix |
| polyglot-c-py | 0/1 | Coordinator told implementer to keep cmain | Tell coordinator to check /tests/ for cleanup expectations |

**Proposed fix:** Add to coordinator.md: "Your verification must not leave permanent changes. If you push to a repo, reset it. If you create test files, remove them. After verifying, the workspace should be in the same state the implementer left it — or cleaner."

**Test plan:** Run all 4 tasks × 3 reps after the fix.

### Category 3: Trivial Bugs (3 tasks)
One-line fixes that don't require prompt changes.

| Task | Pass rate | Root cause | Fix |
|------|-----------|------------|-----|
| mcmc-sampling-stan | 5/6 | `refresh = 0` suppresses progress messages | Can't fix via prompt — model choice |
| dna-insert | 0/1 | Trailing newline in FASTA file | Can't fix via prompt — model formatting |
| regex-chess | 1/4 | FEN fields 3 and 4 swapped | Ran out of turns debugging — time issue |

**These are implementation quality issues, not prompt issues.** More model capability or more turns would help. No prompt fix proposed.

### Category 4: Coordinator Doesn't Read Tests (3 tasks)
The coordinator delegates without checking what the verifier actually tests.

| Task | Pass rate | Root cause | Proposed fix |
|------|-----------|------------|--------------|
| polyglot-c-py | 0/1 | Didn't know verifier checks directory state | Coordinator should read /tests/ |
| sqlite-with-gcov | 2/3 | .gcda in build dir, verifier checks install dir | Same — read tests first |
| install-windows-3.11 | 3/4 | Monitor socket at wrong path | Same — read test fixtures |

**Proposed fix:** Strengthen step 2 in coordinator.md: "Read tests yourself — this defines success. Look in /tests/, check for test_outputs.py, verify.sh, or similar. If tests check specific file paths, directory contents, or socket locations, include those constraints in your delegation."

**Test plan:** Run all 3 tasks × 3 reps after the fix.

### Category 5: Confirmation Bias (1 task)
Coordinator commits to wrong hypothesis, implementer doesn't push back.

| Task | Pass rate | Root cause | Proposed fix |
|------|-----------|------------|--------------|
| fix-code-vulnerability | 390/391 | Coordinator decided CWE-20, implementer fixed CWE-93 but called it "unrelated" | Tell implementer to report contradictory evidence |

**Proposed fix:** Add to subagent.md: "If your work reveals that the coordinator's analysis may be wrong — for example, you fix something the coordinator said was unrelated, or your findings contradict the coordinator's hypothesis — report this prominently in your communicate message."

**Test plan:** Run fix-code-vulnerability × 3 reps.

### Category 6: Vision Tasks (2 tasks)
Image analysis tasks where vision usage is unreliable.

| Task | Pass rate | Root cause | Proposed fix |
|------|-----------|------------|--------------|
| chess-best-move | 0/1 full eval, 3/5 overlay, 1/1 core.md | Implementer defaults to pixel code instead of describing what it sees | See investigation below |
| gcode-to-text | 0/1 | Had OCR answer, didn't write file | Write-early fix (Category 1) |

### Category 7: Not Fixable via Prompt (6 tasks)

| Task | Root cause | Why not fixable |
|------|------------|-----------------|
| break-filter-js-from-html | GPT-5.4 refused on ethical grounds | Model safety filter |
| mteb-retrieve | Embedding reproducibility across library versions | Environment issue |
| overfull-hbox | Article grammar change (an→a) | Implementation subtlety |
| qemu-alpine-ssh | Guest network interface down, serial automation fragile | Domain knowledge gap |
| path-tracing | Implementer cheated (called /app/orig) | Need anti-cheating guidance |
| compile-compcert | CompCert build failure on modern Ubuntu | Build system fragility |

## Chess-Best-Move Investigation

This task needs special attention because it **regressed**. The overlay experiments showed 3/5 pass rate but core.md versions show ~1/4.

**Key data points:**
- Overlay (system_prompt_append, coordinator only): v1 PASS, v2 PASS, v5 PASS, v3 FAIL, v4 FAIL = 3/5
- Core.md negative framing: 0/2 (wrong moves d5c4, pending)
- Core.md positive framing: 0/2 (wrong move g5d8, pending)
- Core.md describe+verify: 1/1 PASS, then 0/2 in vision-tasks job
- Full-89 eval (core.md describe+verify): 0/1
- Total core.md: 1/5 = 20%

**Hypothesis:** The overlay experiments worked better because only the coordinator saw the vision prompt. The coordinator described the board and passed a text description to the implementer. When the implementer has the vision prompt AND sees the image, it falls into the pixel-analysis trap anyway. The text description from coordinator → implementer may be more effective than the implementer looking at the image itself.

**Test plan:**
1. Revert core.md vision section to minimal ("you have vision, read_file shows you images")
2. Add vision description guidance to coordinator.md only: "When the task involves an image, read it yourself, describe what you see in complete detail, and include your description in the implementer's task."
3. Run 5 reps to compare with the overlay experiments

**Success criteria:** 3/5 or better (matching overlay performance).

## Execution Order

1. **Chess-best-move investigation** — highest risk regression, needs to be understood first
2. **Write-early fix** — highest impact (3 tasks), core.md change
3. **Don't-contaminate-workspace fix** — 4 tasks, coordinator.md change
4. **Read-tests fix** — 3 tasks, coordinator.md change
5. **Escalate-contradictions fix** — 1 task, subagent.md change
6. **Full eval** — after all fixes validated individually
