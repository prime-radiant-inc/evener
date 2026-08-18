# Delegate Resource Recovery Implementation Plan

> **COMPLETE — do not execute this plan.** All fourteen tasks shipped and landed
> on `main` (kata `my73`) as a linear commit series — there is no merge commit —
> running from `3507688cf` ("feat: add the stable delegate store") through
> `554221673` ("fix: accept fsynced steering before stop", closing the final
> Task 14 fixed-range review). The executor instruction below is
> retained for provenance only; running it would re-implement a subsystem that
> already exists.
>
> **The checkboxes lie.** Only 7 of 76 are ticked. They were abandoned as a
> tracking mechanism partway through and an unticked box here means nothing —
> Task 14 was independently reviewed and accepted with most boxes still empty.
> To check what actually landed, run the gates, not the boxes:
>
> ```bash
> go test ./agent -run '^TestDelegateLegacyDormancy_' -count=1
> go test ./agent -run '^(TestStableDelegateWatch_|TestDelegateResourceSupervision_|TestDelegateRuntimeReclaim_|TestStableDelegateShell_|TestStableDelegateReadOnly_)' -count=1
> ```
>
> The shipped contract is `docs/subagent-management/11-delegate-resource-model.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace delegate activation jobs with one root-owned stable delegate controller and one durable delegate aggregate, making `dlg_...` the only delegate control identity while deleting more lifecycle machinery than the replacement adds.

**Architecture:** Tasks 1–5 built and proved a dormant `delegatestore` plus a synchronous one-mutex `delegateTreeController` while the legacy route remained active. Tasks 6–14 complete one flag-day vertical branch: bootstrap and runtime first, preserved delivery/supervision/retention behavior next, every tool and client consumer after that, and legacy-authority deletion only after those replacements are live. Intermediate commits are review checkpoints, not deployable compatibility phases. The first deployable branch accepts the stable store and stable watch journal, rejects legacy delegate JobRecord and delegate-job-addressed watch state, and contains no dual writer or fallback route.

**Execution status (2026-08-14):** Task 6 is complete at immutable commit `5df4ad5f487f3674f4016f40eeb82d3cf49b7aa4`; Task 7 is complete at immutable commit `521a4892d977927154f34636343d84e8dda15508`. The recovery branch remains an intentionally nondeployable flag-day checkpoint. Tasks 8–14, final integration verification, merge, and deployment remain incomplete.

**Tech Stack:** Go, append-only JSONL with fsync, Evener Session/transcript/provider seams, deterministic channel barriers, Rapid/native fuzz replay, AppWire Go/TypeScript generation, React/Vitest/Biome, Kata, and repository Make gates.

## Global Constraints

- Jesse is the human partner. The authoritative target is `docs/subagent-management/11-delegate-resource-model.md`; this dated file is an execution plan, not a second product specification.
- Execute Tasks 6–14 only in `/Users/jesse/prime-radiant/toil-suite/evener/.worktrees/delegate-resource-task6-clean` on `wip/delegate-resource-task6-clean`, starting from completed Task 5 commit `2da9863390e3e064fc015afe79a54fe8a8ce1d8f`. Before each task, verify the branch, expected prior task commit, and clean tracked porcelain. Never merge, rebase, cherry-pick, reset, clean, stash, switch branches, or push while executing this plan.
- Do not copy lifecycle code from `delegate-identity-integration`. Pure DTO/rendering ideas and still-valid behavioral tests must be re-derived against `main`.
- There is one stable public delegate identity (`dlg_...`), one root-owned controller, one durable delegate fold, one current private `uint64` generation per delegate, and one lifecycle mutex per root tree.
- A delegate generation is never a JobRecord, job ID, output file, independent reducer, query target, notification rail, or public row.
- Child Sessions inherit only the controller pointer and immutable owning delegate ID. Exact generation leases travel in the active run context, never in a mutable Session token, subagent manager, or job-manager mirror.
- Shell jobs remain durable `job_...` resources with output, watch, status, and stop. A shell launched by a delegate records `ParentDelegateID`; delegate lineage never uses `ParentJobID`.
- The controller mutex may cover validation, its own store append/fsync, and process-only claim/receipt admission. It may not cover transcript/history operations, provider calls, timers, process start/cancel, hooks, notifications, event delivery, runtime close/restore, filesystem lane mutation, worktree operations, shell-store reads, or completion waits.
- No Session, subagent, transcript, or shell job-manager lock may enter the controller. Callbacks snapshot exact identity, release local locks, and then report.
- Starting reservations are process-local and hold capacity. A delegate start uses that reservation as its construction receipt; do not add a second generic delegate receipt. Shell external starts use exact work tokens.
- Running steering is durably appended before acknowledgement and is bound exactly once at the next legal model boundary. Normal/communicate settlement cannot strand an earlier accepted steer.
- One exact finish event shape covers completed, failed, exhausted, cancelled, and stopped outcomes. `completed_no_action` is a private attention disposition whose public outcome is `completed`; there is no failure record.
- Pending owner deliveries are an ordered collection keyed by a deterministic delegate+generation delivery ID. Later generations cannot overwrite earlier unacknowledged delivery.
- Inline owner delivery is not acknowledged when the delegate waiter receives a packet. The delegate tool result carries one private process-only handoff to the caller Session's existing aggregated tool-result persistence boundary; that round is forced through a durable append, records private delivery metadata on the same turn, and calls `CompleteDelivery(true)` only after fsync. Append failure calls `CompleteDelivery(false)` and leaves the head queued; crash after fsync replays idempotently from the transcript metadata.
- The existing receiver transcript is the only durable attention journal. A model-bound steering turn carries private `AttentionID`; a provider-excluded presentational resolution marker records consumed or discarded. Stable IDs are `delegate:<deliveryID>` and `shell:<jobID>:<terminalGeneration>`. The daemon steering queue and `<stateDir>/queues/*.json` snapshot may remain transient/best-effort for non-attention nudges but are never attention or lifecycle authority.
- `TurnAttentionResolution` is transparent to tool-round structure. Context compaction and history repair may omit it from provider history but cannot use it as a cutoff between an assistant tool call and its result, and marker removal cannot create an orphan result or dangling call.
- Shell terminal markup remains `<job-notification job_id="job_..." job_type="shell">`. Delegate attention/result markup is `<delegate-notification delegate_id="dlg_...">` and contains no `job_id` or `job_type="delegate"`. Hub probing uses only `descendant_session_ids`/`descendant_states`, and delegate clients label the resource "Delegate", not "Job".
- There is at most one pending subtree stop per root tree. Same-target retry joins it; any different target returns typed busy until completion. No delegate reopens while a stop is pending.
- Root close first closes controller-wide admission, joins/drains a pending stop, performs whole-tree stop and postorder teardown, then closes the store.
- Idle terminal child runtimes normally remain resident. Preserve `max_retained_terminal` as demand-triggered reclamation during create or cold restore: claim exact quiescent runtime subtrees under the controller, close them postorder after unlock, and clear only their exact resident pointers. Do not add automatic/time-based unload, durable unload events, close flights, epochs, wildcard generations, detached supervisors, lock sharding, or lifecycle mirrors.
- Preserve the existing watch journal and all shipped `watch_parent`, observer, filter, coalescing, budget, inspection, terminal-ordering, restart, and `SessionMeta.ObservedBy` behavior. Replace delegate-job indirection with typed `session`, `shell`, and stable `delegate` endpoints. Do not add a second watch journal, loose receiver field, or activation fallback.
- This is a flag-day cutover. There is no migration or alias for legacy delegate JobRecords and no mixed lifecycle loader, compatibility window, dual write, fallback route, or feature flag. Any old delegate job bytes fail `legacy_delegate_state` even if a new store is also present; old delegate-job-addressed watch rows fail `legacy_delegate_watch_state`; shell-only history and the existing stable watch journal are permitted; unknown delegate-store versions fail closed.
- Foundation commits may contain dormant directly tested code, but the old production route remains the only active route until the one vertical cutover. No commit may expose a half-switched public delegate path.
- Before any production change, capture desired public behavior on unchanged `main` using temporary compile-valid tests and existing seams. Record honest RED/GREEN evidence in Kata `my73`, then remove intentional failures before ordinary hooks run.
- Every final regression test exercises registered tools or real Session/provider/executor behavior. Compile errors, missing selectors, timeouts, queue-only inspection, and internal symbol assertions are not behavioral RED evidence.
- Use channels/barriers and scripted providers/executors, never sleeps or polling races. Fix any sighted flake immediately.
- Fault-inject each lifecycle event boundary: created, run-started, terminal-prepared, run-finished, resumability-closed, stop-requested, stop-completed, and delivery-acknowledged. Append failure cannot leak in-memory mutation or external launch.
- Use `apply_patch` for edits, `gofmt` for Go, `npx biome check --write` on touched frontend `src/` files, and `make generate` for AppWire outputs. Never hand-edit generated TypeScript or protocol Markdown.
- Run `git status --short` before explicit staging; never use `git add -A`, wildcard staging, or bypass a hook. Commit each dormant foundation task and each later coherent cut with a detailed intent/evidence body.
- Stop for architectural review if correctness appears to require delegate JobRecords, a second lifecycle or watch fold, a Session/job-manager lifecycle mirror, an ancestor epoch vector, wildcard/zero-generation matching, reopen during stop, controller lock across timers/hooks/provider/transcript/runtime/worktree/notification work, automatic unload, migration aliases, a detached supervisor goroutine, or caller-specific recovery exceptions.

---

## Final file ownership

### Durable delegate state

- `agent/internal/delegatestore/event.go` — version header, event/batch envelope, and exactly eight event payloads.
- `agent/internal/delegatestore/record.go` — descriptor, sandbox snapshot, aggregate, private disposition, public outcome, prepared packet, ordered deliveries, and pending-stop membership.
- `agent/internal/delegatestore/fold.go` — the only pure delegate reducer.
- `agent/internal/delegatestore/store.go` — live append/fsync/open/recovery, private to the root controller.
- `agent/internal/delegatestore/read_events.go` — missing-file-tolerant read-only inspection; never creates, truncates, repairs, or writes.

### Controller and runtime

- `agent/delegate_tree_controller.go` — actor authorization, durable working fold, live exact runtime bindings, reservations, capacity, snapshots, and append-then-fold.
- `agent/delegate_tree_start.go` — create/idle/attention reservations and exact non-launched start binding.
- `agent/delegate_tree_steer.go` — durable steer, model/tool admission, settlement arbitration.
- `agent/delegate_tree_finish.go` — prepared terminal, exact finish, private disposition, ordered delivery state, immutable update plans.
- `agent/delegate_tree_work.go` — shell start receipts and exact committed shell work.
- `agent/delegate_tree_stop.go` — one globally serialized pending stop and immutable cancellation plans.
- `agent/delegate_tree_restore.go` — provider-free reducer repair from immutable external evidence.
- `agent/delegate_runtime.go` — child Session construction/launch adapter; no lifecycle authority.
- `agent/delegate_delivery.go` — post-unlock inline/notification insertion and acknowledgement.
- `agent/session_attention.go` — idempotent receiver-transcript attention append/fold/consume/discard; no queue or store.
- `agent/delegate_legacy_state.go` — narrow raw-envelope old-state rejection only; no old fold or translation.

### Existing owners changed at cutover

- Session/runtime: `agent/schema/turn.go`, `agent/session.go`, `session_config.go`, `session_init.go`, `session_lifecycle.go`, `session_model_call.go`, `session_queue.go`, `session_queue_persist.go`, `session_tools.go`, `session_tool_round.go`, `session_tools_communicate.go`, `transcript_read.go`, `transcript_render.go`; and `agent/internal/contextmgr/{context_manager.go,context_manager_test.go,compaction_seqfuzz_test.go,maybecompact_fc1_seqfuzz_test.go}` for resolution-marker-transparent compaction.
- Delegate mechanics: `agent/job_delegate.go`, `subagents.go`, `subagent_manager.go` (deleted or reduced to non-authoritative construction helpers).
- Shell jobs: `agent/job_shell.go`, `jobs.go`, `jobs_nested.go`, `job_notify.go`, `job_watch.go`, `session_jobtree_drain.go`.
- Tools: `agent/internal/tool/definitions.go`, `session_tools_jobs.go`, `session_outline.go`, `job_transcript_read.go`, `session_tools_transcript.go`.
- Persistence deletion: `agent/internal/jobstore/{event,record,fold,store}.go` becomes shell/watch-only after consumers move.
- Projection: `agent/events/{events,payloads}.go`, `jobs_activity.go`, `jobs_activity_past.go`, `historical_jobs.go`, `internal/appprojector/appwire_projection.go`, `appwire/types.go`, `appwire/protocol.go`, `server/appwire_runtime.go`, `cmd/evener-hub/app_jobs.go`; plus the stable live/reconnect status bridge `agent/{status.go,status_test.go,status_support_program_fuzz_test.go}`, `cmd/evener/{serve.go,serve_test.go,serve_coverage_fuzz_test.go}`, and `server/{server.go,server_test.go,server_surface_fuzz_test.go}`.
- Clients: `cmd/evener-hub/internal/hubcore/{prober.go,prober_test.go,prober_wire_test.go,scenarios_fuzz_test.go}`; `cmd/evener-tui/internal/transcript/{job_notification.go,reducer.go,types.go,reducer_test.go,cov_rtui_transcript_test.go,reducer_fuzz_test.go,fuzz_coverage_union_test.go}`; `cmd/evener-tui/{hub_notifications.go,hub_notifications_test.go,hub_notifications_fuzz_test.go,model_misc_serffuzz_test.go}`; `cmd/evener/{run_drain_test.go,run_drain_nested_test.go}`; and the exact Hub frontend activity/transcript/store, notification, fixture, and overflow-harness files named in Task 11.
- Operations: `agent/doctor`, `cmd/evener-doctor`, worktree/disposal helpers, bundled doctor references, prompts, and evergreen docs.

## Established foundation interfaces

Tasks 1–5 are complete at commit 2da9863390e3e064fc015afe79a54fe8a8ce1d8f. Tasks 6–14 consume the concrete interfaces below. If a task needs another process-only receipt or claim, it must add it in that task with a causal concurrency test; it may not rename or duplicate an existing lifecycle operation.

The controller is configured and owned once by the root Session:

~~~go
type delegateTreeControllerConfig struct {
    store         *delegatestore.Store
    rootSessionID string
    stateDir      string
    worktreeRoot  string
    turnLimit     int
    driveLimit    int
    now           func() time.Time
}

func openDelegateTreeController(cfg delegateTreeControllerConfig) (*delegateTreeController, error)
~~~

The completed start, runtime, settlement, shell, stop, delivery, and recovery surfaces are:

~~~go
func (c *delegateTreeController) ReserveCreate(actor delegateActor, descriptor delegatestore.Descriptor) (*delegateStartReservation, error)
func (c *delegateTreeController) ReserveStart(actor delegateActor, delegateID string) (*delegateStartReservation, error)
func (c *delegateTreeController) ReserveAttention(runtime *Session, attentionID string) (*delegateStartReservation, error)
func (c *delegateTreeController) CommitStart(reservation *delegateStartReservation) (delegateStartCommit, error)
func (c *delegateTreeController) AttachRuntime(lease delegateLease, runtime *Session) error
func (c *delegateTreeController) AdmitStartInput(lease delegateLease, admitInput func() error) (delegateMutationPlans, error)
func (c *delegateTreeController) AbortStart(reservation *delegateStartReservation) error

func (c *delegateTreeController) Steer(ctx context.Context, actor delegateActor, delegateID, message string) (delegateMutationPlans, error)
func (c *delegateTreeController) BeginModelRequest(lease delegateLease) ([]llm.Message, error)
func (c *delegateTreeController) BeginTool(lease delegateLease) error
func (c *delegateTreeController) BeginSettlement(lease delegateLease, packet *delegatestore.TerminalPacket) (bool, delegateMutationPlans, error)
func (c *delegateTreeController) FinishGeneration(lease delegateLease, finish delegateFinish) (delegateMutationPlans, error)

func (c *delegateTreeController) BeginShellWork(owner delegateLease) (delegateWorkToken, error)
func (c *delegateTreeController) CommitShellWork(token delegateWorkToken, shellJobID string, cancel context.CancelFunc) (bool, error)
func (c *delegateTreeController) AbortShellWork(token delegateWorkToken) error
func (c *delegateTreeController) ReportShellFinished(token delegateWorkToken, shellJobID string) (delegateMutationPlans, error)

func (c *delegateTreeController) StopSubtree(actor delegateActor, targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error)
func (c *delegateTreeController) CloseResumability(actor delegateActor, delegateID, reason string) (delegateMutationPlans, error)
func (c *delegateTreeController) BeginDelivery(plan delegateDeliveryPlan) (delegateDeliveryToken, bool, error)
func (c *delegateTreeController) CompleteDelivery(token delegateDeliveryToken, committed bool) (delegateMutationPlans, error)
func (c *delegateTreeController) ReportAttentionResolved(requestSeq, evidenceVersion uint64, delegateID, attentionID string, disposition delegateAttentionResolution, runtime *Session) (delegateMutationPlans, error)

func (c *delegateTreeController) Snapshot() delegateUpdatePlan
func (c *delegateTreeController) ReconcileRequirements() delegateReconcileRequirements
func (c *delegateTreeController) Reconcile(evidence delegateReconcileEvidence) (delegateMutationPlans, error)
func (c *delegateTreeController) Close(ctx context.Context) error
~~~

ReserveCreate and ReserveStart return the exact construction reservation. CommitStart accepts that same pointer and returns a delegateStartCommit containing the private lease, immutable descriptor paths, cancellation context, and captured update. BeginModelRequest already snapshots the bound Session history itself; callers do not pass a snapshot callback. Runtime bindings use *Session directly. CommitShellWork uses context.CancelFunc. ReportAttentionResolved requires both the durable stop request sequence and the process-only evidence version. Later tasks must use these signatures rather than restating obsolete token/value forms.

The dormant foundation still has callback-based transcript work in AdmitStartInput, Steer, and BeginModelRequest. Task 6 replaces AdmitStartInput with an input claim before registering production create/restore. Task 7 replaces steering append and model-history snapshot with claims before registering send/model execution. In the final route every transcript/history operation, attention, watch, timer, hook, provider, runtime, worktree, shell-manager, event, and notification action is claimed under the mutex and executed after unlock.

The durable store API is fixed:

~~~go
const CurrentVersion = 1

func delegatestore.Open(path string) (*delegatestore.Store, error)
func delegatestore.ReadEvents(path string) ([]delegatestore.Event, error)
func (s *delegatestore.Store) Load() ([]delegatestore.Event, error)
func (s *delegatestore.Store) Append(state delegatestore.State, event delegatestore.Event) (delegatestore.Event, delegatestore.State, error)
func (s *delegatestore.Store) AppendBatch(state delegatestore.State, events []delegatestore.Event) ([]delegatestore.Event, delegatestore.State, error)
func (s *delegatestore.Store) Close() error
func delegatestore.Fold(events []delegatestore.Event) (delegatestore.State, error)
func delegatestore.Apply(state delegatestore.State, event delegatestore.Event) error
~~~

The first JSONL line is the version header. The eight lifecycle events remain exactly delegate_created, delegate_run_started, delegate_terminal_prepared, delegate_run_finished, delegate_resumability_closed, delegate_subtree_stop_requested, delegate_subtree_stop_completed, and delegate_delivery_acknowledged. Watch state remains in the existing watch journal and is never copied into this fold.

The existing attention-resolution foundation and the delivery metadata completed in Task 7, then consumed and extended for watch recovery in Task 8, are private correlation fields, not lifecycle identities:

~~~go
type DelegateDeliveryCommit struct {
    ToolCallID string
    DeliveryID string
}

type AttentionResolutionInfo struct {
    AttentionID string
    Disposition string
}
~~~

AttentionID is valid only on a model-bound steering turn. AttentionResolution is valid only on a provider-excluded presentational resolution turn. The first pending entry for an ID is authoritative; an exact duplicate is idempotent; reuse with different content or disposition is corruption. No private delivery, update-sequence, generation, claim, receipt, watch cursor, or attention ID appears in provider messages or public control identity.

The only permitted new operational surfaces are assigned to explicit tasks:

- Task 6: BeginStartInput and CompleteStartInput replace callback-based AdmitStartInput with one stop-fenced process claim; FailCommittedStart batches permanent post-commit construction/admission failure from existing terminal-prepared, run-finished, and resumability-closed events.
- Task 7: BeginSteerPersistence, CompleteSteerPersistence, and AbortSteerPersistence replace the locked steering append; BeginModelRequest, CompleteModelRequest, and AbortModelRequest claim/snapshot/revalidate a model boundary without taking a transcript/history lock under the controller; the minimal idempotent receiver-attention append and inline caller delivery-commit bridge share the caller tool-result fsync boundary; fake-clock watchdog bindings ask the controller to admit ordinary attention; and one root-owned reconcile driver keeps positive stop waits self-driving after the requesting context ends.
- Task 8: consume the Task 7 attention append and inline commit bridge, then add cold attention folding/resolution plus enqueue and delivery receipts for the existing watch journal keyed by delivery ID and update sequence.
- Task 9: a process-only reclamation claim over exact quiescent resident runtime subtrees, plus recursive stop/root-close drain through the Task 7 reconcile driver; it adds no second driver.
- Task 10: read-only stable snapshots and pure cold readers; no lifecycle mutation or delivery acknowledgement.
- Task 11: monotonic projection revisions and lossless DTOs; revisions fence rendering only and are never control identities.

---

Tasks 1–5 below are retained as the completed foundation record and must not be rerun or rewritten. Their dormant implementations still contain the callback-based AdmitStartInput, locked steering append, and locked model-history snapshot described by their historical steps. Those are not approved production exceptions: Tasks 6 and 7 replace them with post-unlock claims before the registered flag-day route is complete.

### Task 1: Characterize desired public behavior on unchanged `main`

**Files:**
- Temporary create/delete: `agent/delegate_resource_phase_zero_test.go`
- Read only: existing `agent/job_delegate_*_test.go`, `job_nested_test.go`, `job_shell_test.go`, `session_communicate_test.go`
- External evidence: Kata `my73`

**Interfaces:**
- Consumes: current registered tools, `fakeAdapter`, `cfg.testOnly.delegateSend`, `cfg.testOnly.subagentAfterFinalStatePublish`, `cfg.testOnly.subagentPrepareFault`, and `blockingStartStreamingExecutor`.
- Produces: exact current-main RED/GREEN evidence and a clean tree; no committed test or production file.

- [ ] **Step 1: Verify the untouched implementation baseline**

Run and record bare outputs:

```bash
pwd
git branch --show-current
git rev-parse HEAD
git status --short
git merge-base main HEAD
```

Expected: named design worktree/branch, a clean porcelain, and production files matching `main`; the branch differs from `main` only by committed design/plan documentation.

- [ ] **Step 2: Add compile-valid current-main characterization tests**

Create one temporary test file using only existing symbols. Define these exact public tests:

```go
func TestDelegateResourcePhaseZero_CreateReturnsOnlyStableIdentity(t *testing.T)
func TestDelegateResourcePhaseZero_RunningSendReachesNextProviderRequestExactlyOnce(t *testing.T)
func TestDelegateResourcePhaseZero_IdleSendStartsOneSuccessor(t *testing.T)
func TestDelegateResourcePhaseZero_ConcurrentIdleSendsNeverStartTwoRuns(t *testing.T)
func TestDelegateResourcePhaseZero_RegisteredStableStopCancelsSubtree(t *testing.T)
func TestDelegateResourcePhaseZero_StopRacingSteerHasOneHonestOrder(t *testing.T)
func TestDelegateResourcePhaseZero_SteerBeforeCommunicateContinues(t *testing.T)
func TestDelegateResourcePhaseZero_StaleFinalizerCannotAffectSuccessor(t *testing.T)
func TestDelegateResourcePhaseZero_ShellStartCannotEscapeAncestorStop(t *testing.T)
func TestDelegateResourcePhaseZero_RestartDoesNotCallProvider(t *testing.T)
```

The stable-ID test calls registered `delegate` and rejects all activation/current/latest/resumed job fields plus any delegate JobRecord/output. The steering/settlement tests block actual provider requests with channels and inspect the next real `llm.Request`, not an in-memory queue. The stop test calls registered `job_stop` with desired `target=dlg_...`; schema rejection is a behavioral RED, not a compile failure. Existing `testOnly` seams pause old classification/finalization/external-start boundaries without naming future controller methods.

- [ ] **Step 3: Run every selector separately and record honest verdicts**

Run:

```bash
go test ./agent -run '^TestDelegateResourcePhaseZero_CreateReturnsOnlyStableIdentity$' -count=1 -v
go test ./agent -run '^TestDelegateResourcePhaseZero_RunningSendReachesNextProviderRequestExactlyOnce$' -count=1 -v
go test ./agent -run '^TestDelegateResourcePhaseZero_(IdleSendStartsOneSuccessor|ConcurrentIdleSendsNeverStartTwoRuns)$' -count=1 -v
go test ./agent -run '^TestDelegateResourcePhaseZero_(RegisteredStableStopCancelsSubtree|StopRacingSteerHasOneHonestOrder)$' -count=1 -v
go test ./agent -run '^TestDelegateResourcePhaseZero_(SteerBeforeCommunicateContinues|StaleFinalizerCannotAffectSuccessor)$' -count=1 -v
go test ./agent -run '^TestDelegateResourcePhaseZero_(ShellStartCannotEscapeAncestorStop|RestartDoesNotCallProvider)$' -count=1 -v
```

Expected: each command either passes as an honest preservation gate or fails a product assertion. Reject `[no tests to run]`, compile failure, timeout, or harness deadlock as evidence. Add the test name, command, exit, and failing assertion to Kata `my73`.

- [ ] **Step 4: Remove intentional failures before any foundation commit**

Delete `agent/delegate_resource_phase_zero_test.go` with `apply_patch`, then run:

```bash
go test ./agent -run '^TestDelegateResourcePhaseZero_' -count=1
git status --short
```

Expected: `[no tests to run]` is acceptable only as proof the temporary file is gone; no failing characterization test remains in the package. The tree contains only the intended docs work. Task 6 recreates final default-on causal tests together with the vertical fix.

---

### Task 2: Add one versioned delegate store, fold, and read-only reader

**Files:**
- Create: `agent/internal/delegatestore/event.go`
- Create: `agent/internal/delegatestore/record.go`
- Create: `agent/internal/delegatestore/fold.go`
- Create: `agent/internal/delegatestore/store.go`
- Create: `agent/internal/delegatestore/read_events.go`
- Create: `agent/internal/delegatestore/fold_test.go`
- Create: `agent/internal/delegatestore/store_test.go`
- Create: `agent/internal/delegatestore/read_events_test.go`
- Create: `agent/internal/delegatestore/fuzz_test.go`
- Modify: `scripts/run-fuzz.sh`

**Interfaces:**
- Consumes: durability and forensic conventions from `agent/internal/jobstore`, descriptor/sandbox inputs currently embedded in `jobstore` but without job/watch identity.
- Produces: the concrete store/fold API above at `<stateDir>/sessions/<rootID>/delegates.jsonl`.

- [ ] **Step 1: Write reducer and domain tests**

Add:

```go
func TestFoldUsesOneAggregatePerStableDelegate(t *testing.T)
func TestApplyRejectsSequenceGapAndPayloadMismatch(t *testing.T)
func TestApplyRejectsStaleGeneration(t *testing.T)
func TestApplyStopWinsOverPreparedNormalFinish(t *testing.T)
func TestApplyStopCompletionDiscardsInternalDeliveriesAndRetainsExternal(t *testing.T)
func TestApplyResumabilityClosureIsMonotonic(t *testing.T)
func TestApplyKeepsTwoUnacknowledgedDeliveriesInOrder(t *testing.T)
func TestApplyCompletedNoActionProjectsCompleted(t *testing.T)
func TestApplyAllowsOnlyOnePendingStopSequence(t *testing.T)
func TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate(t *testing.T)
func TestFoldReconstructsProjectionRevisionAcrossRestart(t *testing.T)
func TestApplyRunOpenDistinguishesUnfinishedAndSettledStopping(t *testing.T)
```

The pre-implementation compile failure is scaffolding feedback only; Task 1 owns behavioral RED evidence.

- [ ] **Step 2: Implement exact durable types and one pure reducer**

Use `uint64` sequence/generation, typed string enums, `json.RawMessage` for structured results, and an ordered delivery slice. Each aggregate also holds `ProjectionRevision uint64` and private `CurrentRunOpen bool`. Run-started sets the bit; exact run-finished clears it once. Stop request may change phase to stopping but never clears it; stop completion requires it false. After validating an event, `Apply` increments the revision once for each affected delegate whose public snapshot changes. This per-delegate counter is deterministic fold state, not the global event sequence or a control identity. `Apply` validates fully before mutating:

```go
func Fold(events []Event) (State, error) {
	state := make(State)
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			return nil, fmt.Errorf("delegate event sequence %d, want %d", event.Seq, i+1)
		}
		if err := Apply(state, event); err != nil {
			return nil, fmt.Errorf("delegate event %d: %w", event.Seq, err)
		}
	}
	return state, nil
}
```

Do not add an activation map, current/latest job IDs, epochs, unload state, a failure event, or a generic event-sourcing package.

- [ ] **Step 3: Write atomic batch/reopen/failure tests**

Add:

```go
func TestStoreAppendBatchIsOneCrashAtomicLine(t *testing.T)
func TestStoreInvalidBatchLeavesBytesSequenceAndStateUnchanged(t *testing.T)
func TestStoreAppendFailureLeavesBytesAndSequenceUnchanged(t *testing.T)
func TestStoreSyncFailureRollsBackWholeBatch(t *testing.T)
func TestOpenRecoversOnlyUnterminatedTrailingBatch(t *testing.T)
func TestOpenRejectsTerminatedMalformedBatch(t *testing.T)
func TestOpenRejectsUnknownVersion(t *testing.T)
```

Inject write/sync failures through a package-private filesystem seam. A create/start batch must never reopen as a valid create without its start.

- [ ] **Step 4: Implement the live store**

Before writing, assign the prospective contiguous sequence values, deep-clone the supplied state, and apply the entire batch to that clone. Any validation error returns without changing bytes, store sequence, or caller state. Only a fully accepted clone may be serialized as one `batchRecord`, appended with one newline, and fsynced once; return the accepted clone only after that succeeds. On write/sync failure truncate to the captured offset and fsync rollback; if rollback fails, latch the store unusable and report both errors. On reopen truncate only an unterminated trailing batch. The store retains no second fold.

- [ ] **Step 5: Add a genuinely read-only inspection path**

`ReadEvents(path)` uses `os.ReadFile`, returns nil on missing file, tolerates only an incomplete unterminated final batch, and never opens write mode, creates, truncates, or repairs. Add tests proving file bytes, mode, mtime, and existence do not change after read; malformed terminated input returns corruption.

- [ ] **Step 6: Add deterministic fuzz replay**

Add:

```go
func FuzzFold(f *testing.F)
func FuzzStoreReplay(f *testing.F)
func FuzzReadEvents(f *testing.F)
```

Assert no panic, deterministic fold, sequence/payload rejection, reopen equivalence, and no mutation by `ReadEvents`.

In the same commit, register all three native targets in the authoritative `scripts/run-fuzz.sh` manifest:

```text
native:agent:./internal/delegatestore:FuzzFold::fold.go
native:agent:./internal/delegatestore:FuzzStoreReplay::store.go
native:agent:./internal/delegatestore:FuzzReadEvents::read_events.go
```

- [ ] **Step 7: Run focused gates and commit**

Run:

```bash
gofmt -w agent/internal/delegatestore/*.go
go test ./agent/internal/delegatestore -count=20
go test -race ./agent/internal/delegatestore -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent/internal/delegatestore -run '^(Test|Fuzz)' -count=1
make fuzz-registry-check
git diff --check
git status --short
```

Explicitly stage the nine Task 2 package files plus the fuzz manifest, then commit:

```bash
git add -- agent/internal/delegatestore/event.go agent/internal/delegatestore/record.go agent/internal/delegatestore/fold.go agent/internal/delegatestore/store.go agent/internal/delegatestore/read_events.go
git add -- agent/internal/delegatestore/fold_test.go agent/internal/delegatestore/store_test.go agent/internal/delegatestore/read_events_test.go agent/internal/delegatestore/fuzz_test.go
git add -- scripts/run-fuzz.sh
git commit -m "feat: add the stable delegate store" -m "Introduce one versioned root-tree delegate log, one pure aggregate fold, crash-atomic event batches, ordered delivery state, restart-safe stop membership, and a separate non-mutating reader for doctor and cold projection. No activation, job, epoch, unload, or compatibility model is introduced."
```

Expected: focused normal/race/fuzz tests and hooks pass.

---

### Task 3: Build the dormant controller lifecycle core

**Files:**
- Create: `agent/delegate_tree_controller.go`
- Create: `agent/delegate_tree_start.go`
- Create: `agent/delegate_tree_controller_test.go`
- Create: `agent/delegate_tree_start_test.go`
- Create: `agent/delegate_tree_controller_fuzz_test.go`
- Modify: `scripts/run-fuzz.sh`

**Interfaces:**
- Consumes: `delegatestore.State`, append/fold API, existing delegate ID generator, existing tree turn/drive limits, and an injected clock/store fault seam.
- Produces: controller open, actor authorization, exact lease validation, create/idle/attention reservations, start commit/failure latch, capacity, snapshots, and immutable update plans. No production Session constructs or calls it yet.

- [ ] **Step 1: Write authorization, capacity, and append-before-fold tests**

Add:

```go
func TestDelegateControllerDirectOwnerAuthorization(t *testing.T)
func TestDelegateControllerRejectsSiblingAndVisibleDescendantMutation(t *testing.T)
func TestDelegateControllerRejectsStaleActorLease(t *testing.T)
func TestDelegateControllerReservationHoldsCapacity(t *testing.T)
func TestDelegateControllerConcurrentIdleReservationsChooseOne(t *testing.T)
func TestDelegateControllerAppendFailureLeavesDurableAndLiveStateUnchanged(t *testing.T)
func TestDelegateControllerRejectedBatchLeavesBytesSequenceAndFoldUnchanged(t *testing.T)
func TestDelegateControllerSnapshotReturnsOneStableRow(t *testing.T)
func TestDelegateControllerMutationReturnsCapturedSnapshot(t *testing.T)
func TestDelegateControllerCapturedSnapshotsCarryMonotonicRevision(t *testing.T)
```

Use a real temporary `delegatestore.Store`. The captured-snapshot test mutates again after the first call returns and proves the first `delegateUpdatePlan` still contains the earlier state and lower durable `revision`, while the second snapshot has the next revision.

- [ ] **Step 2: Implement one durable working fold plus one live sidecar**

`c.durable` is the sole in-memory copy of the persisted fold. `c.live` holds only exact runtime pointer/lease, pending transcript-entry IDs, generation-keyed waiter map, recovery latch, and activity time. Implement:

```go
func (c *delegateTreeController) appendLocked(events ...delegatestore.Event) ([]delegatestore.Event, error) {
	appended, next, err := c.store.AppendBatch(c.durable, events)
	if err != nil {
		return nil, err
	}
	c.durable = next
	return appended, nil
}
```

The store preflights the assigned batch through the same reducer against a transient deep clone before any write. A rejected transition therefore cannot become durable or partially mutate `c.durable`; the controller only swaps in the returned clone after fsync. No second reducer or mirror may update lifecycle.

- [ ] **Step 3: Implement public actor authorization and exact internal lease checks**

Root actors directly control root children. Delegate actors must carry the exact active lease and directly own the target. Separate identity from admission:

```go
func (c *delegateTreeController) exactLeaseLocked(lease delegateLease) (*delegatestore.Aggregate, *delegateLiveState, error)
func (c *delegateTreeController) admitLeaseLocked(lease delegateLease, phases ...delegatestore.Phase) (*delegatestore.Aggregate, *delegateLiveState, error)
```

`admitLeaseLocked` rejects `closing`, pending-stop ancestry, `recoveryRequired`, wrong phase, and stale lease. Exact finish later uses `exactLeaseLocked` so stop cannot prevent its covered generation from settling.

- [ ] **Step 4: Implement process-only reservation tokens and capacity**

`ReserveCreate` mints an unexposed stable ID, derives deterministic transcript/worktree locations without writing them, and creates a starting reservation. `ReserveStart` marks a durable idle delegate starting only in process-local reservation state; it does not increment durable generation. Both reserve turn/drive capacity immediately and carry construction cancellation. `AbortStart` releases exactly one pre-commit reservation. No child filesystem side effect is permitted while only a process-local reservation exists.

- [ ] **Step 5: Implement runtime-identity attention reservation**

`ReserveAttention(runtime, attentionID)` is not a public actor call. Its only caller immediately folds the receiver transcript outside `c.mu` and passes one exact pending ID from that runtime. Under `c.mu`, verify `c.live[id].runtime == runtime`, require the delegate idle/resumable with no pending stop, and reserve drive capacity for that exact ID without transcript I/O. Resolution cannot race this idle proof except through a stop, and stop admission makes the reservation fail. After restart, no attention can start until lazy restore installs the selected runtime and the helper re-folds that exact transcript.

- [ ] **Step 6: Commit durable ownership before construction or input**

For create, `CommitStart` appends `created+run_started` as one batch before any transcript, worktree, environment, or child-state write. For idle it appends `run_started`. In both cases it folds running and transfers the reservation cancellation into an exact non-launched live binding with `runtime=nil, ready=false`. The caller then constructs or restores only deterministic paths owned by that aggregate, calls `AttachRuntime`, and invokes `AdmitStartInput` under the narrow controller-to-transcript lock order; successful admission sets `ready=true`. Until then only exact finish and stop are admitted; steer, model, tool, child, and shell admission return busy. An attention trigger already owns an exact pending `AttentionID` proven by a read-only receiver-transcript fold and a resident runtime, so its commit installs that exact runtime ready without appending a second input. A crash before commit loses only the reservation; a crash after commit leaves a durable aggregate that owns every partial artifact and reconciles failed/runtime_lost.

If construction fails, exact `FinishGeneration` settles failed without provider launch. If input admission fails, `AdmitStartInput` attempts the same canonical atomic terminal prepare-and-finish batch used by every non-settling failed generation:

```go
if err := admitInput(); err != nil {
	terminal, finish := inputPersistFailureBatch(id, generation, err)
	if _, finishErr := c.appendLocked(terminal, finish); finishErr != nil {
		live.recoveryRequired = true
		return capturedRunningPlansLocked(state), errors.Join(err, finishErr)
	}
	c.releaseGenerationLocked(id, generation)
	return capturedFailedPlansLocked(state), err
}
```

The batch contains the bounded `terminal_error` owner packet and the single `delegate_run_finished` failed/input_persist_failed outcome, so restart delivery uses the canonical shape. The double-failure path keeps cancellation/binding/capacity, launches no provider, rejects later admission, and remains stoppable/restart-repairable. Once `CommitStart` succeeds, the registered tool serializes any later construction/input failure as an ordinary structured result containing stable ID, lifecycle, last outcome, and reason; it does not return a Go/tool transport error that would hide the durable resource. Only pre-commit failure may return an ordinary tool error.

- [ ] **Step 7: Fault-inject created and run-started boundaries**

Add:

```go
func TestDelegateControllerCreatedAppendFailurePublishesNothing(t *testing.T)
func TestDelegateControllerRunStartedAppendFailureDoesNotInstallBinding(t *testing.T)
func TestDelegateControllerCrashBeforeCreateCommitLeavesNoChildArtifacts(t *testing.T)
func TestDelegateControllerCrashAfterCreateCommitReconcilesOwnedPartialArtifacts(t *testing.T)
func TestDelegateControllerCommittedUnreadyStartAdmitsOnlyStopOrFinish(t *testing.T)
func TestDelegateControllerInputAndCompensatingFinishFailureKeepsExactBinding(t *testing.T)
func TestDelegateControllerStopCanSettleRecoveryRequiredStart(t *testing.T)
```

Mutation assertions cover store bytes, fold, live map, capacity, update plans, and launch count.

- [ ] **Step 8: Fuzz controller transition invariants**

`FuzzDelegateControllerTransitions` generates reserve/abort/commit/finish stubs and asserts generation monotonicity, at most one exact binding, capacity equals reservations+active generations, closed resumability never reopens, persisted fold equals `c.durable`, and process IDs are never used as durable correlation.

Register `native:agent:.:FuzzDelegateControllerTransitions::delegate_tree_controller.go;delegate_tree_start.go` in `scripts/run-fuzz.sh` in the same commit.

- [ ] **Step 9: Prove the dormant controller has no active production caller**

Add a source/call-graph assertion that production files outside the new dormant controller do not construct it or call its lifecycle methods. Do not modify `Session`, `treeCounter`, registered tools, or job activity in this task. The explicit inherited `*jobActivityClock` replacement happens inside the atomic Task 6 cutover; until then, current production remains byte-for-byte unchanged.

- [ ] **Step 10: Run focused gates and commit**

Run:

```bash
gofmt -w agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_controller_test.go agent/delegate_tree_start_test.go agent/delegate_tree_controller_fuzz_test.go
go test ./agent -run '^TestDelegateController' -count=20
go test -race ./agent -run '^TestDelegateController' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateControllerTransitions$' -count=1
make fuzz-registry-check
git diff --check
git status --short
```

Explicitly stage only the five dormant Task 3 package files plus the fuzz manifest, then commit:

```bash
git add -- agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_controller_test.go agent/delegate_tree_start_test.go agent/delegate_tree_controller_fuzz_test.go
git add -- scripts/run-fuzz.sh
git commit -m "feat: add the delegate controller core" -m "Add one dormant root-tree lifecycle controller with direct-owner authorization, exact leases, process-only reservations, capacity accounting, append-before-fold mutation, immutable update plans, and an honest recovery latch for start/input double failure. The active delegate runtime remains unchanged until the vertical cutover."
```

Expected: focused normal/race/fuzz/activity tests and hooks pass.

---

### Task 4: Build dormant steering, settlement, exact finish, and ordered delivery

**Files:**
- Create: `agent/delegate_tree_steer.go`
- Create: `agent/delegate_tree_finish.go`
- Create: `agent/delegate_delivery.go`
- Create: `agent/delegate_tree_steer_test.go`
- Create: `agent/delegate_tree_finish_test.go`
- Create: `agent/delegate_delivery_test.go`
- Create: `agent/delegate_conversation_fuzz_test.go`
- Modify: `scripts/run-fuzz.sh`

**Interfaces:**
- Consumes: exact live binding, concrete child Session transcript/history methods supplied by test fixtures, `delegatestore.TerminalPacket`, ordered delivery fold, and controller update plans.
- Produces: durable steer, request/tool admission, settlement arbitration, prepared terminal, exact finish, waiter ownership, idempotent delivery, and acknowledgement. Still dormant from registered tools.

- [ ] **Step 1: Write durable steering and request-binding tests**

Add:

```go
func TestDelegateControllerSteerPersistsBeforeAcknowledgement(t *testing.T)
func TestDelegateControllerSteerAppendFailureIsNotAccepted(t *testing.T)
func TestDelegateControllerSteerUpdatesActivityWithoutStateRevision(t *testing.T)
func TestDelegateControllerBeginModelRequestBindsPendingEntriesOnce(t *testing.T)
func TestDelegateControllerSteerAfterRequestBindWaitsForNextRequest(t *testing.T)
func TestDelegateControllerBeginToolRejectsStoppingOrStaleLease(t *testing.T)
```

Use a concrete fake runtime whose transcript method asserts controller-to-transcript lock order and returns captured history. Do not add an arbitrary admission callback to `Steer`.

- [ ] **Step 2: Implement steering and exact model/tool admission**

Under `c.mu`, `Steer` authorizes direct ownership, validates running exact binding, invokes `live.runtime.session.appendDelegateSteeringDurably`, records only the durable entry ID in `pendingSteers`, sets `live.latestAt` from the admitted transcript-entry timestamp, and returns an update plan with the unchanged state revision so clients can max-merge activity. `BeginModelRequest` validates lease, snapshots history under the permitted one-way lock order, and clears only pending IDs included in that snapshot. `BeginTool` validates before pre-tool hooks.

- [ ] **Step 3: Write settlement and terminal preparation tests**

Add:

```go
func TestDelegateControllerNormalSettlementDefersEarlierSteer(t *testing.T)
func TestDelegateControllerCommunicateSettlementDefersEarlierSteer(t *testing.T)
func TestDelegateControllerNormalSettlementPreparesMissingTerminal(t *testing.T)
func TestDelegateControllerCrashAfterNormalSettlementFinishesPreparedOnce(t *testing.T)
func TestDelegateControllerFatalFinishPreparesAndFinishesAtomically(t *testing.T)
func TestDelegateControllerTerminalPreparedAppendFailureKeepsRunning(t *testing.T)
func TestDelegateControllerInputPersistFailureUsesCanonicalAtomicFinish(t *testing.T)
func TestDelegateControllerStopOverridesPreparedNormalPacket(t *testing.T)
func TestDelegateControllerStoppedFinishRemainsStoppingWithDiagnostic(t *testing.T)
func TestDelegateControllerStopCompletedClearsPreparedDiagnostic(t *testing.T)
```

If an earlier steer is pending, return `continueRun=true`, append no terminal event, and keep running. Otherwise `BeginSettlement` must materialize either the accepted communicate packet or the bounded missing-terminal packet, append it, and fold settling. A settling aggregate without a prepared packet is invalid. Fatal/exhausted/cancelled completion while running uses one validated atomic batch containing `terminal_prepared` and `run_finished` so restart cannot observe the intermediate state; attention `completed_no_action` needs no packet and finishes directly.

- [ ] **Step 4: Implement canonical packets and exact finish**

Preserve raw structured JSON value distinctions and existing size/schema bounds. `FinishGeneration` uses `exactLeaseLocked`, treats stale lease as a no-op, resolves stop precedence, requires a prepared packet for settling, atomically prepares any non-settling terminal packet with its finish, releases capacity, and captures the immutable update plus a delivery plan only if the packet is now the ordered head. A stop that covers settling moves phase to stopping but retains the same prepared packet as diagnostic-only evidence. Stopped finish records the stop-selected outward outcome and remains durable stopping; stop-completed clears the diagnostic and folds idle. A normal finish clears its prepared packet and becomes idle.

- [ ] **Step 5: Keep private disposition separate from public outcome**

An attention run with no report writes `DispositionCompletedNoAction` and `OutcomeCompleted`, creates no delivery, and never serializes `completed_no_action` through tool/AppWire/client status. A user-triggered run without accepted communicate writes failed/missing-terminal plus a bounded terminal-error packet.

- [ ] **Step 6: Implement deterministic delivery identity and ordered pending state**

`delegateDeliveryID(id,generation)` is the sole durable delivery key. `FinishGeneration` appends one delivery without removing older pending entries. Add:

```go
func TestDelegateControllerTwoGenerationsCanFinishBeforeFirstAck(t *testing.T)
func TestDelegateControllerSecondDeliveryWaitsForFirstLiveAck(t *testing.T)
func TestDelegateControllerBlockedFirstDeliveryPreservesSecondInlineWaiter(t *testing.T)
func TestDelegateControllerInlineTimeoutWithdrawsBeforeHeadClaim(t *testing.T)
func TestDelegateControllerHeadClaimWinsInlineTimeout(t *testing.T)
func TestDelegateControllerNextHeadClaimWinsInlineTimeout(t *testing.T)
func TestDelegateControllerInlineClaimFailureFallsBackAndResolvesWaiter(t *testing.T)
func TestDelegateControllerInlineHandoffDoesNotAcknowledgeBeforeReceiverCommit(t *testing.T)
func TestDelegateControllerInlineCommitFailureLeavesNAndNPlusOneQueued(t *testing.T)
func TestDelegateControllerInlineCommitReleasesNPlusOneOnlyAfterN(t *testing.T)
func TestDelegateControllerInlineReplayAfterReceiverCommitIsIdempotent(t *testing.T)
func TestDelegateControllerBeginDeliveryCreatesOneExactReceipt(t *testing.T)
func TestDelegateControllerFailedDeliveryCompletionLeavesHeadPending(t *testing.T)
func TestDelegateControllerCommittedDeliveryCompletionAcknowledgesExactHead(t *testing.T)
func TestDelegateControllerDeliveryAckRemovesOnlyExactID(t *testing.T)
func TestDelegateControllerRestartReplaysTwoDeliveriesInOrder(t *testing.T)
func TestDelegateControllerRestartThenFinishUsesNewDeliveryID(t *testing.T)
```

`FinishGeneration` returns a delivery plan only when the appended packet is the ordered head and its sender is not already covered by a pending stop. A later finish appends behind it but dispatches nothing. A stop-selected finish creates no delivery when its owner delegate belongs to the same stop; it queues, but does not dispatch, a packet owed to the root or an owner outside that stop. Each start reservation optionally carries a waiter; commit transfers it into `live.waiters[generation]`. When a head plan is made, the controller atomically deletes that exact waiter from the map and transfers sole ownership into the plan. The waiter receives the packet plus `delegateToolResultCommit`, not a final delivery acknowledgement. `CompleteDelivery(committed=true)` accepts only the exact admitted head, removes it durably, and claims the next entry's generation-keyed waiter the same way even if a successor lease is current. `committed=false` removes only the process receipt and leaves N at the head. This commit-then-acknowledge chain owns live and restart delivery, so generation N+1 cannot dispatch before N's receiver commit or lose its inline claimant.

- [ ] **Step 7: Serialize inline timeout against finish**

At most one starting call registers `delegateInlineWaiter` for the exact generation. Timeout withdraws only if the exact pointer remains in `live.waiters[generation]` under `c.mu`, then returns running. If absent because head dispatch already claimed it, timeout waits on the waiter's buffered resolution and does not return first. Post-unlock dispatch must resolve every claim immediately with either packet+private commit handoff or `fallback=true`; fallback makes the tool return running and leaves the same durable head pending for notification/replay. The handoff itself does not acknowledge delivery. Apply the same rule when N acknowledgement claims N+1. Use barriers to prove both lock orders and claim-failure resolution; never use sleeps.

- [ ] **Step 8: Implement receiver idempotency and acknowledgement**

`deliverDelegatePacket` executes after unlock for the head only by first calling `BeginDelivery`. Under `c.mu`, that method revalidates that the exact packet is still head, returns `admitted=false` when the sender or delegate receiver is covered by a pending stop, or creates one process-local delivery receipt. The caller resolves a claimed waiter with fallback on `admitted=false`; otherwise it hands the inline waiter packet+`delegateToolResultCommit`, or idempotently appends the packet's stable `<delegate-notification>` content as a model-bound attention turn carrying private `AttentionID=delegate:<deliveryID>`. Background attention calls `CompleteDelivery(true)` only after that transcript append fsyncs. Inline delivery does not call `CompleteDelivery` here: the caller Session's existing aggregated tool-result persistence boundary owns that call after fsync. `committed=false` removes the receipt and leaves the durable head pending, while `committed=true` removes the receipt, appends the exact acknowledgement, and releases only the next ordered plan. Stop waits for every pre-admitted receipt whose sender or receiver intersects its subtree. Nested parent replay uses its durable transcript reference directly when no parent runtime exists; it constructs no provider/Session. A crash between receiver commit and acknowledgement replays the same head, finds private delivery/attention metadata, and acknowledges without duplicate input. Since stop request and receipt creation serialize on `c.mu`, delivery is either pre-stop admitted and drained/cleaned or cannot enter the stopped receiver.

- [ ] **Step 9: Fault-inject prepared, finished, and acknowledged boundaries**

Add exact append-failure tests for `delegate_terminal_prepared`, the crash-atomic prepare+finish batch, `delegate_run_finished`, and `delegate_delivery_acknowledged`. Each asserts fold/live/capacity/generation-keyed waiter/delivery/output state and proves no post-unlock plan claims an uncommitted transition or durable settling-without-packet state.

- [ ] **Step 10: Fuzz conversation transitions**

`FuzzDelegateConversationTransitions` generates steer/bind/settle/finish/ack sequences and asserts no accepted steer is lost, no entry binds twice, durable settling always has exactly one prepared packet, stale generations cannot mutate, stop precedence is monotonic, delivery IDs and waiters remain generation-keyed, and public outcomes exclude private dispositions.

Register `native:agent:.:FuzzDelegateConversationTransitions::delegate_tree_steer.go;delegate_tree_finish.go;delegate_delivery.go` in `scripts/run-fuzz.sh` in the same commit.

- [ ] **Step 11: Run focused gates and commit**

Run:

```bash
gofmt -w agent/delegate_tree_steer.go agent/delegate_tree_finish.go agent/delegate_delivery.go agent/delegate_tree_steer_test.go agent/delegate_tree_finish_test.go agent/delegate_delivery_test.go agent/delegate_conversation_fuzz_test.go
go test ./agent -run '^TestDelegateController' -count=20
go test -race ./agent -run '^TestDelegateController' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateConversationTransitions$' -count=1
make fuzz-registry-check
git diff --check
git status --short
```

Explicitly stage the seven Task 4 package files plus the fuzz manifest, then commit:

```bash
git add -- agent/delegate_tree_steer.go agent/delegate_tree_finish.go agent/delegate_delivery.go agent/delegate_tree_steer_test.go agent/delegate_tree_finish_test.go agent/delegate_delivery_test.go agent/delegate_conversation_fuzz_test.go
git add -- scripts/run-fuzz.sh
git commit -m "feat: add exact delegate conversation lifecycle" -m "Add dormant durable steering, exact model/tool admission, settlement arbitration, prepared terminal state, one finish shape, deterministic ordered delivery, inline waiter ownership, and idempotent acknowledgement. Public outcomes remain independent from private attention disposition."
```

Expected: focused normal/race/fuzz tests and hooks pass.

---

### Task 5: Build dormant shell receipts, globally serialized stop, and pure restart repair

**Files:**
- Create: `agent/delegate_tree_work.go`
- Create: `agent/delegate_tree_stop.go`
- Create: `agent/delegate_tree_restore.go`
- Create: `agent/delegate_shell_repair.go`
- Create: `agent/delegate_tree_work_test.go`
- Create: `agent/delegate_tree_stop_test.go`
- Create: `agent/delegate_tree_restore_test.go`
- Create: `agent/delegate_shell_repair_test.go`
- Create: `agent/delegate_tree_restart_fuzz_test.go`
- Modify: `agent/delegate_delivery.go`, `delegate_delivery_test.go` to make delivery receipts intersect stop membership and completion
- Modify: `scripts/run-fuzz.sh`

**Interfaces:**
- Consumes: stable parent graph, exact runtime bindings, starting reservations, controller capacity, deterministic stop request sequence, ordered delivery, and immutable externally collected shell evidence.
- Produces: shell work tokens, one pending stop, exact cancellation plan, stop completion, provider-free post-unlock shell-store repair, provider-free restart repair, and lazy-restore prerequisites. Still dormant from registered tools.

- [ ] **Step 1: Write exact shell-work receipt tests**

Add:

```go
func TestDelegateControllerShellReceiptHoldsStopOpen(t *testing.T)
func TestDelegateControllerCommitShellWorkAfterStopCancelsImmediately(t *testing.T)
func TestDelegateControllerShellFinishRequiresTokenAndJobID(t *testing.T)
func TestDelegateControllerAbortShellReceiptReleasesOnce(t *testing.T)
```

`CommitShellWork(token,jobID,cancel)` converts one pending token into one committed entry indexed by that same token and job ID. `ReportShellFinished` must match both. Do not gather an unrelated shell set later.

- [ ] **Step 2: Write global stop ordering and retry tests**

Add:

```go
func TestDelegateControllerStopPersistsBeforeCancellationPlan(t *testing.T)
func TestDelegateControllerSameTargetStopRetryJoins(t *testing.T)
func TestDelegateControllerDifferentTargetStopIsBusy(t *testing.T)
func TestDelegateControllerCoveringAndIntersectingStopAreBusy(t *testing.T)
func TestDelegateControllerSuccessorWaitsForStopCompletion(t *testing.T)
func TestDelegateControllerRestartThenStopUsesNewRequestSequence(t *testing.T)
func TestDelegateControllerStopRescansCancellationAttentionBeforeCompletion(t *testing.T)
func TestDelegateControllerStopPreservesOwnerDeliveryOutsideSubtree(t *testing.T)
func TestDelegateControllerStopSuppressesCoveredOwnerDelivery(t *testing.T)
func TestDelegateControllerStopDefersExternalOwnerDeliveryUntilCompletion(t *testing.T)
func TestDelegateControllerIdleStopWithPendingDeliveryPersistsAndSuppresses(t *testing.T)
func TestDelegateControllerDeliveryReceiptBeforeStopDrainsAndCleans(t *testing.T)
func TestDelegateControllerStopBeforeDeliveryReceiptDefersAdmission(t *testing.T)
```

The stop-request event's assigned sequence is the private stop identity. One `c.stop` exists. Same target returns the same completion channel/sequence; every different target is busy until completion.

- [ ] **Step 3: Implement the locked stop phase**

Authorize and collect target+descendants from durable edges, then always append stop requested—even when every generation appears idle. Fold current members stopping, invalidate starting reservations, fence all admission, and capture one immutable plan containing exact leases/cancels, child runtimes, and exact shell work token+job pairs. The common reconciliation loop proves shell notifications, attention, receipts, and owner deliveries empty or correctly suppressed/deferred before completion. A no-work operation may report `previous_lifecycle=idle`, but there is no non-durable `already_idle` branch. Do not re-resolve stable IDs after unlock.

- [ ] **Step 4: Implement post-unlock cancellation and exact attention cleanup**

Execute construction/provider/process cancellation, leaf-first child requests, and shell-manager stop outside `c.mu`. Exact finish/report methods drain `active`, `starts`, `work`, and intersecting pre-admitted delivery receipts. Once those sets are empty, snapshot only exact shell-store paths, receiver transcript references, and the evidence version under `c.mu`, then release it. Fold each receiver transcript read-only outside the lock to obtain exact pending `AttentionID` values. Any still-running shell or covered pending shell notification yields a post-unlock shell repair plan; every covered pending attention ID gets an idempotent fsynced discarded marker, also outside the lock and without a model. Report/recollect after every repair or resolution and repeat the cold transcript fold because cancellation can commit more attention. A stale evidence version or replaced runtime cannot satisfy cleanup; shell append or attention-marker append failure keeps the stop pending, and no provider/Session is constructed.

Pending owner deliveries use receiver-aware stop rules. While a sender remains a member of a pending stop, dispatch admits no new packet. The final `stop_completed` fold discards deliveries whose owner delegate is covered by the same stop, retains deliveries owed to the root or an owner outside the subtree, and only then releases the oldest retained external head. `BeginDelivery` serializes its process-local receipt with stop under `c.mu`; the receiver-transcript append remains outside the lock, and stop completion waits for every receipt whose sender or receiver intersects the subtree. Delivery therefore either holds a pre-stop receipt that stop drains and whose committed `AttentionID` the repeated transcript fold resolves, or cannot enter the covered owner. A post-unlock plan never performs an unaccounted attention append.

Stop completion occurs only when the latest immutable evidence has no covered running shell, pending shell notification, or pending attention ID and one final locked rescan sees empty active/start/work/delivery-receipt sets, then appends `stop_completed` in that same critical section. The controller rejects evidence captured before any intervening relevant mutation. Append failure keeps admission closed and the same stop retryable. The daemon steering queue and `<stateDir>/queues/*.json` snapshot provide neither this evidence nor lifecycle authority.

- [ ] **Step 5: Make root close stronger without overlapping stop algebra**

Under lock set `closing=true`, rejecting all new work and delivery receipts tree-wide. Outside lock join/drain the one pending stop if present, then initiate whole-tree teardown stop, bounded receipt/work drain, postorder child close, and store close. Do not manufacture a second overlapping `delegateStopState`.

- [ ] **Step 6: Fault-inject stop request/completion and resumability closure**

Add append failure tests for `delegate_subtree_stop_requested`, `delegate_subtree_stop_completed`, and `delegate_resumability_closed`. Request failure emits/cancels nothing. Completion failure keeps admission closed and retryable. Resumability failure performs no worktree/runtime destruction.

- [ ] **Step 7: Define immutable restart evidence collection**

Add:

```go
type delegateReconcileEvidence struct {
	evidenceVersion uint64
	shells          map[string]shellRuntimeLossEvidence
	attention       map[string][]string
}

func collectDelegateReconcileEvidence(stateDir string, requirements delegateReconcileRequirements) (delegateReconcileEvidence, error)
func executeDelegateShellRepair(plan delegateShellRepairPlan, now time.Time) error
func (c *delegateTreeController) ReconcileRequirements() delegateReconcileRequirements
```

`ReconcileRequirements` snapshots only durable shell-store paths, delegate transcript references, and a process-only `evidenceVersion` under `c.mu`. Any relevant active/start/work/delivery/attention change increments that version. `ReportShellFinished` runs only after the exact terminal generation and its pending notification source are durable, and increments the version. `collect...` preserves it while using `jobstore.ReadEvents` plus missing-file-tolerant read-only transcript folds outside the lock to capture exact running shell IDs, pending shell notification identities, and pending `AttentionID` values. It reads each shell source store before the corresponding receiver transcript: because source acknowledgement follows receiver fsync, this order cannot observe both sides absent during a handoff. A missing transcript is empty evidence and is not created. `Reconcile(evidence)` rejects a version mismatch, takes no `Session`, opens no shell/transcript store under the lock, and only validates/appends/folds/captures exact cleanup and `delegateShellRepairPlan` values. The version is optimistic evidence validation, never a durable lifecycle/control identity.

`executeDelegateShellRepair` runs after unlock and constructs no provider or child Session. It opens the exact shell store for append, re-folds it to reject stale evidence, and uses the existing shell-only job reconciliation rules to append `job_finished(stopped/runtime_lost)` for each exact still-running shell. For a shell owned by a pending stopped subtree it atomically consumes, rather than arms, that terminal notification; it also consumes a preexisting matching pending notification and clears shell-only terminal watches. Outside a pending stop it preserves the ordinary pending-notification contract. The helper is idempotent by job ID plus terminal generation. Any append failure leaves the controller stop pending. The caller closes the shell store, recollects read-only evidence, and calls `Reconcile` again; stop completion requires no running shell or covered pending shell notification in the final evidence.

- [ ] **Step 8: Implement restart repair without model construction**

In sequence order: current-run-open running becomes failed/runtime_lost (or stopped-by-parent if covered), open settling completes from its prepared packet and enables transcript repair, and a stopping member with `CurrentRunOpen=false` is never finished, delivered, or capacity-released again. Process-local delivery receipts vanish on crash; their durable heads remain pending and receiver idempotency handles any already-committed transcript entry. Running-shell/pending-notification evidence produces exact post-unlock shell-store repair plans, and the cold receiver-transcript fold produces exact discarded-marker plans. The caller performs those plans without constructing a Session/provider, recollects read-only evidence, and calls `Reconcile` until a final locked empty-evidence check commits stop completion, discards covered-owner packets, and only then releases the oldest external pending delivery (each acknowledgement releases the next). Resumability remains monotonic. No provider, model client, hook, worktree mutation, child Session, transcript mutation, notification delivery, or shell-store append runs under the controller lock.

- [ ] **Step 9: Write depth-three restart and collision tests**

Add:

```go
func TestDelegateControllerRestartThreeLevelTreeIsProviderFree(t *testing.T)
func TestDelegateControllerRestartRepairsPreparedTerminalOnce(t *testing.T)
func TestDelegateControllerRestartCompletesStopBeforeAdmission(t *testing.T)
func TestDelegateControllerRestartAfterStoppedFinishDoesNotFinishTwice(t *testing.T)
func TestDelegateControllerRestartCleansAttentionWithoutRuntime(t *testing.T)
func TestDelegateControllerRestartRepairsDescendantShellBeforeStopCompletion(t *testing.T)
func TestDelegateControllerReconcileRejectsStaleExternalEvidence(t *testing.T)
func TestDelegateControllerRestartPreservesOrderedDeliveries(t *testing.T)
func TestDelegateControllerRestartDefersExternalStopDeliveryUntilCompletion(t *testing.T)
func TestDelegateControllerRestartIdleStopFencesQueuedCoveredDelivery(t *testing.T)
func TestDelegateControllerRestartCannotCollideStopOrDeliveryIdentity(t *testing.T)
func TestDelegateShellRepairAppendsRuntimeLostAndConsumesCoveredNotification(t *testing.T)
func TestDelegateShellRepairPreservesNotificationOutsideStop(t *testing.T)
func TestDelegateShellRepairAppendFailureKeepsStopPending(t *testing.T)
func TestDelegateShellRepairIsIdempotentAfterReopen(t *testing.T)
```

- [ ] **Step 10: Fuzz restart equivalence**

`FuzzDelegateRestartEquivalence` compares uninterrupted controller state with store-close/read/fold/reconcile for valid generated histories, dropped process-local delivery receipts, and immutable shell evidence. It asserts no external constructor/provider callback is invoked and covered-owner delivery never survives stop completion.

Register `native:agent:.:FuzzDelegateRestartEquivalence::delegate_tree_work.go;delegate_tree_stop.go;delegate_tree_restore.go;delegate_shell_repair.go` in `scripts/run-fuzz.sh` in the same commit.

- [ ] **Step 11: Run focused gates and commit**

Run:

```bash
gofmt -w agent/delegate_tree_work.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_shell_repair.go agent/delegate_delivery.go agent/delegate_tree_work_test.go agent/delegate_tree_stop_test.go agent/delegate_tree_restore_test.go agent/delegate_shell_repair_test.go agent/delegate_delivery_test.go agent/delegate_tree_restart_fuzz_test.go
go test ./agent -run '^TestDelegate(Controller|ShellRepair)' -count=20
go test -race ./agent -run '^TestDelegate(Controller|ShellRepair)' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateRestartEquivalence$' -count=1
make fuzz-registry-check
git diff --check
git status --short
```

Explicitly stage the eleven Task 5 package files plus the fuzz manifest, then commit:

```bash
git add -- agent/delegate_tree_work.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_shell_repair.go agent/delegate_delivery.go agent/delegate_tree_work_test.go agent/delegate_tree_stop_test.go agent/delegate_tree_restore_test.go agent/delegate_shell_repair_test.go agent/delegate_delivery_test.go agent/delegate_tree_restart_fuzz_test.go
git add -- scripts/run-fuzz.sh
git commit -m "feat: add delegate subtree stop and restart repair" -m "Add exact shell work and delivery receipts, one globally serialized subtree stop identified by durable request sequence, immutable post-unlock cancellation, quiescent completion, provider-free shell-store repair, and restart recovery from externally collected read-only evidence."
```

Expected: focused normal/race/fuzz tests and hooks pass. The old production route is still the only registered route at this checkpoint.

---

## Task 6–14 integration-defect ownership

Each row is a required behavioral RED before its owner slice and a count-20 normal/race proof in that task and Task 14. The named test is the causal owner; broader gates do not substitute for it.

| Abandoned-cutover defect or preservation risk | Owner | Causal test |
|---|---|---|
| Descendant ordinary events disappeared when spawn config was replaced | Task 6 | TestDelegateResourceCreate_DescendantEventCallbackSurvivesSpawnConfig |
| Worktree/sandbox lifecycle still depended on delegate JobRecords | Task 9 | TestStableDelegateWorktree_SandboxRestoreUsesDescriptorNotLegacyJob |
| Delegate-owned shell completion attention and ancestor visibility disappeared | Task 8 | TestStableDelegateShell_CompletionAttentionReachesDirectOwner and TestStableDelegateShell_AncestorCanSeeDescendantShell |
| Post-commit construction failure published false resumability | Task 6 | TestDelegateResourceCreate_PostCommitConstructionFailureClosesResumability |
| Auto-nudge, SubagentStop, quiet supervision, and final-round salvage disappeared | Task 7 | TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin, TestDelegateResourceSupervision_SubagentStopBlockingStartsOneContinuation, TestDelegateResourceSupervision_QuietWatchdogUsesTenMinuteThresholdAndThirtySecondChecks, and TestDelegateResourceSupervision_FinalRoundFailedSalvageAddsResumeHint |
| max_retained_terminal reclamation disappeared | Task 9 | TestDelegateRuntimeReclaim_UsesPublicMaxRetainedTerminalDefault2048 |
| Positive stop wait cancelled the sole reconciliation driver | Task 7 | TestDelegateResourceRuntime_PositiveStopWaitKeepsReconciliationDriverAlive |
| Foreground shell timeout leaked its controller receipt | Task 9 | TestDelegateResourceStop_ForegroundShellTimeoutAbortsUncommittedReceipt |
| to=caller appended into an unfinished root tool round | Task 7 | TestDelegateResourceRuntime_CallerCannotWriteIntoUnfinishedRootToolRound |
| Explicit null, validation, exhaustion, worktree, slots, timing, usage, or stable diagnostics were dropped | Tasks 7, 10, 11 | TestDelegateResourceRuntime_TerminalPacketPreservesTaskModelEffortTimingUsageAndWorktree, TestStableDelegateTools_ListPreservesTurnSlotsAllowanceAndWatchDiagnostics, and TestDelegateProjection_PreservesNullValidationExhaustionAndTurnSlots |
| Call-scoped wait_ignored_reason was dropped from delegate_send or copied into stable delegate state | Tasks 10, 11 | TestStableDelegateTools_WaitIgnoredReasonIsOwnField and the Task 11 jobTools/subagent-module Vitest cases |
| Historical reads mutated logs | Task 10 | TestStableDelegateReadOnly_FileBytesAndMetadataRemainUnchanged |
| Backend cutover outpaced Hub/TUI/web/doctor/prompt consumers | Tasks 10, 11, 13 | TestDoctorStableDelegatePreservesShellAndWatchDiagnostics, TestDelegateProjection_DescendantOrdinaryEventsReachRootTransport, TestTUIStableDelegateWatchAndObserverNoticesRemainVisible, the exact Task 11 Vitest cases, and TestBundledDelegatePromptPreservesWatchSupervisionAndShellGuidance |
| Stable watches/observer sidecars were mistaken for deletion targets | Task 8 | TestStableDelegateWatch_PreservesFiltersEveryCoalescingAndBudget and TestStableDelegateObserver_EmitsExactlyOneControllerTerminalPacket |

---

### Task 6: Flag-day authority/bootstrap and stable create/restore

**Status:** Complete at immutable commit `5df4ad5f487f3674f4016f40eeb82d3cf49b7aa4`. Final Task 6 verification and sequential review closure are recorded in `.superpowers/sdd/2026-08-12-delegate-resource-recovery/task-6-stabilization-report.md`.

**Files:**

- Create: agent/delegate_runtime.go, agent/delegate_legacy_state.go, agent/delegate_resource_bootstrap_test.go, agent/delegate_resource_create_test.go.
- Modify: agent/session.go, agent/session_config.go, agent/session_init.go, agent/session_lifecycle.go, agent/session_lifecycle_test.go.
- Modify: agent/delegate_tree_controller.go, agent/delegate_tree_controller_test.go, agent/delegate_tree_start.go, agent/delegate_tree_start_test.go, agent/delegate_tree_restore.go, agent/delegate_tree_restore_test.go.
- Modify: agent/job_delegate.go, agent/subagents.go, agent/subagent_manager.go, agent/sandbox_delegate.go.
- Modify: agent/session_tools.go, agent/session_tool_registry.go, agent/internal/tool/definitions.go, agent/internal/tool/definitions_test.go.
- Modify: agent/session_tools_worktree.go, agent/session_worktree_relock.go, agent/session_worktree_resume.go, agent/session_worktree_sweep.go.
- Modify: agent/events/events.go, agent/events/payloads.go, agent/events/eventdata.go.

**Interfaces:**

- Consume the exact Task 1–5 controller and store interfaces listed above.
- Produce one root-owned controller/store bootstrap, inherited child controller plus immutable stable owning delegate ID, generation-scoped run leases, a provider-free cold restore coordinator, legacy_delegate_state and legacy_delegate_watch_state guards, and the registered create path returning one stable dlg_ identity.
- delegateRuntime constructs, attaches, starts, and closes a child Session after controller unlock. It owns no lifecycle state.
- delegate_legacy_state.go scans existing lifecycle and watch envelopes read-only before either writable store is opened. It recognizes only the two forbidden legacy classes and does not translate them.
- ReserveCreate mints the stable public identity from identifier.MustNewDelegateID (through a deterministic test seam where needed), not jobstore.NewDelegateID. No stable delegate identity helper remains owned by jobstore after Task 12.
- Replace AdmitStartInput with BeginStartInput(lease delegateLease) (delegateInputClaim, error) and CompleteStartInput(claim delegateInputClaim, committed bool, failure delegateFinish) (delegateMutationPlans, error). The first method claims the exact non-launched binding; the transcript append/fsync occurs after unlock; the second revalidates the claim and either marks ready or atomically settles input_persist_failed. No transcript callback runs under the controller.
- Add FailCommittedStart(lease delegateLease, finish delegateFinish, closeReason string) (delegateMutationPlans, error) on the controller. For a permanently unrestorable post-commit construction/admission failure it appends terminal preparation, run finish, and resumability closure in one existing-event batch; it adds no event kind or failure record.

- [x] **Step 1: Prove fail-closed bootstrap and single authority**

Add these behavioral tests:

~~~go
func TestDelegateResourceBootstrap_RootOwnsOneController(t *testing.T)
func TestDelegateResourceBootstrap_ChildInheritsControllerAndStableOwnerID(t *testing.T)
func TestDelegateResourceBootstrap_LegacyDelegateStateFailsClosed(t *testing.T)
func TestDelegateResourceBootstrap_LegacyDelegateWatchStateFailsClosed(t *testing.T)
func TestDelegateResourceBootstrap_ShellOnlyAndStableWatchStateOpen(t *testing.T)
func TestDelegateResourceBootstrap_UnknownStoreVersionFailsClosed(t *testing.T)
func TestDelegateResourceBootstrap_RestartIsProviderFreeAndLazy(t *testing.T)
~~~

Immediately before bootstrap implementation, run and retain the behavioral RED:

~~~bash
go test ./agent -run '^TestDelegateResourceBootstrap_' -count=1
~~~

Expected RED: the real Session open path either has no root controller, accepts a forbidden legacy fixture, or constructs a child/provider during restart. A compile error, missing selector, or fixture-only parser assertion is invalid.

Implement root bootstrap in session_init.go/session_lifecycle.go and the narrow read-only guards in delegate_legacy_state.go. Open exactly one delegatestore Store, pass it to openDelegateTreeController, and install the pointer on the root before registered tools become available. A child receives that pointer and its immutable stable owning delegate ID through retained construction config. Each generation receives the fresh immutable lease returned by CommitStart only through its run context and generation-scoped closures. Restart performs ReadEvents/fold/reconcile evidence collection only; it starts no provider, timer, hook, nudge, salvage, worktree operation, or child Session. Ensure writable Open is not called until both legacy scans succeed.

Run GREEN:

~~~bash
go test ./agent -run '^TestDelegateResourceBootstrap_' -count=20
go test -race ./agent -run '^TestDelegateResourceBootstrap_' -count=20
~~~

- [x] **Step 2: Prove isolation-before-durable-publication and recoverable post-commit failure**

Add:

~~~go
func TestDelegateResourceCreate_IsolationFailurePublishesNothing(t *testing.T)
func TestDelegateResourceCreate_StableIdentityCommitsBeforeRuntimeLaunch(t *testing.T)
func TestDelegateResourceCreate_PostCommitConstructionFailureClosesResumability(t *testing.T)
func TestDelegateResourceCreate_PostCommitFailureRemainsInspectableAfterRestart(t *testing.T)
func TestDelegateResourceCreate_MissingRestoreInputsCloseResumabilityBeforeCleanup(t *testing.T)
func TestDelegateResourceCreate_ResumabilityAppendFailureDestroysNothing(t *testing.T)
func TestDelegateResourceCreate_DescendantEventCallbackSurvivesSpawnConfig(t *testing.T)
func TestDelegateResourceCreate_ChildTranscriptIsPreseededBeforeRun(t *testing.T)
func TestDelegateResourceCreate_InputTranscriptAppendRunsAfterControllerUnlock(t *testing.T)
~~~

Use fake worktree/sandbox constructors, a scripted child Session constructor, a provider panic sentinel, an append fault seam, and a captured root event callback. Immediately before changing create/restore, run:

~~~bash
go test ./agent -run '^TestDelegateResourceCreate_' -count=1
~~~

Expected RED: at least the stable registered create route, post-commit recovery, and descendant callback cases fail against the current route.

Implement the order:

1. validate descriptor and resolve model/reasoning/sandbox inputs;
2. call ReserveCreate to hold capacity and obtain one process-only reservation containing the uncommitted stable ID and exact isolation paths;
3. complete deterministic worktree/sandbox isolation outside the controller lock, calling AbortStart and cleaning only the reservation's artifacts on failure;
4. call CommitStart on that exact reservation;
5. publish the stable update;
6. construct and attach the child outside the lock;
7. call BeginStartInput, release the controller, durably preseed its transcript, and call CompleteStartInput on the exact claim;
8. start provider execution.

Before step 4 failure publishes no ID and writes no delegate aggregate. After step 4 a permanently unrestorable failure calls FailCommittedStart so the failed canonical packet and resumability closure commit atomically; it never reports a resumable but unrestorable idle delegate. A transient failure may remain resumable only when the durable descriptor is sufficient for later restore. If the atomic append fails, keep the generation fenced for reconcile, retain its artifacts, and return the stable ID plus durable error; destroy nothing. Copy the root-installed ordinary event callback, transcript subscription, owner root ID, controller pointer, and stable owning delegate ID into child config without copying any legacy JobRecord handle or generation lease. Carry the fresh lease only in the active generation's run context and closures.

Run GREEN:

~~~bash
go test ./agent -run '^TestDelegateResourceCreate_' -count=20
go test -race ./agent -run '^TestDelegateResourceCreate_' -count=20
~~~

- [x] **Step 3: Register stable creation without activation aliases**

Update the registered delegate tool schema and implementation so creation returns delegate_id, child_session_id, transcript_ref, and durable metadata from the stable descriptor. Do not return an activation job ID. Keep shell job registration unchanged. Add:

~~~go
func TestDelegateResourceCreate_RegisteredToolReturnsOnlyStableDelegateIdentity(t *testing.T)
func TestDelegateResourceCreate_RegisteredToolUsesRootController(t *testing.T)
func TestDelegateResourceCreate_RegisteredNestedCreateUsesCurrentLease(t *testing.T)
~~~

Immediately before registration, run:

~~~bash
go test ./agent -run '^TestDelegateResourceCreate_Registered' -count=1
~~~

Expected RED: the registered tool still constructs or returns a delegate JobRecord.

Run the task GREEN and broader gates:

~~~bash
gofmt -w agent/delegate_runtime.go agent/delegate_legacy_state.go agent/delegate_resource_bootstrap_test.go agent/delegate_resource_create_test.go agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/session_lifecycle_test.go agent/delegate_tree_controller.go agent/delegate_tree_controller_test.go agent/delegate_tree_start.go agent/delegate_tree_start_test.go agent/delegate_tree_restore.go agent/delegate_tree_restore_test.go agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/sandbox_delegate.go agent/session_tools.go agent/session_tool_registry.go agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/session_tools_worktree.go agent/session_worktree_relock.go agent/session_worktree_resume.go agent/session_worktree_sweep.go agent/events/events.go agent/events/payloads.go agent/events/eventdata.go
go test ./agent -run '^(TestDelegateResourceBootstrap_|TestDelegateResourceCreate_)' -count=20
go test -race ./agent -run '^(TestDelegateResourceBootstrap_|TestDelegateResourceCreate_)' -count=20
go test ./agent/internal/delegatestore -count=1
go test ./agent/internal/tool -count=1
make fuzz-registry-check
git diff --check
git status --short
~~~

Review the diff before staging. Stage only:

~~~bash
git add -- agent/delegate_runtime.go agent/delegate_legacy_state.go agent/delegate_resource_bootstrap_test.go agent/delegate_resource_create_test.go
git add -- agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/session_lifecycle_test.go
git add -- agent/delegate_tree_controller.go agent/delegate_tree_controller_test.go agent/delegate_tree_start.go agent/delegate_tree_start_test.go agent/delegate_tree_restore.go agent/delegate_tree_restore_test.go agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/sandbox_delegate.go
git add -- agent/session_tools.go agent/session_tool_registry.go agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git add -- agent/session_tools_worktree.go agent/session_worktree_relock.go agent/session_worktree_resume.go agent/session_worktree_sweep.go
git add -- agent/events/events.go agent/events/payloads.go agent/events/eventdata.go
git commit -m "feat: bootstrap stable delegate authority" -m "Install one root-owned delegate controller, reject legacy delegate and delegate-job watch state before writable open, preserve descendant event wiring, and route registered creation through deterministic isolation and a durable stable identity. Post-commit construction failures now close resumability instead of publishing an unrestorable delegate."
~~~

Do not deploy this intermediate commit; the branch is atomic only after Task 14.

---

### Task 7: Registered runtime lifecycle

**Status:** Complete at immutable commit `521a4892d977927154f34636343d84e8dda15508`. Final Task 7 review and verification closure are recorded in `.superpowers/sdd/2026-08-12-delegate-resource-recovery/task-7-report.md`.

**Files:**

- Create: agent/delegate_resource_runtime_test.go, agent/delegate_resource_supervision_test.go.
- Modify: agent/delegate_runtime.go, agent/delegate_delivery.go.
- Modify: agent/delegate_tree_controller.go, agent/delegate_tree_controller_test.go, agent/delegate_tree_start.go, agent/delegate_tree_start_test.go, agent/delegate_tree_steer.go, agent/delegate_tree_steer_test.go, agent/delegate_tree_finish.go, agent/delegate_tree_finish_test.go, agent/delegate_tree_stop.go, agent/delegate_tree_stop_test.go, agent/delegate_tree_restore.go, agent/delegate_tree_restore_test.go.
- Modify: agent/session_attention.go and agent/schema/turn.go for private attention content and delivery-commit persistence.
- Modify: agent/session.go, agent/session_config.go, agent/session_model_call.go, agent/session_tools.go, agent/session_tool_round.go, agent/session_tool_round_test.go, agent/session_assistant_persistence_test.go, agent/session_tools_communicate.go, agent/session_queue.go, agent/session_queue_persist.go, agent/session_lifecycle.go.
- Modify: agent/job_delegate.go, agent/subagents.go, agent/job_notify.go, agent/job_watch.go.
- Modify: agent/salvage.go, agent/salvage_test.go, agent/settlement_salvage_test.go, agent/job_supervision_test.go.
- Modify: agent/subagents_test.go, agent/cov_w3init_subagents_test.go, agent/internal/hooks/hooks.go, agent/internal/hooks/hooks_test.go.
- Modify: agent/internal/delegatestore/record.go and agent/internal/delegatestore/fold.go for canonical packet/exhaustion/usage/worktree fidelity; modify agent/internal/delegatestore/fold_test.go, agent/internal/delegatestore/store_test.go, and agent/internal/delegatestore/fuzz_test.go to prove validation, clone, append/reopen, and replay. Add no event kind or second record.

**Interfaces:**

- Consume committed starts, exact leases, BeginModelRequest, BeginTool, BeginSettlement, FinishGeneration, StopSubtree, immutable post-unlock plans, and receiver delivery receipts.
- Produce registered running/idle send, safe to=caller persistence, the minimal idempotent receiver-attention append and inline delivery-commit bridge, canonical packet construction, typed exhaustion, generic Stop/SubagentStop integration, one auto-nudge, quiet-watchdog attention admission, final-round salvage guidance, and a self-driving positive-stop reconcile loop.
- Add one process-only delegateSteeringClaim with BeginSteerPersistence, CompleteSteerPersistence, and AbortSteerPersistence operations. The claim orders the exact accepted send while the transcript append runs after unlock; it is neither persisted nor publicly addressable.
- Change BeginModelRequest to return a delegateModelRequestClaim containing the exact lease/runtime and claimed pending-steer entry IDs. Snapshot child history after unlock. CompleteModelRequest(claim, history) revalidates the claim and returns expanded immutable provider history while consuming only claimed IDs found in that snapshot. AbortModelRequest releases a failed snapshot claim. Stop drains these claims; no provider starts before completion.
- Complete the live receiver persistence foundation in this task. `DelegateDeliveryCommit` is private metadata on the same `TurnToolResults` turn as the caller's aggregated tool results and is excluded from provider/public projections. The Session append helper is idempotent by attention identity plus content. A round carrying a delivery commit must use the durable append path even when it has no terminal shell result; call `CompleteDelivery(true)` only after that fsync succeeds, and call `CompleteDelivery(false)` on append/fsync failure. The same helper serves quiet-watchdog attention. The controller continues to fence delivery N+1 until N's durable completion.

- [x] **Step 1: Prove running, idle, caller, and settlement behavior**

Add:

~~~go
func TestDelegateResourceRuntime_RunningSendPersistsBeforeAck(t *testing.T)
func TestDelegateResourceRuntime_RunningSendDoesNotStartSuccessor(t *testing.T)
func TestDelegateResourceRuntime_IdleSendReservesOneSuccessor(t *testing.T)
func TestDelegateResourceRuntime_ConcurrentIdleSendsStartOneGeneration(t *testing.T)
func TestDelegateResourceRuntime_CallerNestedPersistsAtNextModelBoundary(t *testing.T)
func TestDelegateResourceRuntime_CallerRootWaitsForToolRoundPersistence(t *testing.T)
func TestDelegateResourceRuntime_CallerCannotWriteIntoUnfinishedRootToolRound(t *testing.T)
func TestDelegateResourceRuntime_ModelHistorySnapshotRunsAfterControllerUnlock(t *testing.T)
func TestDelegateResourceRuntime_PendingSteerWinsAtTerminalBoundary(t *testing.T)
func TestDelegateResourceRuntime_CommunicateSettlesExactlyOnce(t *testing.T)
func TestDelegateAttention_AppendIsIdempotentByIdentityAndContent(t *testing.T)
func TestDelegateAttention_ConflictingIdentityIsCorruption(t *testing.T)
func TestDelegateAttention_DeliveryCommitUsesCallerToolResultFsync(t *testing.T)
func TestDelegateAttention_DeliveryCommitAppendFailureLeavesNAndNPlusOnePending(t *testing.T)
func TestDelegateAttention_DeliveryCommitReleasesNPlusOneOnlyAfterNFsync(t *testing.T)
~~~

Put the attention append cases in `agent/session_assistant_persistence_test.go` and the caller-round delivery-commit cases in `agent/session_tool_round_test.go`. Use scripted providers, durable transcript readback, append-fault seams, and channel barriers that hold the caller's aggregated tool-result fsync. Prove the blocked fsync cannot acknowledge N, append failure leaves both N and N+1 pending, and only N's successful fsync/ack releases N+1 in order. Immediately before the send/caller implementation slice, run:

~~~bash
go test ./agent -run '^TestDelegateResourceRuntime_(Running|Idle|Concurrent|Caller|ModelHistory|PendingSteer|Communicate)' -count=1
go test ./agent -run '^TestDelegateAttention_(Append|Conflicting|DeliveryCommit)' -count=1
~~~

Expected RED: the legacy activation route or direct to=caller insertion violates at least one runtime contract, and the caller round has no durable attention/delivery-commit bridge.

Move running steering transcript I/O after controller unlock behind the exact claim. Split model-boundary validation, history snapshot, and claim completion so no child history lock is acquired under the controller; bind accepted steering once at CompleteModelRequest. For to=caller, hand the payload and private delivery commit to the caller Session's existing durable tool-result boundary; never append into an unfinished root tool round. Add the idempotent durable attention helper and make the inline bridge complete delivery only after that boundary returns from fsync. Idle send uses ReserveStart/CommitStart and the runtime adapter. Run both selectors GREEN before continuing:

~~~bash
go test ./agent -run '^(TestDelegateResourceRuntime_(Running|Idle|Concurrent|Caller|ModelHistory|PendingSteer|Communicate)|TestDelegateAttention_(Append|Conflicting|DeliveryCommit))' -count=20
go test -race ./agent -run '^(TestDelegateResourceRuntime_(Running|Idle|Concurrent|Caller|ModelHistory|PendingSteer|Communicate)|TestDelegateAttention_(Append|Conflicting|DeliveryCommit))' -count=20
~~~

- [x] **Step 2: Prove one canonical packet and lossless exhaustion**

Add:

~~~go
func TestDelegateResourceRuntime_CanonicalPacketReusedAcrossFinishReplayAndDelivery(t *testing.T)
func TestDelegateResourceRuntime_StructuredResultExplicitNullIsPresent(t *testing.T)
func TestDelegateResourceRuntime_InvalidStructuredResultIsBoundedAndExplained(t *testing.T)
func TestDelegateResourceRuntime_ToolRoundExhaustionIsTypedAndResumable(t *testing.T)
func TestDelegateResourceRuntime_TurnExhaustionClosesResumabilityAtomically(t *testing.T)
func TestDelegateResourceRuntime_TerminalPacketPreservesTaskModelEffortTimingUsageAndWorktree(t *testing.T)
func TestDelegateResourceRuntime_StaleGenerationCannotPublishPacket(t *testing.T)
~~~

Immediately before packet/exhaustion implementation, run:

~~~bash
go test ./agent -run '^TestDelegateResourceRuntime_(Canonical|Structured|Invalid|ToolRound|TurnExhaustion|TerminalPacket|Stale)' -count=1
~~~

Build the canonical TerminalPacket once from durable run inputs. Track structured-result presence separately from decoding so raw JSON null is present. Bound invalid bytes and carry structured_result_valid plus structured_result_reason. Extend the existing Outcome in record.go/fold.go with typed exhaustion budget, limit, and explicit resumability so metadata-only status never reads packet contents. Encode tool_round_budget_exhausted in both packet metadata and latest outcome with budget/limit and resumable=true. Encode turn_budget_exhausted the same way with resumable=false, and append finish plus resumability closure in one batch. Preserve descriptor, resolved model, effort, start/end/activity timestamps, cumulative self-only usage, worktree evidence, validation, and warnings. Run the selector GREEN count 20 and race.

- [x] **Step 3: Prove hooks, nudge, salvage, and quiet supervision**

Add:

~~~go
func TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin(t *testing.T)
func TestDelegateResourceSupervision_AutoNudgeSuppressedBySteerCancellationAndExhaustion(t *testing.T)
func TestDelegateResourceSupervision_SubagentStopRunsAfterFinishAndBeforeContinuation(t *testing.T)
func TestDelegateResourceSupervision_SubagentStopBlockingStartsOneContinuation(t *testing.T)
func TestDelegateResourceSupervision_SubagentStopNonblockingStartsNoContinuation(t *testing.T)
func TestDelegateResourceSupervision_FinalRoundFailedSalvageAddsResumeHint(t *testing.T)
func TestDelegateResourceSupervision_SuccessExhaustionCancellationStopAndStaleSalvageAddNoHint(t *testing.T)
func TestDelegateResourceSupervision_QuietWatchdogUsesTenMinuteThresholdAndThirtySecondChecks(t *testing.T)
func TestDelegateResourceSupervision_QuietWatchdogFiresOncePerQuietStretch(t *testing.T)
func TestDelegateResourceSupervision_QuietAttentionAppendFailureRetriesSameIdentity(t *testing.T)
func TestDelegateResourceSupervision_RestartStartsNoWatchdogOrProvider(t *testing.T)
~~~

Use a fake clock and explicit timer channel, not sleeps. Immediately before implementation, run:

~~~bash
go test ./agent -run '^TestDelegateResourceSupervision_' -count=1
~~~

Route activity through the exact lease at retry, awaiting-model, streaming, and tool-running boundaries. The binding timer only requests controller admission of ordinary owner attention after unlock. Set the quiet latch only after receiver transcript fsync. Preserve exact hook ordering, one eligible auto-nudge, one blocking-hook continuation, pending-steer precedence, suppression rules, and final-round-only salvage wording. Restart replays none of them. Run GREEN count 20 and race.

- [x] **Step 4: Prove generic Stop and positive wait keep reconciliation alive**

Add:

~~~go
func TestDelegateResourceRuntime_GenericStopUsesCanonicalFinish(t *testing.T)
func TestDelegateResourceRuntime_PositiveStopWaitKeepsReconciliationDriverAlive(t *testing.T)
func TestDelegateResourceRuntime_PositiveStopWaitReturnsAfterDurableCompletion(t *testing.T)
func TestDelegateResourceRuntime_StopWaitTimeoutLeavesReconciliationRunning(t *testing.T)
func TestDelegateResourceRuntime_ZeroWaitReturnsAfterRequestFsync(t *testing.T)
~~~

Place barriers around cancellation, attention repair, and stop_completed append. Immediately before changing stop waiting, run:

~~~bash
go test ./agent -run '^TestDelegateResourceRuntime_(GenericStop|PositiveStop|StopWait|ZeroWait)' -count=1
~~~

The reconcile driver is controller/root owned and lives independently of the requesting tool context. max_wait_ms only bounds that caller's wait on StopSubtree.done. Timeout cannot cancel the driver or its claims. Generic Session Stop uses the same canonical finish and stop algebra as explicit job_stop.

Run task GREEN and broader gates:

~~~bash
gofmt -w agent/delegate_resource_runtime_test.go agent/delegate_resource_supervision_test.go agent/delegate_runtime.go agent/delegate_delivery.go agent/delegate_tree_controller.go agent/delegate_tree_controller_test.go agent/delegate_tree_start.go agent/delegate_tree_start_test.go agent/delegate_tree_steer.go agent/delegate_tree_steer_test.go agent/delegate_tree_finish.go agent/delegate_tree_finish_test.go agent/delegate_tree_stop.go agent/delegate_tree_stop_test.go agent/delegate_tree_restore.go agent/delegate_tree_restore_test.go agent/session_attention.go agent/schema/turn.go agent/session.go agent/session_config.go agent/session_model_call.go agent/session_tools.go agent/session_tool_round.go agent/session_tool_round_test.go agent/session_assistant_persistence_test.go agent/session_tools_communicate.go agent/session_queue.go agent/session_queue_persist.go agent/session_lifecycle.go agent/job_delegate.go agent/subagents.go agent/subagents_test.go agent/cov_w3init_subagents_test.go agent/job_notify.go agent/job_watch.go agent/salvage.go agent/salvage_test.go agent/settlement_salvage_test.go agent/job_supervision_test.go agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go agent/internal/delegatestore/record.go agent/internal/delegatestore/fold.go agent/internal/delegatestore/fold_test.go agent/internal/delegatestore/store_test.go agent/internal/delegatestore/fuzz_test.go
go test ./agent -run '^(TestDelegateResourceRuntime_|TestDelegateResourceSupervision_|TestDelegateAttention_)' -count=20
go test -race ./agent -run '^(TestDelegateResourceRuntime_|TestDelegateResourceSupervision_|TestDelegateAttention_)' -count=20
go test ./agent/internal/delegatestore -count=1
go test ./agent -run '^(TestJobDelegate|TestSubagent|TestSessionToolRound)' -count=1
git diff --check
git status --short
~~~

Stage only:

~~~bash
git add -- agent/delegate_resource_runtime_test.go agent/delegate_resource_supervision_test.go agent/delegate_runtime.go agent/delegate_delivery.go
git add -- agent/delegate_tree_controller.go agent/delegate_tree_controller_test.go agent/delegate_tree_start.go agent/delegate_tree_start_test.go agent/delegate_tree_steer.go agent/delegate_tree_steer_test.go agent/delegate_tree_finish.go agent/delegate_tree_finish_test.go agent/delegate_tree_stop.go agent/delegate_tree_stop_test.go agent/delegate_tree_restore.go agent/delegate_tree_restore_test.go
git add -- agent/session_attention.go agent/schema/turn.go agent/session.go agent/session_config.go agent/session_model_call.go agent/session_tools.go agent/session_tool_round.go agent/session_tool_round_test.go agent/session_assistant_persistence_test.go agent/session_tools_communicate.go agent/session_queue.go agent/session_queue_persist.go agent/session_lifecycle.go
git add -- agent/job_delegate.go agent/subagents.go agent/subagents_test.go agent/cov_w3init_subagents_test.go agent/job_notify.go agent/job_watch.go
git add -- agent/salvage.go agent/salvage_test.go agent/settlement_salvage_test.go agent/job_supervision_test.go agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git add -- agent/internal/delegatestore/record.go agent/internal/delegatestore/fold.go agent/internal/delegatestore/fold_test.go agent/internal/delegatestore/store_test.go agent/internal/delegatestore/fuzz_test.go
git commit -m "feat: run delegates through stable lifecycle" -m "Route registered send, caller steering, caller delivery commits, settlement, exhaustion, hooks, nudge, salvage, quiet supervision, generic stop, and positive stop waits through exact stable leases and canonical packets. Inline acknowledgement now follows the caller tool-result fsync through one idempotent attention append; external transcript, timer, hook, and provider work remains claimed under the controller and performed after unlock."
~~~

---

### Task 8: Attention, watch, observer, and shell delivery

**Files:**

- Modify: agent/session_attention.go. Create: agent/session_attention_test.go, agent/session_attention_fuzz_test.go for cold fold, caller delivery crash replay, resolution, compaction, and watch-delivery coverage; the Task 7 live inline delivery-commit tests remain in their caller-persistence owners.
- Create: agent/delegate_resource_watch_test.go, agent/delegate_resource_shell_test.go.
- Modify: agent/schema/turn.go, agent/transcript_read.go, agent/history_repair.go, agent/internal/contextmgr/context_manager.go, agent/internal/contextmgr/context_manager_test.go.
- Modify: agent/delegate_shell_repair.go, agent/delegate_shell_repair_test.go.
- Modify: agent/delegate_delivery.go, agent/delegate_tree_controller.go, agent/delegate_tree_finish.go, agent/delegate_tree_restore.go, agent/delegate_tree_stop.go, agent/delegate_tree_work.go.
- Modify: agent/internal/delegatestore/record.go, agent/internal/delegatestore/fold_test.go, agent/internal/delegatestore/store_test.go, agent/internal/delegatestore/fuzz_test.go to persist and round-trip ParentWatchGranted in the stable descriptor without adding an event kind.
- Modify: agent/job_watch.go, agent/job_notify.go, agent/job_shell.go, agent/jobs.go, agent/jobs_nested.go, agent/session_jobtree_drain.go.
- Modify: agent/internal/jobstore/watch.go, agent/internal/jobstore/event.go, agent/internal/jobstore/record.go, agent/internal/jobstore/fold.go.
- Modify: agent/session_tools_jobs.go, agent/session_tools_jobs_watch_test.go, agent/job_watch_parent_test.go, agent/job_watch_observer_test.go, agent/job_watch_restore_end_notice_test.go, agent/job_watch_restore_lost_notice_test.go, agent/job_watch_end_notice_test.go.
- Modify: agent/root_watch_tree_program_fuzz_test.go, agent/job_watch_delegate_fuzz_test.go, agent/watch_seqfuzz_test.go, agent/watch_observer_fuzz_test.go, agent/watch_pending_frame_program_fuzz_test.go, agent/watch_restore_clear_history_program_fuzz_test.go, agent/watch_attach_terminal_program_fuzz_test.go, agent/watch_config_validation_program_fuzz_test.go.
- Modify: scripts/run-fuzz.sh for FuzzDelegateAttentionFold and FuzzStableDelegateWatchDelivery.

**Interfaces:**

- Consume the Task 7 idempotent live attention append and inline caller delivery-commit bridge, controller terminal packets, stop membership, shell work receipts, the existing jobstore watch journal, and receiver Session transcripts.
- Produce cold attention and caller delivery-commit fold helpers plus durable resolution; typed session/shell/delegate watch endpoints; enqueue/delivery receipts; stable parent grants; exact observer folding; unreachable-owner escalation; and shell completion routing through ParentDelegateID.
- The watch journal remains authoritative. No watch state enters delegatestore and no arbitrary receiver field is added.

- [ ] **Step 1: Prove attention resolution durability and tool-round transparency**

Add:

~~~go
func TestDelegateAttention_ResolutionFsyncPrecedesSourceAck(t *testing.T)
func TestDelegateAttention_ResolutionMarkerDoesNotSplitToolCallAndResult(t *testing.T)
func TestDelegateAttention_HistoryRepairCannotCreateOrphanedToolResult(t *testing.T)
func TestDelegateAttention_RestartFoldIsProviderFreeAndReadOnly(t *testing.T)
func TestDelegateAttention_RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult(t *testing.T)
func FuzzDelegateAttentionFold(f *testing.F)
~~~

Put this target and the Step 2 watch-delivery target in session_attention_fuzz_test.go under the serffuzz build tag. Register `native:agent:.:FuzzDelegateAttentionFold::session_attention.go` in scripts/run-fuzz.sh in this task.

Immediately before adding the turn/helper implementation, run:

~~~bash
go test ./agent -run '^TestDelegateAttention_(ResolutionFsyncPrecedesSourceAck|ResolutionMarkerDoesNotSplitToolCallAndResult|HistoryRepairCannotCreateOrphanedToolResult|RestartFoldIsProviderFreeAndReadOnly|RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult)$' -count=1
~~~

Consume Task 7's `DelegateDeliveryCommit` and idempotent live append without redefining their caller-fsync boundary. Extend the existing AttentionID, provider-excluded AttentionResolution, and live append foundation with missing-file-tolerant readPendingAttention, durable resolution, and cold append-after-identity-validation helpers. The cold caller-delivery helper must recognize the exact `ToolCallID` plus `DeliveryID` pair on a durable `TurnToolResults`; neither field alone is sufficient.

Place the caller crash-replay test in `agent/session_attention_test.go`. Persist N's real caller tool-result turn with its `DelegateDeliveryCommit`, crash after transcript fsync but before controller acknowledgement, then reopen the transcript and delegate store through fresh process/controller state. Replay the durable head through the actual cold transcript fold and prove that it acknowledges N without appending a second tool-result or attention turn before releasing N+1 in order. Install panic sentinels for provider calls and Session construction so cold replay cannot pass through a live-runtime path or reuse a process-local fake map.

Move the generic attention fold/cold helpers from delegate_shell_repair.go into session_attention.go so shell repair keeps only shell-specific orchestration and the registered fuzz surface names its real owner. Ensure context compaction and history repair treat the marker as transparent. Run the focused selector GREEN count 20/race plus the registered deterministic fuzz replay.

- [ ] **Step 2: Prove typed watches and crash-safe delivery**

Add:

~~~go
func TestStableDelegateWatch_TypedSessionShellAndDelegateSources(t *testing.T)
func TestStableDelegateWatch_DelegateReceiverIsImplicit(t *testing.T)
func TestStableDelegateWatch_ParentRequiresLeaseEdgeAndPersistedGrant(t *testing.T)
func TestStableDelegateWatch_ParentGrantIsNonTransitive(t *testing.T)
func TestStableDelegateWatch_PreservesFiltersEveryCoalescingAndBudget(t *testing.T)
func TestStableDelegateWatch_PreservesListInspectClearAndObservedBy(t *testing.T)
func TestStableDelegateWatch_EnqueueFsyncPrecedesCursorAdvance(t *testing.T)
func TestStableDelegateWatch_ReceiverFsyncPrecedesDeliveredAck(t *testing.T)
func TestStableDelegateWatch_LaterCoalescedUpdateSurvivesEarlierAck(t *testing.T)
func TestStableDelegateWatch_RestartRepairsReceiverDurableSourceUnacked(t *testing.T)
func TestStableDelegateWatch_StopFencesAndDrainsBothReceiptClasses(t *testing.T)
func TestStableDelegateWatch_TerminalFramePrecedesEndNotice(t *testing.T)
func TestStableDelegateWatch_RestartCancellationEmitsEndNotice(t *testing.T)
func TestStableDelegateWatch_LegacyDelegateJobRowFailsClosed(t *testing.T)
func FuzzStableDelegateWatchDelivery(f *testing.F)
~~~

Register `native:agent:.:FuzzStableDelegateWatchDelivery::session_attention.go;delegate_delivery.go;job_watch.go` in scripts/run-fuzz.sh. Port FuzzRootWatchTreeProgram and the watch configuration/pending/observer programs to typed stable endpoints; remove only the obsolete WatchdelDelegateResume subtarget, not WatchdelWatchOps or the existing watch-journal fuzz surfaces.

Use append-fault seams and channel barriers at each fsync/receipt boundary. Immediately before typed endpoint and receipt implementation, run:

~~~bash
go test ./agent -run '^TestStableDelegateWatch_' -count=1
~~~

Bind a stable delegate source to stable ID plus current private generation. Keep the receiver implicit from the watcher Session. Acquire enqueue receipt, fsync frame/cursor, acquire delivery receipt and refold, fsync receiver attention keyed by delivery ID plus update sequence, fsync source acknowledgement, then release before provider work. Stop blocks new receipts, drains both sets, resolves source cursors, discards matching receiver attention, and repeats until the evidence version is stable. Run GREEN count 20/race and fuzz.

- [ ] **Step 3: Prove observer and unreachable-attention behavior**

Add:

~~~go
func TestStableDelegateObserver_EmitsExactlyOneControllerTerminalPacket(t *testing.T)
func TestStableDelegateObserver_NoOrdinaryDuplicateAfterCallback(t *testing.T)
func TestStableDelegateAttention_ReachableColdOwnerRetainsAttentionAfterRestoreFailure(t *testing.T)
func TestStableDelegateAttention_UnreachableOwnerTransfersToNearestReachableAncestor(t *testing.T)
func TestStableDelegateAttention_AncestorFsyncPrecedesChildDiscard(t *testing.T)
func TestStableDelegateAttention_ConsumedEntryIsNeverEscalated(t *testing.T)
func TestStableDelegateAttention_StartupRepairUsesNoProvider(t *testing.T)
~~~

Immediately before observer/escalation implementation, run:

~~~bash
go test ./agent -run '^TestStableDelegate(Observer|Attention)_' -count=1
~~~

Fold a parent observer callback into exactly one canonical controller terminal packet. For permanent owner loss, deterministically fsync ancestor attention before child discard. Do not transfer attention merely because a reachable cold delegate has a transient restore failure. Run GREEN count 20/race.

- [ ] **Step 4: Prove delegate-owned shell delivery and ancestor visibility**

Add:

~~~go
func TestStableDelegateShell_ParentDelegateIDReplacesSyntheticParentJob(t *testing.T)
func TestStableDelegateShell_CompletionAttentionReachesDirectOwner(t *testing.T)
func TestStableDelegateShell_AncestorCanSeeDescendantShell(t *testing.T)
func TestStableDelegateShell_AncestorCannotControlWithoutDirectDelegateHandle(t *testing.T)
func TestStableDelegateShell_OutputStatusWatchAndStopRemainJobAddressed(t *testing.T)
func TestStableDelegateShell_RestartRepairsCompletionAttentionOnce(t *testing.T)
~~~

Immediately before shell routing implementation, run:

~~~bash
go test ./agent -run '^TestStableDelegateShell_' -count=1
~~~

Keep shell jobs as job_ resources. Persist ParentDelegateID, preserve shell terminal generation and direct-owner attention, and compute ancestor visibility from stable delegate edges. Remove only the synthetic delegate ParentJobID dependency.

Run task GREEN and broader gates:

~~~bash
gofmt -w agent/session_attention.go agent/session_attention_test.go agent/session_attention_fuzz_test.go agent/delegate_resource_watch_test.go agent/delegate_resource_shell_test.go agent/schema/turn.go agent/transcript_read.go agent/history_repair.go agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/context_manager_test.go agent/delegate_shell_repair.go agent/delegate_shell_repair_test.go agent/delegate_delivery.go agent/delegate_tree_controller.go agent/delegate_tree_finish.go agent/delegate_tree_restore.go agent/delegate_tree_stop.go agent/delegate_tree_work.go agent/internal/delegatestore/record.go agent/internal/delegatestore/fold_test.go agent/internal/delegatestore/store_test.go agent/internal/delegatestore/fuzz_test.go agent/job_watch.go agent/job_notify.go agent/job_shell.go agent/jobs.go agent/jobs_nested.go agent/session_jobtree_drain.go agent/internal/jobstore/watch.go agent/internal/jobstore/event.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go agent/session_tools_jobs.go agent/session_tools_jobs_watch_test.go agent/job_watch_parent_test.go agent/job_watch_observer_test.go agent/job_watch_restore_end_notice_test.go agent/job_watch_restore_lost_notice_test.go agent/job_watch_end_notice_test.go agent/root_watch_tree_program_fuzz_test.go agent/job_watch_delegate_fuzz_test.go agent/watch_seqfuzz_test.go agent/watch_observer_fuzz_test.go agent/watch_pending_frame_program_fuzz_test.go agent/watch_restore_clear_history_program_fuzz_test.go agent/watch_attach_terminal_program_fuzz_test.go agent/watch_config_validation_program_fuzz_test.go
go test ./agent -run '^(TestDelegateAttention_|TestStableDelegateWatch_|TestStableDelegateObserver_|TestStableDelegateAttention_|TestStableDelegateShell_)' -count=20
go test -race ./agent -run '^(TestDelegateAttention_|TestStableDelegateWatch_|TestStableDelegateObserver_|TestStableDelegateAttention_|TestStableDelegateShell_)' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^(FuzzDelegateAttentionFold|FuzzStableDelegateWatchDelivery)$' -count=1
go test ./agent/internal/jobstore -run 'Watch' -count=1
make fuzz-registry-check
git diff --check
git status --short
~~~

Stage only the named files:

~~~bash
git add -- agent/session_attention.go agent/session_attention_test.go agent/session_attention_fuzz_test.go agent/delegate_resource_watch_test.go agent/delegate_resource_shell_test.go
git add -- agent/schema/turn.go agent/transcript_read.go agent/history_repair.go agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/context_manager_test.go
git add -- agent/delegate_shell_repair.go agent/delegate_shell_repair_test.go
git add -- agent/delegate_delivery.go agent/delegate_tree_controller.go agent/delegate_tree_finish.go agent/delegate_tree_restore.go agent/delegate_tree_stop.go agent/delegate_tree_work.go
git add -- agent/internal/delegatestore/record.go agent/internal/delegatestore/fold_test.go agent/internal/delegatestore/store_test.go agent/internal/delegatestore/fuzz_test.go
git add -- agent/job_watch.go agent/job_notify.go agent/job_shell.go agent/jobs.go agent/jobs_nested.go agent/session_jobtree_drain.go
git add -- agent/internal/jobstore/watch.go agent/internal/jobstore/event.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go
git add -- agent/session_tools_jobs.go agent/session_tools_jobs_watch_test.go agent/job_watch_parent_test.go agent/job_watch_observer_test.go agent/job_watch_restore_end_notice_test.go agent/job_watch_restore_lost_notice_test.go agent/job_watch_end_notice_test.go scripts/run-fuzz.sh
git add -- agent/root_watch_tree_program_fuzz_test.go agent/job_watch_delegate_fuzz_test.go agent/watch_seqfuzz_test.go agent/watch_observer_fuzz_test.go agent/watch_pending_frame_program_fuzz_test.go agent/watch_restore_clear_history_program_fuzz_test.go agent/watch_attach_terminal_program_fuzz_test.go agent/watch_config_validation_program_fuzz_test.go
git commit -m "feat: preserve stable delegate delivery" -m "Keep the existing watch journal and shipped observer behavior while replacing delegate-job indirection with typed stable endpoints. Extend the Task 7 attention foundation with cold fold and durable resolution, then add stop-fenced watch receipts, unreachable-owner escalation, and ParentDelegateID shell routing without a second lifecycle or watch authority."
~~~

---

### Task 9: Retention, stop, worktree, and close

**Files:**

- Create: agent/delegate_tree_reclaim.go, agent/delegate_tree_reclaim_test.go, agent/delegate_tree_reclaim_fuzz_test.go, agent/delegate_resource_retention_stop_test.go, agent/delegate_resource_worktree_test.go.
- Modify: agent/delegate_tree_controller.go, agent/delegate_tree_start.go, agent/delegate_tree_stop.go, agent/delegate_tree_restore.go, agent/delegate_tree_work.go.
- Modify: agent/delegate_runtime.go, agent/session.go, agent/session_config.go, agent/session_lifecycle.go, agent/tree_counter.go.
- Modify: agent/job_shell.go, agent/jobs.go, agent/session_jobtree_drain.go.
- Modify: agent/sandbox_delegate.go, agent/session_tools_worktree.go, agent/session_tools_worktree_dispose.go, agent/session_worktree_close.go, agent/session_worktree_relock.go, agent/session_worktree_resume.go, agent/session_worktree_sweep.go.
- Modify: agent/delegate_disposal_hint_test.go, agent/session_tools_worktree_livework_test.go, agent/session_tools_worktree_dispose_test.go, agent/session_worktree_close_test.go.
- Modify: scripts/run-fuzz.sh for FuzzDelegateReclaimStopRestart.

**Interfaces:**

- Consume exact controller runtime bindings, stable descriptors, stop request sequence, work and delivery receipts, immutable repair plans, and durable resumability closure.
- Produce max_retained_terminal admission reclamation, recursive stable stop, root close, foreground-shell receipt cleanup, stable worktree live/disposal guards, and restart equivalence.
- Add ClaimRuntimeReclamation(required int), CompleteRuntimeReclamation(claim, closed), and AbortRuntimeReclamation(claim) as process-only claim operations. The claim contains exact Session pointers and stable IDs; it has no durable event or public identity.

- [ ] **Step 1: Prove bounded admission-time reclamation**

Add:

~~~go
func TestDelegateRuntimeReclaim_UsesPublicMaxRetainedTerminalDefault2048(t *testing.T)
func TestDelegateRuntimeReclaim_ClaimsOnlyQuiescentTerminalSubtrees(t *testing.T)
func TestDelegateRuntimeReclaim_ClosesPostorderAfterUnlock(t *testing.T)
func TestDelegateRuntimeReclaim_ClearsOnlyExactResidentPointers(t *testing.T)
func TestDelegateRuntimeReclaim_PrefersClosedThenAcknowledgedThenOldestThenID(t *testing.T)
func TestDelegateRuntimeReclaim_InsufficientCapacityFailsBeforeIDMintOrConstruction(t *testing.T)
func TestDelegateRuntimeReclaim_CreateAndColdRestoreTriggerReclamation(t *testing.T)
func TestDelegateRuntimeReclaim_NoTimerUnloadEventOrStableDataDeletion(t *testing.T)
func FuzzDelegateReclaimStopRestart(f *testing.F)
~~~

Put the target in agent/delegate_tree_reclaim_fuzz_test.go under the serffuzz build tag. Register `native:agent:.:FuzzDelegateReclaimStopRestart::delegate_tree_reclaim.go;delegate_tree_stop.go;delegate_tree_restore.go` in scripts/run-fuzz.sh in this task.

Immediately before reclamation implementation, run:

~~~bash
go test ./agent -run '^TestDelegateRuntimeReclaim_' -count=1
~~~

At create entry, satisfy the resident bound through the reclamation claim before ReserveCreate can mint its process-only ID. At cold-restore entry, do the same before runtime construction. Claim exact quiescent runtime subtrees under the mutex, close postorder after unlock, then clear only exact pointers still matching the claim. Never delete aggregate, descriptor, transcript, outcome, lineage, delivery, or resumability. No background timer invokes reclamation. Run GREEN count 20/race.

- [ ] **Step 2: Prove recursive stop and self-driving drain**

Add:

~~~go
func TestDelegateResourceStop_StableStopIsAlwaysRecursive(t *testing.T)
func TestDelegateResourceStop_IncludeChildrenFalseIsIgnoredForDelegate(t *testing.T)
func TestDelegateResourceStop_RequestFsyncPrecedesExternalCancellation(t *testing.T)
func TestDelegateResourceStop_DrainsRuntimeShellWatchAttentionAndDeliveryReceipts(t *testing.T)
func TestDelegateResourceStop_SameTargetRetryJoinsDifferentTargetIsBusy(t *testing.T)
func TestDelegateResourceStop_PositiveWaitCannotOwnOrCancelDriver(t *testing.T)
func TestDelegateResourceStop_RestartCompletesPendingStopProviderFree(t *testing.T)
func TestDelegateResourceStop_CompletionAppendFailureKeepsAdmissionClosed(t *testing.T)
func TestDelegateResourceStop_RootCloseJoinsStopAndTeardownPostorder(t *testing.T)
~~~

Immediately before registered stop/root-close implementation, run:

~~~bash
go test ./agent -run '^TestDelegateResourceStop_' -count=1
~~~

Stable job_stop(dlg_...) always uses StopSubtree recursively and ignores include_children. The controller/root-owned reconcile driver repeatedly collects read-only external evidence, executes exact repairs after unlock, and attempts stop completion. Root close closes admission, joins any pending stop, performs whole-tree stop and postorder child teardown, then closes stores. Run GREEN count 20/race.

- [ ] **Step 3: Prove shell timeout receipt correctness**

Add:

~~~go
func TestDelegateResourceStop_ForegroundShellTimeoutAbortsUncommittedReceipt(t *testing.T)
func TestDelegateResourceStop_ForegroundShellTimeoutReportsCommittedShellOnce(t *testing.T)
func TestDelegateResourceStop_TimeoutRaceCannotLeakStopMembership(t *testing.T)
~~~

Use executor and timeout channels, not time.Sleep. Immediately before touching the timeout branch, run:

~~~bash
go test ./agent -run '^TestDelegateResourceStop_ForegroundShellTimeout' -count=1
go test ./agent -run '^TestDelegateResourceStop_TimeoutRace' -count=1
~~~

Every BeginShellWork has exactly one CommitShellWork or AbortShellWork outcome, including the foreground timeout path. A committed shell reports finish by exact token and job ID. Run GREEN count 20/race.

- [ ] **Step 4: Prove stable worktree and sandbox lifecycle**

Add:

~~~go
func TestStableDelegateWorktree_LiveGuardUsesStableDelegateState(t *testing.T)
func TestStableDelegateWorktree_RootCloseCleansEligibleScratch(t *testing.T)
func TestStableDelegateWorktree_ExplicitDisposalPreservesDirtyAndD0Checks(t *testing.T)
func TestStableDelegateWorktree_ForcePreservesLockProvenanceAndEvidence(t *testing.T)
func TestStableDelegateWorktree_ResumabilityClosureFsyncPrecedesDestruction(t *testing.T)
func TestStableDelegateWorktree_ClosureAppendFailureDestroysNothing(t *testing.T)
func TestStableDelegateWorktree_CleanupFailureReportsRetainedResidueWithoutReopen(t *testing.T)
func TestStableDelegateWorktree_DisposalAndRestartAreIdempotent(t *testing.T)
func TestStableDelegateWorktree_SandboxRestoreUsesDescriptorNotLegacyJob(t *testing.T)
~~~

Immediately before worktree/sandbox rewiring, run:

~~~bash
go test ./agent -run '^TestStableDelegateWorktree_' -count=1
~~~

Replace all live-work, owner, isolation, and cleanup lookups that still depend on a delegate JobRecord with stable descriptor/snapshot lookups. Keep scratch retention, lock provenance, dirty/D0 checks, force semantics, and cleanup evidence. Append resumability closure before destructive teardown; later physical failure records residue and cannot reopen.

Run task GREEN and broader gates:

~~~bash
gofmt -w agent/delegate_tree_reclaim.go agent/delegate_tree_reclaim_test.go agent/delegate_tree_reclaim_fuzz_test.go agent/delegate_resource_retention_stop_test.go agent/delegate_resource_worktree_test.go agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_tree_work.go agent/delegate_runtime.go agent/session.go agent/session_config.go agent/session_lifecycle.go agent/tree_counter.go agent/job_shell.go agent/jobs.go agent/session_jobtree_drain.go agent/sandbox_delegate.go agent/session_tools_worktree.go agent/session_tools_worktree_dispose.go agent/session_worktree_close.go agent/session_worktree_relock.go agent/session_worktree_resume.go agent/session_worktree_sweep.go agent/delegate_disposal_hint_test.go agent/session_tools_worktree_livework_test.go agent/session_tools_worktree_dispose_test.go agent/session_worktree_close_test.go
go test ./agent -run '^(TestDelegateRuntimeReclaim_|TestDelegateResourceStop_|TestStableDelegateWorktree_)' -count=20
go test -race ./agent -run '^(TestDelegateRuntimeReclaim_|TestDelegateResourceStop_|TestStableDelegateWorktree_)' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateReclaimStopRestart$' -count=1
go test ./agent/internal/worktree -count=1
make fuzz-registry-check
git diff --check
git status --short
~~~

Stage only:

~~~bash
git add -- agent/delegate_tree_reclaim.go agent/delegate_tree_reclaim_test.go agent/delegate_tree_reclaim_fuzz_test.go agent/delegate_resource_retention_stop_test.go agent/delegate_resource_worktree_test.go
git add -- agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_tree_work.go agent/delegate_runtime.go
git add -- agent/session.go agent/session_config.go agent/session_lifecycle.go agent/tree_counter.go agent/job_shell.go agent/jobs.go agent/session_jobtree_drain.go
git add -- agent/sandbox_delegate.go agent/session_tools_worktree.go agent/session_tools_worktree_dispose.go agent/session_worktree_close.go agent/session_worktree_relock.go agent/session_worktree_resume.go agent/session_worktree_sweep.go
git add -- agent/delegate_disposal_hint_test.go agent/session_tools_worktree_livework_test.go agent/session_tools_worktree_dispose_test.go agent/session_worktree_close_test.go scripts/run-fuzz.sh
git commit -m "feat: preserve delegate retention and cleanup" -m "Rehome max_retained_terminal as admission-triggered runtime reclamation, make stable stop recursively self-driving, close exact shell receipts on timeout, and bind worktree/sandbox guards and disposal to stable delegate descriptors without adding unload lifecycle state."
~~~

---

### Task 10: Stable tools and read-only projections

**Files:**

- Create: agent/delegate_resource_tools_test.go, agent/delegate_resource_readonly_test.go.
- Modify: agent/internal/tool/definitions.go, agent/internal/tool/definitions_test.go, agent/internal/tool/definitions_program_fuzz_test.go.
- Modify: agent/session_tools_jobs.go, agent/session_tools_jobs_test.go, agent/session_tools_jobs_list_test.go, agent/session_tools_jobs_stop_delegate_test.go, agent/session_tools_jobs_watch_test.go, agent/delegate_schema_test.go.
- Modify: agent/fuzz_ar_delegate_test.go, agent/registry_schemafuzz_test.go, agent/session_tools_jobs_contract_program_fuzz_test.go, agent/session_tools_jobs_lifecycle_fuzz_test.go.
- Modify: agent/session_outline.go, agent/job_transcript_read.go, agent/session_tools_transcript.go, agent/transcript_render.go, agent/transcript_render_test.go, agent/transcript_render_job_test.go.
- Modify: agent/jobs_activity.go, agent/jobs_activity_past.go, agent/historical_jobs.go, agent/status.go.
- Modify: agent/events/events.go, agent/events/payloads.go, agent/events/eventdata.go.
- Modify: agent/internal/delegatestore/read_events.go, agent/internal/delegatestore/read_events_test.go.
- Modify: agent/doctor/doctor.go, agent/doctor/audit.go, agent/doctor/jobs.go, agent/doctor/tree.go, agent/doctor/sessions.go, agent/doctor/watches.go, agent/doctor/audit_test.go, agent/doctor/jobs_test.go, agent/doctor/tree_test.go, agent/doctor/sessions_test.go, agent/doctor/watches_test.go.
- Modify: cmd/evener-doctor/main.go, cmd/evener-doctor/main_test.go, cmd/evener-doctor/README.md.
- Modify: cmd/evener-hub/internal/hubcore/prober.go, cmd/evener-hub/internal/hubcore/prober_test.go, cmd/evener-hub/internal/hubcore/prober_wire_test.go, cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go.
- Modify: cmd/evener-hub/app_threadread.go, cmd/evener-hub/app_threadread_test.go.

**Interfaces:**

- Consume stable controller snapshots, delegatestore.ReadEvents/Fold, existing jobstore.ReadEvents/Fold, typed watch diagnostics, canonical packets, descriptors, shell ParentDelegateID, and one sampled clock.
- Produce stable registered tool schemas/results; unified job_list; metadata-only non-acknowledging delegate status; historical rendering; live/cold activity; doctor/prober/thread-read pure projections.
- Read paths may reject legacy aliases but never call append-capable Open, repair a tail, create a file, construct a Session/provider, acknowledge delivery, or mutate metadata.

- [ ] **Step 1: Prove stable tool schemas and exact list/status semantics**

Add:

~~~go
func TestStableDelegateTools_CreateSendStopStatusUseDelegateID(t *testing.T)
func TestStableDelegateTools_StatusReadsMetadataWithoutPacketOrAck(t *testing.T)
func TestStableDelegateTools_StatusRejectsActivationAlias(t *testing.T)
func TestStableDelegateTools_StopIgnoresIncludeChildrenAndRemainsRecursive(t *testing.T)
func TestStableDelegateTools_WaitIgnoredReasonIsOwnField(t *testing.T)
func TestStableDelegateTools_ListUnifiesShellAndDelegateCandidates(t *testing.T)
func TestStableDelegateTools_ListPreservesTypeStatusAndVisibilityFilters(t *testing.T)
func TestStableDelegateTools_ListOwnerWinsDedupeAndSortsBeforePaging(t *testing.T)
func TestStableDelegateTools_ListPreservesOffsetLimitCountTotal(t *testing.T)
func TestStableDelegateTools_ListPreservesTurnSlotsAllowanceAndWatchDiagnostics(t *testing.T)
~~~

Immediately before schema/tool implementation, run:

~~~bash
go test ./agent -run '^TestStableDelegateTools_' -count=1
~~~

Keep established numeric limits and error codes. job_status(dlg_...) reads aggregate metadata only and does not read terminal packet contents or acknowledge delivery. Positive live steering may return wait_ignored_reason; it is not a warning. Build one candidate set, dedupe owner-first, globally sort, then page. Run GREEN count 20/race.

- [ ] **Step 2: Prove historical fidelity and nonmutation**

Add:

~~~go
func TestStableDelegateReadOnly_HistoricalSendRendersWithoutLiveAlias(t *testing.T)
func TestStableDelegateReadOnly_ActivityPreservesTimingUsageQuietWorktreeAndDiagnostics(t *testing.T)
func TestStableDelegateReadOnly_OneSampledClockDrivesQuietRunningAndDuration(t *testing.T)
func TestStableDelegateReadOnly_ColdAndLiveProjectionMatch(t *testing.T)
func TestStableDelegateReadOnly_MissingFilesRemainMissing(t *testing.T)
func TestStableDelegateReadOnly_TornTailIsReportedButNotRepaired(t *testing.T)
func TestStableDelegateReadOnly_FileBytesAndMetadataRemainUnchanged(t *testing.T)
func TestStableDelegateReadOnly_NoSessionProviderOrWritableOpen(t *testing.T)
~~~

Record inode/size/mtime/hash before and after reads and install constructor/open panic sentinels. Immediately before cold/history implementation, run:

~~~bash
go test ./agent -run '^TestStableDelegateReadOnly_' -count=1
~~~

Historical job_send_message rendering is presentational only and cannot resolve a live activation alias. Use ReadEvents plus Fold for cold activity and history. Sample time once per projection. Preserve explicit null/validation/exhaustion, task/description/type/model/effort, timing, cumulative self usage, worktree evidence, warnings, turn slots, allowance, and active/recent watch diagnostics. Run GREEN count 20/race.

- [ ] **Step 3: Prove doctor, prober, and Hub cold reads use pure APIs**

Add or update:

~~~go
func TestDoctorStableDelegateReadOnlyDoesNotMutateState(t *testing.T)
func TestDoctorStableDelegateReportsLegacyStateAndWatchFailures(t *testing.T)
func TestDoctorStableDelegatePreservesShellAndWatchDiagnostics(t *testing.T)
func TestHubProberStableDelegateUsesDescendantSessionsNotDetailedJobs(t *testing.T)
func TestHubThreadReadStableDelegateDoesNotExtractActivationID(t *testing.T)
func TestHubThreadReadStableDelegateIsReadOnly(t *testing.T)
~~~

Immediately before operational reader changes, run:

~~~bash
go test ./agent/doctor -run '^TestDoctorStableDelegate' -count=1
go test ./cmd/evener-hub -run '^TestHubThreadReadStableDelegate' -count=1
go test ./cmd/evener-hub/internal/hubcore -run '^TestHubProberStableDelegate' -count=1
~~~

Doctor reports both fail-closed legacy classes without attempting migration. Hub probing uses descendant_session_ids/descendant_states only. Thread read and prober never call writable Open.

Run task GREEN and broader gates:

~~~bash
gofmt -w agent/delegate_resource_tools_test.go agent/delegate_resource_readonly_test.go agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go agent/session_tools_jobs.go agent/session_tools_jobs_test.go agent/session_tools_jobs_list_test.go agent/session_tools_jobs_stop_delegate_test.go agent/session_tools_jobs_watch_test.go agent/delegate_schema_test.go agent/fuzz_ar_delegate_test.go agent/registry_schemafuzz_test.go agent/session_tools_jobs_contract_program_fuzz_test.go agent/session_tools_jobs_lifecycle_fuzz_test.go agent/session_outline.go agent/job_transcript_read.go agent/session_tools_transcript.go agent/transcript_render.go agent/transcript_render_test.go agent/transcript_render_job_test.go agent/jobs_activity.go agent/jobs_activity_past.go agent/historical_jobs.go agent/status.go agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/internal/delegatestore/read_events.go agent/internal/delegatestore/read_events_test.go agent/doctor/doctor.go agent/doctor/audit.go agent/doctor/jobs.go agent/doctor/tree.go agent/doctor/sessions.go agent/doctor/watches.go agent/doctor/audit_test.go agent/doctor/jobs_test.go agent/doctor/tree_test.go agent/doctor/sessions_test.go agent/doctor/watches_test.go cmd/evener-doctor/main.go cmd/evener-doctor/main_test.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/prober_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_test.go
go test ./agent -run '^(TestStableDelegateTools_|TestStableDelegateReadOnly_)' -count=20
go test -race ./agent -run '^(TestStableDelegateTools_|TestStableDelegateReadOnly_)' -count=20
go test ./agent/doctor -run '^TestDoctorStableDelegate' -count=20
go test -race ./agent/doctor -run '^TestDoctorStableDelegate' -count=20
go test ./cmd/evener-hub -run '^TestHubThreadReadStableDelegate' -count=20
go test -race ./cmd/evener-hub -run '^TestHubThreadReadStableDelegate' -count=20
go test ./cmd/evener-hub/internal/hubcore -run '^TestHubProberStableDelegate' -count=20
go test -race ./cmd/evener-hub/internal/hubcore -run '^TestHubProberStableDelegate' -count=20
go test ./cmd/evener-doctor -count=1
make fuzz
make fuzz-registry-check
git diff --check
git status --short
~~~

Stage only:

~~~bash
git add -- agent/delegate_resource_tools_test.go agent/delegate_resource_readonly_test.go
git add -- agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go
git add -- agent/session_tools_jobs.go agent/session_tools_jobs_test.go agent/session_tools_jobs_list_test.go agent/session_tools_jobs_stop_delegate_test.go agent/session_tools_jobs_watch_test.go agent/delegate_schema_test.go
git add -- agent/fuzz_ar_delegate_test.go agent/registry_schemafuzz_test.go agent/session_tools_jobs_contract_program_fuzz_test.go agent/session_tools_jobs_lifecycle_fuzz_test.go
git add -- agent/session_outline.go agent/job_transcript_read.go agent/session_tools_transcript.go agent/transcript_render.go agent/transcript_render_test.go agent/transcript_render_job_test.go
git add -- agent/jobs_activity.go agent/jobs_activity_past.go agent/historical_jobs.go agent/status.go agent/events/events.go agent/events/payloads.go agent/events/eventdata.go
git add -- agent/internal/delegatestore/read_events.go agent/internal/delegatestore/read_events_test.go
git add -- agent/doctor/doctor.go agent/doctor/audit.go agent/doctor/jobs.go agent/doctor/tree.go agent/doctor/sessions.go agent/doctor/watches.go agent/doctor/audit_test.go agent/doctor/jobs_test.go agent/doctor/tree_test.go agent/doctor/sessions_test.go agent/doctor/watches_test.go
git add -- cmd/evener-doctor/main.go cmd/evener-doctor/main_test.go cmd/evener-doctor/README.md cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/prober_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_test.go
git commit -m "feat: project stable delegates without mutation" -m "Cut stable tool, list, status, activity, history, doctor, prober, and Hub thread-read surfaces to the delegate aggregate while preserving paging and fidelity. Cold readers now use pure ReadEvents/Fold paths and status remains metadata-only and non-acknowledging."
~~~

---

### Task 11: AppWire, Hub, TUI, and web cutover

**Files — Go projection and transport:**

- Create: internal/appprojector/delegate_projection_test.go.
- Modify: agent/events/events.go, agent/events/payloads.go, agent/events/eventdata.go, agent/events/events_test.go, agent/events/events_fuzz_test.go, agent/events/payloads_test.go, agent/events/eventdata_program_fuzz_test.go.
- Modify: agent/status.go, agent/status_test.go, agent/status_support_program_fuzz_test.go.
- Modify: agent/jobs_activity.go, agent/jobs_activity_past.go, agent/tree_counter.go.
- Modify: internal/appprojector/appwire_projection.go, internal/appprojector/appwire_projection_test.go.
- Modify: appwire/types.go, appwire/protocol.go, appwire/types_test.go, appwire/protocol_test.go.
- Modify: server/server.go, server/server_test.go, server/server_surface_fuzz_test.go, server/appwire_runtime.go, server/appwire_runtime_test.go, server/thread_envelope.go, server/thread_envelope_test.go.
- Modify: cmd/evener/serve.go, cmd/evener/serve_test.go, cmd/evener/serve_coverage_fuzz_test.go, cmd/evener/run_drain_test.go, cmd/evener/run_drain_nested_test.go.
- Modify: cmd/evener-hub/app_jobs.go, cmd/evener-hub/app_jobs_test.go, cmd/evener-hub/app_threadread.go, cmd/evener-hub/app_threadread_test.go.
- Modify: cmd/evener-tui/hub_notifications.go, cmd/evener-tui/hub_notifications_test.go, cmd/evener-tui/hub_notifications_fuzz_test.go, cmd/evener-tui/model_misc_serffuzz_test.go.
- Modify: cmd/evener-tui/internal/transcript/job_notification.go, cmd/evener-tui/internal/transcript/reducer.go, cmd/evener-tui/internal/transcript/types.go, cmd/evener-tui/internal/transcript/reducer_test.go, cmd/evener-tui/internal/transcript/cov_rtui_transcript_test.go, cmd/evener-tui/internal/transcript/reducer_fuzz_test.go, cmd/evener-tui/internal/transcript/fuzz_coverage_union_test.go.
- Modify: cmd/evener-tui/internal/msgrender/tool_bodies.go, cmd/evener-tui/internal/msgrender/tool_bodies_test.go, cmd/evener-tui/internal/msgrender/tool_renderers.go, cmd/evener-tui/internal/msgrender/tool_renderers_test.go, cmd/evener-tui/internal/msgrender/tool_renderers_fuzz_test.go, cmd/evener-tui/internal/msgrender/cov_rtui_msgrender_test.go.
- Modify: cmd/evener-tui/internal/toolsummary/tool_summary.go, cmd/evener-tui/internal/toolsummary/tool_summary_test.go, cmd/evener-tui/internal/toolsummary/tool_summary_fuzz_test.go, cmd/evener-tui/internal/toolsummary/fuzz_coverage_union_test.go.

**Files — generated and web:**

- Generate through make generate: cmd/evener-hub/frontend/src/protocol/types.gen.ts and docs/appwire-protocol.md.
- Modify: cmd/evener-hub/frontend/src/stores/threads.ts, cmd/evener-hub/frontend/src/stores/threads.test.ts, cmd/evener-hub/frontend/src/stores/activityPanel.ts, cmd/evener-hub/frontend/src/stores/activityPanel.test.ts.
- Modify: cmd/evener-hub/frontend/src/protocol/model.ts, cmd/evener-hub/frontend/src/protocol/reducer.ts, cmd/evener-hub/frontend/src/protocol/reducer.test.ts.
- Modify: cmd/evener-hub/frontend/src/panes/session/chrome/activityData.ts, cmd/evener-hub/frontend/src/panes/session/chrome/activityData.test.ts, cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.ts, cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.test.ts, cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.tsx, cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx.
- Modify: cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts, cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.test.ts.
- Modify: cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts, cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts, cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx, cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx.
- Modify: cmd/evener-hub/frontend/src/protocol/fixtures/tool-and-jobs.jsonl, cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx.

**Interfaces:**

- Consume lossless stable snapshots, canonical packets, descendant event callbacks, ParentDelegateID shells, typed watches, and per-delegate monotonic projection revision.
- Produce DELEGATE_UPDATED internally and evener/delegate/updated on AppWire, carrying one immutable stable snapshot. SerfDelegateInfo and SerfDiagnostics.Delegates carry the same stable fields through live, reconnect, and cold thread-read. Neither stable type carries call-scoped wait_ignored_reason; delegate_send results and their transcript/UI transport carry it separately. Projection revision fences rendering only; it is not a control identity.
- Replace address-derived tree-clock sharing with one explicitly inherited *jobActivityClock for projection ordering only. It carries no lifecycle, generation, capacity, authorization, phase, or stop state.
- Remove activation cards and legacy Detailed.Jobs discovery without removing send/stop/status/watch/observer/navigation capability.

- [ ] **Step 1: Prove event inheritance and lossless Go DTOs**

Add:

~~~go
func TestDelegateProjection_DescendantOrdinaryEventsReachRootTransport(t *testing.T)
func TestDelegateProjection_LateRootReceivesStableDelegateSnapshot(t *testing.T)
func TestDelegateProjection_OwnerRootFencesForeignUpdates(t *testing.T)
func TestDelegateProjection_RevisionRejectsStaleStateButMergesLatestActivityByMax(t *testing.T)
func TestDelegateProjection_PreservesNullValidationExhaustionAndTurnSlots(t *testing.T)
func TestDelegateProjection_PreservesTimingUsageQuietWorktreeWarningsAndDiagnostics(t *testing.T)
func TestDelegateProjection_ShellUsesParentDelegateID(t *testing.T)
func TestDelegateProjection_TranscriptPreseedSubscriptionAndThreadReadRemainAvailable(t *testing.T)
func TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen(t *testing.T)
func TestAgentToServerDetailedStatus_DelegatesLossless(t *testing.T)
func TestStatusEndpoint_DetailedStatusIncludesStableDelegates(t *testing.T)
func TestAppDiagnosticsFromDetailedStatus_DelegatesLossless(t *testing.T)
func TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus(t *testing.T)
~~~

Immediately before event/DTO implementation, run:

~~~bash
go test ./internal/appprojector -run '^TestDelegateProjection_' -count=1
go test ./agent -run '^TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen$' -count=1
go test ./cmd/evener -run '^TestAgentToServerDetailedStatus_DelegatesLossless$' -count=1
go test ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=1
go test ./cmd/evener-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=1
~~~

Keep the root-installed descendant callback installed on child spawn. DELEGATE_UPDATED carries projection_revision and refreshes facetDiagnostics in the thread freshness table. JOB_STARTED/JOB_FINISHED are shell-only. Carry all stable fidelity fields explicitly through agent events, status, app projector, AppWire, server, Hub, and TUI types, while omitting call-scoped wait_ignored_reason from those snapshots. Carry that field only on delegate_send result DTOs and their transcript/UI rendering. Latest activity is max-merged independently of revision. Do not reconstruct packets or infer structured-result presence from decoded value. Run GREEN count 20/race.

- [ ] **Step 2: Prove TUI stable behavior**

Add or update:

~~~go
func TestTUIStableDelegateNotificationHasNoDelegateJobIdentity(t *testing.T)
func TestTUIStableDelegateReducerRejectsStaleRevision(t *testing.T)
func TestTUIStableDelegateRendersTimingUsageQuietWorktreeAndWarnings(t *testing.T)
func TestTUIStableDelegateShellRemainsJobAddressed(t *testing.T)
func TestTUIStableDelegateWatchAndObserverNoticesRemainVisible(t *testing.T)
~~~

Immediately before TUI changes, run:

~~~bash
go test ./cmd/evener-tui/... -run '^TestTUIStableDelegate' -count=1
~~~

Render the resource label as Delegate and control it by dlg_. Retain shell job markup and stable watch/observer notices. Run GREEN and race before continuing:

~~~bash
go test ./cmd/evener-tui/... -run '^TestTUIStableDelegate' -count=20
go test -race ./cmd/evener-tui/... -run '^TestTUIStableDelegate' -count=20
~~~

- [ ] **Step 3: Prove web stores, activity tree, and transcript controls**

Add exact Vitest cases in the named test files:

- threads.test.ts: ignores stale delegate revision, preserves descendant ordinary events, and restores a late stable snapshot.
- activityPanel.test.ts: stable delegate selection survives reconnect and opens its child session.
- reducer.test.ts: carries explicit null, validation, exhaustion, timing, usage, worktree, warnings, turn slots, and stable diagnostics while omitting call-scoped wait reason from stable state.
- activityData.test.ts and activityRows.test.ts: render stable delegate lineage and ParentDelegateID shell ancestry.
- ActivityTree.test.tsx: stable delegate rows retain navigation, send, stop, status, watch, and observer affordances without activation cards.
- ToolCallItem.test.tsx: update active delegate fixtures from activation `job_id` to stable `delegate_id`, reject activation-only delegate controls, and retain `job_id` only for real shell-job fixtures.
- jobTools.test.tsx, subagentModule.test.tsx, and subagentModuleStore.test.ts: use delegate_id, never synthesize a delegate job, preserve wait_ignored_reason on the delegate_send result and its transcript rendering, and never copy it into the stable module snapshot.
- steeringClassify.test.ts, NotificationCard.test.tsx, and SteeringItem.test.tsx: distinguish delegate notification markup from shell job notification markup.

Immediately before web implementation, run:

~~~bash
scripts/web-preflight.sh
cd cmd/evener-hub/frontend
npx vitest run --maxWorkers=4 src/stores/threads.test.ts src/stores/activityPanel.test.ts src/protocol/reducer.test.ts src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/activityRows.test.ts src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/tools/subagentModuleStore.test.ts src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx
cd ../../..
~~~

Expected RED: the current generated type or at least one store/component still requires activation job data.

From the repository root, run make generate. Then implement the stores and components against generated stable DTOs. Run Biome only on the touched source files.

- [ ] **Step 4: Run cutover gates and commit**

~~~bash
gofmt -w internal/appprojector/delegate_projection_test.go agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/events/events_test.go agent/events/events_fuzz_test.go agent/events/payloads_test.go agent/events/eventdata_program_fuzz_test.go agent/status.go agent/status_test.go agent/status_support_program_fuzz_test.go agent/jobs_activity.go agent/jobs_activity_past.go agent/tree_counter.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/types.go appwire/protocol.go appwire/types_test.go appwire/protocol_test.go server/server.go server/server_test.go server/server_surface_fuzz_test.go server/appwire_runtime.go server/appwire_runtime_test.go server/thread_envelope.go server/thread_envelope_test.go cmd/evener/serve.go cmd/evener/serve_test.go cmd/evener/serve_coverage_fuzz_test.go cmd/evener/run_drain_test.go cmd/evener/run_drain_nested_test.go cmd/evener-hub/app_jobs.go cmd/evener-hub/app_jobs_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_test.go cmd/evener-tui/hub_notifications.go cmd/evener-tui/hub_notifications_test.go cmd/evener-tui/hub_notifications_fuzz_test.go cmd/evener-tui/model_misc_serffuzz_test.go cmd/evener-tui/internal/transcript/job_notification.go cmd/evener-tui/internal/transcript/reducer.go cmd/evener-tui/internal/transcript/types.go cmd/evener-tui/internal/transcript/reducer_test.go cmd/evener-tui/internal/transcript/cov_rtui_transcript_test.go cmd/evener-tui/internal/transcript/reducer_fuzz_test.go cmd/evener-tui/internal/transcript/fuzz_coverage_union_test.go cmd/evener-tui/internal/msgrender/tool_bodies.go cmd/evener-tui/internal/msgrender/tool_bodies_test.go cmd/evener-tui/internal/msgrender/tool_renderers.go cmd/evener-tui/internal/msgrender/tool_renderers_test.go cmd/evener-tui/internal/msgrender/tool_renderers_fuzz_test.go cmd/evener-tui/internal/msgrender/cov_rtui_msgrender_test.go cmd/evener-tui/internal/toolsummary/tool_summary.go cmd/evener-tui/internal/toolsummary/tool_summary_test.go cmd/evener-tui/internal/toolsummary/tool_summary_fuzz_test.go cmd/evener-tui/internal/toolsummary/fuzz_coverage_union_test.go
make generate
cd cmd/evener-hub/frontend
npx biome check --write src/stores/threads.ts src/stores/threads.test.ts src/stores/activityPanel.ts src/stores/activityPanel.test.ts src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts src/panes/session/chrome/activityData.ts src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/activityRows.ts src/panes/session/chrome/activityRows.test.ts src/panes/session/chrome/ActivityTree.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/transcript/ToolCallItem.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/subagentModule.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/tools/subagentModuleStore.ts src/panes/session/transcript/tools/subagentModuleStore.test.ts src/panes/session/transcript/messages/steeringClassify.ts src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/messages/NotificationCard.tsx src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx src/dev/overflowharness-entry.tsx
npx vitest run --maxWorkers=4 src/stores/threads.test.ts src/stores/activityPanel.test.ts src/protocol/reducer.test.ts src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/activityRows.test.ts src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/tools/subagentModuleStore.test.ts src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx
cd ../../..
make test-web
go test ./internal/appprojector ./appwire ./server ./cmd/evener ./cmd/evener-hub ./cmd/evener-tui/... -count=1
go test ./agent -run '^TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen$' -count=20
go test -race ./agent -run '^TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen$' -count=20
go test ./cmd/evener -run '^TestAgentToServerDetailedStatus_DelegatesLossless$' -count=20
go test -race ./cmd/evener -run '^TestAgentToServerDetailedStatus_DelegatesLossless$' -count=20
go test ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test -race ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test ./cmd/evener-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test -race ./cmd/evener-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test ./internal/appprojector -run '^TestDelegateProjection_' -count=20
go test -race ./internal/appprojector -run '^TestDelegateProjection_' -count=20
make lint-generated
make fuzz
make fuzz-registry-check
git diff --check
git status --short
~~~

Stage only:

~~~bash
git add -- internal/appprojector/delegate_projection_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go
git add -- agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/events/events_test.go agent/events/events_fuzz_test.go agent/events/payloads_test.go agent/events/eventdata_program_fuzz_test.go agent/status.go agent/status_test.go agent/status_support_program_fuzz_test.go agent/jobs_activity.go agent/jobs_activity_past.go agent/tree_counter.go
git add -- appwire/types.go appwire/protocol.go appwire/types_test.go appwire/protocol_test.go server/server.go server/server_test.go server/server_surface_fuzz_test.go server/appwire_runtime.go server/appwire_runtime_test.go server/thread_envelope.go server/thread_envelope_test.go
git add -- cmd/evener/serve.go cmd/evener/serve_test.go cmd/evener/serve_coverage_fuzz_test.go cmd/evener/run_drain_test.go cmd/evener/run_drain_nested_test.go cmd/evener-hub/app_jobs.go cmd/evener-hub/app_jobs_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_test.go
git add -- cmd/evener-tui/hub_notifications.go cmd/evener-tui/hub_notifications_test.go cmd/evener-tui/hub_notifications_fuzz_test.go cmd/evener-tui/model_misc_serffuzz_test.go
git add -- cmd/evener-tui/internal/transcript/job_notification.go cmd/evener-tui/internal/transcript/reducer.go cmd/evener-tui/internal/transcript/types.go cmd/evener-tui/internal/transcript/reducer_test.go cmd/evener-tui/internal/transcript/cov_rtui_transcript_test.go cmd/evener-tui/internal/transcript/reducer_fuzz_test.go cmd/evener-tui/internal/transcript/fuzz_coverage_union_test.go
git add -- cmd/evener-tui/internal/msgrender/tool_bodies.go cmd/evener-tui/internal/msgrender/tool_bodies_test.go cmd/evener-tui/internal/msgrender/tool_renderers.go cmd/evener-tui/internal/msgrender/tool_renderers_test.go cmd/evener-tui/internal/msgrender/tool_renderers_fuzz_test.go cmd/evener-tui/internal/msgrender/cov_rtui_msgrender_test.go
git add -- cmd/evener-tui/internal/toolsummary/tool_summary.go cmd/evener-tui/internal/toolsummary/tool_summary_test.go cmd/evener-tui/internal/toolsummary/tool_summary_fuzz_test.go cmd/evener-tui/internal/toolsummary/fuzz_coverage_union_test.go
git add -- cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git add -- cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.test.ts cmd/evener-hub/frontend/src/stores/activityPanel.ts cmd/evener-hub/frontend/src/stores/activityPanel.test.ts cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/protocol/reducer.test.ts
git add -- cmd/evener-hub/frontend/src/panes/session/chrome/activityData.ts cmd/evener-hub/frontend/src/panes/session/chrome/activityData.test.ts cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.ts cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.test.ts cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.tsx cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx
git add -- cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.test.ts
git add -- cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx cmd/evener-hub/frontend/src/protocol/fixtures/tool-and-jobs.jsonl cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx
git commit -m "feat: cut clients to stable delegates" -m "Carry stable delegate events and lossless lifecycle metadata through AppWire, Hub, TUI, and web while preserving descendant events, navigation, watches, observers, shells, timing, usage, quiet, and worktree evidence. Remove activation cards and Detailed.Jobs discovery without removing stable control capability."
~~~

---

### Task 12: Legacy retirement and semantic dormancy

**Files:**

- Create: agent/delegate_legacy_dormancy_test.go.
- Modify or delete only delegate lifecycle branches in: agent/internal/jobstore/event.go, agent/internal/jobstore/event_clone.go, agent/internal/jobstore/record.go, agent/internal/jobstore/fold.go, agent/internal/jobstore/store.go.
- Modify or rewrite: agent/internal/jobstore/cov_s4_jobstore_test.go, agent/internal/jobstore/event_test.go, agent/internal/jobstore/fold_fuzz_test.go, agent/internal/jobstore/fold_test.go, agent/internal/jobstore/jobstore_program_fuzz_test.go, agent/internal/jobstore/record_test.go, agent/internal/jobstore/seqfuzz_test.go, agent/internal/jobstore/store_incremental_test.go, agent/internal/jobstore/store_persistence_fuzz_test.go, agent/internal/jobstore/store_test.go.
- Modify or delete only unreachable legacy delegate definitions in: agent/job_delegate.go, agent/subagents.go, agent/subagent_manager.go, agent/jobs.go, agent/jobs_nested.go, agent/job_notify.go, agent/session_tools_jobs.go, agent/session_outline.go, agent/historical_jobs.go. Do not change a registered route proven GREEN in Step 1.
- Modify or delete only unreachable legacy delegate definitions in: agent/session.go, agent/session_config.go, agent/session_init.go, agent/session_lifecycle.go, agent/delegate_runtime.go, identifier/domains.go, identifier/domains_test.go. Do not change a registered route proven GREEN in Step 1.
- Rewrite against registered stable behavior or remove implementation-shape cases in: agent/job_delegate_test.go, agent/job_delegate_create_test.go, agent/job_delegate_decode_test.go, agent/job_delegate_send_test.go, agent/job_delegate_send_fifo_test.go, agent/job_delegate_finalize_test.go, agent/job_delegate_finalize_retry_structured_test.go, agent/job_delegate_drivedown_test.go, agent/job_delegate_budget_test.go, agent/job_delegate_isolation_test.go, agent/job_delegate_model_echo_test.go, agent/job_delegate_model_selection_test.go, agent/job_nested_test.go, agent/subagent_manager_test.go, agent/subagents_test.go.
- Rewrite or retire the exact legacy fuzz owners in: agent/delegate_seqfuzz_test.go, agent/fuzz_lx_delegate_test.go, agent/fuzz_delegate_creation_restore_config_test.go, agent/fuzz_delegate_finalize_report_test.go, agent/fuzz_jd_classify_delegate_send_target_test.go, agent/fuzz_jd_resolve_delegate_terminal_status_test.go, agent/fuzz_jd_validate_delegate_grant_test.go, agent/fuzz_jdr_restore_lifecycle_test.go, agent/job_delegate_attach_finalize_seed100_fuzz_test.go, agent/job_delegate_exact_create_send_fuzz_test.go, agent/job_delegate_exact_finalize_report_fuzz_test.go, agent/job_delegate_exact_restore_fuzz_test.go, agent/job_delegate_exact_running_attach_fuzz_test.go, agent/job_delegate_exact_tail_create_restore_fuzz_test.go, agent/job_delegate_exact_tail_finalize_fuzz_test.go, agent/job_delegate_exact_tail_running_fuzz_test.go, agent/job_delegate_git_report_seed100_fuzz_test.go, agent/job_delegate_sandbox_schema_seed100_fuzz_test.go, agent/job_delegate_seed100_fuzz_test.go, agent/job_delegate_send_fuzz_test.go, agent/job_delegate_send_seed100_fuzz_test.go, agent/subagents_fuzz_test.go, agent/subagents_seed100_exact_fuzz_test.go, agent/nested_subagent_lifecycle_program_fuzz_test.go.
- Modify only the retired delegate subtargets in these mixed fuzz owners: agent/root_watch_tree_program_fuzz_test.go, agent/job_watch_delegate_fuzz_test.go, agent/session_tools_jobs_contract_program_fuzz_test.go. Preserve their stable watch and stable tool coverage.
- Modify: audit and rewrite or retire only non-allowlisted legacy references in these exact coverage owners: agent/agent_misc_program_fuzz_test.go, agent/cov_s1_classify_test.go, agent/cov_s1_job_log_helpers_test.go, agent/cov_s1_watch_targets_test.go, agent/cov_s1_watchlist_receiver_test.go, agent/cov_w2dlg_helpers_test.go, agent/cov_w2dlg_newjm_test.go, agent/cov_w2dlg_restore_helpers_test.go, agent/cov_w2dlg_resume_finalize_test.go, agent/cov_w2tail_jobs_helpers_test.go, agent/cov_w2watch_list_test.go, agent/cov_w3dlg_attach_test.go, agent/cov_w3dlg_finalize_test.go, agent/cov_w3dlg_resume_test.go, agent/cov_w3dlg_send_test.go, agent/cov_w3dlg_sendrunning_test.go.
- Modify: audit and rewrite or retire only non-allowlisted legacy references in these exact watch/notification owners: agent/doctor/watches_receiver_test.go, agent/fuzz_jd_keep_listed_job_row_test.go, agent/fuzz_lx_notify_test.go, agent/fuzz_wv_validate_send_target_test.go, agent/fuzz_wx_watch_test.go, agent/job_notify_consume_test.go, agent/job_notify_test.go, agent/job_watch_config_test.go, agent/job_watch_drain_render_fuzz_test.go, agent/job_watch_loopguard_test.go, agent/job_watch_pending_state_fuzz_test.go, agent/job_watch_registry_receiver_test.go, agent/job_watch_seams_fuzz_test.go, agent/job_watch_send_test.go, agent/job_watch_test.go, agent/job_watch_timers_observe_fuzz_test.go, agent/notification_test.go, agent/shell_notify_digest_program_fuzz_test.go.
- Modify: audit and rewrite or retire only non-allowlisted legacy references in these exact job/session owners: agent/job_manager_error_recovery_fuzz_test.go, agent/job_reconcile_test.go, agent/job_shell_seed100_fuzz_test.go, agent/job_transcript_read_test.go, agent/jobs_activity_past_test.go, agent/jobs_activity_test.go, agent/jobs_nested_seed100_more_test.go, agent/jobs_seed100_fuzz_test.go, agent/jobs_seed100_more_test.go, agent/jobs_seed100_range_a_test.go, agent/jobs_test.go, agent/lifecycle_ops_test.go, agent/nested_drain_branches_fuzz_test.go, agent/session_jobtree_drain_seed100_more_test.go, agent/session_jobtree_drain_stall_test.go, agent/session_jobtree_drain_test.go, agent/session_misc_fuzz_test.go, agent/session_provenance_test.go, agent/session_restore_close_status_program_fuzz_test.go, agent/session_subagent_livetree_test.go, agent/session_tools_jobs_fuzz_test.go, agent/session_tools_jobs_seed100_final_test.go, agent/session_tools_jobs_seed100_more_test.go, agent/session_tools_jobs_seed100_range_b_test.go, agent/session_tools_jobs_seed100_range_c_test.go, agent/session_tools_jobs_seed100_range_d_test.go, agent/session_tools_transcript_job_read_test.go.
- Modify: audit and rewrite or retire only non-allowlisted legacy references in these exact isolation/lifecycle owners: agent/sandbox_delegate_create_test.go, agent/session_init_worktree_seed100_fuzz_test.go, agent/session_lifecycle_tail_coverage_fuzz_test.go, agent/session_tools_worktree_dispose_execute_test.go, agent/session_tools_worktree_dispose_resume_test.go, agent/session_tools_worktree_remove_force_dispose_test.go. ParentJobID remains valid only in shell-to-shell fixtures; historical rendering and explicit legacy-rejection fixtures remain read-only allowlists.
- Modify: scripts/run-fuzz.sh with the exact manifest disposition below.
- Do not delete or weaken: agent/job_watch.go, agent/internal/jobstore/watch.go, agent/job_shell.go, agent/salvage.go, the nudge/stop-hook implementation in agent/subagents.go, agent/delegate_tree_reclaim.go, agent/delegate_resource_supervision_test.go, agent/delegate_resource_watch_test.go, agent/delegate_resource_shell_test.go, or historical rendering in agent/transcript_render.go.

**Interfaces:**

- Consume the fully registered stable path from Tasks 6–11.
- Produce a shell/watch-only JobRecord reducer, no delegate activation aliases, no current/latest job mirror, no standalone failure record, and no loose watch receiver.
- Preserve the existing watch journal, quiet supervision, hooks/nudge/salvage, admission reclamation, stable shell routing, old transcript rendering, and all client behavior.

- [ ] **Step 1: Require semantic dormancy to be GREEN before deleting authority**

Add:

~~~go
func TestDelegateLegacyDormancy_NoDelegateJobRecordCanBeCreated(t *testing.T)
func TestDelegateLegacyDormancy_NoActivationAliasResolvesForLiveControl(t *testing.T)
func TestDelegateLegacyDormancy_StableWatchJournalStillDelivers(t *testing.T)
func TestDelegateLegacyDormancy_QuietSupervisionStillDelivers(t *testing.T)
func TestDelegateLegacyDormancy_RetentionStillReclaimsOnAdmission(t *testing.T)
func TestDelegateLegacyDormancy_ShellParentDelegateIDStillRoutes(t *testing.T)
func TestDelegateLegacyDormancy_HistoricalSendStillRendersReadOnly(t *testing.T)
func TestDelegateLegacyDormancy_LegacyLifecycleAndWatchRowsFailClosed(t *testing.T)
~~~

These cases exercise the registered tools, durable folds, and preserved behavior; they are not identifier-name-only assertions. Immediately before any production removal, format the new test and run the semantic-dormancy gate as required GREEN evidence:

~~~bash
gofmt -w agent/delegate_legacy_dormancy_test.go
go test ./agent -run '^TestDelegateLegacyDormancy_' -count=20
go test -race ./agent -run '^TestDelegateLegacyDormancy_' -count=20
go test ./agent -run '^(TestStableDelegateWatch_|TestDelegateResourceSupervision_|TestDelegateRuntimeReclaim_|TestStableDelegateShell_|TestStableDelegateReadOnly_)' -count=20
go test -race ./agent -run '^(TestStableDelegateWatch_|TestDelegateResourceSupervision_|TestDelegateRuntimeReclaim_|TestStableDelegateShell_|TestStableDelegateReadOnly_)' -count=20
~~~

Expected GREEN: Tasks 6–11 have already cut every registered delegate route to the stable controller while preserving the named surfaces. If any case is RED, stop Task 12 and return the defect to its exact Task 6–11 owner. Add the causal behavioral RED there, make the smallest owner-task fix, rerun that task's gates, and create a corrective owner-task commit before restarting this GREEN gate. Task 12 must not repair a live route or claim that earlier-task failure as its own RED.

Do not create a separate routing commit in this task. With the gate GREEN, any remaining delegate JobRecord definitions are unreachable structure rather than registered behavior.

- [ ] **Step 2: Use source/schema inventory as structural RED, then delete dormant authority**

Before changing production, inventory the unreachable definitions below. Their non-allowlisted matches are the structural RED for this deletion-only task. Then delete only the branches and types that create, fold, mirror, or identify delegate JobRecords, plus their implementation-shape tests and fuzz targets. Retain shell/watch JobRecord types and every allowlisted stable or historical surface. The Step 1 GREEN gate proves public dormancy; this step proves the old authority is physically absent without changing the stable contract.

In scripts/run-fuzz.sh remove the rows for FuzzLxValidateDelegateRestoreState, FuzzDelegateCreationRestoreConfigProgram, FuzzRootDelegateResumeLifecycleProgram, FuzzDelegateFinalizeReportProgram, FuzzDgfzSendDelegateMessage, FuzzWatchdelDelegateResume, FuzzJdValidateDelegateGrant, FuzzJdResolveDelegateTerminalStatus, FuzzJdClassifyDelegateSendTarget, FuzzJdrDelegateRestoreLifecycle, and every FuzzJobDelegate-prefixed target after their old functions are removed. Rewrite FuzzJobtoolsContractProgram to point only at stable tool functions. Retain FuzzWatchdelWatchOps, FuzzRootWatchTreeProgram, every generic watch-journal target, FuzzWvQuietWatchdogTick, the three Task 1–5 delegate-store/controller/restart fuzz targets, and the Task 8–9 attention/watch/reclamation targets.

Run these inventories before and after deletion. Before deletion, at least one command must identify the unreachable legacy authority as the required structural RED; after deletion, every command must be empty or contain only an explicit historical-fixture/legacy-rejection allowlist documented in delegate_legacy_dormancy_test.go:

~~~bash
rg -n 'JobTypeDelegate|JobDelegate|job_type.*delegate|DelegateJobID|CurrentJobID|LatestJobID|DelegateGeneration|StopGateClosed|findRunningDelegateByTranscriptRef|resumeOrFindRunningDelegate|relinkDelegateChildToJob|attachDelegateJob|finalizeDelegateOnce|ReceiverDelegateID|receiverDelegateID|applyReceiverWatchSend|installParentSourceWatchForChild|clearParentSourceWatchForChild|attachDelegateJobFromWatch|runFromWatch|staleDelegateWatchSend|delegateStoppedAfterWatchSendPending|delegate failure record' agent identifier --glob '*.go'
rg -n 'ParentJobID' agent --glob '*.go'
rg -n 'activation.*(status|output|history|watch|stop)|unsupported_delegate_watch' agent cmd internal appwire --glob '*.{go,ts,tsx,md}'
rg -n 'Detailed\.Jobs' cmd/evener-hub internal appwire --glob '*.{go,ts,tsx}'
~~~

ParentJobID remains legal only for shell-to-shell ancestry; it must not encode delegate lineage. Detailed.Jobs remains legal only for real jobs, never delegate discovery.

Run Task 12 GREEN and broader gates:

~~~bash
task12_go_files=$(git diff --name-only --diff-filter=ACM HEAD -- '*.go')
test -z "$task12_go_files" || gofmt -w $task12_go_files
go test ./agent -run '^TestDelegateLegacyDormancy_' -count=20
go test -race ./agent -run '^TestDelegateLegacyDormancy_' -count=20
go test ./agent/internal/jobstore -count=1
go test ./agent -run '^(TestStableDelegateWatch_|TestDelegateResourceSupervision_|TestDelegateRuntimeReclaim_|TestStableDelegateShell_|TestStableDelegateReadOnly_)' -count=20
go test -race ./agent -run '^(TestStableDelegateWatch_|TestDelegateResourceSupervision_|TestDelegateRuntimeReclaim_|TestStableDelegateShell_|TestStableDelegateReadOnly_)' -count=20
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^(FuzzDelegateAttentionFold|FuzzStableDelegateWatchDelivery|FuzzDelegateReclaimStopRestart)$' -count=1
make fuzz
make fuzz-registry-check
git diff --check
git status --short
~~~

Stage only:

~~~bash
git add -- agent/delegate_legacy_dormancy_test.go agent/internal/jobstore/event.go agent/internal/jobstore/event_clone.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go agent/internal/jobstore/store.go
git add -- agent/internal/jobstore/cov_s4_jobstore_test.go agent/internal/jobstore/event_test.go agent/internal/jobstore/fold_fuzz_test.go agent/internal/jobstore/fold_test.go agent/internal/jobstore/jobstore_program_fuzz_test.go agent/internal/jobstore/record_test.go agent/internal/jobstore/seqfuzz_test.go agent/internal/jobstore/store_incremental_test.go agent/internal/jobstore/store_persistence_fuzz_test.go agent/internal/jobstore/store_test.go
git add -- agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/jobs.go agent/jobs_nested.go agent/job_notify.go agent/session_tools_jobs.go agent/session_outline.go agent/historical_jobs.go
git add -- agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/delegate_runtime.go identifier/domains.go identifier/domains_test.go
git add -- agent/job_delegate_test.go agent/job_delegate_create_test.go agent/job_delegate_decode_test.go agent/job_delegate_send_test.go agent/job_delegate_send_fifo_test.go agent/job_delegate_finalize_test.go agent/job_delegate_finalize_retry_structured_test.go agent/job_delegate_drivedown_test.go agent/job_delegate_budget_test.go agent/job_delegate_isolation_test.go agent/job_delegate_model_echo_test.go agent/job_delegate_model_selection_test.go agent/job_nested_test.go agent/subagent_manager_test.go agent/subagents_test.go
git add -- agent/delegate_seqfuzz_test.go agent/fuzz_lx_delegate_test.go agent/fuzz_delegate_creation_restore_config_test.go agent/fuzz_delegate_finalize_report_test.go agent/fuzz_jd_classify_delegate_send_target_test.go agent/fuzz_jd_resolve_delegate_terminal_status_test.go agent/fuzz_jd_validate_delegate_grant_test.go agent/fuzz_jdr_restore_lifecycle_test.go
git add -- agent/root_watch_tree_program_fuzz_test.go agent/job_watch_delegate_fuzz_test.go agent/session_tools_jobs_contract_program_fuzz_test.go
git add -- agent/agent_misc_program_fuzz_test.go agent/cov_s1_classify_test.go agent/cov_s1_job_log_helpers_test.go agent/cov_s1_watch_targets_test.go agent/cov_s1_watchlist_receiver_test.go agent/cov_w2dlg_helpers_test.go agent/cov_w2dlg_newjm_test.go agent/cov_w2dlg_restore_helpers_test.go agent/cov_w2dlg_resume_finalize_test.go agent/cov_w2tail_jobs_helpers_test.go agent/cov_w2watch_list_test.go agent/cov_w3dlg_attach_test.go agent/cov_w3dlg_finalize_test.go agent/cov_w3dlg_resume_test.go agent/cov_w3dlg_send_test.go agent/cov_w3dlg_sendrunning_test.go
git add -- agent/doctor/watches_receiver_test.go agent/fuzz_jd_keep_listed_job_row_test.go agent/fuzz_lx_notify_test.go agent/fuzz_wv_validate_send_target_test.go agent/fuzz_wx_watch_test.go agent/job_notify_consume_test.go agent/job_notify_test.go agent/job_watch_config_test.go agent/job_watch_drain_render_fuzz_test.go agent/job_watch_loopguard_test.go agent/job_watch_pending_state_fuzz_test.go agent/job_watch_registry_receiver_test.go agent/job_watch_seams_fuzz_test.go agent/job_watch_send_test.go agent/job_watch_test.go agent/job_watch_timers_observe_fuzz_test.go agent/notification_test.go agent/shell_notify_digest_program_fuzz_test.go
git add -- agent/job_manager_error_recovery_fuzz_test.go agent/job_reconcile_test.go agent/job_shell_seed100_fuzz_test.go agent/job_transcript_read_test.go agent/jobs_activity_past_test.go agent/jobs_activity_test.go agent/jobs_nested_seed100_more_test.go agent/jobs_seed100_fuzz_test.go agent/jobs_seed100_more_test.go agent/jobs_seed100_range_a_test.go agent/jobs_test.go agent/lifecycle_ops_test.go agent/nested_drain_branches_fuzz_test.go agent/session_jobtree_drain_seed100_more_test.go agent/session_jobtree_drain_stall_test.go agent/session_jobtree_drain_test.go agent/session_misc_fuzz_test.go agent/session_provenance_test.go agent/session_restore_close_status_program_fuzz_test.go agent/session_subagent_livetree_test.go agent/session_tools_jobs_fuzz_test.go agent/session_tools_jobs_seed100_final_test.go agent/session_tools_jobs_seed100_more_test.go agent/session_tools_jobs_seed100_range_b_test.go agent/session_tools_jobs_seed100_range_c_test.go agent/session_tools_jobs_seed100_range_d_test.go agent/session_tools_transcript_job_read_test.go
git add -- agent/sandbox_delegate_create_test.go agent/session_init_worktree_seed100_fuzz_test.go agent/session_lifecycle_tail_coverage_fuzz_test.go agent/session_tools_worktree_dispose_execute_test.go agent/session_tools_worktree_dispose_resume_test.go agent/session_tools_worktree_remove_force_dispose_test.go
git add -- agent/job_delegate_attach_finalize_seed100_fuzz_test.go agent/job_delegate_exact_create_send_fuzz_test.go agent/job_delegate_exact_finalize_report_fuzz_test.go agent/job_delegate_exact_restore_fuzz_test.go agent/job_delegate_exact_running_attach_fuzz_test.go agent/job_delegate_exact_tail_create_restore_fuzz_test.go agent/job_delegate_exact_tail_finalize_fuzz_test.go agent/job_delegate_exact_tail_running_fuzz_test.go
git add -- agent/job_delegate_git_report_seed100_fuzz_test.go agent/job_delegate_sandbox_schema_seed100_fuzz_test.go agent/job_delegate_seed100_fuzz_test.go agent/job_delegate_send_fuzz_test.go agent/job_delegate_send_seed100_fuzz_test.go agent/subagents_fuzz_test.go agent/subagents_seed100_exact_fuzz_test.go agent/nested_subagent_lifecycle_program_fuzz_test.go scripts/run-fuzz.sh
git commit -m "refactor: delete dormant delegate job schema" -m "Delete the now-unreachable delegate JobRecord reducer branches, activation fields, helpers, identifiers, tests, and fuzz registrations after the registered route is already stable-only. Preserve the existing watch journal, shell jobs, supervision, reclamation, historical rendering, and explicit fail-closed legacy detection."
~~~

---

### Task 13: Shipped documentation and prompts

**Files:**

- Modify: docs/subagent-management/11-delegate-resource-model.md status only after implementation matches it.
- Modify: docs/architecture.md, docs/job-control.md, docs/subagent-runtime-contracts.md, docs/tools/transcripts.md, docs/hooks.md.
- Modify: agent/prompts/sections/delegation.md, agent/prompts/sections/background-jobs.md, agent/prompts/templates/subagent.md.tmpl.
- Modify: internal/bundled/agents/subagent.md, internal/bundled/plugins/coordinator-workflow/agents/coordinator.md.
- Modify: internal/bundled/skills/doctoring-evener/references/data-model.md, internal/bundled/skills/doctoring-evener/references/failure-modes.md, internal/bundled/skills/doctoring-evener/references/finding-contract.md, internal/bundled/skills/doctoring-evener/references/repair-guardrails.md, internal/bundled/skills/doctoring-evener/references/writing-runbooks.md.
- Modify: agent/bundled_prompt_tool_mentions_test.go, internal/bundled/bundled_test.go.

**Interfaces:**

- Consume the shipped tool schemas, stable behavior, legacy failure codes, and operational evidence contracts from Tasks 6–12.
- Produce one consistent public vocabulary: delegate/dlg_ for delegate control, job/job_ for shell work, typed stable watches, recursive stable stop, metadata-only status, and pure cold diagnosis.
- Preserve watch_parent, observers, quiet supervision, max_retained_terminal reclamation, hooks/nudge/salvage, shell routing, worktree/disposal, restart recovery, and historical rendering guidance.

- [ ] **Step 1: Add minimal prompt-surface contract tests and record RED**

Add:

~~~go
func TestBundledDelegatePromptUsesStableControlIdentity(t *testing.T)
func TestBundledDelegatePromptPreservesWatchSupervisionAndShellGuidance(t *testing.T)
func TestBundledCoordinatorUsesStableDelegateAndShellIdentities(t *testing.T)
~~~

These tests load the actual assembled/bundled prompt and assert only the public identity/tool-capability contract; they do not regex-match a generated script or large rendered document.

Immediately before prompt edits, run:

~~~bash
go test ./agent -run '^TestBundledDelegatePrompt' -count=1
go test ./internal/bundled -run '^TestBundledCoordinatorUsesStable' -count=1
~~~

Expected RED: shipped guidance still teaches an activation job or omits a preserved stable capability.

- [ ] **Step 2: Translate shipped docs and prompts without changing architecture**

Update each named file from its existing purpose. Remove instructions to control a delegate through an activation job. Keep shell jobs public and job-addressed. Describe typed stable watches, exact watch-parent authorization, quiet watchdog, admission reclamation, hook/nudge/salvage, canonical exhaustion and warnings, worktree disposal, provider-free restart, pure doctor reads, and the two fail-closed legacy codes where operationally relevant. Do not introduce new lifecycle shapes or copy the dated plan into evergreen docs.

Set the evergreen status to shipped only after the implementation and tests are green.

Run GREEN and documentation checks:

~~~bash
gofmt -w agent/bundled_prompt_tool_mentions_test.go internal/bundled/bundled_test.go
go test ./agent -run '^TestBundledDelegatePrompt' -count=20
go test -race ./agent -run '^TestBundledDelegatePrompt' -count=20
go test ./internal/bundled -run '^TestBundledCoordinatorUsesStable' -count=20
go test -race ./internal/bundled -run '^TestBundledCoordinatorUsesStable' -count=20
go test ./agent -run '^(TestBundledAgent|TestBundledPrompt|TestBuiltinAgent)' -count=1
go test ./internal/bundled -count=1
make lint-docs
make lint-generated
git diff --check
git status --short
~~~

Review every documentation diff for retained behavior. Stage only:

~~~bash
git add -- docs/subagent-management/11-delegate-resource-model.md docs/architecture.md docs/job-control.md docs/subagent-runtime-contracts.md docs/tools/transcripts.md docs/hooks.md
git add -- agent/prompts/sections/delegation.md agent/prompts/sections/background-jobs.md agent/prompts/templates/subagent.md.tmpl
git add -- internal/bundled/agents/subagent.md internal/bundled/plugins/coordinator-workflow/agents/coordinator.md
git add -- internal/bundled/skills/doctoring-evener/references/data-model.md internal/bundled/skills/doctoring-evener/references/failure-modes.md internal/bundled/skills/doctoring-evener/references/finding-contract.md internal/bundled/skills/doctoring-evener/references/repair-guardrails.md internal/bundled/skills/doctoring-evener/references/writing-runbooks.md
git add -- agent/bundled_prompt_tool_mentions_test.go internal/bundled/bundled_test.go
git commit -m "docs: teach stable delegate resources" -m "Update shipped architecture, job-control, transcript, hook, doctor, subagent, and coordinator guidance to use dlg_ control identities while preserving watches, observers, supervision, reclamation, shells, worktrees, recovery, and historical read behavior."
~~~

---

### Task 14: Final verification and recovery proof

**Files:**

- No planned production, test, generated, or documentation edits.
- If a check exposes a defect, return to the owning Task 6–13 test and implementation files, record a behavioral RED there, make the smallest fix, rerun that task's full gates, amend only that task's explicit staging list with the exact additional file, and add a new corrective commit. Do not hide fixes in this verification task.

**Interfaces:**

- Consume the complete flag-day branch.
- Produce evidence that every causal defect is fixed, preserved behavior remains live, legacy state fails closed, restart is provider-free, tests are mutation-sensitive, the branch is reviewable, and no push occurred.

- [ ] **Step 1: Rerun every integration-defect selector normal and race count 20**

~~~bash
agent_selector='^(TestDelegateResourceBootstrap_|TestDelegateResourceCreate_|TestDelegateResourceRuntime_|TestDelegateResourceSupervision_|TestDelegateAttention_|TestStableDelegateWatch_|TestStableDelegateObserver_|TestStableDelegateAttention_|TestStableDelegateShell_|TestDelegateRuntimeReclaim_|TestDelegateResourceStop_|TestStableDelegateWorktree_|TestStableDelegateTools_|TestStableDelegateReadOnly_|TestDelegateLegacyDormancy_|TestBundledDelegatePrompt)'
go test ./agent -run "$agent_selector" -count=20 -timeout=90m
go test -race ./agent -run "$agent_selector" -count=20 -timeout=120m
go test ./agent/doctor -run '^TestDoctorStableDelegate' -count=20
go test -race ./agent/doctor -run '^TestDoctorStableDelegate' -count=20
go test ./internal/appprojector -run '^TestDelegateProjection_' -count=20
go test -race ./internal/appprojector -run '^TestDelegateProjection_' -count=20
go test ./internal/bundled -run '^TestBundledCoordinatorUsesStable' -count=20
go test -race ./internal/bundled -run '^TestBundledCoordinatorUsesStable' -count=20
go test ./cmd/evener-tui/... -run '^TestTUIStableDelegate' -count=20
go test -race ./cmd/evener-tui/... -run '^TestTUIStableDelegate' -count=20
go test ./cmd/evener -run '^TestAgentToServerDetailedStatus_DelegatesLossless$' -count=20
go test -race ./cmd/evener -run '^TestAgentToServerDetailedStatus_DelegatesLossless$' -count=20
go test ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test -race ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test ./cmd/evener-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test -race ./cmd/evener-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test ./cmd/evener-hub/internal/hubcore -run '^TestHubProberStableDelegate' -count=20
go test -race ./cmd/evener-hub/internal/hubcore -run '^TestHubProberStableDelegate' -count=20
scripts/web-preflight.sh
cd cmd/evener-hub/frontend
npx vitest run --maxWorkers=4 src/stores/threads.test.ts src/stores/activityPanel.test.ts src/protocol/reducer.test.ts src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/activityRows.test.ts src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/tools/subagentModuleStore.test.ts src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx
cd ../../..
~~~

Every selector must enumerate at least one test; inspect the package output for accidental [no tests to run]. Save exit codes, not partial logs. There is no new RED in Task 14 because it has no implementation slice; its mutation evidence comes from the recorded pre-implementation RED for each named test. Vitest has no Go race mode, so its deterministic reducer/store/component suite runs once here after its Task 11 RED and repeated focused GREEN.

- [ ] **Step 2: Prove mutation sensitivity through temporary exact patches**

Save the clean pre-mutation `git diff`. Apply one temporary mutation at a time with apply_patch, run its exact command, capture the expected failing assertion in Kata my73, reverse that exact patch with apply_patch, rerun the same command GREEN, then verify `git diff --check` and byte-for-byte equality with the saved pre-mutation diff. Never reset, checkout, stash, or combine mutations.

1. Skip descendant callback inheritance; run `go test ./agent -run '^TestDelegateResourceCreate_DescendantEventCallbackSurvivesSpawnConfig$' -count=1`.
2. Commit/publicize create before deterministic isolation; run `go test ./agent -run '^TestDelegateResourceCreate_IsolationFailurePublishesNothing$' -count=1`.
3. Omit the post-commit resumability-closure event; run `go test ./agent -run '^TestDelegateResourceCreate_PostCommitConstructionFailureClosesResumability$' -count=1`.
4. Acknowledge a stable watch source before receiver transcript fsync; run `go test ./agent -run '^TestStableDelegateWatch_ReceiverFsyncPrecedesDeliveredAck$' -count=1`.
5. Bind the stop reconcile driver to the requesting tool context; run `go test ./agent -run '^TestDelegateResourceRuntime_PositiveStopWaitKeepsReconciliationDriverAlive$' -count=1`.
6. Omit AbortShellWork on the foreground-timeout branch; run `go test ./agent -run '^TestDelegateResourceStop_ForegroundShellTimeoutAbortsUncommittedReceipt$' -count=1`.
7. Append to=caller directly into the root's unfinished tool round; run `go test ./agent -run '^TestDelegateResourceRuntime_CallerCannotWriteIntoUnfinishedRootToolRound$' -count=1`.
8. Treat raw JSON null as absent; run `go test ./agent -run '^TestDelegateResourceRuntime_StructuredResultExplicitNullIsPresent$' -count=1`.
9. Replace ReadEvents with writable Open in historical activity; run `go test ./agent -run '^TestStableDelegateReadOnly_FileBytesAndMetadataRemainUnchanged$' -count=1`.
10. Drop wait_ignored_reason from the delegate_send result or turn-slot diagnostics from the stable list projection; run `go test ./agent -run '^(TestStableDelegateTools_WaitIgnoredReasonIsOwnField|TestStableDelegateTools_ListPreservesTurnSlotsAllowanceAndWatchDiagnostics)$' -count=1`.

A selector that passes under its matching mutation is inadequate: restore first, strengthen only its owner task's behavioral assertion, record a new RED, and repeat the task gates before continuing.

- [ ] **Step 3: Run fuzz registry and replay**

~~~bash
make fuzz-registry-check
make fuzz
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^(FuzzDelegateControllerTransitions|FuzzDelegateConversationTransitions|FuzzDelegateRestartEquivalence|FuzzDelegateAttentionFold|FuzzStableDelegateWatchDelivery|FuzzDelegateReclaimStopRestart)$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent/internal/delegatestore -run '^Fuzz(Fold|StoreReplay|ReadEvents)$' -count=1
go test ./agent/internal/jobstore -run '^FuzzOutputMatcher$' -count=1
~~~

- [ ] **Step 4: Prove clean flag-day restart and fail-closed legacy input**

Run these exact tests from their Task 6/8/12 files:

~~~bash
go test ./agent -run '^(TestDelegateResourceBootstrap_RestartIsProviderFreeAndLazy|TestDelegateResourceBootstrap_LegacyDelegateStateFailsClosed|TestDelegateResourceBootstrap_LegacyDelegateWatchStateFailsClosed|TestDelegateAttention_RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult|TestStableDelegateWatch_RestartRepairsReceiverDurableSourceUnacked|TestDelegateResourceStop_RestartCompletesPendingStopProviderFree|TestDelegateLegacyDormancy_LegacyLifecycleAndWatchRowsFailClosed)$' -count=20
go test -race ./agent -run '^(TestDelegateResourceBootstrap_RestartIsProviderFreeAndLazy|TestDelegateAttention_RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult|TestStableDelegateWatch_RestartRepairsReceiverDurableSourceUnacked|TestDelegateResourceStop_RestartCompletesPendingStopProviderFree)$' -count=20
~~~

Use fresh temporary state directories. Assert no provider, Session, timer, hook, nudge, salvage, worktree, notification, or metadata mutation occurs during bootstrap/reconcile.

- [ ] **Step 5: Run source/AST inventories**

~~~bash
rg -n 'JobTypeDelegate|JobDelegate|DelegateJobID|CurrentJobID|LatestJobID|delegate failure record' agent --glob '*.go'
rg -n 'unsupported_delegate_watch|activation.*(status|output|history|watch|stop)' agent cmd internal appwire --glob '*.{go,ts,tsx,md}'
rg -n 'Detailed\.Jobs' cmd/evener-hub internal appwire --glob '*.{go,ts,tsx}'
rg -n 'time\.(NewTicker|AfterFunc)|go func' agent/delegate_*.go agent/session_attention.go
rg -n 'delegatestore\.Open|jobstore\.Open' agent/historical_jobs.go agent/jobs_activity_past.go agent/doctor cmd/evener-hub/app_threadread.go cmd/evener-hub/internal/hubcore/prober.go
rg -n 'queues/|queue.*attention|attention.*queue' agent/delegate_*.go agent/session_attention.go
~~~

Review every nonempty result against the explicit allowlists: legacy rejection fixtures, shell-only JobRecord behavior, existing watch journal, historical rendering, and root-owned reconcile driver. There must be no second lifecycle/watch log, automatic unload timer, activation fallback, mutable identity mirror, or append-capable cold read.

- [ ] **Step 6: Run full repository gates individually**

~~~bash
make lint
make build
make build-go
ROOT_FULL=1 make test
make test-dev-tooling
make test-fuzz
make test-race
make vet
make fuzz
make fuzz-registry-check
make fuzz-gap-check
make fuzz-corpus-scan
make test-web
make test-web-browser
git diff --check
git status --short
~~~

Capture each bare exit code, duration, and failure evidence root. Do not infer success from a partial transcript and do not combine gates through a pipeline. Missing browser prerequisites are reported as a limitation/failure, never a pass. The canonical merge-approval composition is make lint, make build, ROOT_FULL=1 make test, and make test-dev-tooling; test-fuzz and deterministic make fuzz remain separate mandatory evidence.

- [ ] **Step 7: Review the entire branch and recovery evidence**

Determine the exact range:

~~~bash
task5_base=2da9863390e3e064fc015afe79a54fe8a8ce1d8f
actual_merge_base=$(git merge-base "$task5_base" HEAD)
test "$actual_merge_base" = "$task5_base"
git log --oneline --decorate "$task5_base"..HEAD
git diff --stat "$task5_base"..HEAD
git diff --check "$task5_base"..HEAD
~~~

Use the asserted fixed Task 5 foundation through HEAD for every requested review; do not derive the implementation range from current `main`. Request sequential whole-range reviews for lifecycle/locking, watches/recovery, worktree/sandbox, tools/projection, clients, and documentation. Resolve every finding through its owning task and rerun the relevant causal selector plus full gates. Then obtain one final fresh review of the same fixed range. A separate comparison to current `main` may be recorded only as integration evidence and does not define or replace the review range.

Final acceptance requires:

- exactly one root controller/mutex/delegatestore lifecycle authority and one public dlg_ identity;
- shell-only public jobs and typed stable watch endpoints backed by the existing watch journal;
- all external I/O after controller unlock with exact process-only claims;
- preserved watches, observers, quiet supervision, hooks/nudge/salvage, reclamation, shells, worktrees, events, historical reads, and fidelity;
- only the approved activation/API/migration cuts;
- provider-free restart and fail-closed legacy state;
- no delegate JobRecord, lifecycle mirror, second queue/log, dual writer, close flight, stop epoch, or automatic unload;
- all gates and fresh reviews clean;
- no push.

Do not create an empty verification commit. Leave the branch clean and report the exact commit range, gate exit codes, fuzz replay results, review dispositions, and any intentionally retained allowlisted source matches.
