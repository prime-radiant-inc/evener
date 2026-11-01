# Delegate terminal reports and unfinished work

## Intent and scope

A delegate's terminal report ends a generation. It does not prove the
assignment is complete, and it does not tell the parent whether anything can
still wake the delegate. A long-running session stopped for seven hours
because its delegate reported while its own work was unfinished, the parent
read the report's prose as "still working", and no job, watch, or generation
remained to report again.

This change makes a terminal report an admission decision, not a message
attribute. The runtime publishes a terminal packet only when one authoritative
inventory of outstanding work is empty. It carries that inventory and the
delegate's task summary to every parent-facing report path. It adds no human
attention surface and no retirement blocker. When no runtime fact corroborates
a "done" claim, the parent's judgment is final.

In scope: delegate report dispositions, the terminal admission boundary, the
outstanding-work inventory, waits and readiness, the terminal packet summary,
every report-bearing delivery path, root parent dispositions, and a durable
retirement reason.

Out of scope: mandatory completion checks, task lists as retirement gates,
automatic delegate resume, runtime-generated human attention or Needs You
entries, idle-timeout changes, and unfinished work not tied to a delegate
report. An explicit parent `needs_response` keeps its existing meaning.

## Goals

- One authoritative inventory of outstanding delegate work, reused by report
  admission, wait validation, the packet summary, and retirement evidence.
- A terminal packet is published only at a serialized finalization boundary,
  after the whole tool batch, when the inventory is empty.
- A delegate can wait only on a waker with a guaranteed completion or
  cancellation signal.
- Observer readiness is a non-terminal update, not a terminal packet beside a
  live watch.
- Every report-bearing path, including inline `delegate_send` results and
  carried earlier results, carries the same task and inventory summary.
- A root's delegate-report notification turn requires an explicit
  disposition; a bare-text acknowledgement gets one existing steering round.
- No attention entry, and no blocking retirement when no work remains.

## Non-goals

- Requiring a machine-checkable done condition for every delegation.
- Treating the task list as authoritative or complete.
- Keeping a daemon resident because a report is unresolved or a task is
  stale.
- Automatically resuming a delegate or retrying a model turn.
- Interpreting a delegate's or parent's natural-language prose.
- Suppressing the automatic supervision attention that can start a new
  delegate generation after a terminal report.
- A new acceptance protocol for nested delegate parents or headless roots.

## Anti-goals

- A fix that keeps every session resident.
- A fix that blocks exit on stale task state.
- A fix that nags the human.
- A fix that publishes a terminal packet while any job, watch, delivery,
  attention, or generation can still produce a result.
- A fix that depends on the model's report text being accurate.
- A second inventory, a second disposition rail, or a second steering loop.

## Constraints

- The authoritative inventory must reuse the existing evidence:
  `Session.retirementEvidence()` and `jobManager.retirementEvidence()`
  (`agent/session_retirement_evidence.go`), the job-tree drain predicates in
  `agent/session_jobtree_drain.go`, `hasPendingDelegateDeliveries`
  (`agent/delegate_delivery.go`), and the delegate attention inventory in
  `agent/session_attention.go`. No new parallel predicate.
- The inventory must include owed watch frames and unsettled sends
  (`terminalFlush`, stable receipts and retries), not only registered watches.
- The terminal packet's existing `Kind` is the terminal marker. Do not repeat
  terminal constants in metadata.
- The delegate and root dispositions share the existing `end_reason` rail with
  audience-gated values. Do not add a second schema property or handler read.
- The bare-text steering round must reuse `handleNoToolCalls` and
  `decideNoToolCalls` with a one-round cap. Do not add a second steering loop.
- `TerminalPacket.Metadata` is the home for a versioned summary. An absent or
  undecodable summary is unknown, never an empty completed list.
- The task summary comes from `task.ListSummary` and `ViewWithError` in
  `agent/task/task_store.go`. Read it strictly. A load error is unknown.
- A delegate with a closed `result_schema` must still be able to report
  `waiting_on_work` or `blocked`; the disposition is a tool argument, outside
  the caller-controlled `output` schema.
- The automatic supervision attention path can start a new generation after a
  terminal report. The terminal notice is generation-scoped, not a promise
  that the delegate will never report again.
- Root `end_reason` is unavailable to delegates and headless roots. The
  explicit parent-disposition requirement applies to roots with a human
  partner; nested and headless parents act on the same summary mid-run.
- The finalization sequence is owned by `subagent.run` in
  `agent/subagents.go`, which already drains, takes the finalization claim,
  resolves attention, builds the packet, and completes settlement. Admission
  must precede the drain that would consume the evidence; the final
  revalidation must run under the claim immediately before publication.
- An evidence read failure makes the inventory unknown. Unknown refuses
  terminal admission; it is never treated as empty.
- Failure, cancellation, and exhaustion packets keep their existing delivery
  path. Successful-report admission applies to `PacketReported` only.
- A readiness update must resolve an inline waiter and carry a typed field
  outside the caller-controlled `output` schema.

## Behavior

### One outstanding-work inventory

Define one itemized snapshot, `outstandingDelegateWork(session)`, returning
the categories and identities below. It is computed from the existing
functions above, not from a new list:

- running shell jobs;
- registered watches, owed watch frames, unsettled sends and settlement
  retries;
- pending job notifications and terminal flush entries;
- queued inputs and steering;
- open child delegate runs and pending child delegate deliveries;
- pending child delegate attention, attention arms, and attention retries;
- an active goal.

The snapshot is used by terminal admission, wait validation, the packet
summary, and the parent's evidence. It carries IDs and categories, not a bare
boolean.

### Terminal admission at finalization

`communicate(end_turn=true)` records a terminal candidate. The disposition
decides what happens next:

- `waiting_on_work` never publishes a terminal packet. Validate the named
  waker; if it is live and has a guaranteed wake, keep the run open. If it is
  absent or already settled, refuse the wait and steer once.
- `done` and `blocked` enter terminal admission.

Terminal admission is a fenced, versioned snapshot, not a bare read. The
finalization owner is `subagent.run` in `agent/subagents.go`; admission must
precede the drain that would consume the evidence.

1. **Fence.** Take the existing finalization claim and a terminal-admission
   gate that every owner of outstanding work observes: session input and
   steering, the job manager (job start, watch registration and sends,
   terminal flush), delegate delivery and attention arming, and the goal
   store. After the gate, no new work for this run is admitted.
2. **Join.** Wait for already-admitted completion callbacks and leases that
   can add or settle work to finish. The gate must cover every callback that
   can move work between categories. If an owner cannot join, the ticket is
   invalid; do not claim a stable snapshot.
3. **Snapshot.** Collect one itemized `outstandingDelegateWork` snapshot and a
   version token: session evidence version, attention version, job-manager
   version, delegate-tree evidence version, and the primary jobs journal
   identity. If any read fails, the snapshot is unknown.
4. **Refuse or settle.** If the snapshot is non-empty, release the gate and
   claim, keep the run open, and steer once with the categories and IDs. The
   candidate is a refused terminal attempt, not a report.
5. **Drain and revalidate.** If the snapshot is empty, drain and settle
   attention and deliveries as today, then re-read the version token under the
   claim. If any version changed, release and refuse; retry admission once
   after the owner loop settles, or leave the run open if the ticket never
   stabilizes.
6. **Publish.** Only an unchanged token and an empty second snapshot publish
   the packet.

Work offered while the gate is held is not admitted to the candidate. Input and
steering stay queued in their owner; watch deliveries stay unconsumed; a job or
child completion callback joins as already-admitted work. Routing on release
depends on the outcome:

- Refusal keeps the current generation open. Queued work returns to that
  generation, and its owner loop resumes it. Do not park it for a generation
  that never starts.
- Publication transfers queued work through the next-generation path or the
  parent delivery path. The owner loop for that path resumes it.
- Cancellation ends the generation. Queued input and steering are offered to a
  replacement generation when one is already starting; otherwise they return
  to the parent delivery path as undelivered input. Watch deliveries stay with
  their owning watch and are not consumed by the cancelled generation.

The finalization claim alone does not make the snapshot consistent, because
the evidence reads use separate session, attention, job-manager, and journal
owners. The gate and version token are the consistency contract. A completion
that moves work between categories while the snapshot is in flight changes the
token and forces a refusal or retry.

If an evidence read fails, release the gate and claim, invalidate the
packet candidate, keep the run open, and record the error. Preserve the report
intent: the persisted `communicate` call for this generation, including its
disposition and payload. Bind the intent to its accepted `communicate` call
identity and the generation's durable `admission_revision`. Increment the
revision whenever work is admitted to the generation or a later report intent
replaces the earlier one; it is a revision counter, not a waiting flag. The
owner loop schedules a paced, evidence-only retry (existing timer cadence,
capped backoff) with no new user turn required. Each retry re-reads only the
evidence and version token; it does not create a turn, a report, or a packet.
A successful retry starts a fresh fenced admission for the same generation
from that intent. If the call identity or revision changed, even when the
intervening work has already finished, the retry refuses and steers; if the
generation was cancelled or superseded, discard the intent. Do not busy-loop
and do not publish. A persistent error leaves the run open and the parent sees
a non-terminal delegate.

The join is bounded. If an owner cannot join, release the gate and claim,
refuse the candidate, keep the run open, and record the stall. When the stalled
owner settles, the owner loop schedules a fresh admission retry for the same
report intent with no user event. Cancellation releases the gate before
teardown and delivers the existing cancellation packet. Never hold the gate
across an unbounded wait.

This boundary covers a same-batch `communicate` followed by a watch or job
creation, a pending child delivery, an owed watch frame, and a pending
attention generation. A published terminal packet therefore means the
inventory was empty at that boundary.

Failure, cancellation, and exhaustion packets are not admitted reports. They
keep their existing delivery path so the parent learns the generation failed
even when cleanup is still outstanding.

A later supervision attention can start a new generation and report. That is a
new generation, not a continuation of the terminal one. The packet and notice
say "this generation is final", never "this delegate will never report
again".

### Dispositions and waits

The delegate and root share the `end_reason` rail, with audience-gated values:

- delegate: `done`, `blocked`, `waiting_on_work`;
- root: `done`, `needs_response`, `waiting_on_work`.

`done` asserts the assignment is complete. `blocked` ends the generation
without completing the assignment; it must name the blocker in a
runtime-defined field outside the caller's result schema. `waiting_on_work`
is non-terminal and creates no packet.

`waiting_on_work` and `blocked` do not require a completed-result payload from
the caller's `output` schema. The disposition is a tool argument outside
`output`; a blocked report names its blocker in a runtime-defined field. A
`done` report keeps the caller's schema contract.

A wait is accepted only when its named waker has a guaranteed completion or
cancellation signal:

- a running job, whose completion notification resumes the run;
- a watch with a live delivery path, including a cancellation notice if the
  watch is removed while awaited;
- a child attention that promises a settlement or report notice; a
  `completed_no_action` child with no packet must produce a settlement notice,
  or it cannot be a wait target;
- queued input or steering.

A wait names its target in a runtime-defined `wait` field outside `output`:
`kind` is `job`, `watch`, `child`, or `input`, and `id` is the target's
durable identity. Persist the target with the existing turn record's tool call
and in `schema.CommunicateInfo`; the open generation plus that record is the
authority for restore. It is a target reference, not a waiting flag. Restore
must not infer a waker from unrelated live work. The `communicate` tool
offers the field when `end_reason=waiting_on_work`; a wait without a target is
refused.

If a watch can be removed without notifying the receiver, either add the
removal notice or refuse the wait. Do not leave a run open with no event that
can resume it.

The wait ends the turn without a terminal packet and leaves the run in the
existing open state. The existing wake and delivery path resumes it. Do not
add a new waiting phase, generation, or stored flag; the recorded target is
the only durable association.

### Observer readiness

Observer readiness is a non-terminal delegate update, not a terminal packet
beside a live watch. The update carries a typed readiness value as a tool
argument outside the caller-controlled `output` schema. `postDelegateUpdate`
must forward it, persist it with the update, and render it to the parent.

An inline `delegate_send(max_wait_ms>0)` waiter resolves on the readiness
update, not only on a terminal packet, so a parent waiting inside the send can
start the watched action. A parent in a notification turn receives the update
as a delegate update. The update does not acknowledge or supersede the
terminal delivery.

Readiness is ordered with other updates, but it must not queue behind a
deferred update that cannot flush while the parent is inside `delegate_send`.
Readiness is the one update kind allowed to claim the inline waiter. If the
parent is inside the send, readiness claims the waiter and, when earlier
deliveries are held for the parent's next round, carries them in order through
the existing held-claim path (`carriableHeldClaimLocked` or an equivalent). It
resolves the waiter inside the send, not on timeout. If no waiter is
registered, readiness is delivered normally. The readiness update carries the
same delivery acknowledgement as other updates.

Update the delegate tool description and `docs/job-control.md` to teach this
path. A parent that waits for readiness before triggering the watched action
must receive the update before it acts.

### Report summary on every path

When a terminal packet is built, attach one versioned summary to
`TerminalPacket.Metadata`:

- tasks:
  - `present` (a missing or unreadable store is false, not zeros),
  - `total`, `done`, `cancelled`, `remaining`,
  - `incomplete`: a bounded list of remaining tasks with ID, status, and
    description, ordered with the in-progress task first,
  - per-description and total byte bounds, with a truncation marker.
- inventory: the same itemized snapshot used at finalization, with IDs and
  categories.
- `report_kind`: `done` or `blocked`.
- one optional task-store owner identity; when a store is shared, summarize
  the owner's store exactly once.

Do not repeat `terminal: true`, `report_again_without_resume`, a derived
`assignment_complete`, a duplicate `current` task, or a `has_waker` boolean.
Terminality is `TerminalPacket.Kind`; assignment completion derives from
`report_kind`.

The summary has two variants selected by `TerminalPacket.Kind`:

- `PacketReported`: `report_kind` is required (`done` or `blocked`) and the
  notice is the no-outstanding-work notice.
- terminal error, cancellation, and exhaustion: `report_kind` is absent; the
  summary may carry task and inventory evidence; the notice says the
  generation ended without a successful report. Renderers must not require
  `report_kind` for these packets and must not emit the no-work notice.

Every report-bearing path projects the summary and the generation-scoped
notice:

- the durable notification;
- an inline `delegate_send(max_wait_ms>0)` result;
- each carried `earlier_results` packet.

Render the summary once. Do not duplicate the raw metadata and the rendered
lines in the same model-facing body. The notice says:

- this generation is final;
- no job, watch, attention, or delivery is outstanding at publication;
- resume the delegate if the work is unfinished;
- a future supervision attention may start a new generation;
- for `blocked`, the assignment is not complete.

### Parent handling

A root session with a human partner requires an explicit disposition when a
notification carries a delegate report:

- `done`: accepts the report as final. This is the parent's judgment, even
  when the summary shows remaining tasks.
- `waiting_on_work`: requires a live waker; refuse and steer once when none
  exists.
- `needs_response`: the parent needs the user.

A bare-text response does not settle the turn. It routes through the existing
`handleNoToolCalls`/`decideNoToolCalls` steering with a one-round cap. When
the cap is spent, the turn settles. The report stays recorded as
unacknowledged; no retirement block, no further loop, and no attention entry
follow.

Nested delegate parents and headless roots do not have the root `end_reason`
rail. They receive the same summary and notice and act mid-run. Their own
terminal report carries their task summary upward. A separate acceptance
protocol for them is out of scope.

Inline reports are consumed mid-turn; the parent sees the summary in the tool
result and does not need a separate disposition. A resumed delegate opens a
new generation, and its report supersedes the earlier one.

No durable obligation, retirement blocker, or attention entry is created by
this path. If the parent chooses `done` and no live work remains, the session
may retire under the existing idle policy.

### Restart and persistence

- A refused terminal candidate is not a durable packet. If the process stops
  before publication, restore sees an open run, not a report, and the owner
  loop reconsiders admission.
- A waiting run's authority is the target recorded on the generation's
  waiting turn: kind and ID in the persisted `CommunicateInfo` and tool call.
  Restore reads that target, then revalidates exactly it: the job journal for a
  job, the watch journal for a watch, the child attention fold for a child, and
  the input queue for queued input. It must not adopt an unrelated live job,
  watch, or child. If the target is gone, the run is open with no waker and the
  owner loop resumes or steers it. Revalidate before
  `reconcileRuntimeLostFromEvidenceLocked` can finish the generation as
  runtime-lost.
- An admitted terminal packet is durable and delivered once. Existing delivery
  recovery preserves the same generation and packet across a crash between
  preparation and delivery; it must not create a duplicate report.
- Older packets without the new summary or `report_kind` read as unknown, not
  as done. Unknown does not authorize terminal admission; failure-packet
  delivery is unaffected.

### Observability

- Record the delegate's report kind and the parent's disposition through the
  existing turn/communicate fields.
- Record a successful retirement's reason durably. The time is the record's
  own `updated_at`; do not add a second timestamp.
- Do not duplicate the packet summary in a second digest.
- Do not add a Needs You or attention row.

## Implementation boundaries

- Inventory: extract one itemized snapshot over the existing
  `agent/session_retirement_evidence.go`, `agent/session_jobtree_drain.go`,
  `agent/session_attention.go`, and `agent/delegate_delivery.go` predicates.
- Finalization: `agent/session_tool_round.go`, `agent/session_lifecycle.go`,
  `agent/delegate_tree_finish.go`, `agent/delegate_tree_intents.go`.
- Disposition and readiness: `agent/internal/tool/definitions.go`,
  `agent/session_tools_communicate.go`, `agent/schema/turn.go`,
  `agent/events/payloads.go`.
- Summary: `agent/task/task_store.go` plus one bounded helper,
  `agent/internal/delegatestore/record.go`, `agent/delegate_delivery.go`, and
  the inline projection in `agent/delegate_runtime.go`.
- Parent steering: the existing no-tool-calls decision core in
  `agent/session_tool_round.go`; do not add a parallel loop.
- Finalization owner: `agent/subagents.go` (`subagent.run`), where the
  terminal candidate, drain, finalization claim, attention settlement, packet
  construction, and delivery meet.
- Docs: `docs/job-control.md`, `docs/daemon-idle-retirement.md`, delegate
  references, and the relevant `docs/product/subsystems.md` rows.

## Implementation stages

Order the work so each stage has an observable contract and its own checks.

1. **Shared inventory snapshot and read-failure recovery.** Reuse the existing
   predicates; add the itemized snapshot, version token, and unknown
   classification. Checks: 24.
2. **Wake delivery guarantees.** Add job completion, watch-removal notice,
   child-attention settlement, readiness payload, the held-claim carry path,
   ordering and inline-waiter resolution. Checks: 4, 16, 21 and 27.
3. **Disposition rail and fenced terminal admission.** Branch on disposition
   first, add the fence, bounded join, fence-time arrival handling, version
   revalidation and unknown-evidence retry in `subagent.run`, and exempt
   failure packets. Checks: 1, 2, 3, 13, 14, 18, 19, 25 and 26.
4. **Summary variants on every path.** Add the bounded task summary and the
   reported/failure variants; render once in notifications, inline results and
   earlier results. Checks: 5, 8, 9, 15 and 20.
5. **Parent handling, wait-target restore, retirement reason and docs.** Add
   the root disposition through the existing steering core, nested/headless
   scope, wait-target restore, retirement reason and doc updates. Checks: 6, 7,
   10, 11, 12, 17, 22 and 23.

## Verification

Use TDD on the real finish, delivery, and notification paths.

1. Same-batch: a terminal candidate followed by a watch or job creation in the
   same response is refused at finalization; no terminal packet is published
   and the run stays open.
2. A terminal candidate with a pending child delivery, an owed watch frame, a
   pending attention generation, or a pending notification is refused.
3. A wait on a live job keeps the run open and resumes on completion. A wait
   on a watch that is removed receives the cancellation notice, or the wait is
   refused. A wait on a packetless child attention is refused unless a
   settlement notice exists.
4. Observer readiness: the non-terminal update reaches the parent, the parent
   starts the watched action, and the watch frame resumes the same delegate.
5. An inline `delegate_send` result and each `earlier_results` packet carry
   the same summary and generation-scoped notice. No notification-only gap.
6. A root notification with bare text takes exactly one existing steering
   round and then settles. No attention event and no retirement block. An
   explicit `done` accepts; `waiting_on_work` with no waker is refused.
7. A nested delegate parent receives the summary and notice without the root
   `end_reason` and continues its own run. A headless root likewise.
8. The packet summary has exact counts, a bounded incomplete list with the
   in-progress task first, one owner identity, and no duplicate fields. An
   absent store yields `present: false`.
9. A closed caller result schema does not block `waiting_on_work` or
   `blocked`; the disposition is outside `output`, and `blocked` names its
   blocker in a runtime field.
10. Incident replay: a delegate reports `done` with one in-progress task and
    no waker. The summary shows the task, the notice says this generation is
    final, and the parent resumes or accepts done. Accept-done may retire;
    no attention entry appears.
11. A terminal report followed by an automatic supervision attention starts a
    new generation and may report again; the notice never claims otherwise.
12. Existing late job notification behavior is unchanged: a later notification
    does not create a second report from the same terminal generation.
13. `waiting_on_work` with no live waker never publishes a terminal packet;
    it is refused and steered.
14. A terminal candidate whose inventory read fails is refused. Unknown is not
    empty.
15. Failure, cancellation and exhaustion packets are delivered with their
    summaries while cleanup remains; their notice is generation-failed, not
    no-work.
16. Readiness resolves an inline `delegate_send` waiter while the parent
    remains inside the send, not on timeout, and the parent starts the watched
    action without waiting for a terminal packet.
17. A crash between terminal preparation and delivery preserves the same
    generation and packet without a duplicate report.
18. A completion is paused during a cross-category handoff while the snapshot
    is in flight. The version token changes; admission refuses or retries and
    no terminal packet publishes.
19. A transient evidence read failure invalidates the packet candidate but
    preserves the report intent, bound to its call identity and
    `admission_revision`. The paced retry re-enters fenced admission and
    publishes when evidence is readable and the revision is unchanged.
    Intervening work, even if it has already finished by retry time, changes
    the revision and the retry refuses and steers. No new user input.
20. Failure, cancellation and exhaustion packets carry the failure summary
    variant with no `report_kind`, are delivered while cleanup remains, and do
    not emit the no-work notice.
21. Readiness carries an earlier deferred update in order through the
    held-claim path and resolves the inline waiter while the parent is still
    inside the send. It does not deadlock on the processing round.
22. Restore distinguishes a refused candidate from an admitted report and a
    waiting run. A refused candidate is not durable, a waiting run revalidates
    its recorded target, an admitted packet is delivered once, and an older
    packet without the new fields reads as unknown. With the recorded target
    gone and another unrelated waker live, restore leaves the run open with no
    waker and does not adopt the unrelated one.
23. Run targeted agent, delegate, task, and lifecycle tests. Let CI run the
    full gates; do not run competing heavy local suites.
24. Inventory snapshot: each category appears with IDs, and the version token
    changes when work moves in any category. No admission is required for this
    unit-level check.
25. A bounded join stall releases the gate and claim, refuses the candidate,
    and keeps the run open. When the stalled owner settles, the owner loop
    retries admission and publishes without a new user turn.
26. Cancellation during joining releases the gate and delivers the existing
    cancellation packet. Queued input and steering go to a replacement
    generation already starting or return to the parent delivery path as
    undelivered; watch deliveries stay with their owner and are not consumed by
    the cancelled generation. Refusal returns queued input and steering to the
    current generation; publication transfers them to the next-generation or
    parent delivery path. No queued arrival is dropped or parked for a
    generation that never starts.
27. Producer-level wake delivery: a job completion, a watch-removal notice, and
    a child settlement each resume the open generation through the existing
    wake path.

A fresh builder review reads this document before implementation starts. Any
question that changes what gets built comes back to the human partner; details
a competent builder can settle are recorded here as builder calls.
