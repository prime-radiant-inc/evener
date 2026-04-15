# Root-Cause Analysis Subagent Prompt Template

Use this template when dispatching subagents to analyze eval failures.
Fill in the placeholders and dispatch.

---

## Prompt

You are doing deep root-cause analysis on an eval task failure. Your job is
to find what in the agent's ENVIRONMENT (instructions, delegation, system
prompt, workflow) caused the wrong decision — not to categorize or assign blame.

This is a blameless postmortem. The agents did what their instructions led
them to do. Your job is to find what was missing or misleading in those
instructions.

### What you have

**Wave:** `{WAVE_ID}`
**Results:** `{RESULTS_PATH}`
**Task:** `{TASK_NAME}`
**Passing reps:** {PASSING_REPS}
**Failing reps:** {FAILING_REPS}
**Native binary for interrogation:** `{NATIVE_BINARY_PATH}`

### Setup

```bash
cd {REPO_PATH} && set -a && source .env && set +a
```

### The sequence — follow it IN ORDER, do NOT skip steps

**Step 1: List sessions for ALL reps (passing and failing)**

```bash
# For each rep:
python3 tools/read_transcript.py --run {WAVE_ID} --rep {REP} --task {TASK_NAME} --list-sessions
```

Note the session numbers for the implementer, verifier, and coordinator in each rep.

**Step 2: Read the implementer transcript for one failing rep**

```bash
python3 tools/read_transcript.py --run {WAVE_ID} --rep {FAIL_REP} --task {TASK_NAME} \
    --session {IMPLEMENTER_SESSION} --full --limit 50
```

Find the FIRST wrong decision. Write it down:
> "At turn N, the agent [did X] when it should have [done Y]."

**Step 3: INTERROGATE the failing implementer (MANDATORY)**

You MUST actually run this tool. Do NOT simulate, imagine, or skip this step.

```bash
python3 tools/interrogate_session.py \
    --run {WAVE_ID} --rep {FAIL_REP} --task {TASK_NAME} \
    --session {IMPLEMENTER_SESSION} \
    --question "We're conducting a blameless postmortem. You [specific wrong
    decision from step 2]. Was there an issue with your instructions? Did
    something in your delegation or system prompt lead you down the wrong
    path? How could your instructions or system prompt have been better?" \
    --force
```

**Step 4: Read the verifier transcript for that failing rep**

```bash
python3 tools/read_transcript.py --run {WAVE_ID} --rep {FAIL_REP} --task {TASK_NAME} \
    --session {VERIFIER_SESSION} --full --limit 50
```

Find what the verifier tested and what it MISSED.

**Step 5: INTERROGATE the failing verifier (MANDATORY)**

```bash
python3 tools/interrogate_session.py \
    --run {WAVE_ID} --rep {FAIL_REP} --task {TASK_NAME} \
    --session {VERIFIER_SESSION} \
    --question "We're conducting a blameless postmortem. The implementer's
    work had [specific flaw] and your verification didn't catch it. Was
    there something in your instructions that led you to test the wrong
    thing? How could your verification instructions have been better?" \
    --force
```

**Step 6: Repeat steps 2-5 for EVERY other failing rep**

Each failing rep may have a different failure mechanism. Do not assume
they all fail the same way. Interrogate each one.

**Step 7: Read and interrogate a passing rep**

Read the passing implementer transcript. Find what it did differently at
the divergence point from step 2. Then interrogate:

```bash
python3 tools/interrogate_session.py \
    --run {WAVE_ID} --rep {PASS_REP} --task {TASK_NAME} \
    --session {IMPLEMENTER_SESSION} \
    --question "You got this right where other reps didn't. At [divergence
    point], you [did Y] while the failing rep [did X]. Was there something
    specific in your instructions or delegation that guided you correctly?
    What drove your choice?" \
    --force
```

**Step 8: Write the report**

For each failing rep, write:

```
REP {N} — {PASS/FAIL}

DECISION POINT: [Turn N: the agent faced choice X vs Y and chose X]

AGENT'S REASONING: [From interrogation — what the agent was optimizing for]

WHAT THE INSTRUCTIONS SAID: [The relevant instruction text]

WHAT WAS MISSING OR MISLEADING: [What instruction gap or conflict caused this]

INTERROGATION QUOTES:
  Implementer: "[exact quote from interrogation output]"
  Verifier: "[exact quote from interrogation output]"
```

Then write the cross-rep summary:

```
PATTERN: [Do the failing reps share a mechanism, or are they different?]

PASSING REP CONTRAST: [What the passing rep did differently and why]

ENGINEERING PRINCIPLES:
  Implementer: [One general principle, no task names]
  Verifier: [One general principle, no task names]

FIX LOCATION:
  [Which file, which section, what kind of change]
```

### Rules

- **Interrogation is mandatory for every failing rep AND the passing rep.**
  A report without interrogation quotes is incomplete. If the tool fails,
  report the error — do not substitute transcript reading.
- **Include the exact commands you ran** and quote the actual output.
  Do not paraphrase or summarize interrogation responses.
- **Focus on instructions, not the agent.** The question is always "what
  in the environment led to this?" not "why did the agent mess up?"
- **Each failing rep gets its own analysis.** Do not generalize from one
  rep to all reps without checking.
