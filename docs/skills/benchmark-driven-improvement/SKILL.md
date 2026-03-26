---
name: benchmark-driven-improvement
description: Use when investigating agent benchmark failures, iterating on agent behavior against eval tasks, or diagnosing why an agent fails specific coding challenges. Covers transcript analysis, session interrogation, fix iteration, and validation.
---

# Benchmark-Driven Improvement

Systematic process for diagnosing and fixing agent failures on benchmark evals.
Run the agent against tasks, analyze transcripts, identify systemic patterns,
iterate on code/prompts, and validate fixes.

**Core principle:** Benchmarks are a proxy for good autonomous engineering. Fixes
should improve general agent capability, not game specific tasks.

## Project-Specific Docs

Project-specific knowledge lives in the project repo, not this skill:
- `docs/experiments/NOTEBOOK.md` — current state, experiment log, key learnings
- `docs/experiments/backlog.md` — prioritized queue of next experiments
- `docs/experiments/infrastructure.md` — how to run evals for this project
- `docs/experiments/task-sets.md` — regression and target task lists

## First: Read the Notebook

Before doing anything, read the project's `docs/experiments/NOTEBOOK.md`. It contains:
- Current experimental state (what's shipped, baseline pass rates)
- What's been tried and what worked/didn't
- Key learnings from all experiments
- Full experiment log

If there's active work in progress, pick up where the notebook says to start.
If starting fresh on a new eval, follow the workflow below.

## The Full Workflow

When given an eval to tune against:

1. **Baseline** — run the eval, establish pass rates
2. **Root cause** — read transcripts for every failure (not error messages — transcripts)
3. **Inventory** — categorize failures by systemic pattern, prioritize fixes
4. **Fix** — one change at a time, test on affected tasks only (3+ reps)
5. **Validate** — after all individual fixes validated, run full eval
6. **Document** — update NOTEBOOK.md after every experiment

## Step 1: Run Baseline

**Rules:**
- Never reuse job names. Auto-generate unique names.
- Always use 1 rep for initial baselines (save budget for iteration).
- Record the job name and git SHA for provenance.

## Step 2: Root Cause Every Failure

**This is the most important step. Do not skip it. Do not guess from error messages.**

For each failing task, read the actual agent transcript and answer:

1. What did the coordinator do? Did it scout? Delegate? Verify?
2. What did the implementer do? What approach did it take? Where did it get stuck?
3. Why did the verifier fail? What specific assertion failed?
4. Did the coordinator catch the problem before submitting?
5. What would have fixed it? (Must be a general principle, not task-specific.)

## Step 3: Build Failure Inventory

Categorize every failure by its systemic root cause. Write a failure inventory with:
- Each failure's root cause (from transcript analysis, not guesses)
- Proposed fix (must be a general principle)
- Test plan (which tasks, how many reps)
- Execution order (highest impact first)

## Step 4: Fix and Test (Hill-Climbing Protocol)

Every change must make things better without making anything worse.

### The regression set

Before you start fixing, define a **regression set**: 5-9 tasks that currently pass
and span different categories (easy, moderate, hard). These must keep passing after
every change. Record the regression set in NOTEBOOK.md.

### For each fix

1. **Make one change.** One prompt edit, one code change. Not two.

2. **Build and test locally.**

3. **Commit on an experiment branch.** Every experiment that gets deployed
   MUST be committed first. This is non-negotiable — it gives you provenance,
   rollback safety, and prevents losing work to accidental checkout.

4. **Test on target tasks** — the tasks this fix is supposed to help. 3 reps.

5. **Test on regression set** — 1 rep each is enough since these should be
   reliable passes.

6. **Evaluate results:**
   - **Target tasks improved (2/3+) AND regression set holds:** Merge to main.
   - **Target tasks improved BUT regression set broke:** Do NOT merge. Root cause
     the regression.
   - **Target tasks didn't improve:** Do NOT merge. Document and move on after
     2-3 failed attempts.

**NEVER deploy an uncommitted experiment.** The experiment branch is your safety net.

### What counts as improvement

With 3 reps per task:
- 0/3 -> 1/3: Not conclusive. Could be noise. Try 2 more reps to confirm.
- 0/3 -> 2/3: Improvement. Ship it.
- 1/3 -> 2/3: Marginal. Probably improvement but confirm with the full eval later.
- 1/3 -> 3/3: Clear improvement.
- Any regression on regression set (was passing, now fails): Block. Investigate.

### Teaching to the test vs good engineering

The goal is NOT to pass specific tasks. The goal is to make the agent a better engineer.
Every fix must be a general principle:

- "Write deliverables early" — good. Helps any task where the agent runs out of time.
- "For chess tasks, use python-chess" — bad. Only helps one task.
- "Read /tests/ before delegating" — good. Helps any task with verifier expectations.
- "Put the QEMU monitor socket at /tmp/qemu-monitor.sock" — bad. Only helps one task.

If you can't articulate the fix as a general principle, it's teaching to the test.

## Step 5: Full Validation

Only after all individual fixes are validated, combined, and re-validated:

- **Overall pass rate** should be higher (or equal if fixes were narrow).
- **No task that passed in the baseline should now fail** (check explicitly).
- If there are regressions, identify which fix caused them.

Record the full eval result in NOTEBOOK.md alongside the baseline for comparison.

## Prompt Engineering for GPT Models

### What works
- **Imperative prose with CRITICAL markers**: "CRITICAL: You must spawn an implementer."
- **Positive framing**: "Before resorting to X, try Y first."
- **Concrete examples**: Showing exact tool call format anchors behavior.
- **Role framing**: "You are a dispatcher" > "You do NOT write code."
- **XML-tagged prerequisites in user messages**: `<mandatory_prerequisites>` blocks with
  numbered steps improve instruction adherence for models that deprioritize system prompts.

### What doesn't work
- **Graphviz flowcharts**: GPT ignores dot syntax. (Works for Claude.)
- **Prohibitions**: "NEVER use write_file" -- model uses shell heredocs instead.
- **Tool restriction at code level**: Model hallucinated or bypassed via shell.
- **Long complex prompts**: More instruction does not equal more compliance.

## Debugging Techniques

### Debrief: Resume and ask WHY

When an agent disobeys an instruction, resume its session and ask why. The model will
tell you which competing instructions it noticed and how it prioritized them.

```
resume_agent(session_id="...", message="You did X instead of Y. Why?")
```

This is fast (~30 seconds) and reveals instruction conflicts that aren't obvious from
reading the prompt.

**Caveat:** The model's self-report is reliable about WHICH instructions it noticed
and how it ranked them. It's less reliable about the deeper WHY (may rationalize).

## Anti-Patterns

| Anti-pattern | Instead |
|-------------|---------|
| Guessing root causes from error messages | Read the actual transcript |
| Reusing job names | Always use auto-generated names |
| Running full eval before understanding failures | Root cause -> fix -> test isolated -> then full eval |
| Bundling multiple changes | One change per experiment |
| Testing with 1 rep | 3+ reps minimum |
| Teaching to the test | Fixes must be general engineering principles |
| Skipping transcript analysis | Read transcripts, identify patterns across failures |
| Not verifying the binary | Always verify the deployed binary contains expected content |
| Not checking transcript headers | After run, verify the prompt in transcript header matches intent |

## Recording Results

Each entry: git SHA, model, tasks, reps, pass rate, behavioral observations,
whether fix was adopted or reverted. Update NOTEBOOK.md after every experiment.

## Infrastructure Reference

See the project's `docs/experiments/infrastructure.md` for deployment, launch
commands, results collection, and environment-specific details.
