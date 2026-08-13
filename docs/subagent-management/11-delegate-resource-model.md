# Delegate Resource Model

Status: Proposed evergreen spec. This is the clean-main recovery design for
stable delegates. Current Serf does not satisfy this document: current main
models one delegate conversation as both a DelegateRecord and a succession of
delegate JobRecords, while the abandoned delegate-identity integration branch
added more coordination around that split. The implementation must start from
current main and replace that lifecycle model. The abandoned branch is
evidence, not an implementation base.

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

The controller is a synchronous Go object protected by one mutex. It is not an
actor goroutine, detached supervisor, second session loop, or general workflow
engine. Its methods change durable aggregate state and return narrow external
actions. Provider calls, process cancellation, hooks, event emission, and
notification delivery happen after the controller mutex is released.

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
10. Delete the delegate-specific job lifecycle and the coordination mechanisms
    needed only to reconcile it.

## Non-goals and deliberate cuts

The first implementation deliberately excludes:

- automatic idle-runtime unload;
- durable watches whose source or receiver is a delegate conversation;
- watch-triggered delegate callback delivery;
- a detached subtree supervisor;
- concurrent activations for one delegate;
- a public activation history or activation read API;
- migration of old delegate JobRecords;
- aliases for old job_id arguments or old delegate result fields;
- a global state-root epoch or a mixed-version loader;
- a general rewrite of shell job management;
- a general AppWire protocol redesign beyond sourcing one stable delegate row;
  and
- performance work that requires sharded lifecycle locks.

Idle delegate runtimes remain resident until the root session closes in the
first implementation. This costs memory but removes unload, reconstruction,
and routing races from the identity cutover. Automatic unload is a separate
project after this controller has shipped and remained stable.

Existing shell watches and self/parent session watches remain in scope.
Creating a watch on a delegate target or sending a watch callback to a delegate
returns a typed unsupported error until the separate delegate-watch design is
implemented against aggregate events.

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
model, sandbox, worktree, observation, and warning metadata. Existing
background/max_wait behavior applies to that generation: a positive wait may
return its canonical terminal packet, while a timeout leaves owner delivery
pending. It never returns a job ID.

delegate_send accepts to=dlg_..., message, and max_wait_ms:

- If the delegate is running, the message is durably admitted to that
  generation's child transcript and steering queue. The call returns
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
applicable.

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
and one outcome: cancelled_by_request, already_idle, or stop_requested. If
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

job_watch in the first implementation accepts existing shell and session
sources. A dlg_... source or receiver returns
unsupported_delegate_watch. This is an intentional scope cut, not a hidden
fallback to the latest generation.

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
    stops     map[string]*delegateStop
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

### Lock and I/O rule

The controller mutex may cover:

- validation and mutation of controller state;
- append and fsync of the controller's own event store;
- a narrow child-transcript admission append that is documented to acquire
  locks only in controller-to-transcript order; and
- creation or release of process-local receipts.

No transcript path may call back into the controller while holding a
transcript lock. Transcript persistence returns first; controller settlement
or delivery acknowledgement runs afterward. This one-way lock order is a
tested invariant, not a convention left to callers.

The controller mutex must not cover:

- provider/model calls;
- process start or cancellation;
- waits for runtime completion;
- hook execution;
- event emission;
- notification delivery;
- filesystem lane creation/removal; or
- arbitrary callbacks.

The live store is private to the controller. No other production component
locks or appends it. Read-only doctor/cold-projection tooling uses the same pure
fold over a closed or snapshot log and never writes. Append succeeds before
the in-memory fold changes; append failure leaves the previous aggregate state
intact.

The controller is the outer lifecycle lock. Code must not acquire it while
holding a child Session, subagent, transcript, or shell job-manager lock.
Runtime and job callbacks snapshot what they need, release their local lock,
then report through the controller. Lock-order tests must enforce this rule.

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
| idle stop vs. send | If stop linearizes while idle, it returns already_idle and a later send may start. If send commits first, stop cancels that generation. |
| running steer vs. stop | If steer commits first, it is durably admitted and stop then cancels the run. If stop commits first, steer is rejected as stopping. |
| finalize vs. stop | If normal finish commits first, stop observes idle. If stop commits first, the exact finish records the stop outcome and cannot publish normal completion over it. |
| stale finish vs. successor | Generation mismatch is a no-op. |
| child/shell start vs. ancestor stop | A committed start is included in the stop; an uncommitted receipt is forced to abort or cancel before stop completes. |
| delayed cancellation vs. successor | No successor can start until stopping completes, so delayed cancellation cannot target a successor. |

### Create

Creation performs only validation and runtime construction before the delegate
becomes public:

1. validate the request, agent policy, sandbox, worktree request, delegation
   allowance, tree capacity, and result schema;
2. under the controller mutex, mint the stable delegate ID and reserve an
   unexposed starting state beneath the owning parent, with a construction
   cancellation context;
3. release the mutex and construct the child Session, transcript, environment,
   and runtime without making a provider call;
4. durably append the initial input to the new child transcript;
5. reacquire the controller mutex, verify the unexposed starting reservation
   is still admitted, append one batch containing delegate_created and
   delegate_run_started(generation=1), install the runtime, and fold running;
6. release the mutex and launch the first model turn; and
7. return the stable projection after input admission is durable.

An ancestor stop or root close can see and cancel the unexposed starting state.
No provider call occurs before step 5. If steps 1–4 fail, no public delegate
exists. If stop invalidates the reservation or the controller append fails,
the unexposed child runtime and newly created state are closed and removed. If
launch fails after step 5, the exact generation settles through the ordinary
failed terminal path.

The creation batch contains descriptor and lineage fields needed for lazy
restore. It does not contain a JobRecord or activation ID.

### Start an idle delegate

An idle send uses an in-memory starting reservation so runtime construction
cannot race a stop:

1. under the controller mutex, validate direct ownership, resumability,
   ancestor state, capacity, and idle phase; reserve starting and a start
   cancellation context;
2. release the mutex and reuse or restore the child runtime without calling the
   provider;
3. reacquire the mutex and verify the starting reservation still owns the
   delegate;
4. increment the generation, append delegate_run_started, install the exact
   runtime/cancel binding, and durably admit the input to the child transcript;
5. fold running, release the mutex, and launch the model turn; and
6. return action=started only after durable input admission.

If stop wins while runtime construction is outside the lock, it changes
starting to stopping and cancels construction. Step 3 then refuses the stale
reservation. The constructed runtime is closed without a provider call.

If the run-start append succeeds but child input persistence fails, the
generation settles as failed with reason input_persist_failed and the send
returns an error. A process crash after run-start but before input persistence
reconciles as runtime_lost; Serf never claims the input was accepted to a
caller whose tool result did not commit.

### Steer a running delegate

Running steering resolves the delegate directly in the controller; it never
looks up a current/latest JobRecord.

Under the controller mutex, the command:

1. validates direct ownership and phase=running;
2. verifies the live runtime carries the current lease;
3. appends the steering turn to the child transcript/queue using the fixed
   controller-to-transcript lock order;
4. records that transcript entry as pending steering for the current
   generation;
5. updates latest activity; and
6. returns success.

The append is the steering linearization point. The child loop must include
that turn at the next legal model boundary exactly once. If a provider request
is already in flight, the steer is consumed after that response and any
already-accepted tool boundary; it does not mutate an in-flight provider
request.

A successful steer means accepted into the active conversation, not that the
model produced a reply. Stop may linearize immediately afterward and cancel
the generation. A failed transcript append returns a delivery error and leaves
no accepted steer.

At each model boundary, BeginModelRequest validates the exact lease and moves
the current pending steering admissions into that request under the controller
mutex. This is the consumption point. The provider call then runs outside the
mutex.

Normal settlement cannot strand an accepted steer. Before ordinary completion
or communicate(end_turn=true) changes running to settling, BeginSettlement
checks pending steering:

- if no steer is pending, settlement wins and later delegate_send is rejected
  as target_busy;
- if a steer is pending, normal settlement is deferred and the child must run
  another model boundary that consumes it; and
- fatal provider failure, exhaustion, or explicit stop may still terminate
  because no legal continuation exists. The accepted message remains in the
  transcript for diagnosis or a later resumable turn.

If communicate(end_turn=true) is deferred by an earlier steer, its tool result
tells the child that new owner input arrived and the activation must continue;
it does not prepare or publish a terminal packet. This gives steer and terminal
acceptance one honest controller order.

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

### Child and shell admission receipts

Starting a descendant delegate or shell process has an external construction
boundary. The controller uses one process-local receipt protocol:

1. BeginWork validates the owning delegate and ancestors, records a receipt
   under the controller mutex, and returns a receipt token.
2. External preparation occurs without the mutex.
3. CommitWork reacquires the mutex. If the subtree is still open, it registers
   the exact runtime/process cancellation handle and releases the receipt. If a
   stop now covers the owner, it releases the receipt and returns
   cancel_immediately without publishing new active work.
4. AbortWork releases a failed receipt.

A subtree stop rejects new receipts and waits for existing receipts in that
subtree to commit or abort before the stop operation can complete. A receipt is
process-local and is not a second durable lifetime identity.

For shell work, the shell manager's existing durable launch record remains
authoritative across restart and must be committed at its established safe
boundary around process start. The controller receipt adds only subtree
admission ordering; it does not duplicate the shell record. After a crash,
delegate receipts disappear, delegate construction has made no provider call,
and shell runtime-loss reconciliation settles any durable shell launch that
survived. The implementation must not assume an OS process vanished merely
because its process-local controller receipt did.

For delegate creation, the starting phase is the receipt. Do not layer a second
generic receipt on the same delegate start.

## Stable-resource subtree stop

job_stop(target=dlg_...) invokes StopSubtree on the controller.

### Locked phase

While holding the one controller mutex, StopSubtree:

1. authorizes the caller against the target delegate;
2. traverses the controller's durable parent graph to identify the target and
   all descendants;
3. appends delegate_subtree_stop_requested with a private stop operation ID and
   the stable target;
4. folds every active member of that subtree to stopping;
5. rejects new delegate, model, tool, and shell admissions under that subtree;
6. cancels in-memory starting reservations;
7. snapshots exact generation cancellation handles, live child Sessions, shell
   jobs, and outstanding work receipts; and
8. returns an external cancellation plan.

The reducer for delegate_subtree_stop_requested applies to the generations
current at that event's sequence. It does not store or interpret a separate
subtree epoch.

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

When every active generation, shell job, and outstanding receipt in the
operation's subtree has settled, the controller appends
delegate_subtree_stop_completed and changes stopped delegates to idle. New
delegate_send calls may then start a later generation.

Stop completion also waits until pending attention owned inside the stopped
subtree is durably acknowledged or discarded without a model drive. The
controller keeps new admissions closed while cancellation-generated
notifications settle, then performs one final quiescent attention cleanup.
This prevents a pre-stop shell/delegate notification from reopening the
subtree immediately after stop. A terminal packet whose receiver is outside
the stopped subtree retains ordinary owner delivery.

max_wait_ms=0 returns after the durable request and cancellation dispatch.
Positive max_wait_ms waits only up to the existing bounded limit and reports
stop_requested if completion is still pending. A subtree returns already_idle
without a durable stop operation only when it has no starting/running work,
shell work, receipts, or pending attention to fence.

If the stop-request append fails, no cancellation is dispatched and aggregate
state is unchanged. Once the append succeeds, cancellation-plan failures leave
the stop pending and return an honest error; retry reuses the same pending
operation rather than reopening the subtree or appending a competing stop.

After restart, a requested but incomplete stop is reconstructed before any
new admission. No old runtime exists, so running generations settle
cancelled/stopped_by_parent, running shell jobs follow shell runtime-loss
reconciliation, the stop completes, and only then may a new generation start.

## Terminal settlement and delivery

### One exact finalizer

Every runtime completion calls FinishGeneration with its delegate lease. Under
the controller mutex, normal completion first passes BeginSettlement. Fatal,
exhausted, cancelled, and stop-forced completion may enter settling without a
continuation. FinishGeneration then:

1. rejects a stale generation as a no-op;
2. resolves stop precedence from current controller state;
3. uses a previously prepared communicate packet when present; records
   completed_no_action with no outward packet for an attention generation that
   legitimately had nothing to report; otherwise creates a bounded canonical
   terminal_error packet;
4. appends delegate_run_finished with outcome, reason, timing, a private
   delivery ID when owner delivery is required, and the canonical packet only
   when no prepared packet already exists;
5. folds the delegate to idle, records last_outcome, releases tree capacity,
   and marks delivery pending; and
6. returns a delivery plan.

Event emission and delivery happen after unlock.

There is no separate failure record. A failed generation has the same
delegate_run_finished event as a completed, exhausted, cancelled, or stopped
generation. Its reason is part of the outcome, and its bounded terminal_error
packet exists only because the owner must be told what happened and restart
must not invent a different result.

completed_no_action is a disposition on delegate_run_finished, not another
record type. It is allowed only for an attention-triggered generation whose
durable queue was successfully processed without communicate. A user-input
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
consumed exactly once by delegate_run_finished.

If stop wins after preparation, the prepared communicate packet remains
diagnostic evidence, but the outward terminal packet and last_outcome describe
the stop/cancellation. Normal completion content cannot override stop
precedence.

If transcript persistence fails before terminal acceptance, the runtime
finishes with a synthetic terminal_error. If terminal acceptance is already
durable, later generic runtime errors cannot replace it.

### Owner delivery

Each outward terminal packet has one private delivery ID scoped to delegate and
generation. It is never a control handle.

- At most one creating/starting call may register an inline waiter for that
  generation.
- Waiter timeout withdraws the waiter under the controller mutex before the
  tool returns a running result.
- FinishGeneration chooses inline delivery when the waiter still owns the
  generation; otherwise it chooses an owner notification. Timeout and finish
  therefore cannot both claim the packet.
- A background completion queues the packet as an owner notification.
- The receiver transcript records the delivery ID when it commits the tool
  result or notification.
- Only after that receiver commit does the controller append
  delegate_delivery_acknowledged.
- Restart replays an unacknowledged packet.
- Receiver-side insertion is idempotent by delivery ID, so a crash between
  receiver commit and acknowledgement does not create duplicate model input.

If selected inline delivery cannot commit because the owner Session closes or
its tool result fails, delivery remains pending and later uses the ordinary
notification/replay path. There is one delivery state in the aggregate, not
separate inline and notification intents.

The first implementation has no delegate watch/callback delivery path. It has
only inline owner delivery and ordinary owner notification.

## Durable representation

### Store ownership

One versioned delegate-tree event log belongs to the root Session. All child
Sessions in that tree share the controller that owns it. The physical file may
use the existing session-state directory, but it is not folded as jobs.jsonl
and it is never opened independently by child job managers.

The first record declares the delegate-store format version. All later records
carry a monotonically assigned sequence. The store supports atomic append of a
small event batch, fsyncs before returning success, and applies the in-memory
fold only after the append succeeds.

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
- model/profile/tool policy and result schema;
- sandbox, worktree, isolation, and restore configuration;
- delegation allowance and public visibility metadata;
- current private generation and durable running/settling/stopping phase;
- a prepared terminal packet for the current settling generation, if any;
- resumability and permanent close reason;
- current run start/activity metadata;
- latest outcome;
- pending terminal delivery; and
- pending subtree-stop membership.

Older run events remain audit history in the append-only log, but the folded
public aggregate keeps only current state and latest outcome. There is no
activation map exposed to production callers.

An append-only run event is not an activation record in the old sense. It has
no independent key, file, reducer, status API, output stream, notification
rail, cancellation route, or public projection. It is evidence used only to
fold the owning delegate aggregate.

pendingSteers is a process-local index of already-durable child-transcript
entries for the current live generation. It is not a second message store.
After process loss that generation reconciles as runtime_lost; the transcript
entries remain part of the conversation seen by a later resumable generation.

### State-version cutover

There is no migration and no mixed loader.

When opening a root that has no new delegate-tree store, Serf checks whether
the existing root job history contains delegate JobRecords. If it does, startup
or restore fails with legacy_delegate_state and directs the operator to use a
fresh state root. It must not silently ignore, translate, or partially load
those records.

A root with shell-only job history may create the new delegate store because
the shell job schema is unchanged. A present delegate-tree store with an
unknown version fails closed.

The public tool schema cutover is atomic with the delegate controller. No
release accepts both job_id and target, returns both job_id and delegate_id, or
routes a dlg_... through old current/latest job fields.

## Restart and lazy restore

Opening a root Session first folds the delegate-tree log without constructing
child model runtimes.

Reconciliation applies in sequence order:

1. A delegate_run_started generation left running without a process-local
   runtime becomes stopped/runtime_lost through one delegate_run_finished
   event. Process-local starting reservations disappear without creating a
   generation.
2. A generation left settling completes from its
   delegate_terminal_prepared packet, repairs the transcript if necessary, and
   preserves the accepted communicate result.
3. A generation covered by an incomplete subtree stop settles
   cancelled/stopped_by_parent and contributes to stop completion.
4. Pending terminal deliveries remain pending and are offered idempotently to
   the owner.
5. Permanent resumability closures remain monotonic.
6. No model client, provider request, hook, worktree mutation, or child Session
   is constructed during reconciliation.

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

## Runtime ownership

### Resident child Sessions

In the first implementation, a created or restored child Session remains
attached to its parent runtime while the root process is live, including while
the delegate is idle. Ending one generation does not call Session.Close and
does not nil or detach its job manager, transcript, provider configuration, or
tree-controller pointer.

The resident child Session owns conversation mechanics:

- transcript and model history;
- steering queue integration;
- provider/model loop;
- tool execution;
- hooks;
- child shell-job manager; and
- sandbox/worktree execution environment.

It does not own whether the delegate is current, running, stopping, resumable,
or authorized. Those decisions belong to the controller.

### Attention drives

An idle delegate may need a model turn because one of its own shell jobs
finished or another existing owner-scoped notification arrived. Such a drive
is a normal delegate generation:

1. the drive requests StartAttention from the tree controller;
2. the controller applies the same owner, ancestor, stop, and capacity checks
   as delegate_send;
3. the controller starts a private generation with trigger=attention;
4. the child Session processes its durable notification queue; and
5. the generation settles through the ordinary finish path.

There are no recordless model-bearing drive turns. A drive is not a JobRecord,
but it is an exact delegate generation governed by the same lease.

If the delegate or an ancestor is stopping, attention remains queued until the
stop completes or normal queue cleanup proves it obsolete. It must not reopen a
stopping subtree.

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
- sandbox policy is re-resolved from persisted inputs and current host facts;
- a worktree delegate resumes in its original lane only when the recorded lane
  and revision policy remain valid;
- missing, pruned, disposed, or policy-invalid durable state may close
  resumability with a stable reason; and
- transient provider/runtime construction failure does not permanently close
  resumability.

Because idle runtimes remain resident, normal generation completion does not
release worktree occupancy in the first implementation. Root close and
explicit authorized disposal retain current safety behavior. A later automatic
unload project may release idle runtime occupancy only through an exact
controller lease.

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

The old JobDelegate enum and delegate branches in job start/finish/stop/list
are not used for new state. They should be deleted once all call sites are
cut over; they must not remain as a dormant alternate runtime path.

## Public events and client projections

Public delegate events identify target=dlg_... and type=delegate. They may
report lifecycle, phase, outcome, reason, timestamps, transcript_ref, and typed
parent. They never include:

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

Doctor output, AppWire, TUI, and web receive controller snapshots and project
one row/card per stable delegate. A generation may contribute activity to that
row or transcript, but it does not create a second task/job row.

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
| Runtime unload | request/fence/close-flight/detach protocol | absent from first implementation |
| Delegate watches | activation/job-target delivery and receiver routing | absent from first implementation |
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
- parent delegate lineage encoded as ParentJobID;
- Session lifecycle-admission token copies;
- job-manager lifecycle-token mirrors;
- subtree epoch vectors or epoch event folds;
- unload-request, unload-completion, or close-flight lifecycle state; or
- caller-specific exceptions that treat a missing/zero generation as a
  wildcard.

Tests and documentation add lines, so raw repository line count is not the
acceptance metric. The measurable complexity metric is one active lifecycle
authority, one durable fold, one private generation, one controller lock, and
zero alternate delegate-job control paths.

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

### One cutover, no dual runtime

Implementation may use small reviewable commits, but the release is one
cutover. There is no feature flag that lets one Session tree use delegate jobs
while another uses the controller, and no adapter that writes both event
models.

A reasonable internal sequence is:

1. add behavioral characterization tests and the private controller/store;
2. route create, resume, steer, model admission, terminal settlement, and
   attention drives through the controller;
3. route nested delegate/shell admission and subtree stop through it;
4. cut tools and client projections to stable identity;
5. delete delegate JobRecord production paths and old fields; and
6. update evergreen docs and run full verification before merge.

This is sequencing guidance, not permission to merge a half-cut-over state.

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
11. restart reconciliation makes no provider call.

A compile failure, missing selector, timeout, or assertion against internal
function names is not behavioral RED evidence.

### Deterministic concurrency tests

Use channels, barriers, scripted providers/executors, and explicit production
seams. Do not use sleeps to create races.

Required interleavings include:

- pause idle restore before CommitStart; stop; release restore;
- pause running steer immediately before transcript admission; stop in the
  opposite ordering in a second test;
- pause normal and communicate settlement, admit steering first, and prove the
  child continues to a request containing it before terminal settlement;
- pause finalization before FinishGeneration; stop; release finalization;
- finish generation N; start N+1; release a delayed N finalizer;
- hold a child-start receipt; stop its ancestor; try to commit the receipt;
- hold a shell-start receipt; stop its ancestor; publish the process handle;
- persist subtree_stop_requested; simulate restart before external
  cancellation; reconcile;
- commit receiver delivery; crash before acknowledgement; replay without
  duplicate model input; and
- append failure at each lifecycle event boundary, proving no in-memory
  mutation or external launch escapes.

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
delivery, a running generation, and an incomplete subtree stop. Close all live
objects, reopen only the root state, and prove reconciliation:

- constructs no child Session or provider;
- makes no model request;
- settles running/stopping generations once;
- completes the stop;
- retains the pending terminal packet;
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
14. Pre-admitted external starts are accounted for through receipts and cannot
    escape stop.
15. Stop completion precedes any successor generation.
16. Normal finalization cannot overwrite stop precedence.
17. Restart reconciliation constructs no model runtime.
18. Pending terminal delivery replays idempotently.
19. There is no standalone failure record; every outcome uses one
    delegate_run_finished shape.
20. Public list/status/events show one delegate row and no private generation.
21. Public lineage uses typed stable parents.
22. Shell jobs remain independently controllable jobs.
23. Nested delegation and mandatory delegate subtree stop work at depth three.
24. Idle runtimes remain resident; no automatic unload code is introduced.
25. Delegate-source/receiver watches fail explicitly as unsupported.
26. Old delegate durable state fails closed; no migration or mixed load exists.
27. Old public argument/result aliases are absent.
28. The deletion inventory has no active production references.
29. The abandoned integration branch is not an ancestor of the implementation
    branch.
30. Evergreen architecture, job-control, runtime, tool, UI, and doctor docs
    describe the shipped behavior at merge.
31. All required normal, race, fuzz, lint, module, and projection gates pass.

## Mandatory design stop conditions

Stop implementation and return to architectural review if correctness appears
to require any of the following:

- retaining delegate JobRecords as lifecycle authority;
- a second durable delegate projection;
- a Session or job-manager lifecycle mirror;
- an ancestor epoch vector;
- a wildcard or zero-generation match;
- reopening while stop is pending;
- holding the controller lock across a provider, process, hook, callback, or
  completion wait;
- implementing automatic unload or delegate watches to make the core cutover
  work;
- a compatibility alias or old-state migration;
- a background supervisor goroutine that owns lifecycle independently; or
- caller-specific recovery exceptions instead of controller invariants.

These conditions indicate that the implementation has drifted back toward the
abandoned architecture.

## Follow-on projects

### Automatic runtime unload

After the resident-runtime design is stable, automatic unload may add one
controller command that detaches only an exact idle lease after descendant and
receipt quiescence. It must not add another lifecycle store or authority. Its
design requires separate approval and deterministic stop/resume/unload tests.

### Durable delegate watches

Delegate watches may later subscribe to stable aggregate activity and deliver
to a stable delegate inbox. They must not target private generations or
recreate delegate JobRecords. Source cursor, receiver delivery, restart, and
unload behavior require a separate design.

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
