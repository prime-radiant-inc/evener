# Delegate Resource Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace delegate activation jobs with one root-owned stable delegate controller and one durable delegate aggregate, making `dlg_...` the only delegate control identity while deleting more lifecycle machinery than the replacement adds.

**Architecture:** Build and prove a dormant `delegatestore` plus synchronous one-mutex `delegateTreeController` while current `main` remains the only active production route. Then perform one flag-day vertical cutover that switches state format, Session ownership, runtime start/steer/settle/finish/stop/restart, registered tools, events, AppWire, TUI, web, doctor, and cold projection together. That first deployable build accepts only the new delegate store and stable contracts; it rejects legacy delegate-job state. Only after every consumer has moved do we delete already-unreachable delegate JobRecords and old lifecycle authorities; that deletion is not a second operational phase.

**Tech Stack:** Go, append-only JSONL with fsync, Serf Session/transcript/provider seams, deterministic channel barriers, Rapid/native fuzz replay, AppWire Go/TypeScript generation, React/Vitest/Biome, Kata, and repository Make gates.

## Global Constraints

- Jesse is the human partner. The authoritative target is `docs/subagent-management/11-delegate-resource-model.md`; this dated file is an execution plan, not a second product specification.
- Work only in `/Users/jesse/prime-radiant/toil-suite/serf/.worktrees/delegate-resource-recovery-design` on `wip/delegate-resource-recovery-design`, whose implementation ancestry is clean `main`. Never merge, rebase, cherry-pick, reset, clean, stash, switch branches, or push.
- Do not copy lifecycle code from `delegate-identity-integration`. Pure DTO/rendering ideas and still-valid behavioral tests must be re-derived against `main`.
- There is one stable public delegate identity (`dlg_...`), one root-owned controller, one durable delegate fold, one current private `uint64` generation per delegate, and one lifecycle mutex per root tree.
- A delegate generation is never a JobRecord, job ID, output file, independent reducer, query target, notification rail, or public row.
- Child Sessions inherit only the controller pointer and immutable owning delegate ID. Exact generation leases travel in the active run context, never in a mutable Session token, subagent manager, or job-manager mirror.
- Shell jobs remain durable `job_...` resources with output, watch, status, and stop. A shell launched by a delegate records `ParentDelegateID`; delegate lineage never uses `ParentJobID`.
- The controller mutex may cover validation, its own store append/fsync, and narrow controller-to-transcript admission. It may not cover provider calls, process start/cancel, hooks, notifications, event delivery, filesystem lane mutation, shell-store reads, or completion waits.
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
- Idle runtimes remain resident. Do not add automatic unload, close flights, epochs, wildcard generations, detached supervisors, lock sharding, or lifecycle mirrors.
- Delegate watch sources/receivers and `watch_parent` are removed; `dlg_...` watch use returns `unsupported_delegate_watch`. Do not add an activation fallback.
- This is a flag-day cutover. There is no migration, mixed loader, compatibility alias/window, dual write, fallback route, or feature flag. Any old delegate job bytes fail `legacy_delegate_state` even if a new store is also present; shell-only history is permitted; unknown delegate-store versions fail closed.
- Foundation commits may contain dormant directly tested code, but the old production route remains the only active route until the one vertical cutover. No commit may expose a half-switched public delegate path.
- Before any production change, capture desired public behavior on unchanged `main` using temporary compile-valid tests and existing seams. Record honest RED/GREEN evidence in Kata `my73`, then remove intentional failures before ordinary hooks run.
- Every final regression test exercises registered tools or real Session/provider/executor behavior. Compile errors, missing selectors, timeouts, queue-only inspection, and internal symbol assertions are not behavioral RED evidence.
- Use channels/barriers and scripted providers/executors, never sleeps or polling races. Fix any sighted flake immediately.
- Fault-inject each lifecycle event boundary: created, run-started, terminal-prepared, run-finished, resumability-closed, stop-requested, stop-completed, and delivery-acknowledged. Append failure cannot leak in-memory mutation or external launch.
- Use `apply_patch` for edits, `gofmt` for Go, `npx biome check --write` on touched frontend `src/` files, and `make generate` for AppWire outputs. Never hand-edit generated TypeScript or protocol Markdown.
- Run `git status --short` before explicit staging; never use `git add -A`, wildcard staging, or bypass a hook. Commit each dormant foundation task and each later coherent cut with a detailed intent/evidence body.
- Stop for architectural review if correctness appears to require delegate JobRecords, a second fold, a Session/job-manager lifecycle mirror, an ancestor epoch vector, wildcard/zero-generation matching, reopen during stop, controller lock across external work, auto-unload, delegate watches, compatibility, supervisor goroutine, or caller-specific recovery exceptions.

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
- Projection: `agent/events/{events,payloads}.go`, `jobs_activity.go`, `jobs_activity_past.go`, `historical_jobs.go`, `internal/appprojector/appwire_projection.go`, `appwire/types.go`, `appwire/protocol.go`, `server/appwire_runtime.go`, `cmd/serf-hub/app_jobs.go`; plus the stable live/reconnect status bridge `agent/{status.go,status_test.go,status_support_program_fuzz_test.go}`, `cmd/serf/{serve.go,serve_test.go,serve_coverage_fuzz_test.go}`, and `server/{server.go,server_test.go,server_surface_fuzz_test.go}`.
- Clients: `cmd/serf-hub/internal/hubcore/{prober.go,prober_test.go,prober_wire_test.go,scenarios_fuzz_test.go}`; `cmd/serf-tui/internal/transcript/{job_notification.go,reducer.go,types.go}` plus their exact tests/fuzz files, `cmd/serf-tui/hub_notifications.go`, and `cmd/serf-tui/model_misc_serffuzz_test.go`; `cmd/serf/{run_drain_test.go,run_drain_nested_test.go}`; and the Hub frontend activity/transcript/store modules, notification parser/card tests, protocol fixture, and overflow harness named in Task 6.
- Operations: `agent/doctor`, `cmd/serf-doctor`, worktree/disposal helpers, bundled doctor references, prompts, and evergreen docs.

## Fixed internal shapes

Later tasks use these names. A task may add non-authoritative operational fields, but it may not add another lifecycle API.

```go
type delegateActor struct {
	rootSessionID string
	delegateID    string
	lease         delegateLease // required when delegateID is non-empty
}

type delegateLease struct {
	delegateID string
	generation uint64
}

type delegateStartToken struct{ id uint64 }
type delegateWorkToken struct{ id uint64 }
type delegateWaiterToken struct{ id uint64 }

type delegateTreeLimits struct {
	maxTurns int
	maxDrives int
}

type delegateVisibility uint8

const (
	delegateDirect delegateVisibility = iota + 1
	delegateDescendants
)

type delegateStartReservation struct {
	token      delegateStartToken
	delegateID string
	create     bool
	trigger    delegatestore.RunTrigger
	cancel     context.CancelCauseFunc
	drive      bool
	waiter     *delegateInlineWaiter
}

type delegateRuntime struct {
	delegateID string
	session    *Session
	owner      *Session
	done       <-chan struct{}
}

type delegateShellWork struct {
	token      delegateWorkToken
	owner      delegateLease
	jobID      string
	cancel     context.CancelCauseFunc
	committed  bool
}

type delegateSteeringAdmission struct {
	entryID string
}

type delegateInlineWaiter struct {
	token      delegateWaiterToken
	generation uint64
	resolution chan delegateInlineResolution // buffered one; claim always resolves
}

type delegateInlineResolution struct {
    packet    *delegatestore.TerminalPacket
    commit    *delegateToolResultCommit // process-only handoff to caller tool-result persistence
    fallback  bool
}

type delegateToolResultCommit struct {
    controller *delegateTreeController
    token      delegateDeliveryToken
    deliveryID string
}

type delegateAttentionResolution string

const (
    delegateAttentionConsumed  delegateAttentionResolution = "consumed"
    delegateAttentionDiscarded delegateAttentionResolution = "discarded"
)

type delegateAttentionEntry struct {
    attentionID string
    turn        schema.Turn // model-bound steering turn carrying the private ID
}

type shellNotificationIdentity struct {
	jobID             string
	terminalGeneration string
}

type shellRuntimeLossEvidence struct {
	runningJobIDs       []string
	pendingNotification []shellNotificationIdentity
}

type delegateLiveState struct {
	runtime          *delegateRuntime
	lease            delegateLease
	cancel           context.CancelCauseFunc
	ready            bool
	pendingSteers    []delegateSteeringAdmission
	attentionIDs     []string // exact IDs bound to the current attention drive; transcript remains authority
	waiters           map[uint64]*delegateInlineWaiter
	recoveryRequired bool
	latestAt         time.Time // transcript-derived monotonic hint, outside the state revision gate
}

type delegateTreeController struct {
	mu          sync.Mutex
	rootID      string
	store       *delegatestore.Store
	durable     delegatestore.State
	live        map[string]*delegateLiveState
	starts      map[uint64]*delegateStartReservation
	work        map[uint64]*delegateShellWork
	deliveries  map[uint64]*delegateDeliveryAdmission
	stop        *delegateStopState
	nextProcess uint64 // reservations, receipts, waiters only; never persisted
	turnsInUse  int
	drivesInUse int
	evidenceVersion uint64 // process-only optimistic validation for external evidence
	closing     bool
}

type delegateUpdatePlan struct {
	snapshot delegateSnapshot // captured under controller lock
}

type delegateMutationPlans struct {
	updates      []delegateUpdatePlan
	deliveries   []delegateDeliveryPlan
	attention    []delegateAttentionCleanupPlan
	shellRepairs []delegateShellRepairPlan
}

type delegateSnapshot struct {
	id            string
	revision      uint64 // per-delegate durable projection revision, never a control ID
	parentID      string
	lifecycle     string
	phase         string
	resumable     bool
	closedReason  string
	transcriptRef string
	lastOutcome   *delegatestore.Outcome
	latestAt      time.Time // merge by max independently of revision
}

type delegateFinish struct {
	outcome     delegatestore.OutcomeStatus
	disposition delegatestore.RunDisposition
	reason      string
	packet      *delegatestore.TerminalPacket
	endedAt     time.Time
}

type delegateDeliveryPlan struct {
	delegateID      string
	deliveryID      string
	ownerDelegateID string
	waiter          *delegateInlineWaiter // ownership already removed from live.waiters under c.mu
	packet          delegatestore.TerminalPacket
}

type delegateDeliveryToken struct {
	processID  uint64
	deliveryID string
}

type delegateDeliveryAdmission struct {
	token      delegateDeliveryToken
	delegateID string
	ownerID    string
}

type delegateAttentionCleanupPlan struct {
    requestSeq   uint64
    delegateID   string
    transcriptRef string
    attentionID  string
    disposition  delegateAttentionResolution
    runtime      *delegateRuntime // exact live identity; nil only during cold reconcile
}

type delegateShellRepairPlan struct {
	delegateID          string
	storePath           string
	runningJobIDs       []string
	pendingNotification []shellNotificationIdentity
	suppressOwnerNotify bool
}

type delegateStopState struct {
	requestSeq uint64
	targetID   string
	members    map[string]struct{}
	active     map[delegateLease]struct{}
	starts     map[delegateStartToken]struct{}
	work       map[delegateWorkToken]string
	deliveries map[delegateDeliveryToken]struct{}
	done       chan struct{}
}

type delegateStopResult struct {
	id                string
	previousLifecycle string
	lifecycle         string
	outcome           string
	requestSeq        uint64
	done              <-chan struct{}
}

type delegateCancelPlan struct {
	requestSeq uint64
	targetID   string
	cancel     []context.CancelCauseFunc
	children   []*delegateRuntime
	shells     []delegateShellWork
}

type delegateReconcileRequirements struct {
	evidenceVersion       uint64
	shellStores          map[string]string // delegate ID to shell-store path
	attentionTranscripts map[string]string // delegate ID to durable transcript reference
}

type delegateReconcileEvidence struct {
	evidenceVersion uint64
	shells         map[string]shellRuntimeLossEvidence
	attention      map[string][]string // exact pending IDs from read-only receiver-transcript folds
}
```

Durable identity is restart-safe:

```go
func delegateDeliveryID(delegateID string, generation uint64) string {
	return delegateID + "/delivery/" + strconv.FormatUint(generation, 10)
}

// A pending stop is identified by its delegate_subtree_stop_requested event
// sequence. Completion carries RequestSeq; no process counter is persisted.
```

Exact leases live in the active run context:

```go
func withDelegateLease(ctx context.Context, lease delegateLease) context.Context
func delegateLeaseFromContext(ctx context.Context) (delegateLease, bool)
```

The existing transcript turn owns the two private durable correlation shapes;
neither is projected into provider messages, public events, Markdown, AppWire,
TUI, or web:

```go
type DelegateDeliveryCommit struct {
    ToolCallID string `json:"tool_call_id"`
    DeliveryID string `json:"delivery_id"`
}

type AttentionResolutionInfo struct {
    AttentionID string `json:"attention_id"`
    Disposition string `json:"disposition"` // consumed | discarded
}

// On schema.Turn:
AttentionID            string                    `json:"attention_id,omitempty"`
DelegateDeliveryCommits []DelegateDeliveryCommit `json:"delegate_delivery_commits,omitempty"`
AttentionResolution    *AttentionResolutionInfo  `json:"attention_resolution,omitempty"`
```

`AttentionID` is valid only on a model-bound `TurnSteering` entry.
`AttentionResolution` is valid only on the new presentational,
provider-excluded `TurnAttentionResolution` kind. A pure fold treats the first
pending turn for an ID as authoritative and a later consumed/discarded marker
as monotonic resolution. An exact duplicate append/resolution is a no-op; an ID
reused with different content or disposition is corruption.

The concrete transcript helpers are:

```go
func delegateAttentionID(deliveryID string) string
func shellAttentionID(jobID, terminalGeneration string) string
func readPendingAttention(path string) ([]delegateAttentionEntry, error) // missing file => nil; read-only
func (s *Session) appendAttentionDurably(entry delegateAttentionEntry) (alreadyPresent bool, error)
func (s *Session) resolveAttentionDurably(ids []string, disposition delegateAttentionResolution) error
func appendColdAttentionResolution(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution) error
```

The live helpers use the already-open Session transcript writer; the cold stop
helper opens the exact existing transcript for append only after read-only
fold/identity validation and never constructs a Session/provider. None touches
`queues/*.json` or creates a second journal.

Controller operations return immutable post-mutation plans where public state changes:

```go
func openDelegateTreeController(rootID, stateDir string, limits delegateTreeLimits) (*delegateTreeController, error)
func (c *delegateTreeController) ReserveCreate(actor delegateActor) (string, delegateStartToken, error)
func (c *delegateTreeController) ReserveStart(actor delegateActor, id string, trigger delegatestore.RunTrigger) (delegateStartToken, error)
func (c *delegateTreeController) ReserveAttention(runtime *delegateRuntime, attentionID string) (delegateStartToken, error)
func (c *delegateTreeController) CommitStart(token delegateStartToken, descriptor *delegatestore.Descriptor) (delegateLease, delegateMutationPlans, error)
func (c *delegateTreeController) AttachRuntime(lease delegateLease, runtime *delegateRuntime) error
func (c *delegateTreeController) AdmitStartInput(lease delegateLease, admitInput func() error) (delegateMutationPlans, error)
func (c *delegateTreeController) AbortStart(token delegateStartToken)
func (c *delegateTreeController) Steer(ctx context.Context, actor delegateActor, id, message string) (delegateMutationPlans, error)
func (c *delegateTreeController) SteerCaller(ctx context.Context, actor delegateActor, message string) (delegateMutationPlans, error)
func (c *delegateTreeController) BeginModelRequest(lease delegateLease, snapshot func() []llm.Message) ([]llm.Message, error)
func (c *delegateTreeController) BeginTool(lease delegateLease) error
func (c *delegateTreeController) BeginSettlement(lease delegateLease, packet *delegatestore.TerminalPacket) (continueRun bool, plans delegateMutationPlans, err error)
func (c *delegateTreeController) FinishGeneration(lease delegateLease, finish delegateFinish) (delegateMutationPlans, error)
func (c *delegateTreeController) BeginShellWork(owner delegateLease) (delegateWorkToken, error)
func (c *delegateTreeController) CommitShellWork(token delegateWorkToken, shellJobID string, cancel context.CancelCauseFunc) (cancelImmediately bool, error)
func (c *delegateTreeController) AbortShellWork(token delegateWorkToken)
func (c *delegateTreeController) ReportShellFinished(token delegateWorkToken, shellJobID string) (delegateMutationPlans, error)
func (c *delegateTreeController) StopSubtree(actor delegateActor, targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error)
func (c *delegateTreeController) ReportAttentionResolved(requestSeq uint64, delegateID, attentionID string, disposition delegateAttentionResolution, runtime *delegateRuntime) (delegateMutationPlans, error)
func (c *delegateTreeController) BeginDelivery(plan delegateDeliveryPlan) (delegateDeliveryToken, bool, error)
func (c *delegateTreeController) CompleteDelivery(token delegateDeliveryToken, committed bool) (delegateMutationPlans, error)
func (c *delegateTreeController) CloseResumability(actor delegateActor, delegateID, reason string) (delegateMutationPlans, error)
func (c *delegateTreeController) Snapshot(actor delegateActor, visibility delegateVisibility) []delegateSnapshot
func (c *delegateTreeController) Reconcile(evidence delegateReconcileEvidence) (delegateMutationPlans, error)
func (c *delegateTreeController) Close(ctx context.Context) error
```

Only `admitInput`, model-history `snapshot`, and concrete runtime steering admission may acquire transcript/history locks under `c.mu`. They must not call the controller. Owner-delivery insertion, attention append/fold/consume/discard, and caller tool-result persistence use process-local receipts/handoffs and happen after unlock. `ReserveAttention` receives an exact runtime plus a read-only pending-ID proof captured from that runtime immediately before the call; it does no transcript I/O while locked. Every other external action consumes returned plans after unlock.

The durable store API is concrete:

```go
const CurrentVersion = 1

type State map[string]*Aggregate

func Open(path string) (*Store, error)
func ReadEvents(path string) ([]Event, error)
func (s *Store) Load() ([]Event, error)
func (s *Store) Append(state State, event Event) (Event, State, error)
func (s *Store) AppendBatch(state State, events []Event) ([]Event, State, error)
func (s *Store) Close() error
func Fold(events []Event) (State, error)
func Apply(state State, event Event) error
```

The first JSONL line is a version header. Each later line is one `batchRecord{Events []Event}`; events have contiguous sequence values. Required event kinds are exactly `delegate_created`, `delegate_run_started`, `delegate_terminal_prepared`, `delegate_run_finished`, `delegate_resumability_closed`, `delegate_subtree_stop_requested`, `delegate_subtree_stop_completed`, and `delegate_delivery_acknowledged`.

`Aggregate.PendingDeliveries` is an ordered slice keyed uniquely by deterministic delivery ID. `Aggregate.PendingStopSeq` is the one request sequence covering that member. `RunFinished.Disposition` may be `completed_no_action`, but `Outcome.Status` is only `completed|failed|exhausted|cancelled|stopped`.

```go
type RunDisposition string

const (
	DispositionReported          RunDisposition = "reported"
	DispositionTerminalError     RunDisposition = "terminal_error"
	DispositionCompletedNoAction RunDisposition = "completed_no_action"
)

type OutcomeStatus string

const (
	OutcomeCompleted OutcomeStatus = "completed"
	OutcomeFailed    OutcomeStatus = "failed"
	OutcomeExhausted OutcomeStatus = "exhausted"
	OutcomeCancelled OutcomeStatus = "cancelled"
	OutcomeStopped   OutcomeStatus = "stopped"
)
```

---

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

### Task 6: Perform the one vertical production cutover

**Files — root/runtime/tool path:**
- Create: `agent/delegate_runtime.go`
- Create: `agent/delegate_legacy_state.go`
- Create: `agent/session_attention.go`, `session_attention_test.go`, `session_attention_fuzz_test.go`
- Create: `agent/delegate_resource_contract_test.go`
- Create: `agent/delegate_resource_race_test.go`
- Create: `agent/delegate_legacy_state_test.go`
- Modify: `agent/schema/turn.go`, `agent/session.go`, `session_config.go`, `session_init.go`, `session_lifecycle.go`, `session_lifecycle_test.go`, `session_model_call.go`, `session_model_call_phase8_test.go`, `session_queue.go`, `session_queue_persist.go`, `session_queue_persist_test.go`, `session_tools.go`, `session_tool_registry.go`, `session_tool_round.go`, `session_tool_round_test.go`, `session_assistant_persistence_test.go`, `session_tools_communicate.go`, `transcript_read.go`, `history_repair.go`, `session_orphaned_tool_repair_test.go`, `tree_counter.go`
- Modify: `agent/internal/contextmgr/context_manager.go`, `context_manager_test.go`, `compaction_seqfuzz_test.go`, `maybecompact_fc1_seqfuzz_test.go`
- Modify: `agent/delegate_delivery.go`, `delegate_delivery_test.go`, `agent/job_delegate.go`, `subagents.go`, `job_shell.go`, `jobs.go`, `jobs_nested.go`, `job_notify.go`, `shell_notify_digest_program_fuzz_test.go`, `job_watch.go`, `session_jobtree_drain.go`; `cmd/serf/run_drain_test.go`, `run_drain_nested_test.go`
- Modify: `agent/session_tools_worktree_dispose.go`, `session_tools_worktree.go`, `session_worktree_close.go`, `session_worktree_relock.go`, `session_worktree_resume.go`, `session_worktree_sweep.go`
- Modify: `agent/sandbox_delegate.go`, `sandbox_delegate_test.go`, `sandbox_delegate_program_fuzz_test.go` to use the descriptor sandbox type in `delegatestore`, before Task 7 removes the jobstore duplicate
- Modify: `agent/internal/jobstore/event.go`, `record.go`, `fold.go` only to add shell `ParentDelegateID`; old delegate types remain until Task 7
- Modify: `agent/internal/tool/definitions.go`, `definitions_test.go`, `definitions_program_fuzz_test.go`
- Modify: `agent/session_tools_jobs.go`, `session_tools_jobs_test.go`, `session_tools_jobs_list_test.go`, `session_tools_jobs_stop_delegate_test.go`, `session_tools_jobs_watch_test.go`, `delegate_schema_test.go`, `registry_schemafuzz_test.go`, `session_outline.go`, `job_transcript_read.go`, `session_tools_transcript.go`
- Modify: `agent/transcript_render.go`, `transcript_render_test.go`, `transcript_render_job_test.go`, `transcript_render_fuzz_test.go`, `transcript_render_lookup_exact_fuzz_test.go`
- Modify: `agent/internal/atif/atif.go`, `atif_test.go` so trajectory export preserves the presentational marker without either private correlation field
- Rewrite active-route expectations in: `agent/job_delegate_create_test.go`, `job_delegate_send_test.go`, `job_delegate_send_fifo_test.go`, `job_delegate_finalize_test.go`, `job_delegate_drivedown_test.go`, `job_delegate_budget_test.go`, `job_nested_test.go`, `subagents_test.go`, `session_tools_jobs_lifecycle_fuzz_test.go`, `nested_subagent_lifecycle_program_fuzz_test.go`

**Files — events/projection/AppWire/clients/doctor:**
- Modify: `agent/events/events.go`, `agent/events/payloads.go`, `agent/events/eventdata.go`, their exhaustive event tests/fuzz programs, `agent/jobs_activity.go`, `agent/jobs_activity_past.go`, `agent/historical_jobs.go`
- Modify: `agent/status.go`, `status_test.go`, `status_support_program_fuzz_test.go`; `cmd/serf/serve.go`, `serve_test.go`, `serve_coverage_fuzz_test.go`; `server/server.go`, `server_test.go`, `server/server_surface_fuzz_test.go`
- Modify: `internal/appprojector/appwire_projection.go`, `appwire/types.go`, `appwire/protocol.go`, `server/appwire_runtime.go`, `cmd/serf-hub/app_jobs.go`
- Modify: `server/thread_envelope.go`, `thread_envelope_test.go` so the new delegate update refreshes its affected diagnostics facet
- Generate: `cmd/serf-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`
- Modify: `cmd/serf-hub/internal/hubcore/prober.go`, `prober_test.go`, `prober_wire_test.go`, `scenarios_fuzz_test.go`
- Modify: `cmd/serf-tui/hub_notifications.go`, `hub_notifications_test.go`, `hub_notifications_fuzz_test.go`, `model_misc_serffuzz_test.go`, `cmd/serf-tui/internal/transcript/job_notification.go`, `reducer.go`, `types.go`, `reducer_test.go`, `cov_rtui_transcript_test.go`, `reducer_fuzz_test.go`, `fuzz_coverage_union_test.go`, `cmd/serf-tui/internal/msgrender/tool_bodies.go`, `tool_renderers.go`, `cmd/serf-tui/internal/toolsummary/tool_summary.go`, plus adjacent render/summary tests including `fuzz_coverage_union_test.go`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts`, `activityRows.ts`, `ActivityTree.tsx`, `ActivityRowDetail.tsx`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`, `threads.test.ts`, `protocol/model.ts`, `reducer.ts`, `reducer.test.ts`
- Modify: `cmd/serf-hub/app_threadread.go`, `app_threadread_test.go` to remove activation-ID extraction from cold transcripts
- Modify: `cmd/serf-hub/frontend/src/stores/activityPanel.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`, `subagentModule.tsx`, `subagentModuleStore.ts`, `cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx`, plus their existing adjacent tests
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`, `steeringClassify.test.ts`, `NotificationCard.tsx`, `NotificationCard.test.tsx`, `SteeringItem.tsx`, `SteeringItem.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/protocol/fixtures/tool-and-jobs.jsonl`, `cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx`
- Modify: `agent/doctor/doctor.go`, `jobs.go`, `tree.go`, `sessions.go`, `watches.go`, their existing tests/fuzz fixtures including `sessions_test.go` and `watches_receiver_test.go`, `cmd/serf-doctor/main.go`, `main_test.go`, `README.md`

**Interfaces:**
- Consumes: every dormant Task 2–5 interface.
- Produces: the only active delegate route, stable registered schemas, one stable public projection, provider-free startup, resident idle runtimes, shell-only job ownership, and default-on causal regression tests. The old lifecycle code may still compile but is unreachable and receives no writes; Task 7 deletes it.

This task is intentionally one commit. Its substeps may leave the working tree red while wiring, but no intermediate commit, feature flag, dual write, or half-switched registered route is allowed.

- [ ] **Step 1: Recreate final default-on causal tests and capture the old-route RED**

Implement the Task 1 product tests as final tests without the `PhaseZero` suffix, plus the missing durability/restart cases:

```go
func TestDelegateResource_CreateReturnsOnlyStableIdentity(t *testing.T)
func TestDelegateResource_RunningSendReachesNextProviderRequestExactlyOnce(t *testing.T)
func TestDelegateResource_RunningSendDoesNotStartSuccessor(t *testing.T)
func TestDelegateResource_IdleSendStartsOneSuccessor(t *testing.T)
func TestDelegateResource_ConcurrentIdleSendsNeverStartTwoRuns(t *testing.T)
func TestDelegateResource_SteerBeforeNormalSettlementContinues(t *testing.T)
func TestDelegateResource_SteerBeforeCommunicateSettlementContinues(t *testing.T)
func TestDelegateResource_StaleFinalizerCannotAffectSuccessor(t *testing.T)
func TestDelegateResource_RegisteredStopCancelsWholeSubtree(t *testing.T)
func TestDelegateResource_StopRacingIdleStartHasOneOrder(t *testing.T)
func TestDelegateResource_StopRacingSteerNeverLosesAcceptedMessage(t *testing.T)
func TestDelegateResource_ChildReservationCannotEscapeAncestorStop(t *testing.T)
func TestDelegateResource_ShellReceiptCannotEscapeAncestorStop(t *testing.T)
func TestDelegateResource_StopSuppressesCoveredOwnerDelivery(t *testing.T)
func TestDelegateResource_StopDefersExternalOwnerDeliveryUntilCompletion(t *testing.T)
func TestDelegateResource_IdleStopFencesQueuedCoveredDelivery(t *testing.T)
func TestDelegateResource_DeliveryReceiptRacingStopHasOneOrder(t *testing.T)
func TestDelegateResource_RestartThreeLevelTreeIsProviderFreeAndLazy(t *testing.T)
func TestDelegateResource_RestartRepairsDescendantShellBeforeStopCompletion(t *testing.T)
func TestDelegateResource_RestartAfterStoppedFinishDoesNotFinishTwice(t *testing.T)
func TestDelegateResource_RestartReplaysTwoDeliveriesExactlyOnce(t *testing.T)
func TestDelegateResource_LiveDeliveryNPlusOneWaitsForNAcknowledgement(t *testing.T)
func TestDelegateResource_BlockedFirstDeliveryPreservesSecondInlineWaiter(t *testing.T)
func TestDelegateResource_InlineToolResultAppendFailureLeavesNThenNPlusOneQueued(t *testing.T)
func TestDelegateResource_InlineToolResultFSyncBeforeAckReplaysWithoutDuplicate(t *testing.T)
func TestDelegateResource_CrashAfterNormalSettlementFinishesPreparedOnce(t *testing.T)
func TestDelegateResource_CrashAfterCreateCommitLeavesNoUnownedState(t *testing.T)
func TestDelegateResource_PostCommitCreateFailureReturnsStableResult(t *testing.T)
func TestDelegateResource_NewStorePlusLegacyJobBytesFailsClosed(t *testing.T)
func TestDelegateResource_CreateInstallsNoParentWatch(t *testing.T)
func TestDelegateResource_WatchCallbackCannotEnterDelegate(t *testing.T)
func TestDelegateResource_RestoredWatchCallbackCannotEnterDelegate(t *testing.T)
func TestDelegateResource_ReversedUpdateEmissionCannotRegressClients(t *testing.T)
func TestDelegateResource_NewerActivityAtMergesAcrossEqualOrStaleStateRevision(t *testing.T)
func TestDelegateResource_CompletedNoActionIsPublicCompleted(t *testing.T)
func TestDelegateResource_TranscriptRendererKeepsDelegateAndShellIdentitySeparate(t *testing.T)
func TestDelegateResource_DelegateAndShellNotificationsUseDistinctMarkup(t *testing.T)
```

Before switching production behavior, run the selectors against the still-active old route and record assertion REDs in Kata `my73`. Test-only barriers may be added to `sessionConfig.testOnly` first, but must not alter non-test behavior. Every concurrency test enters through registered tools and asserts provider/process/transcript/public results.

- [ ] **Step 2: Open one controller at the root before side effects**

Add `Session.delegateController *delegateTreeController` and `Session.owningDelegateID string`; no lease field. A root always checks legacy bytes before accepting the new store, opens/folds the one store, snapshots reconciliation requirements, collects shell and attention evidence outside the controller lock, reconciles, and only then registers tools/emits startup. A child inherits the pointer/owning ID and never opens a delegate store.

`delegate_legacy_state.go` reads only `{kind,type}` from root `jobs.jsonl` and rejects old delegate start/create/stop-gate/disposed bytes. It cannot fold, translate, or project them. Shell-only history opens normally; unknown delegate version fails closed.

- [ ] **Step 3: Make exact leases run-context values**

`delegateRuntime.launch(lease,input)` derives `withDelegateLease(ctx,lease)` for the entire provider/tool/finalizer goroutine. `Session.delegateActor(ctx)` requires the exact context lease for a delegate-originated command. Root commands use a root actor. A retained idle Session stores no current generation.

- [ ] **Step 4: Cut creation and idle/attention start to controller reservations**

Refactor current child policy/profile/sandbox/worktree/transcript construction into:

```go
func (s *Session) constructDelegateRuntime(ctx context.Context, id string, descriptor *delegatestore.Descriptor) (*delegateRuntime, delegatestore.Descriptor, error)
func (s *Session) admitDelegateInputDurably(ctx context.Context, text string, p *provenance.Causal) (durableDelegateInput, error)
func (s *Session) processAdmittedDelegateInput(ctx context.Context, input durableDelegateInput) (string, bool, error)
```

Construction performs no provider call and publishes to no subagent/job manager. Before any child filesystem write, create derives deterministic locations and commits the atomic create/start batch plus exact non-launched cancellation binding; only then does it construct artifacts owned by that aggregate, attach the runtime, and durably admit input. Idle send likewise commits run-start before restore/reuse. A post-commit failure is a structured stable-ID/failed-outcome tool result, never a transport error that drops the ID; a pre-commit crash leaves no child artifact, and a post-commit crash reconciles the owned partial state as failed/runtime_lost. Attention uses exact resident runtime+durable entry identity. Normal finish retains idle runtime and worktree occupancy.

- [ ] **Step 5: Cut running steering and caller routing**

Resolve `dlg_...` directly in controller state. Append the steering transcript entry under controller-to-transcript order, return `action=steered` immediately, ignore waiting for running steer, and bind it exactly once before the next provider request. `to=caller` calls `SteerCaller`: stable parent runtime when nested, root durable steering admission when parent is root. Never look up an activation JobRecord.

- [ ] **Step 6: Cut model, tool, communicate, and finish boundaries**

Before request construction, derive context lease and call `BeginModelRequest`; refactor `prepareModelRequestFromHistory` to consume the captured history. At the start of each `execTool`, before pre-tool hooks, call `BeginTool`. In `session_tool_registry.go`, replace the Session-owned communicate terminal setters/watch callback with one dependency that invokes controller `BeginSettlement` and returns its continue/settle decision to the handler. Communicate validates/bounds the packet, continues if steer won, and prepares no terminal in that case. Ordinary completion with no communicate passes nil so `BeginSettlement` prepares the bounded missing-terminal packet before folding settling. Fatal/exhausted/cancelled paths use atomic prepare+finish. After tool-result persistence, exact finish records one outcome, returns immutable update/head-delivery plans, and never calls Session.Close.

The delegate handler uses existing `ctxToolCallID` to bind an inline packet's process-only `delegateToolResultCommit` to that exact caller tool call in a Session-owned pending map; do not add a field to generic `tool.ExecResult` or a cross-tool receipt framework. `persistToolResults` drains only commits for the round's call IDs and passes them to `appendToolResults`. When the set is non-empty, `appendToolResults` constructs one `TurnToolResults` whose persisted copy carries `DelegateDeliveryCommits`, forces the existing `appendTurnWithDurableTranscriptMessage`/`writeTranscriptDurable` path even if no shell-terminal result otherwise requires it, and does not expose the metadata in the live `llm.Message`. A commit-bearing tool-result turn requires the attached transcript writer; the pre-attachment held-turn path is not a durable success for this case. Only after that append returns from `transcript.Writer.AppendDurable` does it call each exact `CompleteDelivery(true)`. Append or fsync failure calls each `CompleteDelivery(false)`, returns the persistence error, and leaves the corresponding heads ordered and pending; cancellation before persistence takes the same false-completion path. If the receiver append committed but delivery-ack append fails or the process crashes, transcript replay finds the exact `ToolCallID+DeliveryID`, does not append a second tool-result turn, and retries only the acknowledgement.

Add deterministic barriers in `session_tool_round_test.go` and `session_assistant_persistence_test.go` around the existing writer seam. `TestSessionDelegateInlineToolResultDoesNotAckBeforeDurableAppend` blocks before fsync and proves N remains head; `TestSessionDelegateInlineToolResultAppendFailureLeavesNAndNPlusOneQueued` fails append and proves false completion plus honest persistence failure; `TestSessionDelegateInlineToolResultAckReleasesNPlusOneOnlyAfterNFSync` finishes N+1 while N is blocked and proves ordering; and `TestSessionDelegateInlineToolResultReplayAfterFsyncBeforeAckIsIdempotent` reopens the transcript and proves no duplicate tool result before N acknowledgement releases N+1. These are behavioral Session/controller tests, not rendered-JSON string matches.

- [ ] **Step 7: Cut inline/background owner delivery and repair**

Creating/idle-start calls may register one waiter keyed by the committed private generation. Timeout withdraws only that keyed waiter. Finish appends by deterministic delivery ID but dispatches only the ordered head; inline dispatch hands the caller Session a packet plus the exact process-only completion object and does not acknowledge at waiter resolution. The caller's aggregated tool-result boundary performs receiver commit as specified in Step 6; background dispatch performs the same ordering with a receiver-transcript attention append. Either receiver commit acknowledges that head and releases the next plan using the next entry's generation-keyed waiter even if a successor is current. Nested replay without a resident parent opens the exact transcript reference for durable idempotent append and uses existing orphaned-tool repair without provider construction. Remove delegate terminal JobRecord notification consumption from the active route.

- [ ] **Step 8: Cut descendant and shell admission plus stable stop**

Delegate construction uses its start reservation. In `runShell`, `BeginShellWork` precedes external start; after existing durable shell launch commit, `CommitShellWork(token,jobID,cancel)` decides publish versus immediate cancel. Stamp `ParentDelegateID` on shell record/event/fold. Registered `job_stop(target=dlg_)` calls controller stop, persists before cancellation, uses exact plan, waits only on the returned done channel, and never re-resolves a later generation.

- [ ] **Step 9: Cut root close, attention, restart, and disposal**

Root close sets controller closing, joins pending stop, whole-tree stops/drains, closes children postorder, then store. Refactor `jobManager.reconcileLostJobs` to share the same shell-only repair primitive with ordinary notification policy; pending delegate stop selects the suppression policy, so there is one jobstore terminal-repair implementation. Explicit isolation disposal first appends resumability closure; only then, after unlock, tears down runtime/worktree. Append failure destroys nothing.

`agent/session_attention.go` is the sole owner of durable attention operations against the existing receiver transcript. Delegate delivery derives `delegate:<deliveryID>`; shell terminal notification derives `shell:<jobID>:<terminalGeneration>`. Live check+append/resolve operations serialize on one narrow process-only `Session.attentionMu` that stores no entries; cold operations are already serialized by the exact delivery receipt or the root's single stop/reconcile loop. The transcript fold remains the only truth. `appendAttentionDurably` performs a missing-file-tolerant read-only fold inside that exclusion, no-ops if the same ID is already pending or resolved, otherwise appends and fsyncs one model-bound `TurnSteering` carrying private `AttentionID`. Only after that fsync may delegate delivery call `CompleteDelivery(true)` or shell notification mark its exact terminal generation delivered. Append failure leaves the delegate delivery or shell notification pending. A crash after receiver fsync but before source acknowledgement replays the source, observes the same pending ID, and acknowledges without a second model-bound turn.

The daemon steering queue and `<stateDir>/queues/*.json` snapshot remain best-effort transport/cache only for non-attention steering; `saveQueues`, `loadQueues`, and daemon-queue drain neither create nor resolve an `AttentionID`. `readPendingAttention` folds transcript turns only, treats a missing file as empty without creating it, and constructs no Session, provider, or queue store. The first pending turn for an ID wins; exact duplicate pending and resolution appends are no-ops; conflicting content or disposition fails closed. `resolveAttentionDurably` appends and fsyncs presentational `TurnAttentionResolution` with private ID and consumed/discarded disposition. `session_model_call.go` excludes that marker and all private attention/delivery metadata from provider history; `history_repair.go` treats it as a presentational interleaving that cannot interrupt a pending tool round; `transcript_render.go`, `internal/appprojector/appwire_projection.go`, and `internal/atif/atif.go` may preserve generic presentational content but never project `AttentionID` or `DelegateDeliveryCommits`.

Cut `agent/internal/contextmgr/context_manager.go` in the same atomic commit. Its cutoff scan treats `TurnAttentionResolution` inside a tool round like the existing structural interleavings: it walks through the marker to the assistant tool call instead of preserving a result-side suffix, and provider/presentation filtering of the marker cannot strand either half of the call/results pair. Update `context_manager_test.go` with `TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults`, covering cutoffs on the marker and the following `TurnToolResults`, marker removal, checkpoint, and summarization fallback. Update both `compaction_seqfuzz_test.go` and `maybecompact_fc1_seqfuzz_test.go` so their generated legal histories include resolution markers between assistant tool calls and results and their no-orphan/no-dangling invariants evaluate the marker-transparent projected sequence.

An attention drive first read-folds the exact receiver transcript, restores or selects the exact resident runtime, then calls `ReserveAttention(runtime, attentionID)`; cold discovery alone never starts a model. The drive binds the exact IDs it owns to its run. Normal and communicate settlement must fsync consumed markers for every bound ID before `FinishGeneration`; a marker append/fsync failure leaves the run open and the attention pending. Stop snapshots only transcript references under lock, then after unlock repeatedly cold-folds and fsyncs discarded markers until no covered ID remains; it must repeat after an apparently empty fold because cancellation may append another entry. Startup follows the same read-only fold and cleanup path without constructing Session/provider, and may drive an ID only after exact runtime restore/selection.

Add deterministic tests in `session_attention_test.go`, `session_queue_persist_test.go`, `session_tool_round_test.go`, `session_lifecycle_test.go`, `session_model_call_phase8_test.go`, `session_orphaned_tool_repair_test.go`, `transcript_render_test.go`, `agent/internal/atif/atif_test.go`, and `internal/appprojector/appwire_projection_test.go`:

```go
func TestDelegateAttentionAppendFSyncsBeforeSourceAck(t *testing.T)
func TestShellAttentionAppendFSyncsBeforeSourceAck(t *testing.T)
func TestAttentionConcurrentDuplicateAppendWritesOnce(t *testing.T)
func TestAttentionCrashAfterReceiverCommitBeforeSourceAckIsIdempotent(t *testing.T)
func TestAttentionConsumedMarkerPersistsBeforeGenerationFinish(t *testing.T)
func TestAttentionConsumedMarkerFailureKeepsGenerationOpen(t *testing.T)
func TestAttentionStopDiscardsAfterUnlockAndRefoldsUntilEmpty(t *testing.T)
func TestAttentionEvidenceSourceBeforeReceiverCannotMissHandoff(t *testing.T)
func TestAttentionMissingTranscriptReadDoesNotCreateFile(t *testing.T)
func TestAttentionColdRestartDoesNotConstructSessionOrProvider(t *testing.T)
func TestAttentionColdRestartDrivesOnlyAfterExactRuntimeRestore(t *testing.T)
func TestQueueSnapshotIsNotAttentionAuthority(t *testing.T)
func TestAttentionPrivateMetadataExcludedFromProviderRenderAndProjection(t *testing.T)
func TestAttentionResolutionMarkerDoesNotInterruptToolRoundRepair(t *testing.T)
func TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults(t *testing.T)
```

Use writer barriers/faults, exact source acknowledgements, provider-constructor counters, and a cancellation-generated second attention append; do not inspect only an in-memory queue. `FuzzDelegateAttentionJournal` in `session_attention_fuzz_test.go` generates duplicate append/consume/discard/crash-reopen sequences and asserts one pending model-bound turn per stable ID, monotonic resolution, provider exclusion, and missing-file read-only behavior. Register `native:agent:.:FuzzDelegateAttentionJournal::session_attention.go;schema/turn.go;transcript_read.go` in `scripts/run-fuzz.sh` and run it with `-tags serffuzz`.

- [ ] **Step 10: Atomically cut registered tool contracts**

Change definitions and handlers together:

```json
{"job_status":{"required":["target"]}}
{"job_stop":{"required":["target"],"optional":["include_children","max_wait_ms"]}}
{"job_list":{"result":{"items":"array"}}}
{"delegate_send":{"to":"dlg_... or caller"}}
```

Delegate results contain stable ID/type/lifecycle/phase/resumability/transcript/typed parent/last outcome and optional inline terminal packet; no activation/current/latest/resumed job fields. `job_status(dlg_)` is metadata-only and does not expose/ack delivery. Each delegate appears once in list. Delegate stop is always recursive regardless of `include_children`. `job:` transcript references are shell-only. `dlg_...` source/receiver watch returns `unsupported_delegate_watch`; remove `watch_parent` and `job_send_message` fallback from the active registry.

Cut `transcript_render.go` in the same commit: render stable delegate results through a delegate-specific shape with `delegate_id` and no activation aliases; retain `job_id` only for shell-job output/status; remove `job_send_message` from tool rendering and lookup classification. Extend normal and fuzz tests to prove a delegate tool call cannot render or accept `job_id`, `started_job_id`, `current_job_id`, `latest_job_id`, or `resumed_from_job_id`, while a shell job still renders its `job_id` exactly.

- [ ] **Step 11: Cut public events and live/cold activity to stable snapshots**

Add `DELEGATE_UPDATED` carrying the immutable `delegateUpdatePlan` snapshot, including per-delegate `projection_revision`, and project it to distinct `serf/delegate/updated`. Register its sealed payload marker and exhaustive event/fuzz cases in `agent/events/eventdata.go` and adjacent tests. Add it to `server/thread_envelope.go`'s freshness table as `facetDiagnostics`, with a producer test proving `DetailedStatus` resamples instead of retaining plausible stale delegate activity. `JOB_STARTED`/`JOB_FINISHED` become shell-only in active emission. Replace address-based `jobTreeClockByTreeCounter` sharing with an explicit inherited `*jobActivityClock`; that clock orders UI snapshots only and carries no generation, capacity, authorization, phase, or stop state. Replace delegate activation grouping/turn arrays with one `JobActivityDelegate` per stable ID, sourced live from controller snapshots and cold from `delegatestore.ReadEvents+Fold`. Live/cold projection carries the same folded state revision. `LatestAt` is explicitly outside that revision gate: live steering uses its durable transcript-entry timestamp, cold/refetch uses transcript metadata, and every merge takes max even for an equal/older state revision. Lifecycle/outcome never comes from transcript recency.

- [ ] **Step 12: Cut AppWire, TUI, and web in the same working tree**

Add `SerfDelegateInfo` with `projection_revision`, typed parent, stable outcome, and turn-free activity delegate. Keep `SerfJobInfo` shell-only. TUI gets `ApplySerfDelegate` keyed only by stable ID: equal/older revisions cannot change state fields but can increase `latest_activity_at`. Web activity uses the same two-part merge, modules key only by delegate ID, and tool renderers parse no activation job fields. Add reversed-emission tests in TUI reducer, web thread store, protocol reducer, and activity store: apply running revision N+1 before delayed idle N and prove every surface remains running; then give delayed N a newer activity timestamp and prove only ordering time advances.

Delete the fallback in `cmd/serf-hub/internal/hubcore/prober.go` that walks `Detailed.Jobs` and infers delegates from `job_type=delegate` or transcript refs. Prober discovery must use only `DescendantSessionIDs` and `DescendantStates`; malformed, empty, or shell-only `Detailed.Jobs` cannot add a running delegate. Pin this in `prober_test.go`, `prober_wire_test.go`, and `scenarios_fuzz_test.go`, including a descendant present only in the stable fields and a delegate-shaped legacy job present only in `Detailed.Jobs`.

Split terminal markup at both producer and client parser. `agent/job_notify.go` formats only shell `<job-notification job_id="job_..." job_type="shell">`; `agent/delegate_delivery.go` formats delegate `<delegate-notification delegate_id="dlg_...">` and never emits `job_id` or `job_type="delegate"`. Update `agent/shell_notify_digest_program_fuzz_test.go` so shell markup remains exact and add delegate format coverage in `delegate_delivery_test.go`.

In TUI, update `internal/transcript/job_notification.go` to parse the two tags into disjoint typed identities, `types.go` to carry `DelegateID` without a job alias, and `reducer.go` to apply delegate headlines/cards only by stable delegate ID. Rewrite `reducer_test.go`, `cov_rtui_transcript_test.go`, `reducer_fuzz_test.go`, and `fuzz_coverage_union_test.go`; update `hub_notifications.go`, `hub_notifications_test.go`, and `hub_notifications_fuzz_test.go` so the delegate notification method and payload never flow through `SerfJobInfo` or job-ID matching. Also rewrite `cmd/serf-tui/model_misc_serffuzz_test.go`: its `SubagentRunInfo` seed uses stable `DelegateID`, its notification seed uses `<delegate-notification delegate_id="dlg_...">`, and it asserts no delegate seed constructs `JobID` or `job_type="delegate"`. Keep `FuzzRootTUIModelMisc` registered in `scripts/run-fuzz.sh`. Fuzz invariants reject a delegate tag with job attributes and reject delegate semantics in a job tag, while accepting the shell tag unchanged.

Update `cmd/serf/run_drain_test.go` and `cmd/serf/run_drain_nested_test.go` with the producer cut. Their scripted provider branches recognize `<delegate-notification delegate_id="dlg_...">` for delegate completion at both root and nested levels, reject delegate completion carried by `<job-notification>`, and retain the existing exact `<job-notification job_id="job_..." job_type="shell">` assertions for shell drain. This proves one-shot drain still re-drives the complete delegate subtree after the identity/markup cutover instead of silently accepting the old test fixture.

In web, extend `messages/steeringClassify.ts` with a disjoint delegate notification variant keyed by `delegateId`; `NotificationCard.tsx` must title and label it `Delegate`, never `Job`, while shell cards remain jobs. Update `steeringClassify.test.ts`, `NotificationCard.test.tsx`, `SteeringItem.tsx`, and `SteeringItem.test.tsx` for single/multiple/malformed tags and raw-text fallback. Replace legacy delegate-job examples in `protocol/fixtures/tool-and-jobs.jsonl` and `dev/overflowharness-entry.tsx` with stable delegate markup so fixtures and the overflow harness exercise the new parser rather than preserving the removed contract.

Update these exact frontend activity/tool tests:

```text
src/panes/session/chrome/activityData.test.ts
src/panes/session/chrome/activityRows.test.ts
src/panes/session/chrome/ActivityTree.test.tsx
src/panes/session/chrome/ActivityRowDetail.test.tsx
src/stores/activityPanel.test.ts
src/stores/threads.test.ts
src/protocol/reducer.test.ts
src/panes/session/transcript/tools/jobTools.test.tsx
src/panes/session/transcript/tools/subagentModule.test.tsx
src/panes/session/transcript/tools/subagentModuleStore.test.ts
src/panes/session/transcript/ToolCallItem.test.tsx
src/panes/session/transcript/messages/steeringClassify.test.ts
src/panes/session/transcript/messages/NotificationCard.test.tsx
src/panes/session/transcript/messages/SteeringItem.test.tsx
```

Update TUI `tool_renderers.go` and `toolsummary/tool_summary.go` so `job_status` and `job_stop` display `target`, while shell output still displays `job_id`; remove `job_send_message`. Extend their unit and fuzz tests with both `dlg_...` and `job_...` targets. Add the delegate notification to `hub_notifications_fuzz_test.go`'s authoritative `notifyMethods` inventory and seed its stable payload shape.

Route `serf/delegate/updated` through both live ingress layers: `protocol/model.ts` keeps private per-delegate greatest state revision plus greatest activity timestamp, `protocol/reducer.ts` bumps refetch when either component advances, and `stores/threads.ts` applies lifecycle fields only for a newer revision while always max-merging activity time. Add causal tests that one newer notification changes the stable row and invalidates/refetches activity, an equal/older state snapshot cannot regress it, an equal/older snapshot with newer activity time advances only ordering/refetch, and a notification for another thread changes neither. `serf/job/started|finished` remains shell-only and must not update delegate rows.

Carry the same stable rows through the reconnect/refetch bridge. Add `Delegates` to `agent.DetailedStatus`, `server.DetailedStatus`, and `appwire.SerfDiagnostics`, with values shaped as `SerfDelegateInfo`; `agent/status.go` sources them from the controller snapshot, `cmd/serf/serve.go` maps every field without deriving from `Jobs`, and `server/server.go` serializes the stable slice. `server/appwire_runtime.go` maps that slice into diagnostics. Cold `cmd/serf-hub/app_threadread.go` projection and a reconnected daemon's DetailedStatus must therefore produce the same stable IDs, typed parents, lifecycle/outcome, projection revision, and independently max-merged activity timestamp. Add `TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen` in `agent/status_test.go`, `TestAgentToServerDetailedStatus_DelegatesLossless` in `cmd/serf/serve_test.go`, `TestStatusEndpoint_DetailedStatusIncludesStableDelegates` in `server/server_test.go`, `TestAppDiagnosticsFromDetailedStatus_DelegatesLossless` in `server/appwire_runtime_test.go`, and `TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus` in `cmd/serf-hub/app_threadread_test.go`. Register the new status cases in `FuzzStatusSupportProgram`, `FuzzServeSeedCoverage`, and the server surface fuzz table so tagged replay exercises the bridge rather than retaining delegate JobInfo fixtures.

- [ ] **Step 13: Cut doctor and cold Hub inspection to the read-only delegate fold**

Doctor locates root `delegates.jsonl`, reports shell jobs separately, and builds stable delegate parent graph from `ReadEvents+Fold`. `sessions.go` obtains delegate counts from that read-only fold rather than `jobstore.FoldDelegates`. Remove delegate receiver fields from watch reports. Cold Hub uses the same reader, removes `delegateJobIDFromRaw`/activation extraction in `app_threadread.go`, and constructs no Session. Add fixtures for depth-three tree, delegate-owned shell, pending stop, two pending deliveries, unknown version, legacy state, shell-only watches, and stable session delegate counts. Human/JSON output contains no private generation, activation job ID, or delegate watch receiver.

- [ ] **Step 14: Remove active old-route calls without deleting definitions yet**

Ensure registered create/send/finish/stop/list/status/events/restart/disposal paths call only the controller. Old `attachDelegateJob*`, current/latest lookup, subagent-manager lifecycle, delegate job finalizer, delegate job notification/watch code may remain as unreachable definitions solely so not-yet-ported legacy tests compile; no active caller or event write reaches them. Add a temporary source/call-graph assertion that every registered handler resolves to the new path and no active or restore route reaches `ReceiverDelegateID`, `receiverDelegateID`, `applyReceiverWatchSend`, `installParentSourceWatchForChild`, `clearParentSourceWatchForChild`, `attachDelegateJobFromWatch`, `FromWatch`, `runFromWatch`, `deliverWatchCallback`, `staleDelegateWatchSend`, or `delegateStoppedAfterWatchSendPending`. The three registered/restored callback tests above prove no callback input reaches a delegate transcript or provider.

The same cutover assertion scans the active Hub/TUI/web source and fixtures. It fails if hubcore prober reads `Detailed.Jobs` for delegate discovery; if any delegate notification contains `<job-notification`, `job_id`, or `job_type="delegate"`; if any TUI/web notification parser classifies a delegate by job type; or if a delegate card says `Job`. It also scans `cmd/serf/run_drain_test.go`, `cmd/serf/run_drain_nested_test.go`, and `cmd/serf-tui/model_misc_serffuzz_test.go` and rejects delegate JobID construction or delegate completion parsed from job markup. Shell markup is allowed only with `job_type="shell"`, and delegate markup must contain one `delegate_id="dlg_..."`.

Rewrite every existing integration/fuzz test that exercises an active registered delegate route to assert stable/controller behavior in this task. Tests that directly unit-test now-unreachable old helpers may remain until Task 7, but no ordinary package test may expect the registered route to create a delegate JobRecord or return activation IDs.

- [ ] **Step 15: Run formatting and generate AppWire outputs idempotently**

Run `gofmt` on every touched Go file. Run:

```bash
make generate
git status --short
```

Review generated changes, then explicitly stage `appwire/types.go`, `appwire/protocol.go`, `docs/appwire-protocol.md`, and `cmd/serf-hub/frontend/src/protocol/types.gen.ts` together with their projector source. Run `make generate` a second time, then:

```bash
git diff --exit-code -- docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
```

Expected: zero working-tree diff against the staged generated files; intentional staged diff versus HEAD remains.

- [ ] **Step 16: Format exact touched frontend files before gates**

From `cmd/serf-hub/frontend`, run:

```bash
npx biome check --write src/panes/session/chrome/activityData.ts src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/activityRows.ts src/panes/session/chrome/activityRows.test.ts src/panes/session/chrome/ActivityTree.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/chrome/ActivityRowDetail.tsx src/panes/session/chrome/ActivityRowDetail.test.tsx src/stores/activityPanel.ts src/stores/activityPanel.test.ts src/stores/threads.ts src/stores/threads.test.ts src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/subagentModule.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/tools/subagentModuleStore.ts src/panes/session/transcript/tools/subagentModuleStore.test.ts src/panes/session/transcript/ToolCallItem.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/messages/steeringClassify.ts src/panes/session/transcript/messages/steeringClassify.test.ts src/panes/session/transcript/messages/NotificationCard.tsx src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx src/dev/overflowharness-entry.tsx
```

Review any write and include it in the exact staging list.

- [ ] **Step 17: Make all causal public tests GREEN repeatedly**

Run:

```bash
go test ./agent -run '^TestDelegateResource_' -count=20 -timeout=20m
go test -race ./agent -run '^TestDelegateResource_' -count=20 -timeout=30m
go test ./agent/internal/contextmgr -run '^TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults$' -count=20
go test -race ./agent/internal/contextmgr -run '^TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults$' -count=20
go test ./agent -run '^Test(Session.*LegacyDelegate|JobTools|DelegateSchema|JobStop.*Delegate|JobWatch.*Delegate|ReadTranscript)' -count=20
go test ./agent -run '^Test(JobShell|NestedShell|SessionCommunicate|SessionClose|WorktreeDispose|RuntimeLost)' -count=1 -timeout=20m
go test ./agent -run '^TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen$' -count=20
go test ./cmd/serf -run '^(TestAgentToServerDetailedStatus_DelegatesLossless|TestRunDrainsDelegatedJobTreeBeforeExit|TestRunDrainsNestedDelegateSubtree)$' -count=20
go test ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test ./cmd/serf-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test ./agent -count=1 -timeout=30m
```

Expected: real steering appears exactly once in the next provider request, stop cancels depth-three delegate+shell+held start, restart invokes no provider, two deliveries replay once, compaction preserves a marker-interleaved tool round, live/reopened DetailedStatus projects the same stable delegates, both one-shot drain levels consume only stable delegate markup, and stable schemas contain no job identity.

- [ ] **Step 18: Run projection, doctor, TUI, and frontend gates**

Run:

```bash
go test ./agent/events ./agent/doctor ./agent/internal/atif ./cmd/serf-doctor ./internal/appprojector ./appwire ./server ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui/... -count=1
SERF_FUZZ_TESTS=1 go test ./agent/internal/contextmgr -run '^(TestCompactionSeqFuzz|TestFc1MaybeCompactSeqFuzz)$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzDelegateAttentionJournal$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent -run '^FuzzStatusSupportProgram$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./cmd/serf -run '^FuzzServeSeedCoverage$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./server -run '^FuzzAppTurnsFromNotifications$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./cmd/serf-tui -run '^FuzzRootTUIModelMisc$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent/events ./agent/doctor ./internal/appprojector ./appwire ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui ./cmd/serf-tui/internal/transcript -run '^(Test|Fuzz)' -count=1
make test-web
git diff --check
```

Expected: full-default-depth Rapid compaction sequences, tagged status/TUI replay, live/cold/doctor parity, transcript-backed attention replay, one stable client row, shell/delegate notification separation, generated drift check, Vitest/typecheck/Biome, and diff check pass. The Rapid command is the focused Task 6 equivalent of `make test-fuzz`; the native tagged commands are deterministic `make fuzz` replay surfaces, not substitutes for it.

- [ ] **Step 19: Stage the vertical cutover with exact paths and commit once**

Run `git status --short`, then these exact staging commands (omit an unchanged listed file; if implementation needs a file not listed here, add that exact path to this plan before staging):

```bash
git add -- agent/delegate_runtime.go agent/delegate_legacy_state.go agent/session_attention.go agent/session_attention_test.go agent/session_attention_fuzz_test.go agent/delegate_resource_contract_test.go agent/delegate_resource_race_test.go agent/delegate_legacy_state_test.go agent/schema/turn.go agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/session_lifecycle_test.go agent/session_model_call.go agent/session_model_call_phase8_test.go agent/session_queue.go agent/session_queue_persist.go agent/session_queue_persist_test.go agent/session_tools.go agent/session_tool_registry.go agent/session_tool_round.go agent/session_tool_round_test.go agent/session_assistant_persistence_test.go agent/session_tools_communicate.go agent/transcript_read.go agent/history_repair.go agent/session_orphaned_tool_repair_test.go agent/tree_counter.go
git add -- agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/context_manager_test.go agent/internal/contextmgr/compaction_seqfuzz_test.go agent/internal/contextmgr/maybecompact_fc1_seqfuzz_test.go
git add -- agent/sandbox_delegate.go agent/sandbox_delegate_test.go agent/sandbox_delegate_program_fuzz_test.go agent/sandbox_delegate_floor_test.go agent/sandbox_delegate_create_test.go
git add -- agent/delegate_delivery.go agent/delegate_delivery_test.go agent/job_delegate.go agent/subagents.go agent/job_shell.go agent/jobs.go agent/jobs_nested.go agent/job_notify.go agent/shell_notify_digest_program_fuzz_test.go agent/job_watch.go agent/session_jobtree_drain.go cmd/serf/run_drain_test.go cmd/serf/run_drain_nested_test.go agent/session_tools_worktree_dispose.go agent/session_tools_worktree.go agent/session_worktree_close.go agent/session_worktree_relock.go agent/session_worktree_resume.go agent/session_worktree_sweep.go agent/internal/jobstore/event.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go scripts/run-fuzz.sh
git add -- agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/internal/tool/definitions_program_fuzz_test.go agent/session_tools_jobs.go agent/session_tools_jobs_test.go agent/session_tools_jobs_list_test.go agent/session_tools_jobs_stop_delegate_test.go agent/session_tools_jobs_watch_test.go agent/delegate_schema_test.go agent/registry_schemafuzz_test.go agent/session_outline.go agent/job_transcript_read.go agent/session_tools_transcript.go agent/transcript_render.go agent/transcript_render_test.go agent/transcript_render_job_test.go agent/transcript_render_fuzz_test.go agent/transcript_render_lookup_exact_fuzz_test.go
git add -- agent/internal/atif/atif.go agent/internal/atif/atif_test.go
git add -- agent/job_delegate_create_test.go agent/job_delegate_send_test.go agent/job_delegate_send_fifo_test.go agent/job_delegate_finalize_test.go agent/job_delegate_drivedown_test.go agent/job_delegate_budget_test.go agent/job_nested_test.go agent/subagents_test.go agent/session_tools_jobs_lifecycle_fuzz_test.go agent/nested_subagent_lifecycle_program_fuzz_test.go
git add -- agent/events/events.go agent/events/payloads.go agent/events/eventdata.go agent/events/events_test.go agent/events/events_fuzz_test.go agent/events/eventdata_program_fuzz_test.go agent/events/payloads_test.go agent/jobs_activity.go agent/jobs_activity_past.go agent/historical_jobs.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go internal/appprojector/project_fuzz_test.go appwire/types.go appwire/protocol.go appwire/types_test.go appwire/protocol_test.go appwire/wiretypes_fuzz_test.go server/appwire_runtime.go server/appwire_runtime_test.go server/thread_envelope.go server/thread_envelope_test.go server/thread_envelope_test_helpers_test.go cmd/serf-hub/app_jobs.go cmd/serf-hub/app_jobs_test.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
git add -- agent/status.go agent/status_test.go agent/status_support_program_fuzz_test.go cmd/serf/serve.go cmd/serf/serve_test.go cmd/serf/serve_coverage_fuzz_test.go server/server.go server/server_test.go server/server_surface_fuzz_test.go
git add -- cmd/serf-hub/internal/hubcore/prober.go cmd/serf-hub/internal/hubcore/prober_test.go cmd/serf-hub/internal/hubcore/prober_wire_test.go cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go
git add -- cmd/serf-tui/hub_notifications.go cmd/serf-tui/hub_notifications_test.go cmd/serf-tui/hub_notifications_fuzz_test.go cmd/serf-tui/model_misc_serffuzz_test.go cmd/serf-tui/hub_appwire_test.go cmd/serf-tui/hub_notifications_catalog_types_test.go cmd/serf-tui/hub_partial_repaint_nxq6_test.go cmd/serf-tui/internal/transcript/job_notification.go cmd/serf-tui/internal/transcript/reducer.go cmd/serf-tui/internal/transcript/types.go cmd/serf-tui/internal/transcript/reducer_test.go cmd/serf-tui/internal/transcript/cov_rtui_transcript_test.go cmd/serf-tui/internal/transcript/reducer_fuzz_test.go cmd/serf-tui/internal/transcript/fuzz_coverage_union_test.go
git add -- cmd/serf-tui/internal/msgrender/tool_bodies.go cmd/serf-tui/internal/msgrender/tool_bodies_test.go cmd/serf-tui/internal/msgrender/tool_renderers.go cmd/serf-tui/internal/msgrender/tool_renderers_test.go cmd/serf-tui/internal/msgrender/tool_renderers_fuzz_test.go cmd/serf-tui/internal/toolsummary/tool_summary.go cmd/serf-tui/internal/toolsummary/tool_summary_test.go cmd/serf-tui/internal/toolsummary/tool_summary_fuzz_test.go cmd/serf-tui/internal/toolsummary/fuzz_coverage_test.go
git add -- cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.test.ts cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.test.tsx cmd/serf-hub/frontend/src/stores/activityPanel.ts cmd/serf-hub/frontend/src/stores/activityPanel.test.ts cmd/serf-hub/frontend/src/stores/threads.ts cmd/serf-hub/frontend/src/stores/threads.test.ts cmd/serf-hub/frontend/src/protocol/model.ts cmd/serf-hub/frontend/src/protocol/reducer.ts cmd/serf-hub/frontend/src/protocol/reducer.test.ts
git add -- cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.test.ts cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx
git add -- cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx cmd/serf-hub/frontend/src/protocol/fixtures/tool-and-jobs.jsonl cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx
git add -- agent/doctor/doctor.go agent/doctor/jobs.go agent/doctor/tree.go agent/doctor/sessions.go agent/doctor/watches.go agent/doctor/jobs_test.go agent/doctor/tree_test.go agent/doctor/sessions_test.go agent/doctor/watches_test.go agent/doctor/watches_receiver_test.go agent/doctor/filesystem_program_fuzz_test.go cmd/serf-doctor/main.go cmd/serf-doctor/main_test.go cmd/serf-doctor/README.md
```

Do not use `*`, directory-wide adds, `git add -u`, or `git add -A`. Re-run `git status --short` and inspect `git diff --cached --stat` plus representative diffs for controller wiring, schemas, generated files, and clients. Then commit:

```bash
git commit -m "refactor: make delegates stable resources" -m "Perform the single production cutover from delegate activation jobs to the root-owned controller. Creation, idle start, durable steering, model/tool admission, settlement, exact finish, ordered delivery, shell receipts, subtree stop, restart, disposal, registered tools, events, AppWire, TUI, web, doctor, and cold projection now use one stable dlg identity and one aggregate. The former implementation remains unreachable only until the next deletion commit; no dual write or compatibility route exists."
```

Expected: hooks pass. If a hook exposes an old-route dependency, fix the dependency and rerun; never disable the hook or commit a half-cut state.

---

### Task 7: Delete delegate jobs and every former lifecycle authority

**Files:**
- Modify: `agent/internal/jobstore/event.go`, `record.go`, `fold.go`, `store.go`
- Delete/rewrite: delegate-only tests under `agent/internal/jobstore`
- Modify: `agent/jobs.go`, `jobs_nested.go`, `job_notify.go`, `job_watch.go`, `job_delegate.go`, `subagents.go`
- Delete: `agent/subagent_manager.go` if no construction-only code remains; otherwise move the minimal construction helper into `agent/delegate_runtime.go` and delete the manager type
- Modify: `agent/session.go`, `session_config.go`, `session_init.go`, `session_lifecycle.go` to remove obsolete fields/callbacks/counters
- Modify: `identifier/domains.go`, `identifier/domains_test.go` to remove the delegate-generation generator/parser
- Delete/rewrite: old shape tests in `agent/job_delegate_create_test.go`, `job_delegate_send_test.go`, `job_delegate_send_fifo_test.go`, `job_delegate_finalize_test.go`, `job_delegate_drivedown_test.go`, `job_nested_test.go`, `subagents_test.go`
- Create: `agent/delegate_legacy_path_test.go`
- Modify: `scripts/run-fuzz.sh` to remove only manifest rows whose old delegate/watch target declarations are deleted

**Interfaces:**
- Consumes: the complete active vertical route from Task 6.
- Produces: shell/watch-only jobstore and job manager, no inactive alternate production path, no delegate generation strings, and measurable authority reduction.

- [ ] **Step 1: Establish the active route is green before deletion**

Run:

```bash
go test ./agent -run '^TestDelegateResource_' -count=20 -timeout=20m
go test ./agent -run '^Test(JobShell|JobTools|JobActivity|HistoricalJobs)' -count=1
git status --short
```

Expected: stable route passes and the tree is clean.

- [ ] **Step 2: Remove delegate durable schema from jobstore**

Delete `JobDelegate`, `DelegateStatus`, `DelegateRecord`, `DelegateRestoreDescriptor`, `SandboxSnapshot` duplicate, delegate event payload/kinds, `FoldDelegates`, `LoadDelegates`, current/latest IDs, string delegate generations, stop gates, delegate restore fields, delegate terminal structured-result fields, and delegate receiver/generation watch state. Keep shell/watch schemas and `ParentDelegateID` on shell job start/record.

Retain only `delegate_legacy_state.go`'s narrow raw-envelope scanner. It recognizes old bytes to fail closed but cannot load/fold/migrate/route/project them.

- [ ] **Step 3: Make `jobManager` and nested forwarding shell-only**

Remove delegate runtime pointers, tree slots, quiet watchdog state, delegate output/notification/forwarding, delegate job finalization, current/latest lookup, stop gates, delegate recursive stop, and delegate branches in `running`. Preserve shell process/output/stop/finalization/watch and any shell-only ancestor visibility; forwarded shell records use typed stable parent, never a delegate activation job.

- [ ] **Step 4: Remove subagent lifecycle and routing mirrors**

Delete `running`, `driving`, `finalizing`, `fatalRunGated`, `cancelRequested`, `disposeGated`, retained-terminal eviction, reconstruction coordination, manager routing map, result/status authority, and copied lifecycle callbacks. Child policy/profile/sandbox/worktree construction remains only in `delegate_runtime.go`; all runtime reachability is `controller.live[id]`.

- [ ] **Step 5: Delete activation attach/relink/finalize/control helpers**

Delete `findRunningDelegateByTranscriptRef`, `resumeOrFindRunningDelegate`, `relinkDelegateChildToJob`, every `attachDelegateJob*`, `finalizeDelegateOnce`, delegate output readers, delegate JobRecord notification methods, delegate job-ID stop/send paths, lifecycle token copies/mirrors, epoch/unload/close-flight names, and `dg_...` generation identifiers.

- [ ] **Step 6: Port behavior tests and delete implementation-shape tests**

Keep sandbox/model/worktree/budget/result/nested behavior assertions by routing them through registered stable tools and controller/store outcomes. Delete tests whose sole contract is delegate JobRecord bytes, output path, current/latest mapping, generation string, stop gate, subagent-manager retention, or old lock shape. Do not weaken Task 6 causal tests.

- [ ] **Step 7: Add an AST/source deletion inventory gate**

`delegate_legacy_path_test.go` scans non-test production Go files under `agent`, `internal`, `cmd`, `server`, and `appwire`, excluding only `delegate_legacy_state.go`'s raw string constants. It fails on these symbols/patterns:

```text
JobDelegate
DelegateRecord
CurrentJobID
LatestJobID
DelegateGeneration
StopGateClosed
findRunningDelegateByTranscriptRef
resumeOrFindRunningDelegate
relinkDelegateChildToJob
attachDelegateJob
finalizeDelegateOnce
delegateRuntimeUnload
closeDelegateLifetime
subtreeEpoch
ReceiverDelegateID
receiverDelegateID
applyReceiverWatchSend
installParentSourceWatchForChild
clearParentSourceWatchForChild
attachDelegateJobFromWatch
FromWatch
runFromWatch
deliverWatchCallback
staleDelegateWatchSend
delegateStoppedAfterWatchSendPending
```

Also assert registered delegate result/schema and delegate branches in `transcript_render.go` contain none of `job_id`, `started_job_id`, `current_job_id`, `latest_job_id`, `resumed_from_job_id`, `watch_parent`, or `job_send_message`; shell-only renderer branches may retain `job_id`.

Add scoped consumer assertions too: `cmd/serf-hub/internal/hubcore/prober.go` must not reference `Detailed.Jobs` or `job_type`; `agent/session_queue.go` and `session_queue_persist.go` must not reference `AttentionID`, delivery attention IDs, or resolution markers; delegate producer/parser/card sources must not combine `delegate-notification` with `job_id`/`job_type`, nor infer delegate semantics from `job-notification`; and the web delegate card branch must contain the `Delegate` label. The assertion also owns `cmd/serf/run_drain_test.go`, `cmd/serf/run_drain_nested_test.go`, and `cmd/serf-tui/model_misc_serffuzz_test.go`: delegate paths must use stable delegate markup/identity, while shell-only cases may retain shell job markup. Tests and fuzz seeds may carry forbidden legacy strings only as explicit rejection inputs.

- [ ] **Step 8: Run structural and behavioral gates**

Run:

```bash
gofmt -w agent/internal/jobstore/event.go agent/internal/jobstore/record.go agent/internal/jobstore/fold.go agent/internal/jobstore/store.go agent/jobs.go agent/jobs_nested.go agent/job_notify.go agent/job_watch.go agent/job_delegate.go agent/subagents.go agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/delegate_runtime.go agent/delegate_legacy_path_test.go
go test ./agent/internal/jobstore -count=20
go test ./agent -run '^TestDelegate(Resource|LegacyPath)|^Test(JobShell|JobManager|JobStore|JobWatch|Nested)' -count=20 -timeout=20m
go test -race ./agent -run '^TestDelegateResource_' -count=20 -timeout=30m
make fuzz-registry-check
git diff --check
git status --short
```

Run the manual inventory too:

```bash
rg -n 'JobDelegate|DelegateRecord|CurrentJobID|LatestJobID|DelegateGeneration|StopGateClosed|findRunningDelegateByTranscriptRef|resumeOrFindRunningDelegate|relinkDelegateChildToJob|attachDelegateJob|finalizeDelegateOnce|delegateRuntimeUnload|closeDelegateLifetime|subtreeEpoch|ReceiverDelegateID|receiverDelegateID|applyReceiverWatchSend|installParentSourceWatchForChild|clearParentSourceWatchForChild|attachDelegateJobFromWatch|FromWatch|runFromWatch|deliverWatchCallback|staleDelegateWatchSend|delegateStoppedAfterWatchSendPending' agent internal cmd server appwire
rg -n 'Detailed\.Jobs|job_type' cmd/serf-hub/internal/hubcore/prober.go
rg -n 'AttentionID|delegate:|shell:|AttentionResolution' agent/session_queue.go agent/session_queue_persist.go
rg -n -g '!**/*_test.go' -g '!**/*fuzz*.go' -g '!**/*.test.ts' -g '!**/*.test.tsx' 'job_type.*delegate|job-notification.*delegate|delegate-notification.*job_(id|type)' agent cmd/serf-tui cmd/serf-hub/frontend/src
rg -n 'job_type="delegate"|<job-notification[^>]*(delegate|dlg_)|SubagentRunInfo.*JobID' cmd/serf/run_drain_test.go cmd/serf/run_drain_nested_test.go cmd/serf-tui/model_misc_serffuzz_test.go
rg -n 'delegate-notification|delegate_id' cmd/serf/run_drain_test.go cmd/serf/run_drain_nested_test.go cmd/serf-tui/model_misc_serffuzz_test.go
```

Expected: the first search has only explicit legacy rejection/test literals or evergreen historical explanation; the first four scoped forbidden searches are empty; the final positive search shows stable delegate markup/identity in both drain paths and the tagged TUI fuzz. Every alternate authority and old consumer contract is gone.

- [ ] **Step 9: Stage exact deletion paths and commit**

Run `git status --short`. For each modified/deleted file shown, issue `git add -- <exact-path>` separately; do not use a glob, directory add, `-u`, or `-A`. Inspect `git diff --cached --stat` and the deletion inventory test, then commit:

```bash
git add -- agent/internal/jobstore/event.go agent/internal/jobstore/event_clone.go agent/internal/jobstore/fold.go agent/internal/jobstore/record.go agent/internal/jobstore/store.go
git add -- agent/internal/jobstore/cov_s4_jobstore_test.go agent/internal/jobstore/event_test.go agent/internal/jobstore/fold_fuzz_test.go agent/internal/jobstore/fold_test.go agent/internal/jobstore/jobstore_program_fuzz_test.go agent/internal/jobstore/record_test.go agent/internal/jobstore/seqfuzz_test.go agent/internal/jobstore/store_incremental_test.go agent/internal/jobstore/store_persistence_fuzz_test.go agent/internal/jobstore/store_test.go
git add -- agent/jobs.go agent/jobs_nested.go agent/job_notify.go agent/job_watch.go agent/job_delegate.go agent/subagents.go agent/subagent_manager.go agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go identifier/domains.go identifier/domains_test.go agent/delegate_runtime.go agent/delegate_legacy_path_test.go
git add -- agent/job_delegate_create_test.go agent/job_delegate_send_test.go agent/job_delegate_send_fifo_test.go agent/job_delegate_finalize_test.go agent/job_delegate_drivedown_test.go agent/job_nested_test.go agent/subagents_test.go
git add -- scripts/run-fuzz.sh
git commit -m "refactor: remove delegate activation jobs" -m "Delete the delegate JobRecord schema, current/latest mappings, generation strings, stop gates, output/notification rails, manager runtime branches, attach/relink/finalize helpers, lifecycle mirrors, and inactive alternate route. Jobstore and jobManager are shell-only; the stable controller and aggregate are now the sole delegate authorities."
```

If `git status --short` shows an additional old shape/fuzz test that had to be rewritten or deleted to preserve a real behavior contract, add that exact path to this Task 7 list before staging it. Never substitute a directory or wildcard add.

Expected: hooks pass. Record production additions/deletions separately from tests/docs in Kata `my73`; authority count, not raw total line count, is the acceptance metric.

---

### Task 8: Update all evergreen shipped documentation and prompts

**Files:**
- Modify: `docs/subagent-management/11-delegate-resource-model.md` status/current-state preface only after verification of shipped behavior
- Modify: `docs/architecture.md`, `docs/job-control.md`, `docs/subagent-runtime-contracts.md`, `docs/tools/transcripts.md`, `docs/hooks.md`
- Modify: `cmd/serf-doctor/README.md`
- Modify: `internal/bundled/skills/doctoring-serf/references/data-model.md`, `failure-modes.md`, `finding-contract.md`, `repair-guardrails.md`, `writing-runbooks.md`
- Modify: `agent/prompts/sections/delegation.md`, `agent/prompts/sections/background-jobs.md`
- Modify: `internal/bundled/agents/subagent.md`
- Modify: `internal/bundled/plugins/coordinator-workflow/agents/coordinator.md`

**Interfaces:**
- Consumes: verified Task 6 behavior and Task 7 deletion inventory.
- Produces: evergreen current-reality documentation only; no new dated spec or compatibility promise.

- [ ] **Step 1: Document controller ownership and lock order**

In `docs/architecture.md`, state one root controller/fold/lock, exact context leases, process-only tokens, restart-safe durable IDs, permitted controller-store/transcript I/O, external shell evidence before lock, immutable update plans, and prohibition on provider/process/hook/delivery/wait under lock.

- [ ] **Step 2: Document stable tool and stop contracts**

In `docs/job-control.md`, describe stable-only delegate identity, `target`, one `items` list, metadata-only status, running steer versus idle start, contextual caller, mandatory recursive stop, one pending stop per tree, shell-only jobs/output/watch, and explicit unsupported delegate watches. Remove current/latest activation guidance.

- [ ] **Step 3: Document runtime, settlement, delivery, restart, and disposal**

In `docs/subagent-runtime-contracts.md`, describe durable steering/request binding, per-tool admission, settlement precedence, private `completed_no_action`, ordered delivery collection, deterministic IDs, start double-failure recovery latch, exact shell receipts, provider-free reconciliation evidence, resident idle runtimes, attention runtime identity, and monotonic resumability closure.

- [ ] **Step 4: Update transcript, hook, doctor, and bundled guidance**

State that delegate conversation is read through `transcript_ref`, `job:job_...` is shell output, delegate lifecycle events are distinct, doctor reads one root delegate store non-mutatingly, and no activation job can be polled/stopped/watched. Remove observer-sidecar/`watch_parent`, `job_send_message`, and activation-job orchestration instructions from prompts and bundled agents.

- [ ] **Step 5: Mark the evergreen decision as shipped without rewriting rationale**

Change only the opening status/current-state paragraphs of `docs/subagent-management/11-delegate-resource-model.md` to reflect verified implementation. Preserve decision history, scope cuts, stop conditions, and follow-on projects.

- [ ] **Step 6: Run documentation and bundled-content gates**

Run:

```bash
go run ./cmd/serf-docscheck
go test ./internal/bundled/... -count=1
go test ./agent/doctor ./cmd/serf-doctor -count=20
git diff --check
git status --short
```

Expected: docscheck, bundled prompt/reference tests, doctor tests, and diff check pass.

- [ ] **Step 7: Stage exact evergreen files and commit**

Run `git status --short`, then issue one explicit `git add -- <exact-path>` for each listed modified doc/prompt/reference. Commit:

```bash
git add -- docs/subagent-management/11-delegate-resource-model.md docs/architecture.md docs/job-control.md docs/subagent-runtime-contracts.md docs/tools/transcripts.md docs/hooks.md cmd/serf-doctor/README.md
git add -- internal/bundled/skills/doctoring-serf/references/data-model.md internal/bundled/skills/doctoring-serf/references/failure-modes.md internal/bundled/skills/doctoring-serf/references/finding-contract.md internal/bundled/skills/doctoring-serf/references/repair-guardrails.md internal/bundled/skills/doctoring-serf/references/writing-runbooks.md
git add -- agent/prompts/sections/delegation.md agent/prompts/sections/background-jobs.md internal/bundled/agents/subagent.md internal/bundled/plugins/coordinator-workflow/agents/coordinator.md
git commit -m "docs: describe the shipped delegate resource model" -m "Make evergreen architecture, job control, runtime, transcript, hook, doctor, and bundled guidance match the stable delegate controller: one identity, one aggregate, exact leases, durable steering, ordered delivery, globally serialized stop, provider-free restart, shell-only jobs, and deliberate watch/unload cuts."
```

Expected: hooks pass.

---

### Task 9: Prove mutation sensitivity, full repository readiness, and independent review

**Files:**
- Modify causal tests only if a verified coverage gap requires an additional assertion; never weaken existing tests
- Modify production/docs only in response to a reproduced Critical/Important finding
- Update Kata `my73` with exact evidence

**Interfaces:**
- Consumes: all prior task outputs.
- Produces: causal mutation evidence, complete gates from bare exits, clean ancestry/porcelain/generated state, sequential independent approvals, and a no-push handoff.

- [ ] **Step 1: Run the final authority/deletion inventory**

Run both Task 7 searches plus:

```bash
rg -n 'job_id|started_job_id|current_job_id|latest_job_id|resumed_from_job_id|watch_parent|job_send_message' agent/internal/tool agent/session_tools_jobs.go agent/transcript_render.go agent/prompts internal/bundled cmd/serf-tui cmd/serf-hub/frontend/src
rg -n 'Detailed\.Jobs|job_type' cmd/serf-hub/internal/hubcore/prober.go
rg -n 'AttentionID|delegate:|shell:|AttentionResolution' agent/session_queue.go agent/session_queue_persist.go
rg -n -g '!**/*_test.go' -g '!**/*fuzz*.go' -g '!**/*.test.ts' -g '!**/*.test.tsx' 'job_type.*delegate|job-notification.*delegate|delegate-notification.*job_(id|type)' agent cmd/serf-tui cmd/serf-hub/frontend/src
rg -n 'delegate-notification|Delegate' agent/delegate_delivery.go cmd/serf-tui/internal/transcript/job_notification.go cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.tsx cmd/serf-hub/frontend/src/protocol/fixtures/tool-and-jobs.jsonl cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx
rg -n 'job_type="delegate"|<job-notification[^>]*(delegate|dlg_)|SubagentRunInfo.*JobID' cmd/serf/run_drain_test.go cmd/serf/run_drain_nested_test.go cmd/serf-tui/model_misc_serffuzz_test.go
rg -n 'delegate-notification|delegate_id' cmd/serf/run_drain_test.go cmd/serf/run_drain_nested_test.go cmd/serf-tui/model_misc_serffuzz_test.go
```

Review every remaining match. Expected: shell job fields, explicit legacy rejection/test literals, or historical explanation only in the broad search; all four forbidden scoped searches are empty; the positive searches show the stable tag/ID parser and `Delegate` card label in production, both fixture paths, both drain paths, and the tagged TUI fuzz. No delegate production dependency, queue authority, prober fallback, tagged replay fixture, drain fixture, or public job alias remains.

- [ ] **Step 2: Prove six causal mutations**

Apply one temporary mutation at a time with `apply_patch`, run the named selector, capture its failing assertion, then reverse exactly that patch with `apply_patch` and rerun GREEN:

1. remove finish generation equality → stale-finalizer test fails;
2. acknowledge steer before transcript append → real-provider steering test fails;
3. allow settlement with pending steer → normal and communicate continuation tests fail;
4. clear stop before start/shell work drains → reservation/receipt tests fail;
5. permit start while stopping → stop-restart successor test fails;
6. skip direct-owner/stale-actor check → authorization tests fail.

After every restoration run `git diff --check` and compare `git diff` to the pre-mutation saved inspection; do not reset or checkout files.

- [ ] **Step 3: Run causal normal/race/fuzz gates**

Run:

```bash
go test ./agent -run '^TestDelegateResource_' -count=20 -timeout=20m
go test -race ./agent -run '^TestDelegateResource_' -count=20 -timeout=30m
go test ./agent/internal/delegatestore -count=20
go test ./agent/internal/contextmgr -run '^TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults$' -count=20
go test -race ./agent/internal/contextmgr -run '^TestAttentionResolutionMarkerInsideToolRoundCompactionPreservesCallAndResults$' -count=20
SERF_FUZZ_TESTS=1 go test ./agent/internal/contextmgr -run '^(TestCompactionSeqFuzz|TestFc1MaybeCompactSeqFuzz)$' -count=1
SERF_FUZZ_TESTS=1 go test -tags serffuzz ./agent/internal/delegatestore ./agent -run '^(Test|Fuzz)(Delegate|RegistrySchema)' -count=1
```

Expected: all pass, including fault injection for all eight event boundaries, crash-safe create publication, crash-durable normal settlement, generation-keyed inline waiters behind blocked delivery heads, head-only live and restart delivery ordering, durable idle stop fencing, provider-free descendant-shell repair before stop completion, stop retry/serialization, repeating attention cleanup, attention runtime identity, marker-transparent compaction at deterministic and full Rapid depth, and input-compensation double failure.

- [ ] **Step 4: Run focused cross-surface regression gates**

Run:

```bash
go test ./agent -run '^Test(JobShell|JobManager|JobStore|JobWatch|ReadTranscript|SessionCommunicate|SessionClose|Worktree|Sandbox|Nested)' -count=1 -timeout=20m
go test ./cmd/serf -run '^(TestAgentToServerDetailedStatus_DelegatesLossless|TestRunDrainsDelegatedJobTreeBeforeExit|TestRunDrainsNestedDelegateSubtree)$' -count=20
go test ./server -run '^(TestStatusEndpoint_DetailedStatusIncludesStableDelegates|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)$' -count=20
go test ./cmd/serf-hub -run '^TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus$' -count=20
go test ./agent/internal/jobstore ./agent/doctor ./agent/internal/atif ./cmd/serf-doctor ./internal/appprojector ./appwire ./server ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui/... -count=1
make test-web
make test-web-browser
```

Expected: shell lifecycle, history, nested delegation, disposal, projections, doctor, TUI, frontend, and browser guards pass. Missing Chrome is a reported failure/limitation, never a pass.

- [ ] **Step 5: Run the canonical repository gates serially and record bare exits**

Run each as its own command and record its exit code, duration, and evidence root on failure:

```bash
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
```

Do not infer a verdict from partial logs. `make lint`, `make build`, `ROOT_FULL=1 make test`, and `make test-dev-tooling` are the canonical merge-approval composition. `make test-fuzz` is additionally mandatory before merge at full default Rapid depth; it is distinct from `make fuzz`, whose native/committed-corpus replay does not replace the full Rapid search. The explicit all-module build/vet, race, deterministic fuzz replay/coverage/corpus, and browser gates have separate required ownership for this change.

After an eventual Jesse-authorized merge, the integrator repeats the post-merge sequence from the repository root and records each bare exit independently:

```bash
make merge-approval-gate
make test-fuzz
make fuzz
```

`make merge-approval-gate` retains its documented four-command composition; the two fuzz-family gates remain separate required post-merge commands. This plan does not authorize the merge or these post-merge mutations itself.

- [ ] **Step 6: Verify generated state, ancestry, diff, and cleanliness**

Run:

```bash
make generate
git diff --exit-code -- docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
git diff --check main...HEAD
git diff --stat main...HEAD
git status --short
git rev-parse HEAD
```

Then run `git merge-base --is-ancestor delegate-identity-integration HEAD` as a separate command and record its bare exit code. Expected: exit 1 (the abandoned branch is not an ancestor), empty porcelain, current generated outputs, clean diff, and a production deletion inventory showing one authority/fold/lock and zero alternate paths.

- [ ] **Step 7: Run three independent reviews sequentially**

Dispatch only one reviewer at a time, forbid recursive delegation, and wait with `timeout_ms: 1800000`:

1. architecture/complexity — sole authority/fold/lock, scope cuts, stop/delivery/attention/restart design, deletion inventory, no compatibility/framework;
2. concurrency/restart — lock order, exact leases, start double failure, steer/settle/finish, shell receipts, one stop, ordered delivery, immutable evidence/update plans;
3. tests/public surface — RED provenance, mutation sensitivity, schemas/events/AppWire/TUI/web/doctor/docs, generated outputs.

Each receives `AGENTS.md`, `docs/testing.md`, evergreen design, this plan, exact branch/HEAD, and `main...HEAD`; each is read-only. If a Critical/Important finding reproduces, add a causal RED, fix root cause, rerun affected/full gates, and restart all three reviews sequentially on the new HEAD.

- [ ] **Step 8: Commit only an intentional reviewed correction**

If review/verification changed files, run `git status --short`, stage each exact path separately, rerun the applicable full gates, and commit with a detailed finding/RED/fix/evidence body. If nothing changed, create no empty verification commit.

- [ ] **Step 9: Hand off without merge or push**

Report branch, HEAD, commits, empty porcelain, main ancestry, abandoned-branch non-ancestry, phase-zero REDs, final GREEN/mutation evidence, every gate exit/duration, three review verdicts, production additions/deletions, and any environmental limitation. Do not merge or push unless Jesse explicitly asks.
