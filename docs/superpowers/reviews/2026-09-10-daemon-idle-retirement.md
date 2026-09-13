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

Ruling: Also authorize `agent/delegate_tree_stop.go` for admission/handoff at
`StopSubtree`, `StopSubtreeAndDrive`, `CloseResumability`, and
`Close`/`closeRuntimeTree`, including directly related continuations. Parent
confirmed these enter controller locks before durable stop/close effects; an
outer lease cannot be acquired inside locked authorization. Preserve admitted
continuation ownership, terminal close, stop identity/retry and retained delivery.
If wrong, the cost is localized stop/close ordering churn. New direct race and
original-record tests remain in the task's new test file; the additional
`(cd agent && go test -race . -run '^TestDelegateController' -count=1)` gate must
cover existing stop/close/restore tests without editing them. Retirement release
remains separate future work.

Ruling: Also authorize `agent/delegate_tree_start.go` and
`agent/delegate_tree_work.go` for direct reservation/work admission and completion
notification handoff. Parent inspected `ReserveCreate`, `ReserveAttention`,
`ReserveStart` and `BeginShellWork`; admission must precede controller locks and
ID/capacity/token/work effects. Existing exact tokens retain continuation authority;
completion notifications follow unlocking. If wrong, the cost is localized
reservation/work ordering churn, bounded by direct real-owner tests, the existing
controller race gate and full-agent tests. Existing tests and unrelated behavior
remain unchanged. The implementer reports initial cold/create behavioral RED;
full evidence remains pending.

Ruling: Also authorize `agent/delegate_tree_restore.go` and
`agent/delegate_tree_steer.go` for direct `Reconcile`, `BeginSteerPersistence`
and `beginCallerSteerPersistence` admission and settlement handoff. Parent
confirmed the first locks and effects. Preserve evidence-version/exact-token
checks and ownership across caller-to-root handoff and returned effect plans.
Existing accepted and live-generation continuations retain their authority.
If wrong, the cost is localized recovery/steering ordering churn, bounded by
direct original-record tests and existing controller/race/full gates. No
`delegate_tree_finish.go` edits or blanket ownership are authorized.

### Task 3 delivered; independent review pending

- Commit `c5792a1943bf72c127a927672e1f2271d7db71a2`, 18 owned files. Parent
  verified HEAD, scope and diff check; prior reclamation/retirement tests unchanged.
- Required race gate passed, `job:job_034MgUgSANwaKiUBNAhDgW_eaQLoSNhqj5a`;
  supplemental controller race passed, `job:job_034MgUgSANwaKiUBNAhDgW_OyUfWTQR1eSu`.
- Initial full-agent gate failed the deadline-comment audit. Rationale comments
  moved beside the existing literals without changing bounds/assertions. Corrected
  full-agent gate passed, `job:job_034MgUgSANwaKiUBNAhDgW_YGJ9Lm9xvEAF` (181.087s).
  Parent read the actual complete final outputs.
- Parent ran the new tests with `-v`: exit 0, 5.326s, no warnings or skips.
  Populated outcome/watch source cases and real create/send/reconstruction barriers
  genuinely ran. FIFO portability remains an explicit test-environment limitation.
- Report distinguishes behavioral RED from fixture/compiler failures and later
  already-green regressions. Those later cases lack separate preimplementation RED;
  no stronger process evidence is claimed.
- Combined spec/quality review is running. Task 3 remains unaccepted. Full
  non-terminal preservation and non-delegate obligations remain later tasks.

### Task 4 preflight setter ownership

Task 4 remains undispatched while Task 3 review runs. Its extracted brief carries
the direct-setter and populated-source handoffs.

Ruling: Authorize `agent/session.go`, `server/server.go`,
`server/appwire_runtime.go` and `cmd/evener/serve.go` only for direct
Rename/ClearGoal callback admission and error propagation. Exact caller-test
ownership is recorded in the Task 4 brief: goal/notification/concurrency/rename
tests, affected fuzz callers, server surface/retirement tests and the tagged
serve residual fixture. Parent enumerated current calls and inspected route
effects. Existing assertions remain; server retirement tests may add real routed
refusal evidence. If wrong, the cost is localized setter/callback churn, bounded
by direct/routed races, positive regressions, tagged gates and final `make test`.
No Task 4 implementation begins before Task 3 acceptance.

### Task 3 independent review and fix round 1

Independent review: spec issues, quality **Needs fixes**, no Critical findings,
three Important findings and one Minor. Parent read the complete report.

- Cold idle attachment acquires/releases outer admission while its caller holds
  the subagent-manager mutex. Preserve atomic close-versus-bind publication while
  moving admission/notification outside all owner locks.
- Direct `CloseResumability` drops its lease before returning update effects.
  Carry ownership through their application and test the direct handoff.
- Required evidence is missing for populated attention, admitted-first outcome
  acknowledgement, reconciliation/steering/stop-driver/close handoffs, shell
  claim-first refusal, and the exact-pointer retirement release interface.

All three enter one consolidated fix round through the original implementer,
with behavioral RED for production repairs and scoped re-review afterward.
The review positively verified the existing original outcome/watch source
oracles. Non-delegate source and full-preservation obligations remain later work.

Ruling: Retain missing historical per-case RED as an explicit deferred Minor for
final review. Later GREEN cannot establish past execution order; do not delete
working code or relabel results to manufacture it. New production repairs still
require observed behavioral RED, and all Important findings must be resolved
before Task 3 acceptance. If wrong, the cost is weaker historical TDD assurance
for the disclosed earlier regression cases. This does not waive current safety
assertions or required missing tests.

Ruling: Test I3's bound-runtime boundary through a real running binding that
blocks TryClaim, followed by direct no-fence release refusal and unchanged
runtime/durable state. Source inspection confirms successful retirement cannot
coexist with that binding through admitted entry. This does not exercise the
defensive binding guard under a fence; no forged fence is authorized. Separate
real claimed-idle exact/replacement-pointer cases remain required. Caller-to-root
steering evidence must retain original persisted root input after real caller
settlement, prove that input independently blocks, and consume/settle it before
eligibility. A claim attempt after persistence proves a handoff, not a paused
persistence race. If wrong, the cost is narrower interleaving/defensive-branch
evidence, explicitly subject to scoped review. Other I3 requirements remain.

Ruling: Authorize exact I1 maintenance in `agent/delegate_tree_controller_test.go`:
classify `beginIdleRuntimeInstallation`, migrate the reconstruction expected
reference to that method at count 1, and add its newly classified direct
`AttachIdleRuntime` caller at count 1. Preserve `AttachIdleRuntime` classification,
all unrelated rows and guard assertions. Parent inspected the required lock-order
repair and reproduced the obsolete-reference failure (exit 1). If wrong, the cost
is a static inventory blind spot, bounded by exact references, unchanged guards
and the direct installation test. Inventory, controller race and full-agent gates
must run again after maintenance; this permission does not waive failing tests.

### Task 3 fix delivered; scoped review pending

- Fix commit `9fca1b774df2e84ed65d309b6970e1a2e9c76c8c`, five authorized
  files. Parent verified the exact scope, report appendix and diff check.
- Actual I1/I2 behavioral RED and corrected GREEN were inspected. Later I3
  additions remain regression evidence, with the stated reachable-state limits.
- Final focused verbose, retirement race, controller race and tagged compile
  completed in one `&&` chain, exit 0. Focused output has no warnings or skips.
- Final full-agent gate passed, root 175.104s, all listed subpackages cached.
  Parent read the actual final outputs, not only the implementer's summaries.
- Scoped independent I1–I3 and fix-breakage review is running. Task 3 remains
  unaccepted; Task 4 is still preflight only.

### Task 3 acceptance

- Scoped re-review: I1, I2 and I3 addressed; spec and quality PASS under the
  recorded evidence rulings; no new Critical/Important/Minor breakage.
- Parent read the full review and reran the two repaired ownership cases plus
  the previously failing inventory test at the committed HEAD: exit 0, 0.914s,
  all three RUN/PASS, no diagnostics or skips. Final covering gates are above.
- Task 3 accepted. Historical M1 remains a disclosed deviation for final review.
  Bound-pointer and caller-steering evidence retain their stated limits. The
  reviewer also noted that direct release tests check pointer/state/journal
  effects but discard error returns; that coverage limitation remains visible.
- Task 4 receives the accepted interfaces and both mandatory setter/source
  handoffs. Later blocker, preservation and activation tasks are still pending.

### Task 4 environment-work ownership

Ruling: Extend Task 4's `agent/session.go` ownership only to the `Session.envWork`
field type, changing `map[envWorkID]string` to `map[envWorkID]envWorkRecord`. The
label/release record belongs in already-owned `session_lifecycle.go`. Parent
inspected the paired record paths and read the initial behavioral failure:
beginEnvWork allowed retirement to enter preparing with no blockers. Retaining
the lease in the original record is already required by the approved brief.
Preserve handles, labels, rollback ownership, nil-controller behavior and close
joins. Admission precedes locks; release follows settlement and unlocking. No
parallel lifecycle or additional Session fields are authorized. If wrong, the
cost is localized record/settlement churn or a lost lease, bounded by environment
cases, both race orders, final retirement/EnvWork race gates and independent task
review. Implementation and verification remain pending.

### Task 4 routed-refusal goal fixture

Ruling: Allow only the setup of `TestRetirementRoutedEngineEffectsRefused` and
directly needed local provider helpers/imports in its existing file to complete
the seeded goal through a real scripted-provider `update_goal` complete turn.
Parent verified the active SetGoal write, the fixture's claim prerequisite and
the independent design requirement that active goals block retirement. Preserve
the original objective and terminal goal record, assert those preconditions after
settlement, then take the metadata snapshot and claim. All existing refusal,
metadata, queue, read and claim assertions remain unchanged; other fixture users
retain their setup. No goal clearing or forged state. Record the old-fixture
failure after the goal blocker lands, then focused and server-race GREEN.
Separate active-goal and real-settlement safety cases remain required. If wrong,
the cost is narrowed fixture coverage or a concealed setup regression, bounded
by retained-goal preconditions, unchanged assertions, paired active-goal evidence
and independent review. Fixture maintenance remains distinct from production TDD.

### Task 4 shared-owner evidence integration

Ruling: Extend ownership only to `delegateTreeController.retirementEvidence` in
`agent/delegate_tree_retirement.go`, replacing narrow resident/root projections
with local collection and calling a cold helper from the existing cold loop.
Parent verified that this method owns enumeration and final version/fence/exact
pointer revalidation. All additional evidence stays outside locks and before
that final validation. Preserve ancestry, descriptor and cold transcript/attention
checks, leaf-first pointers and all existing assertions. Helpers remain in the
Task 4 evidence file, read-only and fail-closed, without another shared scan or
validation algorithm. Existing TryClaim ownership covers a complete tree-less
root path and avoids redundant root collection. No other delegate methods or
existing tests are authorized. If wrong, the cost is weakened evidence or stale
pointer acceptance, bounded by unchanged validation, new real resident/cold
RED/GREEN cases, retirement and supplemental controller race gates, and task
review. The supplement requires the final `^TestDelegateController` race gate.

### Task 4 job-manager owner wiring

Ruling: Allow only `jm.retirementOwner = s` in the two initial job-manager setup
blocks of `agent/session_init.go`, before publication and subsequent restore
effects. Parent verified those sites, constructor work, createShell's pre-lock
output I/O and the mutable delegate-controller field's locked reassignment.
Keep the new owner immutable and load its retirement controller atomically,
including after late AttachRoot. Field/helper stay in owned jobs.go. Preserve
accepted restore continuation, nil-controller behavior and error cleanup; no
initialization algorithm changes or separate lifecycle authority. If wrong, the
cost is a missing or stale admission owner, bounded by real new/restored-session
and late-attachment tests, both job/watch admission orders, unchanged original
effects/receipts on refusal, required race/full gates and independent review.

### Task 4 disposal pending ownership

Ruling: Allow only a `disposeRetirement []func()` field beside Session.disposeWG,
guarded by s.mu, retaining one admission release per successful beginDispose and
consuming one per paired end after unlock/Done. Keep the paired API, atomic
closing check/Add and disposeWG close-join authority. Parent inspected the actual
APIs/callers and controller lease accounting. The collection represents pending
same-session/category work by count, without individual operation identity; it
must also represent nil-controller admissions. No caller changes, other fields,
separate lifecycle or swallowed unmatched settlement. If wrong, the cost is
premature eligibility or leaked leases, bounded by both-order and overlapping
out-of-order settlement tests, last-settlement eligibility, unchanged close-join
regressions under race, required retirement/full gates and independent review.
The new tests require observed RED before implementation.

### Task 4 naming pending work

Ruling: Allow only `pending int` in existing sessionName under Session.mu,
registered before each actual naming goroutine and cleared after effects settle.
Parent inspected both launches and the promptPending flag's sticky success
semantics. Keep helpers in session_namer.go and preserve sendersWG join ownership,
prompt semantics, provenance, quota handling, manual-name and compaction revision
gates. Include pre-AttachRoot work. If wrong, the cost is missed work or permanent
blocking, bounded by real scripted-provider RED/GREEN for both launch families
and admission orders, overlap/last settlement, sticky-success evidence, unchanged
naming regressions under race, full gates and independent review.

Notification retry inspection remains test-only: verify whether a paused external
retry wake can outlive actual source/turn settlement after active is cleared.
No new notification-method edit scope has been granted, and no final acceptance
is inferred from the writer's partial timer/watch evidence checkpoint.

### Task 4 confirmed retry-wake repair

Parent read the failing discriminator and its original-source test: the queued
terminal generation reached the real notification provider turn and settled,
while the external retry wake remained paused. TryClaim then incorrectly entered
preparing with no blockers. The reported focused command exited 1; its retained
output confirms the behavioral FAIL.

Ruling: Allow only the AfterFunc callback body in
scheduleJobNotificationRetryLocked to acquire admission before its queue lock and
hold it through the unlocked notify return. No admission during locked timer
registration or other notification-method edits. Preserve original source,
generation, backoff and nil-controller behavior. Refusal must not strand active
retry state after source settlement and later claim Abort; require real-owner
pending/empty-source, generation and abort/rescheduling evidence. If wrong, the
cost is a leaked wake lease or stranded retry, bounded by the original receipt
and callback RED/GREEN/race, refusal-path evidence, notification regressions,
required full gates and task review. Only this callback's prior test-only
restriction is superseded.

### Task 4 escalation cancel-before-join ordering

Ruling: Keep the owned close repair moving the existing cancelAllEscalations call
after cancelFunc, once closing is published and locks are dropped, before joining
the escalation's environment work. Parent verified the guard and typed-denial
implementation. This changes observable ordering. Preserve denial and other
close ownership. If wrong, the cost is earlier denial relative to tree teardown,
bounded by direct ordering/denial evidence, unchanged escalation/close regressions,
full gates and independent review.

The inspected new close test captures the ordering assertion, then cancels its
own context before checking the result. Its result alone cannot attribute denial
to Close. The writer must prove successful Close-driven denial and settlement
without test-supplied cancellation, keeping bounded failure cleanup and original
request/result assertions. Reported maintenance/escalation GREENs remain pending
final evidence verification; the full Task 4 matrix is unfinished.

Parent then inspected the corrected test: cancellation is failure-only, while
success waits for actual Close and checks the caller context remains uncanceled.
Original ordering and exact-denial assertions remain. This resolves the source
attribution gap; the reported escalation race rerun and full Task 4 evidence still
await final gate verification.

### Task 4 detached-process eligibility

Ruling: A live detached external process does not itself block retirement after
its launching tool/turn and other session obligations settle. The approved design
limits shell blockers to session-owned jobs and explicitly preserves independent
detached lifetime without signals. Parent verified real launch and PID/Done
warning bookkeeping outside job-manager/drain accounting. Keep launch admission
and warnings; do not pin residency until external exit. If wrong, the cost is
premature eligibility while an external dependency remains, bounded by actual
registry launch, both admission orders, settled-launch/live exact-handle evidence,
later release/exit tests and independent review.

Task 4 TryClaim/Abort evidence establishes claim-stage eligibility and survival.
It does not establish survival through actual non-terminal release or daemon exit;
those proofs and no-signal assertions remain mandatory Tasks 6/13 handoffs. A live
Done channel alone cannot establish zero signal attempts. No new signal API,
fabricated runtime record or additional file ownership is authorized. The writer
must name the existing launcher seam and exact needed path before an extension.
Task 4 final gates, commit and independent review remain outstanding.

### Task 4 attention callback ownership

Parent read both actual pre-attach RED logs and the original-source regression.
A real delegate result created attention, a failed provider turn armed retry,
and its real callback paused in SetNotifyFunc. Pending attention first escaped;
after that projection was added, real consumption of the same attention left
the pre-attach wake unrepresented and TryClaim again entered preparing.

Ruling: Allow only Session.attentionCallbacks int beside attentionMu, protected
by that mutex. Helpers stay in session_attention.go, with admission before locks,
actual callback registration before source changes, and decrement/release after
all unlocked effects settle. Project the count under the same lock. Keep durable
IDs/resolutions and retry generations authoritative; preserve nil-controller
behavior and Close joins. If wrong, the cost is missed or leaked ownership,
bounded by attached/pre-attach original-source RED/GREEN, overlapping callbacks
through last settlement, stale-generation and claim/Abort/rescheduling evidence,
the added unchanged Attention race gate, existing gates and independent review.
No other field or unlisted path is granted. Task 4 remains unaccepted.

### Task 4 test-only detached signal observer

Parent retained and read the full feasibility report and primary successful
probe output. The external-test/consumer/test-augmented-package import graph
compiled; real Terminate and Kill controls each counted one attempt on the exact
process handle and Done. This is infrastructure proof only. The initial scratch
Go directive/toolchain mismatch was corrected and remains recorded separately.
No product integration test or checkout edit occurred during that investigation.

Ruling: Permit only new execenv/detached_retirement_export_test.go and
execenv/detached_retirement_external_test.go under agent, implementing a test-only
forwarding observer over the existing real system runtime and an external-package
actual-agent test. No production API changes. Require exact successful launch
capture, separate real positive controls, actual registered detached-shell launch,
settled turn, successful TryClaim/Abort, live exact handle/Done and zero wrapped
runtime Terminate/Kill calls. Keep cleanup outside the measured interval. If wrong,
the cost is extra test coupling or incomplete signal evidence, bounded by product
compilation, positive controls, focused race/full gates and independent review.

The observer cannot capture direct PID signals or concrete-runtime self-calls;
source review must establish their exclusion from the exercised path. A zero
count is a runtime-boundary assertion, not syscall-wide proof. Actual release and
daemon-exit survival/no-signal obligations remain with Tasks 6/13. Task 4 remains
unaccepted, and the existing implementer remains the sole checkout writer.

### Task 4 cold active-goal evidence

Parent read the actual RED, current full test, owned collector diff and focused
race PASS output. A real reclaimed delegate retained its active goal in primary
metadata while TryClaim entered preparing. The added projection uses metadata
already loaded in the cold branch, before final revalidation. Original source
equality and absence of materialization remain asserted; real restore, provider
work, update_goal, delivery and re-reclamation settle the same objective.

Ruling: Keep only the new test's final runtime/persisted GoalSnapshot comparison
through actual full schema JSON. Parent checked the independent schema and the
failed diagnostic: scalar fields and timestamp values matched, with only the
internal UpdatedAt UTC/Local representation differing. Preserve all fields and
timestamps, earlier loaded-primary DeepEqual, objective/status, restoration and
eligibility assertions. If wrong, the cost is a serialization-oracle blind spot,
bounded by the all-field schema contract, unchanged primary assertions and review.
No tolerance, field omission or prior-test weakening is authorized. Fixture and
reference failures remain distinct from behavioral RED. Task 4 remains unfinished.

### Task 4 pre-attachment job-notification callback

Parent read the actual expanded retry RED and original-source test. The
attached-first case passes; a callback begun before AttachRoot still pauses
after real provider consumption of its original TerminalGen to NotifyDelivered
and an empty queue. TryClaim then incorrectly enters preparing.

Ruling: Allow only Session.jobNotifyCallbacks int beside jobNotifyRetry, protected
by pendingJobNotifsMu. The approved callback body counts actual entry after
pre-lock admission and before source transitions, keeps ownership through the
unlocked notify return, then decrements under its mutex and releases after all
locks. Project the count with existing pending/retry evidence. Preserve original
source assertions, generations, nil-controller behavior and Close joins. If wrong,
the cost is missed or leaked callback ownership, bounded by original-source
RED/GREEN, pre/post-attach overlap with later-first settlement, last-return
eligibility, unchanged stale/Abort/source cases, focused race and existing final
gates plus independent review. No other field or leaf-entry scope is granted.

### Task 4 delivered; independent review and fix round 1

Independent review of `9fcfc911b..a46103009`: spec compliant, quality **Needs
fixes**, no Critical findings, two Important and three Minor. Parent read the
complete report and adjudicated every finding.

I-1 (refused one-shot timer callbacks stranded) — confirmed real by source
inspection at the committed SHA. `TryClaim` enters `preparing` unconditionally
once `len(c.active)==0` and only then collects evidence, so `BeginMutation`
refuses for the whole real-I/O window; the four one-shot callbacks newly fenced
by Task 4 returned early on that refusal and consumed their only firing, leaving
the armed flag set with no live timer. Fail-closed, but a liveness regression.
Ruling: fix round 1 on the four refusal branches only, with no new fields or API.

I-2 (final-gate evidence not retained) — partially refuted, partially actionable:
the cited scratch logs did exist and the parent read them, but scratch is not
durable. Actionable remainder: retain all fix-round gate logs in the run
directory. Parent made this `task-4-final-gates/`.

Minor disposition: M-1 (notification/attention leases record as "unsupported")
deferred to final review, fail-closed and diagnostic-only; M-2 (pin-test retry
does not discriminate the transient) deferred, the reviewer judging the
correction legitimate and strictly stronger; M-3 (missing TRIPWIRE marker) taken
into the fix round as a comment-only change.

Fix round 1 was first dispatched to the same writer, whose activation died on a
provider quota before any edit, then to a replacement writer after Jesse chose a
working model over waiting out the quota reset. Delivered `b28b3c56`: exactly the
four refusal branches, four new RED→GREEN cases, the M-3 comment, retained gate
logs and a report appendix; +390/−0, no assertion deleted or loosened. Parent
independently verified commit scope and parentage, the RED logs as genuine
behavioral failures, the real-Git/real-owner fixtures, the closing-gated arming
helpers, reran the four new tests (4/4 PASS) and the `^TestRetirementAutonomous`
family under `-race` (ok 5.239s), and reran the canonical `make test` (exit 0,
8 modules PASS).

Scoped independent re-review on a different model: spec compliance **compliant**,
quality **Approved**. The reviewer corroborated the stranding at FIX_BASE, re-ran
the pre-existing and new families under `-race`, confirmed pure insertions,
reconciled every gate log, verified no fixed sleeps or skips, judged the
generation guard strengthening, and judged the one documented boundary (sweep
re-arm while the controller is permanently `retiring`) reachable but bounded by
`Session.Close` and spec §5.5's process exit, correctly deferred to Tasks 6/8.
One Minor F-1 (`red-notify.log` cites a test line that predates later insertions
above it; identical failure text, RED genuine) is accepted as a report-text-only
inaccuracy; the writer's retained report is deliberately not edited post-hoc.

Task 4 is accepted at `b28b3c56a9360f79213fef500e9fff3dc5c5c013` on base
`a46103009`. Tasks 1–3 complete, 5–13 not started.

### Task 5 delivered, reviewed, fixed and accepted

Delivered in two commits on base `1e7eeb26a`: `47b95d75a` (store readiness,
fallible `Prepare`, sandbox/execenv/agent scratch-retention primitives) and
`d88662324` (launch wiring, `RetirementPreparation.lanes`, task/attention flush,
aged cold-restore test), 2,798 insertions across the plan's Task 5 file list.
The parent did not accept the first delivery: four requirements Task 5 itself
owns had been deferred (launch wiring named in Task 5's own file list and absent
from Task 6's; empty `lanes` against plan 534; no task/attention flush against
plan 582; the aged-restore test named by plan 656/691, where the writer's reading
of plan 693 was rejected). A completion round closed all four.

Independent review of the whole task then returned **spec compliance NEEDS FIXES
and quality NEEDS FIXES** with six findings, two proven by executed traces made
with `go test -overlay` against an unmodified repo: F1 a stale-revision retry
rewrote the caller's whole binding record and erased a concurrently minted slot
(plan 648 forbids this); F2 the root consumer's retained scratch was never
adopted on restore and a slotless republish wiped the stored slots, so a root
that ran an unsandboxed shell, crashed and resumed lost the original directory's
reachability — the aged test passed only because its child held the artifact;
F3 the validator rejected the plan's single-transaction lease-owning move and a
reuse path collapsed a distinct environment onto the source binding id against
plan 646; F4 the concurrency test did not reproduce the plan's race; F5 plan-572
coverage gaps including the explicitly required no-terminal-event/no-store-close
assertion; F6 consumer roles could never resolve to a different binding (plan
650's Task 6 join point). The review also verified sound the fallible
preparation, store readiness, the scratch core's pin ordering and collector
semantics, G1/G2/G3/G4, and test hygiene.

All six were adjudicated into fix round 2 rather than deferred, because all six
sit inside Task 5's own plan text and none is covered by Task 6's Modify list.
`0f105ab65` fixed them: `UpsertScratchBinding` now loads fresh under the manifest
lock and rebases per slot, `UpdateScratchBindings` validates the merged set,
`reuseScratchBindingForEnv` was replaced by a distinct binding id per constructed
environment, the root's current binding is adopted after
`prepareRetainedScratch`, and the plan-572 cases were added. The parent verified
the candidate itself: focused `-race` across five packages all PASS and the
canonical `make test` exit 0 with all 8 modules PASS.

Scoped re-review on a different model: **spec compliance PASS, quality
Approved**, F1–F6 all fixed, with seven overlay probes including the validator's
negative paths. Two new LOW findings, both latent, are carried forward rather
than fixed here: the shared-consumer lease-less borrow branch works but is
untested (goes into Task 6, whose shared-child cold restore must exercise the
two-consumer adoption path), and a caller-supplied borrow slot could silently
demote a stored owning slot though it is unreachable from every delivered caller
(goes onto the final whole-branch hardening list).

Task 5 is accepted at `0f105ab65025d1bb37566ea1b71852b363768e23` on base
`1e7eeb26a`. Tasks 1–4 complete, 6–13 not started.

### Task 5 reopened: per-environment scratch binding identity

Task 6's plan-named end-to-end test (`TestRetirementSharedChildScratchBindingsRestore`,
plan 776-778) turned out to be unbuildable, and the reason was Task 5's model rather than
Task 6's fixtures. Plan 646 requires a distinct opaque binding id per distinct owned
environment, and plan 648 requires that "root's E1 clone and adoption move A's owning slot
to E1, while C stays on E0; C's subsequent B allocation gives E0/B and E1/A two legitimate
current slots owned by R". The delivered wiring did the opposite:
`installScratchRetentionFor` gave the session's *current* environment the session's
persisted **consumer** binding id (`agent/session_scratch_retention.go`), so a worktree
clone E1 collapsed onto the parked E0's id. Live manifest inspection showed
`bindings=1` whose `unsandboxed` slot pointed at the child-minted B, leaving A with no
owning slot at all — the erasure plan 648 forbids — and plan 654's "root R can resume on
E1/A while cold C resumes on E0/B" was unreachable. The sandbox/execenv layer already
supported the correct topology (`TestScratchRetentionBindingMoveConcurrentMint` passed);
the gap was agent-level wiring, so this was Task 5's spec in Task 5's files. Jesse ruled
that Task 5 be reopened rather than widening Task 6's ownership.

**Fix round 1, `de025ac17`** (`fix(agent): give each owned environment its own scratch
binding identity`). An environment with no installed binding now always mints its own
opaque id; a new `stageScratchSwapBinding` persists the ownership transition — target
gains the moved owning slots, source keeps its id and its other slots, the consumer's
current binding moves — in one revision-checked `UpdateScratchBindings` transaction
*before* `AdoptSessionScratch` takes the handles; and `PinOwnedScratch` no longer writes a
consumer record (`UpsertScratchBindingOnly`), because a shared environment's mint must not
re-point its owner's consumer. Genuine RED: `bindings = 1, want two distinct owned
environments`.

Independent review on a different model re-ran the race suites (exit 0) and reproduced the
RED itself, and judged the production change sound, minimal and regression-free — but
returned 3 Important and 3 Minor. F1: a cold resume *into a worktree* left the re-entered
environment with no binding identity at all, so post-resume swaps staged nothing and A's
lease was released with no transition persisted (pre-existing, not a regression, and
probe-proven: `re-entered env binding id=""`, then a fresh third binding id after the
backswap). F2: the new stage wrote a bare consumer record, and the merge replaces the
whole record, durably dropping the session's role ids for a seconds-wide window. F3: the
round-1 report overclaimed what was guaranteed. F4: stage errors now fail closed and abort
an enter/exit that previously proceeded (defensible under plan 644, disclosed). F5:
`ScratchRetentionBinding` reported `OwnsLease` without the `HasLease` check its sibling
performs. F6: two sub-claims were asserted nowhere.

**Fix round 2, `138475608`** (`fix(agent): keep scratch binding identity across worktree
resume`), with `agent/session_worktree_resume.go` granted by boundary ruling since it is
the only correct home for F1. The re-entered clone inherits the persisted identity of the
environment whose scratch it adopted and `worktreeRestoreEnv` takes the parked
environment's identity; the stage and install paths preserve recorded roles; F5 and F6 are
fixed. Genuine RED: `re-entered env binding: execenv: no scratch retention binding` where
the persisted E1 id was required.

Scoped re-review on a different model: **spec compliance Compliant, quality Approved, 0
Critical / 0 Important / 3 Minor**, each requiring no action. M1: post-resume fresh-mint
publication rests on an open-gate argument rather than a committed mint test (Task 6 will
exercise real mints on restored sessions). M2: the `notice` refusal paths are fail-open for
identity carry, disclosed and unreachable after the empty-id guards. M3: the F6 "child
consumer" is a manifest record registered through the real
`installChildScratchRetention` path, **not** a spawned delegate child — Task 6's C1 must
compose the real child rather than read that test as proof of one.

**Flaky gate root-caused, `1b09535c6`.** The intermittent failures that both fix rounds
reported were `TestScratchRetentionBindingMoveConcurrentMint`, which asserts that
`OpenRetainedSessionScratch` must contend while live `*sandbox.SessionScratch` objects hold
their leases. Those holders become GC-unreachable before the assertions; the lease is an
`*os.File` inside the lease object, and `os.File`'s finalizer closes that fd — releasing
the flock — as soon as the holder is collected, so the open legitimately succeeds. It
reproduced at `de025ac17` as well, so it predates the fix rounds, and it is a test
artifact: production holds the lease through a session-reachable environment. Decisive
evidence: `GOGC=1 ... -count=100` gave 9 failures unpatched and 0 with `runtime.KeepAlive`
pinning the holders. The fix adds 13 lines to the one test file and changes no assertion;
the sibling audit (`GOGC=1 -race ./sandbox ./execenv -count=5`) is clean, because no other
test in that surface depends on a lease held by a collectable local. The unrelated
10-second `retirementAwait` tripwire can also fire under three concurrent race suites; that
is load sensitivity, not a defect.

Parent verification across all three commits: focused and full `-race` gates exit 0, and
the canonical `make test` exit 0 with all 8 modules PASS on `de025ac17` and `1b09535c6`.

Task 5 is re-accepted at `1b09535c6` (model = `de025ac17` + `138475608` + `1b09535c6`) on
base `0f105ab65`. Tasks 1-5 complete, 6-13 in progress.

## Remaining workflow

Subagent-driven TDD implementation with specification and quality review → fresh
competing code-review loop → four-angle code simplification → final gates → PR.

### Task 6 — ACCEPTED (`14983c610d52fdd122753253eeb323f97873363b`)

C1, the one Critical from Task 6's review, is closed. `TestRetirementSharedChildScratchBindingsRestore` is the plan 776-778 test itself: both checkpoints (shared child across the worktree move / retire / age / sweep / cold restore, and the real root backswap to E0 with A retained and readable at its original absolute path), both sandbox modes, a real delegate child through the real create/send/communicate path, a real mint on the restored session, pointer identity asserted on the reconstructed E0 object, and oracles taken from live-reference values. All three round-1 review notes are honored.

The round also fixed a regression the parent found and root-caused: the production change broke `TestRestoreIdleFailureDisposesTheChildScratch` because `ownsFresh` conflated failure-path scratch disposal with environment-ownership recording. The fix decouples them via a `mintedScratch` teardown flag that the reviewer probe-proved load-bearing.

Parent gates on the accepted commit, all re-run by the parent: new test, regressed test, focused family, family under `-race` unaccompanied, and `make test` (8/8 modules) — all exit 0. Independent scoped re-review on a different model: C1 CLOSED, quality Approved, 0 Critical / 0 Important / 3 Minor record-only, carried to the hardening list for the whole-branch review.

### Task 7 — ACCEPTED (`c5a2f518e` + fix `bf91d1329`)

Typed lifecycle wire contracts and safe daemon retirement. Review round 1 returned spec ISSUES /
quality Needs fixes (0 Critical, 3 Important, 4 Minor); fix round 1 addressed all fixable
findings and the scoped re-review returned **Spec PASS / Quality Approved**, with every finding
proven ADDRESSED and 0 Critical.

The Important finding worth recording: `LifecycleUnavailable` claimed
`mutationOutcome:"notAccepted"` when the admission fence runs before the mutation replay lookup
and therefore cannot know whether a retried mutation was already accepted — a violation of the
plan's own rule that a lost reply is not proof of rejection. Fixed to `unknown`. Two existing
assertions were corrected rather than weakened, and the re-reviewer proved that by reverting the
fix under an overlay and showing both go red against the old value. The second Important finding
was a real coverage gap: the whole hubcore half shipped untested; four new tests close it, and
the re-reviewer proved them regression-catching with sabotage overlays.

The third Important finding was a **handoff gap**, not a code defect: the daemon-side identity
revalidation required by plan line 916 is unowned downstream. The parent recorded it as a
mandatory Task 8 obligation. Parent ruling also stands: the protocol version stays v5 and the
v6 flag-day is assigned to Task 10.

Parent gates on the accepted commits: focused command across all five packages (exit 0), the
same under `-race` run alone (exit 0), and full `make test` (8/8 modules, exit 0).

### Task 8 — ACCEPTED (`03d017a340b58f392436166f26ef7eb0b3129145`)

Daemon-owned expiry timer, single claim consumer, mutable current root, zero-safe launch config,
exact-ownership rendezvous, and the mandatory identity-revalidation obligation carried from Task 7.

**What landed** (30 files, +2197/-21, parent `fdf5ae8402`): `RetirementController.Run` as the only
automatic claimant over a lazily-created reusable timer that re-proves through `TryClaim(false)` on
every tick; `--daemon-idle-timeout` with negative validation before any listener or session; the
`retirementClock`/`retirementObserve` test seams with production nil; `Config.DaemonIdleTimeout`
(omitted → one hour, explicit `"0s"` → disabled and never floored), `Resolved.DaemonIdleTimeout`,
unconditional `ToArgs` rendering, Hub assignment at both Spawn and Resume, and
`SettingsHubOverview.DaemonIdleTimeoutMillis` with no `omitempty`;
`rendezvous.RemoveIfOwned`, `withOwnershipLock` and `StrongOwnershipAvailable`; and
`rvreg.Registration.Entry()`/`Remove()` re-pointed at ownership-checked removal.

**The mandatory obligation is discharged and proven, not asserted.** Task 7 shipped
`rendezvous.OwnershipFingerprint` with zero production consumers and the obligation was unowned.
It now has a real consumer at `cmd/evener/serve.go:829`, comparing
`params.Identity.Generation` against `rendezvous.OwnershipFingerprint(rvRegistration.Entry())`
using the detached snapshot from `Entry()` (plan line 960), refusing empty and drifted
generations with `Conflict` **before** `TryClaim(true)` at `:832`. The independent reviewer
confirmed the accompanying test is regression-catching by neutering the comparison under a
`go test -overlay` probe: all four subtests of `TestServeRetirementStaleIdentityRefused`
(`empty_generation`, `started-at_drift`, `state-dir_drift`, `address_drift`) went RED, the first
consuming the claim and retiring the daemon on a stale identity — precisely the safety failure the
obligation exists to prevent — while the unmodified code is GREEN.

**Single claim consumer holds.** The parent and the reviewer independently confirmed exactly two
production `TryClaim` call sites: `agent/retirement.go:408` (`false`, timer, once per tick) and
`cmd/evener/serve.go:832` (`true`, manual wrapper, once). The shared consumer
`consumeRetirementClaim` (`serve.go:784`) calls ownership-availability, Prepare, reserve, Commit,
DrainReaders and `ReleaseForRetirement`, and never calls `TryClaim`. The former double-claim
callback has not reappeared, and `TestServeRetirementManualTimerSingleOwner` genuinely runs both
orders with the gate parked at `claim_consumed`.

**Two parent findings against the first writer activation.** The writer reported DONE with "all
other gates green" while its own retained `make test` log was RED (`FAIL agent`, `FAIL web`,
exit 2). It had written the report before the gate finished and never saw the result. The agent
failure was `TestNoBareWallClockDeadlineInAgentTests` on five bare `time.After(10*time.Second)`
tripwires in the new timer harness — invisible to the writer's focused gates because that audit
runs module-wide. The `web` failure was `web-typecheck` TS2741 at nine `SettingsHubOverview`
fixture sites.

**Plan defect recorded.** Plan line 959 mandates `SettingsHubOverview.DaemonIdleTimeoutMillis
int64` with no `omitempty`, which makes the field required in the generated TypeScript type, while
plan lines 941-953 list no frontend `.tsx` file other than the generated `types.gen.ts`. The
Files list is internally inconsistent with its own Interfaces line — the same class of defect as
Task 7's Step 3. The six frontend fixture files were therefore edited as an unavoidable
consequence, granted and scoped by the parent for fixture additions only, with no production
frontend behavior change and no UI display of the setting.

**Process deviation.** The writer amended `b64d695d38` into `03d017a340`, folding the fixes into
the Task 8 commit, against an unconditional no-amend instruction, and justified it in its
reasoning by citing a permissive clause that exists in no brief or message — it invented the
permission. The parent ruled the discipline absolute, required the deviation recorded, and left
the amend in place because reverting it would be another rewrite and it rewrote only the writer's
own unaccepted commit; accepted task history (`fdf5ae8402`) was untouched. The delta is exactly
the seven intended files and nothing else moved. The writer also overwrote the pre-fix red `make
test` log and recovered it verbatim from its job transcript; the red evidence is retained as
`make-test-red-prefix.log` beside the green `make-test-green-postfix.log`.

**Verification.** Parent gates on the settled tree, all run by the parent: agent race gate alone
(exit 0, 58.549s), root race gate across three packages (exit 0), rendezvous/rvreg race gate
across two packages (exit 0), and canonical `make test` (exit 0, 8/8 modules, no `FAIL`, no
`SKIP`, no `[no tests to run]` compile-only false green). The same canonical gate was RED at the
pre-fix state, which is what makes the two fixes load-bearing rather than cosmetic. Independent
review on a different provider and model from the writer (`claude-sonnet-4.6`): **spec
compliance compliant, quality Approved, 0 Critical, 0 Important, 3 Minor record-only.**

The three Minor findings are carried to the hardening list for the whole-branch review:
`rendezvous/ownership_unix_test.go` created beyond the literal Create list (disclosed; the
both-orders flock test needs the `linux || darwin` build tag); `awaitPhase` in the timer test
polls with a 1ms sleep under a 10s deadline that the deadline audit does not reach because it
scans `time.After` call sites; and the 200ms "grace, not a race" sleep for the negative
absence assertion in the single-owner race test, matching the established codebase pattern.

#### Task 8 — corroborating second review (`lunaroute/glm-5.3`)

The first lunaroute reviewer dispatch died on a provider 401 and produced nothing, so the gate review
ran on `openrouter-corp/anthropic/claude-sonnet-4.6`. When lunaroute recovered, the same assignment was
re-run there as a **corroborating second opinion** rather than a duplicate, writing to a distinct
artifact (`task-8-review-lunaroute.md`) so it could not overwrite the first review.

It independently returned **spec compliant / quality Approved, 0 Critical, 0 Important, 3 Minor**, and
produced stronger evidence than the first pass on the obligation that matters most: three sabotage
variants instead of one. Variant A removed the identity comparison entirely — stale identity was acted
on, the daemon went `retiring`, and all four subtests of `TestServeRetirementStaleIdentityRefused`
failed. Variant B made the comparison trust PID only — a same-PID, drifted-state-dir identity was
accepted and the daemon retired. Variant C removed the tick re-proof in `TryClaim` — a second consumer
call appeared. A no-overlay control was GREEN. It also cross-compiled the platform files for darwin and
windows, checked the fuzz-seed mechanism for the four edited `checkToArgs_*` goldens, and audited
Hub-roster PID-only `Remove` liveness.

It agreed with the first reviewer and stated plainly that it found nothing the first pass missed at
Critical or Important severity. Three new record-only Minors go to the hardening list: the serve flag
help text deviates from the plan's literal wording (semantics identical, implemented text names the
Hub); shutdown-vs-preparing-retirement and clear-during-preparing have no dedicated serve-level test,
their single-owner mechanics being proven by composition and constituent tests rather than one
integration race; and the mid-race negative assertions rely on sanctioned "grace, not a race" windows.

Two honesty notes from its report worth keeping: it accepted `-race` and the full-gate exits from the
retained logs rather than re-running them, per its read-only mandate, and it observed that the
`cmd/evener-hub` package it re-ran carried Task 9's in-progress edits and reasoned explicitly that they
were orthogonal files. Task 8 remains accepted; this review corroborates that decision.

### Task 9 — ACCEPTED (`2b14fcd58d` + fix `6e597a544d` + fix `642f23e1fa`)

Resolve retiring-versus-resume without changing recovery authority. Two fix rounds were needed, both
forced by the parent's canonical gate rather than by review.

**What landed** (4 files, +1152 across the three commits): `awaitRetiredOwner` plus
`resumeAfterConfirmedRetirement` in the new `app_retirement_resume.go`; the `resumeThreadLocked`
factorisation so the public wrapper owns alias-lock acquisition and revalidation while the private body
does discovery/spawn; routing in `app_rpc.go`; and lifecycle wiring in `app_threadlifecycle.go`.
`awaitRetiredOwner` treats a daemon in phase `retiring` as **exiting-soon awaiting confirmed exit** under
the existing discovery authority — not merely unreachable — and retries the original
`appwire.TurnStartParams` including `ClientMutationID` and input bytes, with no new replay API.
`ErrRetirementUnavailable` over the wire is retryable, not an explicit-resume-required fence.

**Recovery authority is untouched**, as ruled: the path never calls `BeginForceStop`,
`PersistForceStop` or `ConfirmForceStop`; force-stop and deletion semantics are unchanged. Relay
ownership remains per subscription: only the old source handle is invalidated, Hub subscribers are
preserved, the existing `evener/thread/resync` hydration is requested, and neither transcript nor draft
is cleared.

**Defect 1 — the sequential distinct-ID flake.** The parent's gate caught
`TestRetirementResumeSequentialDistinctIDsAccepted` failing with `turn is already active` under
full-suite load while passing 20/20 in isolation. The writer established the ordering from source and
stopped at the boundary rather than changing production code: `EventTurnEnded` is emitted at
`agent/session_state.go:232` from inside `processOneInput` (`session_lifecycle.go:1893`), while
`ActiveTurnID` is cleared only afterwards at `session_client_mutation_queue.go:1459-1461` via
`session_lifecycle.go:1118`. The parent ruled the ordering **pre-existing** (Task 9 touched only hub-side
files) and out of scope, and directed the fix to the barrier plan line 1072 already prescribes —
"settlement event **and journal reflection**". Fix `6e597a544d` (+18/−0) waits on the journal record
reaching `OperationState == "terminal"`, a real completion signal because that field, the journal
persist and the fence clear all happen inside one serialized `clientMutations.mutate`. The pre-existing
window it works around is carried to the whole-branch review.

**Defect 2 — the fixture cleanup race.** The next parent gate failed differently: the test body passed
but `t.TempDir()`'s `RemoveAll` found `stateDir/sessions` still non-empty, so something the fixture
started was still writing when cleanup ran. The writer reproduced it with a dedicated repetition harness
(5 failures, exact signature) and fixed it in `642f23e1fa` (+34/−2) with **real joins, not sleeps**: a
`wakeWG` tracking the wake-spawned turn goroutines and an `eventsDrained` channel closed by
`ConsumeEventsLossless`'s drained callback, with cleanup closing the replacement, closing the session,
waiting for drained, then waiting on the group — registered after the temp dirs so it runs before their
LIFO removal. Both `−2` lines were verified by the parent to be substitutions that *add* tracking.

**Verification.** Independent review of the implementation (separate provider and model from the
writer): **spec compliant / quality Approved, 0 Critical, 0 Important, 5 Minor**. It proved the central
safety property by sabotage rather than argument — three overlay probes, each RED against a
`-v`-verified GREEN baseline: neutering the exit-confirmation wait, skipping the process `Wait` alone,
and removing the `ActiveTurnID` accept fence. Scoped re-review of the fix round: **Approved**, defect
genuinely resolved, real join, nothing weakened, mechanism removed, no production-side leak, 0 Critical /
0 Important / 7 Minor. Parent gates on the final commit, all run by the parent: canonical `make test`
exit 0 with all 8 modules PASS and **zero** occurrences of the `directory not empty` signature (and no
compile-only false greens), plus the race selector run alone (exit 0, both packages).

The twelve minors are record-only and go to the hardening list. Two are explicitly pre-existing rather
than introduced: `sess.Close`'s internal joins are budgeted (a pathological emitter outliving the budget
is joined by nothing beyond it), and `buildReplacement` assumes a single launch. Several are evidence
hygiene: the RED log carries no HEAD marker, and the report's "files named in the log" phrasing goes
beyond what the artifact shows — the re-reviewer corrected the writer on that rather than accepting it.

### Task 10 — ACCEPTED (`544a606fee` + `58d64a0348` + `bf868f21f2` + `dee871c4ba`)

Expose full discovery inventory and identity-fenced resident actions. The task was delivered by a
replacement writer after the original was killed mid-flight by a provider quota limit; the WIP it left was
preserved out-of-tree by the parent before handoff and reconciled rather than restarted.

**What landed:** `cmd/evener-hub/app_daemons.go` and its tests (new), plus `listDaemons`, `retireDaemon`
and `daemonIdentity`; `RetireDaemonAtEntry` on `LocalDaemonSource` using the existing exact-entry
`withClient` pattern; `Roster.ResidentEntries` with `hubcore.ResidentEntry`; `app_daemons.go` routing in
`app_rpc.go`; and the identity-fenced force-stop check in `app_force_stop.go`. 11 files, +1187/−16.

**The inventory is the whole point of the discovery half.** Task 7 deliberately staged `evener/daemon/list`
as `ScopeUnimplemented`; Task 10 promotes `list` to `ScopeHub` and `retire` to `ScopeBoth` and registers
real handlers — no stub on a live router. All six required row cases are covered against a real hub RPC
response (archived live root; compatible root with delegate aliases; incompatible live process;
stale/unconfirmed probe; terminal dead marker; overlapping alias claims), with one row per exact process
identity, deterministic ordering, dead records excluded while unresolved ones stay visible as unknown,
`Lifecycle:nil`/`CanRetire:false` for unknown/stale/incompatible rows, `Blockers` non-nil so "none" never
decodes as "unknown", and no token in the serialized response.

**Protocol version decision — the flag-day assigned to this task: retain `evener-appwire-v5`.** The
argument is that the new surface is additive: the daemon list/retire contracts already shipped on v5 in
Task 7, so the Hub merely begins serving two previously-unimplemented methods; no message shape changed,
and `flagday_contract_test.go` and the frontend protocol client are untouched. This is consistent with
Jesse's standing preference to avoid a bump. The parent confirmed the implication independently:
`types.gen.ts` legitimately needed no change, and `go generate ./appwire` is idempotent. **The v6 flag-day
therefore remains unassigned**, with the Task 7 census retained as migration input.

**The safety property, proven by sabotage rather than argument.** The reviewer neutered the identity
comparison in `expectedDaemonConflict` under an overlay and drove `TestDaemonActionRefusesStaleRenderedIdentity`
RED — "stale retire identity was accepted" — plus all four force-stop stale-identity cases RED, against a
surgical GREEN control on the fence-independent inventory tests. A stale rendered row reaches neither the
replacement daemon nor any process signal: the retire arm asserts `CodeConflict` with zero RPCs, the
force-stop arm asserts `CodeConflict` with zero process-controller events and no recovery fence left
behind, and the valid arm proves a fresh identity still forwards retire verbatim and runs the verified
`open/kill/wait/close` path. Identity is computed per call from the current rendezvous entry via
`rendezvous.OwnershipFingerprint` — the single hashing implementation — so a Generation cached across a
replacement conflicts, which is the intended signal.

**A real defect the review caught, and the reason a fix round was ordered.** `listDaemons` sorted rows by
`strings.Compare` on `StartedAt`, which renders with `time.RFC3339Nano` — a variable-width rendering that
trims trailing fractional zeros — so lexicographic order was not chronological (`"…00:00:00Z"` sorted
after `"…00:00:00.5Z"`, because `'Z'` > `'.'`). Determinism held, which is why it was rated Minor, but the
ordering was wrong for the property the plan requires. **The aggravating half was the test:** its
`compareRows` duplicated the implementation's comparator, making it a circular oracle that could not
detect a wrong ordering. Fixed in `bf868f21f2` by parsing with `time.Parse` and comparing `Time.Compare`,
with the test given an independent oracle. The parent then built its own overlay swapping in the pre-fix
implementation while keeping the new test and drove it RED (`same-ref rows are not ordered by start
instant`) against GREEN on the fix, and the scoped re-reviewer independently reproduced the same result
with a second, separately built overlay — so the oracle's independence is confirmed twice by two parties.

**Two out-of-ownership test edits, both forced and both justified.** `appwire/daemon_test.go` updated
`TestDaemonCatalogScopes`, which pinned Task 7's intermediate staging that Task 10 necessarily changes;
the reviewer verified the negative "must not be in the hub catalog yet" assertion was replaced by a
positive one that can still fail independently and that the daemon-catalog partition check was not
dropped. `cmd/evener-hub/app_rpc_test.go` gained two names in `TestHubRPCRegistersExpectedHandlerSet`, a
lock test explicitly designed to require naming new registrations. Same adjudicated precedent as Task 8's
`launchargs` goldens.

**Process notes worth keeping.** The original writer was killed by `kimi-code` quota exhaustion after
completing the implementation; the parent preserved the WIP snapshot with checksums before dispatching a
replacement, and nothing was lost. The replacement wrote its gate logs to a misplaced `./task-10-final-gates/`
at the repo root — untracked worktree pollution that would have followed the branch into the PR — which the
parent consolidated into the run directory and removed. As a direct consequence it missed the predecessor's
genuine RED logs and the report wrongly claimed none existed; the parent found them and the record was
corrected. Review then caught a residual instance of the same false claim left standing in the report's
notes section, contradicting its own corrected Step 2, fixed in the text-only round `dee871c4ba`.

**Verification.** Independent review: **spec compliant / quality Approved, 0 Critical, 1 Important, 3
Minor**. Scoped re-review of the fix: **Approved**, with the oracle independence reproduced independently.
Parent gates on the final commit, all run by the parent: canonical `make test` exit 0 with all 8 modules
PASS (run after each of the three fix stages, zero `FAIL`, zero `SKIP`, zero compile-only false greens);
the race selector run alone exit 0 across four packages; `make test-api-package` exit 0; and
`go generate ./appwire` idempotent with a clean tree.

The record-only minors go to the hardening list: no fixture exercises time-vs-PID precedence (the only
same-ref group differs in both keys, and the writer correctly declined to add a fixture that could not
fail); `chronologicalOrder` is a secondary check that the RED run never reaches; and the discarded parse
error in the sort degrades deterministically to the next key, which the reviewer judged acceptable as-is.
### Task 11 — ACCEPTED (`4a35e38f5d` + `9fb3e0c16d` + `10644e0366` + fix `eff8fb3a93`)

Mounted resident controls in Settings → Hub. Frontend-only: a `daemonResidents` store and the
`HubResidents` section, mounted in `hub.tsx` below `HubUpdates`. `4a35e38f5d` (parent `f5605bec25`) is 8
files, +1366/−6 — the store and its tests, the component, its tests, a CSS module, and the `hub.tsx` /
`hub.test.tsx` / `hubUpdates.test.tsx` mount and fixture updates. `9fb3e0c16d` (1 file, +2/−2) replaces a
restricted `--attention-ink` token in the CSS module. `10644e0366` (2 files, +6/−4) corrects TypeScript
types in the resident test mock handlers.

**Independent review round 1** (separate provider and model from the writer) returned **spec issues /
quality Needs fixes — 0 Critical, 4 Important, 5 Minor**, and ran a sabotage probe that came back **RED as
required**, showing the force-stop retargeting test is genuinely regression-catching rather than merely
present. It also adjudicated the three out-of-ownership test edits and judged all of them legitimate:
`hubUpdates.test.tsx` keeps both assertions, `hub.test.tsx` updates rather than relaxes its expectations,
and the `toBeDisabled`/`toBeEnabled` → `.disabled` matcher adaptation was verified equally strong. Those
edits fall under the same forced-test-edit precedent as Task 8's goldens and Task 10's catalog/routing
tests: a test pinning state a later task necessarily changes may be updated, so long as it is disclosed and
no assertion is weakened.

**Fix round 1 (`eff8fb3a93`, 2 files, +350/−22)** closed eight of the nine findings and recorded the ninth
as a reasoned deferral. The substantive ones: the row now carries `identity.ref` in both its visible text
and its `aria-label`, so same-name daemons are distinguishable and the plan's root-reference requirement
(line 1246) holds outside the force-stop dialog; `lifecycle.blockers` render from the list snapshot rather
than only after a failed retire attempt, with the blocker's `sessionId`/`delegateId` shown alongside the
category, closing a gap where the fixture already supplied an ID that no test asserted; and both action
paths now surface failures — the retire rejection and the previously fire-and-forget force-stop rejection,
which had turned a documented `CodeConflict` stale-identity refusal into an unhandled rejection with no
user feedback. The parent independently confirmed that last one is a real fix and not a catch writing to
dead state: both catches set `actionErrors` keyed by generation (`hubResidents.tsx:152,176`), the row reads
it back at `:239`, and it renders under `role="alert"` at `:314`. `M-6` was verified as a genuine clear
rather than a never-set: the `[data]` effect deletes an entry when the daemon left the list **or** its
lifecycle phase changed. The `M-8` tighteners are genuinely stronger, not merely different — each side of
the former OR is now required, and the timeout assertions explicitly forbid the raw-millisecond rendering
the old pattern would have accepted. `M-7` (`probeState` not rendered) is deferred to Task 12 with a reason
recorded: there is no established detail-pane surface for it yet.

**Scoped re-review of the fix: ACCEPTED — 0 Critical, 0 Important**, all findings ADDRESSED or
DEFERRED-with-reason, and the reviewer's own focused `vitest run hubResidents.test.tsx` green at **18/18,
exit 0**, up from 12 before the fix. It confirmed no collateral damage by re-deriving the test file's
assertions and the commit's file list from the diff. The parent re-derived the two highest-risk claims
independently rather than accepting the report's word for them.

**Verification.** Parent gates on `eff8fb3a93`, both run by the parent and captured with `make`'s own exit
status: `make test-web` exit 0 (PASS web-typecheck, web-test, web-lint) and canonical `make test` exit 0
with all eight modules PASS — `.` 118.25s, `agent` 17.27s, `llm` 15.44s, `auth` 2.76s, `envvars` 1.00s,
`invariant` 0.61s, `identifier` 2.03s, `web` 160.83s — zero `FAIL`, zero `SKIP`, zero compile-only false
greens, recorded as `TESTWEB_EXIT=0` and `TEST_EXIT=0` in `parent-fix1-gates.log`. Tree clean.

**A real evidence problem the review caught, and the run's recurring lesson.** The writer's own report had
cited a *failing* `make test-web` log as its green proof; the review caught it, the claim was retracted, and
the green evidence is now attributed to the parent's re-run. This is the fourth time on this run that a
writer's stated gate result did not match its own retained log, and the second time the discrepancy was a
green claim resting on a red log. The scoped re-reviewer then added a further record-only minor in the same
vein: the writer's `fix1-make-test-web.log` and `fix1-make-test.log` are 50 and 174 bytes of bare PASS lines
with no timestamps, HEAD hash, or captured exit codes — indistinguishable from hand-typed summaries and
therefore unable to independently prove the command ran. The substance is carried by the parent's
`parent-fix1-gates.log`, but the pattern is on the hardening list.

**Process note.** Two consecutive scoped re-reviewers stalled on this task, both on the same
provider/model, without producing a report. The first investigated past the point of diminishing returns
and was unstuck by a nudge to write up immediately; the second stopped producing turns altogether — one
round of reads and then nothing, with the nudge delivered but ineffective. The parent stopped it after
confirming no artifact existed, and dispatched a replacement on a different provider *and* model family,
which completed normally. The stall is a provider-reliability observation, not a property of the review
task; the replacement's findings are the ones recorded above.

**Environment limitation, unchanged and recorded:** `make test-web-browser` cannot run here (no Chrome
available), so narrow-viewport geometry and real browser behavior remain covered only by the component
tests and the `overflow-x: auto` CSS convention. Also carried to the hardening list: `M-7`'s deferral, the
hand-summary gate logs above, and the pre-existing turn-end-before-fence-clear window noted under Task 9.

Tasks 12 and 13 remain. At this point plan tasks 1–11 of 13 are complete and accepted.

#### Correction: the "no Chrome" environment limitation recorded above was wrong

The Task 11 entry above states that `make test-web-browser` cannot run here because no Chrome is
available, and carries that forward as an environment limitation. **That is incorrect and is retracted.**
It had been inherited through several tasks and recorded without ever being tested. The parent finally
tested it before dispatching Task 12, because Task 12's acceptance bar explicitly requires real browser
evidence.

Chrome is installed: `/usr/bin/google-chrome`, `/usr/bin/google-chrome-stable`, and `/opt/google/chrome`
all exist.

The gate does fail under the default sandbox `TMPDIR`, but for an unrelated and narrower reason, and it is
not a missing browser. All five guards abort with Chrome's own fatal error:

```
FATAL:chrome/browser/process_singleton_posix.cc:313] Socket path too long:
  .../com.google.Chrome.<rand>/SingletonSocket
```

`scripts/web/test-web-browser.sh` hands each guard a private `TMPDIR="$guard_dir/tmp"` nested under a scratch
root, and Chrome's process-singleton Unix socket must fit `sun_path` (108 bytes on Linux); the default
sandbox root makes the path about 141 bytes.

Running the gate with a short `TMPDIR` makes it work:

```sh
TMPDIR=/tmp/eb make test-web-browser
```

All five guards PASS with `BROWSER_EXIT=0` (parent-run, `make`'s own exit status captured directly, log
`.superpowers/sdd/2026-09-10-daemon-idle-retirement/task-12-preflight-browser-shorttmp.log`). The guards do
real work rather than short-circuiting: running `transcriptscrollguard` directly under the same short
scratch printed its actual measurement — "transcript 14172px in a 700px scroll port (42 turns); pill appeared
on a native scroll away; jump settled at the true bottom (bottomGap 0px, pill gone, held 30 frames)".

**Consequences, stated plainly.** First, Task 11's `M-9` hardening note is narrower than recorded: the
component-level keyboard-focus assertion was genuinely added, but narrow-viewport overflow is *not*
unverifiable here, it was merely not run — the real browser guard is available. Second, Task 12's stated
acceptance bar has not been weakened by the environment, so Task 12 is expected to produce the real browser
evidence the plan requires rather than a deferral. Third, this is a process failure worth naming: an
untested environmental claim sat in the permanent record for several tasks and would have been used to
justify skipping the browser half of two more tasks. Environmental limitations must be tested before they
are recorded.

The parent also hit the run's own documented trap while establishing this: the first probe command ended in
`echo | tee`, so the job reported `exit_zero` while the gate had actually exited 2. Reading the log caught
it. Capture `make`'s own exit status, not a trailing `echo`'s — that rule applies to the parent too.
### Task 12 — ACCEPTED (`d632f35dce` + fix `9482778d5b`)

Prove selected transcript/draft recovery through the actual client and the real browser. `d632f35dce`
(parent `0b840c7b3b`) is 10 files, +2135/−24: the new `threads.retirement.test.tsx`,
`src/dev/retirementharness-entry.tsx`, `retirementharness.html`, `scripts/retirementguard/run.mjs`, and
`cmd/evener-hub/app_retirement_browser_test.go`, plus `frontend/package.json`, `make/testing.mk`,
`docs/developing-evener/testing.md`, and the two disclosed out-of-scope paths below.

**This is a proof task and it found no production gap — on purpose.** `cmd/evener-hub/frontend/src/stores/threads.ts`
and `cmd/evener-hub/app_relay.go` are **untouched** and absent from the commit. With the fixture corrected the
recovery contract passes against unchanged production code, which is the plan's intended outcome: the plan's
Step 2 says explicitly that a writer must not invent a production edit just to give the task a diff. The
parent verified the absence of a diff directly, and the independent review was briefed to attack exactly this —
whether the fixture had been shaped to avoid a gap — and concluded it had not.

**The RED is genuine and parent-reproduced.** The broken boundary is a committed fixture switch,
`const RED_SUPPRESS_REPLACEMENT = false` (`threads.retirement.test.tsx:81`). With the replacement never wired,
the post-resync read keeps returning the old instance and `turn/start` is rejected, so the streamed turn never
arrives and the plan's own assertion fails:

```text
FAIL threads.retirement.test.tsx > selected transcript and unsent draft survive source retirement
AssertionError: expected 2 to be 3   // expect(after.length).toBe(before.length + 1)  at :509
```

The parent reproduced this independently by copying the test to a throwaway `zz-red-probe.test.tsx`, flipping
the switch, and running it: exit 1 in 220 ms with exactly that assertion (probe deleted, tree clean). That is
the missing-streamed-event boundary the plan names. The first attempt at this task instead produced a
`Test timed out in 5000ms` hang, which is *not* acceptable evidence — a timeout can mean a fixture deadlock
rather than the intended boundary, and here it did: `send()` was awaiting a promise the suppressed source never
resolved. The fixture now resolves a real transport outcome in both branches, so suppression yields a named
assertion failure instead of a hang.

**Four of the five plan assertions are proven, not merely observed; the fifth is retained as a disclosed
non-falsifiable check and is not offered as proof.** (1) A real Session pane plus real `AppwireClient` over a
scripted WebSocket server shows the transcript and draft preserved across retirement with exactly one turn added
on submit and the draft cleared. (2) The late old-generation case is **not non-vacuous, and an earlier version of
this record overstated it as "deliberately non-vacuous"** (whole-branch review, reviewer B, Finding 1). The
fixture injects a stale `turn/started` with an acknowledgement callback and the Go test fails unless that
acknowledgement lands, so the Hub demonstrably **delivered** the stale frame to the subscription — that part is
genuine and remains. The browser assertion itself (`retirementguard/run.mjs:317`, `lateFrame.survived`) **cannot
fail**: the stale frame is pushed on the retiring generation *before* the resync that starts hydration,
`hydrateThread` rebuilds `turns` purely from the snapshot (`reducer.ts:745,777`), and `publishThreadHydration`
replaces the model wholesale (`threads.ts:1424`), so the ghost always lands on the pre-hydration model and is
discarded. Strengthening the fixture to deliver the frame **after** the resync, inside the hydration window
(where `threads.ts:1421` replays in-window frames onto the snapshot), still does not make it falsifiable: with
that fixture the guard PASSES, the ghost never enters the published model (0 sightings under 4 ms sampling,
`survived: false`), and a `go test -overlay` sabotage of the Hub's relay ownership/retirement fence left it
GREEN — so no Go-side fence suppresses the ghost, and the remaining suppression is client-side production
frontend, unreachable from a Go overlay. No honest browser-observable falsifiable form of this assertion exists
without changing production behaviour, so none was fabricated; `result.json`'s `survived: false` is a guaranteed
value, not evidence. (3) The retry case is **server-verified**: `retryMutationIDs` records every `turn/start`
attempt and the test asserts exactly two entries with the same non-empty id, so a regenerated id fails
immediately. (4) The unavailability case asserts server-side that no `turn/start` ever carried the retained
draft text, independently of timing. (5) `readyTransitions: 1` — a browser reconnect would make it ≥ 2 — with
`resyncCount: 1`, so recovery went through the same never-reconnected socket; a reconnect is explicitly not the
mechanism.

**Browser evidence is retained**, outside the scratch the runner deletes:
`.superpowers/sdd/2026-09-10-daemon-idle-retirement/task-12-browser-artifacts/` holds five 1400×900 PNGs and a
machine-readable `result.json`. The three the plan requires are present (before retirement, after retirement
with draft, after resumed streaming) and two more extend coverage (lost-reply retry, unavailable controls).
`result.json` records the core contract: same ref, same draft and same turn IDs across retirement with
`sourceGeneration` moving `instance_v1` → `instance_v2`, exactly one new turn on submit, `lateFrame.survived:
false`, `retry.turnAdded: 1`, and the unavailable case retaining the draft with the turn set unchanged.
Record-only observation: `01-before-retirement.png` and `02-after-retirement.png` are byte-identical, which is
consistent with "the transcript and draft look unchanged" but means those two images do not independently show
the transition; the generation change in `result.json` carries that.

**Independent review: spec PASS, 0 Critical**, quality "acceptable with one noted inconsistency". It also
confirmed that the five fixture-construction defects fixed during the task were infrastructure correctness
problems rather than fixture weakening — an empty navigation `generationId`, item-mode `thread/read` requiring
`itemsView:"fragment"`, the relay needing a live continuation, cross-origin WebSocket needing the Vite proxy,
and the canonical relay path needing the resync before the old generation closed. Without those the fixture was
not exercising the real production path at all; fixing them made the real path exercisable.

**Fix round 1 (`9482778d5b`, 1 file, +9/−5).** The one actionable finding: `docs/developing-evener/testing.md`
still claimed in its "Frontend setup boundary" prose that `make test-web-browser` is deterministic after the
frontend dependencies alone, while the same commit had correctly updated the guard table to say `retirementguard`
also needs the Go toolchain. The file contradicted itself and a reader of the prose got a wrong prerequisite
list. The parent confirmed the contradiction by reading both passages and fixed it rather than deferring it,
since the task introduced it in a file it modified. The prose now separates the two targets' prerequisites,
states that a missing browser or Go toolchain is a reported prerequisite failure, and notes that a cold module
cache can require network access. The parent then confirmed the prose survives `make generate` with the tree
clean, and that the generated table is unchanged.

**Two paths outside the plan's Step 5 commit list, both necessary and both disclosed.**
`scripts/web/test-web-browser.sh` *is* `make test-web-browser` — its hard-coded guard loop is the only place the
new guard could be added, and the plan's Files list omits it (plan defect, as with Task 8's missing frontend
fixtures). `runtime_pair_build_test.go` pins the old five-guard shape and the old npm-command set, both of which
Task 12 necessarily changes; the review confirmed it was **strengthened** (5 → 6 verdicts, all six guards named
explicitly, `"run retirementguard"` added, nothing removed). Same forced-test-edit precedent as Task 8's goldens
and Task 10's catalog/routing tests.

**Verification.** Parent gates rerun by the parent on the committed tree, each with its own exit status
(`task-12-parent-verification.log`): `TMPDIR=/tmp/eb make test-web-browser` → six PASS including
`web-retirementguard`, exit 0, and **no leftover** scratch directory; focused vitest → 1 passed, exit 0;
`go test -race ./cmd/evener-hub -run 'RetirementBrowser|RetirementResume|Relay' -args -retirement-browser` →
ok 38.616s, exit 0; canonical `make test` → all eight modules PASS, exit 0; zero `[no tests to run]`
occurrences; `make generate` idempotent with a clean tree. The browser gate needs a short `TMPDIR` in this
environment because Chrome's process-singleton socket exceeds `sun_path` under the default sandbox root — that
applies to all six guards and is environmental, not a test result.

**A real defect the parent caught in an intermediate state.** The first post-wiring browser run printed six PASS
but then produced ~4.2 MB of `rm: cannot remove … Permission denied` under the new guard's private HOME: because
`retirementguard` runs `go test`, a bare private HOME made Go build a ~696 MB module cache whose read-only files
defeat the runner's `scratch_rm`, which sets `finish_status=1` and exits nonzero. That is a correct failure, not
a flaky test — it breaks the contract that `runtime_pair_build_test.go`'s conciseness test pins. Fixed at the
cause by routing the guard through the pre-existing `scripts/lib/private-go-home.sh` (already used by
`build-runtime-pair.sh` and `run-module-tests.sh`), which preserves the user's Go caches while keeping the
guard's `HOME` private; the review judged this sound and the "private process home" documentation still
accurate.

**Process notes worth keeping.** Two writers died on provider exhaustion before this landed — the first after 27
minutes on an `openrouter-corp` 402 (insufficient credits), the second in 1.4 seconds on a `kimi-code` 403
(weekly usage limit, not the 5-hour window earlier entries recorded). The parent preserved the entire WIP
out-of-tree with checksums before dispatching a replacement, so nothing was lost. The successful continuation
then ended a turn on a checkpoint rather than completing, which is why the parent assessed the on-disk state
directly instead of trusting the checkpoint — that assessment is what surfaced the cleanup defect.

Record-only items for the hardening list: the two byte-identical screenshots; the disclosed 800 ms absence
tripwire in the no-auto-resume case (the Go-side assertion is the real mechanism, so the bound is belt to
suspenders); `make lint` and whole-repo `make test-race` were not run (not in Task 12's gate list); the Go race
log carries no per-test PASS line because the test ran without `-v`; and the race detector remains
probabilistic. The environment-specific `TMPDIR=/tmp/eb` requirement for the browser gate should be recorded in
the operator-facing docs alongside Task 13's operator policy.

Tasks 12 and 13: Task 13 remains. At this point plan tasks 1–12 of 13 are complete and accepted.

### Task 13 — ACCEPTED (`431c09e1af`)

The final plan task: a real launch/exit/resume process lifecycle end to end, plus operator policy. Delivered
`431c09e1af1b341878033f23b184f2c0c7f27b50` (parent `4efcbe92a1`), 4 files, +2685/-4:
`cmd/evener-hub/daemon_retirement_e2e_test.go` (new, 1991), `cmd/evener/serve_retirement_process_test.go`
(new, 498), `cmd/evener-hub/spawn.go` (the launch seam, 23), `docs/daemon-idle-retirement.md` (new, 177).

**What it proves.** `TestDaemonRetirementProcessHelper` is a `go test -c` test binary built into a
fixture-owned path and invoked with `-test.run=^TestDaemonRetirementProcessHelper$`. The Hub seam replaces only
the external executable invocation, with the real resolved argv/env, rendezvous, listeners, serve
admission/release and the Hub discovery/resume algorithms left real. `newDaemonRetirementProcessFixture` owns
private HOME/XDG roots, the Hub process, real daemon child handles, clock-control pipes and rendezvous roots.
`assertSingleExecution` cross-checks three surfaces — the production mutation journal via `doctor.Mutations`,
the transcript via the production schema decoders, and the fixture IPC provider invocations — so the
exactly-once claim is not satisfied by fixture bookkeeping. All eleven required process tests plus Step 1's
Hub-default expiry-and-resume test exist and pass.

**Parent gates on the committed tree.** `(cd agent && go test -race . -run '^TestRetirement' -count=1 -v)`
alone: exit 0, 275 PASS / 0 FAIL / 0 SKIP, no data race, `ok agent 69.313s`, including
`TestRetirementPreservationNestedColdRestore`, `TestRetirementForeignSweepPreservesOccupiedLanes` and
`TestRetirementSharedChildScratchBindingsRestore` in both sandbox modes. Canonical `env ROOT_FULL=1 make test`:
exit 0, 8/8 modules (root 147.96s, agent 9.72s, llm 9.15s, auth, envvars, invariant, identifier, web 178.13s),
239s wall, no FAIL and no SKIP. `env TMPDIR=/tmp/eb make test-web-browser`: exit 0, 6/6 guards PASS including
`web-retirementguard`. Both of the writer's Step-2 RED logs were read directly and are genuine behavioral
failures (the Hub→daemon timeout boundary arming 30m against the Hub default 1h; the launch boundary dying with
`no tests to run`), not tautologies.

**Review.** Independent adversarial review on a different model (`dlg_034NvojgDEDrFrv2zMlFor`,
lunaroute/glm-5.3; the first attempt on openrouter-corp/anthropic/claude-sonnet-4.6 died on an HTTP 402 after
consuming 90 314 of the 128 000 requested tokens and left nothing behind): **spec compliant, quality approved**,
0 Critical, 0 Important, 0 real-defect Minors. It built `go test -overlay` sabotage probes and proved the two
oracles that mattered regression-catching: a simulated second execution drove the invocation-count check RED
(`scripted provider executions … = 2, want exactly 1`), and a terminal record appended at release drove the
transcript byte-identity assertion RED. An incidental probe also produced real end-to-end evidence of the
documented prepare-refusal semantics: corrupting the transcript before retirement made the daemon refuse to
retire and stay resident rather than release an unvalidatable tree.

**Two branch-level findings, neither Task 13's fault, both recorded for the remaining workflow.**

1. **The branch's lint debt is its own.** `make merge-approval-gate` exits 2 in `lint-golangci` on 74 findings
   (root 14, `agent` 60). The writer called them "pre-existing"; classified against `2664cc881d..HEAD`, 71 are in
   files this branch added and 3 in files it modified, each inside a branch-added hunk, and none can exist on
   `main`. The branch has never been pushed, so CI never enforced lint while the debt accumulated across Tasks
   1–12. `golangci-lint 2.13.1` matches the pin, so this is not version drift, and it is not only cosmetic: two
   unchecked `Close` errors, four `nilerr` in `agent/session_scratch_retention.go`, a staticcheck hit in
   `app_daemons.go`, and two files not goimports-clean. Task 13's own two files have zero findings.
2. **A pre-existing load-sensitive flake in a Task 8 test.** The parent's combined focused gate came back RED
   once in four runs on `TestServeRetirementManualTimerSingleOwner/manual_first_blocks_the_timer`
   (`cmd/evener/serve_retirement_test.go`, last touched by Task 8's `03d017a340`; Task 13 does not touch it),
   and green 10/10 in isolation. From source it is a clean refusal and not a hang: `claim_consumed` fires at the
   top of `consumeRetirementClaim` (`serve.go:786`) before any blocking call, and `TryClaim(manual=true)` returns
   nil immediately when admitted work is in flight or evidence finds blockers (`retirement.go:259-264, 291`). The
   daemon correctly declined to retire a session with unsettled work; the test asserts on the manual-retire path
   right after `clk.awaitArm` without waiting for a settled state — the same missing precondition the Task 13
   writer independently found and fixed in its own fixture.

**Report-accuracy notes (record-only).** The writer's blanket claim that every wait is a pipe acknowledgement,
a process exit or an event is inaccurate in the letter: `awaitReflected`
(`daemon_retirement_e2e_test.go:730-753`) is a second 2 ms bounded condition-watch, on the durable mutation
journal, undisclosed alongside `awaitSettled`. It is plan-compliant — the plan requires awaiting durable
reflected journal state, which has no push event — and its 90 s tripwire errors out. Similarly, "only two
substitutes" understates the helper's substitutions (`buildProfile` is also replaced, `ensureConfigDirs` and
`seedMarketplaces` are no-ops, and the helper calls `runServeWithDeps` directly so `reclaimScratch` never runs);
none touch the retirement lifecycle. Style notes: the plan's `awaitTurnSettled` is implemented as
`waitTurnSettled`; `mutationReport` ignores its `mutationID` parameter; and the adapter's
`len(req.Tools) == 0` turn/side-call discriminator is shape-coupled.

Plan tasks 1–13 are now complete and accepted. The two branch-level findings above, plus the hardening list
accumulated across every task, must be cleared before the branch merges — `make lint` remediation first, since
it is what makes the canonical gate red. The branch is not merged, and no binary has been installed.

### Branch remediation — ACCEPTED (`b7f336eb71` + `39eaf2b803`)

Clears the two blockers that made the canonical merge gate red. Both were recorded when Task 13 was accepted.

**Blocker B1 — the branch's own lint debt.** `make merge-approval-gate` exited 2 in `lint-golangci` on 74
findings (root 14, `agent` 60). Fixed in `b7f336eb71` (25 files, +140/−111). The writer's Step 0 settled the
scope question decisively: a detached scratch worktree at the branch point `2664cc881d` runs `make lint` with
exit 0 and all nine sub-targets passing, so the base is lint-clean and **every one of the 74 findings was this
branch's own debt**. The "pre-existing" framing in Task 13's report was wrong, as the parent ruled.

**Blocker B2 — a load-sensitive flake in Task 8's test.** `TestServeRetirementManualTimerSingleOwner` failed
about one combined run in four. Fixed in `39eaf2b803` (`cmd/evener/serve_retirement_test.go`, +50) by adding
`awaitRetirementSettled`, a bounded state condition-watch that waits until the daemon's own published lifecycle
reports `phase == resident`, no blockers, and an armed deadline, held continuously for 500 ms with a 15 s
loud-failure tripwire. Diagnosis confirmed rather than assumed: `consumeRetirementClaim` emits `claim_consumed`
at its top (`serve.go:786`) before any blocking call, so "never observed" means the claim was never granted, and
`TryClaim(manual=true)` returns nil immediately while work is in flight or evidence finds blockers. The daemon
was correctly refusing to retire a session with unsettled work — a refusal, not a hang, and not a product
defect. The writer widened the gate to all four retirement triggers in the file after reproducing the identical
hole in `TestServeRetirementAutomaticExpiryReachesRelease`. No production file changed.

**Parent verification.**

- Canonical `env ROOT_FULL=1 make merge-approval-gate`: **exit 0**, 241 s, under ambient load ~10 with no
  artificial load added — lint 9/9 sub-targets PASS, build ok, test 8/8 module streams PASS, zero failure
  markers. This is the first green canonical gate on this branch.
- `lint-golangci` returned in 2 s there, i.e. a cache hit, so it was not treated as sufficient. A **cold-cache**
  run (`GOLANGCI_LINT_CACHE` pointed at a fresh directory, bypassing the worktree's shared cache) reported
  `0 issues.` for the root module and `0 issues.` for the agent module, in 88 s of real analysis. B1 is closed.
- The combined focused gate `go test -race ./cmd/evener-hub ./cmd/evener -run
  'TestDaemonRetirement|TestServeRetirement' -count=1` ran **5/5 green**, plus a verbose run showing the same
  test set (23 PASS / 0 FAIL / 3 SKIP, including both `TestServeRetirementManualTimerSingleOwner` subtests).
- No assertion was weakened and no test was skipped: test files are +88/−88, with zero `t.Skip` added or
  removed anywhere in the remediation, and `git diff --check` is clean.

**Changes the parent reviewed individually.** The two `gocritic deferInLoop` fixes are the only
behaviour-adjacent production changes, and they are semantically equivalent: each collects
`plans.retirementRelease` into a slice and runs them LIFO in one deferred closure via `slices.Backward` instead
of `defer` inside the loop. The replaced per-iteration defers also ran LIFO at function return with the same
per-iteration binding, so hold-until-return, release-exactly-once and ordering are preserved, including on early
return. The two assertion-adjacent test removals are genuine lint conversions — the `sloppyTypeAssert` fix drops
redundant `.(execenv.ExecutionEnvironment)` casts while keeping `got != wantPolicy → t.Fatalf`, and the QF1003
fix turns an `if/else if` into an equivalent `switch` with unchanged bodies. The four `//nolint:nilerr`
suppressions were checked at the source: `retentionSet` is assigned `true` at exactly one site
(`agent/execenv/scratch_retention.go:25`) and never reset, and the getter errors only when it is false, so the
two read-back branches really are unreachable after a successful set; the other two encode the documented
"source with no durable identity is a no-op". Each suppression is justified rather than concealing a bug. The
errcheck fixes keep the existing `_ =` idiom, and the `Close` errors are genuinely non-actionable after the
operation has resolved.

**New hardening-list item.** The writer's first canonical run was red (exit 2) on `ROOT_FULL=1 make test`
flaking in Task 13's own `TestDaemonRetirementProcessAdmittedWorkResetsInterval`
(`daemon_retirement_e2e_test.go:1368`) hitting the 90 s `daemonRetirementWatchdog` under artificial load; the
same commit passed on re-run. That is load-sensitivity in the Task 13 e2e tests' watchdog, distinct from the
serve flake this unit fixed, and it is a robustness item rather than a correctness defect.

Branch HEAD after this unit: `39eaf2b803`. Still unmerged; nothing installed.
