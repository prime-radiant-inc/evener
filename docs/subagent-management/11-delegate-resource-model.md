# Delegate Resource Model

Status: Shipped evergreen product authority for the stable-delegate flag-day
recovery.

## Decision

A delegate is one long-lived resource identified by dlg_.... An activation is
one private attempt by that resource to process input. An activation is not a
job, has no job_... ID, has no independently queryable record, and is never a
public control target.

One root-session delegate-tree controller is the only lifecycle authority for
every delegate in that session tree. It owns:

- the stable delegate records and parent graph;
- each delegate's private generation number and current phase;
- the live runtime and cancellation handle for the current generation;
- admission receipts for work that is being started;
- pending subtree-stop operations; and
- durable terminal delivery state.

The existing watch journal remains the authority for watch registration,
pending/coalesced frames, delivery acknowledgements, and end notices. It is a
separate source journal, not a second delegate lifecycle fold. The controller
orders stable-delegate watch admission and stop fencing against the lifecycle
aggregate through exact process-only receipts. Receiver transcripts remain the
only durable attention journal.

The controller is a synchronous Go object protected by one mutex. It is not an
actor goroutine, detached supervisor, second session loop, or general workflow
engine. Its methods change durable aggregate state and return narrow external
actions. Provider calls, timers, process cancellation, hooks, event emission,
transcript delivery, runtime close/restore, worktree operations, and
notification delivery happen after the controller mutex is released. Exact
process-only claims and receipts may serialize those actions, but they create
no public identity or durable lifecycle authority.

Shell processes remain jobs. The existing job manager becomes shell-only for
new work. Delegate history and lifecycle stop being folded through job_started,
job_finished, current_job_id, latest_job_id, or parent_job_id.

## Why the project is being restarted

The project inherited real defects. Before the abandoned integration work:

- delegate_send resolved a stable delegate through current/latest activation
  JobRecords, transcript references, live subagent state, and a second running
  job lookup;
- steering had already needed several race fixes and could report busy or
  unavailable from snapshots that no longer described the child;
- job_stop controlled a concrete delegate JobRecord, while product intent was
  to stop the current work of the stable delegate and its subtree; and
- recursive stop snapshotted live sessions and jobs independently, so a
  concurrent resume or child start could fall outside the cancellation set.

Those bugs are not consequences of the August identity refactor; they existed
in the split DelegateRecord-plus-JobRecord model on main.

The abandoned refactor did make the repair much harder. It kept the split
lifecycle authority and then coordinated it with a stable delegate fold,
private activation JobRecords, Session admission tokens, job-manager mirrors,
subtree epochs, unload state, close flights, and live runtime state. Repeated
Task 12 reviews found cases where those components described different
generations. More fences could repair individual races, but they would preserve
the root cause.

This design therefore keeps the valid product decision—one public stable
delegate identity—and replaces the lifecycle implementation that made the
stable resource an overlay on jobs.

## Alternatives considered

### Continue repairing the abandoned branch

Rejected. The branch contains useful race tests and forensic examples, but its
core shape requires several authorities to agree about the current delegate
lifetime. Each additional correctness rule adds another cross-component
snapshot, epoch check, or completion fence. Landing it would increase the
system's conceptual and operational complexity.

### Add a controller around main's existing delegate jobs

Rejected. A controller that still treats activation JobRecords as lifecycle
authority would be a third layer above DelegateRecord and JobRecord. It might
serialize some callers, but status, stop, finalization, restart, notifications,
and runtime ownership would still have two durable identities to reconcile.

### Replace delegate jobs with one delegate-tree controller

Selected. A private generation lease is enough to reject stale completion. A
single tree lock is enough to order stop, resume, steering, and descendant
admission. One durable fold is enough for restart, listing, and delivery. The
tradeoff is that delegate lifecycle commands within one root session tree are
serialized. Their volume is small, and correctness and maintainability are
more important than parallel lifecycle mutation.

## Goals

1. Make dlg_... the only public and durable delegate control identity.
2. Make one component authoritative for lifecycle, generation, runtime binding,
   subtree admission, and stop ordering.
3. Make delegate_send reliably steer a running delegate or start an idle one.
4. Make job_stop(target=dlg_...) stop exactly the work current at its
   linearization point and recursively fence that delegate's subtree.
5. Preserve one serial model activation per delegate.
6. Preserve nested delegation, direct-owner authorization, transcript
   continuity, structured terminal results, sandbox/worktree configuration,
   turn budgets, and tree-wide delegate limits.
7. Keep shell jobs and their existing job_... control model.
8. Reconcile restart loss without constructing model runtimes.
9. Produce one stable delegate row in tools, events, doctor output, AppWire,
   TUI, and web projections.
10. Preserve shipped watches, observer sidecars, quiet supervision, hook/nudge
    behavior, final-round salvage guidance, unreachable-attention escalation,
    delegate-owned shell delivery, retention bounds, and terminal/result/
    worktree fidelity through the stable identity.
11. Delete the delegate-specific job lifecycle and only the coordination
    mechanisms needed to reconcile it; do not delete behavior merely because
    its current implementation is coupled to a delegate JobRecord.

## Approved product cuts and architecture non-goals

The flag-day implementation deliberately excludes:

- public activation-addressable status, output, history, watch, stop, and UI
  rows;
- migration or aliases for legacy delegate JobRecord state; a root containing
  it fails closed with legacy_delegate_state;
- migration of old delegate-job-addressed watch rows; they fail closed with
  legacy_delegate_watch_state;
- non-recursive stable delegate stop; job_stop(dlg_...) is recursive and
  include_children does not weaken it;
- job_status(dlg_...) returning terminal packet contents or acknowledging
  terminal delivery;
- autonomous or time-based idle-runtime unload as a lifecycle protocol;

Those six bullets are the complete accepted cuts to shipped product behavior.
The following architecture constraints are not additional behavior cuts; they
rule out new machinery or work outside this identity cutover:

- a detached subtree supervisor;
- concurrent activations for one delegate;
- a public activation history or activation read API;
- a global state-root epoch, mixed-version loader, compatibility window, or
  dual writer;
- a general rewrite of shell job management or the existing watch journal;
- a general AppWire protocol redesign beyond the lossless stable-delegate
  cutover; and
- performance work that requires sharded lifecycle locks.

In particular, stable delegate watch sources/receivers, watch_parent, observer
callbacks, the quiet watchdog, SubagentStop and auto-nudge behavior, attention
escalation, the public max_retained_terminal bound, and terminal/result/
worktree fidelity are existing product behavior to preserve, not deferred
projects or removal targets.

Automatic/background unload remains forbidden. The existing
max_retained_terminal policy is instead rehomed as demand-triggered reclamation
of exact resident idle terminal child-runtime subtrees during delegate create
or cold restore. Reclamation changes only process residency; it never deletes
the stable aggregate, descriptor, transcript, outcome, lineage, delivery state,
or resumability and adds no durable unload event.

## Vocabulary

- **Delegate**: the long-lived conversation resource named by dlg_....
- **Delegate tree**: every delegate descended from one root Session.
- **Controller**: the root-session object that owns delegate-tree lifecycle.
- **Generation**: a private monotonically increasing integer for one attempt to
  process input. It is scoped to one delegate and is never serialized publicly.
- **Lease**: the pair of delegate ID and generation captured by one live run.
- **Runtime**: the retained child Session, model configuration, transcript
  handles, and cancellation function used to execute a generation.
- **Starting receipt**: process-local proof that runtime construction or a
  child/shell start began while the subtree admitted new work.
- **Terminal packet**: the bounded canonical result or error delivered to the
  delegate's owner for one generation.
- **Attention entry**: one model-bound steering turn in the receiver's existing
  append-only transcript, named by a private stable attention ID and resolved
  by a later provider-excluded consumed/discarded marker in that same
  transcript.
- **Subtree stop**: one controller operation that fences a target delegate and
  every descendant before external cancellation begins.

## Public model

### Identity and lineage

The public delegate identifier is dlg_.... The child Session ID and private
generation are diagnostics and persistence details, not control handles.

Every public parent relation is typed:

~~~json
{
  "parent": {
    "id": "dlg_...",
    "type": "delegate"
  }
}
~~~

A shell job launched by a delegate uses the same typed parent relation with
the owning delegate ID. No new delegate output contains parent_job_id.

### Lifecycle, phase, and outcome

Public lifecycle has two states:

- running: a generation is starting, processing, settling, or stopping;
- idle: no generation is processing.

An optional phase may distinguish starting, awaiting_model, model_streaming,
tool_running, settling, and stopping while lifecycle is running.

The latest generation's outcome is separate:

- completed;
- failed;
- exhausted;
- cancelled; or
- stopped.

A failed, cancelled, or stopped generation may leave the delegate idle and
resumable. Permanent resumability is separate metadata:

~~~json
{
  "resumable": false,
  "not_resumable_reason": "isolation_disposed"
}
~~~

### Tool contracts

delegate creates a delegate and starts generation 1. It returns delegate_id,
type=delegate, lifecycle/status, resumability, transcript_ref, and applicable
model, sandbox, worktree, observation, and warning metadata after the stable
descriptor and initial input are durable. Creation has no inline-wait option and
never returns a job ID. Completion is notification-driven; subsequent
interaction uses delegate_send. watch_parent remains a non-transitive grant to
the new child and is persisted in the stable descriptor.

delegate_send accepts to=dlg_..., message, and max_wait_ms:

- If the delegate is running, the message is durably admitted to that
  generation's child transcript and bound into the next legal model request.
  The call returns
  action=steered after that admission succeeds. It does not start another
  generation and does not wait for a reply.
- If the delegate is idle and resumable, the call restores or reuses the child
  runtime, starts the next private generation, durably admits the message as
  that generation's input, and returns action=started.
- If the delegate is starting, settling, or stopping, the call returns a typed
  target_busy error. It does not guess which generation should receive the
  message.
- If the delegate is permanently non-resumable, the call returns
  target_not_resumable with the durable reason.

delegate_send never accepts a job_... target and never returns a job ID.
A positive max_wait_ms applies only when that call starts an idle delegate. A
live steer returns on durable admission and reports that waiting was not
applicable in the public wait_ignored_reason field. That field is specific to
the call, remains only on the delegate_send result and the transcript/UI
transport of that result, and is not folded into warnings, the delegate
aggregate, stable events/status, or client snapshots.

to=caller remains the explicit route from a delegate to its controlling
caller. If the caller is another delegate, the route uses that stable parent's
controller steering path and lifecycle checks. If the caller is the root
Session, it uses the root's existing steering admission. caller is a contextual
route, not a second delegate identity.

job_status accepts target=job_... or target=dlg_.... A delegate result is
metadata-only:

~~~json
{
  "id": "dlg_...",
  "type": "delegate",
  "status": "idle",
  "resumable": true,
  "transcript_ref": "local:...",
  "last_outcome": {
    "status": "completed",
    "ended_at": "..."
  }
}
~~~

job_status does not return full terminal result content and does not
acknowledge terminal delivery.

job_stop accepts target=job_... or target=dlg_..., include_children, and
max_wait_ms. Shell behavior remains unchanged. A delegate target always means
stable-resource subtree stop. include_children has no effect for a delegate:
both values perform mandatory recursive stop.

A delegate stop result contains stable id/type, previous and current lifecycle,
and one outcome: cancelled_by_request, already_idle, or stop_requested.
already_idle means the common durable stop request completed and found no live
work; it is not a shortcut around fencing/reconciliation. If
normal completion linearizes first, stop sees idle; if stop linearizes first,
stop precedence applies. A bounded wait that expires reports stop_requested
without claiming cancellation has settled.

job_list returns one items array. Each delegate appears exactly once as
id=dlg_.... Delegate generations never appear. Shell jobs remain id=job_....
Ordering is descending latest activity, then ascending public ID. Existing
visibility and include_nested/include_descendants rules remain, but lineage is
resolved through delegate IDs rather than delegate JobRecords.

read_transcript reads the delegate conversation through transcript_ref.
job:job_... continues to name shell output only.

job_watch preserves self, parent, shell-job, and stable-delegate sources. Its
receiver is always the session that created the watch; a delegate receiver is
therefore implicit when that watcher session belongs to a stable delegate, and
an arbitrary receiver remains forbidden. Public watch endpoints are typed as
session, shell, or delegate rather than routed through delegate-job identity.
A stable delegate source binds its stable ID plus the current private
generation so a later activation cannot be mistaken for the watched one.

Parent-source authorization requires all three current facts: the child actor
carries the exact lease, its stable parent edge resolves to the source, and its
descriptor persisted ParentWatchGranted. The grant is non-transitive.
watch_parent never grants arbitrary ancestor or sibling access.

All shipped watch semantics remain: event/output/progress filters, every,
bounded frames, latest-frame-wins coalescing, the delivery budget, list/inspect/
clear, terminal ordering, observer callbacks, restart cancellation/end notices,
and append-only deduplicated SessionMeta.ObservedBy UI metadata. Legacy rows
whose source or receiver is addressed through a delegate JobRecord fail closed
with legacy_delegate_watch_state; they are not translated to the stable ID.

### Authorization

Knowing a stable ID does not grant control.

- The root Session directly owns its direct delegates.
- A delegate directly owns only the delegates it created.
- delegate_send may target a directly owned delegate or caller.
- job_stop may directly target a directly owned delegate. Recursive stop then
  reaches all descendants through the controller.
- Ancestors may list or inspect visible descendants under existing
  include_nested/include_descendants rules, but visibility does not grant
  direct send authority.
- Siblings and unrelated session trees receive the existing non-disclosing
  not-found/not-controllable behavior.

The controller receives the caller's stable actor identity with each command
and performs authorization before mutation.

## Controller shape

The following types are illustrative. Names may change to match surrounding Go
style, but the ownership boundaries may not.

~~~go
type delegateTreeController struct {
    mu        sync.Mutex
    store     *delegateStore
    delegates map[string]*delegateState
    stop      *delegateStop
    receipts  map[uint64]*workReceipt
}

type delegateState struct {
    id            string
    parentID      string
    descriptor    delegateDescriptor
    generation    uint64
    phase         delegatePhase
    resumable     bool
    closedReason  string
    lastOutcome   *delegateOutcome
    live          *delegateRuntime
    pendingSteers []steeringAdmission
    deliveries    []pendingDelivery
}

type delegateLease struct {
    delegateID string
    generation uint64
}
~~~

There is one controller instance per root Session tree. Child Sessions inherit
a pointer to it plus their owning delegate ID. They do not copy lifecycle state
into Session tokens or job managers.

The controller map is the only live routing index from delegate ID to current
runtime. The durable store is the only restart authority. Session fields may
cache non-authoritative runtime details, but no caller may decide lifecycle by
combining a Session snapshot with a job-store snapshot.

Process-local start, input, steer, model-boundary, shell-work, enqueue,
delivery, runtime-reclamation, and waiter claims may use one in-memory counter
because they never survive restart.
Durable correlation does not use that counter: owner and watch delivery IDs are
derived from durable source identity, a stop operation is identified by the
sequence of its durable stop-request event, and an exact watch cursor includes
its update sequence. A restarted controller therefore cannot collide with an
earlier delivery or stop. These tokens serialize post-unlock work only; none is
an activation identity, durable event stream, or lifecycle mirror.

### Lock and I/O rule

The controller mutex may cover:

- validation and mutation of controller state;
- append and fsync of the controller's own event store;
- creation or release of process-local receipts.

The controller never acquires a transcript/history lock or performs transcript
I/O. A controller claim returns first, transcript persistence happens after
unlock, and exact claim completion later re-enters the controller. No
transcript path may call the controller while holding its lock. This one-way
order is a tested invariant, not a convention left to callers.

The controller mutex must not cover:

- provider/model calls;
- timer callbacks or sleeps;
- process start or cancellation;
- waits for runtime completion;
- hook execution;
- event emission;
- notification delivery;
- watch-journal or receiver-transcript delivery I/O;
- runtime close or restore;
- filesystem lane creation/removal; or
- arbitrary callbacks.

The live store is private to the controller. No other production component
locks or appends it. Read-only doctor/cold-projection tooling uses a separate
missing-file-tolerant `ReadEvents` path plus the same pure fold. That path never
creates, truncates, repairs, locks for append, or writes the log. Before an
append writes bytes, the store assigns the prospective sequence values and
applies the whole batch through the same reducer to a transient deep clone of
the caller's current fold. Rejection changes neither bytes, store sequence, nor
live state. A valid append fsyncs before the controller swaps in that accepted
clone; append failure leaves the previous aggregate state intact. The store
retains no second fold.

The controller is the outer lifecycle lock. Code must not acquire it while
holding a child Session, subagent, transcript, or shell job-manager lock.
Runtime and job callbacks snapshot what they need, release their local lock,
then report through the controller. Lock-order tests must enforce this rule.

The receiver transcript, not the process steering queue or its best-effort
snapshot, is the durable attention journal. Attention append, consume, discard,
and read-only fold happen outside the controller mutex. Source acknowledgement
may occur only after the receiver append has fsynced. The controller may retain
an in-memory index or wake hint for a resident runtime, but loss or disagreement
of that cache cannot create, consume, discard, or otherwise settle attention.
A receiver-local process mutex may serialize the fold-plus-append operation so
concurrent retries of one ID write once, but it stores no pending state and is
not a second journal or lifecycle authority. Cold operations are serialized by
the exact delivery receipt or the root's single stop/reconciliation loop.

The watch journal has the same one-way relationship. A source append/refold or
receiver attention append never occurs while the controller mutex is held. The
controller admits an exact cursor through a process receipt, the watch journal
persists it, and only then may the receiver be touched. Source acknowledgement
follows receiver fsync. Neither the journal reducer nor a transcript callback
may call into the controller while holding its own lock.

## Internal state machine

Internal phases are:

- idle;
- starting;
- running;
- settling;
- stopping; and
- closed.

Allowed transitions are:

~~~text
idle -> starting -> running -> settling -> idle
idle -> starting -> idle                 (construction rejected or aborted)
starting -> stopping -> idle
running -> stopping -> idle
settling -> stopping -> idle
idle -> closed
~~~

The generation increments exactly once when starting commits. closed is
permanent. A normal completed, failed, exhausted, cancelled, or stopped run
returns to idle; its outcome does not close the conversation.

Only controller methods change phase. Runtime code reports observations with
an exact lease. A report whose generation is not current is a harmless stale
no-op and cannot change the aggregate, detach a successor runtime, acknowledge
a successor delivery, or alter public status.

The controller does not reopen a delegate while it is stopping. A new
delegate_send may start the next generation only after the stop operation has
completed and the delegate is idle. This single rule removes the need for
stop-over-finalization epoch dominance.

## Command semantics and linearization

Every lifecycle command has one linearization point while the controller mutex
is held. Overlapping commands are correct according to that order; the
implementation does not invent wall-clock precedence from snapshots taken
before the lock.

| Race | Required result |
|---|---|
| idle send vs. idle send | One call reserves starting. A truly concurrent call receives target_busy; after the first reaches running, a later call steers. |
| idle stop vs. send | If stop linearizes while idle, its durable request fences admission, completes, and may report already_idle; only then may a later send start. If send commits first, stop cancels that generation. |
| running steer vs. stop | If steer commits first, it is durably admitted and stop then cancels the run. If stop commits first, steer is rejected as stopping. |
| finalize vs. stop | If normal finish commits first, stop observes idle. If stop commits first, the exact finish records the stop outcome and cannot publish normal completion over it. |
| stale finish vs. successor | Generation mismatch is a no-op. |
| child/shell start vs. ancestor stop | A committed start is included in the stop; an uncommitted receipt is forced to abort or cancel before stop completes. |
| delayed cancellation vs. successor | No successor can start until stopping completes, so delayed cancellation cannot target a successor. |

### Create

Creation makes deterministic isolation precede stable publication while durable
delegate ownership precedes child conversation/provider construction:

1. validate the request, agent policy, sandbox, worktree request, delegation
   allowance, tree capacity, retention bound, and result schema; derive the
   immutable descriptor inputs;
2. under the controller mutex, mint the stable delegate ID and reserve an
   unexposed starting state, capacity, deterministic transcript/worktree paths,
   and construction cancellation context beneath the owning parent;
3. release the mutex and complete deterministic sandbox/worktree isolation at
   those reserved paths. Failure aborts the reservation, cleans only its exact
   uncommitted artifacts, and publishes no delegate;
4. under the mutex, verify the exact reservation and append one batch containing
   delegate_created and delegate_run_started(generation=1), transfer the
   construction cancellation into an exact non-launched live binding, and fold
   running;
5. release the mutex and construct the child Session, transcript, environment,
   and runtime only in the deterministic locations now owned by that durable
   delegate; make no provider call;
6. under the mutex, attach the runtime and acquire an exact input-admission
   claim fenced by stop; release the mutex, append/fsync initial input to the
   child transcript, then re-enter the controller to complete that exact claim
   and mark the binding ready;
7. release the mutex and launch the first model turn; and
8. return the stable projection after input admission is durable.

Before step 4 the stable ID is neither durable nor public. Exact reservation
cleanup and startup worktree/scratch reconciliation treat any pre-commit
isolation residue as uncommitted; it cannot become a live delegate. After step
4 the aggregate exists and owns every partially constructed conversation and
runtime artifact. A crash or construction failure therefore reconciles the
exact generation as failed/runtime_lost and never leaves unowned live state. A
stop before step 4 invalidates the reservation; a stop afterward cancels and
settles the durable generation. If construction or input admission fails after
step 4, the tool
returns a normal structured result containing the stable ID and failed outcome
rather than a transport error that would drop the result; the durable resource
is never hidden from its owner. If the configured output bound cannot hold the
full post-commit result, create omits optional diagnostics in error, model, then
sandbox order; the bounded core always retains the stable ID, child identity,
type, status, reason, resumability, and transcript reference. A permanently
unrestorable failure atomically appends terminal preparation, run finish, and
resumability closure using the existing event kinds. Pre-commit validation or
append failure may return a tool error because no delegate exists. If the
compensating batch also fails, the exact non-launched binding and capacity stay
latched for explicit stop or restart repair. No provider call occurs through any
failure path.

From step 4 until ready is marked, the durable generation is active and may be
stopped or exactly finished, but steer, model, tool, child, and shell admission
return target_busy. Before step 4, the window contains only a cancellable
process reservation and cleanup responsibility for its exact uncommitted
artifacts; there is no durable generation or public delegate. Readiness is a
process-local launch gate, not a second lifecycle phase; if it disappears on
restart, the durable generation settles failed/runtime_lost.

The creation batch contains descriptor and lineage fields needed for lazy
restore. It does not contain a JobRecord or activation ID.

The descriptor's Config is the effective child configuration captured before
step 4, after applying child-specific turn, agent, effort, MCP, and sandbox
decisions. Stable child construction starts from that snapshot and overlays
only process-local engine dependencies, callbacks, and tree linkage. The live
parent configuration is not a post-commit source for model-visible child
semantics. Config is the sole durable authority for the child agent name and
reasoning effort. Its sandbox mode and network fields are the configuration
projection of the descriptor's richer Sandbox execution-policy snapshot; the
durable fold rejects any disagreement between them.

TaskTemplates stores a copy of every template in the selected named agent's
ordered workflow, including each title, prompt, reasoning effort, type, and
insertion directive. Stable construction populates the child task store from
that complete committed slice. Registered delegate creation supplies no parent
task templates, preserving the existing child-workflow behavior without
consulting the live agent registry after commit.

ToolNameCeiling stores the sorted, unique, pre-commit ceiling derived from the
effective parent registry, named-agent policy, watch and delegation grants,
isolation restrictions, and result-tool policy. It is not a promise that every
name can be constructed by the child: parent runtime or MCP-shaped tools may be
absent there. Child construction applies the final intersection between its
fully built registry and this ceiling before caching the provider-facing tool
surface. The ceiling is never widened by intrinsic child tools and always
contains Config's effective result tool.

When Config.ShareTasksWithChildren is enabled, the descriptor also records
SharedTaskStoreOwnerSessionID. Live construction may attach the current shared
TaskStore pointer only when that owner identity matches. Cold restoration must
resolve this owner key to the existing shared store before constructing the
child; creating a separate child store would fork the committed task history.
Cold restoration must likewise reconstruct from the committed effective Config,
complete TaskTemplates, and ToolNameCeiling rather than live parent or plugin
semantics.

### Start an idle delegate

An idle send uses an in-memory starting reservation and the same
durable-before-side-effect protocol:

1. under the controller mutex, validate direct ownership, resumability,
   ancestor state, capacity, and idle phase; reserve starting and a start
   cancellation context;
2. still under the mutex, verify the reservation, increment the generation,
   append delegate_run_started, transfer its cancellation into an exact
   non-launched live binding, and fold running;
3. release the mutex and reuse or restore the child runtime without calling the
   provider;
4. under the controller mutex, attach that runtime to the exact generation and
   acquire an exact input-admission claim fenced by stop;
5. release the mutex, append/fsync input to the child transcript, then re-enter
   the controller to complete the exact claim and mark the binding ready;
6. release the mutex and launch the model turn; and
7. return action=started only after durable input admission.

If stop wins before step 2, it invalidates the reservation. If stop wins while
runtime construction is outside the lock, it cancels and settles the already
durable exact generation. Runtime attachment or input admission then refuses
the stopped lease, and the constructed runtime closes without a provider call.

If the run-start append succeeds but child input persistence fails, the
generation settles as failed with reason input_persist_failed by atomically
appending the canonical bounded terminal_error preparation and the one
delegate_run_finished event. Owner delivery and restart therefore use the same
shape as every other non-settling failed finish, and the send returns a
structured stable-ID/failed-outcome result. A process crash after run-start but
before input persistence reconciles as failed/runtime_lost; Serf never claims
the input was accepted to a caller whose tool result did not commit.

If both input persistence and that compensating atomic batch append fail, the
exact non-launched runtime binding remains installed and capacity
remains held. A process-local recovery-required latch rejects model, tool,
steer, and successor admission without changing durable lifecycle authority.
Explicit stop may settle that exact lease, and restart deterministically folds
the durable running generation as failed/runtime_lost. The provider is never
launched through this failure path.

The same recovery-required latch protects an exact running binding when a
terminal-preparation append fails and the fallback atomic finish cannot be
persisted. The controller retains the binding, capacity, and finalization
claim. Only an exact covering stop may repair the generation: it fsyncs the
canonical stopped run finish first and releases process state only after that
append succeeds. A failed repair leaves the same stop and exact binding intact
for retry.

### Steer a running delegate

Running steering resolves the delegate directly in the controller; it never
looks up a current/latest JobRecord.

The command uses one exact process-only steering claim:

1. under the controller mutex, validates direct ownership, phase=running, and
   the exact live lease, then acquires a stop-fenced steering claim;
2. releases the mutex and appends/fsyncs the steering turn to the child
   transcript;
3. re-enters the controller with the exact claim, records that transcript entry
   as pending steering for the current
   generation;
4. updates latest activity from the durable transcript-entry timestamp;
5. captures a public update with the unchanged lifecycle revision so clients
   can max-merge that activity hint; and
6. releases the claim and returns success.

Claim admission orders steering against stop; transcript fsync is the durable
acceptance point. Stop drains an earlier admitted claim, so an accepted turn is
either completed into pending steering before cancellation or the append fails
and is aborted. The child loop must include that turn at the next legal model
boundary exactly once. If a provider request is already in flight, the steer is
consumed after that response and any already-accepted tool boundary; it does
not mutate an in-flight provider request.

A successful steer means accepted into the active conversation, not that the
model produced a reply. Stop may linearize immediately afterward and cancel
the generation. A failed transcript append returns a delivery error and leaves
no accepted steer.

At each model boundary, BeginModelRequest validates the exact lease and claims
the current pending steering admissions under the controller mutex. The child
then snapshots its transcript after unlock. CompleteModelRequest re-enters the
controller with that exact process claim, revalidates stop/lease, consumes only
the claimed entries present in the snapshot, and returns the immutable provider
history. No transcript/history lock is acquired under the controller. The
provider call starts only after claim completion and runs outside the mutex.

Normal settlement cannot strand an accepted steer. Before ordinary completion
or communicate(end_turn=true) changes running to settling, BeginSettlement
checks pending steering:

- if no steer is pending, settlement constructs the accepted communicate
  packet or a bounded missing-terminal terminal_error for ordinary completion,
  appends delegate_terminal_prepared, folds settling, and later delegate_send
  is rejected as target_busy;
- if a steer is pending, normal settlement is deferred and the child must run
  another model boundary that consumes it; and
- fatal provider failure, exhaustion, or explicit stop may still terminate
  because no legal continuation exists. The accepted message remains in the
  transcript for diagnosis or a later resumable turn.

If communicate(end_turn=true) is deferred by an earlier steer, its tool result
tells the child that new owner input arrived and the activation must continue;
it does not prepare or publish a terminal packet. This gives steer and terminal
acceptance one honest controller order.

Ordinary and terminal completion then share one process-only finalization
claim. The claim fences later quiet-attention admission and captures the
completion channel of any quiet append already admitted for the exact
generation. The runtime waits on that channel outside the controller mutex.
Only after the quiet append commits or aborts may the controller revalidate the
claim and perform durable terminal preparation or finish. Stop retains both
claims and drains the quiet append before the same finalizer records the stopped
outcome; it does not delete either process claim to manufacture progress.

### Model and tool admission

Before each provider call and before each new tool execution, the child Session
asks the controller to validate its exact lease. BeginModelRequest also binds
all pending steering admissions to the request. The validation checks in one
locked operation that:

- the delegate and every ancestor remain open;
- the generation is current;
- phase admits the requested boundary; and
- no subtree stop covers the delegate.

The provider call or tool execution then runs outside the controller lock.
Stop owns the current generation's cancellation context, so work that was
admitted immediately before stop is cancelled/drained through the existing
context rather than requiring the lock to cover external execution.

The controller is also the tree-wide delegate-turn capacity authority. A
starting reservation holds capacity immediately so concurrent constructors
cannot overbook the tree. delegate_run_started converts that reservation into
the running generation; abort releases it, and delegate_run_finished releases
the running slot. Notification/attention drives are normal generations and use
the existing separately bounded drive capacity.

### Delegate reservations and shell admission receipts

Starting a descendant delegate or shell process has an external construction
boundary. A descendant delegate uses its starting reservation as the receipt;
do not layer another generic token around the same construction. A shell start
uses this process-local receipt protocol:

1. BeginWork validates the owning delegate and ancestors, records a receipt
   under the controller mutex, and returns a receipt token.
2. External preparation occurs without the mutex.
3. CommitWork reacquires the mutex. If the subtree is still open, it registers
   the exact runtime/process cancellation handle and releases the receipt. If a
   stop now covers the owner, it releases the receipt and returns
   cancel_immediately without publishing new active work.
4. AbortWork releases a failed receipt.

A subtree stop cancels and waits for descendant starting reservations, rejects
new shell receipts, and waits for existing shell receipts in that subtree to
commit or abort before the stop operation can complete. A receipt is
process-local and is not a second durable lifetime identity.

For shell work, the shell manager's existing durable launch record remains
authoritative across restart and must be committed at its established safe
boundary around process start. The controller receipt adds only subtree
admission ordering; it does not duplicate the shell record. After a crash,
delegate receipts disappear, delegate construction has made no provider call,
and shell runtime-loss reconciliation settles any durable shell launch that
survived. The implementation must not assume an OS process vanished merely
because its process-local controller receipt did.

The committed shell work remains indexed by that exact receipt token together
with its durable shell job ID until shell completion reports both identities.
Stop never tries to join an unrelated token set to a separately gathered shell
set.

## Stable-resource subtree stop

job_stop(target=dlg_...) invokes StopSubtree on the controller.

### Locked phase

While holding the one controller mutex, StopSubtree:

1. authorizes the caller against the target delegate;
2. traverses the controller's durable parent graph to identify the target and
   all descendants;
3. appends delegate_subtree_stop_requested with the stable target and uses that
   event's durable sequence as the private stop operation identity;
4. folds every active member of that subtree to stopping;
5. rejects new delegate, input, steer, model, tool, shell, watch, and delivery
   admissions under that subtree;
6. cancels in-memory starting reservations;
7. snapshots exact generation cancellation handles, live child Sessions, shell
   jobs, outstanding input/steer/model/work/watch receipts, and pre-admitted
   delivery receipts; and
8. returns an external cancellation plan.

The reducer for delegate_subtree_stop_requested applies to the generations
current at that event's sequence. It does not store or interpret a separate
subtree epoch.

The first implementation permits one pending subtree stop per root tree. An
exact retry of the same stable target joins that operation. Any different
target receives typed target_busy until the pending stop completes, regardless
of whether its subtree would be disjoint, covering, or intersecting. This
global serialization is deliberate: it avoids an overlap algebra that the
first release does not need. Root Session close first closes admission for the
whole controller, drains or joins the pending stop, and then performs the
whole-tree teardown stop.

### External phase

After releasing the mutex, the caller:

- invokes delegate and provider cancellation handles;
- asks each live descendant Session's shell-only job manager to stop its
  running shell jobs;
- requests leaf-first cancellation where ordering is observable;
- releases or waits for pre-admitted work through the existing bounded drain
  rules; and
- reports each completion back with its exact lease or shell identity.

No controller lock is held while cancelling, waiting, running hooks, emitting
events, or forwarding notifications.

### Completion

An exact generation finalizer covered by a stop records
cancelled/stopped_by_parent even if normal communicate completion raced after
the stop request. Existing terminal content may be retained for diagnostics,
but it does not replace the stop outcome.

When every active generation, shell job, and outstanding input/steer/model/
work/watch/delivery receipt in the operation's subtree has settled, the controller appends
delegate_subtree_stop_completed and changes stopped delegates to idle. New
delegate_send calls may then start a later generation.

Stop completion also waits until the read-only receiver-transcript fold finds
no pending attention entry owned inside the stopped subtree. These entries are
distinct from aggregate owner-delivery packets. While a sender remains in the
pending stop, no new owner delivery is admitted. Stop completion discards
packets whose receiver delegate is covered by that same stop, because
re-notifying a stopped ancestor would reopen the subtree; packets owed to the
root or an owner outside the subtree remain queued and become eligible only
after completion. The controller keeps new admissions closed and captures
exact transcript references plus evidence versions under its mutex. After
unlock, cleanup appends and fsyncs an idempotent discarded resolution marker
for each exact attention ID without constructing or consulting a runtime, then
re-reads the transcript fold and reports the evidence. Cancellation can commit
a new attention entry, so cleanup repeats until one final locked validation of
an immediately preceding empty transcript fold observes no active work or
receipts and can append stop completed in that same critical section.

Delivery admission serializes with stop by creating one process-local receipt
under the controller mutex after revalidating the exact head and receiver.
Inline waiter resolution and background idempotent transcript insertion then
happen after unlock. Stop waits for every receipt whose sender or receiver
intersects the subtree before its final attention rescan. A delivery therefore
either reserves before the stop request as pre-admitted work that must drain
and be cleaned, or observes stopping and cannot enter the covered receiver. A
post-unlock plan never appends attention to a receiver transcript without an
accounted receipt.

After restart there is no resident runtime or shell process to query. Root
initialization snapshots covered transcript references, shell-store paths, and
a process-only evidence version under the controller lock, then uses a
missing-file-tolerant read-only transcript fold to collect exact pending
attention IDs plus folded running-shell/pending-notification identities outside
the lock. It reads each durable shell source before its receiver transcript:
because source acknowledgement is receiver-fsync-first, that order cannot
observe both sides absent during the handoff. Shell finalization reports its
controller evidence-version change only after the terminal generation and
pending source state are durable. Reconciliation returns exact
transcript-discard and shell-store
repair plans. After unlock, the caller revalidates the exact shell records,
appends shell stopped/runtime_lost plus consumed source-notification state for
the covered stop, clears shell-only terminal watches, and appends the attention
resolution markers—all without constructing a child Session or provider.
Runtime-loss repair outside a pending stop preserves the ordinary shell
pending-notification contract. It then collects read-only evidence again and
repeats until the controller accepts the unchanged evidence version and the
final locked empty-evidence check can complete the stop. Shell-store or
transcript-resolution append failure keeps the stop pending. Transcript reads,
transcript appends, and shell-store writes never occur while the controller
mutex is held.

max_wait_ms=0 returns after the durable request and cancellation dispatch.
Positive max_wait_ms waits only up to the existing bounded limit and reports
stop_requested if completion is still pending. Stable delegate stop has no
already_idle bypass: even an apparently idle subtree appends the durable
request, fences admission, reconciles shell/attention/delivery evidence, and
appends completion. A completed no-work request may report that the previous
lifecycle was idle, but it still uses the same operation. This removes a
separate path that could miss queued delivery or notification work.

If the stop-request append fails, no cancellation is dispatched and aggregate
state is unchanged. Once the append succeeds, cancellation-plan failures leave
the stop pending and return an honest error; retry reuses the same pending
operation rather than reopening the subtree or appending a competing stop.
If a stop reconciliation driver previously returned an append error, close
joins that exact driver and retries the same pending stop. Persistent store
failure returns promptly; a successful retry clears the failed driver result
and lets close continue without inventing a second stop identity.

After restart, a requested but incomplete stop is reconstructed before any
new admission. No old runtime exists, so running generations settle
cancelled/stopped_by_parent, running shell jobs follow shell runtime-loss
reconciliation, the stop completes, and only then may a new generation start.
For a stopping member, the folded current_run_open bit is authoritative: true
appends the one stopped run_finished; false never finishes, delivers, or
releases capacity again and proceeds only with remaining stop cleanup.

## Terminal settlement and delivery

### One exact finalizer

Every runtime completion calls FinishGeneration with its delegate lease. Under
the controller mutex, runtime completion first passes BeginFinalization. That
one process claim orders both ordinary and terminal completion against quiet
attention; only the ordinary mode also arbitrates pending steering. Normal
completion then passes CompleteSettlement, which
must durably prepare either the accepted communicate packet or the canonical
missing-terminal packet before it folds settling. There is no durable settling
state without a prepared packet. Fatal, exhausted, cancelled, and stop-forced
completion does not expose such an intermediate state: if it needs a packet
while still running, FinishGeneration appends terminal_prepared and
run_finished in one crash-atomic store batch. Attention completed_no_action has
no outward packet and appends only run_finished without entering settling.
FinishGeneration then:

1. rejects a stale generation as a no-op;
2. resolves stop precedence from current controller state;
3. uses the required prepared packet when settling; for a non-settling
   terminal path, records the private disposition completed_no_action with
   public outcome completed and no outward packet when an attention generation
   legitimately had nothing to report, otherwise creates a bounded canonical
   terminal_error packet for the atomic prepare+finish batch;
4. appends delegate_run_finished with outcome, reason, timing, and a private
   delivery ID when owner delivery is required; a non-settling terminal path
   that needs a packet appends prepare+finish as one batch;
5. for an ordinary finish folds the delegate to idle; for a generation covered
   by a pending stop records the stopped outcome and releases tree capacity but
   leaves lifecycle phase stopping until delegate_subtree_stop_completed;
   creates no delivery when the owner delegate belongs to the same stop, and
   queues without dispatching the stop-selected packet for a root or owner
   outside the subtree; and
6. returns an immutable post-mutation public snapshot and, only when this
   packet is the ordered collection head, one delivery plan.

Event emission and initial delivery planning happen after unlock from the
returned immutable plans. Actual delivery re-enters the controller to validate
the exact head and receiver admission before resolving a waiter or inserting
into a transcript. A caller never unlocks and re-reads mutable controller state
to emit an earlier transition, because a later command could otherwise be
mislabeled as the earlier event.

There is no separate failure record. A failed generation has the same
delegate_run_finished event as a completed, exhausted, cancelled, or stopped
generation. Its reason is part of the outcome, and its bounded terminal_error
packet exists only because the owner must be told what happened and restart
must not invent a different result.

completed_no_action is a private disposition on delegate_run_finished, not
another record type and not a public outcome status. It is allowed only for an
attention-triggered generation whose bound durable attention entries were
successfully consumed without communicate. Its public latest outcome is
completed. A user-input
generation that ends without an accepted communicate result receives the
ordinary missing-terminal terminal_error.

The full child conversation remains in the transcript. The aggregate stores
only the bounded canonical terminal packet needed for stable owner delivery,
not a second transcript or arbitrary provider history. This deliberate bounded
duplication is simpler than a cross-file prepared-locator protocol and removes
the need to reconstruct a delivery by searching for the last communicate call.

A communicate packet preserves the accepted raw message/output value,
structured-result validation, warnings, and applicable bounded operational
metadata. A terminal_error packet preserves the outcome, stable reason, and
safe diagnostic text. Scalar, array, object, and explicit-null structured
values remain distinct. Formatting a packet for a tool result, notification,
event, TUI, or web card must not change its semantic value.

Structured-output presence is independent from the decoded value. A bounded
json.RawMessage containing null is present, valid output and must survive every
bridge exactly like a scalar, array, or object. Invalid-but-bounded structured
bytes are retained for diagnosis together with structured_result_valid=false
and the exact structured_result_reason; absence retains the established
omission semantics. No projector may infer presence from a non-nil decoded Go
value.

Exhaustion is typed. Tool-round exhaustion records outcome exhausted, reason
tool_round_budget_exhausted, the budget name and configured limit, and remains
resumable. Turn exhaustion records outcome exhausted, reason
turn_budget_exhausted, its budget name and limit, and is non-resumable; the
finish and resumability closure are one atomic delegate-store batch. Neither is
flattened to failure or cancellation. The canonical packet's terminal metadata
and the folded latest outcome carry the same exhaustion budget, limit, and
resumability. job_status(dlg_...) reads those fields from the aggregate outcome,
never from packet contents, so it remains metadata-only and non-acknowledging.

The canonical packet and aggregate also preserve task, description, agent type,
persisted resolved model, reasoning effort, run start/end, latest activity,
cumulative self-only usage, warnings, terminal worktree evidence, cleanup/
validation state, and exact result omissions. Running, quiet, and duration
values are derived from one sampled clock value per projection. Tools, events,
activity, AppWire, TUI, web, Hub cold reads, and doctor must carry those fields
losslessly or explicitly omit fields the contract marks absent; none may
reconstruct them from transient Session communicate state.

Terminal worktree evidence is sampled only after failed-generation cleanup has
joined the exact completion receipt of every owned shell or descendant job it
successfully stopped. A shell signal that merely begins asynchronous process
termination is not completion. Stop-admission or persistence failures remain
part of the failed generation's terminal diagnostic instead of being discarded.

### communicate

The dedicated communicate(end_turn=true) path:

1. validates and bounds the raw result according to the active result schema;
2. asks BeginSettlement to prove there is no earlier pending steer;
3. if a steer is pending, returns a continue result and prepares no terminal;
4. otherwise appends delegate_terminal_prepared with the exact generation and
   bounded canonical packet, then transitions running to settling;
5. returns the accepted terminal tool result to normal transcript persistence;
6. after the transcript lock is released and its append is durable, finalizes
   that same lease into delegate_run_finished; and
7. if stop won while settling, retains the prepared content only as diagnostic
   evidence and gives the stop outcome precedence.

If the process crashes after the controller accepts communicate but before the
transcript stores the tool result, restart uses the aggregate's canonical
terminal packet to repair the orphaned tool result through the existing
history-repair mechanism. It does not search by recency.

delegate_terminal_prepared is an event inside the same delegate aggregate, not
a second record or lifecycle authority. It contains the bounded packet itself,
not a cross-store locator. It is valid only for the current generation and is
used exactly once to decide delegate_run_finished. An ordinary finish clears
it. If stop changes settling to stopping, the same field remains bounded
diagnostic evidence through stopped finish and is cleared by
delegate_subtree_stop_completed; it is never delivered as the normal result.

If stop wins after preparation, the prepared communicate packet remains
diagnostic evidence, but the outward terminal packet and last_outcome describe
the stop/cancellation. Normal completion content cannot override stop
precedence.

If transcript persistence fails before terminal acceptance, the runtime
finishes with a synthetic terminal_error. If terminal acceptance is already
durable, later generic runtime errors cannot replace it.

### Owner delivery

Each outward terminal packet has one private delivery ID scoped to delegate and
generation. The ID is deterministically derived from that pair, so restart
cannot reuse it. It is never a control handle.

- At most one creating/starting call may register an inline waiter for that
  generation. The process-local waiter map is keyed by private generation (and
  therefore by its deterministic delivery ID); it is not a single current-run
  slot.
- Waiter timeout withdraws only that exact generation's waiter under the
  controller mutex before the tool returns a running result. Withdrawal wins
  only if the pending waiter is still in the keyed map.
- When a packet reaches the ordered collection head, dispatch chooses inline
  delivery by atomically removing the exact waiter from the map under the
  controller mutex and transferring sole ownership into the immutable delivery
  plan. If timeout finds that the waiter is already absent because dispatch
  claimed it, timeout loses and waits for that claim's buffered handoff instead
  of returning a running result. The post-unlock claim must always hand off
  either the packet plus one private process-only delivery completion token, or
  an explicit fallback signal that makes the tool return running while the
  durable head remains queued for notification. Timeout and head dispatch
  therefore cannot both claim the packet.
- A background completion queues the packet for later head dispatch.
- Before either inline handoff or background transcript insertion, dispatch
  re-enters the controller mutex, validates that the exact packet remains head
  and neither its sender nor delegate receiver is fenced by a pending stop,
  then creates one process-local delivery receipt. The actual waiter handoff or
  idempotent transcript insertion occurs after unlock. Inline handoff is not
  receiver commit: the caller's existing aggregated tool-result persistence
  boundary carries the private completion token until that exact tool-result
  turn has been appended and fsynced. Exact receipt completion either
  acknowledges the durable head or leaves it queued; stop waits for
  intersecting receipts, so stop and receiver admission still have one linear
  order without transcript I/O under the lifecycle lock.
- A stop-selected finish suppresses delivery to an owner delegate covered by
  the same stop. Existing queued packets to covered owners are discarded by
  stop completion. Packets for the root or an owner outside the stopped
  subtree remain ordered but are not dispatched until stop completion.
- The receiver transcript records the delivery ID as private idempotency
  metadata on the same committed tool-result or attention turn. Provider
  projection, public events, transcript rendering, TUI, and web exclude that
  metadata.
- Inline tool results force the durable transcript append even when no other
  result in that parallel tool round requires it. Only after the append fsyncs
  does the tool-result persistence boundary call
  `CompleteDelivery(committed=true)`. Append or fsync failure calls
  `CompleteDelivery(committed=false)`, propagates the persistence failure, and
  leaves the same durable head queued for later notification/replay; it never
  reports a committed inline result.
- Exact receipt completion with `committed=false` removes only the process-local
  admission and leaves the durable head queued. Only exact receipt completion
  after receiver commit appends delegate_delivery_acknowledged and releases the
  next head.
- Only the head pending delivery may be dispatched. Acknowledging that exact
  head removes it and returns the delivery plan for the next head, if any.
- Restart begins with the oldest unacknowledged packet and advances through the
  same acknowledge-then-release chain.
- Receiver-side insertion is idempotent by delivery ID, so a crash between
  receiver commit and acknowledgement finds the private delivery metadata,
  acknowledges the already-committed head, and does not create duplicate model
  input.

If selected inline delivery cannot commit because the owner Session closes or
its tool result fails, delivery remains pending and later uses the ordinary
notification/replay path. The aggregate keeps an ordered collection keyed by
delivery ID, with at most one entry per generation. A later generation may
finish while an earlier delivery remains unacknowledged; it appends a second
entry rather than overwriting the first, but receives no independent delivery
plan until every earlier entry is acknowledged. Waiter selection occurs when
an entry becomes the head and looks up that entry's generation-keyed waiter,
even if a successor generation is now current. A restart loses process-local
waiters and therefore uses notification/replay for every remaining head. There
is one durable collection of delivery state in the aggregate, not separate
inline and notification intents.

The same ownership transfer applies when acknowledgement of N releases N+1.
The controller removes N+1's keyed waiter before returning its plan. N+1 is not
dispatched merely because N's packet was handed to its caller: N's caller
tool-result turn must fsync and N's acknowledgement must commit first. A racing
N+1 timeout either withdraws first and forces notification, or observes the
claim and waits for its handoff; no delayed plan can deliver inline after the
tool has returned running.

### Watch delivery and observer callbacks

The existing watch journal owns the source side of delivery. A stable delegate
watch endpoint records the stable delegate ID and the private generation bound
when the watch is registered; no public activation handle is created. Watch
frames delivered to a delegate enter that delegate's receiver transcript as
durable attention and use the same later attention-generation path as shell or
owner delivery.

One source cursor may have a pending frame and a later coalesced update. The
crash-safe delivery order is:

1. acquire an enqueue receipt under the controller, fenced against stop;
2. fsync the complete pending frame and exact cursor in the watch journal;
3. acquire a delivery receipt and refold that exact cursor;
4. fsync one idempotent receiver-transcript attention entry keyed by delivery
   ID plus update sequence;
5. fsync the matching delivered acknowledgement in the watch journal; and
6. release the receipt before any provider execution.

The acknowledgement proves receiver durability. It may acknowledge only the
exact update sequence it delivered and cannot erase a later coalesced update.
After a crash, a transcript-fsynced/source-unacknowledged cursor is repaired by
idempotency key and acknowledged without a provider call or duplicate
attention.

Stop closes both receipt classes before cleanup. It drains receipts, resolves
pending source cursors covered by the stop, discards matching receiver
attention, refolds both journals, and repeats until immediately revalidated
evidence is stable. No post-unlock watch plan may append attention without an
accounted receipt.

An observer's accepted terminal communicate is its callback. The controller
constructs exactly one canonical terminal packet and routes that packet as the
observer callback; the same generation must not also emit an ordinary duplicate
owner packet. Callback ordering, terminal watch settlement, provenance,
runaway/budget behavior, and restart end notices stay those of the existing
watch system.

## Durable representation

### Store ownership

One versioned delegate-tree event log belongs to the root Session. All child
Sessions in that tree share the controller that owns it. The physical file may
use the existing session-state directory, but it is not folded as jobs.jsonl
and it is never opened independently by child job managers.

The first record declares the delegate-store format version. Each later JSONL
record is one small append batch containing one or more events with
monotonically assigned sequence values. Encoding a create/start pair in one
record prevents crash recovery from accepting a valid prefix of that pair. The
store fsyncs before returning success and applies the in-memory fold only after
the append succeeds. Reopen may truncate only an unterminated trailing batch;
a newline-terminated malformed batch fails closed.

Required event kinds are:

- delegate_created;
- delegate_run_started;
- delegate_terminal_prepared;
- delegate_run_finished;
- delegate_resumability_closed;
- delegate_subtree_stop_requested;
- delegate_subtree_stop_completed; and
- delegate_delivery_acknowledged.

No event kind exists for a delegate activation JobRecord, current/latest job
mapping, job stop gate, Session token mirror, subtree epoch, unload request,
close flight, or standalone failure record.

### Delegate aggregate

The durable fold retains:

- stable delegate ID;
- stable parent delegate ID or root owner;
- child Session ID and transcript ref;
- original task/description and agent type;
- model/profile, effective Config, complete task templates, tool-name ceiling,
  and result schema;
- the rich sandbox execution snapshot, its matching Config projection,
  worktree, isolation, and restore configuration;
- delegation allowance and public visibility metadata;
- per-delegate public projection revision, deterministically incremented by
  every applied event that changes that delegate's public snapshot;
- current private generation and durable running/settling/stopping phase;
- private current_run_open, set by delegate_run_started and cleared exactly
  once by delegate_run_finished; while phase is stopping this distinguishes a
  generation still requiring exact stopped finish from one already settled and
  waiting only for subtree-stop cleanup/completion;
- exactly one prepared terminal packet whenever phase is settling; phase
  stopping may retain at most that same generation's already-prepared packet
  as diagnostic evidence, while every other phase has none;
- resumability and permanent close reason;
- current run start/activity metadata;
- latest outcome;
- ordered pending terminal deliveries keyed by deterministic delivery ID; and
- pending subtree-stop membership.

Older run events remain audit history in the append-only log, but the folded
public aggregate keeps only current state and latest outcome. There is no
activation map exposed to production callers.

An append-only run event is not an activation record in the old sense. It has
no independent key, file, reducer, status API, output stream, notification
rail, cancellation route, or public projection. It is evidence used only to
fold the owning delegate aggregate.

pendingSteers is a process-local index of already-durable child-transcript
entries for the current live generation. Attention IDs bound to a drive are a
process-local working set derived from the same transcript fold. Neither is a
second message store. After process loss that generation reconciles as
runtime_lost; unresolved attention and steering entries remain in the
conversation seen by a later resumable generation.

### Flag-day state and schema cutover

This is a flag-day cutover. There is no migration, mixed loader, compatibility
window, dual writer, fallback route, or feature flag.

When opening a root, Serf always checks whether the existing root job history
contains delegate JobRecords. If it does, startup or restore fails with
legacy_delegate_state and directs the operator to use a fresh state root,
whether or not a new delegate-tree store is also present. It must not silently
ignore, translate, prefer, or partially load either side of mixed state.

The same preflight scans the existing watch journal for rows addressed through
a legacy delegate JobRecord. Any such row fails closed with
legacy_delegate_watch_state before the new controller admits work. Stable
session/shell/delegate watch rows remain in the existing watch journal and are
not migrated into the delegate store.

A root with shell-only job history may create or open the new delegate store because
the shell job schema is unchanged. A present delegate-tree store with an
unknown version fails closed.

The public tool schema cutover is atomic with the delegate controller. No
release accepts both job_id and target, returns both job_id and delegate_id, or
routes a dlg_... through old current/latest job fields.

## Restart and lazy restore

Opening a root Session first folds the delegate-tree log without constructing
child model runtimes.

Reconciliation applies in sequence order:

1. A current_run_open generation left running without a process-local runtime
   becomes failed/runtime_lost through one delegate_run_finished event.
   Process-local starting reservations disappear without creating a
   generation.
2. A current_run_open generation left settling completes from its
   delegate_terminal_prepared packet, repairs the transcript if necessary, and
   preserves the accepted communicate result.
3. A generation covered by an incomplete subtree stop settles
   cancelled/stopped_by_parent only when current_run_open is true. A stopping
   generation whose run_finished already cleared the bit is not finished,
   delivered, or capacity-released again and contributes directly to remaining
   stop cleanup/completion.
4. A reconstructed stop reads shell evidence and folds each covered receiver
   transcript outside the controller lock, performs provider-free exact
   shell-store repair plus idempotent attention-discard marker appends after
   unlock, and repeats until no covered running shell, pending shell source
   notification, or unresolved attention ID remains and a final empty-evidence
   pass completes the stop without constructing a child Session or provider.
5. Pending terminal deliveries remain ordered; only the head is offered
   idempotently to the owner, and each acknowledgement releases the next.
6. Pending stable watch cursors are repaired from the watch source before the
   receiver transcript. A receiver-fsynced/source-unacknowledged cursor is
   acknowledged idempotently; restart cancellation/end notices follow the
   existing journal contract.
7. Permanently unreachable attention is transferred only after deterministic
   ancestor attention fsync; reachable cold delegates retain their attention.
8. Permanent resumability closures remain monotonic.
9. No model client, provider request, timer, hook, nudge, salvage attempt,
   worktree mutation, or child Session is constructed during reconciliation.

After reconciliation, every delegate is idle or permanently closed. A later
delegate_send may lazily restore an idle resumable child from its descriptor
and transcript using the start protocol. Restore receives the tree controller
pointer and owning delegate ID directly; it does not reconstruct an ancestor
token vector.

Nested descriptors and parent edges live in the one tree store, so delegate
lifecycle reconciliation does not depend on recursively folding child job
stores. Existing shell runtime-loss reconciliation may open descendant
shell-job stores without constructing child model runtimes, especially while
completing an interrupted subtree stop.

Any descendant shell evidence needed for reconciliation is collected through
read-only shell-store APIs before the controller mutex is acquired. The locked
reducer receives an immutable evidence value and performs only controller
validation, controller-store append/fold, and plan capture. It never opens a
shell store, calls a Session, or performs external repair while holding the
controller mutex.

## Runtime ownership

### Resident child Sessions and bounded reclamation

A created or restored child Session normally remains attached while the root
process is live, including while the delegate is idle. Ending one generation
does not by itself call Session.Close or detach its job manager, transcript,
provider configuration, or tree-controller pointer.

The resident child Session owns conversation mechanics:

- transcript and model history;
- durable steering-turn admission and model-boundary projection;
- provider/model loop;
- tool execution;
- hooks;
- child shell-job manager; and
- sandbox/worktree execution environment.

It does not own whether the delegate is current, running, stopping, resumable,
or authorized. Those decisions belong to the controller.

The public max_retained_terminal option retains its existing spelling, default
2048, and fail-loud behavior. It bounds resident idle terminal child-runtime
subtrees, not stable history. Reclamation runs only on demand during delegate
create or cold restore when the admission would exceed the bound:

1. compute the exact number of runtime subtrees that must be reclaimed before
   minting or committing a stable ID or constructing a runtime;
2. under the controller, choose exact quiescent idle terminal subtree roots,
   preferring permanently closed, then acknowledged terminal, then oldest
   outcome, then stable ID;
3. claim those exact resident subtrees so no start, attention, shell admission,
   stop, or competing reclamation can enter them;
4. close each claimed subtree post-order after controller unlock; and
5. under the controller, clear only the exact resident runtime pointer if the
   claim still matches.

If too few reclaimable subtrees exist, admission fails before stable ID mint or
commit and before runtime construction. Reclamation never deletes or rewrites
the stable aggregate, descriptor, transcript, outcome, lineage, watch/delivery
state, or resumability. It appends no unload lifecycle event. There is no timer,
background scan, idle deadline, or autonomous unload protocol.

### Supervision and continuation behavior

The quiet watchdog remains shipped behavior. A running generation reports
latest activity through its exact lease at the existing model-retry,
awaiting-model, streaming, and tool-running boundaries. A binding timer checks
every 30 seconds against the 10-minute quiet threshold and only asks the
controller to admit an ordinary owner attention; it cannot change lifecycle,
construct a runtime, or call the provider itself. One quiet attention is
allowed per continuous quiet stretch. The quiet-notified latch is set only
after the receiver transcript append fsyncs, so append failure retries the same
private attention identity. Fresh activity re-arms the stretch. Restart starts
no timer and performs no provider call.

Generation finalization is one process-only fence for ordinary, terminal, and
stop outcomes. It rejects later quiet attention and drains any exact quiet
append admitted before the fence; the runtime waits for that drain outside the
controller lock. If a durable stop covers the exact active generation before
ordinary finalization acquires the fence, that finalization becomes the stop's
terminal owner, skips steer continuation and ordinary settlement, and records
the canonical stopped finish after the quiet append completes or aborts. If
ordinary finalization acquires the fence first, it remains ordinary when stop
arrives. Neither ordering may release the generation while a quiet append is
still in flight.

The communicate auto-nudge remains exactly once for eligible builtin delegates.
SubagentStop runs in its established order before final settlement; if it
blocks, the same generation receives exactly one continuation. A pending owner
steer accepted before the terminal boundary takes precedence over ordinary
finish and forces continuation. Cancellation, either budget exhaustion, and
stop suppress hook/nudge replay. Startup and restart never replay a hook or
nudge.

Final-round provider salvage preserves its existing guidance in the canonical
terminal packet warnings. Only an exact failed salvage on the final tool round
receives the resume hint. Success, tool-round exhaustion, turn exhaustion,
cancellation, stop, and a stale salvage callback do not.

Unreachable-child attention also remains durable behavior. A reachable,
resumable cold delegate retains its own pending attention even after transient
restore failure. When an owner is permanently unreachable, attention transfers
idempotently to the nearest reachable ancestor or root: fsync deterministic
ancestor attention before fsyncing the child discard. Consumed attention is
never transferred, and startup repair is provider-free.

### Attention drives

An idle delegate may need a model turn because one of its own shell jobs
finished or another existing owner-scoped notification arrived. Such a drive
is a normal delegate generation:

1. the source derives one stable private attention ID from its own durable
   identity: `delegate:<deliveryID>` for a delegate packet or
   `shell:<jobID>:<terminalGeneration>` for a shell terminal;
2. after controller/stop admission and outside the controller mutex, the
   receiver idempotently appends and fsyncs one model-bound steering turn with
   that private ID to its existing transcript;
3. only after that fsync does the source acknowledge its delivery; a crash
   before source acknowledgement replays by ID without appending a second
   steering turn;
4. the retained runtime requests StartAttention from the tree controller with
   its exact runtime pointer and one still-pending transcript attention ID;
5. the controller applies the same owner, ancestor, stop, and capacity checks
   as delegate_send;
6. the controller starts a private generation with trigger=attention and binds
   the exact pending IDs selected for that drive;
7. the child Session processes those model-bound transcript turns; and
8. before that generation finishes, settlement appends and fsyncs one
   provider-excluded consumed resolution marker per bound ID.

There are no recordless model-bearing drive turns. A drive is not a JobRecord,
but it is an exact delegate generation governed by the same lease.

Attention does not use a public delegate actor, because an idle resource has no
active generation lease to authenticate. The controller instead verifies that
the supplied runtime is the exact resident runtime currently bound to that
stable delegate, and narrowly verifies the pending attention ID through that
runtime's transcript fold. A missing-file-tolerant read-only fold reconstructs
pending attention as model-bound turns minus later consumed/discarded markers;
it never creates a transcript, Session, provider, or queue file. After restart,
the root may inspect and select pending IDs while cold, but lazy restore first
installs only the selected delegate's exact runtime; only then may that runtime
request an attention generation after revalidating the ID. This is runtime
identity, not a copied Session lifecycle token.

Attention append, consume, and discard are idempotent by exact ID. A duplicate
pending append with the same source identity is a no-op; exact duplicate
consumed or discarded markers are no-ops and may be retried after crash.
Conflicting content for one ID or conflicting consumed/discarded dispositions
fail closed. The resolution marker is presentational and excluded from
provider history. It is also transparent to conversation structure: compaction
and repair treat a marker inside an assistant tool-call/result exchange as an
interleaving, never as permission to cut the exchange. Removing the marker for
provider projection or compaction must leave neither an orphan tool result nor
a dangling tool call. Sequence invariants apply to the projected history as if
the marker were absent. Its private attention ID is also excluded from public
events and rendering. If consumed-marker fsync fails, the generation does not
finish and the entry remains pending. Stop performs the same durable
discarded-marker append after unlock and repeats the cold fold until none
remain.

If the delegate or an ancestor is stopping, unresolved attention remains in the
receiver transcript until stop appends its discarded marker. It must not reopen
a stopping subtree.

### Root and child close

Root Session close asks the controller to stop the entire delegate tree,
performs bounded runtime drain, closes child Sessions post-order, then closes
the delegate store. Closing a parent child Session outside root teardown uses
the same controller operation for that delegate subtree.

Session.Close remains stronger than job_stop:

- job_stop cancels current work and leaves delegates idle/resumable;
- Session.Close tears down live runtimes and ends the root process lifetime;
- authorized isolation disposal additionally closes resumability
  monotonically.

No generation finalizer may call Session.Close as ordinary completion cleanup.

## Sandbox, worktree, and resumability

The stable delegate descriptor stores the inputs required to restore the same
execution policy. Existing rules remain:

- a child cannot widen its parent's effective tool or sandbox authority;
- the rich Sandbox snapshot is canonical for full execution policy, while its
  mode and network must exactly match the effective Config projection;
- sandbox policy is re-resolved from persisted inputs and current host facts;
- a worktree delegate resumes in its original lane only when the recorded lane
  and revision policy remain valid;
- missing, pruned, disposed, or policy-invalid durable state may close
  resumability with a stable reason; and
- transient provider/runtime construction failure does not permanently close
  resumability.

Normal generation completion does not release worktree occupancy. Root close,
explicit authorized disposal, and demand-triggered exact subtree reclamation
are the only runtime-close paths. Reclamation may release process residency and
worktree occupancy without closing resumability; it is not autonomous unload.

Stable delegates participate in the existing live-work guards, root-close
cleanup, scratch retention, explicit disposal, lock provenance, dirty/D0
checks, force semantics, cleanup evidence, and idempotency. Isolation is
resolved before stable create commit so failure is deterministic. Destructive
teardown requires a durable resumability closure first. If that append fails,
nothing is destroyed. A later physical cleanup failure cannot reopen
resumability and reports retained residue, worktree evidence, validation, and
warnings honestly through every projection.

## Shell-job integration

Shell jobs keep job_... identity, durable JobRecords, output files, status,
watch support, and existing stop semantics.

When a delegate starts a shell job:

- the job records ParentDelegateID=dlg_... as its typed causal parent;
- it does not record a delegate activation JobRecord as parent;
- its external start uses a controller work receipt owned by that delegate;
- subtree stop discovers it through its live owner Session and cancels it
  through the shell-only job manager; and
- shell finalization remains authoritative in the shell job store.

Existing nested-shell forwarding may remain where required for ancestor
visibility, but a forwarded shell record never becomes delegate lifecycle
authority. Public projection resolves its typed parent to the stable delegate.

Delegate-owned shells retain their stable job identity, output/status/watch/
stop controls, exact completion attention, and direct-owner routing. Ancestors
may list and inspect them through the existing nested/descendant visibility and
owner-wins dedupe. A deeper shell remains directly controllable only through
the caller's direct stable delegate handle under the existing authorization
rule; stable lineage does not synthesize a delegate parent JobRecord.

Failed delegate finalization captures each owned job's private completion
receipt under that job manager's lock before signalling it. A manager enumerates
its durably started runs, captures their exact receipts, and accepts their stops
in one critical section, so finalization or abandonment cannot fall between an
ID snapshot and receipt capture. It signals the entire live subtree before
waiting, holds no job-manager or Session lock while joining, and samples
terminal worktree evidence only after every accepted stop has reached its exact
durable terminal boundary. This join belongs only to delegate finalization;
ordinary job_stop keeps its established nonblocking request semantics. Closing
a process receipt during root-close abandonment is not durable terminal
completion: the join returns an exact cleanup failure for that job rather than
treating teardown release as proof that process Wait and terminal persistence
finished.

Terminal attention markup follows the same identity split. Shell completion
retains `<job-notification job_id="job_..." job_type="shell">`. Delegate owner
attention uses `<delegate-notification delegate_id="dlg_...">` and never emits
`job_id` or `job_type="delegate"`. TUI and web parsers branch on the tag and
bind delegate cards/headlines by stable delegate ID; delegate copy says
"Delegate", while shell copy says "Job".

The old JobDelegate enum and delegate branches in job start/finish/stop/list
are not used for new state. They should be deleted once all call sites are
cut over; they must not remain as a dormant alternate runtime path.

## Public events and client projections

Public delegate events identify target=dlg_... and type=delegate. They may
report lifecycle, phase, outcome, reason, timestamps, transcript_ref, typed
parent, and one per-delegate projection_revision. That revision is a durable
fold counter incremented whenever an event changes that delegate's public
projection. It is ordering metadata only—not an event-log sequence, generation,
or control identity. `latest_activity_at` is the one field outside that
revision gate: steering is already durable in the conversation transcript, not
a ninth delegate lifecycle event. Live projection uses the admitted transcript
entry timestamp; cold projection derives the same hint from transcript
metadata. Clients merge it by max timestamp even when an incoming state
revision is equal or stale, and never derive lifecycle/outcome from it. They
never include:

- private generation;
- child Session ID as a control identity;
- job_id, current_job_id, latest_job_id, or resumed_from_job_id;
- stop operation ID;
- delivery ID; or
- controller/store sequence.

Event names may follow the existing event vocabulary, but a delegate lifecycle
event must not masquerade as a shell job event. Client projectors must be able
to distinguish delegate lifecycle from shell job lifecycle without consulting
ID prefixes.

Every durable controller mutation that changes the public projection first
increments the aggregate's projection_revision in the pure fold, then captures
an immutable post-mutation delegate snapshot while the controller mutex is
held and returns it as an emission plan. The caller emits that captured value
after unlock; it does not re-read the controller. Emission itself may be
reordered after unlock, so every live/cold projector and TUI/web stable-row
store keeps the greatest revision seen per delegate and ignores equal/older
lifecycle/phase/outcome fields while still merging latest_activity_at by max.
Restart reconstructs the same state revision by folding events and
reconstructs the activity hint from the transcript, so a cold snapshot and
later live event share explicit merge rules rather than letting transcript
recency become lifecycle authority.

Doctor output, AppWire, TUI, and web receive controller snapshots and project
one row/card per stable delegate. The flag-day client cutover includes live
notification ingress: the web protocol reducer invalidates the target thread's
activity view on serf/delegate/updated, and the thread router applies that
snapshot to the stable delegate module row. Shell job notifications do not
update delegate rows. A generation may contribute activity to that row or
transcript, but it does not create a second task/job row.

Root-installed descendant event callbacks survive runtime construction and
restore. A child continues to emit ordinary session, tool, hook, and turn events
through the root callback with owner-root fencing, transcript preseed,
subscription/thread-read support, and the existing late-root behavior. Spawn
configuration replacement must copy this callback explicitly. Read-only alias
routes remain rejected. The only discovery path removed is Hub inference of a
delegate through legacy Detailed.Jobs; stable descendant session/state fields
and controller snapshots remain authoritative.

Every DTO bridge is lossless for the stable projection. In particular it must
not drop explicit JSON null, validation flags/reasons, exhaustion type/budget/
limit, task/description/agent type/model/effort, timing, cumulative self-only
usage, quiet state, turn slots/delegation allowance, watch diagnostics,
warnings, or worktree/disposal evidence. Stable snapshot, lifecycle-event,
status, and cold-projection DTOs omit the call-scoped wait_ignored_reason;
delegate_send result bridges and transcript/UI renderers preserve it
separately. Cold and live paths use the same folded types; historical thread
and doctor reads use ReadEvents plus pure folds and never an append-capable
Open.

The Hub daemon prober discovers live descendants only through
`descendant_session_ids` and `descendant_states`. It does not infer delegate
runtimes from `Detailed.Jobs`, transcript references, or `job_type=delegate`;
those are removed in the same flag-day consumer cutover.

The first implementation need not redesign all AppWire DTOs. It must, however,
remove activation rows and source the existing stable delegate fields from the
controller. Any larger AppWire version change remains a separate project.

## Complexity budget and deletion inventory

This design is a complexity-reduction project, not an additive abstraction.
The controller may land only if the old delegate lifecycle is removed in the
same release.

| Concern | Current/abandoned shape | Required target |
|---|---|---|
| Durable lifecycle authority | DelegateRecord plus delegate JobRecords, with later epoch/unload overlays | One delegate aggregate fold |
| Live lifecycle authority | Session/subagent flags plus job-manager running map | Controller phase plus exact runtime binding |
| Public identities | dlg_... plus exposed/forwarded activation job_... fields | dlg_... only |
| Stale completion defense | job ID, generation strings, epoch vectors, mirrors, compare-and-swap publication | delegate ID plus uint64 generation lease |
| Stop ordering | snapshot target job, gate, recursive live walk, later cancellation | one tree lock marks subtree stopping before cancellation |
| Resume ordering | resolve current/latest job and reconcile retained runtime | controller idle-to-starting-to-running |
| Runtime residency | autonomous unload request/fence/close-flight protocol | admission-triggered exact idle-subtree reclamation with no durable unload state |
| Delegate watches | activation/job-target receiver indirection | typed stable endpoint plus existing watch journal and transcript attention |
| Restart | cross-fold job/delegate/session state | one delegate-tree fold, then lazy Session restore |

Before merge, production must no longer depend on:

- JobType=delegate;
- delegate JobRecord start, finish, output, notification, or forwarding;
- CurrentJobID or LatestJobID in DelegateRecord;
- DelegateGeneration strings used as public/durable correlation IDs;
- StopGateClosed or StopGateClosedJobID;
- findRunningDelegateByTranscriptRef;
- attachDelegateJob, attachDelegateJobWith..., or relinkDelegateChildToJob;
- delegate branches in jobManager.running;
- delegate stop by private job ID;
- Hub prober discovery through Detailed.Jobs, delegate job type, or delegate
  transcript references;
- delegate terminal markup carried as `<job-notification>`, `job_id`, or
  `job_type="delegate"`;
- ReceiverDelegateID/receiverDelegateID watch routing,
  applyReceiverWatchSend, installParentSourceWatchForChild,
  clearParentSourceWatchForChild, attachDelegateJobFromWatch, FromWatch,
  runFromWatch, deliverWatchCallback, staleDelegateWatchSend, or
  delegateStoppedAfterWatchSendPending as JobRecord-coupled or loose-receiver
  implementations; the typed stable endpoint, parent grant, source cursor,
  callback, stop fencing, and restart behavior they currently provide must have
  stable replacements before deletion;
- parent delegate lineage encoded as ParentJobID;
- Session lifecycle-admission token copies;
- job-manager lifecycle-token mirrors;
- subtree epoch vectors or epoch event folds;
- unload-request, unload-completion, or close-flight lifecycle state; or
- caller-specific exceptions that treat a missing/zero generation as a
  wildcard; or
- process queue snapshots, raw notification strings, or in-memory inboxes used
  as durable attention authority.

Tests and documentation add lines, so raw repository line count is not the
acceptance metric. The measurable complexity metric is one active lifecycle
authority, one durable fold, one private generation, one controller lock, and
zero alternate delegate-job control paths. The existing watch source journal
and shell JobRecords do not violate this metric because neither owns delegate
lifecycle.

## Implementation boundaries

### Start from main

Implementation begins in a fresh branch and worktree from then-current main.
Do not merge, rebase, or cherry-pick the abandoned
delegate-identity-integration branch.

Code from that branch may be reconsidered only when it is:

- a pure public DTO or renderer with no lifecycle dependency;
- a deterministic test that expresses a still-valid product invariant; or
- a documentation correction.

Even then, re-derive the change against main and review it normally. Do not
port lifecycle locks, epochs, unload state, activation JobRecords, admission
tokens, or compatibility scaffolding.

### One flag-day cutover, no dual runtime

Implementation may use small reviewable dormant-foundation commits, but the
release is one flag-day cutover. There is no feature flag that lets one Session
tree use delegate jobs while another uses the controller, and no adapter that
writes both event models.

A reasonable internal sequence is:

1. characterize the desired public behavior on unchanged main, recording each
   honest RED and removing intentional failing test files before ordinary
   hooks run;
2. add and test the dormant private store, fold, controller transitions,
   exact finish/delivery, steering/admission, stop, receipts, and restart
   reducer while the old production route remains the only active route;
3. cut root ownership and stable create/restore, then registered runtime,
   attention/watch/shell delivery, retention/stop/worktree/close, tools and
   read-only projections, and every AppWire/TUI/web consumer in small reviewed
   commits on one private flag-day branch;
4. delete delegate JobRecord production paths, old fields, and the inactive
   legacy implementation only after every behavior and consumer has a stable
   replacement;
5. update all remaining evergreen docs/prompts; and
6. run the full recovery, mutation, gate, inventory, and independent-review
   proof before merge.

The cutover branch is atomic as a release even though its implementation is
split into small reviewable commits. Intermediate commits after the first
production route changes may be non-deployable and must never be merged,
released, or run against durable user state alone. The final branch switches
controller ownership, state format, registered schemas, watches, projections,
clients, and legacy rejection together. Deleting already-unreachable legacy
source is not a compatibility period or second operational phase.

Foundation code may be temporarily dormant and directly tested; it may not be
selected by a feature flag or dual-written beside the old route. Intermediate
flag-day commits may temporarily switch only part of the registered path, but
they are non-deployable implementation checkpoints and must never be merged,
released, or used against durable user state alone. The final branch may not
leave a registered delegate path whose start, steering, exact finish, stop,
restart, and projection authorities disagree.

### No speculative framework

The controller remains package-internal. Do not introduce:

- a generic aggregate framework;
- a general event-sourcing library;
- a distributed actor abstraction;
- a pluggable lock service;
- a new message bus;
- a generic saga/transaction coordinator; or
- interfaces with only one implementation.

Extract a helper only when two real call sites share the same rule.

## Behavioral proof strategy

### Phase-zero characterization

Before production changes, write deterministic tests through the registered
tools and real Session/provider seams. These tests intentionally characterize
the desired product behavior, not the old implementation shape. Record each
test's honest result on current main: an already-correct behavior is a GREEN
preservation gate; a missing or broken behavior must produce a behavioral RED.
Do not weaken or distort a test merely to make main fail.

At minimum, characterize:

1. delegate returns one stable identity without activation JobRecord fields;
2. a running delegate_send is durably present in the child model's next legal
   request exactly once;
3. an idle delegate_send starts exactly one successor generation;
4. concurrent idle sends never start two model runs;
5. job_stop(target=dlg_...) cancels the current run and descendant work;
6. stop racing an idle start follows one controller order and never reports
   success while leaving the stopped generation running;
7. stop racing steering either accepts-before-stop or rejects-after-stop, with
   no lost successful send;
8. normal or communicate settlement cannot strand an earlier accepted steer;
9. a stale finalizer cannot settle or detach a successor;
10. a child/shell start admitted before stop is cancelled, while a later start
   is rejected; and
11. restart reconciliation makes no provider call;
12. descendant event callbacks survive stable runtime construction;
13. watch_parent, stable delegate sources/receivers, observer callback
    de-duplication, coalescing, stop fencing, and restart repair survive;
14. delegate-owned shell completion reaches its direct owner, retains ancestor
    visibility, and is joined before failed-generation terminal worktree
    evidence is sampled;
15. quiet supervision, communicate auto-nudge, SubagentStop continuation,
    final-round salvage, and unreachable attention preserve their exact trigger
    and suppression rules;
16. max_retained_terminal reclaims only exact quiescent resident subtrees at
    admission and fails before create side effects when insufficient;
17. positive stop wait cannot suspend or cancel the sole reconciliation driver;
18. a foreground shell timeout releases its exact controller receipt;
19. to=caller never writes directly into an unfinished root tool round;
20. explicit JSON null, validation, exhaustion, worktree evidence, turn slots,
    timing, usage, and diagnostics survive live/cold/client bridges, while
    wait_ignored_reason survives only the delegate_send result and its
    transcript/UI transport; and
21. cold activity, Hub thread reads, and doctor do not mutate any historical
    log or construct a Session/provider.

A compile failure, missing selector, timeout, or assertion against internal
function names is not behavioral RED evidence.

### Deterministic concurrency tests

Use channels, barriers, scripted providers/executors, and explicit production
seams. Do not use sleeps to create races.

Required interleavings include:

- pause idle restore after CommitStart has installed the exact non-launched
  generation; stop; release restore;
- crash a create before CommitStart and prove no child artifact exists; crash
  after CommitStart during construction and prove the aggregate owns and
  reconciles every partial deterministic path;
- pause running steer immediately before transcript admission; stop in the
  opposite ordering in a second test;
- pause normal and communicate settlement, admit steering first, and prove the
  child continues to a request containing it before terminal settlement;
- crash after ordinary completion has prepared the missing-terminal packet but
  before run_finished, and prove restart finishes that exact packet once;
- pause finalization before FinishGeneration; stop; release finalization;
- admit quiet attention, persist stop before ordinary finalization, then prove
  the finalizer drains that attention and records stopped before releasing the
  exact generation;
- finish generation N; start N+1; release a delayed N finalizer;
- hold a child-start receipt; stop its ancestor; try to commit the receipt;
- hold a shell-start receipt; stop its ancestor; publish the process handle;
- persist subtree_stop_requested; simulate restart before external
  cancellation with a descendant shell still durably running; reconcile the
  shell store to stopped/runtime_lost, consume its covered notification, and
  only then complete the stop;
- commit stopped run_finished, crash before subtree_stop_completed, reopen,
  and prove current_run_open=false prevents duplicate finish, delivery, or
  capacity release;
- hand inline packet N to its exact caller tool result, block that aggregated
  tool-result append before fsync, finish N+1, and prove neither N
  acknowledgement nor N+1 dispatch occurs; fail the append and prove
  `committed=false` leaves N then N+1 durably queued; and
- fsync caller tool-result N with its private delivery metadata, crash before
  acknowledgement, and prove replay acknowledges N without a duplicate tool
  result before releasing N+1; and
- give N+1 a live inline waiter while N's receiver is blocked, then prove N's
  post-fsync acknowledgement releases N+1 to that exact generation-keyed
  waiter rather than losing or notifying it; and
- append delegate and shell attention, crash after receiver transcript fsync
  but before source acknowledgement, and prove exact-ID replay acknowledges the
  source without a duplicate model-bound turn; fail consumed-marker fsync and
  prove attention settlement cannot finish; and
- let stop's first attention fold become empty, append a
  cancellation-generated attention entry, and prove the repeated cold fold
  durably discards it before stop completion; hold a descendant-to-covered-parent
  delivery and prove completion suppresses it, while a delivery to the root or
  an owner outside the subtree remains ordered and is released only after stop
  completion, in both live and restart orders; and
- capture public revision N, commit N+1, emit N+1 before delayed N, and prove
  live AppWire, TUI, web module, and activity stores ignore the stale snapshot;
  give delayed N a newer transcript activity timestamp and prove only ordering
  time max-merges; close/reopen and prove cold fold plus transcript metadata
  obey the same two-part merge; and
- append failure at each lifecycle event boundary, proving no in-memory
  mutation or external launch escapes.

Additional preservation interleavings must bind a watch cursor through receiver
fsync/source acknowledgement, coalesce a later update during acknowledgement,
race both watch receipt classes with stop, fire the quiet threshold under a fake
clock, block and release SubagentStop, fail final-round salvage, reclaim a
post-order runtime subtree during create, hold a foreground-shell receipt across
timeout, and block a root tool-result append while to=caller arrives. These use
scripted providers, fake clocks, barriers, channels, and durable readback. No
sleep, polling race, generated-script snapshot, compile failure, or selector
with no tests is valid RED evidence.

### Real steering proof

The steering regression must use a scripted provider with one request blocked
at a controlled boundary. It calls registered delegate_send, releases the
provider, and inspects the provider's next actual request. The message must
appear exactly once as steering/model input. Merely finding it in an in-memory
queue or transcript file is insufficient.

The test also proves:

- delegate ID resolves directly through the controller;
- no activation JobRecord was created;
- no second generation was started; and
- delegate_send returns after durable admission, not after a reply.

### Real stop proof

The stop regression must create a running coordinator with at least:

- one running descendant delegate;
- one running descendant shell job; and
- one blocked attempted child or shell start.

It calls registered job_stop with the stable delegate ID, observes the durable
stop request before cancellation signals, then proves:

- every pre-admitted run/process receives cancellation;
- the blocked start cannot publish active work;
- no provider call starts after the stop request;
- exact finalizers settle the covered generations as stopped/cancelled;
- the stop completes once receipts and active work drain; and
- a later explicit send may start one new generation.

### Restart proof

Construct a three-level delegate tree with durable descriptors, pending
delivery, a running generation, a descendant shell durably running, and an
incomplete subtree stop. Close all live
objects, reopen only the root state, and prove reconciliation:

- constructs no child Session or provider;
- makes no model request;
- settles running/stopping generations once;
- repairs the descendant shell store to stopped/runtime_lost and leaves no
  covered pending shell notification;
- completes the stop;
- suppresses packets addressed to owners covered by the stop, retains a
  pending packet owed outside the stopped subtree, and releases that external
  packet only after stop completion;
- restores stable lineage/status/list projections; and
- lazily restores only the selected delegate on a later send.

### Verification gates

The implementation plan must derive exact commands from docs/testing.md, but
the minimum final evidence includes:

- every causal selector in normal mode for at least 20 repetitions;
- the same concurrency selectors under the race detector for at least 20
  repetitions;
- controller reducer/store unit and fuzz tests;
- transcript/history-repair tests;
- shell job regression tests;
- nested recursion and tree-capacity tests;
- registered tool schema and renderer tests;
- AppWire/TUI/web/doctor projection tests;
- the complete agent package;
- all repository-required module, lint, fuzz, and generated-artifact gates;
- the authoritative native fuzz registry check after every commit that adds or
  removes a fuzz declaration;
- git diff --check; and
- clean tracked porcelain with only intentionally committed docs/code.

Mutation checks should remove or weaken generation/phase/authorization
assertions and prove the corresponding behavioral tests fail.

## Acceptance criteria

The project is complete only when all of the following are true:

1. dlg_... is the only public delegate control identity.
2. No delegate activation creates a JobRecord or job_... ID.
3. One controller owns every delegate lifecycle mutation in a root Session
   tree.
4. Child Sessions and job managers contain no copied lifecycle authority.
5. A delegate has at most one starting/running/settling/stopping generation.
6. Every runtime callback carries an exact private generation lease.
7. Stale callbacks cannot affect a successor.
8. Running delegate_send reaches the next real provider request exactly once.
9. Running delegate_send never starts a second generation.
10. Normal settlement cannot strand an earlier accepted steer.
11. Idle delegate_send starts exactly one generation or returns a typed busy
    error under concurrency.
12. job_stop accepts a stable delegate target and durably fences its subtree
    before external cancellation.
13. No new model, tool, delegate, or shell admission can commit below a
    stopping ancestor.
14. Pre-admitted external starts and owner deliveries are accounted for
    through receipts and cannot escape stop.
15. Stop completion precedes any successor generation.
16. Normal finalization cannot overwrite stop precedence; a stop that wins
    before the fence adopts the exact active finalizer and drains its admitted
    quiet append before recording the stopped outcome.
17. Restart reconciliation constructs no model runtime.
18. Pending terminal delivery replays idempotently and dispatches head-only;
    queued generations retain their own inline waiters, covered-owner packets
    are suppressed, and external packets wait for stop completion. Inline
    delivery is acknowledged only after the caller's tool-result turn fsyncs
    with private delivery metadata; append failure leaves the head queued, and
    crash after fsync replays without duplication.
19. There is no standalone failure record; every outcome uses one
    delegate_run_finished shape.
20. Public list/status/events show one delegate row and no private generation.
    Reordered equal/older projection revisions cannot regress a client row.
    Hub descendant probing uses only descendant session IDs/states, delegate
    notifications use stable delegate markup/identity, and shell notifications
    alone retain job markup/identity.
21. Public lineage uses typed stable parents.
22. Shell jobs remain independently controllable jobs.
23. Nested delegation and mandatory delegate subtree stop work at depth three.
24. Idle runtimes remain resident until root close, explicit disposal, or an
    admission-triggered max_retained_terminal reclamation. Reclamation claims
    exact quiescent subtrees, closes post-order, and deletes no durable state;
    no automatic/background unload protocol exists.
25. Stable delegate sources/receivers, watch_parent, self/parent/shell sources,
    filters/every/coalescing/budget/list/inspect/clear, terminal ordering,
    observer callbacks, restart notices, and ObservedBy metadata remain. The
    watch journal stays authoritative, a stable source binds its private
    generation, and receiver fsync precedes exact source acknowledgement.
26. Receiver transcripts are the only durable attention journal. Exact-ID
    append/source-ack, consume, discard, unreachable transfer, restart, and
    stop cleanup are idempotent, while queue snapshots remain non-authoritative.
27. Quiet supervision retains the 10-minute threshold, 30-second check, and
    once-per-stretch behavior; hooks, auto-nudge, salvage, and attention
    escalation retain their exact trigger, ordering, and replay suppression.
28. Delegate-owned shells retain completion attention, direct-owner routing,
    ancestor visibility, stable ParentDelegateID, and independent job control.
29. Canonical packets preserve explicit JSON null, invalid bounded structured
    output plus validation reason, typed exhaustion, warnings, timing, usage,
    task/model/effort, and worktree/disposal evidence across every live and cold
    projection.
30. Positive stable stop waits cannot disable the reconciliation driver;
    foreground shell timeout cannot leak its controller receipt; to=caller
    cannot write into an unfinished root tool round.
31. Historical transcript/activity/Hub/doctor reads are pure and never create,
    append, repair, truncate, or mutate a log.
32. Old delegate durable state fails legacy_delegate_state and old
    delegate-job-addressed watch state fails legacy_delegate_watch_state; no
    migration or mixed load exists.
33. Old public argument/result aliases are absent.
34. The deletion inventory has no active production references and preserved
    behavior has stable replacements before old helpers are removed.
35. The abandoned integration branch is not an ancestor of the implementation
    branch.
36. Evergreen architecture, job-control, runtime, tool, UI, and doctor docs
    describe the shipped behavior at merge.
37. All required normal, race, fuzz, lint, module, projection, and clean
    flag-day restart gates pass.

## Mandatory design stop conditions

Stop implementation and return to architectural review if correctness appears
to require any of the following:

- retaining delegate JobRecords as lifecycle authority;
- a second durable delegate projection;
- a second durable attention queue/store beside the receiver transcript;
- a Session or job-manager lifecycle mirror;
- an ancestor epoch vector;
- a wildcard or zero-generation match;
- reopening while stop is pending;
- holding the controller lock across a provider, process, hook, callback, or
  completion wait;
- autonomous/time-based unload, durable unload events, or a close-flight state
  machine instead of admission-triggered exact reclamation;
- a second watch lifecycle journal, activation-addressed stable watch API, or
  delegate-job fallback instead of the existing watch journal and typed stable
  endpoints;
- a compatibility alias or old-state migration;
- a background supervisor goroutine that owns lifecycle independently; or
- caller-specific recovery exceptions instead of controller invariants.

These conditions indicate that the implementation has drifted back toward the
abandoned architecture.

## Follow-on projects

### AppWire evolution

A later AppWire version may expose richer stable delegate activity, terminal
packets, or tree views. The core implementation only guarantees one stable
delegate resource and typed lineage.

### Lock sharding

The single tree lock may be measured after correctness ships. Sharding is
permitted only with evidence that lifecycle serialization is a material
bottleneck and with a design that retains one authoritative order for subtree
stop. It is not pre-implemented.

## Documentation authority

This document owns the proposed target architecture until implementation
ships. At merge, current-behavior contracts must also be reflected in:

- docs/architecture.md;
- docs/job-control.md;
- the shipped subagent runtime contract;
- tool reference and bundled-agent guidance;
- AppWire/TUI/web projection documentation; and
- doctor/failure-mode documentation.

Dated superpowers specs and the abandoned branch remain historical evidence.
They do not override this evergreen target.
