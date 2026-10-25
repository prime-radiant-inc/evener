# Notification Repair Stack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair the remaining notification migration failures through four scoped PRs, each with its own CI and ten-round repair ceiling.

**Architecture:** Preserve the implemented common notification receiver and wake path. Repair lifecycle, receiver, Stop and producer defects at their existing owners, with supported-path tests. Keep the original migration PR and original terminal-report PR unchanged, and publish a dependent repair stack rather than reconstructing the migration.

**Tech Stack:** Go, existing transcript/delegate/job journals, GitHub Actions, existing scripted-provider test boundary.

**Spec:** `docs/superpowers/specs/2026-10-23-unified-session-notifications-design.md`

## Authorization and scope

Jesse selected “Four repair PRs, with stack CI” after authorizing “decompose it and keep working. 10 rounds for each new subpr”. This is the execution breakdown of that selected scope, not a new architecture proposal. Keep the earlier Sol 6.1 implementer method. Do not restart completed historical implementation, simplify, PAR or whole-branch reviews. New fixes receive scoped review and current-head CI evidence.

Preserve `delegate-terminal-reports` / PR4841 at `9852924f0e1a06771ed1b985145a07eafefd1e4d` and `unified-session-notifications` / PR4951 at `4d8ec6f52690aa04e4a29eeaed9176ad4bd73bbc`. No merge-to-main, deployment, release or live-state mutation is authorized by this repair plan.

## Global Constraints

- “No new queue store is needed for the chosen design.”
- “A duplicate with identical content re-establishes durability if needed and succeeds without another append. The same ID with different content is an integrity error.”
- “A status read, transcript read, inline waiter resolution or successful enqueue is not observation either.”
- “Waiting releases execution, not ownership.”
- “Explicit Stop parks execution without deleting accepted messages.”
- “Keep current old-record readers. Do not introduce a new compatibility mode, migration store or legacy notification codec.”
- “Do not add a human attention gate, a Needs You row, a task-list completion gate, a report-only extra model round, a second inbox store, a general actor framework, or a periodic poll for model work.”
- Keep intentionally empty StateDir supported. Configured persistence failure must never fall back to nonpersistent operation.
- Read `docs/developing-evener/testing.md`. No copy-string assertions, internal-behavior mocks, removed safety assertions, widened deadlines, or suppression of incidental warnings.
- Reproduce locally with minimal targeted tests and scripted providers; full suites, lint and browser gates run in CI. Preserve original warning limitations rather than claiming pristine output from exit zero alone.
- Never bypass hooks, force-push, rewrite old branches or change required check strictness. Keep independent writers isolated; shared-file handoffs are serial.

## Review Focus

1. Failed root wait followed by genuinely new input: fresh input reaches a request, unsafe resumption remains refused. Task 1.
2. Runtime identity published before initialization: asynchronous wake must never read fields while the fixture attaches them. Task 2.
3. Accepted notification below a permanently closed ancestor: prove reachable custody or an addressed permanent-target disposition, not merely retained stranded debt. Task 3, with Task 2 attention-owner handoff if necessary.
4. Crash after shell receiver custody but before source acknowledgment: reopening reconstructs the same complete message and one canonical ID. Task 4.
5. Callback produced by a descendant whose registration/runtime disappears: both bootstrap candidate paths recover its retained frame without recreating the registration. Task 4.

## Branches, CI and round accounting

| Order | New branch | PR target | Scope |
| --- | --- | --- | --- |
| 1 | `notification-repair-execution` | `unified-session-notifications` | Idle Stop, honest terminal outcomes, failed wait supersession, stack CI |
| 2 | `notification-repair-receiver` | `notification-repair-execution` | Custody/observation fixtures, publication race, projection interleaving, nonpersistent autonomy |
| 3 | `notification-repair-stop` | `notification-repair-receiver` | New Stop preservation and historical cleanup |
| 4 | `notification-repair-replay` | `notification-repair-stop` | Shell identity and callback cold replay |

Task 1 starts at the preserved migration head. Each next branch starts at the preceding checked local tip. No node dependency installs are needed for these Go changes. Keep temporary test files on the session scratch mount; do not delete other lanes to reclaim space.

Task 1 extends only `.github/workflows/ci.yml` pull-request base filters to the exact four feature bases above, retaining `main`. Push triggers stay main-only. No wildcard expansion, permissions change, job omission, deployment trigger or workflow-dispatch addition. Prove actual CI launches for each new PR; parsed YAML alone does not prove GitHub dispatch.

Each PR receives at most ten repair rounds. A round is one batch of fixes, one push, one matching CI/review wait. Track head, findings, evidence and outcome per PR in the plan ledger. Findings attributable to a later named repair unit remain visible baseline failures, never green claims or silent dismissals. Earlier stack layers can remain red for inherited defects; only the integrated tip can establish all repairs passing. Do not merge a red layer to enable later CI. At the cap, report unresolved findings and stop that PR's loop.

The parent owns CI/review waits. Read actual failed jobs, exact-head review bodies and own per-commit reviews. A successful review status is not a clean verdict. A missing CI run is a blocker, not permission for empty commits or close/reopen cycles.

---

### Task 1: Execution recovery and stack CI

**Files:**
- Modify: `.github/workflows/ci.yml`, `agent/delegate_user_stop.go`, `agent/session_wait.go`, `agent/session_execution.go`, `agent/delegate_tree_intents.go` only when the corresponding regression proves runtime work is needed.
- Test: new `ci_stack_workflow_test.go`; existing `agent/delegate_user_stop_test.go`, `agent/delegate_tree_finish_test.go`, `agent/session_wait_test.go`, `agent/notification_wait_test.go`.
- Docs: owning wait/lifecycle passages in `docs/subagent-runtime-contracts.md` and `docs/product/subsystems.md` only when actual behavior changes.

**Interfaces:** Consume existing `(*subagent).requestRunCancel(asUserStop bool) (chan struct{}, error)`, `safeWaitBoundary(checkpoint schema.WaitCheckpointInfo, entries []transcript.Entry, resultToolName string) bool`, `(*Session).claimWaitResumption(turnID string) error`, and `(*Session).beginExecution(name string) error`. Preserve signatures and all existing tool/generation identities. Produce corrected fresh-input admission and direct-Stop behavior without a second recovery owner.

- [ ] Reproduce `TestUserStopRefusesARunThatIsNotRunning` and `TestDelegateControllerSettlingReportedPacketOverridesLaterRuntimeError` separately on unchanged production. Record that the nil-session fixture proves a helper/fixture panic, not public reachability.
- [ ] Add `TestFreshRootInputSupersedesFailedWaitCheckpoint`: write a wait checkpoint plus failed completion, tear down/reopen, submit a new root input, and assert admission, preserved client/input identity, actual scripted request and distinct execution identity. Assert no fake observation or reuse of the failed execution. Watch it fail before changing production.
- [ ] Add `TestCIIncludesNotificationRepairStack`: parse the actual workflow with the existing YAML dependency and assert main plus the four exact PR bases, unchanged main-only push trigger, and no broader trigger/permission expansion. Watch the missing branch filters fail before editing YAML. Avoid testing a duplicate trigger implementation.
- [ ] Establish whether nil-session subagents are valid inputs. Initialize the idle fixture if not; otherwise guard only the supported nil lookup. Preserve initialized idle refusal, exact target cancellation/done channel, repeated/settling refusal and yielded/cold yielded Stop.
- [ ] Separate settled failed-execution supersession from unsafe wait resumption in the existing owner. Preserve unknown/missing evidence retry and complete tool-pair requirements. Do not swallow unsafe errors or delete the checkpoint before durable admission.
- [ ] Correct the terminal proof to require honest failed outcome while preserving the accepted opaque report payload and one generation-scoped terminal notification. Retain no-early-delivery and retry checks; change runtime only if the supported case proves a defect.
- [ ] Run the new tests and the existing P/T/W controls from the failure report, with focused race checks. Exact root command: `go test . -run '^TestCIIncludesNotificationRepairStack$' -count=1`. Agent regressions: `go -C agent test . -run '^(TestUserStopRefusesARunThatIsNotRunning|TestDelegateControllerSettlingReportedPacketOverridesLaterRuntimeError|TestFreshRootInputSupersedesFailedWaitCheckpoint)$' -count=1 -v`. List and run the safe-wait/direct-Stop controls separately; an empty selection is not evidence.
- [ ] Format touched Go files, inspect the exact diff, commit only named owned paths with normal hooks, report command outputs and unresolved baseline failures, then receive independent scoped review before publication.

### Task 2: Receiver custody, observation and fixture publication

**Files:**
- Tests: `agent/delegate_tree_start_test.go`, `agent/session_attention_test.go`, `agent/delegate_tree_controller_test.go`, `agent/notification_inline_delivery_test.go`, `agent/delegate_resource_supervision_test.go`, `agent/session_state_goal_exact_fuzz_test.go`, `agent/notification_final_regression_test.go`.
- Runtime only after supported regression: `agent/delegate_tree_start.go`, `agent/delegate_delivery.go`, `agent/session_attention.go`, `agent/delegate_tree_attention.go`, `agent/session_state.go`, `agent/session_notifications.go`, `agent/session.go`.

**Interfaces:** Consume canonical `enqueueNotification(notification) error`, source acknowledgment and successful-response receipts from the existing migration. Produce no new receiver API or queue. Preserve common release/dispatch and selected generation/ID reservation.

- [ ] Reproduce the three receiver-start failures, the projection-clear failure and `FuzzSessionStateGoalExactCoverage` committed seeds before changes.
- [ ] Initialize runtime identity, transcript and controller binding before publication/finalization release. Use a real receiver for inline delivery and actual successful request coverage for observation. Keep failed input/start, unavailable receiver, source retry and exact selected ID/generation controls.
- [ ] Establish the projection interleaving causally: a real accepted ID arrives during stale empty-fold clearing and remains pending/runnable. Distinguish cache bypass or a wake-only fixture from a genuine production race. Preserve unreadable-transcript uncertainty and fully observed clearing without another model request.
- [ ] Replace the state-goal fixture's legacy queue ingress with supported canonical nonpersistent acceptance. Preserve every existing state/goal seed, hold/release assertion and configured-storage-failure negative control.
- [ ] Run anchored selectors for all affected tests, then targeted race checks and `GOENV=off GOFLAGS= GOWORK="$PWD/go.work" go -C agent test -tags evenerfuzz -run '^FuzzSessionStateGoalExactCoverage$' -count=1 -v .`. Keep tests mutating global seams serial and join their async work.
- [ ] Commit owned paths with normal hooks and obtain scoped review. Supply the accepted-content/permanent-target interface to Task 3; do not preemptively expand Stop semantics.

### Task 3: Stop preservation and historical cleanup

**Files:** `agent/delegate_tree_stop.go`, `agent/delegate_tree_restore.go`, `agent/internal/delegatestore/fold.go` only after regression; tests `agent/delegate_stop_attention_scope_test.go`, `agent/delegate_tree_restore_test.go`, `agent/delegate_tree_stop_test.go`, `agent/delegate_attention_stop_discard_test.go`. Coordinate any needed `agent/delegate_tree_attention.go` change with the completed Task 2 owner.

**Interfaces:** Consume `PreserveNotifications:true` on new Stop records and the existing historical false/missing-bit fold. Preserve source debt, receiver identities, execution holds and cancellation/writer fences.

- [ ] Reproduce all twelve Stop failures identified in unit S of the failure report.
- [ ] Exercise historical cleanup through genuine historical Stop records rather than stripping preservation from new events. Retain scoped/version/replacement evidence rejection, append-failure debt and single-writer controls on paths that actually attempt cleanup.
- [ ] Prove new Stop retains exact produced/accepted IDs, records no fake observation and makes no request through the hold. Explicit release delivers the same content without unrelated input. Preserve cancellation-produced content and subtree scope.
- [ ] Resolve the permanently closed ancestor case with a real reachable resumption or addressed permanent-target diagnostic/disposition. Retention of an unreachable ID alone cannot pass.
- [ ] Run the complete anchored S selector from the failure report, focused race checks and the store-fold package if changed. Do not restore destructive new-Stop behavior to satisfy old expectations.
- [ ] Update owning preservation docs if runtime changes, commit exact owned paths and obtain scoped review before publication.

### Task 4: Shell and callback restart replay

**Files:** Runtime `agent/delegate_shell_repair.go`, `agent/jobs.go`, `agent/job_watch.go`, `agent/delegate_tree_watch.go`; tests `agent/delegate_shell_repair_test.go`, `agent/delegate_resource_shell_test.go`, `agent/watch_attach_terminal_program_fuzz_test.go`, `agent/notification_producers_test.go`.

**Interfaces:** Consume canonical receiver acceptance and immutable complete-message identity. Both current and historical unacknowledged callback candidate paths must recognize real callback source records, without reinstating a callback-only transport or retired registration.

- [ ] Reproduce the shell finalizer failure and `FuzzWatchAttachTerminalProgram` seeds. Keep the exact finalizer fence and retained new-Stop obligation.
- [ ] Add `TestShellReplayPreservesAcceptedMachineryMessage`: accept live notification, retain source debt, tear down/reopen, cold-repair and assert one canonical record with identical ID/body/metadata and ack only after custody. Observe red before fixing the shell construction boundary; retain mismatched same-ID refusal.
- [ ] Add `TestColdDescendantCallbackReplayAfterRootRestart`: produce but prevent acceptance in a descendant, tear down runtime/controller/manager, reopen from root and observe the original frame without another input. Include one-shot/removal, repeated reopen and failed source ack controls. Observe red before changing both bootstrap candidate filters.
- [ ] Give the fuzz fixture real receiver custody. Identify callback and sidecar obligations by structured identity; retain every seed/operation instead of widening an expected count.
- [ ] Run the new regressions, H/C controls and deterministic watch seed replay with targeted race checks. Run cross-boundary Stop/source and nonpersistent controls on the integrated tip. Full nonroot/race/fuzz/web gates run in CI, not as local pre-push suites.
- [ ] Commit owned paths and receive scoped review. Publish once per repair round, await exact-head CI/review, and report newly exposed failures by their actual owner. Do not claim all branch failures were known before the suite-blocking panic was removed.

## Completion

Every new PR is named in the final report with head, round count, scoped evidence and actual CI/review status. The integrated tip must pass the full required checks; earlier layer failures remain explicitly attributed, never relabeled green. Preserve the existing original PRs, source history and evidence. Stop at an exhausted ten-round cap or an unresolved consequential product choice; no deployment or merge is included.
