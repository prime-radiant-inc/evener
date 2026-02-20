# Terminal-Bench v2.0 Failure Analysis

**Date:** 2026-02-20
**Benchmark:** terminal-bench@2.0 via harbor framework
**Model:** gpt-5.2-codex (OpenAI Responses API)
**Agent:** serf (main branch, commits up to 39bfddc)

## Run Summary

| Run | Reasoning | Tasks | Passing | Timeouts | Errors | Score |
|-----|-----------|-------|---------|----------|--------|-------|
| r5c | xhigh | 89 | 33 | 19 | 1 | 37.5% |
| timeout-medium | medium | 19 (retries) | 4 | 2 | 0 | 21.1% |
| **Combined best** | | **89** | **37** | | | **41.6%** |

## Failure Categories (37 completed-but-wrong from r5c)

### 1. POSSIBLE GRADING ISSUE (6 tasks, 16%)

Agent reports successful completion with reasonable output, but scored 0.0.
May be output format mismatch or actual grading bugs.

| Task | Notes |
|------|-------|
| query-optimize | "Optimized SQLite query saved to /app/sol.sql; verified identical output" |
| dna-insert | "Designed a single Q5 SDM primer pair to insert the 39-bp segment" |
| build-pov-ray | "POV-Ray 2.2 built and installed... sanity render completed successfully" |
| filter-js-from-html | "Created sanitizer, tests passed" |
| overfull-hbox | "Successfully fixed LaTeX overfull hbox warnings" |
| video-processing | "Generated jump_analyzer.py and output.toml" |

**Action:** Investigate grading criteria for these 6 tasks. May recover 6 points.

### 2. ENVIRONMENT_ISSUE (7 tasks, 19%)

Failures due to API timeouts, safety filters, missing dependencies, or CLI bugs.
Not fixable in the agent.

| Task | Issue |
|------|-------|
| regex-chess | OpenAI API context deadline exceeded |
| circuit-fibsqrt | OpenAI API context deadline exceeded |
| crack-7z-hash | 7z extraction failed (password-protected file, environment tools missing) |
| sam-cell-seg | Environment lacks numpy at runtime |
| pytorch-model-recovery | CLI flag parsing error — agent never started |
| code-from-image | OpenAI safety filter blocked the prompt |
| qemu-startup | QEMU launch failed, missing utilities |

**Action:** API timeouts (2) may be transient. pytorch-model-recovery is a serf CLI bug.
sam-cell-seg and crack-7z-hash are task environment issues.

### 3. WASTED_ROUNDS (6 tasks, 16%)

Agent retries the same failing approach 100+ rounds without recognizing it's stuck.
No progress detection or approach-switching mechanism.

| Task | Rounds | Pattern |
|------|--------|---------|
| winning-avg-corewars | 75 | 9 repetitive subagent surveys, then crashed |
| train-fasttext | 152 | 70+ rounds iterating hyperparameters without strategy |
| compile-compcert | 152 | Retried different configure flags endlessly |
| financial-document-processor | 75 | 100+ diagnostic shell commands in loops |
| path-tracing | 152 | Iterative PPM analysis, never submitted answer |
| path-tracing-reverse | 151 | Same pattern as path-tracing |

**Root cause:** No mechanism to detect "I've tried this N times without progress."
**Action:** Add stuck detection / approach-switching guidance to prompt.

### 4. PREMATURE_QUIT (5 tasks, 14%)

Agent stops work without calling communicate(result), often after subagent survey fails.
Many hit the turn limit (~150 tool calls) and the log just ends.

| Task | Rounds | Pattern |
|------|--------|---------|
| caffe-cifar-10 | 154 | Log cut off during CIFAR-10 data download |
| build-cython-ext | 152 | pip install hung, no recovery attempted |
| make-doom-for-mips | 154 | Subagent timed out, never attempted build |
| make-mips-interpreter | 154 | 3 subagent timeouts, never attempted task |
| fix-git | 45 | Asked human for decision instead of completing |

**Root cause:** Agent unaware of remaining turn budget. When log hits ~150 lines,
it stops without final communicate(result).
**Action:** Inject turn budget awareness so agent can wrap up before cutoff.

### 5. CLOSE_BUT_BUGGY (5 tasks, 14%)

Agent claims success but implementation is broken. No verification step.

| Task | Issue |
|------|-------|
| install-windows-3.11 | Built all infrastructure but socket handshake failed |
| configure-git-webserver | Reported success but implementation doesn't pass validation |
| kv-store-grpc | Server built but protocol doesn't match test expectations |
| torch-tensor-parallelism | Created code, said "tests not run because python unavailable", claimed success |
| cancel-async-tasks | Created code but never executed or validated it |

**Root cause:** Agent skips verification before calling communicate(result).
**Action:** Strengthen verification-before-completion in prompts.

### 6. SURVEY_OVERHEAD (4 tasks, 11%)

Subagent wait loops burn significant rounds on coordination overhead.

| Task | Rounds | Survey Rounds | Pattern |
|------|--------|---------------|---------|
| mteb-leaderboard | 152 | 11+ | 11 failed spawn_agent attempts |
| db-wal-recovery | 82+ | 4 surveys | 4 subagent surveys, never found data |
| mailman | 75+ | 4+ | 10+ wait calls with 1000ms timeouts |
| fix-code-vulnerability | 75 | 3+ | 11+ sequential wait loops |

**Root cause:** Survey subagent spawning fails or times out, agent retries
excessively. Pre-30s-clamp, 1000ms wait timeouts caused rapid retry burn.
**Action:** The 30s wait clamp (39bfddc) helps. Consider making survey inline
instead of subagent-based.

### 7. WRONG_APPROACH (2 tasks, 5%)

Model fundamentally misunderstands the task requirements.

| Task | Issue |
|------|-------|
| mteb-retrieve | Misinterpreted "5th highest cosine similarity" vs actual retrieval ranking |
| sparql-university | SPARQL query created but incorrect for the ontology |

**Action:** Not directly fixable in agent. Better task comprehension is a model issue.

### 8. MODEL_CAPABILITY (2 tasks, 5%)

Correct approach but model can't produce correct results.

| Task | Issue |
|------|-------|
| rstan-to-pystan | Couldn't discover PyStan API structure despite 50+ attempts |
| mcmc-sampling-stan | Correct pipeline but wrong numerical results |

**Action:** Not fixable in agent. Pure model capability gap.

---

## Systemic Issues (Ranked by Impact)

### Issue 1: No Turn Budget Awareness (~15 tasks affected)

Many logs end at exactly line 150-154 without a communicate(result) call. The agent
hits the harbor framework's max_turns limit and is killed. It never knows it's about
to run out of turns, so it can't wrap up gracefully.

**Evidence:**
- caffe-cifar-10: 154 rounds, no communicate
- compile-compcert: 152 rounds, no communicate
- path-tracing: 152 rounds, no communicate
- train-fasttext: 152 rounds, no communicate
- build-cython-ext: 152 rounds, no communicate

**Proposed fix:** Inject remaining turn count into context. When turns_remaining < 5,
force the agent to submit whatever it has via communicate(result).

### Issue 2: Subagent Survey Failures Cascade (~9 tasks affected)

The mandatory survey subagent (spawn_agent for "Survey this project") frequently
fails via wait timeout or spawn error. When this happens, the main agent either:
- Gives up entirely (make-doom-for-mips, make-mips-interpreter)
- Wastes rounds retrying the survey (mteb-leaderboard: 11 retries)
- Gets confused and loses direction

**Evidence:**
- make-doom-for-mips: subagent timeout -> never attempted build
- make-mips-interpreter: 3 subagent timeouts -> never attempted task
- mteb-leaderboard: 11 spawn_agent failures, never started real work
- mailman: 10+ wait retries with 1000ms timeouts

**Proposed fix:** Make survey inline (glob + read) instead of subagent-based.
Or make survey optional / fail-fast with immediate fallback.

### Issue 3: No Verification Before Claiming Success (~5 tasks affected)

Agent calls communicate(result) claiming success without running validation tests.
The base prompt says to verify, but the model ignores it under pressure.

**Evidence:**
- torch-tensor-parallelism: "tests not run because python unavailable" -> claimed success
- configure-git-webserver: reported success, didn't validate
- kv-store-grpc: reported success, protocol broken

**Proposed fix:** Strengthen verification language in prompts. Research how
Claude/OpenAI system prompts handle this.

### Issue 4: No Stuck Detection (~6 tasks affected)

Agent retries the same failing approach indefinitely. No mechanism to recognize
"I've been doing the same thing for 20 rounds without progress" and switch tactics.

**Evidence:**
- compile-compcert: 152 rounds of configure flag variations
- path-tracing: 152 rounds of PPM analysis
- train-fasttext: 70+ rounds of hyperparameter tweaking

**Proposed fix:** Add "if you've tried the same approach 3 times without progress,
step back and try a different strategy" guidance.

---

## Score Attribution

If all systemic issues were fixed:

| Category | Tasks | Potentially Recoverable |
|----------|-------|------------------------|
| Grading issues | 6 | 6 (investigate) |
| Agent-fixable | 20 | ~10-15 (optimistic) |
| Environment | 7 | 2 (API timeouts are transient) |
| Model capability | 4 | 0 |
| **Total** | **37** | **~18-23** |

**Optimistic ceiling:** 37 (current) + 23 = 60/89 = 67%
**Realistic target:** 37 + 12 = 49/89 = 55%

---

## Medium Rerun Failures (13 completed-but-wrong)

Tasks that previously timed out with xhigh reasoning, rerun with medium, completed
but still scored 0.0.

| Task | Rounds | communicate? | Category | Issue |
|------|--------|-------------|----------|-------|
| gpt2-codegolf | 28 | Yes (cannot_complete) | PREMATURE_QUIT | Gave up after inspecting format |
| break-filter-js-from-html | 14 | Yes (success) | WRONG_APPROACH | Claimed bypass but grader rejected |
| largest-eigenval | 27 | Yes (completed) | CLOSE_BUT_BUGGY | Power iteration insufficiently accurate |
| adaptive-rejection-sampler | 23 | Yes (implemented) | CLOSE_BUT_BUGGY | Subtle sampler correctness bug |
| chess-best-move | 76 | No (turn limit) | WASTED_ROUNDS | 76 rounds of image processing, no move |
| qemu-alpine-ssh | 25 | Yes (unable) | ENVIRONMENT_ISSUE | Rosetta/ARM incompatibility |
| torch-pipeline-parallelism | 17 | Yes (completed) | WRONG_APPROACH | Wrote code, never tested |
| extract-moves-from-video | 2 | Yes (refused) | PREMATURE_QUIT | Refused on turn 1, didn't check filesystem |
| gcode-to-text | 11 | Yes (success) | WRONG_APPROACH | Guessed "Embossed text" instead of parsing |
| raman-fitting | 75 | Yes (fitted) | CLOSE_BUT_BUGGY | Extensive fitting, wrong parameters |
| polyglot-c-py | 11 | Yes (completed) | CLOSE_BUT_BUGGY | C worked, Python side broken |
| model-extraction-relu-logits | 19 | Yes (done) | WRONG_APPROACH | Simplistic extraction technique |
| sanitize-git-repo | 41 | Yes (verified) | CLOSE_BUT_BUGGY | Cleaned files but not git history |

Notable: extract-moves-from-video used only 2 rounds before refusing — didn't even
check if the video was already on disk. chess-best-move burned all 76 rounds on image
processing without ever computing a chess move.

---

## Deep Dive: Subagent Survey Failure Mechanisms

Four distinct failure mechanisms were identified across the 9 tasks with survey overhead:

### Mechanism A: Invalid agent_type (4 tasks)

Model passes `agent_type:"research"` but no builtin agents were registered.
Returns: `"unknown plugin agent type: research"`

| Task | Attempts before fallback to "" |
|------|-------------------------------|
| mteb-leaderboard | 5 |
| mailman | 2 |
| make-mips-interpreter | 1 |
| fix-code-vulnerability | 1 |

Cost: 1 LLM round per failed spawn (~8k input tokens each).
**Status:** Fixed by `explorer` builtin agent type.

### Mechanism B: 1-second wait timeouts (2 tasks)

Model passes `timeout_ms:1000` for tasks needing 30+ seconds.
Returns: `"wait timeout"` every second.

| Task | 1s waits before giving up |
|------|--------------------------|
| make-doom-for-mips | 10 (then 1 at 5s) |
| make-mips-interpreter | 10 + 10 (two cycles) |

Cost: 10+ LLM rounds per cycle.
**Status:** Fixed by `minWaitTimeoutMS` clamp.

### Mechanism C: Closed-channel instant-return polling (3 tasks)

After subagent completes, Go's closed `done` channel returns instantly on
subsequent `wait()` calls. The model calls `wait` 10+ times, all returning
`done` immediately with no useful data.

| Task | Polling wait calls |
|------|-------------------|
| mailman | 10 + 8 (two cycles) |
| db-wal-recovery | 4 |
| fix-code-vulnerability | 9 + 9 + 1 (three cycles) |

Cost: Extreme — 10 rapid-fire LLM rounds with no progress.
**Status:** PARTIALLY FIXED. The minWaitTimeoutMS clamp adds latency but
doesn't prevent the fundamental issue. waitAgent() should return an error
on re-wait of a completed agent, not silently return the empty result.

### Mechanism D: Empty subagent results (2 tasks)

Subagent runs and does work but never calls `communicate(result)`.
Returns empty output to parent. Parent respawns.

| Task | Spawn cycles before useful data |
|------|--------------------------------|
| winning-avg-corewars | 9 |
| db-wal-recovery | 4 |

Cost: 2 LLM rounds per spawn+wait cycle plus wasted subagent compute.
**Status:** Fixed by `defaultSubagentInstructions` with explicit
`communicate(result)` guidance (eb96dbd).

### Token Waste Summary

| Task | Wasted rounds | Primary mechanisms |
|------|--------------|-------------------|
| fix-code-vulnerability | ~32 | A + C |
| make-mips-interpreter | ~24 | A + B |
| winning-avg-corewars | ~18 | D |
| make-doom-for-mips | ~14 | B |
| mailman | ~12 | A + C |
| db-wal-recovery | ~10 | C + D |
| mteb-leaderboard | ~7 | A |

### Remaining Gap: Mechanism C

The closed-channel polling bug is only partially addressed. When a subagent
completes, its `done` channel closes. Go's `<-closedChannel` returns immediately,
bypassing the timeout timer. The model sees `{success: true, output: ""}` and
retries. The `minWaitTimeoutMS` clamp only delays timeout-path waits, not
done-path waits.

**Fix needed:** `waitAgent()` should track whether results were already consumed
and return an error like `"agent already completed; results previously returned"`
on subsequent wait calls.

---

## Research: Turn Budget Awareness in Other Agents

### Approaches Found

| Agent | Awareness? | Method | Wrap-up? |
|-------|-----------|--------|----------|
| **OpenCode** | Binary (last step only) | Fake assistant message disabling tools | Yes — forced summary |
| **Google BATS** | Continuous (4 regimes) | `<budget>` tags after tool results | Implicit via strategy |
| **Timely Machine** | Continuous (timer tool) | RL-trained time awareness | Trained behavior |
| **Mini-SWE-Agent** | Template vars available | Jinja2 `{n_model_calls}` | Hard exception |
| **SWE-bench** | None | N/A | Evaluates artifact state |
| **OpenHands** | None | Hard kill | No |
| **Claude Code** | Token/cost only | System reminders | No (headless hard kill) |
| **Cursor** | None | Human checkpoint at 25/200 | Human decides |

### Best Practices

**OpenCode's approach** (most practical for serf):
Injects a fake assistant message on the last step containing:
```
CRITICAL - MAXIMUM STEPS REACHED
Tools are disabled. Respond with text only.
Must provide: summary of work done, remaining tasks, recommendations.
```

**BATS approach** (most effective empirically):
Continuous budget tracking after each tool response:
```xml
<budget>
Steps Used: 45, Steps Remaining: 30
Make the best use of the available resources.
</budget>
```
Four regimes: HIGH (>=70% remaining), MEDIUM (30-70%), LOW (10-30%),
CRITICAL (<10%). Each regime has strategy guidance.
Result: 40% fewer tool calls, 31% lower cost with same accuracy.

### Recommended Design for Serf

Hybrid approach combining OpenCode's forced wrap-up with BATS-style awareness:

1. **Continuous awareness**: Inject turn count into tool results when <50% remaining
2. **Warning at 20%**: Add strategy guidance ("focus on completing, not exploring")
3. **Forced wrap-up at last 3 turns**: Reserve final turns for communicate(result)
4. **Hard stop**: If communicate not called, inject forced summary message

---

## Research: Verification Before Completion

### Current State in Serf

Two lines in `agent/prompts/base.md`:
```
- After making changes, run tests to verify correctness.
- Verify your work before claiming completion. Run the relevant tests or commands and
  confirm the output. Do not say "should work" or "looks correct" — show evidence.
```

Provider-specific prompts (anthropic/openai/gemini .md files) contain zero
verification language.

### What Other Agents Do

**Devin AI** (strongest):
- Mandatory self-reflection before declaring completion
- Explicit anti-pretending: "You don't pretend that broken code is working"
- Uses `think` command before completion to audit own output

**Gemini CLI**:
- "Validation is the only path to finality"
- Structured Plan -> Act -> Validate inner loop
- "A change is incomplete without verification logic"

**Codex CLI**:
- Full "Validating your work" section
- "iterate up to 3 times on formatting"
- Specific guidance for interactive vs non-interactive modes

**Anthropic research** (effective-harnesses-for-long-running-agents):
- Primary failure mode: agents "fail to recognize that the feature didn't work end-to-end"
- Recommends structured feature lists where agents mark features "passing" after verification

### Recommended Prompt Addition

Replace the two verification lines with a dedicated section:

```markdown
## Verification before completion

Before calling communicate(result), you MUST:

1. Build: If the project has a build step, run it.
2. Test: Run tests covering your changes. Start specific, then broaden.
3. Verify output: Confirm created/modified files exist with expected content.
4. Check the original task: Re-read the request. Did you address every part?
5. Reflect: Does this actually work, or am I hoping it works?

If tests fail, fix them. If you cannot fix after 3 attempts, report exactly
what failed — do not claim success.

Never say "this should work." Show evidence or state explicitly that you
could not verify and explain why.
```
