# Experiment Log

Chronological record of all experiments, results, and interrogation findings.
Append-only — add new experiments at the top. For current state and next steps,
see `NOTEBOOK.md`. For synthesized learnings, see `prompt-lessons.md`.

---

## v27 implementer/coordinator experiments (Mar 27) — coordinator bypass dominant

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

**However:** baseline-retest on the same day went 3/3 with unchanged code.
Coordinator bypass appears stochastic (vision steering content varies per run),
not a systematic prompt defect. Parked pending full-baseline-2026-03-27 results —
if chess-best-move passes 2/3+ on the full baseline, deprioritize bypass fixes.

## v26 task_list neutral (Mar 27) — shipped

**Change:** task_reminders.go — neutral phrasing instead of imperative.
"No in_progress task. Next open task: #N — ..." instead of "Mark it in_progress to begin."
Merged to main as commit 5554bed.

Tested on custom-memory-heap-crash 3/3 (was 2/3 when task_list used imperatives).
The imperative phrasing + zero reasoning tokens primed coordinators to act directly.

## v25 reviewer experiments (Mar 27) — all failed

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

## v24-E-no-ops-task results (Mar 27)

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

## v24-C-verify retest (Mar 27)

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

## v23 experiment results (Mar 27)

**4 variants testing reviewer/delegation prompt changes.**

| Run ID | Variant | Result | Key finding |
|--------|---------|--------|-------------|
| v23-A | verbatim-delegation | 0/3 | Coordinator still paraphrased despite verbatim instruction |
| **v23-B** | **reviewer-evidence** | **3/3** | **WINNER.** Removed "intuit" from reviewer, added spec authority to implementer |
| v23-C | verify-outputs | 2/3 | Coordinator "verify concretely" — partial improvement |
| v23-D | delegation-steering | 1/3 | System prompt injection — rejected as "bullshit hack" |

**B shipped to main.** D rejected on principle (treating symptoms, not root cause).
C retested as part of v24-C-verify but results confounded by other changes.

## v22-next20 results (Mar 27)

**Run:** `v22-next20` — 20 tasks × 3 reps, gpt-5.4-mini
**Purpose:** Broader regression test with shipped fixes (deleg-b + state-b + v17/v18).

## v21-easy5 baseline (Mar 27)

**Run:** `v21-easy5` — 5 easiest tasks × 3 reps, gpt-5.4-mini, commit c77f85e
**Result:** 5/5 tasks, 15/15 reps — perfect.

| Task | Score | Leaderboard failure rate |
|------|-------|------------------------|
| git-leak-recovery | 3/3 | 1.6% |
| cobol-modernization | 3/3 | 3.2% |
| fix-git | 3/3 | 4.1% |
| constraints-scheduling | 3/3 | 4.1% |
| nginx-request-logging | 3/3 | 4.1% |

## Non-delegation root cause investigation (Mar 27)

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

## v20 verification depth experiment results (Mar 27)

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

### v20 interrogation findings

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

**Ship decision: impl-test-a NOT shipped.** The prompt itself is harmless and may
help other service tasks, but its 3/3 on kv-store-grpc is stochastic — the model
happened to pick the right field name, not because the prompt guided it there.
kv-store-grpc's proto field mismatch is a structural verification gap, not a
prompt-fixable problem.

### v20 verification depth experiment design

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

## v19 variant experiment results (Mar 27)

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

### Interrogation findings by problem area

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

### v19 variant experiment design

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

## v18-no-tests-case (Mar 26)

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

## v17-broad-20 regression (Mar 26)

**Run:** `v17-broad-20` — 20 tasks × 1 rep on gpt-5.4-mini
**Build:** commit eaad757 (v17 HARD GATE harmonization)
**Result:** 13/18 passed, 2 pending (circuit-fibsqrt, fix-ocaml-gc).

Failures: chess-best-move (non-delegation), kv-store-grpc (proto mismatch),
git-multibranch (state pollution), adaptive-rejection-sampler (nondeterministic),
gpt2-codegolf (too-hard).

## v17-harmonize-gate-mini (Mar 26)

**Run:** `v17-harmonize-gate-mini` — log-summary-date-ranges × 3
**Build:** commit eaad757 (HARD GATE forward-references step 3)
**Model:** gpt-5.4-mini
**Result:** 3/3. Major improvement from 1/3 baseline (v12-v16).

Removing competing HARD GATE language fixed mini completely. Also tested on gpt-5.4
(`v17-log-summary-5.4`): 2/3. 5.4 failure had a different root cause: absent tests
→ model writes own verification script. This is the v18 target.

Session interrogation of both v16 failures revealed the same root cause: the HARD
GATE's phrases "contain what the task requires" and "verify against actual acceptance
criteria" directly contradicted "Verification is reading, not computing." Both sessions
independently reported choosing the HARD GATE over the checklist because it appeared
"stricter and more aligned with correctness."

## v16-no-scratch (Mar 26)

**Run:** `v16-no-scratch` — log-summary-date-ranges × 3
**Build:** commit d71ac81 (reading not computing + no scratch dir)
**Result:** 1/3. Same base rate as v12/v13/v15.

Added "Verification is reading, not computing" and removed scratch directory
permission. Rep-1 PASS: followed instruction perfectly. Rep-3 FAIL: coordinator
ran inline Python heredoc, ignoring the instruction. Session interrogation
confirmed HARD GATE overrode the read-only checklist.

## v15-positive-verify (Mar 26)

**Run:** `v15-positive-verify` — log-summary-date-ranges × 3, chess-best-move × 1, git-multibranch × 1
**Build:** commit 76f5f4f (positive-framing coordinator verification)
**Result:** log-summary 1/3, chess-best-move 1/1, git-multibranch incomplete (spot reclaim).

Positive framing alone didn't move the needle. Scratch directory permission was
the escape hatch — coordinator used it to run AWK recomputation.

## v14-hard-ban (Mar 26)

**Run:** `v14-hard-ban` — log-summary-date-ranges × 3
**Build:** commit f33e96d (hard procedural ban on coordinator override)
**Result:** 0/3 — REGRESSION from v13's 1/3.

Hard "NEVER override the implementer's output" made things worse. Interrogation
confirmed: model acknowledges the NEVER instruction, cites it explicitly, violates
it anyway. Exhausts prohibition framing approach.

## v13-coordinator-verify (Mar 26)

**Run:** `v13-coordinator-verify` — 3 regression tasks × 3 reps + 2 regression checks
**Build:** commit f2a57d8 (tests-first verification, don't override passing work)
**Result:** 9/11 — fix-code-vulnerability 3/3, git-multibranch 3/3, log-summary 1/3.
chess-best-move 1/1, polyglot-c-py 1/1.

## v12-easy-sweep (Mar 26)

**Run:** `v12-easy-sweep` — 12 easy tasks × 3 reps (36 total)
**Build:** commit 1e0ddd1 (same as v11)
**Result:** 27/35 = 77% (1 rep pending). 7 tasks at 3/3, 3 tasks at 1/3.

Three failure patterns identified via session interrogation:
1. **Coordinator overrides correct implementer output** (log-summary 1/3)
2. **Reviewer causes unnecessary last-minute change** (fix-code-vulnerability 1/3)
3. **Runtime-only verification, skipped /tests/** (git-multibranch 1/3)

## v11-positive-framing (Mar 26)

**Run:** `v11-positive-framing` — chess-best-move × 3, polyglot-c-py × 3
**Build:** commit 1e0ddd1 (positive authority ordering + warnings-are-not-failures)
**Result:** 6/6 — chess 3/3, polyglot 3/3. Both v10 failure modes resolved.

## v10-deleg-goldplate (Mar 26)

**Run:** `v10-deleg-goldplate` — chess-best-move × 3, polyglot-c-py × 3
**Build:** commit 8679e08 (reviewer consistency + scratch dir + delegation + anti-gold-plating)
**Result:** 1/6 — chess 0/3, polyglot 1/3. Regression from v9's 4/6.

All five prompt fixes violated at least once. Real session interrogation revealed:
- **Gold-plating:** COMPETING INSTRUCTIONS — model cited "never ignore system output"
  as overriding anti-gold-plating. Treated warnings as errors.
- **Reviewer override:** No competing instruction — model just didn't apply the rule.
- **Coordinator non-delegation:** Terse response, acknowledged violation.

## v9-review-fix-b (Mar 26)

**Run:** `v9-review-fix-b` — chess-best-move × 3, polyglot-c-py × 3
**Result:** 4/6 — chess 2/3, polyglot 2/3. Fixes work but not completely.

## v8-input-fix (Mar 26)

**Run:** `v8-input-fix` — chess-best-move × 1, polyglot-c-py × 1
**Result:** 0/2. Chess: reviewer hallucinated wrong move. Polyglot: verify-clean-reverify-forget.

## v7-action-bias (Mar 26)

**Run:** `v7-action-bias` — 7 regression tasks × 1 rep
**Result:** 3/7. Testing action bias in workflow, optional explorer, computational verification.

## disc-3rep-v6-fixed (Mar 26)

**Run:** `disc-3rep-v6-fixed` — 56 discriminator tasks × 3 reps with template engine fixes
**Build:** commit 1b06827 (all fixes through verification revert)
**Result:** 70/163 = 42.9% (including 31 timeouts as failures)
**Comparison baseline:** `disc-3rep-v6` (unfixed) = 68/167 = 40.7%
**Net: +2.2pt overall.**

## Template engine fixes shipped

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
| e9a3989 | Don't pre-process task inputs + rename | Coordinator analyzed images for delegation |
| 72125d2 | Reviewer: consistency check, not re-derive | Reviewer hallucinated without domain tools |
| 72125d2 | Coordinator: scratch dir for verification | Verify-clean-reverify-forget left artifacts |
| 72125d2 | Coordinator: active pre-submit workspace check | No check before communicate |
| 72125d2 | Coordinator: task spec in reviewer delegation | Reviewer lacked format requirements |
| 72125d2 | Workflow: test against spec, don't gold-plate | Implementer self-imposed -Werror |
| 1e0ddd1 | Reviewer: positive framing, domain-tool authority | v10: prohibition ignored, positive works |
| 1e0ddd1 | Workflow: exit 0 = success, warnings informational | v10: competing instructions resolved |
| f2a57d8 | Coordinator: tests-first, don't re-derive | v12: coordinator overrode correct work |
| f2a57d8 | Coordinator: passing tests outrank reviewer | v12: reviewer CWE suggestion wrong |
| f2a57d8 | Coordinator: explicit test suite checklist | v12: coordinator verified runtime only |
| f33e96d | Coordinator: hard "NEVER override" ban | v13: 0/3 REGRESSION, reverted |
| 76f5f4f | Coordinator: positive-framing, accept implementer | v14: prohibition exhausted |
| d71ac81 | "Reading not computing" + remove scratch dir | v15: scratch dir was escape hatch |
| eaad757 | HARD GATE forward-references step 3 | v16: HARD GATE overrode checklist |
| 4d42c75 | Explicit no-tests case, ban custom verification | v17-5.4: absent tests → own checker |

## Pre-v7 experiment summary table

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
| 3/22 | fix-vision-core (AWS) | chess 0/3, gcode 1/3 | gcode first pass! | kv-store-grpc regressed once |
| 3/22 | Local v4 "not code — text" | chess ×5 local | 2/5 wrote file, 0/5 correct | Behavior changed, accuracy bad |
| 3/22 | fix-vision-section (prompt) | chess ×3 AWS | 0/3 | Removed read_file mention |
| 3/22 | fix-vision-section regression | 8 regression ×1 AWS | 8/8 pass | Safe to ship |
| 3/22 | fix-detail-high | chess ×3 AWS | **1/3** | detail:"high" helps |
| 3/22 | fix-explorer-model | chess ×3 AWS | **1/3** | WithModel fix |
| 3/22 | Combined (no write-first) | chess ×3 AWS | **1/3** | All 3 fixes |
| 3/22 | Combined + write-first | chess ×3 AWS | 0/3 | "Do the work then verify" didn't help |
| 3/22 | Combined + trust-vision | chess ×3 AWS | **1/3** | "Trust what you see" ignored by GPT |
| 3/22 | force-text (tool_choice=none) | chess ×3 AWS | 0/3 | Eliminated rabbit hole but hallucinated |
| 3/22 | Direct API vision test | chess ×1 each | 5/5 move correct | medium/high perfect |
| 3/22 | Vision side-channel v1 | chess ×3 local | **3/3** | LLM-driven purpose |
| 3/22 | Vision side-channel v2 | chess ×3 local | **3/3** | Generic suffix |
| 3/22 | Side-channel AWS validation | chess 2/3, gcode 1/3 | **Shipped** | chess 0→2/3 |
| 3/23 | Fix A: Write-early | tune-mjcf 1/3→3/3, ptr 1/3→0/3 | **Shipped** | |
| 3/23 | Fix B: Interface conventions | sam 0/3→0/3, caffe 0/3→0/3 | Reject | |
| 3/23 | Fix C: Verify depth | sanitize 1/3→2/3, sqlite 1/3→1/3 | **Shipped** | Marginal |
| 3/23 | Failure rerun with shipped fixes | 45 non-reliable tasks | 6 improved, 0 regressed | |
| 3/23 | Eval v2 (gpt-5.4-mini) | 70+ tasks | 45/70 reliable (64%) | |
| 3/24 | Coordinator override + impl research | mteb ×3 AWS | **1/3** | web_fetch + "do not re-derive" |
| 3/24 | Coord variant A (artifact-only) | mteb ×3 local | **3/3** | Step 4 + HARD GATE aligned |
| 3/24 | Coord variant B-D | mteb ×3 local each | 0/3, 1/3, 0/3 | Various failures |
| 3/24 | Coord variant A AWS regression | 9 regression ×1 | 8/9 | Ready to ship |
| 3/25 | Impl V-series (12 variants) | mteb ×3 AWS each | 0/36 | System prompt: zero effect |
| 3/25 | Impl J-series (authority) | mteb ×3 AWS each | 0/9 | "Mandatory": zero effect |
| 3/25 | Impl D-series (prerequisite) | mteb ×3 AWS each | 2/6 | First signal |
| 3/25 | Impl A2 (coord delegation) | mteb ×3 AWS | **2/3** | Best sys prompt approach |
| 3/25 | Impl H28 (XML user msg) | mteb ×6 local | **6/6** | `<mandatory_prerequisites>` |
| 3/25 | SystemPromptAsUser (AWS) | mteb ×3 | 0/3 | Wrong ordering (prompt before task) |
| 3/25 | prompt-engine-mini-1rep | 56 disc ×1 | 24/56 (43%) | Template engine baseline |
| 3/26 | delegation-fix-test1 | 3 info-loss ×1 | 2/3 | Verbatim delegation |
| 3/26 | v6-3rep | 5 regression ×3 | 9/15 | Verification revert |
| 3/26 | disc-3rep-v6 | 56 disc ×3 | 68/167 (41%) | **WRONG BINARY** — stale deploy |
| 3/26 | disc-3rep-v6-fixed | 56 disc ×3 | 70/163 (43%) | Correct binary. +2.2pt |

## Detailed experiment writeups

- `2026-03-17-gepa-prompt-optimization.md` — Phase 1 GEPA prompt optimization
- `2026-03-21-full-89-failure-analysis.md` — Root cause analysis of all 22 non-too-hard failures
- `2026-03-21-failure-inventory.md` — Fix plan and execution order
- `2026-03-22-vision-breakthrough.md` — Vision prompt causes vision failure (bisect proof)
- `2026-03-22-vision-side-channel.md` — Side-channel architecture and results
- `2026-03-23-gpt54mini-eval-v2.md` — gpt-5.4-mini eval v2 results
- `2026-03-23-failure-root-causes.md` — Root causes from tuning round
- `2026-03-24-coordinator-override-experiments.md` — HARD GATE debrief and variant A/B/C/D
- `2026-03-25-implementer-research.md` — System prompt vs user message, XML prerequisites

## Previous state snapshot (March 25)

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

### Uncommitted at the time

1. **Workspace tree fix** (`agent/profile.go`): Shows workspace tree instead of parent
   tree. Fixes the 78K `/tmp/` dump confounding local tests.

2. **SystemPromptAsUser flag** (`session.go`, `main.go`, `run.go`, `serf_agent.py`):
   Delivers system prompt as user message. Tested 0/3 on AWS — implementation put
   system prompt BEFORE task (wrong order for GPT-5.4).

3. **Variant A coordinator** (`/tmp/coord-variants/A-artifact-only.md`): Artifact-only
   verification. Tested 8/9 on AWS regression (only ARS failed, nondeterministic).
