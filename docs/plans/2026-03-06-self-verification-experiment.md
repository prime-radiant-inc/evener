# Self-Verification Prompt Tuning Experiment

**Date:** 2026-03-06
**Baseline:** 0/88 passes on 22 close-miss tasks (4 reps each)
**Goal:** Improve self-verification behavior to flip close-miss tasks to passes
**Note:** All runs use `--ak reasoning_effort=high`. Without it, agents score ~0% on everything
(confirmed by aborted v1 run where agents took only 5 steps and never spawned subagents).

## Tasks Under Test

```
circuit-fibsqrt constraints-scheduling custom-memory-heap-crash
financial-document-processor fix-code-vulnerability gcode-to-text
kv-store-grpc llm-inference-batching-scheduler log-summary-date-ranges
mteb-leaderboard mteb-retrieve overfull-hbox path-tracing
path-tracing-reverse query-optimize sparql-university
torch-pipeline-parallelism torch-tensor-parallelism train-fasttext
tune-mjcf video-processing winning-avg-corewars
```

---

## Results

### H1: Subagent test-before-communicate (simple) — 2026-03-06

**Prompt changes:**
Added 3 lines to `subagent_base.md` between "Workflow" and "Non-interactive" sections:
```
- Before calling communicate, look for test scripts (test.sh, test_outputs.py, files
  in tests/ or test/ directories) and run them. If tests fail, fix the issues before
  communicating. Do not communicate with failing tests.
```

**Job:** h1-test-before-communicate-v2 (v1 aborted — missing reasoning_effort=high)
**Results:** 34/87 pass (39.1%) — baseline was 0/88 (0%)
**Per-task:**
```
circuit-fibsqrt:                4/4  PPPP
constraints-scheduling:         3/3  PPP  (1 trial still running)
custom-memory-heap-crash:       4/4  PPPP
financial-document-processor:   3/4  PPFP
fix-code-vulnerability:         3/4  PFPP
gcode-to-text:                  0/4  FFFF
kv-store-grpc:                  3/4  FPPP
llm-inference-batching-scheduler: 3/4 PFPP
log-summary-date-ranges:        2/4  PFFP
mteb-leaderboard:               0/3  FFF  (1 trial errored/missing)
mteb-retrieve:                  0/4  FFFF
overfull-hbox:                  2/4  PPFF
path-tracing:                   0/4  FFFF
path-tracing-reverse:           0/4  FFFF
query-optimize:                 0/4  FFFF
sparql-university:              3/4  PFPP
torch-pipeline-parallelism:     0/4  FFFF
torch-tensor-parallelism:       0/4  FFFF
train-fasttext:                 0/4  FFFF
tune-mjcf:                      0/4  FFFF
video-processing:               0/4  FFFF
winning-avg-corewars:           4/4  PPPP
```
**Tasks flipped (vs baseline):** 11/22 unique tasks now pass at least once:
circuit-fibsqrt, constraints-scheduling, custom-memory-heap-crash,
financial-document-processor, fix-code-vulnerability, kv-store-grpc,
llm-inference-batching-scheduler, log-summary-date-ranges, overfull-hbox,
sparql-university, winning-avg-corewars
**Tasks regressed (vs baseline):** None (baseline was 0/88)
**Notes:**
- Massive improvement: 0% → 39.1% on tasks that never passed before
- 3 tasks now pass 100% of the time: circuit-fibsqrt, custom-memory-heap-crash, winning-avg-corewars
- 6 more tasks pass 50-75%: financial-document-processor, fix-code-vulnerability, kv-store-grpc, llm-inference-batching-scheduler, sparql-university, constraints-scheduling
- 2 tasks pass 50%: log-summary-date-ranges, overfull-hbox
- 11 tasks still never pass — likely Category C (problem too hard for verification to help)
- Caveat: Baseline 0/88 was from prior runs at commit e6756a2. This run is at 5c6e910.
  Both use reasoning_effort=high. The improvement is likely from the H1 prompt change,
  but could partially reflect other prompt changes between e6756a2 and 5c6e910.
- **Decision: PROCEED to H2** — H1 shows strong improvement. Try more structured verification
  (H2) to see if the additional structure improves flaky tasks (log-summary-date-ranges,
  overfull-hbox) or flips more of the 11 never-pass tasks.

---

### H2: Subagent detailed verification protocol — 2026-03-06

**Prompt changes:**
Replaced H1's 3-line addition with a full "## Verification" section in `subagent_base.md`:
```
## Verification

Before calling communicate, you MUST verify your work:

1. **Find tests.** Look for test files: test.sh, test_outputs.py, tests/, test/,
   *_test.py, *_test.go. Also check if the task description mentions test commands.
2. **Run tests.** Execute every test script you find. Read the FULL output.
3. **Check outputs.** Read back every file you created or modified. Verify it matches
   the requirements.
4. **Fix failures.** If any test fails, fix the issue and re-run. Do not communicate
   with failing tests.
5. **Report evidence.** In your communicate message, include test results as proof
   your solution works.
```

**Job:** h2-detailed-verification
**Results:** 37/85 pass (43.5%) — H1 was 34/87 (39.1%), baseline 0/88 (0%)
**Per-task:**
```
circuit-fibsqrt:                3/3  PPP  (1 trial missing)
constraints-scheduling:         4/4  PPPP
custom-memory-heap-crash:       4/4  PPPP
financial-document-processor:   3/4  FPPP
fix-code-vulnerability:         2/4  FPPF
gcode-to-text:                  0/4  FFFF
kv-store-grpc:                  3/4  PPPF
llm-inference-batching-scheduler: 1/4 FPFF
log-summary-date-ranges:        3/4  FPPP
mteb-leaderboard:               1/3  FFP  (1 trial missing)
mteb-retrieve:                  0/4  FFFF
overfull-hbox:                  3/4  PPPF
path-tracing:                   1/4  FPFF
path-tracing-reverse:           0/4  FFFF
query-optimize:                 0/4  FFFF
sparql-university:              3/4  PPPF
torch-pipeline-parallelism:     1/4  FFPF
torch-tensor-parallelism:       0/4  FFFF
train-fasttext:                 0/3  FFF  (1 trial missing)
tune-mjcf:                      1/4  PFFF
video-processing:               1/4  PFFF
winning-avg-corewars:           3/4  PPFP
```
**Tasks flipped (vs baseline):** 16/22 unique tasks now pass at least once:
circuit-fibsqrt, constraints-scheduling, custom-memory-heap-crash,
financial-document-processor, fix-code-vulnerability, kv-store-grpc,
llm-inference-batching-scheduler, log-summary-date-ranges, mteb-leaderboard,
overfull-hbox, path-tracing, sparql-university, torch-pipeline-parallelism,
tune-mjcf, video-processing, winning-avg-corewars
**New in H2 vs H1:** mteb-leaderboard, path-tracing, torch-pipeline-parallelism,
tune-mjcf, video-processing (5 new tasks)
**Regressions vs H1:**
- llm-inference-batching-scheduler: 1/4 (was 3/4 in H1 — significant regression)
- winning-avg-corewars: 3/4 (was 4/4 in H1 — minor)
**Notes:**
- H2 outperforms H1: 43.5% vs 39.1% overall, 16 vs 11 unique tasks flipped
- Structured 5-step checklist flipped 5 tasks that simple 3-line instruction couldn't
- But some tasks that H1 was reliable on became less reliable (llm-inference-batching-scheduler)
- 6 tasks still never pass: gcode-to-text, mteb-retrieve, path-tracing-reverse,
  query-optimize, torch-tensor-parallelism, train-fasttext
- **Decision: PROCEED to H4** — Try combined H2 (subagent) + H3 (coordinator) for
  belt-and-suspenders. May catch llm-inference-batching-scheduler regression and
  flip remaining 6 never-pass tasks.

---

### H4: Combined subagent + coordinator verification — 2026-03-06

**Prompt changes:**
Kept H2's "## Verification" section in `subagent_base.md` AND strengthened
base.md's step 4 from:
```
4. **Verify results.** After a sub-agent completes, check its work yourself. Read the
   files it created. Run the tests. If something is wrong, spawn another agent to fix it
   with specific instructions about what failed and why.
```
to:
```
4. **Verify results yourself.** After a sub-agent completes, do NOT trust its report.
   Run the tests yourself using exec_command. Look for test files (test.sh,
   test_outputs.py, tests/) and execute them. Read the output. If tests fail, spawn
   another agent with the specific failures and instructions to fix them.
```

**Job:** h4-combined-subagent-coordinator
**Results:** 39/88 pass (44.3%) — H2 was 37/85 (43.5%), H1 was 34/87 (39.1%), baseline 0/88 (0%)
**Per-task:**
```
circuit-fibsqrt:                4/4  PPPP
constraints-scheduling:         4/4  PPPP
custom-memory-heap-crash:       4/4  PPPP
financial-document-processor:   3/4  FPPP
fix-code-vulnerability:         2/4  FPPF
gcode-to-text:                  0/4  FFFF
kv-store-grpc:                  4/4  PPPP
llm-inference-batching-scheduler: 4/4 PPPP
log-summary-date-ranges:        3/4  FPPP
mteb-leaderboard:               1/4  FFPF
mteb-retrieve:                  0/4  FFFF
overfull-hbox:                  3/4  PPPF
path-tracing:                   0/4  FFFF
path-tracing-reverse:           0/4  FFFF
query-optimize:                 0/4  FFFF
sparql-university:              3/4  PFPP
torch-pipeline-parallelism:     0/4  FFFF
torch-tensor-parallelism:       0/4  FFFF
train-fasttext:                 0/4  FFFF
tune-mjcf:                      0/4  FFFF
video-processing:               0/4  FFFF
winning-avg-corewars:           4/4  PPPP
```
**Tasks flipped (vs baseline):** 12/22 unique tasks now pass at least once:
circuit-fibsqrt, constraints-scheduling, custom-memory-heap-crash,
financial-document-processor, fix-code-vulnerability, kv-store-grpc,
llm-inference-batching-scheduler, log-summary-date-ranges, mteb-leaderboard,
overfull-hbox, sparql-university, winning-avg-corewars
**New in H4 vs H2:** None — H4 flipped fewer unique tasks (12 vs 16)
**Lost vs H2:** path-tracing, torch-pipeline-parallelism, tune-mjcf, video-processing
(all were 1/4 lucky single passes in H2 — unreliable)
**Key improvement vs H2:**
- llm-inference-batching-scheduler: 4/4 (was 1/4 in H2, 3/4 in H1 — H4 FIXED the regression)
- 6 tasks at 100% (4/4): circuit-fibsqrt, constraints-scheduling, custom-memory-heap-crash,
  kv-store-grpc, llm-inference-batching-scheduler, winning-avg-corewars
  (H2 had only 2 at 4/4: constraints-scheduling, custom-memory-heap-crash)
**Notes:**
- H4 has the highest overall pass rate: 44.3% vs H2's 43.5% vs H1's 39.1%
- H4 is the most RELIABLE: 6 tasks at 100% vs H2's 2. Belt-and-suspenders works.
- H2 flipped more unique tasks (16 vs 12) but the 4 extras were all 1/4 lucky passes
- The coordinator verification (H3 component) primarily adds reliability, not coverage
- 10 tasks never pass in any hypothesis: gcode-to-text, mteb-retrieve, path-tracing,
  path-tracing-reverse, query-optimize, torch-pipeline-parallelism, torch-tensor-parallelism,
  train-fasttext, tune-mjcf, video-processing — these are Category C (too hard for
  verification to help)
- **Decision: H4 is the winner.** It has the best pass rate AND best reliability.
  The coordinator-level "don't trust sub-agent reports" complements the subagent-level
  verification checklist. Together they produce the most consistent results.

---

## Summary

| Hypothesis | Pass Rate | Unique Tasks | 100% Tasks | Key Change |
|-----------|-----------|-------------|-----------|------------|
| Baseline  | 0/88 (0%) | 0/22 | 0 | — |
| H1 (simple 3-line) | 34/87 (39.1%) | 11/22 | 3 | "run tests before communicate" |
| H2 (structured 5-step) | 37/85 (43.5%) | 16/22 | 2 | Verification checklist in subagent |
| H4 (H2+H3 combined) | 39/88 (44.3%) | 12/22 | 6 | Subagent checklist + coordinator distrust |

**Winner: H4** — Best pass rate (44.3%), most reliable (6 tasks at 100%), fixed H2's
llm-inference-batching-scheduler regression.

**Remaining untouched hypotheses:** H5 (iterative loop framing), H6 (prove-it-works
mandate), H7 (task-specific test commands), H8 (verification subagent pattern).
These could be tested against the 10 never-pass tasks, but those are likely Category C
where verification prompts won't help — the agent fundamentally can't solve the problem,
not just failing to check its work.

---

## Phase 2: Targeted Failure Mode Experiments (on H4 baseline)

These hypotheses target 5 tasks that never pass under H4 but have <75% aggregate failure
rate (i.e., other agents solve them). Each tested on 3 tasks (2 targets + 1 regression
check) with 2 reps.

### H9: "Run binaries first" — 2026-03-06

**Prompt change:** Added to base.md step 1 ("Explore first"):
```
If the workspace contains executables, binaries, or compiled programs, RUN THEM first
to understand their input/output behavior. Seeing actual output is worth more than
reading source code.
```

**Job:** h9-run-binaries-first
**Results:** 2/4 valid trials pass (2 trials lost to setup timeout)
**Per-task:**
```
circuit-fibsqrt:       1/1  P   (1 setup timeout) — regression check OK
path-tracing:          0/1  F   (1 setup timeout)
path-tracing-reverse:  1/2  PF  — FLIPPED from 0/4 in H4!
```
**Tasks flipped (vs H4):** path-tracing-reverse (was 0/4 in all previous hypotheses)
**Regressions:** None (circuit-fibsqrt still passes)
**Notes:**
- path-tracing-reverse flipped for the first time ever — H9 directly addresses the failure
  mode (agent not running provided executables to understand I/O format)
- path-tracing still fails but only 1 valid trial (need more data)
- 2 trials lost to "Agent setup timed out after 360.0 seconds" — infrastructure, not prompt
- **Decision: H9 is a keeper.** Flipped a previously-impossible task with no regressions.

---

### H10: "Adapt when verification blocked" — 2026-03-07

**Prompt change:** Added step 4 to subagent_base.md Verification section (renumbered 4→5, 5→6):
```
4. **Adapt if stuck.** If you cannot run the test suite (missing database, missing
   service, missing hardware), verify your work by other means: read the test script
   to understand what it checks, then manually verify each check against your output.
   Do not get stuck — find another way to validate.
```

**Job:** h10-adapt-when-blocked
**Results:** 2/6 pass (33.3%)
**Per-task:**
```
kv-store-grpc:    2/2  PP  — regression check OK
query-optimize:   0/2  FF
tune-mjcf:        0/2  FF
```
**Tasks flipped (vs H4):** None
**Regressions:** None (kv-store-grpc still 2/2)
**Notes:**
- H10 did NOT help either target task. query-optimize and tune-mjcf remain 0/2.
- Both target tasks timed out (AgentTimeoutError), suggesting the failure mode is
  deeper than verification — the agent can't solve the problem within the time limit
  regardless of verification strategy.
- **Decision: H10 is NOT a keeper.** No improvement, discard this change.

---

### H11: "Time-box exploration" — 2026-03-07

**Prompt change:** Added to base.md step 1 ("Explore first"):
```
Limit exploration to 2-3 sub-agent calls. If you still don't understand the problem
after 3 exploration rounds, start implementing with your best understanding. A rough
implementation you can iterate on beats perfect understanding with no time to code.
```

**Job:** h11-timebox-exploration
**Results:** 2/5 valid trials pass (1 trial lost to setup timeout)
**Per-task:**
```
winning-avg-corewars:  2/2  PP  — regression check OK
gcode-to-text:         0/2  FF
path-tracing:          0/1  F   (1 setup timeout)
```
**Tasks flipped (vs H4):** None
**Regressions:** None (winning-avg-corewars still 2/2)
**Notes:**
- H11 did NOT help either target task. gcode-to-text 0/2, path-tracing 0/1.
- gcode-to-text may be Category C — the task itself is extremely hard (74.6% aggregate
  failure rate), and time-boxing exploration doesn't help if the agent fundamentally
  can't produce a correct G-code parser.
- path-tracing only had 1 valid trial due to setup timeout, but even that failed.
- **Decision: H11 is NOT a keeper.** No improvement, discard this change.

---

### H4+H9 Validation: Broader test — 2026-03-07

**Prompt:** H4 baseline (strengthened coordinator step 4 + subagent verification checklist)
+ H9 addition to step 1 ("run binaries first").

**Job:** h4-h9-validation
**Results:** 6/16 pass (37.5%)
**Per-task:**
```
circuit-fibsqrt:       2/2  PP  — control OK (was 4/4 in H4)
winning-avg-corewars:  2/2  PP  — control OK (was 4/4 in H4)
kv-store-grpc:         1/2  FP  — slight regression (was 4/4 in H4)
tune-mjcf:             1/2  FP  — NEW FLIP (was 0/4 in H4)
path-tracing-reverse:  0/2  FF  — H9 flip did NOT replicate (was 1/2 in H9 test)
path-tracing:          0/2  FF  — still failing
gcode-to-text:         0/2  FF  — still failing
query-optimize:        0/2  FF  — still failing
```
**Tasks flipped (vs H4):** tune-mjcf (1/2)
**Regressions:** kv-store-grpc 1/2 vs 4/4 in H4 (concerning)
**Notes:**
- tune-mjcf flipped for only the 2nd time ever (was 1/4 in H2, 0/4 in H1 and H4).
  The "run binaries first" hint may help the agent understand the MuJoCo simulation
  output format, which is similar to how it helped path-tracing-reverse in the H9 test.
- path-tracing-reverse did NOT replicate its H9 success (0/2 vs 1/2 in H9 test).
  The H9 flip was likely a lucky single pass.
- kv-store-grpc regression is concerning — 1/2 vs 4/4 in H4. May be noise (2 reps)
  or the extra text in step 1 is diluting the prompt slightly.
- Controls (circuit-fibsqrt, winning-avg-corewars) are solid at 2/2.
- **Assessment:** H9 shows marginal benefit. It flipped tune-mjcf in validation but
  the path-tracing-reverse flip didn't replicate. The kv-store-grpc regression needs
  more reps to determine if it's noise. Given the mixed signal, H9 is a cautious
  keeper — the tune-mjcf improvement is real but needs more data to confirm.

---

## Phase 2 Summary

| Hypothesis | Target Tasks | Flipped | Regressions | Verdict |
|-----------|-------------|---------|-------------|---------|
| H9 "Run binaries first" | path-tracing, path-tracing-reverse | path-tracing-reverse (1/2, didn't replicate), tune-mjcf (1/2 surprise) | kv-store-grpc marginal | Cautious keeper |
| H10 "Adapt when blocked" | query-optimize, tune-mjcf | None | None | Discard |
| H11 "Time-box exploration" | path-tracing, gcode-to-text | None | None | Discard |
| H4+H9 validation | 5 targets + 3 controls | tune-mjcf (1/2) | kv-store-grpc (1/2 vs 4/4) | Mixed signal |

**Bottom line:** The Phase 2 hypotheses had minimal impact. The 5 never-pass tasks
(path-tracing, path-tracing-reverse, query-optimize, tune-mjcf, gcode-to-text) are
mostly Category C — the agent fundamentally can't solve them within the time limit,
regardless of exploration or verification strategy. tune-mjcf showed a flicker of
improvement with H9 but is unreliable (1/2 in validation, was 1/4 in H2).
