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

For each failing task:

### 2a. Read the transcripts

Read the actual agent transcript (coordinator AND subagent sessions) and answer:

1. What did the coordinator do? Did it inventory? Delegate? Verify?
2. What did the implementer do? What approach did it take? Where did it get stuck?
3. Why did the verifier fail? What specific assertion failed?
4. Did the coordinator catch the problem before submitting?
5. What would have fixed it? (Must be a general principle, not task-specific.)

### 2b. Interrogate the sessions

**This is required, not optional.** After reading transcripts, resume the failed
sessions and ask the model WHY it made its decisions. Use `tools/interrogate_session.py`.

The model honestly reports which instructions it noticed, how it prioritized them,
and what it was aware of but chose not to follow. This frequently reveals root causes
that transcript reading alone cannot — e.g., the model knew a rule but violated it
because another instruction felt higher-priority, or a subagent lacked context it
needed because the coordinator didn't include it in the delegation.

Interrogate every agent involved in the failure chain, not just the coordinator.
If the reviewer made a bad call, interrogate the reviewer. If the implementer went
down a wrong path, interrogate the implementer.

Ask specific questions about the decision that went wrong:
- "Your prompt says X. Why did you do Y instead?"
- "Did you see instruction Z? How did it interact with instruction W?"
- "What information would you have needed to make the right decision?"

### 2c. NEVER conclude without interrogation

**Do not declare a ceiling, exhausted approach, or unsolvable problem without
having interrogated the failed sessions.** Transcript analysis alone is
insufficient — it shows WHAT happened but not WHY the model chose to ignore
an instruction. Session interrogation reveals the model's actual reasoning:
which instructions competed, how it ranked priorities, and what prompt change
would have produced different behavior.

What looks like stochastic non-compliance (model follows instruction 33% of the
time) is almost always a competing instruction conflict. The model deterministically
resolves the conflict — the apparent randomness comes from which instruction it
encounters first in its reasoning chain, which varies by run. Interrogation
reveals the competing instruction, which can then be removed or harmonized.

If you've iterated 2+ times without improvement and are tempted to move on:
interrogate first. The fix may be a single competing phrase.

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
  Stronger prohibitions can trigger WORSE compliance (inverse dose-response).
- **Tool restriction at code level**: Model hallucinated or bypassed via shell.
- **Long complex prompts**: More instruction does not equal more compliance.
- **Duplicate verification criteria**: If two sections define what "verified" means
  with different specificity, the model picks the stricter one — even if it
  contradicts the intended behavior. Harmonize or use forward references.

### Competing instructions
The most common cause of apparent stochastic non-compliance. The model follows
instruction A ~33% of the time and instruction B ~67% (or vice versa). This
looks random but is actually the model resolving a conflict between two
instructions in its prompt. Session interrogation reliably identifies both
sides of the conflict. The fix is to remove or harmonize the competing
instruction — not to strengthen the intended one (which can backfire).

## Root-Cause Analysis

**Root-causing means comparing, not categorizing.** For every failure, you need
BOTH the failing transcript AND a passing transcript for the same task. Then
you diff the coordinator behavior step by step.

### The comparison protocol

1. Download the coordinator transcript from a failing rep AND a passing rep
2. List the first 10 tool calls side by side
3. Find where behavior diverges — that's your investigation point
4. If both delegated, diff the delegation task text word for word
5. Check the verifier output — what specifically failed and how close was it?
6. Check system prompt differences (build version in transcript header)

### Dispatching root-cause subagents

Use the prompt template at `docs/skills/benchmark-driven-improvement/root-cause-prompt.md`.
It enforces the comparison protocol — the subagent must produce side-by-side
tool flows, delegation text diffs, and specific divergence points.

**Do NOT dispatch subagents with vague prompts like "analyze these failures."**
They'll produce surface-level categorization instead of real root causes. The
template exists to prevent this.

### Session interrogation (required — see step 2b)

Interrogation is a required part of root-cause analysis, not an optional fallback.

The tool uses `serf --resume-with` to replay the FULL conversation history,
placing the model back in its exact original context before asking questions.
It supports interrogating any session — coordinator, implementer, or reviewer.

```bash
# List all sessions for a rep (shows role, model, turn count)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK_NAME --list-sessions

# Interrogate the coordinator (default)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK_NAME

# Interrogate a subagent by index (from --list-sessions)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK_NAME \
    --session 3 \
    --question "Your prompt says X. Why did you do Y instead?"

# Interrogate a subagent by session ID prefix
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK_NAME \
    --session 01KMPF5M \
    --question "Why did you override the computational proof?"

# Custom questions (always include "what changes would fix this")
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK \
    --question "Your prompt says X. Why did you do Y instead?" \
    --question "What specific changes to your instructions would have made you do the right thing?"
```

**Interrogate every agent in the failure chain.** Use `--list-sessions` to see
all sessions, then `--session INDEX` to target specific ones. The coordinator's
delegation is often the root cause (missing context, wrong instructions), but
the subagent's own instruction conflicts are equally important.

**Always ask "what changes would fix this."** The model in its original context
is the best source for what framing would have changed its behavior. This
produces actionable prompt fixes, not just explanations.

**Reliable for:** which instructions competed, what the model noticed, how it
ranked priorities, what context was missing. **Less reliable for:** deeper "why"
(may rationalize).

### What good root causes look like

Bad: "Delegation info loss — coordinator paraphrased the spec."
Good: "Failing coordinator's spawn_agent task said 'ensure correct CSV format'
but omitted the actual header `period,severity,count`. Passing coordinator
included the full CSV example verbatim. The omission caused the implementer
to choose wide format (5 rows) instead of long format (15 rows). Verifier
failed on header check at test_outputs.py:40."

Bad: "Timeout — agent ran out of time."
Good: "Failing coordinator spent 4 rounds reading files directly (list_dir,
read_file x2, exec_command) before delegating at step 5. Passing coordinator
delegated at step 2. The 3 wasted rounds cost ~45 seconds of wall time,
and the implementer's PyStan sampling needed the full 900s budget."

## Verifying Deployed Binaries

**ALWAYS verify the binary before trusting eval results.** The build cache
and staging pipeline can silently deploy stale code.

```bash
# Check transcript header for build version
python3 -c "
import json, subprocess
content = subprocess.run(['aws', 's3', 'cp', 'S3_PATH', '-', '--region', 'us-west-1'],
    capture_output=True, text=True).stdout
h = json.loads(content.split(chr(10))[0])
print('build:', h.get('build_version'))
sp = h.get('system_prompt', '')
print('Has expected text:', 'YOUR_EXPECTED_STRING' in sp)
"

# Check binary directly
strings serf-linux-amd64 | grep "expected phrase"
strings serf-linux-amd64 | grep -c "must not appear"

# Check the S3 tarball (not just the local binary)
aws s3 cp s3://BUCKET/agents/RUN_ID/agent.tar.gz /tmp/check.tar.gz
tar xzf /tmp/check.tar.gz -C /tmp/check/
strings /tmp/check/serf-linux-amd64 | grep "expected phrase"
```

Use `make build-linux` which invalidates the Go embed cache automatically.

## Anti-Patterns

| Anti-pattern | Instead |
|-------------|---------|
| Analyzing transcripts without interrogating | Resume sessions and ask the model why it decided what it did |
| Concluding "prompt ceiling reached" without interrogation | Interrogate failures first — competing instructions are usually fixable |
| Categorizing failures without comparison | Side-by-side transcript diff against passing run |
| Guessing root causes from error messages | Read the actual transcript, both passing and failing |
| Dispatching subagents with vague prompts | Use the root-cause-prompt.md template |
| Reusing job names | Always use unique names |
| Running full eval before understanding failures | Root cause -> fix -> test isolated -> then full eval |
| Bundling multiple changes | One change per experiment |
| Testing with 1 rep | 3+ reps minimum |
| Teaching to the test | Fixes must be general engineering principles |
| Not verifying the deployed binary | Check transcript header build_version AND tarball contents |
| Trusting `go build` after changing embedded files | Use `make build-linux` (runs go clean -cache) |
| Staging agent dir under /tmp/ | Use isolated subdirectory to avoid parent-dir binary contamination |

## Recording Results

Each entry: git SHA, model, tasks, reps, pass rate, behavioral observations,
whether fix was adopted or reverted. Update NOTEBOOK.md after every experiment.

## Infrastructure Reference

See the project's `docs/experiments/infrastructure.md` for deployment, launch
commands, results collection, and environment-specific details.
