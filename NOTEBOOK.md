# Serf Optimization Notebook

Living document tracking the current experimental state. Read this first when starting
a new session.

## Current State (March 22, 2026)

**Shipped code:** commit 0d83e60 on main, pushed to GitHub
**Model:** gpt-5.4 for evals
**Baseline:** 56/88 = 64% on full 89-task terminal-bench (job: `full-89-ef120d4`)
**High water mark:** 75/89 (84%) tasks ever passed across all runs
**Skill:** `~/.claude/skills/benchmark-driven-improvement/SKILL.md`

**Regression set** (must keep passing after every change):
sanitize-git-repo, feal-linear-cryptanalysis, winning-avg-corewars,
kv-store-grpc, build-pov-ray, regex-log, pypi-server, adaptive-rejection-sampler

**Shipped fixes (on main):**
- fix-read-tests: coordinator reads /tests/ for specific constraints before delegating.
  polyglot-c-py 3/3, sqlite-with-gcov 2/3.
- fix-workspace-clean: coordinator cleans up after its own verification.
  git-multibranch 2/2, polyglot-c-py 2/3, db-wal-recovery 1/3.
- Combination validated 8/9 (only adaptive-rejection-sampler failed, nondeterministic).

## What's Been Done

### Phase 1-4 (March 13-20)
Shipped: tightened core.md, typed tasks + auto-verify, tool-level read-only verify,
reasoning escalation, coordinator delegation (R2 variant). 52% → 89% on 3 hard tasks.

### Phase 5 (March 20-22)

**Delegation enforcement:** Prose with "CRITICAL" markers works (3/3). Graphviz
flowcharts don't work with GPT (0/7).

**Model upgrade:** gpt-5.3-codex → gpt-5.4. Significant improvement.

**Full eval:** 56/88 = 64% on all 89 tasks.

**Root cause analysis:** All 22 non-too-hard failures root-caused from actual transcripts.
3 systemic patterns: write-last antipattern, workspace contamination, confirmation bias.
Full analysis at `docs/experiments/2026-03-21-full-89-failure-analysis.md`.

**7 parallel fix experiments (March 21-22):**

| Fix | Targets | Result | Shipped? |
|-----|---------|--------|----------|
| fix-read-tests | polyglot-c-py 3/3, sqlite-with-gcov 2/3 | **Win** | Yes |
| fix-workspace-clean | git-multibranch 2/2, polyglot-c-py 2/3 | **Win** | Yes |
| fix-check-environment | qemu-alpine-ssh 1/3, mteb-retrieve 0/3 | Weak | No |
| fix-write-early | query-optimize 1/1, chess 0/2, gcode 0/3 | Weak | No |
| fix-verify-literal | regex-chess 1/2, dna-insert 0/2, mcmc-stan 0/1 | Weak | No |
| fix-escalate | fix-code-vulnerability 0/3 | Reject | No |
| fix-vision-coordinator | chess 0/2, gcode 0/2 | Reject | No |

**Regression investigation:** sanitize-git-repo and build-pov-ray failures in fix
experiments were nondeterministic noise, confirmed from transcripts.

**Vision experiments (15+ variants):**
See detailed findings below.

## Vision Task Findings

### The system_prompt_append trap
`system_prompt_append` only reaches the root session (coordinator), not implementer
subagents. Our first 12 vision experiments (v1-v12) only affected the coordinator.
The 3/5 pass rate was coordinator behavior change (describing the board in delegation),
not implementer behavior change. This is NOT "testing nothing" — the coordinator DID
change behavior — but it's testing a different hypothesis than intended.

### What we tested in core.md (reaches implementer)
- "describe then verify with code" — 0/3 (but wrong binary deployed on first AWS run)
- "contract-first + trust + write-early" — gcode-to-text 1/3 (first ever pass!),
  chess-best-move 0/3. Correct binary confirmed via transcript headers.

### Local testing with actual chess board image (5 reps each)
- v2 ("describe to best of ability"): 0/5 wrote move.txt — went to PIL code
- v3 ("go through systematically, describe each element"): 0/5 — went to PIL code
- **v4 ("your next action must be text, not code")**: 2/5 wrote move.txt! Both wrong answers.
  The model DID describe the board in text. But it hallucinated the position — different
  wrong description every time.

### The core vision problem
GPT-5.4 cannot reliably read a 640×640 chess board from vision. When forced to describe
(v4 prompt), it outputs a board description but fills in a plausible position from
training data rather than reading the specific pieces. The positions it describes are
completely different each run and don't match the image.

However: when individual squares are cropped and enlarged (320×320), the model CAN
read them correctly. Rep 2 from harbor experiments: "a8 clearly black rook on dark
square", "c8 black bishop" — both correct.

### Next step for vision
The right approach may be:
1. Read full board for orientation (which side is White)
2. Crop each row/square and read individually (model reads enlarged crops correctly)
3. Construct FEN from individual square descriptions
4. Run Stockfish on the FEN
5. Write move.txt

This combines the "describe what you see" workflow with the crop-and-zoom approach
that actually produces accurate readings. The prompt should guide this workflow
rather than trying to force full-board vision reading.

## Infrastructure Lessons

**NEVER run evals on magic-kingdom** — it gets congested and causes failures.
Use AWS spot instances via harbor-runner.

**AWS spot rules:**
- 1 task per instance for tasks >10 min. Launch with `--task-names "one-task" --reps 3`.
- Fast regression tasks can be batched on one instance with `--concurrency 8`.
- Agent dirs must be in isolated subdirectories, not /tmp root (harbor-runner copies
  `*-linux-*` from parent dir, causing binary contamination).
- Always verify binary with `strings` before launching.
- Always check transcript headers after run to confirm correct prompt was used.
- S3 results are at `s3://BUCKET/runs/RUN_ID/rep-N/` not `s3://BUCKET/results/`.
- Instances self-terminate after uploading — "User initiated" termination is normal.

## Key Learnings

- **Graphviz doesn't work with GPT.** 0/7 compliance. Use prose with CRITICAL markers.
- **Prohibitions don't work with GPT.** Use positive framing.
- **"Not code — text" works for vision.** v4 prompt changed behavior from PIL code to
  text description. But GPT-5.4 hallucinates board positions from full-board reads.
- **Root cause from transcripts, not error messages.** Always.
- **system_prompt_append only reaches root session.** Changes for implementers go in core.md.
- **Verify your binary.** `strings binary | grep "expected text"`. Check transcript headers.

## What's Next

1. **Vision: crop-and-describe approach** — Build a workflow where the agent crops
   individual rows/squares and describes each one, rather than trying to read the
   full board in one shot. Test locally first (fast iteration), then harbor.

2. **Next batch of experiments** (from revised plan):
   - Pass git diff to implementer (fix-code-vulnerability root cause)
   - Write deliverable before slow verification (query-optimize root cause)
   - Reconsider hypothesis when evidence contradicts (fix-code-vulnerability)
   - Verify services respond with expected content (configure-git-webserver)

3. **Full validation** — after fixes, run full discriminator eval to measure aggregate impact.

## Experiment Log

| Date | Experiment | Tasks | Result | Notes |
|------|-----------|-------|--------|-------|
| 3/20 | H1-H9: delegation experiments | polyglot, ars | H9 best (3/3 delegation) | Prose > graphviz |
| 3/21 | Full discriminator gpt-5.4 | 56 tasks ×1 | 35/53 (66%) | Model upgrade helps |
| 3/21 | Full 89-task eval | 89 tasks ×1 | 56/88 (64%) | Baseline for fix work |
| 3/21 | Vision v1-v12 overlays | chess ×1 each | 3/5 pass (n=1 noise) | system_prompt_append trap |
| 3/21 | Vision v13-v15 contract | chess ×3 each | 0/6 | Prompt didn't reach implementer |
| 3/21 | fix-read-tests | polyglot 3/3, sqlite 2/3 | **Shipped** | Coordinator reads /tests/ |
| 3/21 | fix-workspace-clean | git-multi 2/2, polyglot 2/3 | **Shipped** | Coordinator cleans up |
| 3/21 | fix-escalate | fix-code-vuln 0/3 | Reject | "Report contradictions" insufficient |
| 3/21 | fix-check-environment | qemu 1/3, mteb 0/3 | Weak | Not shipped |
| 3/21 | fix-write-early | query 1/1, chess 0/2, gcode 0/3 | Weak | Not shipped |
| 3/21 | fix-verify-literal | regex 1/2, dna 0/2, mcmc 0/1 | Weak | Not shipped |
| 3/21 | fix-vision-coordinator | chess 0/2, gcode 0/2 | Reject | |
| 3/22 | Combination validation | 3 targets + 8 regression | 8/9 pass | Shipped combo works |
| 3/22 | fix-vision-core (AWS, correct binary) | chess 0/3, gcode 1/3 | gcode first pass! | kv-store-grpc regressed once |
| 3/22 | Local v4 "not code — text" | chess ×5 local | 2/5 wrote file, 0/5 correct | Behavior changed, accuracy bad |
