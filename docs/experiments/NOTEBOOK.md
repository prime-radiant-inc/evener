# Serf Optimization Notebook

Living document tracking the current experimental state. Read this first when starting
a new session.

**Methodology:** The `benchmark-driven-improvement` skill defines the experimental
process — hill-climbing protocol, commit-on-branch-before-deploy, root-cause-from-transcripts.
Invoke it before starting work.

## Current State (March 27, 2026)

**Model:** gpt-5.4-mini for current eval iteration
**Scoreboard:** `./tools/scoreboard.py` — canonical 89-task matrix
**Score:** 83/89 tested, mean 0.608

### Shipped prompt fixes (on main)
- **deleg-b** (coordinator.md): "quality gate, not the worker" — fixes chess-best-move
- **state-b** (implementer.md): post-test deliverable mutation check — fixes git-multibranch
- **v17 harmonize gate** + **v18 no-tests case**: fixes log-summary-date-ranges
- impl-test-a NOT shipped (interrogation showed 3/3 was stochastic, not causal)
- **v23-B reviewer-evidence** (reviewer.md + communicate.agent-reviewer.md): remove "intuit" phrasing, spec authority for implementer
- **ops-task removal** (agent/skills/): deleted ops-task embedded skill — was priming coordinators to implement directly
- **coordinator inventory fix** (coordinator.md): "Inventory means listing, not reading" + "Small tasks are not exceptions"
- **implementer stuck guidance** (implementer.md): folded useful ops-task content into "When you get stuck" section
- **v26 task_list neutral** (task_reminders.go): neutral phrasing — "No in_progress task. Next open task: #N" instead of imperative "Mark it in_progress to begin"

### Results system
- `tools/collect_results.py` — download from S3, normalize, update metadata
- `tools/scoreboard.py` — view scoreboard and per-task history
- `docs/experiments/scoreboard.json` — machine-readable matrix
- `docs/experiments/tasks/*.json` — per-task scorecards
- `docs/experiments/runs/*.json` — per-run metadata
- Local cache: `~/.serf-evals/tasks/{task}/{run}/{rep}/`
- Scoring: mean of reps from last run; same-date tiebreak = highest score

### Pending runs

- **full-baseline-2026-03-27** — all 89 tasks × 3 reps, gpt-5.4-mini, main @ 5554bed.
  Clean baseline with all shipped fixes. 3 × c6i.2xlarge, concurrency 8. Running.

### Local coordinator delegation harness

`tools/coord-repro/` — fast local test for coordinator delegation vs bypass.
Builds serf, sets up chess board workspace, runs with `--max-rounds 8`, checks
transcript for `spawn_agent` (DELEGATE) vs `write_file`/shell (BYPASS). ~2 min/run.
See `docs/experiments/infrastructure.md` for usage.

25 coordinator prompt variants in `tools/coord-repro/generate-variants.py`.
Partial local results (13/26 variants tested, 2 reps each) showed baseline
delegating 2/2 locally — consistent with AWS baseline-retest 3/3. The local
harness has a lower bypass rate than AWS Docker, limiting its discriminative
power. Best used for smoke-testing variants before AWS runs.

### v21-easy5 baseline (Mar 27)

**Run:** `v21-easy5` — 5 easiest tasks × 3 reps, gpt-5.4-mini, commit c77f85e
**Result:** 5/5 tasks, 15/15 reps — perfect.

| Task | Score | Leaderboard failure rate |
|------|-------|------------------------|
| git-leak-recovery | 3/3 | 1.6% |
| cobol-modernization | 3/3 | 3.2% |
| fix-git | 3/3 | 4.1% |
| constraints-scheduling | 3/3 | 4.1% |
| nginx-request-logging | 3/3 | 4.1% |

### v25 reviewer experiments (Mar 27) — all failed

**3 variants testing reviewer/coordinator interaction when reviewing computational
output. All run chess-best-move × 3 reps on gpt-5.4-mini.**

**Root cause:** In v24-E rep 3, the implementer correctly found both mate-in-one
moves (e2e4, g2g4) via python-chess. The reviewer then read chess_board.png with
`purpose: "determine the position and best move"` (singular). Serf's vision
side-channel injected a wrong description saying only Qe4# was checkmate. The
reviewer trusted the vision injection and rejected the correct answer.

| Variant | Change | SHA | Result |
|---------|--------|-----|--------|
| **v25-A** | Coordinator passes implementer verification methodology to reviewer | 7498c4a | **0/3** |
| **v25-B** | Reviewer: do not re-derive from primary sources when lacking equivalent tools | bd01002 | **0/3** |
| **v25-C** | Both A + B | fe9286f | **1/3** |

**Baseline:** baseline-retest chess-best-move = **3/3** (main @ 5554bed, same day)

**Conclusion:** All v25 reviewer changes made things worse. The baseline without
reviewer modifications scores 3/3 on chess-best-move. Do not ship any v25 changes.

### v26 task_list neutral (Mar 27) — shipped

**Change:** task_reminders.go — neutral phrasing instead of imperative.
"No in_progress task. Next open task: #N — ..." instead of "Mark it in_progress to begin."
Merged to main as commit 5554bed.

Tested on custom-memory-heap-crash 3/3 (was 2/3 when task_list used imperatives).
The imperative phrasing + zero reasoning tokens primed coordinators to act directly.

### v27 implementer/coordinator experiments (Mar 27) — coordinator bypass dominant

**4 variants testing different approaches to chess-best-move failures.**

| Variant | Change | Result | Finding |
|---------|--------|--------|---------|
| v27-A coordinator-restrict | Tighter coordinator delegation rules | 0/3 | Coordinator still bypassed |
| v27-B implementer-compute | Implementer told to "enumerate programmatically" | 2/3 | **Teaches to the test** — mentions image analysis |
| v27-B2 implementer-derive | Generic "derive from tools not context" | 0/3 | 2/3 coordinator bypass, 1/3 implementer confirmation bias |
| v27-C reviewer-no-reanalyze | Reviewer told not to re-derive | 0/3 | Coordinator bypass |

**Interrogation findings (v27-B2):**
- Rep 1: Coordinator bypass — vision steering gave "Nxd5+", coordinator wrote directly
- Rep 2: Implementer delegated but only validated vision's candidate instead of searching
- Rep 3: Coordinator bypass — vision steering gave "Rh4+", coordinator wrote directly

**Key insight:** 2/3 failures were coordinator bypass (implementer never spawned).
The implementer prompt is irrelevant when the coordinator never delegates.

**However:** baseline-retest on the same day went 3/3 with unchanged code. The
coordinator bypass may be intermittent/stochastic rather than systematic. The full
89-task baseline (running) will establish whether the current main code is solid.

### v24-E-no-ops-task results (Mar 27)

**Run:** `v24-E-no-ops-task` — 3 non-delegation tasks × 3 reps, gpt-5.4-mini, commit 70095a4
**Build:** B merge + ops-task removal + coordinator inventory fix
**Result:** 6/9 = 66.7%

| Task | Rep 1 | Rep 2 | Rep 3 | Score |
|------|-------|-------|-------|-------|
| chess-best-move | 1 | 0 | 0 | 1/3 |
| crack-7z-hash | 1 | 1 | 1 | 3/3 |
| custom-memory-heap-crash | 0 | 1 | 1 | 2/3 |

**Non-delegation fix confirmed:** All 9 trials delegated to implementers (vs 0/3
in baseline). Remaining failures are implementation-quality issues, not delegation.

**Interrogation findings:**
- **crack-7z-hash 3/3:** Delegation works. Task solved.
- **chess-best-move rep 2 (fail):** Implementer misread chess piece from image
  (wrong FEN → missed g2g4). Capability issue, not prompt.
- **chess-best-move rep 3 (fail):** Implementer found both correct moves. Reviewer
  overrode correct answer — vision steering injection said only Qe4# was correct.
  → v25 experiments target this.
- **custom-memory-heap-crash rep 1 (fail):** Coordinator implemented directly
  (1 of 3 reps). task_list tool's "begin" phrasing + zero reasoning tokens primed
  direct execution. → Separate fix needed for task_list reminder wording.

**Hypothesis confirmed:** ops-task skill was the primary cause of non-delegation.
Removing it + tightening coordinator inventory wording fixed crack-7z-hash (0/3 → 3/3)
and custom-memory-heap-crash (0/3 → 2/3).

### v24-C-verify retest (Mar 27)

**Run:** `v24-C-verify` — 4 tasks × 3 reps, gpt-5.4-mini, commit 819d8f4
**Build:** B merge + C (verify outputs) + ops-task removal
**Result:** Mixed — binary includes B + C + ops-task removal, NOT C alone.

| Task | Score | vs baseline |
|------|-------|-------------|
| crack-7z-hash | 3/3 | was 0/3 — improved |
| custom-memory-heap-crash | 3/3 | was 0/3 — improved |
| chess-best-move | 1/3 | was 1/3 — flat |
| log-summary-date-ranges | 2/3 | was 3/3 — regressed |

**Note:** Cannot attribute changes to C alone since binary also includes ops-task removal.

### v23 experiment results (Mar 27)

**4 variants testing reviewer/delegation prompt changes.**

| Run ID | Variant | Result | Key finding |
|--------|---------|--------|-------------|
| v23-A | verbatim-delegation | 0/3 | Coordinator still paraphrased despite verbatim instruction |
| **v23-B** | **reviewer-evidence** | **3/3** | **WINNER.** Removed "intuit" from reviewer, added spec authority to implementer |
| v23-C | verify-outputs | 2/3 | Coordinator "verify concretely" — partial improvement |
| v23-D | delegation-steering | 1/3 | System prompt injection — rejected as "bullshit hack" |

**B shipped to main.** D rejected on principle (treating symptoms, not root cause).
C retested as part of v24-C-verify but results confounded by other changes.

### v22-next20 results (Mar 27)

**Run:** `v22-next20` — 20 tasks × 3 reps, gpt-5.4-mini
**Purpose:** Broader regression test with shipped fixes (deleg-b + state-b + v17/v18).

### Non-delegation root cause investigation (Mar 27)

Interrogated 3 baseline sessions where coordinators failed to delegate:
- chess-best-move, crack-7z-hash, custom-memory-heap-crash

**All three showed the same pattern:**
1. Coordinator reads ops-task skill early in the turn
2. Gets primed by "Try it. Fix it." instructions
3. Implements directly instead of spawning implementer
4. gpt-5.4-mini uses zero reasoning tokens — snap decisions, so recency/salience wins

**Additional factor:** Old coordinator.md said "For small workspaces, use list_dir and
read_file directly" for inventory. Model extended this "do it directly" permission
beyond inventory into implementation.

**Fix applied (70095a4):**
1. Deleted ops-task embedded skill entirely
2. Rewrote coordinator inventory step: "Inventory means listing, not reading or running"
3. Added anti-rationalization: "Small tasks and simple workspaces are not exceptions"
4. Folded useful ops-task content (stuck guidance) into implementer.md

### v19 variant experiment results (Mar 27)

**9 variants across 3 problem areas, 27 experiments total.**

| Run ID | Task | Result | Key finding |
|--------|------|--------|-------------|
| v19-deleg-a | chess-best-move × 3 | 0/3 | Rep 1: no delegation. Reps 2-3: delegated but wrong answer |
| **v19-deleg-b** | chess-best-move × 3 | **3/3** | **WINNER.** 100% delegation + correct answers |
| v19-deleg-c | chess-best-move × 3 | 0/3 | Both delegated but accepted single move (missed g2g4) |
| v19-deleg-d | chess-best-move × 3 | 0/3 | Reps 1,3: no delegation. Rep 2: delegated, wrong (b2b3) |
| v19-deleg-e | chess-best-move × 3 | 0/3 | Both delegated, both found only e2e4 |
| v19-tasklist-a | kv-store-grpc × 3 | 0/3 | Not info-loss — all failed on gRPC proto contract mismatch |
| v19-tasklist-b | kv-store-grpc × 3 | 0/3 | Same as tasklist-a. Verification depth, not delegation text |
| v19-state-a | git-multibranch × 3 | 1/3 | Rep 1: Git HEAD misconfigured. Rep 3: testing mutated deliverable |
| **v19-state-b** | git-multibranch × 3 | **3/3** | **WINNER.** Post-test mutation check |

**Winners shipped to main:** deleg-b (coordinator.md) + state-b (implementer.md)

#### Interrogation findings by problem area

**Non-delegation (chess-best-move):**
- deleg-b's "quality gate, not the worker" framing was uniquely effective — 100% delegation AND correct answers
- deleg-a: 1/3 failed to delegate at all. Other 2 delegated but implementer got wrong answer
- deleg-c: Both delegated, both accepted single move without finding the second checkmate (g2g4)
- deleg-d: 2/3 failed to delegate. The 1 that delegated got wrong answer (b2b3)
- deleg-e: Both delegated via task list but implementer found only e2e4
- Common across failures: "Do NOT re-derive" instruction prevents coordinator from catching wrong implementer answers

**Delegation info loss (kv-store-grpc):**
- **Reclassified: NOT info-loss, base capability issue.** Both task-list variants (0/3 each) failed identically.
- Root cause across all 6 reps: gRPC proto field names/RPC signatures don't match verifier's expected wire format
- Coordinator consistently verified superficially (files exist, port listening) but never tested actual gRPC contract
- Even reps with reviewers (2-3 review passes) didn't catch proto mismatch — nobody ran end-to-end RPC round-trip
- Rep 3 of tasklist-a had 5 sessions (coordinator + 2 implementers + 2 reviewers) and still failed same way
- **Lesson:** Task-list/verbatim approaches can't fix verification depth problems. The coordinator needs to actually exercise the service interface, not just check superficial health

**State pollution (git-multibranch):**
- state-b "check whether testing mutated" went 3/3 — clear winner
- state-a "clone for testing" went 1/3:
  - Rep 1: Git bare repo's default HEAD not set correctly. Cloning produced "remote HEAD refers to nonexistent ref." Implementer relied on ad hoc runtime state (sshpass wrapper) instead of persistent config
  - Rep 3: `/dev/index.html` returned "dev version" instead of expected "dev branch content" — implementer's testing mutated deployed content and left it in place (exactly the state pollution state-b fixes)
- **Lesson:** Post-hoc mutation detection (state-b) more robust than prevention (state-a) because it catches unanticipated mutation paths

### v20 verification depth experiment results (Mar 27)

**Target:** kv-store-grpc × 3 reps per variant. 10 variants testing how to fix
verification depth — the coordinator/implementer never test the actual gRPC wire
contract, just check file existence and port liveness.

| Variant | Changes to | Result | Approach |
|---------|-----------|--------|----------|
| **v20-impl-test-a** | **implementer** | **3/3** | **"Write a minimal client command, verify response"** |
| **v20-combined-b** | **both** | **3/3** | **verify-a coordinator + impl-test-c implementer** |
| v20-tasklist-a | coordinator | 2/3 | Task list reinjection (basic) |
| v20-verify-b | coordinator | 2/3 | "Write grpc_cli/curl command, run it" |
| v20-verify-a | coordinator | 1/3 | "Make a real request through protocol" |
| v20-verify-c | coordinator | 1/3 | "Depth must match complexity" |
| v20-impl-test-b | implementer | 1/3 | "Test script like outside evaluator" |
| v20-impl-test-c | implementer | 1/3 | Acceptance criteria check |
| v20-tasklist-b | coordinator | 0/3 | Task list (per-endpoint emphasis) |
| **v20-combined-a** | **both** | **0/3** | **tasklist-a + impl-test-a — INTERFERENCE** |

**Two winners at 3/3:**
- **impl-test-a** — implementer: "Write a minimal client command that sends a
  request and verify the response matches the task requirements"
- **combined-b** — coordinator verify-a ("port checks alone are not verification")
  + implementer impl-test-c (acceptance criteria check, "list every requirement,
  verify each concretely")

**Critical finding: combined-a went 0/3 despite containing the winning impl-test-a.**
The task-list reinjection coordinator variant interferes with the implementer's
behavior. Hypothesis: the coordinator's expanded task list changes the delegation
text in a way that overrides or crowds out the implementer's own verification
instructions. This is a classic competing-instruction problem.

**Key insight:** Implementer-side verification fixes beat coordinator-side because
the implementer has direct access to the code, the running process, and the
ability to write and run a test client. The coordinator can only inspect from the
outside. But coordinator changes that expand the delegation text can INTERFERE
with implementer behavior (combined-a 0/3 vs impl-test-a alone 3/3).

#### v20 interrogation findings

**Root cause: spec interpretation, not verification depth.** Every kv-store-grpc
failure across ALL 10 variants traces to the same bug: the proto field is named
`val` instead of `value` in SetValRequest. The task spec says "a value (int)" and
the model interprets this as field name `val` for brevity/consistency with other
message names (SetValRequest, GetValRequest).

**Self-referential verification is structurally blind to this.** When the
implementer tests its own service with its own generated stubs, the client and
server agree on `val` — the test passes. Only an external client using the
*expected* field name `value` would detect the mismatch. No amount of "verify
deeper" prompting can fix a test that validates against itself.

**impl-test-a's 3/3 is likely stochastic.** The prompt change ("write a minimal
client command") doesn't address field naming. The model happened to choose
`value` in those 3 runs. Combined-b's 3/3 may also be stochastic for the same
reason.

**Implication:** To reliably fix kv-store-grpc, we need either:
1. A prompt that instructs the agent to cross-check spec language against proto
   field names (fragile, task-specific)
2. External verification (evaluator-provided test client or proto definition)
3. Accept it as nondeterministic (~50-70% pass rate) and move to other tasks

**This changes the ship decision for impl-test-a.** The prompt itself is harmless
and may help with other service tasks, but its 3/3 on kv-store-grpc shouldn't be
treated as signal that the prompt reliably fixes that task.

### v20 verification depth experiment design (Mar 27)

**Coordinator variants (5):**
1. **tasklist-a**: Task list reinjection — coordinator writes numbered 8-step plan before spawning. Includes "exercise the actual protocol"
2. **tasklist-b**: Task list with emphasis on per-endpoint service verification
3. **verify-a**: Step 3.4 added: "make a real request through the protocol. Port checks and file existence alone are not verification"
4. **verify-b**: Step 3.4 added: "write a command using grpc_cli, curl, python gRPC client. Run it and verify"
5. **verify-c**: Preamble: "Verification depth must match task complexity: File→read, Service→connect and request, API→call each endpoint"

**Implementer variants (3):**
6. **impl-test-a**: "Write a minimal client command that sends a request and verify the response"
7. **impl-test-b**: "Write a test script that exercises your deliverable the way an outside evaluator would"
8. **impl-test-c**: Acceptance criteria check — "list every requirement, verify each concretely"

**Combined (2):**
9. **combined-a**: tasklist-a coordinator + impl-test-a implementer
10. **combined-b**: verify-a coordinator + impl-test-c implementer

### v19 variant experiment design (Mar 27)

**Problem 1: Non-delegation (chess-best-move)**
Coordinator ignores delegation rules and handles tasks directly. 5 framing variants:
- **a**: "If you do the work yourself, you cannot verify it was done correctly"
- **b**: "You are the quality gate, not the worker. A gate cannot inspect what it built"
- **c**: "The coordinator exists to catch implementer mistakes. If you implement, nobody catches yours"
- **d**: "Delegation is the mechanism that produces correct solutions. Without it you are unreviewed"
- **e**: "Create a task list before taking action" (forces planning that includes delegation)

**Problem 2: Delegation info loss (kv-store-grpc)**
Coordinator paraphrases specs, losing exact field names/formats. 2 variants:
- **tasklist-a**: Pre-delegation task list with "VERBATIM task description you will pass"
- **tasklist-b**: "CHARACTER-FOR-CHARACTER" copying + "re-read and verify before spawning"

**Problem 3: Implementer state pollution (git-multibranch)**
Implementer testing mutates deliverable (git refs, configs). 2 variants:
- **state-a**: "Clone or copy deliverable to temporary location for testing"
- **state-b**: "Check whether your testing process mutated the deliverable"

### Latest eval: disc-3rep-v6-fixed (Mar 26)

**Run:** `disc-3rep-v6-fixed` — 56 discriminator tasks × 3 reps with template engine fixes
**Build:** commit 1b06827 (all fixes through verification revert)
**Result:** 70/163 = 42.9% (including 31 timeouts as failures)
**Result excl timeouts:** 70/132 = 53.0%
**Comparison baseline:** `disc-3rep-v6` (unfixed, same tasks) = 68/167 = 40.7%

**Net: +2.2pt overall.** 15 task improvements >15pt, 12 regressions >15pt. But only
1 regression was caused by our fixes (polyglot-c-py — verification artifact left behind).
The other 11 regressions are nondeterministic variance (implementer approach quality).

### Latest test: v18-no-tests-case (Mar 26)

**Run:** `v18-no-tests-5.4` + `v18-no-tests-mini` — log-summary-date-ranges × 3 each
**Build:** commit 4d42c75 (no-tests case in verification checklist)
**Models:** gpt-5.4 and gpt-5.4-mini
**Result:** mini 3/3, 5.4 3/3. log-summary-date-ranges SOLVED on both models.

The no-tests case fix worked: 5.4 improved from 2/3 → 3/3. Mini held at 3/3.

**Score history (log-summary-date-ranges):**
- v12 baseline: 1/3 (mini)
- v13 soft prohibition: 1/3
- v14 hard prohibition: 0/3 (REGRESSION)
- v15 positive framing: 1/3
- v16 reading-not-computing: 1/3
- v17 harmonize gate: 3/3 mini, 2/3 5.4
- v18 no-tests case: **3/3 mini, 3/3 5.4** ✓

### Broad regression: v17-broad-20 (Mar 26)

**Run:** `v17-broad-20` — 20 tasks × 1 rep on gpt-5.4-mini
**Build:** commit eaad757 (v17 HARD GATE harmonization)
**Result:** 13/18 passed, 2 still pending (circuit-fibsqrt, fix-ocaml-gc).

| Task | Result | Notes |
|------|--------|-------|
| feal-linear-cryptanalysis | PASS | |
| build-pov-ray | PASS | |
| regex-log | PASS | |
| pypi-server | PASS | |
| distribution-search | PASS | |
| polyglot-c-py | PASS | |
| fix-code-vulnerability | PASS | |
| largest-eigenval | PASS | |
| multi-source-data-merger | PASS | |
| count-dataset-tokens | PASS | |
| headless-terminal | PASS | |
| log-summary-date-ranges | PASS | |
| gpt2-codegolf | FAIL | Known too-hard (>75% failure on leaderboard) |
| chess-best-move | FAIL | **Non-delegation**: coordinator didn't spawn any subagent. Read image, hallucinated "e2e6", wrote move.txt directly. Backlog #2. |
| kv-store-grpc | FAIL | Base capability: implementer used `val` instead of `value` in proto field name. Delegation worked correctly. |
| adaptive-rejection-sampler | FAIL | Base capability: ARS envelope math wrong for normal dist. Known nondeterministic (~50%). |
| git-multibranch | FAIL | Implementer polluted bare repo state during self-testing. Verifier expects pristine repo but found leftover branch refs. Implementer suggests: "test in throwaway clone, restore pristine state." |
| winning-avg-corewars | PASS | Late arrival (spot delayed) |
| circuit-fibsqrt | PENDING | Spot reclaim, re-run needed |
| fix-ocaml-gc | PENDING | Spot reclaim, re-run needed |

### Previous test: v17-harmonize-gate-mini (Mar 26)

**Run:** `v17-harmonize-gate-mini` — log-summary-date-ranges × 3
**Build:** commit eaad757 (HARD GATE forward-references step 3)
**Model:** gpt-5.4-mini
**Result:** 3/3. Major improvement from 1/3 baseline (v12-v16).

Removing competing HARD GATE language fixed mini completely. The "reading, not
computing" checklist now takes effect consistently.

Also tested on gpt-5.4 (`v17-log-summary-5.4`): 2/3. Improvement from 1/3 but
not fully fixed. Interrogation of 5.4 rep-2 failure revealed a different root
cause: no tests exist → model feels verification is incomplete → writes own
verification script. This is the v18 target.

Session interrogation of both v16 failures revealed the same root cause: the
HARD GATE's phrases "contain what the task requires" and "verify against actual
acceptance criteria" directly contradicted "Verification is reading, not computing."
Both sessions independently reported choosing the HARD GATE over the checklist
because it appeared "stricter and more aligned with correctness."

Fix: HARD GATE now forward-references step 3 instead of establishing a competing
verification standard. The conflicting phrases are removed. The checklist is
declared exhaustive — "if every item passes, submit."

### Previous test: v16-no-scratch (Mar 26)

**Run:** `v16-no-scratch` — log-summary-date-ranges × 3
**Build:** commit d71ac81 (reading not computing + no scratch dir)
**Result:** 1/3. Same base rate as v12/v13/v15.

Added "Verification is reading, not computing" and removed scratch directory
permission entirely. Transcript analysis:
- Rep-1 (PASS): coordinator followed instruction perfectly — read_file, list_dir,
  communicate. Zero exec_commands during verification.
- Rep-3 (FAIL): coordinator explicitly ran "Recompute counts independently" via
  inline Python heredoc, ignoring the instruction.

Session interrogation of rep-2 and rep-3 failures identified the root cause:
the HARD GATE section ("contain what the task requires", "verify against actual
acceptance criteria") overrode the read-only checklist. Both sessions independently
reported the same conflict and suggested harmonizing the HARD GATE with step 3.

### Previous test: v15-positive-verify (Mar 26)

**Run:** `v15-positive-verify` — log-summary-date-ranges × 3, chess-best-move × 1, git-multibranch × 1
**Build:** commit 76f5f4f (positive-framing coordinator verification)
**Result:** log-summary 1/3, chess-best-move 1/1, git-multibranch incomplete (spot reclaim).

Positive framing alone didn't move the needle. Transcript showed coordinator used
scratch directory to run AWK recomputation, overriding correct implementer output
with flawed count. The scratch directory sentence was the explicit escape hatch.

### Previous test: v14-hard-ban (Mar 26)

**Run:** `v14-hard-ban` — log-summary-date-ranges × 3
**Build:** commit f33e96d (hard procedural ban on coordinator override)
**Result:** 0/3 — REGRESSION from v13's 1/3.

Hard "NEVER override the implementer's output based on your own recomputation"
made things worse. Interrogation of rep-1 confirmed: model acknowledges the NEVER
instruction, cites it explicitly, and violates it anyway. Said: "I violated this
instruction...I treated my homegrown check as stronger than the implementer's
deliverable."

This exhausts the prohibition framing approach:
- v12 baseline: 1/3 (no override instruction)
- v13 soft: 1/3 ("your check is more likely wrong")
- v14 hard: 0/3 ("NEVER override...you may only direct changes when a test fails")

### Previous test: v13-coordinator-verify (Mar 26)

**Run:** `v13-coordinator-verify` — 3 regression tasks × 3 reps + 2 regression checks
**Build:** commit f2a57d8 (tests-first verification, don't override passing work)
**Result:** 9/11 — fix-code-vulnerability 3/3, git-multibranch 3/3, log-summary 1/3.
chess-best-move 1/1, polyglot-c-py 1/1.

Two of three v12 failure modes fixed:
- fix-code-vulnerability: reviewer CWE suggestion no longer overrides passing tests
- git-multibranch: explicit test suite checklist catches runtime-only verification
- log-summary-date-ranges: coordinator still overrides correct implementer output

### Previous test: v12-easy-sweep (Mar 26)

**Run:** `v12-easy-sweep` — 12 easy tasks × 3 reps (36 total)
**Build:** commit 1e0ddd1 (same as v11)
**Result:** 27/35 = 77% (1 rep pending). 7 tasks at 3/3, 3 tasks at 1/3.

Three failure patterns identified via real session interrogation:

1. **Coordinator overrides correct implementer output** (log-summary-date-ranges 1/3):
   Implementer produced correct answer. Coordinator re-derived with flawed grep,
   got different number, forced implementer to "fix" to wrong answer. Both failures
   identical: coordinator's verification method was less precise than implementer's.
   Model acknowledged violating "Do not fix work that passed implementer's verification."

2. **Reviewer causes unnecessary last-minute change** (fix-code-vulnerability 1/3):
   Implementer identified correct CWE-93 from task's provided list, all tests passed.
   Reviewer suggested CWE-113 was "more precise." Coordinator changed it. Verifier
   expected CWE-93. Both failures identical pattern.

3. **Runtime-only verification, skipped /tests/** (git-multibranch 1/3):
   Implementer configured live machine. Coordinator verified running services but
   never ran `/tests/`. Verifier starts from clean state, services not running.

**Fixes applied (uncommitted):**
- Coordinator step 3: "Run the project's test suite first. If your independent check
  disagrees with a passing implementation, your check is more likely wrong."
- Coordinator step 4: "Do NOT change work that passes tests — not based on your own
  analysis, and not based on a reviewer's suggestion."
- Submit gate: explicit 3-step checklist (run tests, check output files, verify against
  task's actual criteria not your interpretation).

### Previous test: v11-positive-framing (Mar 26)

**Run:** `v11-positive-framing` — chess-best-move × 3, polyglot-c-py × 3
**Build:** commit 1e0ddd1 (positive authority ordering for reviewer + warnings-are-not-failures)
**Result:** 6/6 — chess 3/3, polyglot 3/3.

Both v10 failure modes resolved:
- Reviewer positive authority ordering ("treat domain-tool results as authoritative,
  computational proof outranks visual inspection") replaced prohibition framing.
- "A command that exits 0 succeeded — warnings are informational" resolved the
  competing-instruction conflict that caused gold-plating.

### Previous test: v10-deleg-goldplate (Mar 26)

**Run:** `v10-deleg-goldplate` — chess-best-move × 3, polyglot-c-py × 3
**Build:** commit 8679e08 (reviewer consistency + scratch dir + delegation-to-reviewer + anti-gold-plating)
**Result:** 1/6 — chess 0/3, polyglot 1/3. Regression from v9's 4/6.

All five prompt fixes were violated at least once:
- **Chess rep-1:** Implementer correct (Python engine, both moves). Reviewer
  overrode with vision despite "consistency not re-derive" instruction.
- **Chess rep-2:** Implementer trusted vision alone, never installed chess engine.
- **Chess rep-3:** Coordinator did work directly — no delegation at all.
- **Polyglot rep-5:** Coordinator verified in workspace (not scratch dir), then
  edited main.py.c itself (not via implementer). Left cmain behind.
- **Polyglot rep-6:** Timeout. Implementer gold-plated gcc warnings despite anti-
  gold-plating instruction. Named competing instructions in interrogation.

**Real session interrogation** (using fixed tool with serf --resume-with) revealed:
- **Gold-plating:** COMPETING INSTRUCTIONS. Model cited "never ignore system output",
  "read errors carefully", "correctness over speed" as overriding anti-gold-plating.
  Treated warnings as errors. Fix: "exit 0 = success, warnings are informational."
- **Reviewer override:** No competing instruction. Model just didn't apply the rule.
  Wants explicit authority ordering. Fix: positive framing "treat domain-tool results
  as authoritative" + "computational proof outranks visual inspection."
- **Coordinator non-delegation:** Terse response, acknowledged violation. Short session.

**Fixes applied (commit 1e0ddd1):**
- Reviewer: "Treat domain-tool results as authoritative" (positive framing)
- Workflow: "exit 0 succeeded — warnings are informational, not failures"

### Previous test: v9-review-fix-b (Mar 26)

Testing reviewer and verification fixes on the two remaining regressions:
- **Reviewer consistency check**: reviewer.md changed from "independently verify"
  to "review results for consistency, not by re-deriving." If the implementer
  validated with a domain tool, the reviewer checks the tool was used correctly
  rather than substituting its own analysis.
- **Scratch directory for verification**: coordinator.md step 3 now requires all
  verification artifacts go to a scratch directory (e.g. `/tmp/verify`), never the
  workspace. Prevents the verify-clean-reverify-forget pattern.
- **Active pre-submit workspace check**: coordinator.md step 5 now requires listing
  the workspace and removing verification artifacts before calling communicate.

**Run:** `v9-review-fix-b` — chess-best-move × 3, polyglot-c-py × 3

### Previous test: v8-input-fix (Mar 26)

**Run:** `v8-input-fix` — chess-best-move × 1, polyglot-c-py × 1
**Result:** 0/2 — both failed.

**chess-best-move:** First implementer got it RIGHT (python-chess found both g2g4
and e2e4). But the reviewer had no chess engine, relied on vision hallucination,
said the answer should be e2f3 (Qf3+). Coordinator trusted the reviewer over the
implementer's computational proof and told a fix-implementer to write e2f3. Root
cause: reviewer re-derived the answer instead of reviewing the implementer's
methodology and consistency.

**polyglot-c-py:** Coordinator compiled gcc into the workspace during verification
(creating cmain), cleaned up, re-verified (re-compiled, re-creating cmain), and
forgot to clean up the second time. Submitted with cmain still in the directory.
Root cause: no scratch directory rule + no active pre-submit workspace check.

### Previous test: v7-action-bias (Mar 26)

Testing 3 prompt changes on 7 regression tasks:
- **Action bias in workflow.md**: "Start building early. Research is not progress;
  working code is progress." Addresses implementer research-loop timeouts.
- **Optional explorer**: Coordinator can scout directly for small workspaces instead
  of mandatory explorer spawn. Saves 30-60s of budget.
- **Capabilities: computational verification**: "Use computational tools to verify
  what you see" instead of "do not write code to extract what you see." Addresses
  chess-best-move where baseline installed python-chess but fixed run trusted vision.

**Run:** `v7-action-bias` — 7 tasks × 1 rep
**Result:** 3/7 — feal-diff PASS, eigenval PASS, rust-c PASS. chess/ars FAIL.
circuit-fibsqrt and polyglot-c-py TIMEOUT.

### Template engine fixes shipped (commits on main)

| Commit | Fix | Root Cause |
|--------|-----|------------|
| 246a150 | Verbatim delegation guidance | Coordinator paraphrases specs |
| 3522d73 | Role before Skills + `<skill-catalog>` | Skill priming before identity |
| 5008632 | Cleanup rule in shared values | Implementer deleted task inputs |
| 830096a | Verification: "Run test commands" | Artifact-only blocked coordinator testing |
| 74230a9 | Revert RootTask injection | Too specific, not general |
| 70ae411 | Verification cleanup in step 4 | polyglot-c-py: compiled binary left behind |
| eecc20a | Action bias + optional explorer | Timeout regressions + budget waste |
| cedf53e | Computational verification for vision | chess-best-move trusted vision alone |
| e9a3989 | Don't pre-process task inputs + rename inventory/coordinator | Coordinator analyzed images for delegation |
| 72125d2 | Reviewer: consistency check, not re-derive | Reviewer hallucinated wrong answer without domain tools |
| 72125d2 | Coordinator: scratch dir for verification | Verify-clean-reverify-forget left artifacts |
| 72125d2 | Coordinator: active pre-submit workspace check | No check before communicate |
| 72125d2 | Coordinator: include task spec in reviewer delegation | Reviewer lacked format requirements, guessed wrong |
| 72125d2 | Workflow: test against spec's criteria, don't gold-plate | Implementer self-imposed -Werror, burned 14min on warnings |
| 1e0ddd1 | Reviewer: positive framing, domain-tool authority ordering | v10: "cannot override" prohibition ignored, positive framing works better |
| 1e0ddd1 | Workflow: exit 0 = success, warnings informational | v10: competing instructions ("never ignore output") overrode anti-gold-plating |
| f2a57d8 | Coordinator: tests-first verification, don't re-derive | v12: coordinator overrode correct implementer with flawed grep |
| f2a57d8 | Coordinator: passing tests outrank reviewer opinions | v12: reviewer CWE suggestion changed correct answer |
| f2a57d8 | Coordinator: explicit test suite checklist in submit gate | v12: coordinator verified runtime state, not reproducibility |
| f33e96d | Coordinator: hard "NEVER override" ban | v13: soft framing insufficient → 0/3 REGRESSION, reverted |
| 76f5f4f | Coordinator: positive-framing verification, accept implementer values | v14: prohibition framing exhausted, apply v11 lesson |
| d71ac81 | Coordinator: "reading not computing" + remove scratch dir permission | v15: scratch dir was escape hatch for recomputation |
| eaad757 | Coordinator: HARD GATE forward-references step 3, no competing criteria | v16: HARD GATE overrode read-only checklist (interrogation-confirmed) |
| 4d42c75 | Coordinator: explicit no-tests case, ban custom verification scripts | v17-5.4: absent tests → model writes own checker (interrogation-confirmed) |

### Known remaining issues

1. **Delegation info loss** — coordinator still paraphrases task specs despite
   "forward verbatim" instruction. The model rewrites. Structural solutions
   (RootTask injection) were too specific. No good general fix yet.
2. **Coordinator reads task inputs during inventory** — coordinator reads images/data
   files during step 1 inventory despite "do NOT pre-process task inputs." v8 chess
   run showed coordinator read chess_board.png as its first action. The "pre-process"
   rule targets delegation (step 2), not inventory (step 1). May need to explicitly
   say "do not read binary files during inventory."
3. **Reviewer lacks task spec context** — coordinator delegates to reviewer without
   including the original task's output format requirements. v9 chess rep-1: reviewer
   didn't know "print them all, one per line" and assumed single-move format. When
   interrogated, reviewer confirmed "I did not have the original task spec." This is
   the delegation info loss pattern applied to reviewer delegation, not just implementer.
   Fix needed: coordinator must include task spec in reviewer delegation.
4. **Reviewer re-derives without tools** — (v9 fix working: 2/3 vs 0/1) reviewer
   without equivalent domain tools falls back on hallucination. Fix: reviewer.md
   changed to "review consistency, not re-derive." But v9 chess rep-1 shows the
   coordinator still trusts reviewer over domain-tool proof AND writes files itself.
4. **Verify-clean-reverify-forget** — (v9 fix in testing) coordinator cleans up after
   first verify, re-verifies (re-creating artifacts), forgets second cleanup. Fix:
   verification artifacts must go to scratch directory, not workspace.
6. **Post-rejection self-fix** — coordinator fixes code itself after reviewer rejection
   instead of spawning a new implementer (violating "NEVER write files yourself").
   v9 chess rep-1: coordinator used cat/mv to overwrite move.txt. When interrogated,
   it admitted "I was aware of that rule; I just failed to follow it."

### Key techniques

- **Session interrogation** (`tools/interrogate_session.py`): Replay failed session
  and ask the model about its decisions. Model honestly reports instruction conflicts.
- **Comparative root-cause analysis** (`docs/skills/benchmark-driven-improvement/root-cause-prompt.md`):
  Template for subagent dispatch. Enforces side-by-side comparison, not categorization.
- **Binary verification**: Always check transcript header `build_version` AND tarball
  contents. `go build` caches embedded files — use `make build-linux`.
- **Harbor-runner staging**: Never put agent dirs under /tmp/ — parent-dir binary
  contamination. Use isolated subdirectories.

### Previous state (March 25)

### Shipped on main (commit 0977af1)

- Vision side-channel (off-loop API call, LLM-driven purpose parameter)
- detail:"original" for GPT-5.4+ images
- WithModel provider/model resolution (fixes explorer and subagent models)
- Vision section rewrite (no read_file mention in core.md)
- "Do the work, then verify" workflow guidance in core.md
- Write-early reinforcement ("If you haven't written your output files, you haven't started")
- Use-defaults: reasoning_effort on spawn_agent + stuck escalation
- Verify-depth: coordinator reads contents, not just checks file existence
- Implementer: web_fetch + task_list tools added
- Implementer: "Do not assume — verify" in How to Work
- Coordinator: "Do NOT re-derive or recompute the answer"

### Uncommitted, ready to test

1. **Workspace tree fix** (`agent/profile.go`): Shows workspace tree instead of parent
   tree in the environment block. Fixes the 78K `/tmp/` dump that was confounding all
   local tests. The parent tree scan (`scanParentTree`) picked up thousands of experiment
   files when the workspace was under `/tmp/`, inflating subagent prompts from ~10K (AWS)
   to ~88K (local). This was a massive confounder — local implementers appeared to read
   READMEs because the filesystem dump included HuggingFace cache paths.

2. **SystemPromptAsUser flag** (`session.go`, `main.go`, `run.go`, `serf_agent.py`):
   Delivers system prompt as user message. Tested 0/3 on AWS, but the implementation
   put the system prompt BEFORE the task message. This doesn't help — GPT-5.4 follows
   the instruction closest to the end of context. The system prompt needs to come AFTER
   the task, or be combined into one message with the task.

3. **Variant A coordinator** (`/tmp/coord-variants/A-artifact-only.md`): Artifact-only
   verification — coordinator inspects files, formats, and workspace state but never
   reruns computation. Tested 8/9 on AWS regression (only adaptive-rejection-sampler
   failed, known nondeterministic). Ready to ship.

## Active Experiment

**SystemPromptAsUser combined message:** The next test is combining the system prompt
and task delegation into ONE user message, so the persona instructions are adjacent to
(not separated from) the task. The current implementation delivers them as separate
messages with system prompt first, which GPT-5.4 ignores. Not yet implemented or tested.

## Key Learnings

### GPT-5.4 instruction following

- **GPT-5.4 follows instructions closest to end of context.** The `instructions`
  parameter goes at the beginning; user messages at the end. The model follows whichever
  is last. 0/45 across 15 system prompt variants on AWS.
- **System prompt instructions are ignored when user message implies routine work.**
  The implementer sees the task as "pure execution" and skips research/prerequisite
  steps regardless of system prompt wording.
- **XML-tagged prerequisites in user messages work.** H28 (`<mandatory_prerequisites>`
  + numbered steps + "Follow the documented usage instructions, not your assumptions"):
  6/6 locally. But local results were contaminated by the /tmp/ dump, so this needs
  AWS validation.
- **XML tags + numbered steps + "follow docs" is the minimum formula.** Remove any
  one element and the implementer stops reading documentation. The XML tags create a
  semantic block the model processes with higher authority than prose.
- **Graphviz doesn't work with GPT.** 0/7 compliance. Use prose with CRITICAL markers.
- **Prohibitions don't work with GPT.** Use positive framing.
- **Prompts don't change GPT-5.4 vision behavior.** "Trust what you see", "describe
  before coding" — all ignored. Only code-level changes (tool_choice, detail parameter)
  affect behavior.

### Delegation architecture

- **The coordinator should NOT pre-research domain problems.** Domain research and
  implementation are interleaved — separating them loses the feedback loop where
  "I need to know X" emerges from "I'm trying to build Y." In every passing
  protein-assembly run, the implementer did its own research. In every failure,
  the coordinator split research into separate subagents that consumed the budget.
- **Explorer = workspace scout** (files, tools, tests). NOT domain research.
- **Coordinator plans, delegates whole problems, verifies.** Does NOT decompose
  into phases.
- **Implementer owns the full problem** including domain research.
- **Time-box:** explorer=5, implementer=50.
- **What failed:** coordinator-does-implementation (0/9), coordinator-explores-itself
  (3/9), budget-aware framing (3/9), hard ban on research (0/3).

### Coordinator behavior

- **Coordinator override: caused by HARD GATE conflicting with no-rederive rule.**
  The HARD GATE demanded "run verification commands yourself," so the coordinator
  ran Python to re-derive answers. Fixed by variant A: artifact-only verification
  where both step 4 and the HARD GATE say "inspect files, not recomputation."
- **"Inspect" and "verify" are too vague.** The coordinator interprets them as
  running Python. Must be specific: "file existence, format, workspace state."

### Vision

- **Vision side-channel architecture works.** chess-best-move: 6/6 locally, 2-3/3
  on AWS. Off-loop API call with no tools forces native vision. LLM-driven `purpose`
  parameter ensures task-relevant descriptions.
- **GPT-5.4 reads chess boards perfectly** at medium+ effort with detail:original
  when no tools are available. The entire problem was the agent pipeline, not the
  model's vision capability.
- **Vision section mentioning read_file causes GPT to call read_file** instead of
  using its native vision on images already in context.
- **system_prompt_append only reaches root session.** Changes for implementers must
  go in core.md.

### Infrastructure and methodology

- **Parent tree scan dumped 78K of /tmp/ into subagent prompts.** This invalidated
  all local test results before the workspace tree fix. Local implementers appeared
  to read READMEs because the filesystem dump gave them breadcrumbs.
- **Local testing diverges from AWS for prompt compliance.** All 12 local implementers
  read the README regardless of prompt. 0/6 AWS implementers did with the same prompt.
- **Local repro with --agent implementer:** Fast iteration (~3 min per run) for testing
  implementer behavior. Sends AWS-style delegation message directly, bypassing coordinator.
- **Debrief technique:** Resume a completed session and ask the model WHY it did
  something. It reliably identifies which instructions it noticed and how it
  prioritized them. Two breakthroughs came from this: coordinator HARD GATE
  contradiction and implementer "pure execution" mental model.
- **Verify your binary.** `strings binary | grep "expected text"`. Check transcript
  headers after run to confirm correct prompt.
- **harbor-runner run IDs collide at minute granularity.** Space launches by at least
  1 minute.
- **detail:"original" is GPT-5.4-specific.** Older models use "high". Set in adapter
  based on model name prefix.
- **WithModel must resolve provider/model strings.** Agent frontmatter uses canonical
  `openai/gpt-5.4-mini` format, but WithModel stored it verbatim. Fixed with provider
  prefix parsing.
- **Agent frontmatter model names must be bare** (e.g., `gpt-5.4-mini` not
  `openai/gpt-5.4-mini`). The provider is inherited from the parent session's profile.
  Provider-prefixed names get sent verbatim to the API and fail.

### Image pipeline architecture

- **read_file on images:** `env_local.go` detects image → `parseImageResult()` extracts
  bytes + MIME type → `ToolExecResult.ImageData` → adapter includes in API call
- **Native OpenAI adapter** adds `input_image` item — model sees images correctly
- **OpenAI-compatible adapter** (third-party providers) was missing image support —
  fixed in the vision side-channel work
- **detail parameter:** "original" for GPT-5.4+ (full fidelity spatial tasks),
  "high" for older models. Set in `openai/adapter.go` based on model name prefix.
- **Despite images reaching the model,** GPT-5.4 defaults to code analysis in
  tool-calling mode. The vision side-channel (off-loop no-tools API call) is the fix.

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
| 3/22 | fix-vision-section (prompt) | chess ×3 AWS | 0/3 | Removed read_file mention, neutral for vision |
| 3/22 | fix-vision-section regression | 8 regression ×1 AWS | 8/8 pass | Safe to ship |
| 3/22 | fix-detail-high | chess ×3 AWS | **1/3** | detail:"high" helps |
| 3/22 | fix-explorer-model | chess ×3 AWS | **1/3** | WithModel fix, explorer works |
| 3/22 | Combined (no write-first) | chess ×3 AWS | **1/3** | All 3 fixes, same rate |
| 3/22 | Combined + write-first | chess ×3 AWS | 0/3 | "Do the work then verify" didn't help |
| 3/22 | Combined + trust-vision | chess ×3 AWS | **1/3** | "Trust what you see" ignored by GPT |
| 3/22 | force-text (tool_choice=none) | chess ×3 AWS | 0/3 | Eliminated rabbit hole but hallucinated positions |
| 3/22 | Direct API vision test | chess ×1 each | 5/5 move correct | medium/high perfect, low/none close |
| 3/22 | force-text (tool_choice=none) | chess ×3 AWS | 0/3 | Empty text + hallucination |
| 3/22 | Vision side-channel v1 | chess ×3 local | **3/3** | LLM-driven purpose, chess-specific suffix |
| 3/22 | Vision side-channel v2 | chess ×3 local | **3/3** | Generic suffix — still works |
| 3/22 | Side-channel AWS validation | chess 2/3, gcode 1/3 | **Shipped** | chess 0→2/3, gcode holds, regression 7/7 |
| 3/22 | install-windows-3.11 | windows ×3 AWS | 0/2 | NOT vision — socket path mismatch |
| 3/23 | Fix A: Write-early | tune-mjcf 1/3→3/3, ptr 1/3→0/3 | **Shipped** | "If you haven't written output, you haven't started" |
| 3/23 | Fix B: Interface conventions | sam 0/3→0/3, caffe 0/3→0/3 | Reject | Coordinator guidelines don't change implementer |
| 3/23 | Fix C: Verify depth | sanitize 1/3→2/3, sqlite 1/3→1/3 | **Shipped** | Marginal on sanitize |
| 3/23 | Failure rerun with shipped fixes | 45 non-reliable tasks | 6 improved, 0 regressed | count-dataset-tokens, sqlite-gcov, tune-mjcf now reliable |
| 3/23 | Eval v2 (gpt-5.4-mini) | 70+ tasks | 45/70 reliable (64%) | 7 tasks improved vs v1, 0 regressed |
| 3/24 | Coordinator override + impl research | mteb ×3 AWS | **1/3** (first pass) | web_fetch + "do not re-derive" shipped |
| 3/24 | Coordinator debrief | mteb rep 3 | N/A | HARD GATE contradicted no-rederive rule |
| 3/24 | Coord variant A (artifact-only) | mteb ×3 local | **3/3** | Aligned step 4 + HARD GATE on artifact inspection |
| 3/24 | Coord variant B (inspect) | mteb ×3 local | 0/3 | "Inspect" too vague, coordinator ran Python |
| 3/24 | Coord variant C (trust-implementer) | mteb ×3 local | 1/3 | No verification breaks other tasks |
| 3/24 | Coord variant D (test-only) | mteb ×3 local | 0/3 | Most tasks lack test suite |
| 3/24 | Coord variant A AWS regression | 9 regression ×1 | 8/9 | Only ARS failed (nondeterministic). Ready to ship |
| 3/25 | Impl V-series (12 variants) | mteb ×3 AWS each | 0/36 | System prompt step 4 wording: zero effect |
| 3/25 | Impl J-series (authority framing) | mteb ×3 AWS each | 0/9 | "Mandatory," "non-negotiable": zero effect |
| 3/25 | Impl D-series (prerequisite framing) | mteb ×3 AWS each | 2/6 | First signal but unreliable |
| 3/25 | Impl A2 (coord delegation) | mteb ×3 AWS | **2/3** | Best sys prompt approach, still stochastic |
| 3/25 | Impl H28 (XML user msg) | mteb ×6 local | **6/6** | `<mandatory_prerequisites>` + numbered + "follow docs" |
| 3/25 | Impl H29M (no XML tags) | mteb ×2 local | 0/2 | Same content without XML: fails |
| 3/25 | Impl H33 (no "follow docs") | mteb ×2 local | 0/2 | XML + numbered but no "follow docs": fails |
| 3/25 | SystemPromptAsUser (AWS) | mteb ×3 + regression | 0/3 | System prompt placed BEFORE task — wrong order |
| 3/25 | prompt-engine-mini-1rep | 56 disc ×1 | 24/56 (43%) | Template engine baseline, 12 regressions from mini baseline |
| 3/26 | delegation-fix-test1 | 3 info-loss tasks ×1 | 2/3 | Verbatim delegation: nginx+multi-source pass, log-summary fail |
| 3/26 | v2-fix-test1 | 5 regression tasks ×1 | 2/5 | + Role reorder: nginx+log-summary pass |
| 3/26 | v4-clean-test | 5 regression tasks ×1 | 2/5 | + RootTask injection (first clean build): log-summary+password pass |
| 3/26 | v6-no-inject | 5 regression tasks ×1 | 2/5 | No injection: fix-git+password pass (verification revert) |
| 3/26 | v6-3rep | 5 regression tasks ×3 | 9/15 | fix-git 3/3, nginx 2/3, password 2/3, log-summary 1/3, multi-source 1/3 |
| 3/26 | disc-3rep-v6 | 56 disc ×3 | 68/167 (41%) | **WRONG BINARY** — stale 38afc9a deployed. Useful as unfixed baseline |
| 3/26 | disc-3rep-v6-fixed | 56 disc ×3 | 70/163 (43%) | Correct binary (1b06827). +2.2pt vs unfixed. 31 timeouts |
| 3/26 | v7-action-bias | 7 regression tasks ×1 | 3/7 | feal-diff PASS, eigenval PASS, rust-c PASS. chess/ars FAIL. 2 timeout |
| 3/26 | v8-input-fix | chess ×1, polyglot ×1 | 0/2 | chess: reviewer hallucinated wrong move. polyglot: verify-clean-reverify-forget |
| 3/27 | **v20-impl-test-a** | kv-store-grpc ×3 | **3/3** | "Write minimal client, verify response." SHIP CANDIDATE |
| 3/27 | v20-tasklist-a | kv-store-grpc ×3 | 2/3 | Task list reinjection. Promising but not reliable |
| 3/27 | v20-verify-b | kv-store-grpc ×3 | 2/3 | "Write grpc_cli/curl command." Promising |
| 3/27 | v20-verify-a | kv-store-grpc ×3 | 1/3 | "Make real request." Not enough |
| 3/27 | v20-verify-c | kv-store-grpc ×3 | 1/3 | "Depth must match complexity." Not enough |
| 3/27 | v20-impl-test-b | kv-store-grpc ×3 | 1/3 | "Test script like evaluator." Too vague |
| 3/27 | v20-impl-test-c | kv-store-grpc ×3 | 1/3 | Acceptance criteria check. Too vague |
| 3/27 | v20-tasklist-b | kv-store-grpc ×3 | 0/3 | Task list (per-endpoint). Overspecified |
| 3/27 | **v20-combined-a** | kv-store-grpc ×3 | **0/3** | tasklist-a + impl-test-a. INTERFERENCE |
| 3/27 | **v20-combined-b** | kv-store-grpc ×3 | **3/3** | verify-a + impl-test-c. SHIP CANDIDATE |
| 3/27 | **v19-deleg-b** | chess-best-move ×3 | **3/3** | "Quality gate not worker." SHIPPED |
| 3/27 | **v19-state-b** | git-multibranch ×3 | **3/3** | "Check for mutation after testing." SHIPPED |
| 3/27 | v19-deleg-a | chess-best-move ×3 | 0/3 | 1 no-deleg, 2 wrong answer |
| 3/27 | v19-deleg-c | chess-best-move ×3 | 0/3 | Delegated, accepted single move |
| 3/27 | v19-deleg-d | chess-best-move ×3 | 0/3 | 2 no-deleg, 1 wrong answer |
| 3/27 | v19-deleg-e | chess-best-move ×3 | 0/3 | Delegated via task list, wrong answer |
| 3/27 | v19-tasklist-a | kv-store-grpc ×3 | 0/3 | Proto mismatch, not info-loss |
| 3/27 | v19-tasklist-b | kv-store-grpc ×3 | 0/3 | Proto mismatch, not info-loss |
| 3/27 | v19-state-a | git-multibranch ×3 | 1/3 | Clone approach less robust |
| 3/26 | v9-review-fix-b | chess ×3, polyglot ×3 | 4/6 | chess 2/3 (1 reviewer info loss), polyglot 2/3 (1 timeout). Fixes work. |
| 3/26 | v10-deleg-goldplate | chess ×3, polyglot ×3 | 1/6 | All fixes violated. Real interrogation: competing instructions + no authority ordering |
| 3/26 | v11-positive-framing | chess ×3, polyglot ×3 | 6/6 | Both v10 failure modes resolved. Positive authority ordering + warnings fix |
| 3/26 | v12-easy-sweep | 12 easy tasks ×3 | 27/35 | 77%. 3 regressions: coordinator overrides correct work, reviewer-driven changes, skipped /tests/ |
| 3/26 | v13-coordinator-verify | 3 regress ×3 + 2 check | 9/11 | fix-code-vuln 3/3, git-multi 3/3, log-summary 1/3. No regressions |
| 3/26 | v14-hard-ban | log-summary ×3 | 0/3 | REGRESSION. "NEVER override" prohibition worse than soft version |
| 3/26 | v15-positive-verify | log-summary ×3 + 2 check | 3/5 | log-summary 1/3, chess 1/1, git-multi incomplete (spot reclaim) |
| 3/26 | v16-no-scratch | log-summary ×3 | 1/3 | "Reading not computing" + no scratch dir. Same base rate. Prompt ceiling reached |

### Detailed experiment writeups

- `2026-03-17-gepa-prompt-optimization.md` — Phase 1 GEPA prompt optimization
- `2026-03-21-full-89-failure-analysis.md` — Root cause analysis of all 22 non-too-hard failures
- `2026-03-21-failure-inventory.md` — Fix plan and execution order
- `2026-03-22-vision-breakthrough.md` — Vision prompt causes vision failure (bisect proof)
- `2026-03-22-vision-side-channel.md` — Side-channel architecture and results
- `2026-03-23-gpt54mini-eval-v2.md` — gpt-5.4-mini eval v2 results
- `2026-03-23-failure-root-causes.md` — Root causes from tuning round
- `2026-03-24-coordinator-override-experiments.md` — HARD GATE debrief and variant A/B/C/D
- `2026-03-25-implementer-research.md` — System prompt vs user message, XML prerequisites

## What's Next

See [backlog.md](backlog.md) for the prioritized queue of next experiments.
