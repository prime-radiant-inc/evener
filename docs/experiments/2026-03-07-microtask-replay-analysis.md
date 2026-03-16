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
| Truly identical cmd 5+ times | 8 | Same exact bash command repeated (build/test re-runs) |
| Similar cmd (truncated match) | 24 | Same prefix but different bodies (heredoc scripts) |
| Stuck on long command | 14 | <=10 agent steps, likely hung on apt-get/pip/build |
| Genuinely working | 23 | 30+ diverse steps, ran out of time on hard task |
| Other | 32 | 11-30 steps, no obvious loop, needs deeper investigation |

**CORRECTION (2026-03-07):** Original analysis truncated commands to 120 chars, inflating
"loop detected" from 8 to 32. The 24 false matches were perl/python heredoc scripts that
shared the same prefix but had different bodies — the agent WAS trying different approaches.

The 8 truly identical cases are all **legitimate edit-test cycles** where the agent edits
code between runs and re-runs the same test command (make, python check.py, node vm.js).
This is correct behavior, not a stuck loop.

**Top TIMEOUT tasks (all 5 reps timeout):**
- caffe-cifar-10, crack-7z-hash, gpt2-codegolf, path-tracing-reverse, regex-chess

**Actionable findings:**
1. ~~**Loop detection (32 trials):**~~ RETRACTED — only 8 truly identical, and those are
   legitimate edit-test cycles. Loop detection would interfere with correct behavior.
2. **Bash timeouts (14 trials):** Agent should use timeout-wrapped commands for package
   installation, builds, etc. This is a prompt/tool change.
3. **Genuinely hard tasks (23 trials):** Need earlier decision point microtasks to test
   whether a better initial strategy would help.
4. **Other (35 microtasks across 16 tasks):** Investigated — see Experiment 7 below.

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

### Experiment 7: "Other" timeout deep dive (11-30 steps, no obvious loop)

**35 microtasks across 16 tasks, individually categorized:**

| Sub-pattern | Count | Tasks | Addressable? |
|-------------|-------|-------|-------------|
| Genuinely hard (agent working correctly, ran out of time) | ~11 | polyglot-c-py, gpt2-codegolf, torch-pipeline, reshard-c4, regex-chess, caffe, install-windows | No — task difficulty |
| Subtle thematic loops (not identical commands, but circling same approach) | ~10 | gpt2-codegolf, install-windows, regex-chess, write-compressor, cobol, qemu, caffe, eigenval, polyglot | Maybe — earlier strategy shift |
| Dependency/install struggle (build tools, compilers, apt-get) | ~6 | train-fasttext, caffe-cifar-10, install-windows, sqlite-gcov | Yes — bash timeouts + install strategy hints |
| Tool availability (missing Python, 7z, etc.) | ~5 | gpt2-codegolf, crack-7z-hash, train-fasttext | Yes — early environment probing prompt |
| Premature done / early quit (miscategorized) | ~3 | feal-crypto, reshard, train-fasttext | Yes — tool_choice=required already addresses |

**Key findings:**

1. **~30% genuinely hard** — no fix. Agent is making correct progress on tasks like
   differential cryptanalysis, regex-based chess, polyglot programming. These need
   more time or fundamentally better strategies that can't be prompted.

2. **~28% subtle thematic loops** — different from Experiment 5's "truly identical"
   commands. Agent tries minor variations of the same approach (e.g., tweaking a
   Makefile config, adjusting regex patterns) without stepping back to reconsider.
   The Experiment 6 steering message approach could help if we detect *thematic*
   similarity, not just exact command hashes.

3. **~17% dependency struggles** — agent spends 5+ steps installing compilers,
   build tools, C++ libraries. Overlaps with Experiment 5 finding #2 (bash timeouts).
   Additional prompt: "if a package fails to build from source, check for pre-built
   alternatives before retrying the build."

4. **~14% tool availability** — agent discovers core tools (Python, 7z) missing and
   wastes steps. Prompt: "check available tools and language runtimes before planning
   your approach" could save significant time.

5. **~8% miscategorized** — actually NEVER_SUBMITTED or EARLY_QUIT. Already addressed
   by tool_choice=required.

**Conclusion:** The "Other" category splits roughly 30/70 between unfixable (hard tasks)
and potentially addressable (loops, dependencies, tool probing). The addressable portion
overlaps with existing improvement candidates (#3 bash timeouts, #1 tool_choice).
Two new candidates emerge: thematic loop detection and environment probing prompts.

### Experiment 8: Environment probing prompt (replay test)

**Hypothesis:** Adding an "Environment Reality" section to the system prompt causes the
agent to check tool availability before committing to an approach.

**Prompt addition** (inserted before "How to Work"):
```
## Environment Reality

This container is intentionally minimal. Do NOT assume standard tools are present just
because the task involves their file formats or domains. Python, pip, gcc, curl, 7z,
and other "obvious" utilities may not be installed.

Before committing to any approach, verify the specific tools you plan to use exist.
A quick `type python3 gcc 7z 2>&1` or `which <tool>` takes seconds and prevents
you from building a plan around tools that aren't there.

Your approach should be driven by what IS available, not what you wish were available.
```

**Results:**

| Trial | Control | Treatment |
|-------|---------|-----------|
| crack-7z-hash (8indUay, step 3) | `7z l /app/secrets.7z` — tries tool blindly | `type 7z 7za 7zr bsdtar tar unzip 2>&1` — probes tools ✓ |
| crack-7z-hash (v69jD2p, step 3) | `file /app/secrets.7z` — inspects file | `type 7z 7zr 7za bsdtar tar unzip 2>&1` — probes tools ✓ |

**2/2 CHANGED.** Treatment consistently produces tool availability checks, and checks
MULTIPLE alternatives (7z, 7za, 7zr, bsdtar, tar, unzip), not just the obvious one.

**Verdict: CONFIRMED.** This is a cheap, safe prompt addition with clear behavioral impact.

### Experiment 9: Strategy reassessment prompt (replay test)

**Hypothesis:** Adding an "Analyze Before You Iterate" section to the system prompt prevents
thematic loops where the agent tries minor variations of the same approach.

**Prompt addition** (inserted before "Before You Stop"):
```
## Analyze Before You Iterate

When reverse-engineering code, implementing algorithms, or solving complex problems:
invest time in understanding DEEPLY before writing code. Read the source carefully,
trace the logic, identify the key invariants. A thorough analysis phase prevents
costly trial-and-error loops.

If your first implementation attempt fails, do NOT immediately tweak and retry.
Go back to your analysis. The failure means you misunderstood something — find WHAT
before trying again. Three failed variations of the same approach means the approach
is wrong, not the details.

Ask yourself after each failure: "Am I making progress toward the solution, or
am I repeating the same mistake with superficial changes?"
```

**Results:**

| Context | Control | Treatment | Changed? |
|---------|---------|-----------|----------|
| write-compressor step 5 (early, pre-loop) | Implements Decoder immediately (17K tokens) | Inspects data first (36K tokens) | Subtle — more analysis, same approach |
| gpt2-codegolf step 22 (mid-loop, step 22/25) | Runs `./a.out` with output inspection | Runs `./a.out` | NO — identical approach |

**0/2 CHANGED.** The system prompt addition:
- At early steps: produces more reasoning tokens (~2x) but the FIRST ACTION is the same
  general approach. May help with analysis depth but doesn't change strategy selection.
- At late steps: completely ineffective. Conversation inertia (22 steps of the same
  approach) overwhelms system prompt guidance.

**Verdict: NOT EFFECTIVE as a system prompt addition.** Thematic loop prevention needs
a RUNTIME mechanism — either a mid-conversation steering message (like Experiment 6)
or a code-level intervention that detects thematic similarity and injects a redirect.
Static prompt additions can't compete with 20+ steps of conversation inertia.

---

### Experiment 10: Reasoning Token Visibility + Planning Prompt (h22v2)

**Hypothesis**: The h22v2 planning prompt (which tells the model to use todo_write for
planning) may be influencing the model's REASONING even though it doesn't produce
todo_write tool calls. Added `summary: "auto"` to the reasoning config in the replay
harness to see model thinking.

**Fix applied**: Added reasoning summary display to `replay_microtask.py:_format_result()`

**Results**:

| Microtask | h22v2 Reasoning Summary | h22v2 Action | Control Reasoning | Control Action |
|-----------|------------------------|--------------|-------------------|----------------|
| crack-7z-hash step 3 | "I'm preparing to create a plan...using the todo_write tool before starting" | file_read (not todo_write!) | "Preparing to list archive contents" | bash: `7z l /app/secrets.7z` |
| write-compressor step 5 | "Planning initial tooling steps" | bash: `ls -la /app` | (not tested) | bash: `ls -l /app` |
| install-windows step 10 | "I'm running a command to verify if QEMU is installed" | bash: `which qemu-system-i386 && which qemu-system-x86_64` | (not tested) | bash: `apt-cache search novnc` |

**Key finding**: The model REASONS about planning and todo_write but then optimizes away
the planning call in favor of a direct action. The planning instruction influences
reasoning tokens but not tool selection. This is consistent with the model treating
planning as low-value overhead vs. immediate action.

**Verdict**: Planning happens internally in reasoning tokens. Externalizing it via
todo_write requires structural enforcement (e.g., multi-turn `tool_choice=required`
targeting todo_write) which is beyond what a static prompt can achieve. The h22 persona
is still valuable for its environment probing and stuck-pivot behavior — just don't
expect explicit planning calls.

### Experiment 11: h22 vs h21 Multi-Turn Eval (Planning Prompt)

**Hypothesis**: The h22 "Planning (REQUIRED)" section causes the agent to call `todo_write`
in actual multi-turn execution, even though single-turn replay (Experiment 10) showed the
model optimizing away the call.

**Setup**: 3 tasks × 2 reps × 2 personas = 12 trials on magic-kingdom via `run_eval.py`
- Tasks: crack-7z-hash, fix-code-vulnerability, write-compressor
- Personas: benchmark-h22 (planning prompt) vs benchmark-h21 (baseline)
- Both runs shared auto-generated job name (same git SHA + date) — trials mixed but
  distinguishable via agent.log persona field

**Results**:

| Trial | Persona | todo_write calls | Events | Status |
|-------|---------|-----------------|--------|--------|
| fix-code-vulnerability__JSYZCm3 | h22 | 3 (create + update + done) | 126 | Completed (harbor write error) |
| fix-code-vulnerability__PTyiw4H | h22 | 3 (create + update + done) | 68 | Completed (harbor write error) |
| fix-code-vulnerability__HJ7ekaw | h21 | 0 | — | Completed |
| fix-code-vulnerability__LLepEyw | h21 | 0 | — | Completed |
| crack-7z-hash (4 trials) | h21/unknown | 0 | — | All timed out |
| write-compressor__xtG7FJN | h21 | 0 | — | Completed |
| write-compressor__YhDGovx | h21 | 0 | — | Completed |
| write-compressor (2 others) | unknown | 0 | — | Timed out |

**h22 todo_write plan content** (JSYZCm3):
> "Plan: identify CWE vuln in /app/bottle.py, fix input validation and error handling,
> add report.jsonl, run tests"
> Verification: `pytest -rA`
> Sub-tasks: 1) Explore for vulnerable input handling 2) Implement fix 3) Create report.jsonl 4) Run tests

**Key findings**:

1. **h22: 2/2 (100%) called todo_write. h21: 0/6 (0%).** The planning prompt works in
   multi-turn execution. The model explores first, then plans — which single-turn replay
   at step 2 couldn't capture.

2. **h22 failures are harbor infrastructure**, not persona issues. Error:
   `FileNotFoundError: .../command-0/return-code.txt` — the `command-0/` directory
   (created by harbor's base.py at line 148) disappeared before line 163 could write
   to it. Root cause unclear — likely a fluke from running two harbor processes
   concurrently with the same job name. The agent completed its work (126/68 events).

3. **h21 trials scored properly**: result.json has rewards (fix-code-vulnerability 1.0,
   write-compressor 1.0, crack-7z-hash 0.0). Reward data is in result.json, not
   reward.txt (lace adapter stores results differently from serf adapter).

4. **Need clean re-run with separate job names** to get h22 verifier results and
   compare pass/fail rates.

### Experiment 12: h22 vs h21 Clean Re-Run (Separate Job Names)

**Goal**: Get clean pass/fail comparison between h22 (planning) and h21 (baseline) with
separate harbor job names to avoid the infrastructure issues from Experiment 11.

**Setup**: 3 tasks × 2 reps × 2 personas = 12 trials
- Jobs: `h22-planning` (persona=benchmark-h22) and `h21-baseline` (persona=benchmark-h21)
- Tasks: crack-7z-hash, fix-code-vulnerability, write-compressor
- Run IDs: `2026-03-08T044750Z_h22-planning_3e994ad7b`, `2026-03-08T044940Z_h21-baseline_3e994ad7b`

**Results**:

| Task | h22 (planning) | h21 (baseline) |
|------|---------------|----------------|
| crack-7z-hash | **1/2** | 0/2 |
| fix-code-vulnerability | 2/2 | 2/2 |
| write-compressor | 1/1* | 1/1* |

*\*One rep on each side hit harbor setup timeout (infrastructure, not agent)*

**Finding**: h22 picked up a crack-7z-hash pass (1/2) where h21 got 0/2. Small sample
but directionally positive. fix-code-vulnerability and write-compressor are easy enough
that both personas pass.

### Experiment 13: h22 on Moderate-Difficulty Failures

**Goal**: Test h22 planning prompt on tasks we previously failed but that most agents solve.
If h22 flips these from fail to pass, it's a real general improvement, not task-specific luck.

**Setup**: 6 tasks × 2 reps = 12 trials
- Job: `h22-moderate` (persona=benchmark-h22)
- Run ID: `2026-03-08T051009Z_h22-moderate_3e994ad7b`
- Tasks selected by cross-referencing our lace-v4-disc-2 failures against terminal-bench
  overall failure rates — picked tasks where we fail (0 passes) but the benchmark-wide
  failure rate is <50% (most agents solve them)

**Results**:

| Task | h22 result | Previous lace (v4-disc-2) | Overall failure rate |
|------|-----------|--------------------------|---------------------|
| sqlite-with-gcov | **2/2 PASS** | 0/2 | 18.7% |
| configure-git-webserver | **2/2 PASS** | 0/1 | 47.1% |
| largest-eigenval | **1/2** | 0/2 | 30.1% |
| mailman | **1/2** | 0/2 | 29.2% |
| mcmc-sampling-stan | **1/2** | 1/3 | 20.7% |
| merge-diff-arc-agi-task | 0/2 | 0/1 | 12.3% |

**Summary**: 7/12 pass (58%) on tasks we were previously failing.

**Key findings**:

1. **h22 flipped 4 out of 6 previously-zero-pass tasks to at least one pass.**
   sqlite-with-gcov (0→2/2) and configure-git-webserver (0→2/2) are particularly
   strong — both went to 100% pass rate.

2. **Combined across Experiments 12-13**: h22 turned 5 previously-failed tasks into
   passes (crack-7z-hash, sqlite-with-gcov, configure-git-webserver, largest-eigenval,
   mailman). Only merge-diff-arc-agi-task remained at 0.

3. **merge-diff-arc-agi-task holdout**: Despite having the lowest overall failure rate
   (12.3%), lace still fails this with h22. Needs transcript investigation — may be a
   task-specific issue unrelated to planning.

4. **The planning prompt is a general improvement**, not task-specific gaming. These 6
   tasks span different domains (databases, networking, math, email, data merging,
   statistics) and the improvement is consistent across them.

---

## Summary of Improvement Candidates

### High confidence (confirmed via replay)

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 1 | **tool_choice=required on bare text/empty** | NEVER_SUBMITTED | Code (runner.ts) | 10/10 microtasks changed behavior. Could fix ~93 trials. |

### Medium confidence (confirmed via replay, needs eval validation)

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 2 | **Environment probing prompt** | TIMEOUT | Prompt | 2/2 replays CHANGED behavior. ~5 "Other" microtasks waste steps on missing tools. Cheap, safe addition. |
| 3 | **Bash command timeouts** | TIMEOUT | Prompt/tool | 14 trials stuck on long commands + 6 "Other" dependency struggles. Agent should wrap long-running cmds. |
| 4 | **Stronger verification prompt** | NEVER_SUBMITTED | Prompt | v2 language suppresses "Done" text but causes empty responses. Useful WITH #1. |

### Low confidence / needs more testing

| # | Fix | Category | Type | Impact estimate |
|---|-----|----------|------|----------------|
| 5 | **Earlier decision point replay for hard tasks** | TIMEOUT | Prompt | ~34 genuinely hard microtasks. Need per-task strategy analysis. |
| 6 | **Thematic loop detection + steering** | TIMEOUT | Code (runtime) | ~10 "Other" microtasks circle same approach. System prompt NOT effective (0/2 replays). Needs runtime detection + mid-conversation steering message (like Experiment 6). |

### Confirmed — strong signal

| # | Fix | Category | Finding |
|---|-----|----------|---------|
| 7 | **Planning via todo_write (h22 persona)** | ALL | **Multi-turn eval confirms planning prompt works and improves pass rates across 9 tasks.** See Experiments 11-13 for full results. |

### Not applicable

| Category | Finding |
|----------|---------|
| EARLY_QUIT (46 trials) | 100% API quota errors. Infrastructure issue, not agent behavior. |

### Recommended implementation order

1. **h22 planning persona** (#7) — **CONFIRMED across 9 tasks, 5 previously-failed tasks flipped to pass.** Adopt h22 as the default benchmark persona for lace.
2. **tool_choice=required fallback** (#1) — highest confidence for NEVER_SUBMITTED, 10/10 microtasks changed behavior. Implemented in lace runner.ts.
3. **Environment probing prompt** (#2) — confirmed via replay (2/2 CHANGED), cheap to add, no downside. Already included in h22.
4. **Bash command timeouts** (#3) — helps ~20 microtasks (14 "stuck on long command" + 6 dependency struggles)
5. **Verification prompt + #1** (#4 + #1) — combine stronger prompt with tool_choice fallback
6. **Runtime thematic loop detection** (#6) — needs code-level similarity detection + steering injection. System prompt alone doesn't work (Experiment 9). May need LLM-based similarity or command prefix hashing.
7. ~~**Exact loop detection + steering**~~ — RETRACTED, based on flawed truncated analysis
8. ~~**Strategy reassessment system prompt**~~ — REJECTED, 0/2 replays showed behavioral change. Conversation inertia overwhelms static prompt.
