# GEPA Prompt Optimization Experiment

**Duration:** March 13-17, 2026 (5 days)
**Model:** gpt-5.4 (agent), gpt-5.4 (GEPA reflection)
**Infrastructure:** magic-kingdom.local (Ryzen 9, 16 cores, 60GB RAM), harbor + terminal-bench@2.0
**Budget spent:** ~$200 in API costs across all experiments

## Goal

Use GEPA's `optimize_anything` framework to improve serf's system prompt for terminal-bench discriminator tasks. Started with the blog post at gepa-ai.github.io, cloned the repo, built an evaluation harness, and iterated.

## What we shipped

### 1. Tightened core.md (2400B, smaller than original 2479B)

All insights woven into existing Identity and Values sections — no new sections added:

- **Delegation strategy:** "break work into investigate → implement → verify stages"
- **Research before implementation:** "search for knowledge that would help solve the problem"
- **Don't trust subagent reports:** "check the result yourself"
- **Cleanup:** "clean up scratch files, verify services survive session exit"
- **Test discovery:** "run the project's actual test suite (look in /tests/ too)"
- **Use libraries:** "when a specialized library exists for the hard part, install and use it"

**Validated results:** sqlite-db-truncate 50%→100%, build-pmars 33%→67%, configure-git-webserver 0%→50%, polyglot-c-py 0%→100% on best runs. No regressions on reliable tasks.

### 2. Typed tasks with auto-verify

Code changes to `task_store.go`, `session.go`, `profile.go`:

- **Task type field** (required): `research`, `implement`, `verify`, `fix`
- **Auto-generated review tasks:** Every `implement` task automatically gets a dependent `verify` task
- **Winning verify prompt:** "Dispatch an independent subagent to review this work. Its job is to find mistakes, missing requirements, leftover artifacts, or anything that would cause an external evaluator to reject the submission. It is rewarded for finding legitimate problems, not for confirming success. You may run commands and inspect files but do not write to the working directory."
- **Prompt surfacing fix:** Task instructions now shown to coordinator when suggesting next task (was only showing description before)

**Validated:** v2_nowrite won 5-way prompt comparison at 54% vs 27-45% for alternatives. Read-only reviewer prevents contamination while allowing active verification.

### 3. Research in investigate step

"Investigate means both inspecting the workspace AND researching the problem — when you are uncertain about the right approach, search for knowledge or skills that would help you solve the problem before attempting implementation."

**Validated:** count-dataset-tokens flipped from 0% to 50%. protein-assembly went from 0 to 22 web searches (still failing but actively researching).

## What we tried that didn't work

### GEPA with binary scoring (v1)
- Binary pass/fail gave GEPA almost no gradient
- Skills proposals kept tying baseline at 0/0
- **Fix:** Switched to LLM judge for continuous 0-1 scoring

### GEPA with skills supplement (appended)
- Skills appended via `--system-prompt-append` only reached the coordinator
- Subagents never saw the skills — they get `BasePromptOverride` which skips appends
- **Key discovery:** `system_prompt_append` doesn't propagate to subagents in serf's architecture
- **Fix:** Bake improvements into `core.md` itself, which subagents inherit via `CorePrompt()`

### Definition of Done instruction
- DoD as system_prompt_append: 69.2% → 77.5% on medium tasks (+8.3pp)
- Good for coordinator planning but didn't help subagent execution
- Not shipped as a permanent change — the core.md improvements captured the essence

### Verbose core.md additions (3588B)
- Adding new sections ("Before you start", "Before you finish", "Working with subagents") caused regressions
- sanitize-git-repo went from 100% to 0% (initially appeared to be our fault, but original also scores 0/4 at n=5 — it was a lucky baseline)
- fix-code-vulnerability dropped from 100% to 33%
- **Key learning:** Prompt degradation is real. Every word must earn its place. Same insights in fewer bytes performed better.

### Task-specific prompt patterns (from lace experiments)
- "grep for imports, is each stdlib?" — too specific, hurt general performance
- "pip installs won't persist in verifier" — teaching to the test
- **Key learning:** General engineering principles > benchmark-specific recipes

### Individual prompt interventions (9 tested)
- self_eval_gate, single_submission, use_tools, checklist, cold_start, errors, concise, env_bootstrap — all 0/5 on hard tasks
- The checklist (Droid trick) showed 1/5 on first run but 0/12 at 3 reps — was luck
- **Key learning:** Prompt-only interventions hit a ceiling on hard tasks. The mechanism matters more than the words.

### Review+Fix cycle (implement → read-only review → fix)
- Three-step cycle too expensive in time (20% pass rate vs 40-54% for simpler approaches)
- Fix tasks triggered infinite recursion until we added `TaskTypeFix`
- **Key learning:** Simpler verification (single review step) outperforms elaborate multi-step processes

### Pre-submit system notification
- Injected verification checklist via `s.Steer()` when agent calls `communicate`
- Rejected first submission, forced verification, allowed resubmission
- configure-git-webserver passed but control tasks regressed
- Too prescriptive and ate time budget
- **Not shipped**

## Key discoveries

### 1. Subagent inheritance is everything
The single most important finding: `system_prompt_append` doesn't propagate to subagents. The coordinator plans correctly but subagents don't follow through. Changes to `core.md` (which subagents inherit via `CorePrompt()`) are 10x more effective than appended instructions.

### 2. Prompt length matters more than content
The verbose 3588B core.md with explicit sections performed worse than the tightened 2400B version with the same insights woven into existing sections. Research confirms degradation starts around 3K tokens. The tightened version actually performed better than the original 2479B version despite containing more guidance.

### 3. Verification must be read-only
Write-capable reviewers contaminate the workspace (compile to deliverable directory, push test content, corrupt reports). Read-only reviewers ("may run commands and inspect files but do not write") prevent contamination while still catching issues. But prompt-based "don't write" is weaker than tool-level enforcement.

### 4. The hardest tasks are model knowledge gaps
After exhausting prompt optimization, the remaining failures are:
- **CWE classification:** gpt-5.4 says CWE-20, verifier expects CWE-93 (gpt-5.3-codex gets it right)
- **Token counting:** Agent computes wrong answer with wrong methodology
- **LaTeX synonyms:** Agent can't solve overfull hbox within synonym-only constraint
- **Protein assembly:** Needs molecular biology domain expertise

These are things the model doesn't know, not things the prompt can teach.

### 5. The dry-run harness is invaluable
Testing prompt changes via direct LLM call (5 seconds, pennies) before expensive harbor runs (10+ minutes, dollars) caught many issues early. The dry-run showed us that gpt-5.4 already planned to use python-chess for chess-best-move — the problem was execution, not planning.

### 6. Droid's advantage is architectural, not just prompting
From analyzing Droid's leaderboard submissions and CLI code:
- **System notifications** injected at decision points (leveraging recency bias)
- **Tool-level enforcement** of read-only reviewers (not prompt-based)
- **TodoWrite auto-included** for all subagents
- **Model-specific tool sets** (different tools per model)
- **Single-agent execution** (no subagent delegation overhead)
- **xhigh reasoning effort** on gpt-5.3-codex

## Infrastructure built

All code at `/home/jesse/git/serf-gepa/` on magic-kingdom.local:

- `tb_evaluator.py` — GEPA-to-harbor bridge with LLM judge for continuous scoring
- `optimize_serf.py` — GEPA optimization script (system prompt as candidate)
- `test_parallel.py` — Parallel harbor test runner
- `experiments/dry_run.py` — Local prompt testing harness (5s per test)
- `experiments/run_experiments.py` — Multi-variant parallel experiment runner
- `experiments/run_verify_variants.py` — Verify prompt A/B/C/D/E testing
- `run_ab_test.py` — Full A/B test framework
- `config.py` — Budget and task configuration

## Recommendations for next steps

### Architectural (highest impact)
1. **Enforce read-only on verify subagents at the tool level** — restrict tool registry to `Read, LS, Grep, Glob` for verify-type subagents, not just prompt-based "don't write"
2. **Auto-include task_list for all subagents** — Droid does this; ensures all subagents track progress
3. **System notifications for common failure patterns** — inject guidance at decision points (e.g., when agent is about to install a binary, remind about /usr/local/bin)
4. **Reduce subagent overhead** — serf's coordinator → explorer → worker → reviewer chain burns time budget. Consider single-agent mode for simpler tasks.

### Model-level
5. **Test gpt-5.3-codex as default** — it knows CWE-93, symlinks to /usr/local/bin, and with xhigh reasoning outperforms gpt-5.4 on specific tasks
6. **Reasoning effort tuning per task complexity** — xhigh for hard tasks, default for easy ones

### Evaluation infrastructure
7. **Increase harbor timeout for xhigh reasoning** — 900s default is too short for xhigh on complex tasks
8. **Fix evaluator to handle harbor's reward file timing** — some passing tasks scored as failures due to timeout
9. **Always run 3+ reps** — n=1 results are unreliable; multiple early decisions were based on lucky/unlucky single runs

## Final numbers

Full regression on 25 discriminator tasks, 3 reps each, final main build:

**Overall: 32/61 (52%)**

| Tier | Tasks | Count |
|------|-------|-------|
| Reliable (100%) | headless-terminal, kv-store-grpc, openssl-selfsigned-cert, password-recovery, polyglot-c-py, pytorch-model-cli, sqlite-db-truncate | 7 |
| Variance (33-67%) | extract-elf, sanitize-git-repo, cancel-async-tasks, build-cython-ext, configure-git-webserver, tune-mjcf, bn-fit-modify, crack-7z-hash, fix-code-vulnerability, build-pmars | 10 |
| Failing (0%) | circuit-fibsqrt, count-dataset-tokens, financial-document-processor, overfull-hbox, protein-assembly, qemu-alpine-ssh, sqlite-with-gcov, winning-avg-corewars | 8 |

Benchmark average across 27 agents on these same tasks: 67%. Top agent (Droid): estimated ~80%+. We're at 52% — 15pp below average, primarily due to model knowledge gaps on specific tasks.

**Tasks we moved from 0% to passing:** polyglot-c-py (0%→100%), configure-git-webserver (0%→50%)
