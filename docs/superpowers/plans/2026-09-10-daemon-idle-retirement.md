# Daemon Idle Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire provably inactive Hub-launched daemons without terminating saved conversations or idle delegates, and expose safe, identity-fenced resident-process controls.

**Architecture:** One process-owned admission controller follows the mutable serve root and fences effects before checking the complete runtime tree. Preparation validates persistence and reconstruction without closing anything; committed retirement uses an explicit non-terminal release policy, not ordinary `Session.Close`. Existing Hub discovery, sorted ownership locks, mutation-ID replay, relay recovery and Settings → Hub remain the integration points.

**Tech Stack:** Go workspace modules, AppWire catalog/code generation, React/TypeScript, Zustand, Vitest, Chrome/CDP browser guards, scripted LLM adapters and real fixture-owned git repositories.

**Spec:** `docs/superpowers/specs/2026-09-10-daemon-idle-retirement-design.md` (written design and policy approved).

## Global Constraints

- “Newly Hub-launched daemons automatically retire after one hour of continuous, proven inactivity. The duration is configurable; zero disables automatic retirement. Standalone `evener serve` remains opt-in.”
- “Work, active watches, pending deliveries, queued input, and outstanding human questions prevent retirement. Passive viewing and Hub probes do not reset the inactivity interval.”
- “Retirement preserves resumable sessions, idle delegates, and required worktrees.”
- “Hub exposes resident daemons, including archived and incompatible sessions, with eligibility information and explicit stop controls.”
- “Existing incompatible daemons require an explicit, verified stop. Age, PPID, an old executable, or a failed probe never authorize an automatic kill.”
- Observable process phases are `resident`, `preparing`, `retiring`; these are not new session states.
- Hub TOML is `daemon_idle_timeout`, default `"1h"`; direct CLI is `evener serve --daemon-idle-timeout`, default zero. Explicit zero must survive all launch/resume layers.
- Do not change provider stream idle timeout, archive retention, explicit shutdown, deletion/force-stop recovery authority, or detached process lifetime.
- Read `docs/developing-evener/testing.md` before implementing tests. No credentials, network providers, ambient HOME, fixed sleeps, or mock Evener subsystems in default gates. Use clock/barrier/process-launcher/LLM/filesystem boundaries.
- No automatic default activation until admission, primary-record preservation, nested cold restore and failure gates are green.
- Do not install binaries, restart production Hub, stop existing daemons, touch the provider-onboarding branch, merge main, or silently remediate the separately reported Vitest audit finding.

---

## Execution, ownership and evidence

This is one plan because the end-to-end safety boundary spans the runtime and its existing owner/recovery UI. A standalone timer or dashboard would not satisfy the approved design. Tasks are review units, not permission to enable a partial feature.

Use this checkout; do not create another worktree merely to execute the plan. In a fresh checkout, install/copy dependencies before gates (`make web-preflight`). Each task gets a fresh implementer, then independent specification and code-quality review. Only one task changes shared files at a time. Later tasks explicitly own their additional edits to shared files; earlier implementers must finish before ownership transfers. Each checkbox is one action; repeat a test's red/green cycle for each additional case named in that task. Commands run from repository root unless enclosed in `(cd … && …)`.

**Required pre-implementation review:** the parent assigns two independent parallel adversarial plan reviewers, competing for five points for distinct legitimate significant findings. False findings or inflated severity disqualify the reviewer. Adjudicate using source, record duplicates/accept/reject reasons/scores, revise this plan, and repeat until both have no unresolved significant issue or five rounds have run. A capped loop with unresolved issues blocks implementation. Then perform the requested four-angle plan simplification (reuse, simplification, efficiency, abstraction level), without removing safety evidence. Review-loop execution belongs to the parent, not this drafting unit.

**Evidence record — Create:** `docs/superpowers/reviews/2026-09-10-daemon-idle-retirement.md`. For every task record commit, red command/exit/assertion, green command/exit, race evidence, implementer/spec-review/quality-review results and environmental skips. Append both competitive review loops and both simplification passes. Do not describe a command as passed until it exits zero; a test selector matching no tests is failure to verify.

### File responsibility map and dependencies

| Task | Owner / responsibility | Depends on |
| --- | --- | --- |
| 1 | `agent/retirement.go`: short admission claims, reader leases, phase/root identity | none |
| 2 | Root/client-mutation/daemon route admission and catalog coverage | 1 |
| 3 | Delegate controller and cold-tree retirement evidence | 1, 2 |
| 4 | Jobs, watches, autonomous and environment admission; whole-tree predicate | 2, 3 |
| 5 | Fallible preparation and primary durable reconstruction checks | 4 |
| 6 | Policy-factored non-terminal release, scratch/lanes and nested restore | 5 |
| 7 | Typed lifecycle diagnostics, unavailable contract, routes and generators | 6 |
| 8 | Daemon timing/current-root ownership and launch configuration | 7 |
| 9 | Hub retirement-versus-resume and relay source recovery | 8 |
| 10 | Full resident inventory and stale-identity-fenced actions | 9 |
| 11 | Mounted Settings → Hub resident controls | 10 |
| 12 | Selected-thread recovery through real client/store/relay and browser | 9, 11 |
| 13 | Real Hub-launched daemon lifecycle acceptance and operator documentation | 8–12 |

### Source-grounded boundaries (read before the relevant task)

- `agent/session_client_mutation.go:248` accepts durably before wake; `claimClientMutationStart` changes runnable ownership before the turn starts. `agent/session_client_mutation_queue.go` owns all durable queue/steer variants. A wire-handler-only fence is insufficient.
- `agent/delegate_tree_reclaim.go:202` checks resident terminal subtrees; `runtimeReclamationIntersectsProcessWorkLocked` covers reservations, input/steer/model/settlement/quiet claims, deliveries, watch receipts and reconciliation. It does **not** prove that a cold tree has no obligations, or that closing a root preserves it.
- `agent/session_lifecycle.go:338–559` sets closed state, stops durable delegates, cancels jobs/watches, disposes lanes, invokes hooks and appends session-end evidence. Calling this unchanged is not retirement. `maybeAutoSave` in `agent/session.go:1902` currently emits a warning instead of returning failure.
- `cmd/evener/serve.go:657–700` owns `currentSess`, clear/swap and the one shutdown pass. Retirement must join this ownership, not retain the startup `sess` pointer.
- `cmd/evener-hub/app_threadlifecycle.go:325–492` uses sorted alias locks, deletion/recovery epochs, discovery and exact-protocol owner checks. `cmd/evener-hub/app_rpc.go:687–732` retries the same `turn/start` params.
- `cmd/evener-hub/app_force_stop.go:21–185` verifies a retained process handle and persists explicit-resume authority. Its existing check protects the target selected **when the request arrives**; resident UI additionally needs the identity selected **when the row was rendered**.
- `cmd/evener-hub/frontend/src/stores/threads.ts:1028` hydrates/subscribes; `handleNotification` handles `evener/thread/resync` through `handleReady`. Preserve this path rather than inventing a second thread store. `cmd/evener-hub/app_relay.go` already has bounded recovery and subscription ownership.

## Cross-task interface contract

All APIs in this section are **proposed new APIs**, not claims about the baseline. The task that produces an API owns its implementation and direct tests. Keep names/types identical at consumers.

```go
// agent/retirement.go; aliases let daemon tests inject the existing clock shape.
type RetirementClock = clock.Clock
type RetirementTimer = clock.Timer
type RetirementTicker = clock.Ticker

type RetirementBlocker struct {
    Category string // closed vocabulary below
    SessionID string
    DelegateID string
}
type RetirementSnapshot struct {
    Phase string
    Timeout time.Duration
    EligibleSince time.Time // zero means unknown/not eligible
    Deadline time.Time      // zero for disabled/not eligible
    Blockers []RetirementBlocker
    Failure string          // bounded machine category, never raw error/prompt/path
}
type RetirementController struct {
    mu sync.Mutex
    root *Session
    generation uint64
    phase string
    active map[uint64]RetirementBlocker
    nextLease uint64
    readers int
    readersDone chan struct{}
    changed chan struct{}
    clock RetirementClock
    timeout time.Duration
    eligibleSince time.Time
    failure string
    blockers []RetirementBlocker
    claim *RetirementClaim
}
type RetirementClaim struct {
    controller *RetirementController
    root *Session
    generation uint64
    committed bool
    finished bool
}
type RetirementPreparation struct {
    claim *RetirementClaim
    sessions []*Session
    evidenceVersion uint64
    lanes []retirementLaneEvidence
    released bool
}
type retirementLaneEvidence struct {
    sessionID string
    delegateID string
    path string
    branch string
    owner string
}

func NewRetirementController(timeout time.Duration, clk RetirementClock) (*RetirementController, error)
func (c *RetirementController) AttachRoot(root *Session) error
func (c *RetirementController) BeginMutation(sessionID, category string) (release func(), err error)
func (c *RetirementController) Borrow() (release func(), err error)
func (c *RetirementController) Snapshot() RetirementSnapshot
func (c *RetirementController) Changed()
func (c *RetirementController) TryClaim(manual bool) (*RetirementClaim, RetirementSnapshot, error)
func (c *RetirementController) Prepare(ctx context.Context, claim *RetirementClaim) (*RetirementPreparation, error)
func (c *RetirementController) Abort(claim *RetirementClaim, failure string) error
func (c *RetirementController) Commit(claim *RetirementClaim) error
func (c *RetirementController) DrainReaders(ctx context.Context) error
func (c *RetirementController) Run(ctx context.Context, retire func(context.Context, *RetirementClaim) error) error
func (s *Session) ReleaseForRetirement(ctx context.Context, prepared *RetirementPreparation) error
```

These new private layouts and the algorithms in Tasks 1 and 5 define the cross-task contract; use the established lane ownership representation rather than inventing a second owner authority. Controller implementation stays in `agent`, so it can inspect private runtime evidence without exporting the session state machine. The daemon owns the controller's lifetime and calls its public methods. Nil controller on non-daemon sessions means existing behavior, not enabled retirement.

`RetirementBlocker.Category` vocabulary: `turn`, `input`, `autonomous`, `question`, `job`, `watch`, `delegate`, `environment`, `persistence`, `admission`, `unsupported`. IDs only, sorted category/session/delegate with duplicates removed. Internal errors are logged with existing logging policy; wire `Failure` contains only `prepare_failed`, `release_failed`, `reader_drain_failed`, or empty. Unknown/corrupt state blocks; never infer eligibility from a missing runtime pointer.

### Admission inventory and handoff rule

**Lock ordering:** `retirement.mu` protects only controller counters/phase/generation and is released before *every* callback, session/controller/job/store lock, I/O, wait, event emission or environment operation. A claim is a phase fence, not a mutex held over teardown. Ordinary entry increments an active count under that short lock. Claim only succeeds if active count is zero; it does not wait for active work. Nested entry is safe because no writer waits while a reader recurses. `Session.mu` must never surround journal I/O. Existing response-side-effect → session lock ordering and delegate controller lock order remain unchanged. `Changed()` and release callbacks execute after owner locks are dropped.

| Entry family / exact existing boundary | Required admission placement and continuing blocker | Task |
| --- | --- | --- |
| `AcceptClientMutationStart`, `claimClientMutationStart`, `InterruptClientMutation` in `agent/session_client_mutation.go` | Before reserve/claim/update, including replay lookup; keep token through durable result/wake. Pending executions/reservations/claimed state then own the obligation. | 2 |
| `AcceptClientMutationQueue`, `AcceptClientMutationSteer`, `AcceptClientMutationDrainAsSteer`, `AcceptClientMutationPromoteQueuedAsSteer`, `AcceptClientMutationCancelQueued` in `agent/session_client_mutation_queue.go` | Before first journal call; token remains through queue projection and wake. Never acknowledge a rejected admission. | 2 |
| `agent/session_queue.go` legacy enqueue/steering, `agent/session_lifecycle.go` `processInputKindWithProvenance` and input-claim transitions | Before queue mutation/runner handoff; root and descendant turns hold the lease until final settlement/autosave. Pending queues and claimed input block between callers. | 2 |
| `server/appwire_runtime.go` all daemon routed handlers | Classify every catalog method; effect methods hold a mutation lease before invoking hook, reads borrow only for in-flight handler access. No subscription-lifetime lease. | 2 |
| `SetModel`, `SetReasoningEffort`, vision/name setters in `agent/session.go`; `Compact` in `agent/session_compaction.go`; `SetGoal` in `agent/session_goal.go` | Direct setters also enter at the effect boundary. Change the existing effect methods to report errors and update callers; no admitted/legacy duplicates or compatibility wrappers. Refuse before effects. | 2, 4 |
| `cmd/evener/serve.go` clear/new-root publication, run-channel wake, shutdown | Clear holds one mutation lease from construction through publication/old-root settlement. Incoming channels may not carry an unrepresented intent. Shutdown remains terminal and claims its existing one-owner path. | 2, 8 |
| `agent/delegate_runtime.go`, `agent/delegate_delivery.go`, `agent/subagents.go` create/send/restore/recovery, result delivery | Admission before reservation or cold construction; retain through binding/reconstruction/rollback or transfer to controller claims. Children inherit the same pointer before any goroutine is launched. | 3 |
| `agent/delegate_tree_controller.go`, `agent/delegate_tree_attention.go`, `agent/delegate_tree_watch.go`, `agent/delegate_tree_reclaim.go` autonomous controller work | All work registration, claims, receipts, stop and reclamation begin under admission **outside** controller mutex. Existing durable/controller maps own subsequent work; completion callbacks notify after unlocking. | 3 |
| `agent/jobs.go`, `agent/job_watch.go` job launch/state transitions, timer/event watch registration, delivery claim/receipt/settlement | Register under admission before publication; active jobs/watches and pending obligations block until settled. Fired one-shot timer is not eligible until its delivery is committed/aborted. | 4 |
| `agent/session_attention.go`, `agent/session_jobtree_drain.go`, `agent/session_namer.go`, `agent/session_goal.go` notifications, drain/retry, naming, goal continuation | Acquire before goroutine enqueue/start; release after callback/persistence. Register pending work before scheduler handoff. An active goal or unresolved attention callback remains a blocker while idle. | 4 |
| `agent/session_lifecycle.go` `beginEnvWork`, `beginDispose`; `agent/session_env_swap.go`; `agent/session_tools_worktree.go`; `agent/session_worktree_sweep.go`; `agent/session_worktree_relock.go` | Admission is acquired before environment bookkeeping, inherited by detached rollback, retained until join completion. Scheduled maintenance must have a pending blocker or a fenced callback; no untracked timer can mutate a retired runtime. | 4 |
| `agent/session_tools_ask.go`, `agent/session_escalation.go` question registration/resolution | Register before publishing callback; unresolved questions/escalations block even when wire state is idle. Decision resolution is an effect, not a read. | 4 |

This inventory is an implementation checklist. An executor must resolve each family to its concrete entry symbols while editing that named file and record the checked symbols in the review record. A route found outside this table must be covered before Task 8, not dismissed as unlikely. Task 2 has a catalog completeness test; Tasks 3–4 have real entry-point race tables, not tests that merely set the controller counter.

---

### Task 1: Implement the nonblocking admission/claim controller

**Files:**
- Create: `agent/retirement.go`
- Create: `agent/retirement_test.go`
- Modify: `agent/session.go` (`Session` controller pointer)
- Read: `agent/internal/clock/clock.go`, `agent/internal/agenttest/clock.go`

**Interfaces:**
- Consumes: existing `clock.Clock`, `clock.Timer`, `*Session`.
- Produces: `NewRetirementController`, `AttachRoot`, `BeginMutation`, `Borrow`, `Changed`, `Snapshot`, `TryClaim`, `Abort`, `Commit`, `DrainReaders`; `ErrRetirementUnavailable` sentinel. `Run` and full predicate are added later, not used yet.
- Internal `retirementClaimCandidate() (*Session, uint64, bool)` is unnecessary: keep claim creation private inside `TryClaim` to avoid two authorities.

- [ ] **Step 1: Write a failing barrier test.** The initial claim predicate checks only controller state; Tasks 2–5 extend it before activation. `clock.Real()` is used below because this test makes no timing assertion.

```go
func TestRetirementAdmissionWinsWithoutWaiting(t *testing.T) {
    c, err := NewRetirementController(0, clock.Real())
    if err != nil { t.Fatal(err) }
    root := newQueuePersistTestSession(t, t.TempDir())
    defer root.Close()
    if err := c.AttachRoot(root); err != nil { t.Fatal(err) }
    release, err := c.BeginMutation(root.ID(), "input")
    if err != nil { t.Fatal(err) }
    claim, state, err := c.TryClaim(true)
    if err != nil { t.Fatal(err) }
    if claim != nil || state.Phase != "resident" || len(state.Blockers) == 0 {
        t.Fatalf("admitted input was not a blocker: claim=%v state=%+v", claim, state)
    }
    release()
    release() // release is idempotent, including concurrent cancellation.
    claim, _, err = c.TryClaim(true)
    if err != nil || claim == nil { t.Fatalf("claim: %v %v", claim, err) }
    if _, err := c.BeginMutation(root.ID(), "input"); !errors.Is(err, ErrRetirementUnavailable) {
        t.Fatalf("claim-first admission: %v", err)
    }
    if err := c.Abort(claim, "prepare_failed"); err != nil { t.Fatal(err) }
    release, err = c.BeginMutation(root.ID(), "input")
    if err != nil { t.Fatal(err) }
    release()
}
```

`newQueuePersistTestSession` exists in `agent/session_queue_persist_test.go:30`; add imports `errors`, `testing`, and the existing internal clock package. Additional tests use channels to hold a borrowed read while committing, assert admission stays fenced, release the reader and await `DrainReaders`; assert nested mutations cannot deadlock; reject stale claims/root generations and double commit; assert failure after commit cannot abort back to resident.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirement' -count=1)` — FAIL, undefined controller APIs.
- [ ] **Step 3: Implement the controller algorithm.** Private state is `mu sync.Mutex`, `root *Session`, `generation uint64`, `phase string`, `active map[uint64]RetirementBlocker`, `readers int`, `readersDone chan struct{}`, `nextLease uint64`, `changed chan struct{}` (capacity one), clock, timeout, eligible-since and failure. Each release closure uses `sync.Once`.

```text
BeginMutation:
  lock; if phase != resident, unlock and return ErrRetirementUnavailable
  allocate lease ID, add category/session ID; clear eligibleSince/deadline
  unlock; coalesce Changed; return once-only release
release:
  lock; delete exact lease; unlock; Changed
Borrow:
  lock; refuse only retiring; increment readers; unlock
  return once-only decrement; close readersDone when retiring and count reaches zero
TryClaim(manual):
  lock; refuse concurrent claim; if active nonempty return resident blockers
  if automatic and disabled/not-due return resident snapshot
  set preparing; capture root pointer and generation in claim; unlock
  check root/tree predicate outside controller lock (Tasks 2–5)
  on blockers restore resident/reset eligible interval and return no claim
  otherwise return exact claim; never wait for admitted work
Commit:
  lock; require exact preparing claim/root/generation; phase = retiring
  initialize reader-drain channel, close immediately if count zero; unlock
Abort:
  lock; require exact preparing claim; resident, clear eligible interval, bounded failure
  unlock; Changed; never permit Abort after Commit
AttachRoot:
  require resident phase; publish same controller on root; update pointer/generation
  reset interval; no controller lock held while accessing any Session lock
```

The root swap caller in Task 8 holds an admitted operation; `AttachRoot` rechecks phase/generation on publication so startup versus claim cannot race. Do not embed an RWMutex lease across work: recursive reads with a waiting writer can deadlock.

- [ ] **Step 4: Run green/race.** `(cd agent && go test -race . -run '^TestRetirement' -count=1)` — PASS, no races; reader-drain timeout leaves `retiring`.
- [ ] **Step 5: Commit.** `git add agent/retirement.go agent/retirement_test.go agent/session.go && git commit -m "feat(agent): add retirement admission and claim controller"`

### Task 2: Fence root input and every daemon RPC resource access

**Files:**
- Create: `agent/session_retirement_admission.go`
- Create: `agent/session_retirement_admission_test.go`
- Create: `server/appwire_retirement_admission.go`
- Create: `server/appwire_retirement_admission_test.go`
- Modify: `agent/session_client_mutation.go`, `agent/session_client_mutation_queue.go`, `agent/session_queue.go`, `agent/session_lifecycle.go`, `agent/session.go`, `agent/session_compaction.go`, `agent/session_goal.go`
- Modify: `internal/appserver/router.go`, `server/appwire_runtime.go`, `server/server.go`
- Modify: `cmd/evener/serve.go`, `cmd/evener/run.go`, `agent/subagents.go`, `agent/delegate_runtime.go`, `agent/session_tool_round.go`, `agent/session_jobtree_drain.go`, `agent/session_self_compact.go`, `agent/session_init.go` (callers of changed effect signatures; later tasks own subsequent edits)
- Modify: `cmd/evener/serve_residual_fuzz_test.go`, `server/reasoning_effort_test.go`, `server/appwire_turns_paging_test.go`, `server/appwire_reasoning_replay_test.go`, `server/appwire_runtime_test.go`, `server/server_surface_fuzz_test.go`, `server/appwire_capabilities_push_test.go`, `server/appwire_server_test.go` (existing callback signatures/test doubles)
- Read: `agent/session_client_mutation_persist.go`, `server/appwire_catalog_test.go`

**Interfaces:**
- Consumes: Task 1 controller and sentinel.
- Produces: `func (s *Session) beginRetirementMutation(category string) (func(), error)` (nil controller returns no-op release), `func (r *Router) SetAdmission(fn func(context.Context, string) (func(), error))` installed before serving, `func (s *Server) SetRetirementAdmission(fn func(context.Context, string) (func(), error))`.
- Produces private `daemonRetirementAccess(method string) (kind string, ok bool)` with kinds `read`, `mutation`, `control`. Router admission wraps the handler invocation, not connection/subscription lifetime. It must not wrap response serialization that uses already detached response values.
- Changes existing methods **in place**, with no parallel admitted/legacy entry points:

```go
func (s *Session) SetReasoningEffort(effort string) error
func (s *Session) Steer(msg string) error
func (s *Session) SteerKind(msg, kind string) error
func (s *Session) SteerTaskCompletion(msg string, blockingDelegateIDs []string) error
func (s *Session) SteerWithProvenance(msg string, p *provenance.Causal, kind string) error
func (s *Session) SteerWithImages(msg string, images []ImageAttachment) error
func (s *Session) SteerFromUser(msg string) error
func (s *Session) SteerFromUserWithImages(msg string, images []ImageAttachment) error
func (s *Session) trySteerMessageUnlessSuperseded(entry steeringMessage, publishedRevision int) (bool, error)
func (s *Session) saveMeta() error
func (s *Server) SetReasoningEffortFunc(fn func(string) error)
func (s *Server) SetSteerFunc(fn func(string) error)
func (s *Server) SetSteerWithImagesFunc(fn func(string, []ImageAttachment) error)
```

`trySteerMessageUnlessSuperseded` (`session_queue.go:346`) is the one existing append boundary: acquire before its first state effect, return `(false, ErrRetirementUnavailable)` on the lifecycle fence, preserve `(false,nil)` for the existing empty/superseded no-op, and `(true,nil)` on enqueue. Change its existing `trySteer`, `trySteerWithImages`, `trySteerWithProvenance`, `trySteerWithImagesAndProvenance`, `trySteerEnqueue` and `trySteerMessage` forwarding chain to the same `(bool,error)` return pair, retaining their current parameters. Public steering methods return that error. Change existing `steerKindForFold(msg,kind string,publishedRevision int)` and `deliverHookContext(text string)` to return `error`; callers explicitly handle it. Boolean notification callbacks retain their existing delivery contract: translate an error to **false**, report the failed admission, and keep the durable receipt pending; never acknowledge delivery or erase the source obligation. This is adapting existing delivery semantics, not adding compatibility entry points.

`SetReasoningEffort` acquires admission before `Session.mu`, returns the sentinel on refusal and `saveMeta()`'s error after mutation. Factor the existing `maybeAutoSave` write body into `saveMeta` here; `maybeAutoSave` keeps its existing warning contract. Update `server.reasoningEffortFunc`, `serveServer.SetReasoningEffortFunc` (`serve.go:81`) and its closure (`serve.go:1000`) to `func(string) error`; the AppWire handler returns the callback error, not `EmptyResponse,nil`. Startup/CLI callers fail the operation/startup; running tool/continuation callers propagate the error into their existing tool/result/retry path. An internal callback that cannot return an error records a warning and retains its pending work instead of reporting success. `SetModel`, `SetVisionModel`, enqueue and client-mutation methods already return errors: retain those signatures and propagate the sentinel. Ordinary terminal `Close()` remains unchanged.

Update the existing server steering callback fields, `serveServer` signatures and closures (`serve.go:64–65,927–933`) to the error-returning signatures above too; return the engine error from the closures. Keep the actual AppWire steering route on durable client-mutation acceptance, not a fallback to these callbacks. Update the named test callbacks/doubles in place, returning nil only after their intended successful fixture effect and asserting errors at invocation sites. This introduces no additional API surface or alternate success path.

- [ ] **Step 1: Write real durable acceptance red tests.** Add the following test and a second variant that accepts then calls `claimClientMutationStart` before trying retirement.

```go
func TestRetirementAcceptedStartBlocksBeforeRunnerWake(t *testing.T) {
    root := newQueuePersistTestSession(t, t.TempDir())
    defer root.Close()
    c, err := NewRetirementController(0, clock.Real())
    if err != nil { t.Fatal(err) }
    if err := c.AttachRoot(root); err != nil { t.Fatal(err) }
    p := appwire.TurnStartParams{
        ClientMutationID: "retirement-start-race",
        Input: []appwire.InputItem{{Type: "text", Text: "opaque-input"}},
    }
    response, err := root.AcceptClientMutationStart(p)
    if err != nil { t.Fatal(err) }
    claim, snapshot, err := c.TryClaim(true)
    if err != nil { t.Fatal(err) }
    if claim != nil { t.Fatal("retired accepted but unclaimed input") }
    if !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool {
        return b.Category == "input" && b.SessionID == root.ID()
    }) { t.Fatalf("missing input blocker: %+v", snapshot) }
    replay, err := root.AcceptClientMutationStart(p)
    if err != nil { t.Fatal(err) }
    if response.Receipt.Disposition != appwire.MutationDispositionApplied ||
        replay.Receipt.Disposition != appwire.MutationDispositionReplayed ||
        response.Turn.ID == "" || replay.Turn.ID != response.Turn.ID ||
        response.Receipt.TurnID != response.Turn.ID || replay.Receipt.TurnID != response.Turn.ID ||
        response.Receipt.ProjectionState != appwire.MutationProjectionPending ||
        replay.Receipt.ProjectionState != appwire.MutationProjectionPending {
        t.Fatalf("wrong acceptance/replay identity or receipt: %#v %#v", response, replay)
    }
    journal := root.clientMutations.snapshot()
    pending := journal.PendingExecutions[p.ClientMutationID]
    if pending.TurnID != response.Turn.ID || !reflect.DeepEqual(pending.Input, p.Input) ||
        len(journal.PendingExecutions) != 1 || journal.AcceptedTurns != 0 {
        t.Fatalf("replay duplicated or changed unclaimed input: %#v", journal)
    }
    // This test deliberately has no runner: one intent, zero executions before wake.
    // The paired scripted-provider test below proves one execution after settlement.
}
```

For claim-first, call `TryClaim(true)`, snapshot `root.clientMutations.snapshot()`, invoke each effect method, require the sentinel, then compare the snapshot **and original journal file bytes** unchanged. Get the journal filename from the existing persistence store path field, not a guessed suffix. For admission-first, use a filesystem write barrier at the existing mutation persistence boundary: pause before the first durable write, call `TryClaim`, assert immediate refusal, release the write, then cold-load the original journal and retry the same ID. Add one test each for queue, steer, drain, promote, cancel, interrupt and pre-turn claim. Extend existing mutation fixtures with barriers at the filesystem dependency, not by replacing acceptance code.

Add `TestRetirementEffectRefusalReportsError` using the same root/controller fixture: claim first, call `root.SetReasoningEffort("high")` and `root.Steer("opaque-input")`, require `errors.Is(err, ErrRetirementUnavailable)` for both and unchanged `root.cfg.ReasoningEffort`/steering queue. `TestRetirementReasoningRPCPropagatesRefusal` invokes the real server route and requires unavailable, never success. Update all production call sites in the named files and the existing test doubles implementing `serveServer`; search changed method names in tests and assert returned errors where they drive behavior. No `_ =` discard of a new lifecycle error is an acceptable caller update.

The replay oracle intentionally differs from whole-response equality: `session_client_mutation_test.go:437–443` independently requires `replayed`, while first acceptance is `applied` (`session_client_mutation.go:298–332`). Add `TestRetirementStartReplayExecutesOnce`: drive the existing queue fixture with a scripted provider, accept/replay before execution (both projections pending), release the provider/runner, await actual settlement, replay again (projection reflected), and assert exactly one transcript user-input entry with the original mutation ID/input, one stable turn and one provider execution. Multiple wake calls are permitted and are **not** executions.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirement(Accepted|Admission|Claim)' -count=1)` and `go test ./server ./internal/appserver -run 'Retirement' -count=1` — FAIL: accepted/claimed inputs currently permit the new controller claim; router has no admission hook.
- [ ] **Step 3: Implement effect/borrow coverage.** Use this exact wrapper before normalization that can touch stores, not after reserve:

```go
release, err := s.beginRetirementMutation("input")
if err != nil { return appwire.TurnStartResponse{}, err }
defer release()
```

Existing error-returning setters return the sentinel on refused admission. Change the existing void effect methods in place to the error-reporting signatures specified below, and update every caller; no second admitted API, no silent return on retirement refusal and no compatibility wrappers. RPC propagates the effect error, even when it also holds an outer lease. An admitted outer lease means nested entry cannot be refused by retirement: claims cannot start while its active count is nonzero. On async handoff, install the pending queue/claim before releasing the token; the turn acquires its token before consuming that blocker, and releases only after final settlement/autosave.

Classify **all current daemon methods** explicitly:

```text
read: thread/list, thread/read, thread/unsubscribe, thread/turns/list,
      evener/tasks/list, evener/jobs/list, evener/jobs/output, model/list
mutation: thread/clear, thread/model/set, evener/thread/name/set,
          thread/reasoning-effort/set, thread/vision-model/set,
          thread/compact/start, turn/start, turn/steer, turn/interrupt,
          turn/queue, turn/drainAsSteer, turn/promoteQueuedAsSteer,
          turn/cancelQueued, goal/set, evener/sandbox/escalation/resolve
control: thread/shutdown
```

Use the catalog constants in code, not these prose strings. Test iterates `appwire.CatalogMethodNames(appwire.ScopeDaemon)`, requiring exactly one classification for every method and no extra classification. New diagnostics/retire methods are added to this table in Task 7. Connection initialize/ping are not runtime borrowers. The `control` shutdown path retains existing semantics and is serialized by serve's exit owner, not counted as an activity reset. Bounded reads during preparing may complete; after commit new runtime reads fail. Existing subscriptions close when their event sources close.

Root predicate now reads `Session` processing/settlement/input fields under `Session.mu`, then journal snapshot under its own lock without `Session.mu`. Reserved, accepted, claimed and pending executions/budget reservations all block; terminal history alone does not. Do not use `WireState()` as the predicate.

- [ ] **Step 4: Run green.** `(cd agent && go test -race . -run 'TestRetirement|TestClientMutation|TestQueuePersist' -count=1)` and `go test -race ./server ./internal/appserver -run 'Retirement|Catalog|Mutation' -count=1` — PASS, unchanged replay IDs and no lock-order races. Test direct engine and actual routed calls, not only middleware.
- Run `go test ./cmd/evener -run 'Serve|Reasoning' -count=1` and `(cd agent && go test . -run 'Steer|Reasoning|RetirementEffect|RetirementStartReplay' -count=1)` to check caller/test-double signature changes. Preserve existing closed-session/empty/superseded behavior assertions; amend only expected error handling justified by this lifecycle contract.
- [ ] **Step 5: Commit named paths.**

```sh
git add agent/session_retirement_admission.go agent/session_retirement_admission_test.go agent/session_client_mutation.go agent/session_client_mutation_queue.go agent/session_queue.go agent/session_lifecycle.go agent/session.go agent/session_compaction.go agent/session_goal.go server/appwire_retirement_admission.go server/appwire_retirement_admission_test.go server/appwire_runtime.go server/server.go internal/appserver/router.go cmd/evener/serve.go cmd/evener/run.go agent/subagents.go agent/delegate_runtime.go agent/session_tool_round.go agent/session_jobtree_drain.go agent/session_self_compact.go agent/session_init.go
git add cmd/evener/serve_residual_fuzz_test.go server/reasoning_effort_test.go server/appwire_turns_paging_test.go server/appwire_reasoning_replay_test.go server/appwire_runtime_test.go server/server_surface_fuzz_test.go server/appwire_capabilities_push_test.go server/appwire_server_test.go
git commit -m "feat(agent): fence durable input and daemon resource borrowing"
```

### Task 3: Cover resident and cold delegate trees atomically

**Files:**
- Create: `agent/delegate_tree_retirement.go`
- Create: `agent/delegate_tree_retirement_test.go`
- Modify: `agent/delegate_tree_controller.go`, `agent/delegate_tree_reclaim.go`, `agent/delegate_tree_attention.go`, `agent/delegate_tree_watch.go`, `agent/delegate_runtime.go`, `agent/delegate_delivery.go`, `agent/subagents.go`, `agent/session_retirement_admission.go`
- Read: `agent/delegate_tree_reclaim_test.go`, `agent/internal/delegatestore/record.go`

**Interfaces:**
- Consumes: shared controller and `runtimeReclamationIntersectsProcessWorkLocked`.
- Produces: `func (c *delegateTreeController) retirementEvidence() ([]RetirementBlocker, []*Session, error)`; it returns leaf-first **exact** resident pointers plus blockers for all durable members, including cold ones. Add a controller retirement fence tied to the outer claim token; it does not change durable phases.
- Produces: `func (c *delegateTreeController) releaseRetiredRuntimes(exact map[string]*Session) error`, which only clears matching process-local pointers after release.

- [ ] **Step 1: Add a cold-member regression test.** Reuse `seedDelegateReclaimRuntime` from `agent/delegate_tree_reclaim_test.go:245` only for predicate-level tests; primary preservation uses real delegates in Task 6. Introduce this test helper in the new file: `newRetirementDelegateController(t *testing.T) (*Session, *delegateTreeController, *RetirementController)` constructs `newQueuePersistTestSession`, attaches the controller, and returns `root.delegateController`; fail if it is nil rather than fabricating a replacement controller.

```go
func TestRetirementColdDelegatePendingOutcomeBlocks(t *testing.T) {
    root, tree, c := newRetirementDelegateController(t)
    defer root.Close()
    seedDelegateReclaimRuntime(t, tree, "cold-pending", "", time.Unix(1, 0), false, false)
    tree.mu.Lock()
    tree.live["cold-pending"].runtime = nil
    tree.mu.Unlock()
    claim, state, err := c.TryClaim(true)
    if err != nil { t.Fatal(err) }
    if claim != nil { t.Fatal("cold pointer concealed pending durable outcome") }
    if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
        return b.Category == "delegate" && b.DelegateID == "cold-pending"
    }) { t.Fatalf("missing cold delegate blocker: %+v", state) }
}
```

Add barrier races using real create/send/cold-reconstruct entry points from `agent/delegate_runtime.go`: before reservation; after reservation before runtime install; during descendant-only send; during outcome acknowledgment; during attention/watch receipt settlement; during reclaim. Assert both winning orders, no duplicate child ID, no lost durable input and no deadlock. A cold member with missing descriptor/transcript, open run, pending delivery, recovery requirement or reserved work must block separately.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirement(Cold|Delegate)' -count=1)` — FAIL: root-only predicate misses cold and descendant obligations.
- [ ] **Step 3: Implement the exact-tree evidence algorithm.**

```text
Acquire outer admission before controller Reserve/Claim/work/receipt registration.
Under delegate controller mutex:
  enumerate all durable IDs, not live-runtime keys
  reject any open run or phase outside idle/closed
  reject pending deliveries, unacknowledged outcome obligations and recovery work
  reuse runtimeReclamationIntersectsProcessWorkLocked for the full member set
  inspect every live entry's binding/recovery/waiters/quiet claim/owed steering
  capture exact leaf-first resident pointers and evidenceVersion
Release controller mutex before reading Session fields, transcript or store files.
A preparing outer claim fences new starts; still revalidate evidenceVersion and
exact pointers before returning preparation. Never mark PhaseClosed or Resumable=false.
Existing reclamation remains capacity-based and may reclaim closed subtrees as before;
factor shared predicates, do not replace its semantics with retirement semantics.
```

Pass the controller pointer when constructing/restoring child configs, before the child can start maintenance or register input. The delegate tree's outer admission and its existing reservation maps overlap during handoff; there must never be a moment where neither owns the obligation. All release/Changed calls happen after controller lock release. Include the controller's own `closing`, `stop`, reclamations, reconciliation queue and root-level work in the full-set check; an empty member set is not evidence that root work is absent.

- [ ] **Step 4: Run green.** `(cd agent && go test -race . -run 'TestRetirement(Cold|Delegate)|TestDelegateRuntimeReclaim|TestDelegateTree' -count=1)` — PASS, existing reclamation tests unchanged.
- [ ] **Step 5: Commit.**

```sh
git add agent/delegate_tree_retirement.go agent/delegate_tree_retirement_test.go agent/delegate_tree_controller.go agent/delegate_tree_reclaim.go agent/delegate_tree_attention.go agent/delegate_tree_watch.go agent/delegate_runtime.go agent/delegate_delivery.go agent/subagents.go agent/session_retirement_admission.go
git commit -m "feat(agent): fence retirement across cold and resident delegates"
```

### Task 4: Complete jobs, watches, autonomous and environment blockers

**Files:**
- Create: `agent/session_retirement_evidence.go`
- Create: `agent/session_retirement_evidence_test.go`
- Modify: `agent/jobs.go`, `agent/job_watch.go`, `agent/session_attention.go`, `agent/session_jobtree_drain.go`, `agent/session_namer.go`, `agent/session_goal.go`, `agent/session_lifecycle.go`, `agent/session_env_swap.go`, `agent/session_tools_worktree.go`, `agent/session_worktree_sweep.go`, `agent/session_worktree_relock.go`, `agent/session_tools_ask.go`, `agent/session_escalation.go`, `agent/retirement.go`

**Interfaces:**
- Consumes: Tasks 1–3 leases and delegate evidence.
- Produces: `func (s *Session) retirementEvidence() ([]RetirementBlocker, error)`; `func (jm *jobManager) retirementEvidence(sessionID string) ([]RetirementBlocker, error)`; controller `TryClaim` calls the whole-tree collector. These methods do not mutate durable state.
- Adds a pending-work registration to every autonomous enqueue that otherwise has no represented blocker. Prefer existing counters/maps; add only the missing pre-enqueue ownership, not a second job state machine.

- [ ] **Step 1: Write the minimal direct environment blocker test.** `beginEnvWork`/`endEnvWork` are existing paired APIs; use the actual return handle.

```go
func TestRetirementEnvironmentLeaseBlocks(t *testing.T) {
    root := newQueuePersistTestSession(t, t.TempDir())
    defer root.Close()
    c, err := NewRetirementController(0, clock.Real())
    if err != nil { t.Fatal(err) }
    if err := c.AttachRoot(root); err != nil { t.Fatal(err) }
    work, ok := root.beginEnvWork("retirement-test")
    if !ok { t.Fatal("environment work was refused") }
    claim, state, err := c.TryClaim(true)
    root.endEnvWork(work)
    if err != nil { t.Fatal(err) }
    if claim != nil || !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
        return b.Category == "environment" || b.Category == "admission"
    }) { t.Fatalf("environment operation escaped: %+v", state) }
}
```

Build the remaining safety table by exercising real entry APIs with scripted provider tools, existing job/watch test fixtures and `agenttest.FakeClock`. Each row independently asserts a blocker and later eligibility after real settlement: provider/tool turn, pre-turn claim, compaction, cancellation settlement, each queue route, active goal, naming launch, maintenance/relock/sweep, attention retry, question, sandbox escalation, running shell/descendant shell, active event watch, one-shot timer, repeating timer, pending send, claimed watch delivery, receipt awaiting commit, delegate recovery, worktree create/swap/rollback/dispose, sandbox teardown, and unreadable persistent evidence. Do not satisfy the table by inserting `RetirementBlocker` values into the controller. Terminal shell records and settled idle delegates are separate positive cases. Test detached shell lifetime with a fixture-owned external process handle and assert no signal operation is invoked by retirement.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirement(Environment|Safety|Watch|Autonomous|Question)' -count=1)` — FAIL for currently unrepresented work; each table row must execute (named `Test` entry, not an unregistered fuzz check).
- [ ] **Step 3: Implement the collector and pre-enqueue leases.**

```text
For exact root and every resident child:
  snapshot Session state under its mutex: turn/settlement, input queues,
  running input, goal continuation, questions/escalations, envWork/dispose,
  outstanding naming/maintenance/reconstruction and attention work
  unlock; snapshot mutation, job/watch, task/attention and delegate owners separately
For job manager:
  nonterminal session-owned jobs block; terminal records do not
  active watches block regardless of root wire state
  pending sends/claims/receipts/retries/flush obligations block even if watch is inactive
Cold delegates:
  use durable tree evidence and validate required job/attention state; unknown => blocker
Sort/deduplicate IDs/categories; never include label arguments, prompts or tokens.
An autonomous scheduler must register a lease or durable blocker BEFORE enqueue;
its completion releases only after settlement writes. Fence timer callback launch
before it can perform work, and explicitly track pending work requiring settlement.
```

Avoid acquiring admission from inside `Session.mu` or `jobManager.mu`: split effect methods into admission → existing lock/mutation → unlock → release. `beginEnvWork` obtains admission before its current lock and stores the release in its work record until `endEnvWork`; detached rollback retains that record. If no runtime/durable contract can preserve a state, report `unsupported` and leave resident. Coalesce `Changed` on every blocker transition; reads never call it as an activity reset.

- [ ] **Step 4: Run green/race.** `(cd agent && go test -race . -run '^TestRetirement|Test.*Watch|Test.*EnvWork|Test.*Goal' -count=1)` — PASS. Before moving on, record concrete checked entry symbols from the admission inventory and verify no row lacks a race in both orders.
- [ ] **Step 5: Commit.**

```sh
git add agent/session_retirement_evidence.go agent/session_retirement_evidence_test.go agent/jobs.go agent/job_watch.go agent/session_attention.go agent/session_jobtree_drain.go agent/session_namer.go agent/session_goal.go agent/session_lifecycle.go agent/session_env_swap.go agent/session_tools_worktree.go agent/session_worktree_sweep.go agent/session_worktree_relock.go agent/session_tools_ask.go agent/session_escalation.go agent/retirement.go
git commit -m "feat(agent): prove whole-runtime retirement eligibility"
```

### Task 5: Prepare retirement with fallible persistence and reconstruction checks

**Files:**
- Create: `agent/session_retirement_prepare.go`
- Create: `agent/session_retirement_prepare_test.go`
- Create: `agent/session_scratch_retention.go`
- Create: `agent/session_scratch_retention_test.go`
- Create: `agent/sandbox/scratch_retention.go`
- Create: `agent/sandbox/scratch_retention_test.go`
- Create: `agent/execenv/scratch_retention.go`
- Create: `agent/execenv/scratch_retention_test.go`
- Modify: `agent/sandbox/session_scratch.go`, `agent/execenv/local.go`, `agent/session_init.go`, `agent/delegate_runtime.go`, `agent/session_env_swap.go`
- Modify: `agent/retirement.go`, `agent/session.go`, `agent/session_client_mutation_persist.go`, `agent/delegate_tree_retirement.go`
- Modify: `agent/internal/delegatestore/store.go`, `agent/internal/jobstore/store.go`
- Create: `agent/internal/delegatestore/retirement_test.go`
- Create: `agent/internal/jobstore/retirement_test.go`
- Read: `agent/transcript/transcript.go` (`Writer.EstablishDurability`, already available)
- Read: `agent/session_init.go`, `agent/delegate_runtime.go`, `agent/session_worktree_resume.go`, `agent/schema/snapshot.go`

**Interfaces:**
- Consumes: exact preparing claim, Tasks 2–4 predicate and Task 2's error-returning `saveMeta`.
- Produces: `Prepare(ctx, claim) (*RetirementPreparation, error)`; `func (s *Session) validateRetirementRestore(ctx context.Context) error`; `func (s *Store) CheckRetirementReady() error` on delegate and job stores. Their existing append paths already synchronize writes; the new method checks pending/sticky write errors and reloads the original store under its existing serialization boundary, without closing it or appending an event. It returns a persistence error on unreadable evidence instead of adding redundant hot-path fsyncs.
- `RetirementPreparation` contains controller and root/generation, exact leaf-first sessions, verified transcript/descriptor identities, occupied lane identity/lock ownership and one-use commit state. It is not a speculative restored runtime. No clone should acquire lanes, launch a namer or write a journal while validating.

- [ ] **Step 1: Write a real primary-file failure test.** This filesystem failure is fixture-owned and restores the original file before cleanup.

```go
func TestRetirementPreparationCorruptTranscriptStaysResident(t *testing.T) {
    root := newQueuePersistTestSession(t, t.TempDir())
    defer root.Close()
    c, err := NewRetirementController(0, clock.Real())
    if err != nil { t.Fatal(err) }
    if err := c.AttachRoot(root); err != nil { t.Fatal(err) }
    path := root.TranscriptPath()
    original, err := os.ReadFile(path)
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(path, []byte("not-json\n"), 0600); err != nil { t.Fatal(err) }
    t.Cleanup(func() {
        if err := os.WriteFile(path, original, 0600); err != nil { t.Error(err) }
    })
    claim, state, err := c.TryClaim(true)
    if err != nil { t.Fatal(err) }
    // Either the evidence read or preparation must refuse the corrupt original.
    if claim != nil {
        if _, err := c.Prepare(context.Background(), claim); err == nil {
            t.Fatal("preparation accepted corrupt original transcript")
        }
        if err := c.Abort(claim, "prepare_failed"); err != nil { t.Fatal(err) }
    } else if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
        return b.Category == "persistence"
    }) { t.Fatalf("corruption lacked persistence diagnostic: %+v", state) }
    if got := c.Snapshot().Phase; got != "resident" { t.Fatalf("phase = %s", got) }
    release, err := c.BeginMutation(root.ID(), "input")
    if err != nil { t.Fatalf("preparation failure closed admission: %v", err) }
    release()
}
```

Also force metadata write failure through existing `SessionConfig.testOnly.metaFS`; mutation/task/attention/delegate/job persistence failures through each store's filesystem boundary; missing child transcript, malformed descriptor, missing scratch artifact, root lacking state directory, dirty/clean occupied lane with wrong owner, and a cold delegate whose reconstruction evidence cannot be read. Assert no session-end/stop/disposal event or store close happened. Failure is not success merely because the daemon remains resident: require the `persistence` blocker or `prepare_failed` diagnostic.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirementPreparation' -count=1)` — FAIL: no fallible preparation exists; autosave currently only warns.
- [ ] **Step 3: Implement preparation and propagate errors.** Use Task 2's factored `saveMeta() error`; keep `maybeAutoSave` warning behavior for its existing callers. Retirement calls `saveMeta`, never `maybeAutoSave` as a success oracle.

```text
Prepare(exact claim):
  require preparing/current-root generation, no mutation leases
  collect full evidence and exact leaf-first tree; fail on any blocker
  for each runtime, with no Session.mu held over I/O:
    finish metadata/task/attention/mutation writes and surface store errors
    call transcript.Writer.EstablishDurability without closing or appending an end event
    read original meta, transcript header/entries and mutation records with production parsers
    check IDs, session/project/state paths and accepted input consistency
  for each durable delegate (cold members included):
    check committed descriptor, parent linkage, phase/resumability, latest outcome/acks
    check child transcript identity and persisted config/tasks/sandbox restore prerequisites
    verify required scratch/artifacts and occupied lanes still exist under exact ownership
  recheck controller evidenceVersion/exact pointers and current root generation
  return one-use preparation; no close, cancel, unlock, hook, sweep or lane creation yet
```

Factor pure validation from the existing restore path into `validateRetirementRestore`; it must share those validators rather than constructing a second session (construction launches process work and can modify state). Track sticky append/flush failures until a subsequent successful required write/read establishes reconstructibility. A corrupt primary record is not repaired from a stale memory snapshot merely to make retirement pass. Do not add fsync to unrelated ordinary hot paths without evidence; use existing store durability contracts and add only the missing final error propagation.

#### Required scratch: durable pins before lease release (Ledger C.2)

Baseline `SessionScratch.Retain` only releases `.evener-session.lock` (`agent/sandbox/session_scratch.go:72–81`); the actual startup sweep deletes old unlocked directories (`:227–254`). `LocalExecutionEnvironment` separately owns sandbox and unsandboxed scratch (`local.go:758–879`), and `AdoptSessionScratch` moves live handles, not durable restore evidence. Therefore neither `Retain` nor `os.Stat` proves preservation. Add one root-owned scratch manifest plus exact-directory retention pins; do not relocate existing absolute paths or disable the collector.

**New types/APIs in `agent/sandbox/scratch_retention.go`:**

```go
type ScratchOwner struct { StateDir, RootSessionID string }
type ScratchReference struct {
    Dir string               // canonical original absolute path, never a relocation
    Kind string              // sandbox or unsandboxed allocation; immutable
}
type ScratchSlot struct {
    Dir string               // references the immutable allocation above
    OwnsLease bool           // false for an existing wrapper-only borrow
}
type ScratchBinding struct {
    BindingID string         // persisted opaque logical environment identity
    OwnerSessionID string    // unchanged by sharing, cloning or moving an allocation
    WorkingDir string        // this environment's original cwd, not its owner's latest cwd
    Slots map[string]ScratchSlot // current slots by kind, within THIS binding
}
type ScratchConsumerBinding struct {
    SessionID string
    CurrentBindingID string
    ParentSharedBindingID string
    WorktreeRestoreBindingID string
    AbandonedBindingIDs []string
}
type ScratchManifest struct {
    Version int              // exactly 1
    Revision uint64          // serializes binding updates, not a second lifecycle version
    Owner ScratchOwner
    References []ScratchReference
    Bindings []ScratchBinding
    Consumers []ScratchConsumerBinding
    Released bool            // terminal-close/deletion tombstone, never set by retirement
}
func LoadScratchRetention(owner ScratchOwner) (ScratchManifest, error)
func (s *SessionScratch) Pin(owner ScratchOwner, ref ScratchReference) error
func UpdateScratchBindings(owner ScratchOwner, expectedRevision uint64,
    bindings []ScratchBinding, consumers []ScratchConsumerBinding) error
func OpenRetainedSessionScratch(owner ScratchOwner, ref ScratchReference) (*SessionScratch, error)
func ReleaseScratchRetention(owner ScratchOwner) error
```

Manifest path is `<StateDir>/scratch-retention/<RootSessionID>.json`; each pinned directory contains `.evener-retained-session.json` with version, exact root `ScratchOwner`, canonical directory and `Kind`. These directory identity fields are immutable. A pin contains **no environment binding, owning-session or current-consumer field**: an allocation moving between environments changes only manifest mappings, never retention authority or directory identity. All files are private, strict-JSON decoded, atomically replaced and fsynced with their containing directory before success. Serialize manifest read/modify/write with a stable root-owned lock file using the existing scratch lease OS locking primitive, never a `Session.mu` held over I/O. `Pin` requires a currently owned scratch lease; never wait for a live scratch lease while holding the manifest lock. Deduplicate references by canonical directory and reject conflicting root/kind identities rather than overwrite another root's pin.

`Pin` writes and synchronizes the directory pin **first**, then the root manifest reference, while still owning the live lease. Only after the reference and its applicable binding update are durable may a new scratch path be exposed to a turn or any retain/reclamation path release that lease. An interrupted write or unreadable/conflicting pin is an error and a preparation blocker. A valid pin with missing/incomplete manifest is conservatively retained with a diagnostic, not treated as safely reconstructible. Finish fallible allocation/wrapper construction before publishing its slot. A failed never-exposed allocation rolls back only its exact newly created pin/reference while still owning the lease and preserves the previous slot; never clear preexisting/exposed pins on abort. Cold/historical references remain even when no binding currently owns their lease; this narrowly scoped retention does not disable collection elsewhere.

**Binding invariant (round-2 correction):** there is at most one current allocation per **`(BindingID,Kind)`**, not per owning session. Multiple environments owned by root R may have different current directories simultaneously. Persist a new opaque binding ID when a distinct owned environment is constructed, including `WithWorkingDirectory`; retain that ID on backswap/reuse. A child sharing the same environment object uses the same ID, not a fabricated child-owned environment. Invocation-only overlays borrowing an existing environment retain its binding rather than creating durable topology. Clone creation does not move a scratch handle: a re-rooted wrapper can borrow the source allocation (`OwnsLease:false`) while the new environment owns none. `AdoptSessionScratch` alone moves owned handles; preserve the `parentSharedEnv` exemptions in `swapEnvAndRefresh`. A directory has at most one lease-owning binding, but may have wrapper borrowers and many session consumers. Working directory and owner identity alone never identify a binding.

`UpdateScratchBindings` replaces only the supplied binding/consumer records in one fsynced manifest transaction, preserving other bindings/references. Under the manifest lock compare `expectedRevision`, validate all slot references and ownership uniqueness, and increment revision; stale revision returns an error without writing. Reload and rebase the **specific observed allocation transition**, not a stale whole-environment snapshot: moving A out of E0 must not erase a B concurrently minted there. All manifest writers, including Pin/release, advance revision. For R/C sharing E0/A, root's E1 clone and adoption move A's owning slot to E1, while C stays on E0; C's subsequent B allocation gives E0/B and E1/A two legitimate current slots owned by R. On backswap, occupied E0 keeps B; incoming A loses only its lease-owning slot and remains a pinned reference (and any real wrapper borrow), exactly as baseline Retain requires. Persist that transition before releasing A's lease. Do not rename R's ownership to C, move consumers with the handle, or mark E0/B historical because R moved. Existing admission/env-work tracking spans updates; failed persistence remains a specific error/blocker with unreleased handles retained, never a successful retirement or a blanket ban on shared children. Preserve the source-mint concurrency window, with identity/revision-checked updates rather than a new moving flag that prevents its supported outcome.

**Allocation/cold coverage, new execenv APIs:** replace the session-keyed setup with `func (e *LocalExecutionEnvironment) SetScratchRetentionBinding(owner sandbox.ScratchOwner, binding sandbox.ScratchBinding) error`; add `func (e *LocalExecutionEnvironment) ScratchRetentionBinding() (sandbox.ScratchBinding,error)`. Keep `ScratchRetentionReferences() ([]sandbox.ScratchReference,error)` for all retained dependencies. `func (e *LocalExecutionEnvironment) RestoreSessionScratch(bindingID string, ref sandbox.ScratchReference, scratch *sandbox.SessionScratch) error` installs only the validated owning slot of that exact binding; a wrapper borrow points at the same retained directory without duplicating its lease. Register root/child current, `parentSharedEnv`, `worktreeRestoreEnv` and `abandonedEnvs` roles in `ScratchConsumerBinding` before publication, using the existing environment-swap serialization without I/O under `Session.mu`. All of this is in the already-owned `session_scratch_retention.go`, `execenv/scratch_retention.go`, `local.go`, `session_init.go`, `delegate_runtime.go` and `session_env_swap.go`; no new descriptor schema or files are needed. Join each cold descriptor's existing `ChildSessionID` to the manifest's `Consumers.SessionID`, validate its existing owner/parent/config evidence, then resolve the exact binding ID and cwd; never infer a shared child's binding from its parent's latest environment. Preserve the mapping across cold reclamation and restore parked bindings even when only a cold consumer references them. Root retention authority and terminal/deletion release remain unchanged; every exposed allocation stays a dependency until that root's terminal close/deletion.

**Collector:** after acquiring a candidate's existing lease, strictly read its retention pin and matching manifest. Matching committed reference with `Released:false` means skip regardless of age. A matching `Released:true` tombstone authorizes ordinary age-based collection for that owner's directory; it does not permit deletion outside the existing namespace/base checks. Malformed/unreadable/orphaned pin means skip **that directory** and return a bounded diagnostic; it does not count as preparation/restore success. No pin means retain the existing age-based cleanup behavior. Hold the acquired lease through the retention check **and removal**, releasing afterward; baseline releases before removal, which would race a same-path restore. Neither allocation nor restore may accept a changed directory/lease inode: verify the canonical prefix-owned directory is still the same after acquiring its lease. Never recursively delete a path supplied only by a pin; removal remains constrained by the allocator namespace/base checks in `SessionScratch.Cleanup`/the existing collector.

**Restore:** new `func (s *Session) prepareRetainedScratch() error` loads/validates the manifest and reacquires **all** referenced leases before root/child initialization launches work. Keep handles in a root-owned pool keyed by canonical path, and reconstruct each persisted logical environment once in a registry keyed by binding ID. Use the binding's original `WorkingDir` and existing validated config/sandbox restore inputs, re-rooting with the established environment logic; do not substitute the owning root's current cwd. New `func (s *Session) adoptRetainedScratch(env *execenv.LocalExecutionEnvironment, bindingID string) error` transfers that binding's owning slots from the pool, installs wrapper-only borrows without duplicate lease ownership, and verifies cwd/binding identity. Resolve every consumer's current/shared/restore/abandoned role through the same registry: root R can resume on E1/A while cold C resumes on E0/B, and R's restore-env pointer can still be that same E0 object. Shared consumers of one binding reuse one environment, not fresh scratch. `RestoreSessionScratch` rebuilds the wrapper with existing `sandbox.NewWrapper`/resolved policy and invalidates file-tool roots like `AdoptSessionScratch`; it refuses mismatched destinations or replacement of exposed fresh scratch. Wire this before `prepareSubagentEnvironment`/NewSession snapshots can allocate (`delegate_runtime.go:1966–1977`), and root bootstrap before tools/naming. The pool keeps historical or not-yet-reconstructed handles until adoption/release. No manifest is expected for an old session that never used this feature; independently unresolved required scratch blocks retirement, but multiple current bindings under one root are valid. Missing bytes, contradictory mappings or identity/permission failures block rather than minting replacement directories.

**Tests before implementation:** add `TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact`, `TestScratchRetentionUnreferencedReleasedStillCollected`, `TestScratchRetentionPinFailurePreventsLeaseRelease`, `TestScratchRetentionRestoreSweepBothOrders`, `TestScratchRetentionConflictingOrUnreadablePin`, `TestRetirementColdDelegateScratchManifest`, and `TestRetirementAgedScratchRestoresAtOriginalPath`. The first actual collector test can begin with:

```go
func TestScratchRetentionStartupSweepKeepsAgedRequiredArtifact(t *testing.T) {
    base, workspace := t.TempDir(), t.TempDir()
    oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
    sessionScratchTempDir = func() string { return base }
    sessionScratchUserCacheDir = func() (string,error) { return base,nil }
    t.Cleanup(func() { sessionScratchTempDir, sessionScratchUserCacheDir = oldTemp, oldCache })
    owner := ScratchOwner{StateDir:t.TempDir(), RootSessionID:identifier.MustNewSessionID()}
    scratch, err := NewSessionScratch(base, workspace)
    if err != nil { t.Fatal(err) }
    ref := ScratchReference{Dir:scratch.Dir, Kind:"unsandboxed"} // artifact-only dependency; binding topology tested below
    artifact := filepath.Join(scratch.Dir, "required.bin")
    want := []byte("opaque-required-artifact")
    if err := os.WriteFile(artifact, want, 0600); err != nil { t.Fatal(err) }
    if err := scratch.Pin(owner, ref); err != nil { t.Fatal(err) }
    if err := scratch.Retain(); err != nil { t.Fatal(err) }
    aged := time.Now().Add(-2 * crashedSessionScratchMaxAge)
    if err := os.Chtimes(scratch.Dir, aged, aged); err != nil { t.Fatal(err) }
    if err := SweepCrashedSessionScratch(workspace); err != nil { t.Fatal(err) }
    restored, err := OpenRetainedSessionScratch(owner, ref)
    if err != nil { t.Fatal(err) }
    got, err := os.ReadFile(artifact)
    if err != nil || !bytes.Equal(got,want) || restored.Dir != scratch.Dir {
        t.Fatalf("original required artifact lost: bytes=%q dir=%s err=%v",got,restored.Dir,err)
    }
    if err := restored.Retain(); err != nil { t.Fatal(err) }
    if err := ReleaseScratchRetention(owner); err != nil { t.Fatal(err) }
    if err := os.Chtimes(scratch.Dir, aged, aged); err != nil { t.Fatal(err) }
    if err := SweepCrashedSessionScratch(workspace); err != nil { t.Fatal(err) }
    if _,err := os.Stat(artifact); !os.IsNotExist(err) { t.Fatalf("unreferenced scratch not collected: %v",err) }
}
```

`identifier.MustNewSessionID` exists in `identifier/domains.go:50`. Preserve `TestSessionScratchSweepRemovesOldReleasedLease`, `TestSessionScratchSweepSkipsOldLiveLease` and `TestSessionScratchAgeSweepsOnlyStaleEvenerDirs` unchanged. The agent-level aged test additionally restores a fresh root and sends to the same cold delegate, reads the artifact through that child's real environment at its original absolute path, and checks content/config/tasks/sandbox. A sandbox-only `Stat` assertion cannot substitute for this restore proof.

Add `TestScratchRetentionBindingMoveConcurrentMint` in the already-planned execenv scratch-retention test file: use the existing `scratchMovedOut` barrier to mint B in the source while A moves, then load the original manifest and require distinct E0/B and E1/A current slots with unchanged root/owner identities. Force stale revision retry and persistence failure; require no overwritten fresh slot, no lease release before its committed reference/mapping, and a specific preparation error if the mapping remains unsettled. Keep the independent `TestAdoptSessionScratchKeepsWhatTheTargetOwnsAndRetainsTheIncoming` in `agent/execenv/sandbox_scratch_lease_unix_test.go:162–186` **unchanged**. It and `local.go:817–855,935–973` establish the supported dual-allocation/clone topology; Task 6 adds the real-session cold-restore proof, rather than treating these environment-only tests as sufficient.

- [ ] **Step 4: Run green.** `(cd agent && go test -race . ./internal/delegatestore ./internal/jobstore -run 'Retirement' -count=1)` — PASS. In each new store test file add `TestRetirementReadyReadsOriginal`: open a fixture store through its existing `Open` constructor, assert readiness, replace original journal bytes with malformed JSON, require readiness error, restore bytes and close. Add sticky write/rollback-error cases using its existing filesystem fault seam. These tests must read primary bytes, not the cached `Load` fold; check delegates via strict event load and jobs via `LoadEvents` after invalidating read cursor. Verify failure occurs before the first teardown seam and existing normal autosave tests still pass.
- Run red before the scratch APIs, then green: `(cd agent && go test -race ./sandbox ./execenv . -run 'TestScratchRetention|TestRetirement.*Scratch|TestSessionScratchAge|TestSessionScratchSweep|TestAdoptSessionScratchKeepsWhatTheTargetOwnsAndRetainsTheIncoming' -count=1)`. Expected initial failure is missing durable-pin API, lost binding or removed aged required bytes; green requires actual startup sweep plus fresh same-ID restore, and unchanged dual-allocation/unreferenced cleanup.
- [ ] **Step 5: Commit.**

```sh
git add agent/session_retirement_prepare.go agent/session_retirement_prepare_test.go agent/retirement.go agent/session.go agent/session_client_mutation_persist.go agent/delegate_tree_retirement.go agent/internal/delegatestore/store.go agent/internal/jobstore/store.go agent/internal/delegatestore/retirement_test.go agent/internal/jobstore/retirement_test.go agent/session_scratch_retention.go agent/session_scratch_retention_test.go agent/sandbox/scratch_retention.go agent/sandbox/scratch_retention_test.go agent/execenv/scratch_retention.go agent/execenv/scratch_retention_test.go agent/sandbox/session_scratch.go agent/execenv/local.go agent/session_init.go agent/delegate_runtime.go agent/session_env_swap.go
git commit -m "feat(agent): validate durable reconstruction before retirement"
```

### Task 6: Release runtimes non-terminally and prove nested cold restore

**Files:**
- Create: `agent/session_retirement_release.go`
- Create: `agent/session_retirement_preservation_test.go`
- Modify: `agent/session_lifecycle.go`, `agent/jobs.go`, `agent/subagents.go`, `agent/session_worktree_close.go`, `agent/session_worktree_resume.go`, `agent/delegate_runtime.go`, `agent/delegate_tree_retirement.go`
- Modify: `agent/session_scratch_retention.go`, `agent/sandbox/scratch_retention.go`, `cmd/evener-hub/project_delete.go`
- Create: `cmd/evener-hub/scratch_retention_delete_test.go`
- Read: `agent/session_worktree_sweep_test.go`, `cmd/evener-hub/app_session_delete.go`
- Read: `agent/session_tools_worktree_create_test.go`, `agent/delegate_tree_reclaim_test.go`, `agent/execenv/sandbox_scratch_lease_unix_test.go`

**Interfaces:**
- Consumes: Task 5 preparation; existing `retainChildScratch`, close budget and lane ownership mechanisms.
- Produces: `ReleaseForRetirement(ctx, prepared) error`; private `type runtimeReleasePolicy uint8` with `releaseTerminal` and `releaseRetirement`; `func (s *Session) releaseRuntime(ctx context.Context, cleanupEnv bool, policy runtimeReleasePolicy) error`. Existing `Close` delegates with `releaseTerminal` and retains its void/logging contract. Return cleanup errors to the retirement caller.
- Adds `func (jm *jobManager) releaseQuiescentRuntime() error`: assert no runtime obligations, stop process-local idle infrastructure and close store without cancelling jobs/dropping watches.
- Consumes Task 5's root manifest/pins. `ReleaseScratchRetention(owner)` is called only after existing terminal root stop/close has committed, or by `cleanupProjectDeletionTargetAndDecisions` under its existing verified deletion ownership before state purge. It atomically/fsyncs `Released:true`, then removes matching per-directory pins under each available lease; a live lease leaves the tombstone/pin for the collector to finish. Keep the manifest/tombstone until all matching pins are removed so interruption cannot expose a false missing-manifest state. Explicit deletion must complete or conservatively retain/record failed pin cleanup before purging that manifest. Child-only deletion cannot release a surviving root's manifest. Ordinary force stop, retirement, archive, cold reclamation and resume never set Released. No new stop semantics or alternate terminal Close API.

- [ ] **Step 1: Write primary-record preservation before teardown code.** Define these helpers in the new test file; they are new fixture utilities, not fake lifecycle implementations:

```go
type retirementPrimaryFiles map[string][]byte

func readRetirementPrimaryFiles(t *testing.T, stateDir string) retirementPrimaryFiles {
    t.Helper()
    files := retirementPrimaryFiles{}
    err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
        if err != nil { return err }
        if d.IsDir() { return nil }
        // Capture primary stores and required artifacts, not lock/socket files.
        rel, err := filepath.Rel(stateDir, path)
        if err != nil { return err }
        if strings.HasSuffix(rel, ".lock") || d.Type()&os.ModeSocket != 0 { return nil }
        data, err := os.ReadFile(path)
        if err != nil { return err }
        files[rel] = data
        return nil
    })
    if err != nil { t.Fatal(err) }
    return files
}

func TestRetirementReleasePreservesRootTranscript(t *testing.T) {
    dir := t.TempDir()
    root := newQueuePersistTestSession(t, dir)
    id := root.ID()
    c, err := NewRetirementController(0, clock.Real())
    if err != nil { t.Fatal(err) }
    if err := c.AttachRoot(root); err != nil { t.Fatal(err) }
    claim, _, err := c.TryClaim(true)
    if err != nil || claim == nil { t.Fatalf("claim: %v %v", claim, err) }
    prepared, err := c.Prepare(context.Background(), claim)
    if err != nil { t.Fatal(err) }
    before, err := os.ReadFile(root.TranscriptPath())
    if err != nil { t.Fatal(err) }
    if err := c.Commit(claim); err != nil { t.Fatal(err) }
    if err := c.DrainReaders(context.Background()); err != nil { t.Fatal(err) }
    if err := root.ReleaseForRetirement(context.Background(), prepared); err != nil { t.Fatal(err) }
    after, err := os.ReadFile(root.TranscriptPath())
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(before, after) { t.Fatal("retirement appended terminal transcript evidence") }
    restored := restoreQueuePersistTestSession(t, dir, id)
    defer restored.Close()
    if restored.ID() != id { t.Fatalf("restored another root: %s", restored.ID()) }
}
```

Use `readRetirementPrimaryFiles` **before preparation and after retirement** in the comprehensive fixture: parse records with independent production readers to assert semantic fields rather than blindly demanding metadata bytes remain unchanged after a legitimate final save. For transcript, mutation journal, delegate events/outcomes/acks, job terminal records, tasks and artifact bytes require exact preservation when no outstanding write is expected. Reject any new stop/cancel/watch-drop/disposal event. The simple test above takes its transcript snapshot after preparation only to isolate release; it does not replace the full before/after oracle.

**Nested fixture specification:** `newRetirementPreservationFixture(t *testing.T) *retirementPreservationFixture` (new helper in this file) creates a real repository with `newWorktreeRepo`, a persisted root with the repository environment, and a scripted `llm.ProviderAdapter` that creates a delegate, has that delegate create another, and ends each through the real communicate/result path. Drive actual delegate create/send APIs already exercised in `agent/delegate_tree_reclaim_test.go`; retain returned stable IDs, acknowledge outcomes through the real parent commit, and await settlement channels. Create one clean and one tracked-dirty occupied lane using real worktree tools. Seed tasks/config/sandbox through their real setters; create a required scratch artifact through the child's environment. The struct holds `root *Session`, `rootID string`, `delegateIDs []string`, `stateDir string`, `lanePaths []string`, and `artifactPaths []string`; no fake tree controller, no mock release.

`(*retirementPreservationFixture).assertRestored(t *testing.T)` loads original root metadata, calls `RestoreSessionFromMetaWithConfig`, sends to each recorded delegate ID using the existing cold send path, and asserts the same child/session/parent IDs, model/config/tasks/sandbox and lane branch/ownership. Between retirement and restore create an independent foreign root and run the real P3/prune sweep past grace, then assert every original occupancy marker and required clean/dirty lane remains. Read original git registry (`git worktree list --porcelain`), refs, HEAD, `git status --porcelain=v1` and tracked bytes before/after; compare with the same independent reference for every named property. Verify durable git markers never disappeared across retirement, the intervening foreign sweep, or restore; separately verify process-local ownership was released/reacquired. Clean lanes must still exist. Terminal cleanup of this fixture runs only after all assertions.

Add `TestRetirementSharedChildScratchBindingsRestore` to this same preservation test file, using the real scripted root/delegate/worktree fixture, with sandbox and unsandboxed cases. Start root R and child C sharing the same E0 object and scratch A; write distinct artifact bytes. Move R into a real worktree through its normal tool path, creating E1 and adopting A. Send real work to C on E0 so it mints unsandboxed B; for the sandbox case, first use the real `EnableSandbox` reprovisioning exercised by the independent dual-allocation test, then run C's command (do not assume a wrapper-only command itself allocates a new owned sandbox scratch). Record **both exact current paths**, both environments' cwd, original artifact bytes and sharing relationships directly from the live environments/command results, independently of the new manifest. Require E1/A for R and E0/B for C, both bindings still owned by R. Settle/ack C, retire through real prepare/commit/release, age both directories and run the actual startup sweep, then restore a fresh root and cold-send to the same C. Compare both current paths/cwds and both artifact byte sets with the original live reference; require two binding identities, not two invented owners, and no child rebinding to R's worktree. Verify R's restored `worktreeRestoreEnv` and C's environment are the same reconstructed E0 object.

Then exercise the real root backswap to E0 while C remains there: E0 keeps B, incoming A is retained, and R/C again share E0/B. Require unchanged original A/B bytes, A's released live lease and retained immutable pin/reference, no second environment for the shared borrower, and unchanged root retention authority. Retire and age/sweep/restore again: both current consumers must still resolve to E0/B and historical A must remain readable at its original absolute path. Compare with the same original references at both checkpoints; neither `Stat` alone nor reading expected paths from the manifest is an oracle. The test must not change the baseline dual-allocation assertion, skip a valid shared-child tree as unsupported, or remove any existing aged/collector/concurrency tests. Terminal fixture cleanup occurs only after these assertions.

- [ ] **Step 2: Run red.** `(cd agent && go test . -run '^TestRetirement(Release|Preservation|Nested|SharedChildScratch)' -count=1)` — FAIL: API absent; using ordinary `Close` demonstrably changes transcript/delegate phase or removes clean lanes; a session-keyed scratch mapping loses the distinct current environments.
- [ ] **Step 3: Factor policy, do not copy the whole close function.**

```text
releaseTerminal: preserve every existing stop/cancel/disposal/hook/end-event branch.
releaseRetirement:
  require exact committed preparation (otherwise error before any side effect)
  close new admission/read borrowing first; reader drain is caller-owned
  mark process-local runtime unusable without persisting SessionClosed
  stop idle maintenance timers and join process-local goroutines within existing budget
  release exact child runtimes leaf-first with retainChildScratch
  do NOT closeOwnedDelegateRuntimeTree, abort delivery commits, cancel escalations,
  closeRuntimeState, disposeDelegateLanesAtClose, disposeLaneResidueAtClose,
  run SessionEnd hook, emit session_closed, or sweep foreign worktrees
  close quiescent job/runtime handles, inactive MCP, transcript/store/artifact handles
  preserve every durable git occupancy marker on occupied root/delegate lanes unchanged
  release only process-local locks/handles; do NOT call unlockOwnManagedWorktreeAtClose
  verify Task 5's committed manifest/pins; release live scratch leases including cold pool
  retain Released:false; do not Cleanup required directories or remove durable pins
  clear exact child bindings only; no durable stop, disposal or outcome rewrite
  errors propagate; never re-open or execute fallback terminal Close after partial release
```

Only factor common ordered cleanup portions from `session_lifecycle.go`; terminal-only portions stay guarded in place. `closeOnce`/terminal ownership must not cause a later deferred `Close` to execute a destructive second pass after retirement. Nonterminal release consumes the same process-local release owner but not the durable terminal event. Retaining scratch is not simply skipping all cleanup: close the retaining scratch lease and inactive runtime resources through existing owners. Environment cleanup must not signal detached processes. Root restore adopts its existing own marker through `EvResumeReenter`/`ActAdopt` (`session_worktree_resume.go:154–179`); delegate restore must likewise verify/adopt its recorded marker rather than unlock/relock a required lane. Release process-local locks only. Failed marker verification blocks restore rather than creating another lane.

Add `TestRetirementForeignSweepPreservesOccupiedLanes`: extend the real-git nested fixture with a **different root session** on the same repo; age lane sidecars beyond grace, retire the original, call the foreign root's existing `runLaneResidueSweep(context.Background())`, then restore/send to original IDs. Independently compare the original git occupancy markers, refs/registry, clean and tracked-dirty bytes before and after the foreign sweep. Seed an additional unlocked merged foreign lane and assert the same pass collects that lane, proving collection was not globally disabled. Keep `TestP3Sweep_CollectsUnlockedMergedForeignLanePastGrace`, `TestP3Sweep_UnchangedForeignLaneCollected`, `TestP3Sweep_LockedLaneSkipped` and all terminal-residue assertions unchanged. Add `TestScratchRetentionTerminalReleaseAllowsCollection` and `TestScratchRetentionDeleteReleasesOnlyTargetRoot` to prove normal terminal/unreferenced cleanup and deletion sibling isolation remain intact.

Inject a transcript/store close failure **after Commit**, assert `retiring` and failed diagnostics with no `Abort` path; readers/drain timeout also cannot reopen admission. A failed preparation still passes the separate resident test from Task 5. Preserve ordinary shutdown tests without changing assertions.

- [ ] **Step 4: Run green/race.** `(cd agent && go test -race . ./sandbox ./execenv -run 'TestRetirement|TestDelegateRuntimeReclaim|Test.*Close|Test.*Worktree.*Resume|TestP3Sweep_|TestSessionScratchSweep|TestScratchRetention|TestAdoptSessionScratchKeepsWhatTheTargetOwnsAndRetainsTheIncoming' -count=1)` — PASS including real-git nested preservation and both shared-child binding/rootswap checkpoints. Do not proceed to default activation without the primary-record and same-ID send evidence.
- Run `go test -race ./cmd/evener-hub -run 'ScratchRetentionDelete|SessionDelete|ProjectDelete' -count=1` for the existing deletion-owned cleanup boundary, without changing deletion admission/fences.
- [ ] **Step 5: Commit.**

```sh
git add agent/session_retirement_release.go agent/session_retirement_preservation_test.go agent/session_lifecycle.go agent/jobs.go agent/subagents.go agent/session_worktree_close.go agent/session_worktree_resume.go agent/delegate_runtime.go agent/delegate_tree_retirement.go agent/session_scratch_retention.go agent/sandbox/scratch_retention.go cmd/evener-hub/project_delete.go cmd/evener-hub/scratch_retention_delete_test.go
git commit -m "feat(agent): release idle runtimes without terminating durable sessions"
```

### Task 7: Define lifecycle wire contracts and bind safe daemon retirement

**Files:**
- Create: `appwire/daemon.go`
- Create: `appwire/daemon_test.go`
- Create: `server/appwire_daemon.go`
- Create: `server/appwire_daemon_test.go`
- Create: `rendezvous/identity.go`
- Create: `rendezvous/identity_test.go`
- Modify: `appwire/types.go`, `appwire/protocol.go`, `server/server.go`, `server/appwire_runtime.go`, `server/appwire_retirement_admission.go`
- Read: `cmd/evener-hub/app_rpc.go` (Hub handlers are owned by Task 10; do not land unimplemented router handlers)
- Modify: `cmd/evener-hub/internal/hubcore/roster.go`, `cmd/evener-hub/internal/hubcore/prober.go`
- Modify (generated): `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`
- Read: `appwire/doc.go`, `appwire/flagday_contract_test.go`, `appwire/client.go`, `cmd/evener-hub/frontend/src/protocol/client.ts`

**Interfaces:**
- Consumes: `RetirementSnapshot`, `TryClaim/Prepare/Commit/ReleaseForRetirement` and admission sentinel.
- Produces shared `func rendezvous.OwnershipFingerprint(entry Entry) string`, consumed by both the daemon identity check and Hub `daemonIdentity`. Hash canonical non-secret identity fields using SHA-256; preserve exact timestamp instant via UTC/RFC3339Nano and include PID, address/endpoint, protocol, source/thread/session/instance IDs, workspace/state/working-directory identity. Exclude HubToken and model/prompt/settings. Test identity changes and token nondisclosure directly in `rendezvous/identity_test.go`.
- Produces the following typed contracts (timestamps are RFC3339 strings, durations integer milliseconds; explicitly name units):

```go
type DaemonIdentity struct {
    Ref string `json:"ref"`
    PID int `json:"pid"`
    StartedAt string `json:"startedAt"`
    Generation string `json:"generation"` // opaque, non-secret fingerprint of exact ownership
}
type DaemonBlocker struct {
    Category string `json:"category"`
    SessionID string `json:"sessionId,omitempty"`
    DelegateID string `json:"delegateId,omitempty"`
}
type DaemonLifecycle struct {
    Phase string `json:"phase"`
    TimeoutMillis int64 `json:"timeoutMillis"`
    EligibleSince string `json:"eligibleSince,omitempty"`
    Deadline string `json:"deadline,omitempty"`
    Blockers []DaemonBlocker `json:"blockers"`
    Failure string `json:"failure,omitempty"`
}
type DaemonResident struct {
    Identity DaemonIdentity `json:"identity"`
    Name string `json:"name"`
    Protocol string `json:"protocol"`
    Compatibility string `json:"compatibility"` // compatible, incompatible, unknown
    Archived bool `json:"archived"`
    ProbeState string `json:"probeState"` // current, stale, unknown
    Lifecycle *DaemonLifecycle `json:"lifecycle,omitempty"`
    CanRetire bool `json:"canRetire"`
    CanForceStop bool `json:"canForceStop"`
}
type DaemonListParams struct{}
type DaemonListResponse struct {
    DefaultTimeoutMillis int64 `json:"defaultTimeoutMillis"`
    Daemons []DaemonResident `json:"daemons"`
}
type DaemonRetireParams struct { Identity DaemonIdentity `json:"identity"` }
type DaemonRetireResponse struct {
    Accepted bool `json:"accepted"`
    Lifecycle DaemonLifecycle `json:"lifecycle"`
}
type DaemonStatusParams struct{}
type DaemonStatusResponse struct { Lifecycle DaemonLifecycle `json:"lifecycle"` }
```

Methods: `MethodEvenerDaemonList = "evener/daemon/list"` (Hub), `MethodEvenerDaemonRetire = "evener/daemon/retire"` (both), `MethodEvenerDaemonStatus = "evener/daemon/status"` (daemon). Status is the explicit typed input to the existing prober; avoid abusing thread status for process lifetime. Add optional `ExpectedDaemon *DaemonIdentity` to `ThreadForceStopParams`, required by resident UI, preserving existing ref-only callers. No arbitrary PID-only endpoint.

Add typed Go methods in `appwire/daemon.go`: `func (c *Client) DaemonList(ctx context.Context, params DaemonListParams) (DaemonListResponse,error)`, `DaemonRetire(ctx context.Context, params DaemonRetireParams) (DaemonRetireResponse,error)`, and `DaemonStatus(ctx context.Context, params DaemonStatusParams) (DaemonStatusResponse,error)`. Each allocates the corresponding response and calls existing `c.Request(ctx, MethodEvenerDaemonList, params, &out)` with its matching method constant; return `out, err`. Typed TypeScript request maps come from the catalog generator, not new hand-written transport methods.

**Scheduling dependency:** Task 7 registers daemon status and retire with `ScopeDaemon`, and defines Hub list as `ScopeUnimplemented` until its real handler lands in Task 10. Task 10 changes retire to `ScopeBoth` and list to `ScopeHub`, then reruns catalog/codegen gates. This intermediate catalog reflects actual implementation availability, not a shipped partial feature. Never put a successful empty/stub response on a live router. Final catalog must have the scopes above.

- [ ] **Step 1: Write typed wire tests.** The protocol unit test exercises structured values, not prose:

```go
func TestDaemonLifecycleZeroAndUnknownAreDistinct(t *testing.T) {
    row := DaemonResident{Lifecycle: &DaemonLifecycle{
        Phase: "resident", TimeoutMillis: 0, Blockers: []DaemonBlocker{},
    }}
    raw, err := json.Marshal(row)
    if err != nil { t.Fatal(err) }
    var decoded DaemonResident
    if err := json.Unmarshal(raw, &decoded); err != nil { t.Fatal(err) }
    if decoded.Lifecycle == nil || decoded.Lifecycle.TimeoutMillis != 0 {
        t.Fatalf("disabled became unknown: %s", raw)
    }
    if decoded.Lifecycle.Deadline != "" { t.Fatalf("disabled deadline: %s", raw) }
    row.Lifecycle = nil
    raw, err = json.Marshal(row)
    if err != nil { t.Fatal(err) }
    decoded = DaemonResident{}
    if err := json.Unmarshal(raw, &decoded); err != nil { t.Fatal(err) }
    if decoded.Lifecycle != nil { t.Fatal("unknown became enabled/disabled") }
}
```

Add actual server request tests: status does not reset interval; manual retire with timeout zero succeeds only through the claim; pending question returns `Accepted:false` plus fresh blockers; raced mutation returns existing `CodeUnavailable` with typed `data.lifecycleReason = "retiring"` or `"preparing"` and `data.retryable = true` **before durable acceptance**. Lost retire response is not proof of exit. Older/incompatible peers never get a legacy fallback.

- [ ] **Step 2: Run red.** `go test ./appwire ./server -run 'Daemon|Retirement|Catalog' -count=1` — FAIL undefined contract or catalog mismatch.
- [ ] **Step 3: Implement typed hooks and errors.** Add `Server.SetDaemonLifecycle(status func() appwire.DaemonLifecycle, retire func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error))`. Handlers copy hooks under server mutex, invoke without it. Status is a detached snapshot and need not borrow session handles; retirement is `control`, not a mutation lease (otherwise it would block itself). The daemon revalidates exact identity against its registration/current root before claiming. Both sides use `rendezvous.OwnershipFingerprint`; compare the recomputed identity server-side and never trust PID/ref alone or introduce a second hashing implementation.

`Unavailable` mapping must keep mutation outcome unknown/accepted semantics intact: do not mark a previously accepted mutation as rejected solely because its reply was lost. Add typed `LifecycleErrorData` and compose with existing mutation error data instead of overwriting it. Append new catalog rows and generator client methods, and bump exact `ProtocolVersion` to `evener-appwire-v6` with flag-day rationale. Update exact-version fixtures from catalog/version constants where they represent the current version; keep explicit old-version incompatibility tests. A runtime peer that cannot supply required ownership/lifecycle evidence remains incompatible.

For probing, extend `ProbeResult`/`LiveEntry` with `*appwire.DaemonLifecycle` and freshness; a failed lifecycle probe clears current capability, not process ownership. Existing failed-probe residency semantics remain. Copy nested slices in roster snapshots. `Snapshot` returns a copied last-evaluated blocker list maintained by `Run`/`TryClaim`; it never dereferences released sessions after commit.

- [ ] **Step 4: Generate and run green.**

```sh
go generate ./appwire
go test ./appwire ./server ./internal/appserver ./cmd/evener-hub/internal/hubcore ./rendezvous -run 'Daemon|Retirement|Catalog|Protocol|Probe|OwnershipFingerprint' -count=1
make test-api-package
```

Expected: PASS, current generated TypeScript has all new types/methods and old-version tests still reject. If generation touches additional generator-owned files, inspect and name them in this task's commit; do not hand-edit generated code. Re-run generation and require no second diff.

- [ ] **Step 5: Commit.**

```sh
git add appwire/daemon.go appwire/daemon_test.go appwire/types.go appwire/protocol.go server/appwire_daemon.go server/appwire_daemon_test.go server/server.go server/appwire_runtime.go server/appwire_retirement_admission.go cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md rendezvous/identity.go rendezvous/identity_test.go
git commit -m "feat(appwire): expose typed daemon lifecycle diagnostics"
```

### Task 8: Wire the daemon-owned timer, mutable root and zero-safe launch config

**Files:**
- Create: `agent/retirement_timer_test.go`
- Create: `cmd/evener/serve_retirement_test.go`
- Create: `cmd/evener-hub/internal/launchconfig/daemon_idle_test.go`
- Create: `rendezvous/ownership.go`
- Create: `rendezvous/ownership_unix.go`
- Create: `rendezvous/ownership_other.go`
- Create: `rendezvous/ownership_test.go`
- Modify: `rendezvous/rendezvous.go`, `cmd/evener/internal/rvreg/rvreg.go`, `cmd/evener/internal/rvreg/rvreg_test.go`
- Modify: `agent/retirement.go`, `cmd/evener/serve.go`, `cmd/evener-hub/config.go`, `cmd/evener-hub/config_test.go`, `cmd/evener-hub/spawn.go`, `cmd/evener-hub/internal/launchconfig/types.go`, `cmd/evener-hub/internal/launchconfig/args.go`
- Modify: `cmd/evener-hub/internal/hubcore/config.go`, `cmd/evener-hub/main.go`, `cmd/evener-hub/app_rpc_settings_overview.go`, `appwire/types.go`
- Modify (generated): `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`
- Read: `cmd/evener/serve_shutdown_session_close_test.go`, `cmd/evener/serve_shutdown_budget_test.go`, `cmd/evener/serve_resume_identity_test.go`, `rendezvous/rendezvous.go`

**Interfaces:**
- Consumes: verified Tasks 1–7; Task 7 hooks; existing `serveDeps`, `runServeWithDeps`, `HubSpawner.Cfg`, `launchconfig.Resolved`, `launchconfig.ToArgs`.
- Produces: controller `Run(ctx, consumeRetirementClaim)` and daemon local `consumeRetirementClaim(ctx context.Context, claim *agent.RetirementClaim) error`, the single claim-consuming preparation/commit/release owner. Timer calls `TryClaim(false)` once and passes that exact claim. Manual RPC wrapper `requestRetirement(ctx context.Context) (appwire.DaemonRetireResponse,error)` calls `TryClaim(true)` once, returns fresh refusal when nil, otherwise passes that exact claim to the same consumer. The consumer never calls `TryClaim`.
- Adds `Config.DaemonIdleTimeout time.Duration` TOML field, `Resolved.DaemonIdleTimeout time.Duration` runtime-only field (not a new user launch-layer override), and `WebConfig.DaemonIdleTimeout time.Duration` for Settings. Hub always assigns it from validated `h.Cfg.DaemonIdleTimeout` at both Spawn and Resume, after resolving ordinary launch layers.
- Adds `SettingsHubOverview.DaemonIdleTimeoutMillis int64` with JSON `daemonIdleTimeoutMillis` (no `omitempty`, so disabled zero is explicit). `app_rpc_settings_overview.go` computes it from `cfg.DaemonIdleTimeout.Milliseconds()`.
- Adds `func (r *rvreg.Registration) Entry() (rendezvous.Entry, bool)` returning a copied current entry under its mutex; daemon identity validation consumes that snapshot rather than retaining the startup alias.
- Adds `serveDeps.retirementClock agent.RetirementClock`; nil selects the real clock. Add test-only `serveDeps.retirementObserve func(event, rootID string)` for root publication and the consumer stages specified below; production nil. Timer synchronization uses the injected clock fixture's acknowledgments. Do not add an environment variable or production fake-clock endpoint.

- [ ] **Step 1: Write zero/default configuration red tests.**

```go
func TestDaemonIdleConfigOmittedAndZero(t *testing.T) {
    for _, tt := range []struct { name, text string; want time.Duration }{
        {"omitted", "", time.Hour},
        {"disabled", "daemon_idle_timeout = \"0s\"\n", 0},
        {"positive", "daemon_idle_timeout = \"5m\"\n", 5 * time.Minute},
    } {
        t.Run(tt.name, func(t *testing.T) {
            path := filepath.Join(t.TempDir(), "hub.toml")
            if err := os.WriteFile(path, []byte(tt.text), 0600); err != nil { t.Fatal(err) }
            cfg, err := LoadConfig(path)
            if err != nil { t.Fatal(err) }
            if cfg.DaemonIdleTimeout != tt.want { t.Fatalf("timeout=%s", cfg.DaemonIdleTimeout) }
        })
    }
}
```

`ToArgs` test constructs `Resolved{DaemonIdleTimeout:0}`, requires `--daemon-idle-timeout` and `0s`; positive test requires `1h0m0s` (Go `Duration.String` contract, not hand formatting). Both actual Hub Spawn and Resume launcher seams capture argv and verify the same effective value. Add negative and malformed Hub/serve configuration failures before launching listeners or sessions. Missing config file must use `DefaultConfig` one hour; `applyConfigDefaults` must never replace zero for this field.

Timer tests use existing fake-clock pattern and an observation channel acknowledged after each evaluation: first settled state starts interval; input before deadline resets the full interval after settlement; blocked duration never accrues eligibility; repeated status/read/list/subscription traffic leaves the deadline unchanged; zero creates **no expiry timer**; startup and restored root start fresh; stale timer/root-generation wake is ignored. Manual zero bypasses elapsed-time check only. Clear races in both orders and targets exactly the new root after publication.

Add `TestServeRetirementAutomaticExpiryReachesRelease` and `TestServeRetirementManualTimerSingleOwner` in `serve_retirement_test.go`. Run actual `runServeWithDeps` with a real idle persisted session, scripted provider and injected clock. A test clock implementing `agent.RetirementClock` wraps the existing fake clock's `NewTimer`/timer `Reset` to acknowledge arming before `Advance`; do not assume an unavailable fake-clock wait helper. Define the serve observation field precisely as `retirementObserve func(event, rootID string)` with events `root_published`, `claim_consumed`, `prepared`, `committed`, `released`; invoke outside owner locks, production nil. It observes the actual shared consumer, not a substitute consumer that merely increments a counter. After expiry require one exact root in every consumer stage, actual runtime release, owned rendezvous removal and normal serve exit. The race test runs both orders: hold the winning consumer at `claim_consumed` on a channel, issue the other trigger (manual RPC or clock expiry), require no second consumer/preparation/commit, then release and require exactly one release/exit. Refusal observes preparing/retiring, not successful second ownership. Also assert a preparation failure aborts only its own claim and permits a later new claim after a full fresh interval. This catches the former double-`TryClaim` callback even if isolated controller tests pass.

- [ ] **Step 2: Run red.** `go test ./cmd/evener ./cmd/evener-hub ./cmd/evener-hub/internal/launchconfig -run 'DaemonIdle|ServeRetirement' -count=1` and `(cd agent && go test . -run '^TestRetirementTimer' -count=1)` — FAIL undefined config/timer or omitted zero argv.
- [ ] **Step 3: Implement timer and shared shutdown owner.**

```text
Run starts above currentSess and owns one reusable expiry timer:
  evaluate after startup and each coalesced Changed event
  never hold retirement.mu while collecting evidence
  if a blocker exists: clear eligibleSince; stop/disarm expiry timer
  if newly settled: eligibleSince = clock.Now (monotonic value retained in-process)
  if timeout == 0: no expiry timer; diagnostics still show eligibility
  else deadline = eligibleSince + timeout; arm remaining duration
  on timer: TryClaim(false) once; pass returned nonnil claim to consumeRetirementClaim
  stale timer or Changed observations recompute; never retire solely on received tick

requestRetirement (manual RPC):
  claim, snapshot, err = TryClaim(true) exactly once
  if err or claim == nil: return Accepted:false with snapshot/error
  pass exact claim to consumeRetirementClaim; never claim a second time
consumeRetirementClaim (timer and RPC):
  Prepare(ctx, claim); if failure, Abort(claim), stay serving and return bounded failure
  reserve existing serve exit owner for exact current root/generation
  Commit; set exit policy retirement; never change it back
  publish retiring response/snapshot; schedule bounded reader drain and release
  use existing listener/rendezvous/bridge teardown ordering, not terminal closeLiveSession
  exit normally; cleanup error remains retiring, logged and returned; no force kill
```

`Run` must not terminate because a preparation failed: clear interval and continue observing. A committed teardown error stops admitting effects permanently. `currentMu`, retirement mutex and `Session.mu` are never nested across callbacks/waits. Clear's admitted lease spans the replacement construction, controller attachment, `setSession`, bridge installation and old-runtime settlement; if retirement has already committed, clear is rejected before creating durable replacement state. Explicit shutdown racing preparing either aborts the still-preparing claim and takes terminal ownership, or observes committed retirement and joins it; only one release policy executes. Preserve all existing shutdown budgets and bridge-drain tests.

Use `fs.Duration("daemon-idle-timeout", 0, "Retire after continuous proven inactivity; zero disables automatic retirement")` with negative validation. Set `DefaultConfig().DaemonIdleTimeout = time.Hour`, keep predecode initialization, and append the CLI value unconditionally in `ToArgs`; copy it onto the resume-resolved configuration after historical model/config reconstruction. Assign `DaemonIdleTimeout: cfg.DaemonIdleTimeout` in `main.go`'s `hubcore.WebConfig` construction. Settings's Hub default must be the actual configured value, not the existing hard-coded spawn-timeout display pattern. Report changes as affecting future spawn/resume only.

Current `rvreg.Registration.Remove` calls PID-only `rendezvous.Remove`; no ownership comparison exists. Add `func rendezvous.RemoveIfOwned(dir string, expected Entry) error` and use it from registration. Serialize production `Write`, `Remove` and `RemoveIfOwned` through a stable per-PID sidecar lock while comparing all original entry fields (timestamps with `Time.Equal`). Compare and unlink share that lock; an unprotected check then unlink is insufficient. Keep the lock inode rather than deleting it on unlock. Implement `withOwnershipLock(dir string, pid int, fn func() error) error` with the existing Linux/Darwin flock pattern in `cmd/evener-hub/internal/hostlock/hostlock_unix.go` (that internal package cannot be imported here). Unsupported platforms report unavailable strong ownership for retirement; preserve the existing disabled standalone behavior rather than breaking ordinary rendezvous writes there. Test replacement publication before removal and a concurrent writer blocked on the same lock in both orders; stale cleanup may remove only its exact record. All current-binary writers participate; this is not a claim to control arbitrary external file editors or retrofit old binaries. The original registration owner, not session metadata mtime, establishes identity.

- [ ] **Step 4: Run green/race.**

```sh
(cd agent && go test -race . -run '^TestRetirement' -count=1)
go generate ./appwire
go test -race ./cmd/evener ./cmd/evener-hub ./cmd/evener-hub/internal/launchconfig -run 'DaemonIdle|ServeRetirement|Shutdown|ResumeIdentity' -count=1
go test -race ./rendezvous ./cmd/evener/internal/rvreg -run 'Ownership|Registration' -count=1
```

Expected: PASS including disabled direct invocation, positive direct override, negative/malformed failures, mutable root and normal shutdown unchanged. This is the first task allowed to enable the one-hour Hub default.

- [ ] **Step 5: Commit.**

```sh
git add agent/retirement.go agent/retirement_timer_test.go cmd/evener/serve.go cmd/evener/serve_retirement_test.go cmd/evener-hub/config.go cmd/evener-hub/config_test.go cmd/evener-hub/spawn.go cmd/evener-hub/internal/launchconfig/types.go cmd/evener-hub/internal/launchconfig/args.go cmd/evener-hub/internal/launchconfig/daemon_idle_test.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/main.go cmd/evener-hub/app_rpc_settings_overview.go appwire/types.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md rendezvous/ownership.go rendezvous/ownership_unix.go rendezvous/ownership_other.go rendezvous/ownership_test.go rendezvous/rendezvous.go cmd/evener/internal/rvreg/rvreg.go cmd/evener/internal/rvreg/rvreg_test.go
git commit -m "feat(daemon): retire continuously idle Hub daemons after configured timeout"
```

### Task 9: Resolve retiring-versus-resume without changing recovery authority

**Files:**
- Create: `cmd/evener-hub/app_retirement_resume.go`
- Create: `cmd/evener-hub/app_retirement_resume_test.go`
- Modify: `cmd/evener-hub/app_threadlifecycle.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_relay.go`, `cmd/evener-hub/internal/appsource/local_daemon.go`
- Read: `cmd/evener-hub/app_force_stop.go`, `cmd/evener-hub/relay_subscription_lifecycle_test.go`, `cmd/evener-hub/internal/daemonprocess/process.go`

**Interfaces:**
- Consumes: lifecycle error data and roster diagnostics; existing `resumeThread`, `resumeTurnStartThread`, `ResumeLocks.For`, `lookupDaemonOwner`, process `Open/Wait/Close`, `relays.startTurn`.
- Produces: `func awaitRetiredOwner(ctx context.Context, cfg hubcore.WebConfig, entry rendezvous.Entry) error`. Success means exact ownership is confirmed exited/absent under existing discovery authority, **not** simply unreachable. `ErrRetirementUnavailable` over wire is retryable, not an explicit-resume-required fence.
- No new replay API. Retry the original `appwire.TurnStartParams`, including `ClientMutationID` and input bytes.

- [ ] **Step 1: Write the live-owner refusal test through the real Hub router.** New fixture `newRetirementHubFixture(t *testing.T) *retirementHubFixture` is defined in this task: construct the real appserver/Hub source registry/roster and scripted daemon AppWire WebSocket server in private `t.TempDir` roots; use existing hub fixture session/project minting. At the external process controller boundary retain a `Wait` channel and a verified target; at the external launcher boundary count launches and register the replacement scripted daemon. Do not replace `resumeThread`, the relay, source registry or mutation handler. Struct fields are `client *appwire.Client`, `ref string`, `exit chan struct{}`, `launches atomic.Int32`, `accepted atomic.Int32`; methods `start(ctx, id string) (appwire.TurnStartResponse,error)` send one text input via real client, and `confirmExit()` closes the exact process wait channel and replaces discovery's fixture record consistently.

```go
func TestRetirementResumeDoesNotReplaceLiveOwner(t *testing.T) {
    f := newRetirementHubFixture(t)
    ctx, cancel := context.WithCancel(context.Background())
    finished := make(chan error, 1)
    go func() { _, err := f.start(ctx, "same-retirement-mutation"); finished <- err }()
    // Fixture awaits the process controller's Wait entry, not elapsed time.
    <-f.waitEntered
    if got := f.launches.Load(); got != 0 { t.Fatalf("launched over live owner: %d", got) }
    cancel()
    if err := <-finished; err == nil { t.Fatal("unconfirmed exit returned success") }
    if got := f.launches.Load(); got != 0 { t.Fatalf("cancel launched replacement: %d", got) }
}
```

Add `waitEntered chan struct{}` to the fixture; its scripted external process `Wait` closes it exactly once and honors context. Additional tests: two clients same ID wait on retiring owner, confirm exit, require one launch and one accepted turn; two distinct IDs with the first provider held at an active-turn barrier require one replacement, one applied acceptance and one existing `CodeConflict`; separately settle the first turn before sending the second ID and require two accepted turns in order (no new queueing semantics); accepted first/reply lost replays same stable turn; incompatible owner, unreadable discovery, deletion fence, force-stop ResumeRequired and stale connection epoch prevent auto-resume. A read/probe/shutdown/queue/steer/settings/background hydration retry must never invoke Spawn/Resume.

Name the distinct-ID cases `TestRetirementResumeConcurrentDistinctIDsConflict` and `TestRetirementResumeSequentialDistinctIDsAccepted`. For these acceptance assertions, the replacement fixture must run the real server/client-mutation session with a scripted LLM provider; the scripted retiring peer may model the unavailable transport, but must not manufacture replacement acceptance/conflict receipts. Start two clients behind the exit barrier, confirm the one old owner exits, hold the first provider invocation until **both RPC results** arrive, and require the unordered result pair to contain exactly one applied receipt and one `CodeConflict`. Read the primary mutation journal to require one accepted stable turn and no runnable intent for the losing ID; release provider and await completion. The sequential test starts the second ID only after the first turn's settlement event and journal reflection; require one replacement, two distinct stable turns, original inputs and two provider executions in order. Never use timing sleeps or require a particular client to win the concurrent race.

- [ ] **Step 2: Run red.** `go test ./cmd/evener-hub -run '^TestRetirementResume' -count=1` — FAIL: current owner reuse/retry path does not interpret retiring as a bounded wait for confirmed exit.
- [ ] **Step 3: Implement narrowly in turn/start recovery.**

```text
On lifecycle preparing: retryable unavailable within existing request budget; no spawn.
On lifecycle retiring:
  resolve exact owning entry and sorted stable/current aliases
  acquire existing alias locks; recheck recovery epochs and deletion/protocol fences
  re-read ownership; if identity changed, restart resolution, never signal the new owner
  open verified process handle; Wait(ctx) without extending request deadline
  if wait/verification/discovery is uncertain, return lifecycle-unavailable
  refresh roster and confirm old owner absent; if a current replacement exists, reuse it
  otherwise execute existing Resume under the same ownership lock
  start/rebind relay to exact replacement before retrying original turn/start params
```

Never call `BeginForceStop`, `PersistForceStop`, or `ConfirmForceStop` on this path. Factor `resumeThreadLocked(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams, automatic bool) (appwire.ThreadResumeResponse, error)` to avoid recursively acquiring alias locks; the public wrapper owns acquisition/revalidation, and the private body does discovery/spawn. Wait without locks that the process waiter or roster callback needs; per-session alias serialization may span the external wait only because those callbacks do not take those locks. Add a test proving this property with the actual callback path.

Relay ownership is per subscription, not per process: on source generation replacement invalidate only the old source handle, preserve Hub subscribers, and use existing `evener/thread/resync` notification to request authoritative hydration. `prepareRelay` must bind the replacement source even when thread ref/session ID is unchanged. Old-source buffered frames after replacement are fenced by source generation; do not clear the transcript or draft. Follow the existing bounded relay retry clock and snapshot/event response cut.

- [ ] **Step 4: Run green/race.** `go test -race ./cmd/evener-hub ./cmd/evener-hub/internal/appsource -run 'RetirementResume|Relay|ForceStop|Recovery|Deletion' -count=1` — PASS with one replacement/one same-ID turn and no bypass of force-stop/deletion authority.
- [ ] **Step 5: Commit.**

```sh
git add cmd/evener-hub/app_retirement_resume.go cmd/evener-hub/app_retirement_resume_test.go cmd/evener-hub/app_threadlifecycle.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_relay.go cmd/evener-hub/internal/appsource/local_daemon.go
git commit -m "fix(hub): resume safely after confirmed daemon retirement"
```

### Task 10: Expose full discovery inventory and identity-fenced resident actions

**Files:**
- Create: `cmd/evener-hub/app_daemons.go`
- Create: `cmd/evener-hub/app_daemons_test.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_force_stop.go`, `cmd/evener-hub/app_force_stop_test.go`, `cmd/evener-hub/internal/hubcore/roster.go`, `appwire/protocol.go`
- Modify (generated): `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`
- Read: `cmd/evener-hub/internal/hubcore/tree.go`, `cmd/evener-hub/app_archive.go`, `rendezvous/rendezvous.go`

**Interfaces:**
- Consumes: Task 7 typed contracts, Task 9 ownership serialization and existing verified force stop.
- Produces: `func listDaemons(ctx context.Context, cfg hubcore.WebConfig) (appwire.DaemonListResponse,error)`, `func retireDaemon(ctx context.Context,cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse,error)`, `func daemonIdentity(entry rendezvous.Entry) appwire.DaemonIdentity`.
- Produces `func (s *LocalDaemonSource) RetireDaemonAtEntry(ctx context.Context, entry rendezvous.Entry, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse,error)` in `local_daemon.go`. Use existing `s.withClient(ctx, entry, callback)` (the exact-entry/recovery-scoped pattern in `ReadThreadAtEntry`), and inside it call `client.DaemonRetire(ctx, params)`. Do not re-resolve the target from ref after the Hub validated/locked the exact entry; do not add this local-process operation to unrelated source interfaces.
- Produces: `func (r *Roster) ResidentEntries() []ResidentEntry`, with new `hubcore.ResidentEntry { Entry rendezvous.Entry; Confirmed *LiveEntry }`. Snapshot combines confirmed and unconfirmed discovered identities under roster lock, cloning slices. It does not scan OS processes or filter archives.

- [ ] **Step 1: Write a stale rendered-target test.** Add helper `residentEntryForTest(t *testing.T, pid int) rendezvous.Entry`: use `hubtest.SessionID(t)`, fixture state dir and loopback endpoint, current protocol and fixed timestamp; no fabricated session ID. This is discovery input, not a mocked ownership algorithm.

```go
func TestDaemonIdentityChangesWithReplacement(t *testing.T) {
    first := residentEntryForTest(t, 101)
    second := first
    second.PID = 102
    second.StartedAt = first.StartedAt.Add(time.Second)
    if daemonIdentity(first) == daemonIdentity(second) {
        t.Fatal("rendered target cannot distinguish replacement")
    }
    reused := first
    reused.StartedAt = first.StartedAt.Add(time.Second)
    if daemonIdentity(first) == daemonIdentity(reused) {
        t.Fatal("PID reuse cannot be distinguished")
    }
}
```

The decisive test uses the real list response, replaces the fixture rendezvous record, invokes both retire and `evener/thread/forceStop` with that row's `Identity`/`ExpectedDaemon`, and asserts no daemon RPC/signal/recovery fence was issued against the replacement. Retain existing force-stop external process-controller fixture and assert valid expected identity still enters verified `Kill/Wait` and explicit ResumeRequired. Also test stale clear alias, unchanged PID with new start instant, protocol change, discovery read failure, changed ownership while waiting for alias locks, and malicious arbitrary PID/ref input.

Inventory table: archived live root; compatible root with many delegate aliases; incompatible live process; stale/unconfirmed probe; terminal dead marker; multiple discovered processes claiming overlapping aliases. Require one row per exact discovered process identity, deterministic sort by ref/start/PID/generation, no duplicate alias row. Dead confirmed-exited records are not residents; unresolved records remain visible with unknown status. No token or raw process environment in serialized response. Unknown/stale/incompatible rows have `Lifecycle:nil`, `CanRetire:false`; force-stop availability means the configured verified path exists, never proof that a future action will pass verification.

- [ ] **Step 2: Run red.** `go test ./cmd/evener-hub ./cmd/evener-hub/internal/hubcore -run 'DaemonIdentity|DaemonResident|DaemonAction|ForceStop' -count=1` — FAIL missing inventory/action fields or stale row stops replacement.
- [ ] **Step 3: Implement snapshot and action protocol.**

```text
list:
  consume configured roster snapshot; no per-row forced process probe on each render
  merge confirmed/unconfirmed by exact ownership fingerprint, not alias map
  decorate archive/name from existing saved metadata/index independently of sidebar filtering
  preserve unknown/stale explicitly; only fresh compatible lifecycle enables Retire
  return actual Hub default plus rows; no tokens
retire:
  resolve local owning entry using existing alias authority
  acquire sorted ResumeLocks (do not BeginForceStop)
  revalidate expected row identity, ownership, deletion and protocol fences
  forward exact identity to owning daemon's safe retire RPC
  return fresh blocker refusal or Accepted:true/retiring; never claim exit from RPC alone
forceStop:
  if ExpectedDaemon supplied, compare before any admission/recovery fence
  after alias locks revalidate expected identity again with existing ownership check
  then run existing retained-handle verification, recovery journal, Kill and Wait unchanged
```

The optional expected identity preserves existing ref-only force-stop callers; the resident UI always supplies it. Make list/retire final catalog scopes Hub/both and register actual handlers now. Safe retirement works with timeout zero and refuses any fresh blocker regardless of stale UI eligibility. Polling list never increments daemon activity. No automatic force-stop fallback on incompatible peers or teardown failure.

- [ ] **Step 4: Run green/generated gate.**

```sh
go generate ./appwire
go test -race ./cmd/evener-hub ./cmd/evener-hub/internal/hubcore ./server ./appwire -run 'Daemon|Retirement|ForceStop|Catalog|Archive' -count=1
make test-api-package
```

Expected: PASS; full final catalog/router parity and archived/incompatible rows visible independently of sidebar tree.

- [ ] **Step 5: Commit.**

```sh
git add cmd/evener-hub/app_daemons.go cmd/evener-hub/app_daemons_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_force_stop.go cmd/evener-hub/app_force_stop_test.go cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/hubcore/roster.go appwire/protocol.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): list residents and fence explicit process actions"
```

### Task 11: Add mounted resident controls to Settings → Hub

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/daemonResidents.ts`
- Create: `cmd/evener-hub/frontend/src/stores/daemonResidents.test.ts`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.test.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/hub.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/hub.test.tsx`
- Read: `cmd/evener-hub/frontend/src/stores/settingsOverview.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.tsx`

**Interfaces:**
- Consumes: generated `DaemonListResponse`, `DaemonIdentity`, `DaemonRetireResponse`; existing `connectionStore`, `Button`, `ConfirmDialog`, `EmptyState`, `friendlyErrorMessage`.
- Produces `daemonResidentsStore`, `useDaemonResidentsStore`, `HubResidents`, `resetDaemonResidentsStoreForTests`. Store state is `{data:DaemonListResponse|null, loading:boolean, error:string|null, pending:Set<string>, refresh():Promise<void>, retire(identity:DaemonIdentity):Promise<DaemonRetireResponse>, forceStop(identity:DaemonIdentity):Promise<void>}`. Identity generation is the pending-action key, not ref or PID.
- Polling lifetime belongs to `HubResidents.useEffect`; store itself starts no permanent interval. Use the existing overview fetch/dedup/current-client style, but refresh residents rather than cache forever.

- [ ] **Step 1: Write a real rendered-row test with the existing external FakeClient boundary.**

```tsx
test("archived incompatible residents remain visible and cannot safely retire", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [{
      identity: {ref: "local:resident-fixture", pid: 101,
        startedAt: "2026-09-10T00:00:00Z", generation: "fixture-generation"},
      name: "Archived fixture", protocol: "evener-appwire-v3",
      compatibility: "incompatible", archived: true, probeState: "unknown",
      canRetire: false, canForceStop: true,
    }],
  }));
  render(<HubResidents />);
  const row = await screen.findByRole("row", {name: /Archived fixture/});
  expect(within(row).getByRole("button", {name: "Retire now"})).toBeDisabled();
  expect(within(row).getByRole("button", {name: "Force stop"})).toBeEnabled();
});
```

These strings are component fixture data, not backend-valid session encodings; backend integration tests use `hubtest.SessionID`. Import existing `FakeClient`, `connectionStore`, RTL `render/screen/within`, Vitest and the component. Reset store/connection and `cleanup` after each test; use Vitest fake timers for polling. Add tests that advance timers only after initial request promise resolves, unmount, advance again and assert no more calls; failed refresh retains old rows but marks data stale; newest-client generation wins a late response; concurrent refresh joins one request; fresh retire refusal displays returned blockers without removing row.

Force-stop test clicks the row, verifies an accessible confirmation dialog names root/PID and warns work/watches may be interrupted, then cancels (no RPC). Confirm sends `evener/thread/forceStop` with exact original `ExpectedDaemon`; change list row behind the dialog and verify the dialog does not silently retarget. Unknown timeout differs from disabled zero. Both Hub default and row effective timeout render. Keyboard focus/disabled pending state and narrow viewport overflow are checked.

- [ ] **Step 2: Run red.** `(cd cmd/evener-hub/frontend && npx vitest run src/stores/daemonResidents.test.ts src/panes/settings/sections/hubResidents.test.tsx --maxWorkers=4)` — FAIL missing component/store.
- [ ] **Step 3: Implement using existing widgets and authenticated connection.**

```ts
async function retire(identity: DaemonIdentity): Promise<DaemonRetireResponse> {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("daemon residents: no client connected");
  return client.request("evener/daemon/retire", { identity });
}

async function forceStop(identity: DaemonIdentity): Promise<void> {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("daemon residents: no client connected");
  await client.request("evener/thread/forceStop", {
    ref: identity.ref, expectedDaemon: identity,
  });
}
```

Wrap actions with pending/error state and `finally` cleanup; refresh after response while preserving the fresh refusal blockers until a newer successful snapshot. `Accepted:true` displays retiring rather than pretending the process already exited. There is no automatic resume after force stop; expose the existing explicit Resume navigation/action, not a new retry that changes authority. Keep unknown/stale status visible and safe-retire disabled. Use friendly error handling, not raw server/internal errors.

Mount one component below `HubUpdates`. Render accessible table/list rows with root/name, PID/start, protocol/compatibility/archive, effective timeout, phase/deadline and blocker IDs/categories; label inventory “Discovered resident daemons” and explain rendezvous scope. Display actual default timeout and future-launch scope. Use tokens and existing responsive overflow conventions; no second settings dashboard. Poll at existing Hub discovery cadence (two seconds default), reuse roster snapshots, and stop on unmount. Component lifecycle owns/cancels the timer; an in-flight response uses a generation check before publication. Do not throw away current rows on request failure.

- [ ] **Step 4: Format and run green.**

```sh
(cd cmd/evener-hub/frontend && npx biome check --write src/stores/daemonResidents.ts src/stores/daemonResidents.test.ts src/panes/settings/sections/hubResidents.tsx src/panes/settings/sections/hubResidents.test.tsx src/panes/settings/sections/hubResidents.module.css src/panes/settings/sections/hub.tsx src/panes/settings/sections/hub.test.tsx)
(cd cmd/evener-hub/frontend && npx vitest run src/stores/daemonResidents.test.ts src/panes/settings/sections/hubResidents.test.tsx src/panes/settings/sections/hub.test.tsx --maxWorkers=4)
make test-web
```

Expected: PASS. Update existing Hub section call assertions to include actual resident-list fetch, not to stop checking requests or weaken archive behavior.

- [ ] **Step 5: Commit.**

```sh
git add cmd/evener-hub/frontend/src/stores/daemonResidents.ts cmd/evener-hub/frontend/src/stores/daemonResidents.test.ts cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hubResidents.module.css cmd/evener-hub/frontend/src/panes/settings/sections/hub.tsx cmd/evener-hub/frontend/src/panes/settings/sections/hub.test.tsx
git commit -m "feat(web): add resident daemon controls to Hub settings"
```

### Task 12: Prove selected transcript/draft recovery through actual client and browser

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/threads.retirement.test.tsx`
- Create: `cmd/evener-hub/frontend/src/dev/retirementharness-entry.tsx`
- Create: `cmd/evener-hub/frontend/retirementharness.html`
- Create: `cmd/evener-hub/frontend/scripts/retirementguard/run.mjs`
- Create: `cmd/evener-hub/app_retirement_browser_test.go`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`, `cmd/evener-hub/app_relay.go` (only if the new tests demonstrate a gap)
- Modify: `cmd/evener-hub/frontend/package.json`, `make/testing.mk`, `docs/developing-evener/testing.md` (generated browser target documentation)
- Read: `cmd/evener-hub/frontend/src/panes/session/composer/draft.ts`, `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`, `cmd/evener-hub/frontend/src/panes/session/composer/Composer.integration.test.tsx`
- Read: `cmd/evener-hub/frontend/src/stores/mutationOutbox.ts`, `cmd/evener-hub/frontend/scripts/transcriptscrollguard/run.mjs`, `cmd/evener-hub/frontend/scripts/browserGuardProcess.mjs`, `cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs`

**Interfaces:**
- Consumes: actual `AppwireClient`, `connectionStore`, `threadsStore`, existing composer draft ownership, Hub relay/resync path, Task 9 fixture.
- Produces: npm `retirementguard`, included in `make test-web-browser`. Browser fixture exports a promise-based test-only control object `window.retirementHarness` with `ready:Promise<void>`, `retire():Promise<void>`, `settled():Promise<void>`, `snapshot():{ref:string,draft:string,turnIDs:string[],text:string,sourceGeneration:string}`. Values are read from real stores/rendered view, not maintained as parallel expected state. Retirement control talks only to its fixture's safe-retire RPC.
- `app_retirement_browser_test.go` extends Task 9's real Hub/scripted-daemon fixture to serve the real frontend harness; scripted provider/process launcher remain the only substitutes. It emits fixture endpoint/auth capability to the browser child process through private IPC, never production state or checked-in data. Define a test-only `flag.Bool("retirement-browser", false, "run isolated retirement browser fixture")`; the test skips in ordinary Go gates unless this flag is passed. npm `retirementguard` runs `cd ../../.. && go test ./cmd/evener-hub -run '^TestRetirementBrowser$' -count=1 -args -retirement-browser`. The Go fixture starts services and then invokes `node frontend/scripts/retirementguard/run.mjs` from `cmd/evener-hub`; the runner only connects to the supplied fixture and never starts another Go test, avoiding recursion. It uses existing isolated Chrome/Vite lifecycle helpers and awaits exact owned process exits.

- [ ] **Step 1: Add the selected-view regression with actual client transport.** The unit fixture uses a local scripted WebSocket server and **real** AppwireClient, not FakeClient. Define `openRetirementClientFixture():Promise<RetirementClientFixture>` in the new test file: initialize the client against the server, wire it into `connectionStore`, open one ref through the actual thread store, answer initial read with persisted transcript and subscribe, mount the real `<ClientProvider client={client}><Composer ref={ref}/></ClientProvider>` and enter draft through native textarea input. Read persistent draft via existing `readDraft(ref)` from `composer/draft.ts`; do not invent a draft store. `RetirementClientFixture` provides `draft():string`, `turnIDs():string[]`, `retire():Promise<void>`, `send(text:string, mutationId:string):Promise<void>`, `close():Promise<void>`; these delegate to production store/client APIs; `send` clicks the mounted real Composer and awaits `subscribeComposerSubmissionCommitted` from `composer/queue/pendingTurnsStore.ts` plus the authoritative completion event. The mutation ID comes from a deterministic UUID boundary in this fixture, not a helper manually clearing the draft. Its scripted server closes only the daemon-side source, emits the real resync contract, and provides a replacement read/event stream after next start. It does not replace the reducer/hydration functions.

```ts
test("selected transcript and unsent draft survive source retirement", async () => {
  const f = await openRetirementClientFixture();
  try {
    const before = f.turnIDs();
    const draft = f.draft();
    await f.retire();
    expect(f.turnIDs()).toEqual(before);
    expect(f.draft()).toBe(draft);
    await f.send(draft, "retirement-view-mutation");
    const after = f.turnIDs();
    expect(new Set(after).size).toBe(after.length);
    expect(after.length).toBe(before.length + 1);
    expect(f.draft()).toBe("");
  } finally { await f.close(); }
});
```

Add an actual Hub integration/browser assertion independently: open the real Session pane and transcript, enter an unsent draft, retire with the pane still selected, confirm original transcript/draft remain, submit once, await replacement streamed delta and turn completion, and compare DOM/store identity with the original durable mutation journal. Test late old-generation frames cannot overwrite replacement; lost start reply retries same ID once; queue/steer/settings unavailability retains unsent input and does not cause auto-resume. A Hub socket remaining connected while the daemon socket closes must still recover; browser reconnect alone is not the test.

- [ ] **Step 2: Run red.** `(cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.retirement.test.tsx --maxWorkers=4)` and `go test ./cmd/evener-hub -run '^TestRetirementBrowser$' -count=1 -args -retirement-browser` — record the actual broken boundary. If behavior already passes, deliberately suppress replacement relay binding in the fixture-owned scripted source, observe missing streamed-event assertion fail, restore it and retain production behavior unchanged. Do not make a production edit merely to give this task one.
- [ ] **Step 3: Implement only proven recovery gaps and the browser guard.**

```text
Keep selected model and draft when source becomes unavailable.
Keep Hub subscription ownership while retiring old source handle.
On replacement, bind the new source before turn/start; send existing thread/resync.
Hydrate with existing response-cut buffering and generation fence, not store clear/rebuild.
If current wireSubscribeDecision suppresses re-subscription across same-ref source change,
invalidate only that ref's wire subscription decision before handleReady resync.
On same mutation ID replay, use persisted turn identity; no second optimistic turn.
Never clear draft until actual mutation acceptance/outbox reconciliation owns the content.
Read/probe/background hydrate may refresh history but never resume the daemon.
```

Browser harness mounts production Session and HubResidents components, uses actual authenticated AppwireClient and real Hub route/relay fixture, and supplies only layout/fixture launch controls. Use native input/click and observe DOM plus store state. Capture screenshots before retirement, after retirement with draft, and after resumed streaming; include exact protocol/process identities and a machine-readable assertion result. At phone/desktop widths verify the residents table does not force document horizontal overflow and confirm dialog remains usable. Extend browser target with one lifecycle guard because existing layout-only guards cannot prove this contract; do not relabel `make test-web-browser`'s existing five guards as already proving recovery.

- [ ] **Step 4: Run green and browser evidence.**

```sh
(cd cmd/evener-hub/frontend && npx biome check --write src/stores/threads.ts src/stores/threads.retirement.test.tsx src/dev/retirementharness-entry.tsx)
(cd cmd/evener-hub/frontend && npx vitest run src/stores/threads.retirement.test.tsx --maxWorkers=4)
go test -race ./cmd/evener-hub -run 'RetirementBrowser|RetirementResume|Relay' -count=1 -args -retirement-browser
make generate
make test-web-browser
```

Expected: all tests actually run, Chrome guard exits zero with three screenshots and structured results in its printed fixture-owned artifact directory. Missing Chrome/Vite/process-inspection capability is incomplete verification. Keep artifact paths in review record; remove only disposable fixture artifacts after evidence has been retained.

- [ ] **Step 5: Commit.**

```sh
git add cmd/evener-hub/frontend/src/stores/threads.retirement.test.tsx cmd/evener-hub/frontend/src/dev/retirementharness-entry.tsx cmd/evener-hub/frontend/retirementharness.html cmd/evener-hub/frontend/scripts/retirementguard/run.mjs cmd/evener-hub/app_retirement_browser_test.go cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/app_relay.go cmd/evener-hub/frontend/package.json make/testing.mk docs/developing-evener/testing.md
git commit -m "test(web): prove selected-thread recovery across daemon retirement"
```

### Task 13: Prove real launch/exit/resume and document operator policy

**Files:**
- Create: `cmd/evener-hub/daemon_retirement_e2e_test.go`
- Create: `cmd/evener/serve_retirement_process_test.go`
- Create: `docs/daemon-idle-retirement.md`
- Modify: `cmd/evener-hub/spawn.go` (external process-launch seam only if existing harness cannot supply a test executable)
- Read: `docs/developing-evener/testing.md`, `cmd/evener-hub/spawn.go`, `cmd/evener/serve_test.go`

**Interfaces:**
- Consumes: real HubSpawner and real `runServeWithDeps`; all prior contracts.
- Produces: `TestDaemonRetirementProcessHelper` test-binary subprocess entry. It receives fixture-only scripted provider/clock control via inherited pipes; calls the actual serve lifecycle with `serveDeps.retirementClock` and scripted LLM adapter; exposes no production endpoint/flag. Build the helper with `go test -c -o "$fixtureDir/evener-serve.test" ./cmd/evener` from repository root, using the host cache and a fixture-owned output path. Invoke its exact helper test with `-test.run=^TestDaemonRetirementProcessHelper$`; inherited IPC carries the real serve argv unchanged, and the helper passes those args directly to `runServeWithDeps`. Hub launch seam replaces only the external executable invocation with this helper command while preserving the real resolved argv/env, rendezvous, listeners, serve admission/release and Hub discovery/resume algorithms.
- New fixture `newDaemonRetirementProcessFixture(t *testing.T) *daemonRetirementProcessFixture` owns private HOME/XDG roots, Hub process, real daemon child process handles, clock control pipes and rendezvous roots. Fields: `hub *HubSpawner`, `entry rendezvous.Entry`, `stateDir string`; methods `advance(d time.Duration) error`, `awaitEvaluation() error`, `waitDaemonExit() error`, `restartHub() error`, `startMutation(id string) (appwire.TurnStartResponse,error)`, `cleanup() error`. Each waits for pipe acknowledgment/process exit/event, never sleeps. A watchdog deadline is a hang tripwire, not the pacing mechanism.
- Add methods `awaitTurnSettled(turnID string) error` and `assertSingleExecution(t *testing.T, mutationID, turnID string)`. `startMutation` retains the exact original `TurnStartParams` by ID and reuses the same input/params for retries. Its scripted provider reports invocation/input over fixture IPC and blocks completion until `awaitTurnSettled` releases it; that method then awaits the real settlement event and durable reflected journal state, not just provider return. `assertSingleExecution` independently reads the original mutation journal and transcript through their production parsers, matches `schema.Turn.ClientMutationID`/`StableTurnID` to the saved original params, requires one user-input entry with original content and no remaining pending execution, and compares fixture IPC provider invocations/input for exactly one execution. Match only that input-bearing turn, not every assistant/tool transcript entry sharing the stable ID. Use a one-round no-tools/no-retries scripted response, so one provider invocation really is the expected execution count. Other provider work such as naming is distinguished from this turn, not counted as its execution. Do not compare whole responses: applied/pending at acceptance and replayed/reflected after settlement are intentionally different.

- [ ] **Step 1: Add the smallest actual process expiry test.**

```go
func TestDaemonRetirementHubLaunchExpiresAndResumes(t *testing.T) {
    f := newDaemonRetirementProcessFixture(t)
    t.Cleanup(func() { if err := f.cleanup(); err != nil { t.Error(err) } })
    original := f.entry.SessionID
    if err := f.awaitEvaluation(); err != nil { t.Fatal(err) }
    if err := f.advance(time.Hour); err != nil { t.Fatal(err) }
    if err := f.waitDaemonExit(); err != nil { t.Fatal(err) }
    if _, err := schema.LoadSessionMeta(f.stateDir, original); err != nil { t.Fatal(err) }
    response, err := f.startMutation("retirement-process-resume")
    if err != nil { t.Fatal(err) }
    if err := f.awaitTurnSettled(response.Turn.ID); err != nil { t.Fatal(err) }
    replay, err := f.startMutation("retirement-process-resume")
    if err != nil { t.Fatal(err) }
    if response.Receipt.Disposition != appwire.MutationDispositionApplied ||
        replay.Receipt.Disposition != appwire.MutationDispositionReplayed ||
        response.Turn.ID == "" || replay.Turn.ID != response.Turn.ID ||
        response.Receipt.TurnID != response.Turn.ID || replay.Receipt.TurnID != response.Turn.ID ||
        response.Receipt.ProjectionState != appwire.MutationProjectionPending ||
        replay.Receipt.ProjectionState != appwire.MutationProjectionReflected {
        t.Fatalf("resume identity/receipt/projection: %#v %#v", response, replay)
    }
    f.assertSingleExecution(t, "retirement-process-resume", response.Turn.ID)
    if f.entry.SessionID != original { t.Fatal("replacement changed saved root identity") }
}
```

This test uses the Hub default, not a special per-test hard-coded one-hour controller. Capture effective daemon diagnostics to prove flag propagation. Additional tests: explicit zero/directed serve zero arm no timer and remain resident after arbitrary fake advancement; positive direct configuration exits; admitted work resets complete interval; reads/probes/subscriptions do not reset; Hub process exits before advancing daemon clock and daemon still retires; restarted Hub rediscovers live owner or resumes offline root; real helper process normal exit code zero, no terminal lifecycle records; stale old rendezvous cleanup leaves replacement record; detached fixture process survives retirement. Run nested primary preservation from Task 6 alongside this real process test rather than claiming this root-only fixture proves delegates.

- [ ] **Step 2: Run red.** `go test ./cmd/evener-hub ./cmd/evener -run 'TestDaemonRetirement|TestServeRetirementProcess' -count=1` — FAIL at the smallest actual launch/exit/config boundary if wiring is incomplete. Each subprocess helper selector must run explicitly and report its exit; no “helper skipped” can count as coverage.
- [ ] **Step 3: Complete integration only where failing evidence points, and write operator docs.** Test helpers instantiate real serve/session/Hub runtime, with scripted provider and clock/external launcher only. Install no binary into user PATH and use no production singleton HOME. Add cleanup through exact fixture-owned process handles; never broad `pkill`/PID-name scans. Documentation must contain this executable example and policy table:

```toml
# hub.toml; affects subsequently spawned or resumed daemons only.
daemon_idle_timeout = "1h"
# Set "0s" to disable automatic retirement, not to select the default.
```

```sh
# Standalone remains resident by default.
evener serve --daemon-idle-timeout=0s
# Explicit standalone opt-in.
evener serve --daemon-idle-timeout=30m
```

Explain: one-hour continuous proven inactivity, passive viewing ignored, questions/watches can pin indefinitely, idle delegates/worktrees/scratch preserved, next-message same-ID resume, retired versus unavailable versus incompatible, settings default versus per-daemon effective timeout, discovered—not machine-wide—inventory, safe Retire now even at zero, fresh blockers, force-stop confirmation and explicit Resume, no legacy upgrade/automatic kill, and how to report `prepare_failed`/`release_failed`. State that already-running daemons are unchanged and old binaries require explicit verified stop/resume. Keep provider idle timeout and archive retention separate. If examples need model/provider arguments in a reader's environment, say they supplement the normal serve invocation; these snippets illustrate only lifecycle flags and are not claimed credential-free runtime tests.

- [ ] **Step 4: Run green and all named acceptance gates.**

```sh
go test -race ./cmd/evener-hub ./cmd/evener -run 'TestDaemonRetirement|TestServeRetirement' -count=1
(cd agent && go test -race . -run '^TestRetirement' -count=1)
go generate ./appwire
make lint-generated
make vet
make merge-approval-gate
make test-api-package
make test-web-browser
git diff --check
```

Expected: each exits zero; record module/package/test counts and any skips. Do not collapse partial success into a green summary. Inspect all warnings and failures. Keep the known moderate Vitest advisory separately reported; lifecycle work does not silently change dependency versions. Verify generated freshness by rerunning generation and comparing the generated paths, not merely trusting generator exit.

- [ ] **Step 5: Commit.**

```sh
git add cmd/evener-hub/daemon_retirement_e2e_test.go cmd/evener/serve_retirement_process_test.go cmd/evener-hub/spawn.go docs/daemon-idle-retirement.md
git commit -m "test(daemon): verify real retirement lifecycle and document operations"
```

## Requirement-to-task acceptance map

| Approved spec requirement | Implementation | Independent acceptance evidence |
| --- | --- | --- |
| §4 complete root/tree predicate, stable non-secret blockers | 2–5 | Per-category real entry tests, cold-member tests, original persistent evidence reads |
| §5 mutation-first survives; claim-first rejects pre-durable | 1–4 | Two-order filesystem/channel barriers for start/claim/queue/steer/autonomous/delegate/worktree; same journal replay |
| §5 lock ordering, no check-then-close, no wait under owner locks | 1–4, 8 | Nested admission, root-swap, actual settlement callbacks and race detector |
| §5 preparation failure resident, committed failure never reopens | 5, 6, 8 | Write/read/close/drain fault tests with phase and effect assertions |
| §6 non-terminal release; same root/delegate IDs/config/tasks/sandbox | 5, 6 | Original meta/transcript/journal/delegate/job records before/after and fresh nested cold send |
| §6 scratch, clean/dirty worktrees, owner locks, no disposal/sweep | 5, 6 | Durable scratch pins before lease release; distinct environment bindings and exact shared-child/root current paths across rootswap, backswap, aged startup sweep and cold restore; original artifact bytes/retained incoming scratch; foreign-root git sweep and occupancy markers; unchanged dual-allocation/unreferenced/terminal cleanup regressions |
| §6 readers drain, subscription closure, inactive handles, rendezvous ownership | 1, 2, 6, 8 | Borrow barrier, subscription close event, owned handle exit and stale-registration test |
| §7 Hub 1h, direct 0, explicit 0, malformed/negative, both launch paths | 8, 13 | Config table, actual resolved argv, daemon diagnostics and subprocess fake-clock exit |
| §4 continuous inactivity, reads ignored, fresh startup/resume | 8, 13 | Fake-clock evaluation acknowledgments and actual process exit, no sleeps |
| §7 typed catalog/client/version/SDK | 7, 10 | Catalog/router parity, generation freshness, `make test-api-package` |
| §8 concurrent same-ID turn resume, live ownership uncertainty/fences | 9, 13 | Real Hub router and exact process wait; one launch/one accepted turn |
| §8 selected thread transcript/draft and streamed replacement events | 9, 12 | Real AppwireClient/store/Hub relay plus Chrome screenshots/structured assertions |
| §8 full discovery, archive/incompatible/unknown, deterministic identities | 10, 11 | Roster/discovery fixtures independent of sidebar; rendered row tests |
| §8 manual safe retirement at zero and fresh blockers | 7, 10, 11 | Actual daemon claim + Hub RPC refusal + UI retained row/blockers |
| §8 stale row/PID reuse safe force stop, explicit Resume retained | 10, 11 | Both identity comparisons before recovery/signal, existing verified process tests |
| §9 Hub absence/restart, detached process lifetime and unchanged scope | 6, 8, 13 | Exact subprocess exits and survival; existing shutdown/archive/deletion tests |
| §9 review loops, simplify, SDD, gates, operator docs and PR | execution sections, 13 | Review record, gate exits/artifacts and PR body |

## Final independent review, simplification and PR handoff

- [ ] Parent runs a **fresh** pair of parallel adversarial code reviewers under the same five-point rules, five-round cap and convergence criteria as the plan loop. Review the actual diff, original spec and primary test evidence, not the implementer's summary. Record distinct/duplicate/accepted/rejected findings and each score in the review record. Significant unresolved findings block PR even at the cap.
- [ ] Fix accepted findings in bounded ownership commits, rerun the decisive red/green tests and affected gates. Do not weaken pairing/index/reference/tolerance or remove failing assertions without independent evidence they were wrong.
- [ ] Run the four-angle code simplification pass: share only matching reclamation/restore/ownership semantics; remove duplicate state and unnecessary timers; avoid repeated per-row probes; keep one admission controller and one teardown owner. No behavior changes, weakened safety predicates, or new general checkpoint service. Rerun focused race/preservation/browser evidence after simplification.
- [ ] Inspect final diff, generation output, git status, scope and all Task 13 gate results. Confirm no production operations, unrelated dependency changes or untracked fixture debris. Record exact remaining limitations. A skipped capability gate remains incomplete and must be rerun on the available host.
- [ ] Commit review record and docs with named paths, then create a PR only after gates/review converge:

```sh
git add docs/superpowers/reviews/2026-09-10-daemon-idle-retirement.md docs/daemon-idle-retirement.md
git commit -m "docs: record daemon retirement verification and review"
git diff --check
git status --short
git diff --stat 2664cc881...HEAD
gh pr create --base main --head wip/daemon-idle-retirement --title "Retire inactive daemons safely and expose resident controls" --body-file docs/superpowers/reviews/2026-09-10-daemon-idle-retirement.md
```

The review record's final PR summary must link spec and plan, explain defaults/zero and unchanged older processes, preserve-vs-force-stop semantics, user-visible recovery, limitations, both reviewer outcomes, simplification results, exact gate commands/exits and browser artifacts. Do not merge the PR. Jesse already chose subagent-driven execution; no further execution-choice question is needed from this drafting unit.

## Ledger C revision dispositions

All six findings from the parent's round-one Ledger C are **addressed in this plan**, not claimed implemented or review-converged. Task IDs remain unchanged. The parent owns the review ledger and the next competitive review round.

| Finding | Plan disposition and decisive planned check |
| --- | --- |
| C.1 worktree preservation | Task 6 retains durable occupancy markers, releases process-local ownership only, adopts on restore; `TestRetirementForeignSweepPreservesOccupiedLanes` runs a real foreign sweep before restore while an unrelated unlocked lane is still collected. Existing terminal/P3 cleanup tests remain. |
| C.2 scratch preservation | Tasks 5–6 define durable root manifest/exact-directory pins before exposure or lease release, collector enforcement, same-path root/cold-child lease adoption and terminal/deletion release. Actual aged startup-sweep plus fresh-child artifact-read tests retain their original absolute-path/bytes oracle and unreferenced cleanup checks. |
| C.3 replay oracle | Tasks 2 and 13 replace both whole-response equalities with stable IDs, original input, applied/replayed and stage-appropriate projections. Primary journal/transcript and scripted execution counts prove one execution separately from duplicate wake calls. |
| C.4 distinct-ID ordering | Task 9 separates active-provider-barrier concurrency (one launch, one applied start, one existing conflict) from explicit settled sequential starts (one launch, two accepted turns). Replacement acceptance uses the real mutation engine, not fabricated receipts. |
| C.5 single claim | Task 8 has timer/manual admission each call `TryClaim` once and a shared consumer that never claims. Actual serve expiry must reach release; competing timer/manual tests in both orders require one preparation/commit/release owner. |
| C.6 compatibility/error propagation | Task 2 changes existing effect/callback signatures in place, names caller/test-double ownership and propagates refusal to RPC/delivery semantics. No new admitted/legacy wrappers or silently discarded lifecycle errors. |

**Round-2 Ledger C — addressed in plan; ready for round 3:** replace the rejected owning-session slot model with separate immutable allocation identity, persisted logical environment bindings and explicit consumer-role mappings. Tasks 5–6 now permit E0/B and E1/A under the same root owner, preserve clone-versus-adoption and occupied-target backswap behavior, restore cold consumers to their exact original bindings, and require `TestRetirementSharedChildScratchBindingsRestore` plus the unchanged baseline dual-allocation test. No other task or lifecycle policy changes. Binding persistence/restore and its concurrency-failure tests remain implementation proof obligations, not established runtime guarantees.

**Ledger C API risks:** scratch pins/manifests and exact-path restore are new, not baseline `Retain` guarantees. Partial pin writes can conservatively retain a directory without proving restore, unknown historical dependencies block rather than disappear, and restore/collector inode races require the two-order lease test. Terminal/deletion release must not release another root's pins or turn a failed cleanup into a false success. Mutation receipts are existing APIs, not a new replay protocol: first applied/pending, replayed/pending before incorporation and replayed/reflected after settlement are different valid observations. A stable ID or duplicate wake alone does not prove original-input preservation or one execution; the paired primary-record/provider oracles remain mandatory.

## Plan self-review checklist

- [x] Every spec section maps to implementation and an independent evidence row above.
- [x] Every task has named ownership/dependencies, defined consumes/produces, representative red test, implementation algorithm/code, exact green commands and named staging/commit.
- [x] No placeholder implementation instructions or unexplained helper names; proposed APIs are distinguished from source-existing APIs.
- [x] All existing source-reference paths exist; new paths explicitly marked Create; all commands honor Go module boundaries.
- [x] Controller/type/wire names remain identical across tasks; claim/reader/prepare/release states are monotonic and old close behavior is preserved.
- [x] No full runtime default is enabled before preservation/admission tests; no backend-only claim substitutes for selected-view/browser proof.

**Material implementation risks:** root runtime-only suspension does not exist at the baseline; current autosave/close paths can swallow errors; `rvreg.Registration.Remove` is PID-only and needs the explicit same-owner removal implementation in Task 8; reclamation's cold/closed semantics differ from retirement; full autonomous admission must be established across enqueue/settlement, not only RPC; same-ref subscription decisions may suppress replacement recovery; and a process can remain indefinitely resident because of an unanswered question or unresolved evidence. The plan places direct failure tests at these boundaries rather than treating any as an existing guarantee. If a boundary cannot be proved, report its blocker and keep automatic retirement disabled; do not ship a timer around ordinary Close.
