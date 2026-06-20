---
name: benchmark-driven-improvement
description: Use when investigating agent benchmark failures, iterating on agent behavior against eval tasks, or diagnosing why an agent fails specific coding challenges. Covers regression detection, transcript analysis, session interrogation, fix iteration, and validation.
---

# Benchmark-Driven Improvement

Systematic process for diagnosing and fixing agent failures on benchmark evals.

**Core principle:** Benchmarks are a proxy for good autonomous engineering. Fixes
should improve general agent capability, not game specific tasks.

## HARD GATE: Root-cause EVERY sub-perfect task

**Every task that scores below 1.0 gets full root-cause analysis.** No
exceptions. Not "just the regressions." Not "skip the pre-existing ones."
Not "this one looks like noise." EVERY failure.

- If a task scored 2/3, the 1 failing rep gets interrogated.
- If a task scored 1/3, BOTH failing reps get interrogated — they may
  have different failure mechanisms. Do not pick one and skip the other.
- If a task improved from 0/3 to 2/3, the 1 remaining failure STILL gets
  interrogated — the improvement doesn't excuse the gap.
- "Pre-existing" describes how long you've been failing to fix it, not that
  it's unfixable. The passing reps PROVE it can pass.
- "Stochastic" is the classification of last resort. See the stochastic
  checklist below — every box must have a concrete answer.

**Interrogate ALL failing reps AND the passing rep.** Every failing rep
gets its own interrogation — do not generalize from one to all. The
passing rep reveals what correct behavior looks like. Without it, you're
guessing at what "fixed" means. Compare at the FIRST point of divergence.

**Interrogation is a blameless postmortem.** Do not ask "Why did you do X?"
(produces defensive rationalization). Ask "Was there an issue with your
instructions? Did something in your delegation or system prompt lead you
down the wrong path? How could your instructions have been better?" Focus
on the environment we control, not the agent's reasoning.

**Do not make ship/iterate/abandon decisions until RCA is complete.** Scores
alone don't tell you whether a change is safe. A tied score can hide a real
regression masked by a lucky win elsewhere. Only RCA reveals the mechanism.

**One RCA subagent per task. Always.** When dispatching deep root-cause
analysis, launch ONE dedicated subagent for EACH task being investigated.
Never batch multiple tasks into one subagent. Each task has its own failure
mechanism, its own passing/failing reps, its own interrogation questions.
A subagent handling 4 tasks does shallow work on all 4. A subagent handling
1 task does deep work on that task — reads every transcript, interrogates
at the decision point, compares pass vs fail, extracts the engineering
principle. The cost of extra subagents is trivial; the cost of shallow RCA
is wasted experiments.

## BEFORE ANYTHING: Environment & Tool Check

**Do this FIRST, before reading the notebook or starting any investigation.**

```bash
# 1. Load API keys
set -a; source .env; set +a

# 2. Verify interrogation works (pick any recent run from docs/experiments/runs/)
python3 tools/transcripts/interrogate_session.py \
    --run RECENT_WAVE --rep 1 --task ANY_TASK --list-sessions
```

If step 2 fails with "no LLM providers configured" or similar:
**STOP. Fix the environment before proceeding.** Do not work around broken
interrogation by substituting transcript reading. Transcript reading shows
WHAT happened. Interrogation reveals WHY. They are fundamentally different
steps and one cannot substitute for the other. If you cannot interrogate,
you cannot complete this workflow.

## Scoring convention: reward-based, not exception-based

**Use reward-based scoring. A rep's score is `verifier_result.rewards.reward`
in `result.json` (or the contents of `verifier/reward.txt`, which mirrors it).
A rep passes when the reward is 1.0 and fails when it is anything less.
Exceptions are informational, not authoritative.**

In particular: a rep that hit `AgentTimeoutError` (`exception_info.exception_type
== "AgentTimeoutError"`) can still pass with `reward == 1.0`. The harbor
verifier runs after the agent is killed and grades the final workspace state.
If the implementer left the workspace in a passing state before the timeout,
the verifier returns 1.0 and the rep is a pass — even though `communicate`
was never called and the agent's session ends in an exception.

Do NOT use a "TIMEOUT = fail" working convention when manually scoring a wave.
It undercounts these reps and can flip experiment verdicts. The session-26
scoring used this convention and four passes were missed; see
`docs/experiments/session-26-verifier-fixes/summary.md` for the corrected
chart and the per-experiment score deltas.

When a chart needs to distinguish the two timeout flavors, use:
- `T*` — timeout but `reward == 1.0` (counts as a pass)
- `T`  — timeout with `reward < 1.0` (a real fail)

The provided scoring tools (`wave_scores.py`, `wave_compare.py`,
`eval_results.compute_score`, `serf-report`, `dashboard/data.py`) all read
the reward field. The "TIMEOUT = fail" mistake is a HUMAN scoring habit, not
something any of the tools do — but if you build an ad-hoc scoring script,
make sure it follows the rule above.

## BEFORE COMPARING WAVES: Infrastructure Validation

**MANDATORY before attributing ANY score drop to prompt changes.**

A wave can be silently corrupted by billing quota exhaustion (OpenAI
`insufficient_quota` 429), spot instance failures, or Docker issues.
These produce reps with 0 tokens that look like agent failures but aren't.

```bash
# Download all result.json files for the wave
aws s3 sync "s3://harbor-eval-results-526275945504/runs/WAVE_ID/" /tmp/WAVE-results/ \
    --exclude "*" --include "*/result.json"

# Count infrastructure failures
python3 -c "
import json, glob
silent = sum(1 for f in glob.glob('/tmp/WAVE-results/**/result.json', recursive=True)
    if (lambda d: (d.get('agent_result') or {}).get('n_output_tokens', 0) == 0
        and (d.get('agent_result') or {}).get('n_input_tokens', 0) == 0
        and not d.get('exception_info'))(json.load(open(f))))
print(f'Silent failures (0 tokens, no exception): {silent}')
# Normal: 2-5 per wave. If >10, wave is contaminated.
"
```

**If silent failures > 5:** The wave is unreliable. Check api.jsonl files
for `insufficient_quota` or `429` errors. Do NOT use this wave for comparisons.

**Session 16 lesson:** wave-e7c0b48 had 38 silent failures (vs normal ~3) due
to billing quota exhaustion at 10:34 UTC. This was misdiagnosed as a -9.0 point
prompt regression, leading to panicked experiment reverts. The experiments were
fine — the wave was broken.

## Read the Notebook

Read `docs/experiments/NOTEBOOK.md`. It has current state, shipped fixes, regression
targets, and what to do next. For prompt behavior patterns, read `prompt-lessons.md`.

## Key Tools

```bash
./tools/eval/serf-report compare CURRENT BASELINE  # find what improved/regressed
./tools/eval/serf-report regressions               # tasks below historical best
./tools/eval/serf-report history TASK              # full timeline with regression markers
./tools/eval/serf-report cost WAVE                 # token/dollar costs per task
./tools/eval/serf-report dashboard                 # generate HTML dashboard
./tools/eval/wave_scores.py WAVE_ID                # live scores (N tasks scored, X/N complete, Y perfect)
./tools/eval/wave_compare.py --labels "a,b" W1 W2  # side-by-side wave comparison with deltas
./tools/eval/run_eval.sh --wave --tasks "t1,t2"    # launch evals
./tools/eval/post_run.sh WAVE_ID                   # collect results
```

For full tool reference, see `tools-reference.md` in this skill directory.

## Two Workflows

### A. Regression Recovery (recovering lost performance)

Use when tasks that used to pass now fail. This is the highest-leverage work.

1. **Find regressions:** `./tools/eval/serf-report regressions`
2. **For each regressed task:** follow the Investigation Sequence below
3. **Write experiments** for behavioral regressions, skip capability gaps
4. **Run experiments** using the Three-Phase Execution Process below

### B. New Improvement (raising the ceiling on never-passed tasks)

Use when all regressions are addressed and you want to improve tasks.

1. **Baseline** — run discriminators (`--tasks discriminators`), establish pass rates
2. **Root cause** — interrogate every failure (not just read transcripts)
3. **Inventory** — categorize by systemic pattern, prioritize
4. **Write experiments** — one change at a time
5. **Run experiments** using the Three-Phase Execution Process below
6. **Document** — update NOTEBOOK.md and prompt-lessons.md

## The Investigation Sequence (for each failing task)

This is the core loop. Follow it for every task, every time.

### Step 0: Check for infrastructure failure

Before investigating the agent's behavior, rule out infra:

```bash
# Check result.json for this specific rep
# If n_output_tokens == 0 AND n_input_tokens == 0: agent never ran (billing/infra)
# If exception_info contains "insufficient_quota" or "429": billing failure
# If exception_info contains "Docker": environment setup failure
# If exception_info contains "Timeout": agent was working but slow — NOT
#   automatically a fail. Read verifier_result.rewards.reward; if it is 1.0
#   the rep is a pass even though the agent was killed mid-execution.
```

If this rep is an infra failure, **skip it** — investigate a different failing
rep, or mark the task as needing a rerun.

### Step 1: Get the history

```bash
./tools/eval/serf-report history TASK_NAME
```

This shows every run, when the task last passed, and when it regressed.
For regressions, note the git SHA where the regression was introduced.

### Step 2: Diff the prompts (regressions only)

```bash
git diff LAST_PASSING_SHA..FIRST_FAILING_SHA -- internal/bundled/agents/ agent/prompts/
```

If a prompt change correlates with the regression, that's your prime suspect.

### Step 3: Read transcripts — passing AND failing

Read the full coordinator + subagent transcripts for BOTH a passing rep and
a failing rep. You're looking for where behavior diverges.

```bash
# List sessions
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --list-sessions
# Read coordinator tool calls
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --tool-calls
# Read a specific session
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --session 0 --full --limit 30
```

Compare side by side. Find the FIRST point of divergence. Everything before
that point was fine. Everything after is a consequence.

### Step 4: Interrogate the failing agent

**This is mandatory. Do not skip it.**

**Before interrogating, rebuild the native binary from the correct branch:**

```bash
git checkout THE_BRANCH_THAT_PRODUCED_THE_RUN
go build -o serf ./cmd/serf/
strings serf | grep "a phrase from the experiment's prompt change"
```

The interrogation tool uses the local `serf` binary to resume sessions.
If this binary is stale (different branch, different date), the model
sees the WRONG system prompt and cites instructions that don't exist in
the experiment. This produces false root causes. Always verify the binary
matches the experiment before interrogating.

Then resume the failed session and ask the model WHY at the exact
decision point:

```bash
export $(grep -v '^#' .env | xargs)
python3 tools/transcripts/interrogate_session.py \
    --run WAVE --rep N --task TASK \
    --question "At turn N you did X. Your prompt says Y. What were you optimizing for?"
```

See `root-cause-task-failure.md` in this skill directory for the full
interrogation methodology, question templates, and interpretation guidance.

### Step 5: Classify the root cause

**"Stochastic" is the classification of last resort.** To classify a task
as stochastic, you must complete this checklist IN YOUR REPORT for that task.
Every box must have a concrete answer, not "N/A" or "skipped":

- [ ] Read ≥2 failing transcripts. Which reps? What tool calls did each make?
- [ ] Read ≥1 passing transcript. Which rep? What tool calls did it make?
- [ ] Ran `interrogate_session.py` on ≥1 failing rep (not just read the
  transcript — actually ran the tool and got a model response). Paste the
  interrogation output.
- [ ] Ran `git diff` between last-passing SHA and first-failing SHA on
  `internal/bundled/agents/` and `agent/prompts/`. Was there a prompt change? If yes,
  what was it? Why isn't it the cause?
- [ ] The passing and failing agents used IDENTICAL strategies (same tool
  sequence, same approach, same delegation pattern) with different outcomes.
  Describe the identical strategy and where the outcomes diverged.

If ANY box is empty, you cannot classify as stochastic. If the passing and
failing agents used DIFFERENT strategies (different tool choices, different
approaches, different delegation text), that is behavioral, not stochastic —
investigate why the strategies differed.

**Classification categories** (from `root-cause-task-failure.md`):
- **Instruction conflict** — two instructions pull opposite directions
- **Missing delegation context** — coordinator omitted critical info
- **Training prior override** — model's default behavior ignores instructions
- **Verification gap** — agent tests the wrong thing
- **Workflow structure mismatch** — wrong time/turn allocation
- **Genuine capability gap** — task beyond model ability (requires 3+ failed structural experiments to declare)

### Step 6: Write experiment or document finding

For behavioral issues → write experiment file in `docs/experiments/backlog/`
following `TEMPLATE.md`. Include interrogation evidence, exact OLD/NEW text,
target tasks, and regression tests.

**The regression tests in your experiment file MUST come from a blame analysis**
(see below). Do not use a generic regression set.

For capability gaps → document in NOTEBOOK.md and move on.

## Blame Before You Change (mandatory)

**Every time you change agent prompts or skills — whether writing an experiment
file, applying a change, or editing code — you MUST run a blame first.**

This is not optional. This is not "if you have time." This is a hard gate.
The purpose: understand what other tasks depend on the text you are about to
modify, so you can include them in your regression set and avoid silently
breaking previously shipped fixes.

### The blame process

```bash
# 1. Identify the lines you plan to change
#    Read the file, find your OLD text

# 2. Blame those lines to find what added them
git blame internal/bundled/plugins/coordinator-workflow/agents/implementer.md -L 90,95
# → each line shows the commit SHA that last touched it

# 3. For each commit SHA, check if it was an experiment
git log --oneline COMMIT_SHA -1
# → "experiment: cancel-async-tasks-2 — clean shutdown standard"
# If the commit message includes task results (per our commit format),
# you can read them directly. Otherwise:

# 4. Find that experiment's target tasks
cat docs/experiments/completed-improved/cancel-async-tasks-2.md | grep -A5 "Target Tasks"
# → cancel-async-tasks was the target task

# 5. Those tasks are now MANDATORY in your regression set
```

### The rule

If your change modifies, replaces, or removes text that was added by a
previous experiment, that experiment's target tasks are mandatory regression
tests. You are changing code that was proven to fix those tasks — you must
verify you haven't broken them.

This applies to:
- **Experiment files** — the Regression Tests section must include blame-derived tasks
- **Direct prompt edits** — before committing, blame what you're changing
- **Refactoring** — even "harmless" rewording can break instruction ordering effects

Always ALSO include distribution-search and portfolio-optimization as baseline
regression checks (they're fast and catch broad breakage).

### Example

You want to add a sentence to the implementer's Verify task. Before writing
the experiment:

```bash
git blame internal/bundled/plugins/coordinator-workflow/agents/implementer.md -L 45,55
# Line 48: 8209375  (experiment: build-cython-ext-1 — preserve existing packages)
# Line 50: 3496f7e  (results: Wave 1 complete)
# Line 52: 0026d39  (experiment: db-wal-recovery-1 — preserve data before inspection)
```

Your regression set must include: build-cython-ext, db-wal-recovery, plus
distribution-search and portfolio-optimization.

## Experiment Execution: Three-Phase Process

Every experiment goes through three phases. Each phase has an explicit
decision gate. Do NOT skip phases or advance past a failing gate.

### Setup (before any phase)

1. Branch from main: `git checkout -b exp/NAME main`
2. **Run blame** on the lines you're changing (see Blame section above).
   The blame-derived tasks are MANDATORY in your Phase 1 regression set.
3. Apply the change, commit on the branch
4. Build and verify binary:
   ```bash
   make build-linux && strings serf-linux-amd64 | grep "expected phrase"
   ```
5. Build native binary for interrogation:
   ```bash
   go build -o serf ./cmd/serf/
   strings serf | grep "expected phrase"
   set -a; source .env; set +a  # load API keys
   ```

### Phase 1: Targeted test (5-10 tasks, ~15 min)

Run ONLY the tasks the experiment targets + blame-derived regression tasks.
**Also run a CONTROL** — the same tasks with the unmodified baseline binary.

```bash
# Experiment
./tools/eval/run_eval.sh --tasks "target1,target2,blame-task1,blame-task2" --reps 3

# Control (build from clean main, same tasks, same reps)
./tools/eval/run_eval.sh --tasks "target1,target2,blame-task1,blame-task2" --reps 3 \
    --run-id "exp-NAME-control-TIMESTAMP"
```

The control establishes what the baseline does on THIS run, with THIS
infrastructure, at THIS time. Without it you're comparing against a stale
wave that may have had different infra conditions. A control that scores
3/3 on a task the experiment also scores 3/3 on tells you nothing. A control
that scores 1/3 while the experiment scores 3/3 is real signal.

After results arrive:

1. **Validate infra** on BOTH experiment AND control — check for silent failures.
2. **Interrogate EVERY failing rep** on target tasks. Not transcript reading —
   real `interrogate_session.py` with specific questions about the change:
   - "Did you see the instruction about X? How did it affect your behavior?"
   - "What would have gotten you to do Y instead of Z?"
   Use the native binary you built in Setup (it matches this experiment's prompts).
3. **Decision gate:**
   - Target tasks improved AND regression tasks hold → proceed to Phase 2
   - Target tasks did NOT improve → STOP. The experiment doesn't work.
     Interrogation should tell you WHY. Revise or abandon.
   - Regression tasks regressed → STOP. Interrogate to understand the
     interaction, then revise.

### Phase 2: Discriminator run (62 tasks, ~45 min)

Run all discriminator tasks to check for broad regressions.

```bash
./tools/eval/run_eval.sh --tasks discriminators
```

After results arrive:

1. **Validate infra** — same check. If >5 silent failures, rerun.
2. **Compare against the same-day CONTROL** (not a stale baseline). Use
   `./tools/eval/wave_compare.py --labels "control,variant" CONTROL_WAVE VARIANT_WAVE`
   for side-by-side per-task deltas. For multiple variants:
   `./tools/eval/wave_compare.py --labels "control,27a,27b,27d" W_CTRL W_A W_B W_D`
3. **Interrogate EVERY task that scored LOWER than baseline.** For each:
   - Is it infra? (Step 0 of investigation sequence)
   - Is it caused by this experiment? (Interrogate with specific question
     about the changed text)
   - Is it stochastic? (Full stochastic checklist required)
4. **Decision gate:**
   - Net score >= baseline AND no unexplained regressions → proceed to Phase 3
   - Net score < baseline → STOP. Interrogation should identify which
     regressions are experiment-caused vs stochastic. If experiment-caused
     regressions outweigh gains, do not ship.

### Phase 3: Cross-check (8 tasks, ~10 min)

Run the always-perfect tasks as a safety net.

```bash
./tools/eval/run_eval.sh --tasks crosscheck --run-id WAVE_ID --backfill
```

After results arrive:

1. **Validate infra.**
2. **If ANY always-perfect task scores below 1.000:** The experiment is
   causing broad harm. Do NOT ship. Interrogate the failure to understand
   what went wrong.
3. **Decision gate:**
   - All 8 tasks at 1.000 → proceed to Ship
   - Any drop → STOP and investigate

### Ship gate

ALL of the following must be true:

- [ ] Phase 1: target tasks improved
- [ ] Phase 1: regression tasks held
- [ ] Phase 1: every failing rep interrogated (paste interrogation output)
- [ ] Phase 2: net score >= baseline (infra-validated comparison)
- [ ] Phase 2: every new regression interrogated and classified
- [ ] Phase 3: all 8 cross-check tasks at 1.000
- [ ] Infra validated at every phase (silent failures < 5)

Then:

```bash
git checkout main
git merge --no-ff exp/NAME -m "experiment: NAME — description

PHASE 1 (targeted):
  target-task-1: X/3 → Y/3
  regression-task-1: 3/3 → 3/3

PHASE 2 (discriminators):
  Mean: 0.XXX (baseline: 0.XXX)
  New regressions: none | task-a (stochastic, interrogated)

PHASE 3 (cross-check):
  All 8 tasks: 3/3

Co-Authored-By: ..."
```

Move experiment file to `completed-improved/` or `completed-did-not-improve/`.
Update NOTEBOOK.md and prompt-lessons.md.

### What counts as improvement

- 0/3 → 2/3+: Ship it
- 0/3 → 1/3: Marginal. Ship only if change is light and no regressions
- 1/3 → 3/3: Clear improvement
- Any regression on regression set: Block and investigate (do not ship)

### Choosing target tasks

Before selecting target tasks for Phase 1, trace the root cause back to
the experiment log:

1. Search `experiment-log.md` for the failure mode your experiment addresses
2. Find which tasks were ORIGINALLY documented with that root cause
3. Find which experiments previously fixed or attempted to fix those tasks
4. Check if the previous fix is still in the prompts (it may already cover your change)
5. Your Phase 1 targets are the tasks from step 2, NOT tasks that sound
   vaguely related

**Session 16 example:** "verify-after-cleanup" was proposed because ONE rep
of large-scale-text-editing had "cleanup removed expected.csv" in the
regression report. But tracing the full chain revealed:
1. The dominant failure mode for that task is Vim macro fragility, not cleanup
2. The cleanup that removed expected.csv was IMPLEMENTER cleanup, not coordinator
3. The implementer already has the H2 fix ("If cleanup undid something, restore it")
4. The experiment added a COORDINATOR-side check for an IMPLEMENTER-side problem
5. sqlite-with-gcov (the other target) fails for PATH reasons, not cleanup at all
The experiment addressed a real but rare finding (1 rep) at the wrong
intervention point. Dropped after Phase 1 interrogation confirmed no effect.

### Teaching to the test

Every fix must be a general engineering principle. If you can't articulate
the fix without naming the target task, it's teaching to the test.

## Prompt Engineering Rules

See `prompt-lessons.md` for the full catalog. Key rules:

- **Implementation standards > verification gates** — tell the agent HOW to
  write code, not just to CHECK afterward
- **Positive framing > prohibitions** — "verification is reading" beats
  "NEVER re-derive"
- **Instruction position matters** — numbered workflow steps get ~100%
  compliance, standalone sections get ~50-60%
- **Competing instructions cause stochastic non-compliance** — always
  interrogate to find the conflict
- **Inverse dose-response** — stronger prohibitions can cause WORSE compliance

## Anti-Patterns

| Anti-pattern | Instead |
|-------------|---------|
| Substituting transcript reading for interrogation | They are different steps. Transcripts show WHAT. Interrogation reveals WHY. If interrogation tools are broken, fix them — do not proceed without them. |
| Working around broken tools silently | STOP and fix the tool, or tell the user it's broken. Never pretend a workaround is equivalent to the real step. |
| Classifying by history pattern alone ("scores are noisy") | Read the actual transcripts and interrogate. Score history tells you THAT something changed, not WHY. |
| Declaring "stochastic" without the full checklist | Every box in the stochastic checklist must have a concrete answer. Empty boxes = incomplete investigation, not stochastic. |
| Declaring "stochastic" when strategies differ | If passing and failing reps used different approaches, that's behavioral — investigate why the approaches differed. |
| Analyzing error messages instead of transcripts | Read the full transcript end-to-end |
| Interrogating without a specific question | Ask about the exact decision point |
| Strengthening a losing instruction | Remove or harmonize the competing instruction |
| Bundling multiple changes | One change per experiment |
| Comparing against "most recent" scoreboard | Compare against the hardcoded baseline wave ID |
| Shipping without interrogation evidence | Confirm causal link between change and improvement |
| Declaring capability gap after 1 experiment | Try 3+ structural changes first |
| Shipping after Phase 1 only (narrow A/B test) | Run all three phases. Phase 1 targets can look great while Phase 2 reveals broad regressions on untested tasks. Session 16 learned this the hard way. |
| Skipping RCA on "pre-existing" failures | Pre-existing = you've been failing to fix it. The passing rep proves it's fixable. Investigate. |
| Skipping RCA on passing reps | Compare passing vs failing to find the divergence. Without the passing rep, you're guessing. |
| Making ship/iterate/abandon decisions on scores alone | Scores + RCA. A tied score can hide regressions masked by lucky wins. |
| Asking text questions to probe self-awareness | Text produces rationalization. Force tool calls whose exit codes can't be rationalized. "Did you verify?" → rationalization. `env -i python3 -c 'import X'` → evidence. (Session 27 finding.) |
| Comparing waves without infra validation | Always count silent failures (0 tokens) before attributing score drops. Session 16: 38 phantom failures misdiagnosed as -9.0pt prompt regression. |
| Reverting experiments based on a single bad wave | Interrogate first. One wave's noise is not evidence. Need infra validation + interrogation before reverting. |
| Comparing partial-wave means mid-run | Each variant has a different mix of tasks completed at any given moment. The wave with more easy tasks scored looks better. Wait for full completion, then compare per-task, per-rep. |
| Manually parsing transcript JSONL | Use `interrogate_session.py` and `read_transcript.py`. The transcript format is complex and tool-specific. Manual parsing wastes time and produces wrong results. |
| Proposing infrastructure hacks instead of clean designs | Brainstorm multiple approaches before proposing infra changes. Prefer designs that work WITH the model (task steps, prompts) over designs that fight it (parsing model output, filesystem hacks). Use the brainstorming skill for non-trivial design decisions. |

## Parallel Experiment Execution

When you have multiple independent experiments ready, run them in parallel
rather than sequentially. Each experiment gets its own isolated worktree,
branch, binary, and eval wave. The orchestrator stays on main and evaluates
results as a batch.

### When to parallelize

- Multiple experiments target DIFFERENT prompt files or non-overlapping lines
- Each experiment has its own target tasks (minimal overlap is fine)
- You have enough AWS vCPU quota for concurrent waves

Do NOT parallelize experiments that modify the same lines — those must be
tested sequentially or combined into a single experiment.

### The workflow

1. **Orchestrator creates worktrees** — one per experiment, branched from
   the current main (which includes any code fixes like tool serialization):

   ```bash
   git worktree add .claude/worktrees/exp-NAME exp/NAME
   ```

2. **Dispatch one subagent per experiment.** Each subagent receives:
   - The worktree path
   - The experiment file path (in `docs/experiments/backlog/`)
   - Instructions to: apply the change, build, launch eval, monitor, report

3. **Subagent lifecycle** (in the worktree):
   ```bash
   cd WORKTREE_PATH
   # Apply experiment (edit the prompt file per the experiment's OLD/NEW)
   # Commit on the experiment branch
   # Build: make build-linux
   # Verify: strings serf-linux-amd64 | grep "expected phrase"
   # Launch: ./tools/eval/run_eval.sh --tasks "target1,target2,regression1" --reps 3
   # Monitor: poll wave_scores.py until complete
   # Report results back to orchestrator
   ```

4. **Orchestrator evaluates the batch:**
   - Collect all wave results
   - Compare each experiment against baseline (wave-092c36a or equivalent)
   - Interrogate every failure on target tasks
   - Decide which experiments ship, which need revision, which are dropped
   - Ship winners: merge experiment branches to main one at a time,
     checking for conflicts

### Subagent dispatch template

```
You are running experiment "{NAME}" in worktree "{PATH}".

Experiment file: docs/experiments/backlog/{NAME}.md
Read it for the exact OLD/NEW text, target tasks, and regression tasks.

Steps:
1. cd {WORKTREE_PATH}
2. Read the experiment file and apply the OLD→NEW change to the prompt file
3. git add -A && git commit -m "experiment: {NAME}"
4. make build-linux
5. strings serf-linux-amd64 | grep "EXPECTED_PHRASE"
6. set -a; source .env; set +a
7. ./tools/eval/run_eval.sh --tasks "TARGET_TASKS,REGRESSION_TASKS" --reps 3
8. Monitor wave_scores.py until all reps complete
9. Report: per-task scores, comparison to baseline, any regressions

If a target task improves, interrogate a passing rep to confirm causality.
If a regression task drops, interrogate to determine if experiment-caused.
```

### Evaluating the batch

After all waves complete:

1. **Infra-validate** every wave (check for silent failures)
2. **Per-experiment decision:** improved targets + held regressions → candidate
   to ship. Any regression → interrogate before deciding.
3. **Conflict check:** if two shipping experiments touch nearby lines, test
   them combined before merging both. The combined effect may differ.
4. **Ship order:** merge experiments one at a time to main. After each merge,
   verify the combined binary still builds and `strings` shows all expected
   phrases from all merged experiments.

## Session 27 additions

### Subagent workspace snapshots are now fresh

Commit `20d3db9` fixed a fundamental bug: subagents previously reused the
coordinator's stale workspace snapshot (captured before the implementer ran).
The verifier would see a pre-work baseline and treat implementer-produced
artifacts as "new files I created." Now `ScanWorkspace` runs at subagent
spawn time. This changes debugging assumptions — if a verifier claims it
doesn't see a file, the file genuinely doesn't exist (not a stale snapshot).

### Note/Restore verifier task steps were deleted

The "Restore noted state" task step instructed the verifier to delete files
it created and restore the workspace. This caused the verifier to delete
implementer-produced build artifacts (`.so` files, compiled binaries) because
the Note step captured pre-build state. Deleted in commit `7c37dae`. The
verifier's task list is now: Plan → Read → Run tests → Fresh eyes → Assess.

### "Understand before running" verifier rule

Commit `7cf9129`. Before executing any command, the verifier must state what
it will read, what it will change, and what it will prove. Proven to
causally improve bn-fit-modify (+2 reps: forced semantic column parsing) and
configure-git-webserver (+2 reps: prevented service-killing by forcing
consequence articulation before destructive commands).

### Transcript access

Commit `09d47f9` + `9681696`. Subagent spawn/wait results now include a
`transcript` field with the path to the subagent's JSONL transcript. After
context compaction, a SYSTEM-REMINDER tells the agent where to find its own
full pre-compaction transcript. The coordinator's Verify step hints it to
pass the implementer's transcript path to the verifier (but NOT to fix
agents — fix agents get a fresh start to avoid anchoring on failed approaches).

### Text questions produce text rationalization

Session 27 tested asking implementers "did you do a good job?" before
submission. Finding: 70% of genuinely-failing implementers accurately
self-reported failure when asked post-hoc. But pre-completion text questions
produced confident rationalization — the implementer described what it
BELIEVED was on disk, not what was actually there. Exit codes and command
output can't be rationalized; text can. Prefer forced tool calls over
text-based self-checks.

### Teaching to the test remains the biggest experiment-design risk

Session 27 experiments repeatedly named specific tools (vim, Python
`__pycache__`), specific failure modes from target tasks, or specific
harness behaviors. Every one that did was caught and revised. The rule:
**if you can't articulate the fix without naming the target task or its
specific toolchain, it's teaching to the test.** General engineering
principles only.
