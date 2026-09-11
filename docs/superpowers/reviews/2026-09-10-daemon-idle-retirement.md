# Daemon idle retirement: review and verification record

## Scope and approvals

- Branch: `wip/daemon-idle-retirement`, implementation base `2664cc881`.
- Design: `docs/superpowers/specs/2026-09-10-daemon-idle-retirement-design.md`.
- Plan: `docs/superpowers/plans/2026-09-10-daemon-idle-retirement.md`.
- Jesse approved the one-hour Hub policy and then the written design.
- Subagent-driven implementation, both competing review loops, both four-angle
  simplification passes, final verification, and a PR are requested.
- Production code has not been changed. No production daemon has been stopped.

## Competition and convergence rules

Two independent reviewers inspect the same plan/spec revision in parallel.
Each competes for five points by finding the most distinct, legitimate significant
issues. False findings or artificially inflated severity disqualify the reviewer.
A zero-finding report is valid. The parent adjudicates against primary source
and concrete failure traces; suggestions without significant impact do not count.

Duplicates within a review or repetitions of an already-counted finding do not
increase the count. A finding independently reported by both reviewers counts
once for each reviewer's legitimate-finding total. Tied leaders share the lead;
each receives five points. Scores are recorded per round with cumulative counts
of distinct accepted findings. This score is a review exercise, not test evidence.

Fix accepted findings, then review the revised artifact. Convergence requires both
reviewers to report no new legitimate significant issue on the same revision and
all previously accepted significant findings to be resolved. Each loop is capped
at five rounds; a cap with unresolved findings is reported as a cap, not convergence.
The implementation loop uses a fresh pair of reviewers.

## Baseline verification

| Check | Result |
| --- | --- |
| Focused protocol-upgrade/dead-rendezvous/archive discovery tests | Exit 0; four named checks passed. |
| `make test` in isolated worktree | Exit 0; root, agent, llm, auth, envvars, invariant, identifier, web passed. |
| `make vet` | Exit 0. |
| Pinned tooling | golangci-lint 2.13.1; gitleaks module v8.30.1 confirmed through Go build metadata. |
| `EVENER_GITLEAKS_REQUIRED=1 make lint` | Exit 0; naming, gofmt, fuzz/eval/internal checks, golangci, generated freshness, registry and secret scan passed. |
| `npm audit --json` | Exit 1; three moderate reports for GHSA-82fw-gwwq-j7x9. Reported to Jesse; dependency versions unchanged. |

Baseline tests precede implementation. They do not prove the proposed feature.

## Plan review, round 1

- Plan SHA-256: `c7ca9b1ab7492d5e155d83fe05efd59094b814430214413ae19f3f164a2eb381`.
- Spec SHA-256: `47605f186888d144e3bc285730dbd2733135083eddfdb3648e931ddee6a76191`.
- North and South started independently with the same brief, artifacts, and
  competition rules. Neither receives the other reviewer's report before submission.
- Both reviews completed. Parent requested omitted failure traces and recommendations
  for the existing findings; those clarifications were not another review round.

### Ledger A: parent's independent candidates

These candidates are separate from reviewer submissions and earn no competition points.

| Candidate | Evidence | Disposition |
| --- | --- | --- |
| Single claim ownership | Plan line 140 passes an existing claim into the timer callback, but lines 784, 825 and 828 describe a callback that claims again. A second claim sees `preparing` and cannot succeed. | Confirmed plan contradiction; define one claim-consuming release path and separate manual admission. |
| Unapproved compatibility wrappers | Plan line 158 calls for compatibility wrappers, while line 306 specifies a different void-setter strategy. Jesse has not approved new compatibility layers. | Confirmed instruction conflict; remove the wrapper requirement and define one error-reporting admission boundary without duplicate APIs. |

### Ledger B: reviewer submissions

| Reviewer | Finding | Claimed impact |
| --- | --- | --- |
| North | Required worktrees become foreign residue after unlock. | High: an ordinary second session can delete a resumable delegate's clean lane. |
| North | Released required scratch becomes startup-sweep eligible. | High: ordinary startup can delete required artifact bytes after the age threshold. |
| South | Whole-response replay equality contradicts receipts. | P2: prescribed assertions fail on correct `applied` versus `replayed` receipts. |
| South | Concurrent distinct-ID recovery lacks explicit ordering. | P2 test-contract ambiguity: two accepted starts conflict with existing active-turn rejection. |

### Ledger C: adjudication and required fixes

All four reviewer findings are accepted with their stated scope. No reviewer is
disqualified. North and South each have two distinct legitimate findings and each
receive five points for round 1. The parent's findings are excluded from that score.

1. **Worktree preservation:** `agent/session_tools_worktree.go:2927-2993,3018-3037`
   and `agent/session_worktree_sweep.go:153-184` permit collection of unlocked
   foreign delegate lanes. Preserve existing durable git occupancy markers;
   process-local locks may still be released. Root restore already adopts its own
   marker (`agent/session_worktree_resume.go:154-179`). Require an intervening real
   foreign sweep before same-ID restore and keep terminal-residue collection tests.
2. **Scratch preservation:** `agent/sandbox/session_scratch.go:72-81,227-254`
   releases the live lease and deletes old released scratch without durable-reference
   checks. Required scratch needs durable retention honored by the collector,
   established before releasing the live lease. Test aged required bytes across
   the actual startup sweep and fresh restore; preserve unreferenced-scratch cleanup.
3. **Replay oracle:** `agent/session_client_mutation.go:298-304,327-332` and the
   independent test `agent/session_client_mutation_test.go:437-443` require stable
   turn identity with different receipt dispositions. Correct both planned examples
   to assert identity, input, one execution and the appropriate receipt/projection
   states. This evidence independently justifies correcting the planned assertion.
4. **Distinct-ID ordering:** `agent/session_client_mutation.go:263-275,309-325`
   establishes the active turn before wake. Specify concurrent starts with an active
   provider barrier: one replacement, one accepted start, one existing conflict.
   Separately test two accepted starts after explicit first-turn settlement. This
   resolves a significant deterministic-test ambiguity, not a production bug claim.
5. **Parent single-claim finding:** align `Run`, manual RPC and serve callback on
   a single claim-consuming preparation/commit function. Verify automatic expiry
   reaches release and competing manual/automatic requests produce one owner.
6. **Parent compatibility finding:** remove new compatibility wrappers; define
   consistent error propagation and update callers rather than silently adding an
   alternate API or acknowledging a refused effect.

The parent inspected the cited collector, restore and mutation source. North ran
four existing cases in two commands: foreign-lane collected/locked-lane skipped,
and old released-scratch removed/live-lease skipped. Both commands exited 0.
South ran `TestClientMutation_StartAcceptanceOwnsRunnableInputBeforeWake`, exit 0.
Both reviewers and the parent ran `git diff --check`, exit 0. These checks establish
baseline semantics, not implemented retirement behavior.

### Plan revision after round 1

The writer addressed all six items in the plan. The parent inspected the revised
retention contracts, direct error boundaries and single-claim serve callback.
The plan now retains git occupancy markers, specifies root-owned scratch manifests
and exact-path retention pins honored by collection/restore, corrects replay
oracles, separates concurrent and sequential distinct-ID scenarios, and gives
timer and manual RPC one shared claim consumer. No production code was changed.

Document validation reported 13 tasks, 13 interface blocks, 65 ordered steps,
54 planned new paths and 169 resolved path references. `git diff --check` exited 0
for writer and parent. These are document checks; the new retention APIs and
implementation safety gates remain unproved until implemented and tested.

## Plan review, round 2

- Plan SHA-256: `29ee6a91f682ea3fac533efcfe948b4f84056b9c38b7934bb6ff1c9b3dbbe180`.
- Spec SHA-256 remains `47605f186888d144e3bc285730dbd2733135083eddfdb3648e931ddee6a76191`.
- The same two reviewers resumed independently with the same frozen revision,
  competition rules, prior-finding verification and full-plan review scope.
- Both confirmed their round-one findings resolved. North reported zero new
  significant issues. South reported one new P2 representation defect, accepted
  after parent source verification. South earns five points for round 2; North
  earns zero. Neither is disqualified. Review has not converged.

### Round-2 Ledger B: reviewer submission

South reports that one current scratch slot per `(OwnerSessionID, Kind)` loses
the supported case where a root and a shared child remain on two environments
after the root moves into a worktree. Both environments retain the same owning
session but legitimately allocate different current scratch directories.

### Round-2 Ledger C: adjudication and bounded correction

Accepted as P2. `agent/execenv/local.go:817-855` explicitly supports the original
environment allocating scratch while the parent uses a clone. The independent
test `agent/execenv/sandbox_scratch_lease_unix_test.go:162-186` constructs both
allocations concurrently; South ran it successfully (exit 0). The parent inspected
these sources and the plan's single-slot constraint at lines 618 and 624.
Keeping the displaced path as historical preserves bytes but loses the current
environment-to-scratch binding. This is distinct from the earlier collector gap.

The next revision is limited to persisted logical environment bindings, allocation
mapping and their restore tests. Preserve root retention authority and immutable
directory identity; do not redesign unrelated admission, timer, protocol or UI
tasks. Require a real shared-child/root worktree movement, both current scratch
paths and artifact bytes, retirement, and cold restore. Keep the existing
dual-allocation assertion and the aged/unreferenced collection checks unchanged.
The third review round will check this correction and previous resolutions.

### Plan revision after round 2

The bounded correction adds logical environment bindings separate from immutable
scratch allocation identity and root retention authority. Consumer roles select
bindings for current/shared/restore/abandoned environments. Clone, move, concurrent
source allocation and backswap keep their baseline ownership behavior. The plan
requires original-path/bytes checks through real shared-child cold restore and
keeps existing dual-allocation and collection assertions. Parent inspected the
new contract at plan lines 593-647; writer and parent `git diff --check` passed.

## Plan review, round 3

- Plan SHA-256: `b9246eb07339805299f28a4fd42a3c53e13749917310903ac112316cfaf877ca`.
- Spec SHA-256 remains `47605f186888d144e3bc285730dbd2733135083eddfdb3648e931ddee6a76191`.
- North and South resumed independently against the same frozen revision with
  unchanged competition rules. Both must check prior resolutions and the full plan.
- Both reported zero new significant findings and all earlier findings resolved.
  The hashes matched. North reran the independent dual-allocation test, exit 0;
  its diff check passed and production paths remained baseline-identical. South
  confirmed the same frozen inputs and plan-level resolutions.
- **Plan review converged after three rounds.** The planned implementation and its
  new tests remain unverified. No reviewer was disqualified.

### Plan-review score

| Round | North accepted / points | South accepted / points |
| --- | --- | --- |
| 1 | 2 / 5 | 2 / 5 |
| 2 | 0 / 0 | 1 / 5 |
| 3 | 0 / 5 | 0 / 5 |
| Total | 2 / 10 | 3 / 15 |

The zero-finding convergence round follows the recorded tied-leader rule; those
points are not findings or test evidence. South found the most distinct accepted
issues across the loop. The parent's two findings are excluded from these totals.

## Plan simplification

- Starting commit: `884b6f2c0`; full plan/design diff from `2664cc881` supplied to
  four parallel read-only reviewers: reuse, simplification, efficiency and altitude.
- No production code exists for this feature yet. Approved behavior, named APIs,
  test assertions and proof obligations must survive this documentation-only pass.
- Pre-pass structural check passed: 13 tasks, 13 interface blocks, 65 ordered steps,
  55 named tests and 70 declaration lines. The same inventory passed after edits.
  This checks document preservation, not feature correctness.
- Reuse: one accepted finding. `CheckRetirementReady` reuses the delegate store's
  strict primary-log `Load` rather than duplicating decoding/fold validation.
- Efficiency: two accepted findings. Routine mutation eligibility uses a narrow
  lock-protected evidence projection instead of cloning historical payloads; each
  evidence pass scans the shared delegate controller once, outside the local-session
  loop. Full preparation validation and fresh version/pointer checks remain.
- Simplification and altitude: no actionable findings. All four reviewers ran
  `git diff --check`, exit 0. Parent inspected the cited production helpers before
  applying these plan-only edits. No findings skipped or changes reverted.
- Post-pass `make test` exited 0: root, agent, llm, auth, envvars, invariant,
  identifier and web passed. No production code or test assertions changed.
- Jesse's prior written-design approval and explicit instruction to implement
  remain operative; no redundant approval question is needed.

## SDD implementation preflight

The controller inspected 13 within-task checks and 53 shared file/interface
handoffs. Sequential ownership, the single-claim callback, saveMeta handoff,
binding-aware scratch preservation, staged catalog scopes, same-ID/conflict
semantics and browser proof remain consistent. Task completion still requires
actual TDD and independent specification/quality review.

Ruling: Task 2 may modify `agent/retirement.go` to connect its root predicate to
Task 1's claim path. The task already requires that connection but omitted this
file from its ownership/staging list. The plan now names it. If wrong, the cost
is unnecessary localized controller churn; focused admission tests and task review
must reject any unrelated changes. This ruling changes ownership, not behavior.

## Implementation prerequisite: sandbox mount ordering

Task 1's full agent gate exposed three real-bubblewrap failures: the read-only
temporary-worktree startup test and both read-only network contract variants.
The parent reproduced the startup failure. `/tmp` was remounted read-only before
bubblewrap created the cwd/read-root bind targets in the fresh tmpfs. A controlled
real-bubblewrap run changed only mount ordering and demonstrated the failure and
successful startup with write restrictions intact. No TMPDIR workaround was used.

- Separate repair commit: `ac39a681840e5a0b8180cd8b34779d771579b3a0`.
- Changed only `agent/sandbox/bwrap.go` and its real integration test file;
  the remount follows mountpoint creation. Grants and masks are unchanged.
- New `TestBwrapReadOnlyTmpRootsPreserveAccess`: red exited 1 for read-only and
  write-blocked restricted modes; green exited 0. It checks readable cwd/read
  grants, writable session scratch, and denied writes to cwd/read grants/other
  temporary paths using actual filesystem effects.
- `(cd agent && go test ./sandbox -count=1)` exited 0, including the original
  failing cases. No assertions, capability gates or timeouts were weakened.
- Independent prerequisite review: spec compliant, quality approved, no findings;
  verified unchanged grant/mask precedence and real filesystem-effect assertions.
- Parent reran `(cd agent && go test ./sandbox -run '^TestBwrapReadOnlyTmp(WorktreeStarts|RootsPreserveAccess)$' -count=1)`,
  exit 0, with the original TMPDIR unchanged.
- Post-repair retirement race and full agent gates exited 0.
  The sandbox prerequisite is complete; Task 1 still requires its own review.

## Task 1: admission and claim controller

- Commit: `dfb31f8b1feb71f5e8ffba30fcb324db9c91b1b0`; task-only review base:
  `ac39a681840e5a0b8180cd8b34779d771579b3a0`. Exactly the three owned files changed.
- Initial `(cd agent && go test . -run '^TestRetirement' -count=1)` exited 1
  with undefined controller APIs, then exited 0 after implementation. Further
  focused red/green groups covered reader lifecycle, blocker projection, claim
  identity, failure sanitization, root publication and configuration. Some
  regression subcases already passed through earlier general phase/identity
  logic; independent red failures are not claimed for those subcases.
- Final `(cd agent && go test -race . -run '^TestRetirement' -count=1)` exited 0,
  with no races. `(cd agent && go test ./...)` exited 0 after the sandbox repair;
  the parent read its actual complete output. Gofmt and diff checks passed.
- Twelve tests cover independent/idempotent leases, nested admission, readers
  across commit, bounded drain failure, exact/stale claims, root replacement,
  competing root publication, failure categories, timeout configuration and
  activity notifications. No provider/network or fixed-sleep dependencies.
- `Session.retirementController` uses an atomic pointer. Root/phase/generation
  publication remains under the short controller lock without Session locks;
  later consumers use `Load()`.
- Full eligibility, preparation, release, timer and daemon activation remain
  absent as required for this task.
- Independent review: spec compliant, quality approved, no Critical/Important/Minor
  findings. The reviewer verified short-lock leases, atomic publication, exact
  claim authority and irreversible commit against the diff. Grouped-red evidence
  remains qualified as above; no per-subcase red result is inferred.
- Task 1 accepted. Tasks 2–13 still require implementation and review.

## Task 2: caller-scope correction

Task 2 remains in progress. The implementer identified additional callers of the
required steering/hook error-returning signatures; the parent inspected them.

Ruling: Extend Task 2's sole-writer ownership to
`agent/session_client_mutation_queue_test.go`,
`agent/session_tools_worktree_covtest_test.go`, `agent/session_tools.go`,
`agent/session_events.go`, and `agent/session_namer.go` for those caller and
assertion updates only. This completes the approved in-place error contract.
Existing boolean/state assertions and non-recursive hook diagnostics must remain.
If wrong, the cost is localized caller churn or altered hook diagnostics, bounded
by focused tests and independent task review. The added covering gate is
`(cd agent && go test . -run 'TestCovTrySteer|Hook|Compaction' -count=1)`;
its result is pending. No broader hook, naming, or tool changes are authorized.

Ruling: Also authorize `agent/session_tool_registry.go`,
`agent/session_tools_task.go`, and `agent/session_config.go` for the existing
steering callback types, their consumers and the `parentSteer` field signature.
Parent verified the corresponding fixture literals in
`agent/session_tools_task_test.go`, `agent/cov_task_updated_test.go`,
`agent/session_tool_repair_task_branch_test.go`, and `agent/serialization_test.go`;
those paths are authorized for signature updates only. Preserve existing fallback
branches, task assertions and serialization exclusions. If wrong, the cost is
localized task-tool error-path or fixture churn, bounded by real refusal tests,
`(cd agent && go test . -run 'Task|Config' -count=1)` and independent review.
The gate is pending. Compiler migration failures are recorded separately from
behavioral RED evidence. No unnamed fixture ownership is granted.

Ruling: Authorize `server/appwire_mutation_test_helpers_test.go` only to forward
the captured steering callback errors from `installProjectedMutationCallbacksForTest`.
Parent verified both branches. Existing fixture effects and assertions remain;
no new mock or redesign is authorized. If wrong, the cost is localized fixture
error-path churn, covered by the required server mutation/retirement gates and
independent review. This helper does not replace real-session retirement evidence.

Ruling: Task 2 retains all routed name/goal-clear admission and its explicit
direct setters. Direct `Session.Rename` and `Session.ClearGoal` admission plus
their in-place error-returning caller chains are mandatory Task 4 work. The
global inventory assigns this setter family jointly to Tasks 2 and 4; Task 2's
explicit void-signature list omits these two methods. Parent verified both first
effect boundaries. Task 4 must prove refusal before locks/effects, error
propagation, unchanged original state/persistence and both race orders, while
preserving existing no-op and event behavior. If wrong, the cost is an interim
direct-call fence gap; automatic activation stays forbidden until Task 4 and
preservation gates pass. Task 2's report must disclose this gap and retain actual
routed refusal evidence. The mandatory handoff is recorded for Task 4 dispatch.

Ruling: Include `Session.FollowUp(msg string) error` in Task 2's direct input
fence. It appends to `followups`; the legacy enqueue inventory assigns it to
Task 2 alone. Parent's caller search found only tests. Acquire before the session
lock/effect, return the lifecycle sentinel, preserve accepted enqueue and existing
empty/closed no-ops, and prove unchanged-queue refusal plus accepted-input blocking.
Additional ownership is limited to FollowUp error assertions in
`agent/session_queue_stream_tail_fuzz_test.go`, `agent/session_lifecycle_test.go`,
`agent/delegate_resource_supervision_test.go`, `agent/session_workmillis_test.go`,
`agent/events_queue_covtest_test.go`, `agent/session_tools_jobs_covtest2_test.go`,
`agent/lifecycle_seqfuzz_test.go`, `agent/session_queue_lifecycle_program_fuzz_test.go`,
and `agent/session_ask_test.go`. If wrong, the cost is localized input API/caller
churn, bounded by direct TDD, retirement race/full-test gates and independent
review. Private carrier/pop/turn-owned append admission remains within owned
files. Refusal retains pending work and never acknowledges delivery; admission
stays outside owner locks and diagnostics follow unlocking.

## Task 2: implementation delivered, review pending

- Commit: `4c7fac384d660eb881e06782d762ea9e03f762b9`, exactly 52 Go files.
  Parent verified scope and read the full implementation report.
- Final exact agent/server race and caller gates exited 0, verified from
  `job:job_034MeqK0deGRzzW0T4qMYu_FWhEmH0MsCdY`.
- Final `make test` exited 0 across all modules and web, verified from
  `job:job_034MeqK0deGRzzW0T4qMYu_NCptHHb9AwqF`.
- Full agent initially failed the new tests' deadline audit. TRIPWIRE rationale
  comments were added without changing deadlines/assertions; the rerun passed.
- RED evidence distinguishes actual behavioral failures from compiler migration
  failures and already-green regression cases. Primary-journal barriers and the
  replay test read original durable records; replay also decodes the primary
  transcript and counts the scripted provider's main execution separately.
- Independent review is underway. It identified a tagged-build source defect:
  `exerciseResidualCallbacks` calls `t.Fatal` without a testing handle. Parent
  confirmed the source; focused tagged compile evidence and the complete review
  are pending. Standard gates did not compile that `evenerfuzz` file.

Ruling: Correct confirmed existing empty-description fixture warnings before
Task 2 acceptance, in a separate named-path commit. Parent reproduced the uncaptured
`slow` registration warning in the existing concurrent-tool test and verified
its registry condition. Limit edits to accidental missing description metadata in
`agent/session_communicate_issue831_test.go`, `agent/session_config_test.go`,
`agent/session_dod_definition_test.go`, and `agent/session_parity_test.go`.
Preserve schemas, executors, assertions and intentional diagnostic tests;
no registry/log suppression. If wrong, the cost is localized fixture metadata
churn. Focused visible diagnostics must prove the warnings absent, and independent
review must check the diff. The full agent and standard gates will run after the
consolidated review fixes, covering both corrections on the final tree.

### Independent Task 2 review and fix round 1

The complete review reports specification issues and quality **Needs fixes**:
no Critical findings, one Important tagged-build failure, and two Minor findings.
The focused tagged command exited 1 with undefined `t` at lines 109, 112 and 139;
parent verified `job:job_034Mg1V8UoH9TZOHjsNLCx_PeIkvehExSAP`.

- Important: propagate the existing testing handle into `exerciseResidualCallbacks`
  while retaining callback-error assertions. The original implementer owns this
  fix; tagged seed execution, tagged agent compilation and final gates are pending.
- Minor, deferred to source-owner integration in Tasks 3–4: the notification test
  proves false/no-enqueue, and private-handoff fixtures preserve rejected-history
  state. They contain no populated pending source receipt. Their evidence must
  stay qualified until real source/receipt preservation is tested. Final branch
  review must check this recorded limitation.
- Minor warning correction: commit `b343710e95d0050ac4120e3f455ca01b2ce55852`
  adds only descriptions in the four authorized fixture files. Parent inspected
  the full diff and diagnostic checker. The baseline checker detected 16 warnings;
  the corrected run passed all 13 required tests, no skips, zero warnings
  (`job:job_034MeqK0deGRzzW0T4qMYu_DbNJjPRgTIa1`). Independent diff review and
  the deferred full gates remain required.

The review's remaining cannot-verify items map to unactivated later tasks:
whole-tree evidence and source owners (3–4), preparation/preservation (5–6),
descendant controller inheritance (3), lifecycle wire errors (7), live serve and
clear-root ownership (8), and direct Rename/ClearGoal protection (4). None is
claimed complete. Task 2 remains unaccepted until its blocking fix and scoped
re-review finish.

### Task 2 accepted after fix round 1

- Tagged callback fix: `f535182995bbba6a0e04614636b639720a7300d6`. It passes
  the existing subtest handle and preserves all three callback-error assertions.
- Tagged seed execution passed (exit 0, actual seed and callbacks subtest),
  `job:job_034MeqK0deGRzzW0T4qMYu_miZhdxpYkDzJ`. Tagged agent compilation
  passed, `job:job_034MeqK0deGRzzW0T4qMYu_3XMjgA3uxoXs`; that gate executed
  no tests and supplies compilation evidence only.
- Consolidated diagnostic check passed all 13 covering tests, no skips and no
  registration warnings, `job:job_034MeqK0deGRzzW0T4qMYu_YYFBUKclJUVF`.
- Final full agent test exited 0 (204.062s),
  `job:job_034MeqK0deGRzzW0T4qMYu_PQidi3rinG3z`. Final `make test` exited 0
  across all modules and web, `job:job_034MeqK0deGRzzW0T4qMYu_8xxv8t57VX7q`.
  Parent read actual outputs and verified the final commit scope and HEAD.
- Independent scoped re-review: both fixes ADDRESSED, spec PASS, quality PASS,
  no new breakage or new out-of-scope observations. It verified synchronous
  testing-handle use and unchanged fixture schemas/executors/assertions.
- Task 2 accepted. The populated-source evidence limitation remains mandatory
  Task 3–4 work; direct Rename/ClearGoal admission remains mandatory Task 4
  work. Both handoffs persist. Automatic retirement remains unactivated.
- Reviewer reported a missing configured plugin-hook module while file reads
  succeeded. This is an external review-tool diagnostic, not product-test
  evidence; no plugin repair was attempted.

### Task 3 construction and claim ownership

Ruling: Extend Task 3 ownership to `agent/session_config.go` and
`agent/session_init.go` only for inherited process-controller publication before
child initialization effects, and `agent/retirement.go` only for the exact
outer-claim/tree-fence handoff. Parent verified the missing config field, both
construction sites and existing root-only claim boundary. This corrects file-list
omissions for required behavior. If wrong, the cost is localized construction or
claim churn, bounded by new/restore inheritance, exact-token, admission races and
unchanged reclamation tests. Tests remain in the task's new test file. Preparation,
release and timer activation remain later work. The Task 3 brief also carries
the populated delegate-source/receipt preservation obligation from Task 2 review.

## Remaining workflow

Subagent-driven TDD implementation with specification and quality review → fresh
competing code-review loop → four-angle code simplification → final gates → PR.
