# Persona Experiments — Terminal-Bench 2.0 Discriminators

## Experiment Design

All runs use 56 discriminator tasks (10-75% failure rate) x 3 reps, gpt-5.2-codex, on magic-kingdom.

Critical rule: NEVER tell the agent it's running a benchmark or evaluation.

## Runs

### Baseline (benchmark persona)
- Job: `lace_gpt-5.2-codex_default_21b9e3443_20260303_1`
- Persona: `benchmark` (original lean autonomous prompt + harness injects autonomous prefix into user message)
- Result: **97/168 (57.7%)**
- Status: Complete

### V2 (regular persona + autonomous override)
- Job: `lace-v2-disc-2`
- Persona: `benchmark-v2` (full lace interactive persona sections + small autonomous override)
- Result: **25/65 (38.5%)** at time of kill
- Status: **Killed** — significant regression
- Root cause: (1) No autonomous user-message prefix (harness only injects for `persona === 'benchmark'`), (2) bloated 21K system prompt with conflicting conversational instructions ("ask for clarification", "TDD by default") drowning out the autonomous override. Agent was handing off to users, giving summaries, and over-exploring.

### V4 (benchmark + adversarial review)
- Job: `lace-v4-disc-2`
- Persona: `benchmark-v4` (V4's lean autonomous base + two competing adversarial reviewers before submission)
- Result: **27/41 (65.9%)** at last check, still running
- Status: Running
- Adversarial review was clearly causal on break-filter-js-from-html (reviewer caught Chromium blocking meta-refresh javascript: URLs, agent changed strategy). Likely helped on hf-model-inference. No help on ~4 other wins (pure implementer quality).

### V5 (V4 + "reread the spec")
- Job: `lace-v5-disc`
- Persona: `benchmark-v5` (V4 base + "Before You Finish" section: reread spec with fresh eyes, check every requirement/constraint/output)
- Result: pending
- Status: Running (launched 2026-03-04, concurrency 2)
- Hypothesis: The self-check before adversarial review catches spec compliance issues (like polyglot-c-py leaving compiled binary in working dir) that reviewers might also miss.

## Failure Analysis (from baseline)

### Consistent failures across all runs (structural)
- **chess-best-move** (0/9): Needs vision — agent can't parse PNG
- **regex-chess** (0/9): Encoding chess rules as pure regex is at capability boundary
- **polyglot-rust-c** (0/9): Rust nested block comments break standard polyglot trick
- **crack-7z-hash** (0/9): Missing p7zip/broken 7z2john.pl, pure-Python AES too slow
- **polyglot-c-py** (0/9): Solution works but compiled binary left in directory fails verifier check
- **merge-diff-arc-agi-task** (0/9): git safe.directory across Docker UIDs (fix is in install script but may not apply in all containers)

### Where adversarial review helped (V4)
- break-filter-js-from-html: Reviewer B caught browser security issue, agent pivoted strategy
- hf-model-inference: Reviewer flagged model path issue matching baseline failure mode

## Future Experiments

- **Single reviewer**: Replace two competing reviewers with one strongly-framed adversarial reviewer. Reviewer A often rubber-stamps; could get same signal at half the cost.
- **Environment robustness**: Investigate why `git config --global safe.directory '*'` in install-lace.sh.j2 isn't preventing merge-diff-arc-agi-task failures.
- **V3 (regular persona + autonomous + adversarial review)**: Never ran. If V5 shows improvement, could test whether the regular persona sections add value when combined with review.
- **Prompt caching impact**: Measure whether the adversarial review's token cost is offset by improved pass rate (cost per passed task).
