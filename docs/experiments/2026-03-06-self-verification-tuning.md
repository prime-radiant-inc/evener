# Self-Verification Prompt Tuning Experiment

## Goal
Improve pass rate on close-miss tasks (currently failing 1-2 verifier tests) by tuning the benchmark persona's self-verification behavior. Current overall rate: 55.1% (245/445). Ceiling: 78%.

## Baseline Run
`lace_gpt-5.2-codex_benchmark_20e20546a_20260306_1` — persona=benchmark, 89 tasks x 5 reps, 55.1%

## Test Tasks (close-miss rejects from baseline)

| Task | Tests Passing | Failure Pattern | Budget |
|------|--------------|-----------------|--------|
| cancel-async-tasks | 5/6 | `test_tasks_cancel_above_max_concurrent` — concurrency logic wrong | 900s |
| fix-code-vulnerability | 5/6 | `test_cwe_id` — must add CWE comment to patched file | 900s |
| protein-assembly | 0/1 | Missing `/app/gblock.txt` — file not created at expected path | 900s |
| pypi-server | 0/1 | PyPI server not serving packages correctly on localhost | 900s |
| overfull-hbox | 3/4 | Modified words not limited to synonyms.txt list | 750s |
| llm-inference-batching-scheduler | 5/6 | Performance threshold missed by ~8% | 1800s |
| sparql-university | 2/3 | SPARQL query results don't match reference | 900s |
| break-filter-js-from-html | 0/1 | XSS bypass doesn't trigger alert after filtering | 1200s |

### Baseline pass rates on test tasks (from full run, 5 reps each)

| Task | Pass | Fail | Timeout | Rate |
|------|------|------|---------|------|
| cancel-async-tasks | 2 | 3 | 0 | 40% |
| fix-code-vulnerability | 3 | 2 | 0 | 60% |
| protein-assembly | 3 | 2 | 0 | 60% |
| pypi-server | 2 | 1 | 2 | 40% |
| overfull-hbox | 2 | 1 | 2 | 40% |
| llm-inference-batching-scheduler | 3 | 1 | 1 | 60% |
| sparql-university | 4 | 1 | 0 | 80% |
| break-filter-js-from-html | 1 | 4 | 0 | 20% |

## Hypotheses

### H1: Agent doesn't reread the spec before finishing
The current "benchmark" persona says "verify one more time. Then you can stop." — too vague.
The agent probably runs its own ad-hoc checks but doesn't go back to the original task
description to confirm every requirement is met.

**Change**: Add explicit "reread the spec with fresh eyes" verification step from V5.
**Expected**: protein-assembly (missing file), fix-code-vulnerability (missing CWE comment)
should improve — these are cases where a specific requirement was missed.

### H2: Agent doesn't run the verifier's exact test commands
Close-miss failures often pass the agent's own tests but fail the verifier's. The agent
may test differently than the verifier (different paths, different args, different env).

**Change**: Add instruction to find and run any test files in /tests/ before finishing.
**Expected**: Tasks with pytest-based verifiers should improve.

### H3: Agent submits without testing at all
Some failures look like the agent built something and stopped without ever running it.

**Change**: Add "you MUST run your solution and verify it produces correct output before
stopping" with specific examples.
**Expected**: protein-assembly, pypi-server should improve.

### H4: Agent doesn't check edge cases / performance requirements
llm-inference-batching-scheduler fails a performance threshold by 8%. The agent probably
verified correctness but not performance.

**Change**: Add "verify both correctness AND performance/edge cases mentioned in the spec."
**Expected**: llm-inference-batching-scheduler, winning-avg-corewars should improve.

### H5: Agent stops iterating after first working version
For tasks like cancel-async-tasks (concurrency logic), the agent gets a basic solution working
but doesn't stress-test the tricky cases.

**Change**: Add "after your solution works, think about what could go wrong — race conditions,
edge cases, off-by-one errors — and test those specifically."
**Expected**: cancel-async-tasks, circuit-fibsqrt should improve.

### H6: Combined best elements
Take the winning elements from H1-H5 and combine into one prompt.

### H7: Concise combined
H3 + H5 distilled into 3 short sentences instead of a bullet checklist.

### H8: User-satisfaction framing (verbatim)
Replace technical checklist with user-centric framing: "You are responsible for verifying
that your work does what the user expects." Full paragraph emphasizing rereading the task,
testing behavior, adversarial review, user satisfaction.

**Change**: Single paragraph, no bullets, user-satisfaction language.
**Expected**: The holistic framing may trigger more thorough natural verification than
a mechanical checklist. H4's technical bullets might be too clinical — the model may
follow the letter (check performance, check edge cases) without the spirit.

### H9: User-satisfaction framing + structured bullets
Your text as the intro, then H4-style bullets rephrased as user-centric questions:
"Does it actually work?", "Did you satisfy every requirement?", "Would the user be satisfied?"

**Change**: Combines the motivation (user satisfaction) with the structure (bullet points).
**Expected**: Best of both worlds — emotional framing drives engagement, bullets drive thoroughness.

### H10: User-satisfaction + H4 specifics appended
Your exact text, then a "Specifically:" paragraph with H4's concrete technical examples
(benchmark perf, test concurrency, verify each output).

**Change**: Tests whether the user framing + technical specifics > user framing alone.
**Expected**: May regress like H6/H7 if adding specifics dilutes the natural framing.

### H11: Minimal user-satisfaction
Just the first two sentences of your text: "You are responsible for verifying that your
work does what the user expects. When you believe you're done, reread the original task
and then look at your work with fresh eyes."

**Change**: Tests the minimum viable intervention — does just the responsibility framing + reread help?
**Expected**: If the user-satisfaction framing itself is the active ingredient, this minimal
version should still beat baseline. If the specifics matter, this will underperform H8.

## Experiment Log

### Experiment 1: H1 — Reread spec before finishing
**Job**: selfverify-h1
**Persona**: benchmark-h1.md
**Change**: Replaced "verify one more time. Then you can stop." with explicit instruction to reread original task instructions with fresh eyes and verify every requirement.
**Reps**: 4

| Task | H1 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 1/4 (25%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 2/4 (50%) | 3/5 (60%) | -10pp |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 1/4 (25%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 4/4 (100%) | 3/5 (60%) | **+40pp** |
| sparql-university | 3/4 (75%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 1/4 (25%) | 1/5 (20%) | +5pp |
| **Total** | **17/32 (53%)** | **20/40 (50%)** | **+3pp** |

**Observations**:
- llm-inference-batching-scheduler: 4/4 perfect — huge improvement, possibly the reread helped the agent notice performance requirements
- pypi-server: 3/4 — strong improvement, agent likely caught missing server verification
- cancel-async-tasks, overfull-hbox: regressed — small sample noise, or reread doesn't help with logic bugs
- fix-code-vulnerability: expected to improve (missed CWE comment) but didn't materially
- Overall: +3pp, within noise. Strong signal on 2 tasks but not broadly effective.

**Verdict**: Partial win. Keep llm-inference and pypi-server improvements, but need other mechanisms for logic bugs.

### Experiment 2: H2 — Find and run test files
**Job**: selfverify-h2
**Persona**: benchmark-h2.md
**Change**: Added instruction to look for test files in `/tests/`, `tests/`, `test_*.py`, etc. and run them before finishing.
**Reps**: 4

| Task | H2 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 1/4 (25%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 1/4 (25%) | 3/5 (60%) | **-35pp** |
| sparql-university | 3/4 (75%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **16/32 (50%)** | **20/40 (50%)** | **0pp** |

**Observations**:
- cancel-async-tasks: 75% — biggest improvement! Finding test files with concurrency tests exposed the bug
- fix-code-vulnerability: 75% — tests likely caught missing CWE comment
- pypi-server: 75% — consistent improvement (also seen in H1)
- llm-inference-batching-scheduler: 25% — big regression! Agent probably spent time hunting for test files instead of optimizing performance. This task's verifier is a benchmark, not pytest.
- break-filter-js-from-html: 0/4 — XSS tasks don't have standard test files
- Overall: 0pp net, but strong wins on test-file tasks offset by losses on non-test tasks.

**Verdict**: Strong for tasks with discoverable test files. Harmful for performance/creative tasks. Must be combined with H1 carefully.

### Experiment 3: H3 — Must run and verify solution
**Job**: selfverify-h3
**Persona**: benchmark-h3.md
**Change**: Added explicit "you MUST run your solution and verify it produces correct output" with concrete examples (run program, check file exists, make request to server, execute script).
**Reps**: 4

| Task | H3 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 1/4 (25%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 4/4 (100%) | 3/5 (60%) | **+40pp** |
| pypi-server | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| overfull-hbox | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| llm-inference-batching-scheduler | 2/4 (50%) | 3/5 (60%) | -10pp |
| sparql-university | 3/4 (75%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **20/32 (62%)** | **20/40 (50%)** | **+12pp** |

**Observations**:
- protein-assembly: 4/4 perfect! The "verify file exists at expected path" instruction directly addresses the /app/gblock.txt miss
- pypi-server: 4/4 perfect! "Make a request to your server" instruction caught serving issues
- overfull-hbox: 3/4 — strong improvement, agent verified output matches constraints
- cancel-async-tasks: still low — concurrency bugs aren't caught by running the program once
- break-filter-js-from-html: still 0/4 — XSS bypass is fundamentally harder
- Best overall result so far: +12pp over baseline

**Verdict**: Strong win. "Must actually run and verify" is the most broadly effective intervention. Core element for H6.

### Experiment 4: H4 — Verify performance and edge cases
**Job**: selfverify-h4
**Persona**: benchmark-h4.md
**Change**: Added "verify correctness AND non-functional requirements" with bullets for performance benchmarking, edge case testing, and verifying all outputs individually.
**Reps**: 4

| Task | H4 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| fix-code-vulnerability | 2/4 (50%) | 3/5 (60%) | -10pp |
| protein-assembly | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| pypi-server | 2/4 (50%) | 2/5 (40%) | +10pp |
| overfull-hbox | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 2/4 (50%) | 4/5 (80%) | -30pp |
| break-filter-js-from-html | 1/4 (25%) | 1/5 (20%) | +5pp |
| **Total** | **21/32 (65%)** | **20/40 (50%)** | **+15pp** |

**Observations**:
- cancel-async-tasks: 4/4 perfect! Edge case testing instruction caught the concurrency bug that H1-H3 all missed
- overfull-hbox: 4/4 perfect! Testing boundary conditions helped
- llm-inference-batching-scheduler: 3/4 — performance verification helped (as predicted)
- sparql-university: regressed to 50% — possible noise, or the extra verification time ate into task budget
- Tied for best overall with H5 at +15pp

**Verdict**: Strong win on logic/edge-case tasks. The "test edge cases specifically" instruction helps where "just run it" (H3) doesn't. Core element for H6.

### Experiment 5: H5 — Adversarial self-testing
**Job**: selfverify-h5
**Persona**: benchmark-h5.md
**Change**: Added "think adversarially about what could go wrong" with specific prompts for race conditions, off-by-one errors, missing error handling, path assumptions.
**Reps**: 4

| Task | H5 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| overfull-hbox | 2/4 (50%) | 2/5 (40%) | +10pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 4/4 (100%) | 4/5 (80%) | +20pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **21/32 (65%)** | **20/40 (50%)** | **+15pp** |

**Observations**:
- pypi-server: 4/4 — consistently improves across all hypotheses
- sparql-university: 4/4 — adversarial thinking caught query edge cases
- cancel-async-tasks: 3/4 — "race conditions" prompt directly relevant
- llm-inference-batching-scheduler: 3/4 — consistent with H4
- protein-assembly: only 2/4 — adversarial thinking doesn't help with "you forgot a file" problems
- break-filter-js-from-html: 0/4 again — nothing helps this task

**Verdict**: Tied with H4 at +15pp. Different task-level strengths — H5 better on sparql, H4 better on overfull-hbox. Both are strong candidates for H6.

### Experiment 6: H6 — Combined (H3 + H4 + H5 + H1)
**Job**: selfverify-h6
**Persona**: benchmark-h6.md
**Change**: Combined all winning elements into 3-step "Before You Stop" section: (1) run and verify output, (2) check edge cases and performance, (3) reread the spec.
**Reps**: 4

| Task | H6 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 2/4 (50%) | 3/5 (60%) | -10pp |
| protein-assembly | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 1/4 (25%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 2/4 (50%) | 3/5 (60%) | -10pp |
| sparql-university | 4/4 (100%) | 4/5 (80%) | +20pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **18/32 (56%)** | **20/40 (50%)** | **+6pp** |

**Observations**:
- Worse than H4 (65%) and H5 (65%) individually!
- The 3-step verification checklist likely causes: (a) time budget consumed by verification instead of implementation, (b) agent spreading attention across all steps instead of focusing, (c) diminishing returns — the most impactful check depends on the task type
- sparql-university: 4/4 — consistent with H5 (adversarial thinking works here)
- overfull-hbox: regressed to 1/4 — H4 got 4/4, suggesting the combined prompt diluted the edge-case focus
- llm-inference-batching-scheduler: 2/4 — H1 got 4/4, H4 got 3/4, combined is worse

**Verdict**: Combining all elements is counterproductive. The prompt is too long/complex, and the agent can't follow all three steps well within the time budget. A focused, shorter prompt (H4 or H5) works better.

### Experiment 7: H7 — Concise combined (H3 + H5 in 3 sentences)
**Job**: selfverify-h7
**Persona**: benchmark-h7.md
**Change**: Distilled H3+H5 into 3 short paragraphs instead of H6's verbose 3-step checklist. "Run end-to-end. Think about what could break. Fix and verify."
**Reps**: 4

| Task | H7 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 1/4 (25%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| overfull-hbox | 0/4 (0%) | 2/5 (40%) | -40pp |
| llm-inference-batching-scheduler | 1/4 (25%) | 3/5 (60%) | **-35pp** |
| sparql-university | 3/4 (75%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 1/4 (25%) | 1/5 (20%) | +5pp |
| **Total** | **15/32 (46%)** | **20/40 (50%)** | **-4pp** |

**Observations**:
- Worse than baseline! The concise combination actually underperformed.
- 2 timeouts on llm-inference — the "adversarial thinking" step consumed budget
- overfull-hbox: 0/4 — worst result for any task across all experiments
- pypi-server: 4/4 — this task consistently benefits from any "verify your server" instruction
- The concise version was actually less effective than the verbose H6, perhaps because brevity made the instructions too vague to follow

**Verdict**: Neither verbose (H6) nor concise (H7) combination works. The best strategy is a single focused prompt, not a combination.

### Experiment 8: H8 — User-satisfaction framing (verbatim)
**Job**: selfverify-h8
**Persona**: benchmark-h8.md
**Change**: Replaced technical bullets with a single user-centric paragraph: "You are responsible for verifying that your work does what the user expects. When you believe you're done, reread the original task and then look at your work with fresh eyes..."
**Reps**: 4

| Task | H8 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 1/4 (25%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 1/4 (25%) | 3/5 (60%) | -35pp |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 2/4 (50%) | 2/5 (40%) | +10pp |
| overfull-hbox | 1/4 (25%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 4/4 (100%) | 4/5 (80%) | +20pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **14/32 (43%)** | **20/40 (50%)** | **-7pp** |

**Observations**:
- Worse than baseline. The user-satisfaction framing alone, without structure, isn't actionable enough.
- sparql-university: 4/4 — the "look with fresh eyes" language helps query correctness (consistent with H1, H5)
- llm-inference: 3/4 — "reread the original task" helps notice perf requirements (consistent with H1)
- fix-code-vulnerability: 1/4 — surprising regression. The paragraph format may be too vague for the agent to extract specific action items.

**Verdict**: User-satisfaction framing without structure underperforms. The agent needs concrete bullets, not prose.

### Experiment 9: H9 — User-satisfaction + structured bullets
**Job**: selfverify-h9
**Persona**: benchmark-h9.md
**Change**: User-centric intro ("You are responsible...") followed by 3 structured questions: "Does it actually work?", "Did you satisfy every requirement?", "Would the user be satisfied?"
**Reps**: 4

| Task | H9 | BL | Delta |
|------|----|----|-------|
| cancel-async-tasks | 1/4 (25%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 1/4 (25%) | 3/5 (60%) | -35pp |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 2/4 (50%) | 4/5 (80%) | -30pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **16/32 (50%)** | **20/40 (50%)** | **+0pp** |

**Observations**:
- Net zero vs baseline, but 16pp below H4.
- Adding structure to the user framing helped vs H8 (43% → 50%), but still worse than H4's purely technical bullets.
- Interesting: overfull-hbox 3/4 and llm-inference 3/4 match H4 levels, but protein-assembly collapsed to 1/4.
- The "Would the user be satisfied?" question is too open-ended — doesn't trigger specific technical checks.

**Verdict**: Structure helps vs pure prose, but user-satisfaction framing adds no value over H4's technical framing.

### Experiment 10: H10 — User-satisfaction + H4 specifics appended
**Job**: selfverify-h10
**Persona**: benchmark-h10.md
**Change**: User-satisfaction paragraph verbatim, then "Specifically:" paragraph with H4's concrete examples (benchmark perf, test concurrency, verify each output).
**Reps**: 4

| Task | H10 | BL | Delta |
|------|-----|----|-------|
| cancel-async-tasks | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 1/4 (25%) | 3/5 (60%) | -35pp |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 1/4 (25%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 4/4 (100%) | 4/5 (80%) | +20pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **17/32 (53%)** | **20/40 (50%)** | **+3pp** |

**Observations**:
- Best of the H8-H11 batch but still 12pp below H4.
- The "Specifically:" paragraph helped cancel-async (75%) and llm-inference (75%), matching H4 on those.
- But user-satisfaction preamble diluted the technical impact — overfull-hbox only 1/4 vs H4's 4/4.
- Confirms the pattern: adding prose preamble before technical bullets weakens the bullets.

**Verdict**: The user-satisfaction framing adds overhead without adding value. H4's purely technical bullets are more effective.

### Experiment 11: H11 — Minimal (responsibility + reread only)
**Job**: selfverify-h11
**Persona**: benchmark-h11.md
**Change**: Just two sentences: "You are responsible for verifying that your work does what the user expects. When you believe you're done, reread the original task and then look at your work with fresh eyes."
**Reps**: 4

| Task | H11 | BL | Delta |
|------|-----|----|-------|
| cancel-async-tasks | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 1/4 (25%) | 3/5 (60%) | -35pp |
| protein-assembly | 2/4 (50%) | 3/5 (60%) | -10pp |
| pypi-server | 3/4 (75%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 0/4 (0%) | 2/5 (40%) | -40pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 3/4 (75%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 1/5 (20%) | -20pp |
| **Total** | **15/32 (46%)** | **20/40 (50%)** | **-4pp** |

**Observations**:
- Below baseline. The minimal intervention isn't actionable enough.
- Interesting: cancel-async 75% and llm-inference 75% — the "reread" instruction alone helps with tasks where the agent misses spec requirements. But that's not enough without technical verification.
- overfull-hbox: 0/4 — without explicit "test edge cases" instruction, the agent doesn't bother.
- Confirms: the user-satisfaction framing is not the active ingredient. The active ingredient is the specific technical directives.

**Verdict**: Minimal framing is insufficient. The agent needs concrete, actionable verification steps.

## Research Report

### Summary

We tested 12 prompt variants for self-verification behavior on 8 close-miss benchmark tasks (tasks where the baseline agent fails 1-2 verifier tests), running 4 reps each (384 total trials across 12 experiments). The goal was to improve the benchmark persona's pass rate on these tasks from 50% to as close to the 78% ceiling as possible.

### Results Overview

| Experiment | Prompt Strategy | Pass Rate | vs Baseline |
|-----------|----------------|-----------|-------------|
| Baseline | "verify one more time" | 20/40 (50%) | — |
| H1 | Reread spec with fresh eyes | 17/32 (53%) | +3pp |
| H2 | Find and run test files | 16/32 (50%) | +0pp |
| H3 | Must run solution end-to-end | 20/32 (62%) | +12pp |
| **H4** | **Verify edge cases + performance** | **21/32 (65%)** | **+15pp** |
| **H5** | **Think adversarially** | **21/32 (65%)** | **+15pp** |
| H6 | Combined (verbose, 3 steps) | 18/32 (56%) | +6pp |
| H7 | Combined (concise, 3 sentences) | 15/32 (46%) | -4pp |
| H8 | User-satisfaction (verbatim prose) | 14/32 (43%) | -7pp |
| H9 | User-satisfaction + structured bullets | 16/32 (50%) | +0pp |
| H10 | User-satisfaction + H4 specifics | 17/32 (53%) | +3pp |
| H11 | Minimal (responsibility + reread) | 15/32 (46%) | -4pp |
| H12 | H4 content as graphviz decision graph | 20/32 (62%) | +12pp |
| H13 | Forced enumeration graph (no escape hatches) | 16/32 (50%) | +0pp |
| H14 | Reread-test-adversarial pipeline graph | 18/32 (56%) | +6pp |
| H15 | H14 + explicit edge case step | 17/32 (53%) | +3pp |
| H16 | H15 as prose bullets (not graph) | 20/32 (62%) | +12pp |
| H17 | H16 + test-first development | 15/32 (47%) | -3pp |
| **H18** | **H4 bullets moved to How to Work** | **22/30 (73%)** | **+23pp** |
| H19 | H4 + "use it like a user" | 19/31 (61%) | +11pp |
| | | | |
| H4 rerun | H4 validation (8 reps/task) | 41/64 (64%) | +14pp |
| H16 rerun | H16 validation (8 reps/task) | 35/64 (55%) | +5pp |

### Key Findings

#### 1. Focused beats combined
The single most important finding: combining multiple verification strategies into one prompt consistently underperforms individual strategies. H4 and H5 each scored 65%, but combining them (H6: 56%, H7: 46%) was worse. This is counterintuitive — more verification steps should help — but the data is unambiguous across two combination attempts.

**Likely mechanism**: The agent has a fixed time budget (750-1800s per task). Each verification step takes time. When given a checklist of 3+ steps, the agent either (a) rushes through each one superficially, (b) spends too much time verifying and too little implementing, or (c) treats the checklist as optional and ignores some steps. A single strong directive is more likely to be followed thoroughly.

#### 2. Technical bullets beat prose framing
Round 2 tested whether user-centric framing ("you are responsible for verifying your work does what the user expects") could match or beat H4's technical bullets. It can't:

| Framing Style | Best Result | Avg |
|--------------|-------------|-----|
| Technical bullets (H4) | 65% | 65% |
| Prose + bullets (H9) | 50% | 50% |
| Prose + H4 specifics (H10) | 53% | 53% |
| Pure prose (H8) | 43% | 43% |
| Minimal (H11) | 46% | 46% |

The pattern is clear: adding a user-satisfaction preamble before technical bullets doesn't help — it dilutes them. The model responds better to direct, actionable instructions ("test edge cases", "benchmark performance") than to motivational framing ("would the user be satisfied?"). The prose versions were consistently 12-22pp below H4.

#### 3. Different tasks need different verification
No single prompt is best for all tasks:

| Task Type | Best Prompt | Why |
|-----------|------------|-----|
| Missing file/output | H3 (must run) | Agent needs to check artifacts exist |
| Concurrency/logic bugs | H4 (edge cases) | Agent needs to test tricky cases |
| Performance thresholds | H1 (reread spec) | Agent needs to notice perf requirements |
| Query/data correctness | H5 (adversarial) | Agent needs to think about what could go wrong |
| XSS/security bypass | None effective | Fundamentally requires creative security thinking |

#### 4. Per-task best results vs baseline

| Task | Best Experiment | Best Rate | BL Rate | Improvement |
|------|----------------|-----------|---------|-------------|
| cancel-async-tasks | H4 | 4/4 (100%) | 40% | +60pp |
| fix-code-vulnerability | H2/H3/H5 (tie) | 3/4 (75%) | 60% | +15pp |
| protein-assembly | H3 | 4/4 (100%) | 60% | +40pp |
| pypi-server | H3/H5/H7 (tie) | 4/4 (100%) | 40% | +60pp |
| overfull-hbox | H4 | 4/4 (100%) | 40% | +60pp |
| llm-inference-batching | H1 | 4/4 (100%) | 60% | +40pp |
| sparql-university | H5/H6 (tie) | 4/4 (100%) | 80% | +20pp |
| break-filter-js-from-html | H1/H4/H7 (tie) | 1/4 (25%) | 20% | +5pp |

If we could pick the best prompt per task, the theoretical max on these 8 tasks would be 29/32 (90%). But since we need one prompt for all tasks, we're constrained to 65%.

#### 5. pypi-server is the universal improver
pypi-server improved under every single prompt variant except H4 (which still hit 50%). Any instruction that mentions "verify your solution actually works" helps this task. This suggests the baseline agent often builds a working server but never tests it from the client side.

#### 6. break-filter-js-from-html is intractable via prompt
This XSS bypass task scored 0-25% across ALL variants. The baseline was already at 20%. No verification prompt helps because the core challenge is creative security thinking, not verification discipline. This task needs a fundamentally different approach (e.g., security-focused tools, example XSS payloads in context, or a more capable model).

#### 7. Variance is high at n=4
With only 4 reps per task, many results could be noise. The strongest signals are:
- **Consistent improvements**: pypi-server improved in 6/7 experiments
- **Consistent failures**: break-filter-js-from-html never exceeded 25% in any experiment
- **Strongest single-task lifts**: H4 on cancel-async (4/4) and overfull-hbox (4/4); H3 on protein-assembly (4/4) and pypi-server (4/4)

### Recommendations

#### For the benchmark persona
Adopt H4 (verify edge cases + performance) as the new baseline prompt. It provides the best aggregate improvement (+15pp) and excels on the task categories that matter most (concurrency bugs, boundary conditions, performance thresholds).

The "Before You Stop" section should read:
```
Verify both correctness AND non-functional requirements:
- Correctness: Does your solution produce the right output for typical inputs?
- Performance: If the task mentions speed, throughput, latency, or efficiency targets,
  benchmark your solution and confirm it meets them.
- Edge cases: If the task mentions constraints, limits, or boundary conditions, test
  those specifically.
- All outputs: If the task asks for multiple outputs, verify each one individually.
```

#### What NOT to do
- Don't combine multiple verification strategies into one prompt
- Don't add "find and run test files" (H2) — it helps some tasks but hurts others equally
- Don't add "reread the spec" as a separate step — it's already implicit in H4's "verify each requirement"

#### Future work
1. **Broader validation**: Run H4 on the full 56-task discriminator set to confirm the +15pp improvement generalizes beyond these 8 tasks
2. **Task-specific routing**: If the prompt harness supports it, route different task types to different verification prompts (H3 for "build X" tasks, H4 for "fix bug" tasks, H5 for "data/query" tasks)
3. **break-filter-js-from-html**: This task needs investigation beyond prompt tuning. Consider adding security-focused tool examples or reference materials to the agent's context.
4. **More reps**: Validate the top findings with 8-10 reps to reduce variance

### Methodology Notes
- All experiments used gpt-5.2-codex via lace harness on magic-kingdom
- Baseline: `lace_gpt-5.2-codex_benchmark_20e20546a_20260306_1` (89 tasks x 5 reps, 55.1%)
- Test tasks selected as "close-miss" failures from baseline: tasks where the agent passes most verifier tests but fails 1-2
- Each hypothesis tested independently: only the "Before You Stop" section changes between variants
- 512 total trials across 16 experiments (16 x 8 tasks x 4 reps, minus setup timeouts)
- Results compared against baseline rates on the same 8 tasks (20/40 = 50%)
- Round 1 (H1-H7): tested different verification strategies
- Round 2 (H8-H11): tested user-satisfaction framing as an alternative to H4's technical bullets
- Round 3 (H12-H13): tested graphviz dot notation as an alternative format for H4's content

### Experiment 12: H12 — Graphviz decision graph
**Job**: selfverify-h12
**Persona**: benchmark-h12.md
**Change**: H4's exact verification content rewritten as a graphviz dot notation decision graph instead of prose bullets. Diamonds for decisions ("Task mentions performance targets?"), boxes for actions ("Benchmark and confirm it meets them"), doublecircles for entry/exit, explicit loop-back on failure.
**Hypothesis**: The model may follow a graph with explicit ordering, conditional diamonds, and loop-back edges more reliably than prose bullets.
**Reps**: 4

| Task | H12 | H4 | BL | H12 vs BL |
|------|-----|----|----|-----------|
| cancel-async-tasks | 3/4 (75%) | 4/4 (100%) | 2/5 (40%) | **+35pp** |
| fix-code-vulnerability | 4/4 (100%) | 2/4 (50%) | 3/5 (60%) | **+40pp** |
| protein-assembly | 1/4 (25%) | 3/4 (75%) | 3/5 (60%) | -35pp |
| pypi-server | 3/4 (75%) | 2/4 (50%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 2/4 (50%) | 4/4 (100%) | 2/5 (40%) | +10pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 3/4 (75%) | 2/4 (50%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 1/4 (25%) | 1/4 (25%) | 1/5 (20%) | +5pp |
| **Total** | **20/32 (62%)** | **21/32 (65%)** | **20/40 (50%)** | **+12pp** |

**Observations**:
- fix-code-vulnerability: 4/4 (100%) — best result for this task across all experiments! The graph's explicit loop-back ("Fix issues found" → "Does it produce correct output?") may have driven more thorough iteration.
- protein-assembly: 1/4 (25%) — significant regression vs H4 (75%). The graph format may not convey the "check file exists" verification as effectively as prose.
- overfull-hbox: 2/4 (50%) vs H4's 4/4 (100%) — the edge case testing is less prominent in graph form.
- cancel-async-tasks: 3/4 (75%) vs H4's 4/4 (100%) — slight regression.
- llm-inference, pypi-server, sparql-university: comparable to H4.
- 2 trials had infrastructure setup timeouts (break-filter-js-from-html, overfull-hbox), counted as failures.

**Verdict**: Graphviz format scored +12pp over baseline but -3pp below H4's prose bullets. The format is competitive but doesn't improve on H4. The graph excelled on fix-code-vulnerability (explicit iteration loop) but lost ground on protein-assembly and overfull-hbox (specific verification actions are less salient in graph nodes than in prose bullets). The data doesn't support switching from bullets to graphviz for this use case.

### Experiment 13: H13 — Forced enumeration graph (no escape hatches)
**Job**: selfverify-h13
**Persona**: benchmark-h13.md
**Change**: Redesigned the graphviz graph to remove all diamond escape hatches. H12 had 4 conditional diamonds ("Task mentions performance targets?" → "no" → skip) that let the agent bypass verification. H13 replaces them with imperative action boxes: "Enumerate all requirements, constraints, and edge cases from the task" → "Verify each one specifically" → single diamond "All verified?" with loop-back on "no".
**Hypothesis**: Diamond escape hatches in H12 let the agent skip verification steps. Forcing enumeration of all requirements should prevent shirking.
**Reps**: 4

| Task | H13 | H12 | H4 | BL | H13 vs BL |
|------|-----|-----|----|----|-----------|
| cancel-async-tasks | 2/4 (50%) | 3/4 (75%) | 4/4 (100%) | 2/5 (40%) | +10pp |
| fix-code-vulnerability | 2/4 (50%) | 4/4 (100%) | 2/4 (50%) | 3/5 (60%) | -10pp |
| protein-assembly | 2/4 (50%) | 1/4 (25%) | 3/4 (75%) | 3/5 (60%) | -10pp |
| pypi-server | 3/4 (75%) | 3/4 (75%) | 2/4 (50%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 1/4 (25%) | 2/4 (50%) | 4/4 (100%) | 2/5 (40%) | -15pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/4 (75%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 3/4 (75%) | 3/4 (75%) | 2/4 (50%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 1/4 (25%) | 1/4 (25%) | 1/5 (20%) | -20pp |
| **Total** | **16/32 (50%)** | **20/32 (62%)** | **21/32 (65%)** | **20/40 (50%)** | **+0pp** |

**Observations**:
- Scored exactly at baseline (50%), 12pp below H12 and 15pp below H4.
- fix-code-vulnerability collapsed from H12's 4/4 to 2/4 — removing the specific iteration loop that made H12 excel on this task.
- overfull-hbox: 1/4 — without H4's explicit "test boundary conditions" instruction, the abstract "verify each one" isn't specific enough.
- cancel-async-tasks: 2/4 — without "test edge cases specifically", the generic enumeration doesn't trigger concurrency testing.
- pypi-server and llm-inference held steady — these tasks benefit from any verification prompt.
- The "Enumerate all requirements, constraints, and edge cases" instruction is too abstract. The agent doesn't know WHAT to enumerate without the concrete examples H4 provides (performance targets, boundary conditions, each output individually).

**Verdict**: Forced enumeration without specific technical guidance performs no better than baseline. The active ingredient in H4 isn't the structure (bullets vs graph) or the control flow (diamonds vs boxes) — it's the **specific technical examples** (performance benchmarks, edge cases, boundary conditions, individual outputs). Abstract instructions like "enumerate all requirements" are not actionable enough for the model. H12's diamond escape hatches weren't the problem — H4's specific content was the solution.

### Experiment 14: H14 — Reread-test-adversarial pipeline graph
**Job**: selfverify-h14
**Persona**: benchmark-h14.md
**Change**: Linear pipeline graph combining H1 (reread spec), H3 (test), and H5 (adversarial thinking): "Think you're done" → "Reread the spec" → "Does your solution meet all requirements, stated and unstated?" → "Carefully test with automated and manual tests" → "Is there any criterion an adversarial reviewer might use to reject?" → Deliver or Fix → loop back. Two decision diamonds, both loop back on failure.
**Hypothesis**: A forced linear pipeline with specific actionable steps (reread, test, adversarial review) and only one exit path should outperform H13's abstract enumeration. The adversarial reviewer framing from H5 (65%) combined with forced testing from H3 (62%) in a graph format.
**Reps**: 4

| Task | H14 | H4 | BL | H14 vs BL |
|------|-----|----|----|-----------|
| cancel-async-tasks | 1/4 (25%) | 4/4 (100%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 2/4 (50%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 2/4 (50%) | 3/4 (75%) | 3/5 (60%) | -10pp |
| pypi-server | 4/4 (100%) | 2/4 (50%) | 2/5 (40%) | **+60pp** |
| overfull-hbox | 3/4 (75%) | 4/4 (100%) | 2/5 (40%) | **+35pp** |
| llm-inference-batching-scheduler | 2/4 (50%) | 3/4 (75%) | 3/5 (60%) | -10pp |
| sparql-university | 3/4 (75%) | 2/4 (50%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 1/4 (25%) | 1/5 (20%) | -20pp |
| **Total** | **18/32 (56%)** | **21/32 (65%)** | **20/40 (50%)** | **+6pp** |

**Observations**:
- Beats H13 (50%) but still 9pp below H4 (65%).
- pypi-server: 4/4 (100%) — forced testing step works well for server tasks.
- overfull-hbox: 3/4 (75%) — recovered from H13's 1/4, the adversarial reviewer framing helps here.
- fix-code-vulnerability: 3/4 (75%) — consistent with H12, the graph loop drives iteration.
- cancel-async-tasks: 1/4 (25%) — big regression vs H4 (100%). The generic "adversarial reviewer" framing doesn't trigger specific concurrency/edge-case testing the way H4's "test boundary conditions specifically" does.
- llm-inference: 2/4 (50%) with 1 timeout — the multi-step pipeline may consume too much budget on this long task.
- Confirms: even in a well-structured graph, generic language ("adversarial reviewer") underperforms H4's specific technical directives ("test edge cases", "benchmark performance").

**Verdict**: The pipeline structure is sound but the language is too generic. The adversarial reviewer framing helps some tasks (overfull-hbox, fix-code-vulnerability) but fails on tasks requiring specific technical verification (cancel-async-tasks, llm-inference). H4's explicit "test boundary conditions" and "benchmark performance" remain more effective than "criterion an adversarial reviewer might use."

### Experiment 15: H15 — H14 pipeline + explicit edge case step
**Job**: selfverify-h15
**Persona**: benchmark-h15.md
**Change**: Added "Test edge cases and boundary conditions" as a forced box step in H14's pipeline, between the requirements check and the automated/manual testing step. This is H4's key language that drove cancel-async-tasks (4/4) and overfull-hbox (4/4).
**Hypothesis**: Adding H4's specific edge-case language to H14's pipeline should recover cancel-async-tasks and overfull-hbox without losing H14's other strengths.
**Reps**: 4

| Task | H15 | H14 | H4 | BL | H15 vs BL |
|------|-----|-----|----|----|-----------|
| cancel-async-tasks | 1/4 (25%) | 1/4 (25%) | 4/4 (100%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 3/4 (75%) | 2/4 (50%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 3/4 (75%) | 2/4 (50%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| pypi-server | 2/4 (50%) | 4/4 (100%) | 2/4 (50%) | 2/5 (40%) | +10pp |
| overfull-hbox | 2/4 (50%) | 3/4 (75%) | 4/4 (100%) | 2/5 (40%) | +10pp |
| llm-inference-batching-scheduler | 3/4 (75%) | 2/4 (50%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 3/4 (75%) | 3/4 (75%) | 2/4 (50%) | 4/5 (80%) | -5pp |
| break-filter-js-from-html | 0/4 (0%) | 0/4 (0%) | 1/4 (25%) | 1/5 (20%) | -20pp |
| **Total** | **17/32 (53%)** | **18/32 (56%)** | **21/32 (65%)** | **20/40 (50%)** | **+3pp** |

**Observations**:
- Slightly worse than H14 (53% vs 56%), and well below H4 (65%).
- Adding the edge case step didn't help cancel-async-tasks (still 1/4) — the graph format buries it among too many other steps.
- pypi-server regressed from H14's 4/4 to 2/4 — the extra step may be consuming budget.
- The graph now has 5 forced steps + a loop, approaching H6's "too many steps" problem.
- Confirms: more steps in the graph → worse results, consistent with the H6/H7 finding that combinations underperform focused singles.

**Verdict**: Adding more steps to the graph makes it worse, not better. The graph format cannot match H4's effectiveness because it distributes attention across too many nodes. H4's power comes from being a short, focused list of specific checks.

### Experiment 16: H16 — H15's steps as prose bullets
**Job**: selfverify-h16
**Persona**: benchmark-h16.md
**Change**: Identical 5-step verification sequence as H15, but as a numbered prose checklist instead of a graphviz graph: (1) Reread the spec, (2) Check all requirements stated and unstated, (3) Test edge cases and boundary conditions, (4) Carefully test with automated and manual tests, (5) Think like an adversarial reviewer. "If any check fails, fix the problem and start this checklist over from step 1."
**Hypothesis**: If bullets outperform graphs (as H4 vs H12 suggested), H16 should beat H15 with identical content.
**Reps**: 4

| Task | H16 | H15 (graph) | H4 | BL | H16 vs BL |
|------|-----|-------------|----|----|-----------|
| cancel-async-tasks | 1/4 (25%) | 1/4 (25%) | 4/4 (100%) | 2/5 (40%) | -15pp |
| fix-code-vulnerability | 3/4 (75%) | 3/4 (75%) | 2/4 (50%) | 3/5 (60%) | **+15pp** |
| protein-assembly | 3/4 (75%) | 3/4 (75%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| pypi-server | 3/4 (75%) | 2/4 (50%) | 2/4 (50%) | 2/5 (40%) | **+35pp** |
| overfull-hbox | 4/4 (100%) | 2/4 (50%) | 4/4 (100%) | 2/5 (40%) | **+60pp** |
| llm-inference-batching-scheduler | 3/4 (75%) | 3/4 (75%) | 3/4 (75%) | 3/5 (60%) | **+15pp** |
| sparql-university | 2/4 (50%) | 3/4 (75%) | 2/4 (50%) | 4/5 (80%) | -30pp |
| break-filter-js-from-html | 0/4 (0%) | 0/4 (0%) | 1/4 (25%) | 1/5 (20%) | -20pp |
| **Total** | **20/32 (62%)** | **17/32 (53%)** | **21/32 (65%)** | **20/40 (50%)** | **+12pp** |

**Observations**:
- Bullets beat graph by 9pp (62% vs 53%) with identical content — confirms the H4-vs-H12 finding.
- overfull-hbox: 4/4 (100%) vs graph's 2/4 — biggest format-driven difference. Bullets make "test edge cases and boundary conditions" more salient than a graph node.
- pypi-server: 3/4 vs graph's 2/4 — bullets also help here.
- cancel-async-tasks: still 1/4 — neither format helps. This task needs something specific that even H4's "test edge cases" alone doesn't consistently provide when combined with 4 other steps.
- Still 3pp below H4 (62% vs 65%). The 5-step checklist is slightly too long — H4 achieves more with 4 focused bullets.

**Verdict**: Confirms bullets > graphs for verification prompts. H16's 5-step checklist is competitive with H4 (62% vs 65%) but doesn't beat it. The extra steps (reread spec, adversarial reviewer) add overhead without enough marginal value over H4's focused technical bullets.

### Rerun: H4 validation (8 reps per task)
**Job**: selfverify-h4-full-rerun
**Purpose**: Validate whether H4's 65% holds up with more data.
**Reps**: 4 (combined with original 4 = 8 per task)

| Task | Original (4) | Rerun (4) | Combined (8) |
|------|-------------|-----------|---------------|
| cancel-async-tasks | 4/4 (100%) | 3/4 (75%) | 7/8 (87%) |
| overfull-hbox | 4/4 (100%) | 3/4 (75%) | 7/8 (87%) |
| protein-assembly | 3/4 (75%) | 3/4 (75%) | 6/8 (75%) |
| fix-code-vulnerability | 2/4 (50%) | 3/4 (75%) | 5/8 (62%) |
| pypi-server | 2/4 (50%) | 3/4 (75%) | 5/8 (62%) |
| sparql-university | 2/4 (50%) | 3/4 (75%) | 5/8 (62%) |
| llm-inference-batching-scheduler | 3/4 (75%) | 2/4 (50%) | 5/8 (62%) |
| break-filter-js-from-html | 1/4 (25%) | 0/4 (0%) | 1/8 (12%) |
| **Total** | **21/32 (65%)** | **20/32 (62%)** | **41/64 (64%)** |

**Conclusion**: H4's true rate is ~64%, very close to the original 65% estimate. The original run was representative. cancel-async-tasks regressed from 4/4 to 3/4 on rerun — the original 100% was lucky but 87% combined is still H4's strongest task.

### Rerun: H16 validation (8 reps per task)
**Job**: selfverify-h16-rerun
**Purpose**: Validate H16's 62% with more data.
**Reps**: 4 (combined with original 4 = 8 per task)

| Task | Original (4) | Rerun (4) | Combined (8) |
|------|-------------|-----------|---------------|
| llm-inference-batching-scheduler | 3/4 (75%) | 3/4 (75%) | 6/8 (75%) |
| fix-code-vulnerability | 3/4 (75%) | 3/4 (75%) | 6/8 (75%) |
| pypi-server | 3/4 (75%) | 3/4 (75%) | 6/8 (75%) |
| overfull-hbox | 4/4 (100%) | 1/4 (25%) | 5/8 (62%) |
| sparql-university | 2/4 (50%) | 2/4 (50%) | 4/8 (50%) |
| protein-assembly | 3/4 (75%) | 1/4 (25%) | 4/8 (50%) |
| cancel-async-tasks | 1/4 (25%) | 1/4 (25%) | 2/8 (25%) |
| break-filter-js-from-html | 0/4 (0%) | 1/4 (25%) | 1/8 (12%) |
| **Total** | **20/32 (62%)** | **15/32 (47%)** | **35/64 (55%)** |

**Conclusion**: H16's true rate is ~55%, well below H4's 64%. The original 62% was inflated by overfull-hbox luck (4/4 → 1/4 on rerun). H4 is genuinely better than H16 by ~9pp.

**Root cause analysis of H16 rerun failures**:
- cancel-async (3/4 fail): One-shot, 4 agent steps, zero verification. Agent skips checklist entirely.
- overfull-hbox (3/4 fail): Reads synonyms.txt but doesn't verify replacements against it. All fail `test_input_file_matches`.
- protein-assembly (2/4 fail): 25 steps on PDB API research rabbit hole, never writes output file.

**Key insight**: Post-implementation checklists are ineffective — agents either skip them or never reach them. Verification needs to happen DURING implementation, not after.

### Experiment 17: H17 — Test-first development
**Job**: selfverify-h17
**Persona**: benchmark-h17.md
**Change**: Added step 3 to How to Work: "Before writing your solution, write a comprehensive test suite that covers all requirements from the task, including edge cases and boundary conditions. These tests should fail initially." Removed "Do NOT follow TDD" from Critical Rules. Kept H16's Before You Stop checklist.
**Hypothesis**: Moving verification into the implementation loop (write tests first → implement → iterate until tests pass) should be more effective than post-implementation checklists that agents skip.
**Result**: 15/32 = **47%** (-3pp vs baseline)

| Task | Pass | Fail | Rate |
|------|------|------|------|
| cancel-async-tasks | 4 | 0 | 100% |
| llm-inference-batching | 4 | 0 | 100% |
| fix-code-vulnerability | 2 | 2 | 50% |
| protein-assembly | 1 | 3 | 25% |
| overfull-hbox | 1 | 3 | 25% |
| break-filter-js-from-html | 1 | 3 | 25% |
| sparql-university | 1 | 3 | 25% |
| pypi-server | 1 | 3 | 25% |

**Analysis**: Test-first actively hurt in three ways: (1) false confidence from bad tests — sparql-university agent wrote regex structural tests that passed immediately but validated nothing semantic; (2) wasted steps when no runtime available — overfull-hbox agent spent 6 steps writing Python tests, trying to run them, and deleting them since no Python was in the container; (3) didn't address actual failure modes — pypi-server agent's tests spun up ephemeral servers for validation but never left the server running persistently.

### Experiment 18: H18 — H4 bullets moved to How to Work
**Job**: selfverify-h18
**Persona**: benchmark-h18.md
**Change**: Moved H4's verification bullets (performance, edge cases, all outputs) from "Before You Stop" into "How to Work" step 3 as sub-bullets of "Implement the solution step by step, keeping these in mind throughout." Before You Stop reduced to just "verify one more time."
**Hypothesis**: Since verification prompts work by priming (not by being followed as procedures), placing them where the agent reads them before starting implementation should strengthen the priming effect. The agent reads "How to Work" at the start and internalizes the verification concerns during implementation, rather than encountering them in a "Before You Stop" section it may never reach.
**Result**: 22/30 = **73%** (+23pp vs baseline, +9pp vs H4 combined 64%). 2 trials still running at recording time (llm-inference-batching, overfull-hbox).

| Task | Pass | Fail | Rate |
|------|------|------|------|
| fix-code-vulnerability | 4 | 0 | 100% |
| llm-inference-batching | 3/3 (+1 running) | 0 | 100% |
| overfull-hbox | 3/3 (+1 running) | 0 | 100% |
| sparql-university | 3 | 1 | 75% |
| pypi-server | 3 | 1 | 75% |
| protein-assembly | 3 | 1 | 75% |
| cancel-async-tasks | 2 | 2 | 50% |
| break-filter-js-from-html | 1 | 3 | 25% |

**Analysis**: Strongest result of the entire experiment. Every task except break-filter (intractable) and cancel-async (consistently ~50% regardless of prompt) scored 75%+. The placement hypothesis appears validated — same content, different location, meaningfully better results. Needs rerun validation.

### Experiment 19: H19 — H4 + "use it like a user"
**Job**: selfverify-h19
**Persona**: benchmark-h19.md
**Change**: Added a 5th bullet to H4's "Before You Stop": "Use it like a user: Before submitting, use your solution the way a real user would. Start the server and make requests to it. Run the program and check the output. Open the file and read it. Don't just run automated tests — manually confirm the deliverable works end-to-end."
**Hypothesis**: Priming the agent to do manual end-to-end verification as a user would should catch failures like pypi-server (server not left running) and sparql (query not actually executed).
**Result**: 19/31 = **61%** (+11pp vs baseline, ~same as H4). 1 trial still running (pypi-server).

| Task | Pass | Fail | Rate |
|------|------|------|------|
| cancel-async-tasks | 4 | 0 | 100% |
| sparql-university | 4 | 0 | 100% |
| fix-code-vulnerability | 3 | 1 | 75% |
| pypi-server | 3/3 (+1 running) | 0 | 100% |
| llm-inference-batching | 2 | 2 | 50% |
| protein-assembly | 2 | 2 | 50% |
| overfull-hbox | 1 | 3 | 25% |
| break-filter-js-from-html | 0 | 4 | 0% |

**Analysis**: Adding a 5th bullet to H4 didn't improve overall performance — consistent with the "focused beats combined" finding. The "use it like a user" bullet helped sparql (100% vs H4's 75%) and pypi-server (100% vs 75%), but hurt overfull-hbox (25% vs H4's 75%) and llm-inference-batching (50% vs 75%). Adding content to the verification section dilutes the priming effect, even when the added content is good.
