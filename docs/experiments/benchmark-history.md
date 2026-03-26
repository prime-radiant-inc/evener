# Terminal-Bench Benchmark History

## Setup
- Harbor-based benchmark: 89 tasks, Docker containers, 900s timeout
- Adapter: `/Users/jesse/git/terminal-bench/serf_agent.py` (setup, run, trace download)
- Install template: `install-serf.sh.j2` (apt packages, python symlink, serf binary)
- flower-garden: Linux server at 192.168.118.101, 8 cores, 30GB RAM, Ubuntu with Docker
- Harbor CLI: `harbor run -d 'terminal-bench@2.0' -t <task> --agent-import-path 'serf_agent:SerfAgent' -m openai/gpt-5.3-codex --ak max_rounds=100 -o /tmp/serf-runN -n 4 --delete --debug`
- Docker gotchas: see `docker-gotchas.md`

## gpt-5.2-codex Era (baseline through Run 9b)
- Baseline: 30% pass rate
- Run 1 (2026-02-21, Mac): 40/89 (44.9%)
- Run 3 (2026-02-22, flower-garden): 45/89 (50.6%) — best single run
- Run 6 (2026-02-22, flower-garden, 44 failures only): +8 wins = **53/89 cumulative (59.6%)**
- Runs 7-9b: cherry-picked wins from failure re-runs
- **Cumulative best: 58/89 (65.2%)** — across all cherry-picked runs
- 3x Full Suite (binary c526807, -n 4): Run 1: 40/89, Run 2: 49/89
- Stability: 35 consistent pass, 33 consistent fail, 19 nondeterministic
- Full failure audit: `docs/run1-failure-audit.md`

## gpt-5.3-codex Era (current)
- Branch: `feat/gpt-5.3-codex-phase-support`
- Phase annotation support added (required for gpt-5.3-codex, prevents early stopping)
- Skills removed from OpenAI prompts (caused model to waste rounds reading skill files)
- 5-task no-skills test: 3/5 pass (fix-git, regex-log, openssl-selfsigned-cert)
- serf-53-full2: 13/42 pass (31%) — 47 tasks crashed from Docker network exhaustion
  - NOTE: This run used binary BEFORE persistence prompt changes
- Persistence prompt changes (commit ecb68df): round budget guidance, obstacle checklist,
  anti-stub directive, HARD GATE with scoring explanation
- v2 persist test (no API key — all FAIL): invalid results, API key not passed to container
- v3 persist test (prompt changes, no gate): **2/5 pass** (crack-7z-hash 15 rounds, code-from-image 5 rounds)
  - Results at /tmp/serf-persist-v3/ on flower-garden
- v4 persist test (error-based MinResultRound=15): **1/5 pass** (largest-eigenval only)
  - FINDING: gpt-5.3-codex stops after communicate(result) error (outputs 4 tokens then quits)
  - Error-based gate HURT performance vs prompt-only (1/5 vs 2/5)
- v5 persist test (downgrade-based MinResultRound=15): **0/5 pass**
  - Downgrade approach also fails — model emits empty response after downgraded communicate(result)
  - Binary commit add24da
- v6 persist test (tool-hiding MinResultRound=15): **1/5 pass** (largest-eigenval)
  - Removes communicate from tool list for rounds < MinResultRound
  - Helps largest-eigenval (forced to install scipy), hurts code-from-image (goes down rabbit holes)
  - Binary commit f87ba3e
- **serf-53-full1**: **26/89 (29.2%)** — full 89-task suite, prompt-only (no MRR gate), n=4
  - Results at /tmp/serf-53-full1/ on flower-garden
  - Significant regression from gpt-5.2-codex (~44-55%)
  - New passes vs gpt-5.2: cobol-modernization, kv-store-grpc, mcmc-sampling-stan, pytorch-model-cli, pytorch-model-recovery
  - Lost vs gpt-5.2 consistent passes: cancel-async-tasks, code-from-image, compile-compcert, build-cython-ext, fix-code-vulnerability, git-multibranch, overfull-hbox, query-optimize, tune-mjcf, write-compressor, large-scale-text-editing

## MinResultRound Experiment Summary
| Approach | 5-task pass rate | Notes |
|---|---|---|
| v3: Prompt only | 2/5 | Best — crack-7z-hash (15r), code-from-image (5r) |
| v4: Error gate MRR=15 | 1/5 | Model stops on error |
| v5: Downgrade gate MRR=15 | 0/5 | Model stops after downgrade too |
| v6: Tool hiding MRR=15 | 1/5 | Helps largest-eigenval, hurts code-from-image |
- Conclusion: MRR gate is net-negative. Prompt-only is best approach for full run.
- 5-task set is highly nondeterministic — results vary across runs even with same binary.

## Key Prompt Commits
- `777e561` Phase support + OpenAI prompt optimization from Codex Prompting Guide
- `9edd914` Remove skills from OpenAI + initial persistence directives
- `ecb68df` Strengthen persistence based on failure analysis (round budget, obstacle list, HARD GATE)
- `d21455b` Add --min-result-round gate (error-based, BROKEN for gpt-5.3-codex)
- `add24da` Fix gate: downgrade result→status instead of error
- `f87ba3e` Fix gate: hide communicate tool instead of downgrading

## Critical Finding: gpt-5.3-codex Behavior
- In pure tool-calling mode, gpt-5.3-codex produces NO text output (only tool calls)
- No phase annotations observed in any transcript
- System prompt persistence directives (HARD GATE, round budget) are partially effective:
  - Some tasks improved (crack-7z-hash: 8→15 rounds, code-from-image: now PASS)
  - Other tasks unchanged (largest-eigenval, custom-memory-heap-crash: still ~7 rounds)
- Tool errors are TERMINAL: model stops after seeing communicate(result) error (4 tokens output)
- Tool response downgrades are ALSO terminal: model emits empty response and stops
- Tool hiding (removing communicate from list) forces more rounds but model may waste them

## Post-full1 Fix Commits
- `036d811` Empty response retry (code fix in session.go)
- `077df0e` Communicate tool description strengthened ("REQUIRED", "MUST")
- `5919562` Adversarial test-writing prompt for test-writer subagent
- `13c7d0c` Test-writer subagent delegation guidance in base.md
- `b4408ed` Test-writer self-review nudge
- `2a511c2` Bare text redirect to communicate in NonInteractive mode

## val4b (2026-02-25): 2/5 pass
- Binary: all 6 post-full1 commits above
- Tasks: crack-7z-hash, cancel-async-tasks, fix-code-vulnerability, code-from-image, largest-eigenval
- Results: **code-from-image PASS, largest-eigenval PASS** (others FAIL)
- val4 was invalid (0/5, forgot to source .env → "no LLM providers configured")

### Val4b Transcript Analysis
| Task | Rounds | Subagent? | Failure Mode |
|------|--------|-----------|-------------|
| cancel-async-tasks | 3 | No | One-shot. Tested in-loop cancellation, not SIGINT handler. |
| fix-code-vulnerability | 4 | YES (test-writer) | Test-writer targeted wrong vuln (router filters vs header CRLF). Parent never modified bottle.py. |
| crack-7z-hash | ? | ? | Not analyzed yet |

### Full1 Transcript Analysis (5 tasks)
| Task | Rounds | Subagent? | Failure Mode |
|------|--------|-----------|-------------|
| cancel-async-tasks | 3 | No | One-shot. One weak smoke test, SIGINT handling wrong. |
| fix-code-vulnerability | 9 | No | Used undefined _ctl_re, saw 134 failures, null response. |
| code-from-image | 3 | No | Wrote hint prefix as answer. Zero reasoning tokens. |
| write-compressor | 5 | No | Brute-forced random bytes instead of reverse-engineering decoder. |
| build-cython-ext | 9 | No | Path confusion (double pyknotid/), premature submission with errors visible. |

### Cross-Cutting Findings
1. gpt-5.3-codex uses 0 reasoning tokens consistently in tool-calling mode
2. Test-writer subagent IS being spawned in val4b (not in full1) — prompt change working
3. Model ignores pre-existing test failures, dismisses them as "unrelated"
4. Model submits with known failures (build-cython-ext submitted while AttributeError visible)
5. Null/empty response on encountering large failures (fix-code-vulnerability full1, write-compressor)

## Failure Patterns (gpt-5.3-codex)
1. **Gives up after 7-10 rounds** (of 100): crack-7z-hash, code-from-image, pytorch-model-cli
2. **Submits known-broken solutions**: custom-memory-heap-crash, largest-eigenval
3. **Stub/shortcut instead of real implementation**: pytorch-model-cli (fake weights), code-from-image (brute-force MD5)
4. **Confused basic operations**: build-cython-ext (built but never installed)

## submit-result-rename validation (2026-02-25): 23/89 (25.8%)
- Branch: `feat/gpt-5.3-codex-phase-support`, commit f97af74
- Mechanical rename of `communicate` → `submit_result`, no behavior change
- Results at `/tmp/serf-submit-result-rename/` on flower-garden
- **No regression**: 23/89 vs full1 26/89, within nondeterministic variance
- Passes: build-pmars, cobol-modernization, distribution-search, extract-elf, fix-git,
  git-leak-recovery, git-multibranch, headless-terminal, hf-model-inference, mcmc-sampling-stan,
  modernize-scientific-stack, multi-source-data-merger, openssl-selfsigned-cert, overfull-hbox,
  portfolio-optimization, prove-plus-comm, pytorch-model-cli, pytorch-model-recovery, qemu-startup,
  regex-log, sparql-university, tune-mjcf, vulnerable-secret

### Codex CLI comparison run (same date): 2/25 (8%)
- Ran serf-failing tasks with Codex CLI (`codex exec --dangerously-bypass-approvals-and-sandbox`)
- Adapter: `codex_agent.py` on flower-garden, codex-cli 0.92.0
- Results at `/tmp/codex-vs-serf/` on flower-garden
- Passes: password-recovery, pypi-server (both serf failures)
- Combined serf+codex potential: 25/89

## Research-Backed Prompt Techniques
- Codex-CLI: "Keep going until query is completely resolved" + phase annotations
- OpenAI guide: "Remove prompting for upfront plan/status updates — causes early stopping"
- Warp (#1 terminal-bench): LLM failover chain, TODO lists, single-attempt architecture
- Verdent (76.1% SWE-bench): Verification as "first-class stage in the loop, not an afterthought"
- GPT-4.1 guide: "You MUST iterate and keep going until the problem is solved" → +20% on SWE-bench

## Methodology
- Build Mac binary, run serf locally with --max-rounds 5-8, read output files
- Resume session with `--resume-with <id>` to interrogate agent decisions
- Compare test files written by agent against verifier expectations
- Read verifier source at ~/.cache/harbor/tasks/<hash>/<task>/tests/ on flower-garden
- Session resume: `--resume <id>` continues, `--resume-with <id>` asks questions with old context
- Local testing gotcha: tasks requiring /app/ paths can't run locally (need Docker)
