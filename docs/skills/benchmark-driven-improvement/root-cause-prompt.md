# Root-Cause Analysis Subagent Prompt Template

Use this template when dispatching subagents to analyze eval failures.
Fill in the placeholders and dispatch.

---

## Prompt

You are doing comparative root-cause analysis on eval task failures. Your job
is NOT to categorize failures — it is to explain exactly WHY a task failed by
comparing the failing run against a passing run of the same task.

### What you have

**Failing run:** `{FAILING_RUN_ID}` at `s3://harbor-eval-results-526275945504/runs/{FAILING_RUN_ID}/`
**Passing run:** `{PASSING_RUN_ID}` at `s3://harbor-eval-results-526275945504/runs/{PASSING_RUN_ID}/`
**Region:** us-west-1

### Tasks to analyze

{TASK_LIST_WITH_REPS}

### For EACH task, do this exact sequence

**Step 1: Download both transcripts**
- Find the coordinator transcript (header has no `parent_session_id`) in the failing rep
- Find the coordinator transcript in a passing rep (same task, other run)
- If the failing run also has a passing rep, use that for comparison

**Step 2: Compare the first 10 tool calls side by side**

Write them out in two columns:

```
PASSING:                              FAILING:
1. spawn_agent(explorer)              1. task_list
2. task_list                          2. read_file(ops-task/SKILL.md)
3. spawn_agent(implementer)           3. list_dir
...                                   ...
```

Note WHERE behavior diverges and what the failing coordinator did instead.

**Step 3: If both delegated, compare the delegation task text**

Pull the full `task` argument from the `spawn_agent(implementer)` call in both.
Diff them. Specifically check:
- Did the failing coordinator forward the complete task spec?
- Are there format specifications, exact strings, or schemas that were
  paraphrased or omitted in the failing version?
- Did the failing coordinator add instructions that weren't in the passing version?

**Step 4: Check the verifier output**

Download `verifier/test-stdout.txt` from the failing rep. Note:
- Which specific test failed
- What was expected vs what was produced
- How close was the answer (e.g., 5/6 tests pass, or completely wrong)

**Step 5: Check the system prompt differences**

Download the transcript headers from both runs. Compare:
- `build_version` — are they the same binary?
- System prompt text — diff the prompts if build versions differ
- Non-interactive guidance — which version does each have?

**Step 6: Write the root cause**

For each task, write exactly:

```
TASK: {task_name}
FAILING: {run_id} rep-{N} | PASSING: {run_id} rep-{N}
BUILD: {failing_sha} vs {passing_sha}

DIVERGENCE POINT: [Where behavior differs — e.g., "step 3: failing reads
skill file instead of spawning explorer"]

WHAT PASSING DID: [The specific successful approach]

WHAT FAILING DID: [The specific failing approach]

WHY IT DIVERGED: [Root cause — e.g., "Skills section appears before Role
section in the prompt, priming the coordinator into implementer mode"]

VERIFIER: [What specifically failed in the test]

FIXABLE: [Yes/No — and what the fix would be if yes]
```

**Step 7: Interrogate if the cause is unclear**

If you can't determine WHY the behavior diverged from the transcripts alone,
use the OpenAI API to replay the failing conversation and ask the model:

```python
# See tools/interrogate_session.py for the full implementation
python3 tools/interrogate_session.py \
    --run {FAILING_RUN_ID} --rep {REP} --task {TASK} \
    --question "Why did you do X instead of Y?"
```

### What NOT to do

- Do NOT just describe the failure mode ("delegation info loss"). Show the
  SPECIFIC text that was lost and WHERE in the passing run it was included.
- Do NOT categorize without comparing. Every failure needs a side-by-side.
- Do NOT assume causes from the error message. Read the transcripts.
- Do NOT skip the system prompt comparison. Build version differences matter.

Save your report to `{OUTPUT_PATH}`
