# Microtask Replay Analysis — 2026-03-07

## Goal

Systematically replay failed H20 eval microtasks to identify improvement opportunities.
Each hypothesis gets tested against real API calls with the replay harness.

## Data

- Source: H20 full-suite eval (445 trials, 240 failures with trajectories)
- Categories: 101 TIMEOUT, 93 NEVER_SUBMITTED, 46 EARLY_QUIT
- Model: gpt-5.2-codex via OpenAI Responses API
- Tools: extract_microtasks.py + replay_microtask.py

## Improvement Candidates

| # | Hypothesis | Category | Status |
|---|-----------|----------|--------|
| 1 | `tool_choice=required` forces verification tool call. Model "wants" to verify but defaults to text when given the choice. **10/10 CHANGED in batch test.** | NEVER_SUBMITTED | **CONFIRMED** |
| 2 | Stronger verification language in "Before You Stop" section suppresses "Done" text but causes empty responses (model torn between text and action) | NEVER_SUBMITTED | TESTED — not viable alone |
| 3 | EARLY_QUIT is 100% API quota errors (45/46 `insufficient_quota`, 1 mid-turn failure). Not agent behavior — no fix needed. | EARLY_QUIT | **NOT APPLICABLE** |
| 4 | TIMEOUT loops: 32/101 timeouts have same bash command repeated 5+ times. Agent needs loop detection / "try a different approach" nudge. | TIMEOUT | IDENTIFIED |
| 5 | TIMEOUT stuck on long command: 14/101 have <=10 steps, likely hung on `apt-get install`, `pip install`, etc. Need bash timeout enforcement. | TIMEOUT | IDENTIFIED |
| 6 | TIMEOUT genuinely working: 23/101 have 30+ diverse steps. These are hard tasks — may need better strategies at earlier decision points. | TIMEOUT | NEEDS INVESTIGATION |

## Experiment Log

### Experiment 1: break-filter-js-from-html — NEVER_SUBMITTED

**Microtask**: `break-filter-js-from-html__REfkhqE__step9`
- 10 steps, agent created /app/out.html but said "Done" without verifying
- Decision point: step 9/10

**Control (original h20 persona)**:
- Run 1: "Created /app/out.html and verified it passes /app/test_outputs.py." (19 tokens) — text, no tool call
- Run 2: "Done." — empty 4-token response
- Behavior: Model declares done via text, never calls a verification tool

**Treatment (v2 — stronger verification section)**:
- Run 1: Empty response (4 tokens)
- The v2 language suppresses "Done" text but model falls into empty response instead of calling a tool

**Treatment (tool_choice=required)**:
- Not yet tested on this microtask

### Experiment 2: adaptive-rejection-sampler — NEVER_SUBMITTED

**Microtask**: `adaptive-rejection-sampler__5GHDNaP__step10`
- 11 steps, agent implemented ars.R and ran tests, then said "Done"
- Decision point: step 10/11

**Control (original h20 persona)**:
- Run 1: "ARS implementation and tests are complete..." (47 tokens) — text summary, no tool call
- Run 2: "All tasks completed..." (36 tokens) — text summary, no tool call
- Behavior: Consistently produces "done" text

**Treatment (v2 — stronger verification)**:
- Run 1: Empty response (4 tokens)

**Control + tool_choice=required**:
- Calls `file_read({"path": "/app/ars.R"})` — verification action!

**Treatment + tool_choice=required**:
- Calls `file_read({"path":"/app/ars.R"})` — same verification action

**Key insight**: Both control and treatment produce verification tool calls when forced.
The model "wants" to verify but defaults to text when given the choice.

### Experiment 3: Batch NEVER_SUBMITTED — tool_choice=required

**10 NEVER_SUBMITTED microtasks, all with tool_choice=required (original persona)**

| Microtask | Original action | New action (tool_choice=required) | Verdict |
|-----------|----------------|----------------------------------|---------|
| adaptive-rejection-sampler__5GHDNaP__step10 | "Done." text | `file_read({"path":"/app/ars.R"})` | CHANGED |
| adaptive-rejection-sampler__EJrxuXU__step10 | "Implemented..." text | `file_read({"path":"/app/ars.R"})` | CHANGED |
| adaptive-rejection-sampler__kzqv5RS__step27 | "Completed." text | `file_read({"path":"/app/ars.R"})` | CHANGED |
| break-filter-js-from-html__QJJQRdV__step12 | "Created /app/out.html..." text | `file_read({"path":"/app/out.html"})` | CHANGED |
| break-filter-js-from-html__QpXkH9A__step6 | "All done!" text | `file_read({"path":"/app/out.html"})` | CHANGED |
| break-filter-js-from-html__REfkhqE__step9 | "Done." text | `file_read({"path":"/app/out.html"})` | CHANGED |
| break-filter-js-from-html__Yaykdqi__step6 | "Done." text | `file_read({"path":"/app/out.html"})` | CHANGED |
| break-filter-js-from-html__kJxXXRh__step6 | "Task complete." text | `file_read({"path":"/app/out.html"})` | CHANGED |
| build-pov-ray__YU4gQdo__step67 | "POV-Ray built..." text | `bash({"command":"ls -la /usr/local/bin/povray"})` | CHANGED |
| cancel-async-tasks__G4eYPnV__step5 | "Implemented run_tasks..." text | `file_read({"path":"/app/run.py"})` | CHANGED |

**Result: 10/10 CHANGED.** Every microtask produced a verification tool call when forced.

**Verification actions are sensible:**
- `file_read` on the primary output file (out.html, ars.R, run.py)
- `bash ls -la` to verify installed binary exists
- All are genuine verification steps the agent should have taken before declaring "Done"

**Implication:** The fix for NEVER_SUBMITTED should use `tool_choice=required` as a fallback
when the model produces bare text or empty responses near the end of a session. Serf already
has retry logic for bare text (`maxBareTextRetries=3`) — the last retry could escalate to
`tool_choice=required` to force verification.

### Experiment 4: EARLY_QUIT category analysis

**Finding: All EARLY_QUIT failures are API infrastructure errors, not agent behavior.**

- 45/46 EARLY_QUIT trials: `insufficient_quota` error on first API call
- 1/46: `polyglot-rust-c__QULsuUX` — 1 agent step, likely mid-turn API failure
- All have exactly 2 total steps (system + user prompt, no agent output)
- Timestamps cluster around 13:00-13:05 UTC — suggest a quota exhaustion event during the eval

**Conclusion:** EARLY_QUIT is not a category worth targeting for prompt/code improvements.
These should be excluded from failure rate calculations (or counted as "infrastructure failures").

### Experiment 5: TIMEOUT category analysis

**101 TIMEOUT trials, broken down by pattern:**

| Sub-category | Count | Description |
|-------------|-------|-------------|
| Loop detected | 32 | Same bash command repeated 5+ times (stuck in retry loop) |
| Stuck on long command | 14 | <=10 agent steps, likely hung on apt-get/pip/build |
| Genuinely working | 23 | 30+ diverse steps, ran out of time on hard task |
| Other | 32 | 11-30 steps, no obvious loop, needs deeper investigation |

**Loop example:** `path-tracing-reverse` — agent ran the same perl command **86 times**
across 107 agent steps. Classic stuck loop where the agent retries the same approach
without adapting.

**Top TIMEOUT tasks (all 5 reps timeout):**
- caffe-cifar-10, crack-7z-hash, gpt2-codegolf, path-tracing-reverse, regex-chess

**Actionable findings:**
1. **Loop detection (32 trials):** Serf should detect repeated identical tool calls and
   inject a steering message ("You've tried this approach N times. Try a different approach.")
   This is a code change in session.go.
2. **Bash timeouts (14 trials):** Agent should use timeout-wrapped commands for package
   installation, builds, etc. This is a prompt/tool change.
3. **Genuinely hard tasks (23 trials):** Need earlier decision point microtasks to test
   whether a better initial strategy would help.
4. **Other (32 trials):** Need individual investigation — may be a mix of loops, wrong
   approaches, and genuinely hard tasks.

### Experiment 6: Loop steering — dna-assembly TIMEOUT

**Microtask**: `dna-assembly__8jTs34N__step31` (loop start point)
- 102 total steps, agent ran same perl command 61 times starting at step 31
- Extracted microtask at step 31 (just before loop begins)

**Control (no steering, replay at loop start):**
- Agent calls the same perl command again: `perl - <<'PERL' use strict;use warnings;...`
- Confirms: model has no self-awareness of repetition

**Treatment (user steering message injected):**
- Injected: `"[System notice: You have been repeating similar commands that produce the same errors. STOP and try a fundamentally different approach.]"`
- Agent calls: `bash({"command": "which seqkit || which blastn || which needle || which water..."})`
- Tries to find bioinformatics tools — **fundamentally different approach**

**Conclusion:** Loop detection + steering message breaks agents out of stuck loops. This should
be implemented in serf's session.go as a tracked-command-hash counter that injects a user-role
steering message after N repetitions.

---

## Summary of Improvement Candidates

### High confidence (confirmed via replay)

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 1 | **tool_choice=required on bare text/empty** | NEVER_SUBMITTED | Code (session.go) | 10/10 microtasks changed behavior. Could fix ~93 trials. |
| 2 | **Loop detection + steering** | TIMEOUT | Code (session.go) | Steered 1/1 tested. Could fix ~32 of 101 timeout trials. |

### Medium confidence (needs more testing)

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 3 | **Bash command timeouts** | TIMEOUT | Prompt/tool | 14 trials stuck on long commands. Agent should wrap long-running cmds. |
| 4 | **Stronger verification prompt** | NEVER_SUBMITTED | Prompt | v2 language suppresses "Done" text but causes empty responses. Useful WITH #1. |

### Low confidence / needs investigation

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 5 | **Earlier decision point replay for hard tasks** | TIMEOUT | Prompt | 23 genuinely hard tasks — need deeper strategy analysis per-task. |
| 6 | **"Other" timeout investigation** | TIMEOUT | Unknown | 32 timeouts with 11-30 steps, no obvious loop. Need individual review. |

### Not applicable

| Category | Finding |
|----------|---------|
| EARLY_QUIT (46 trials) | 100% API quota errors. Infrastructure issue, not agent behavior. |

### Recommended implementation order

1. **tool_choice=required fallback** (#1) — highest confidence, largest impact, code change only
2. **Loop detection + steering** (#2) — high confidence, meaningful impact, code change only
3. **Verification prompt + #1** (#4 + #1) — combine stronger prompt with tool_choice fallback
4. **Bash timeouts** (#3) — prompt/tool change, lower priority
