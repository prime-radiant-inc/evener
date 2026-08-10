# Delegate Identity Simplification Design

Date: 2026-08-09
Status: Draft for written review
Scope: Delegate identity, job-control tools, notifications, runtime release, and projections

## Summary

Serf will expose one public identity per kind of work:

- `job_...` identifies one shell execution.
- `dlg_...` identifies one durable delegate conversation.

Delegate activations will no longer have public job IDs. Serf may retain private activation keys inside the supervisor, but no tool, result, notification, event, transcript rendering, UI payload, error, or prompt may expose or accept them.

The existing job-control tools will control both resource types through neutral targets. `job_status`, `job_stop`, and `job_watch` will accept shell job IDs or delegate IDs where supported. `job_list` will return one unified item array, with each delegate appearing once regardless of activation count.

A terminal delegate activation will persist an exact terminal result, make the delegate idle, and request automatic runtime unload. If the delegate has no active descendants, unload occurs immediately. If descendants remain active, Serf keeps the minimum existing coordinator runtime needed for routing and unloads it automatically when the subtree becomes quiescent. This deliberately avoids a new detached subtree-supervisor architecture.

This is a clean contract and durable-state cutover. The implementation will provide no aliases, dual schemas, legacy delegate-job lookups, state migration, or mixed-epoch loading.

## Problem

The current public API assigns two handles to a delegate:

- a stable `delegate_id` for the conversation;
- a fresh `job_id` for each activation.

A delegate permits only one active activation. The activation job ID therefore provides no public concurrency distinction. It forces callers to remember which operations use which handle, creates duplicate list rows, complicates lineage, watches, and notifications, and keeps completed child sessions resident.

The activation key remains useful internally for event folding, cancellation races, notification deduplication, and restart reconciliation. Those are supervisor concerns, not a second public resource.

## Goals

1. Give each delegate one stable public identity.
2. Reuse existing job-control tools instead of adding delegate-specific lifecycle tools.
3. Show each delegate once in `job_list` and user interfaces.
4. Preserve shell execution behavior while adopting neutral public identity and relation fields.
5. Keep delegate activations serial.
6. Preserve exact real `communicate` calls and canonical abnormal terminal errors in the child transcript.
7. Deliver the complete accepted terminal `communicate` call to the parent, never an excerpt.
8. Make that full-packet guarantee safe through an acceptance bound and reserved parent continuation capacity.
9. Unload terminal delegate runtimes automatically once their descendant subtree is quiescent.
10. Keep conversation-lifetime delegate watches active across unload and restore.
11. Retain private activation supervision without creating a public contract for its key.
12. Preserve current operational metadata not tied to activation identity.

## Non-goals

- Do not add `delegate_status`, `delegate_stop`, `delegate_watch`, or `delegate_close`.
- Do not expose activation numbers, run IDs, generations, or aliases for private activation keys.
- Do not preserve compatibility with public delegate job IDs, old argument names, or old durable job-store state.
- Do not build a detached durable subtree supervisor merely to unload an ancestor early.
- Do not delete transcripts when runtimes unload.
- Do not delete worktrees, branches, or dirty files as part of runtime release.
- Do not create a raw cross-activation output stream for delegates.
- Do not move full delegate results into `job_status`.
- Do not rewrite the supervisor merely to remove its private activation key.
- Do not add snapshot pagination, a `last_outcome` filter, or a new authorization grant system.

## Identity and Relation Model

### Shell jobs

A shell job is one execution. Its public identity is `job_...`. Its lifecycle is terminal: once completed, failed, cancelled, or stopped, it never runs again.

### Delegates

A delegate is one durable child conversation. Its public identity is `dlg_...`. Its lifecycle is:

- `running`: one activation is processing;
- `idle`: no activation is processing.

The latest activation has a separate outcome:

- `completed`;
- `failed`;
- `exhausted`;
- `cancelled`;
- `stopped`.

Lifecycle and outcome remain separate. A delegate whose latest activation failed is idle and may still be resumable.

Resumability is separate metadata:

```json
{
  "resumable": false,
  "not_resumable_reason": "isolation_disposed"
}
```

Permanent causes include disposed isolation, missing or pruned transcript state, exhausted conversation limits, and policy revocation. Transient restore failures leave `resumable: true` and do not change lifecycle. This avoids adding a third lifecycle state while preventing endless retries against a permanently unusable delegate.

### Private activation keys

The supervisor may mint a private key whenever an idle delegate starts processing. It may use that key to:

- associate cancellation with the active runtime;
- fold start and terminal events;
- order and deduplicate terminal notifications;
- reconcile a lost runtime after restart;
- bind one activation to its exact terminal transcript entry;
- prevent concurrent resume races.

Private activation keys have no stable format or public lifetime. Tests must fail if one appears in model-facing JSON, rendered output, notifications, public events, UI data, doctor reports, prompts, or errors.

### Typed public lineage

Public lineage must not use activation job IDs. Every public parent edge uses the neutral typed relation:

```json
{
  "parent": {
    "id": "dlg_...",
    "type": "delegate"
  }
}
```

or the corresponding shell relation with `id: "job_..."` and `type: "shell"`.

This relation replaces `parent_job_id` in tool results, list rows, events, AppWire, doctor output, UI projections, transcript renderings, nested-depth calculations, and scenarios. Internal records may retain private linkage, but every public fold resolves it to the owning shell or delegate resource before rendering.

### Transcript identity and terminal locator

The child session's `transcript_ref` is the archival handle for the delegate conversation. The transcript contains every activation's messages, tool calls, terminal results, and validation metadata.

Each private activation outcome persists an exact locator only after the accepted terminal tool result is durable:

- transcript/session ID;
- stable turn or entry sequence;
- tool-call ID.

Notification replay resolves the result from this locator. It must never search for “the last communicate.” The locator is private metadata and is not a control handle.

`read_transcript` accepts the session ref through its session-read path. `job:<job_id>` names shell output only. It does not accept a delegate ID or private activation key.

## Terminal Result Model

Every activation has exactly one canonical terminal result after finalization.

### Accepted communicate result

When the child successfully commits `communicate(end_turn=true)`, the result stores the exact raw tool arguments:

```json
{
  "kind": "communicate",
  "communicate": {
    "end_turn": true,
    "message": "...",
    "output": {
      "message": "...",
      "data": {},
      "artifacts": []
    }
  },
  "structured_result_validation": {
    "valid": true
  }
}
```

`communicate.output` is `any`. With a custom `result_schema`, it may instead be the schema-shaped value, for example:

```json
{
  "kind": "communicate",
  "communicate": {
    "end_turn": true,
    "message": "...",
    "output": {"verdict": "pass", "count": 3}
  },
  "structured_result_validation": {
    "valid": true
  }
}
```

Serf does not normalize custom output into the default envelope. Validation metadata is a sibling and does not alter the raw call.

### Abnormal terminal result

Cancellation, provider failure, model exhaustion, panic recovery, or restart reconciliation may end an activation before a terminal communicate call exists. Serf then appends a canonical synthetic terminal entry:

```json
{
  "kind": "terminal_error",
  "terminal_error": {
    "outcome": "stopped",
    "reason": "runtime_lost",
    "message": "...",
    "ended_at": "...",
    "transcript_ref": "local:..."
  },
  "structured_result_validation": {
    "valid": false,
    "reason": "no_terminal_communicate"
  }
}
```

The message is sanitized diagnostic prose. This envelope is explicitly Serf-generated; it must never be described as the child's communicate call.

### Full-packet safety bound

A real terminal communicate notification is complete: no delegate-specific excerpting, truncation, or summarization is allowed. To make that implementable:

1. The maximum serialized raw `communicate` arguments are 16 KiB. The lifecycle wrapper has a separate fixed allowance.
2. Every parent profile allowed to create a delegate must reserve that packet allowance plus protocol overhead.
3. Delegate creation persists the bound with the restore descriptor.
4. `communicate(end_turn=true)` serializes and validates the whole raw call before acceptance. An oversized call returns a typed `terminal_packet_too_large` tool error and does not end the activation; the child must retry with a smaller call or store large evidence as artifacts and reference it from the bounded call.
5. The parent continuation builder reserves the required capacity. It compacts parent history before injection when necessary and does not consume the delivery intent until the exact packet can be committed.

The same 16 KiB bound applies to default and custom-schema output. Serf-generated terminal errors use bounded sanitized diagnostics and fit the same reserved delivery capacity. This preserves the user's full-call guarantee without allowing a valid but permanently undeliverable packet. Exact larger evidence remains available through transcript or artifact references chosen by the child; Serf does not silently substitute those references.

## Public Tool Contract

### `delegate`

Inputs remain unchanged. This includes `task`, profile/model fields, `max_wait_ms`, delegation allowance, observation, isolation, sandbox fields, and `result_schema`.

A running result contains the stable identity and all applicable existing non-identity operational metadata:

```json
{
  "delegate_id": "dlg_...",
  "type": "delegate",
  "status": "running",
  "resumable": true,
  "running_in_background": true,
  "timed_out": false,
  "transcript_ref": "local:...",
  "model": "...",
  "sandbox": {},
  "worktree": {}
}
```

If a positive `max_wait_ms` observes terminal completion, the result reports `status: "idle"`, `last_outcome`, resumability, and:

```json
{
  "terminal_result": {
    "kind": "communicate",
    "communicate": {
      "end_turn": true,
      "message": "...",
      "output": {}
    },
    "structured_result_validation": {"valid": true}
  }
}
```

For an abnormal result, `terminal_result.kind` is `terminal_error` and contains the canonical error envelope. Applicable operational metadata remains alongside `terminal_result`.

Observer readiness fields such as `watching` and `watches`, isolated-worktree path/branch/HEAD/ahead/dirty/disposal metadata, model/sandbox echoes, and existing warning fields remain present when applicable. The identity cutover removes activation-ID fields only.

The result never contains `job_id`, `started_job_id`, `current_job_id`, `latest_job_id`, or `resumed_from_job_id`.

An activation that remains running when the tool returns is notification-armed. Inline observation does not settle that delivery intent until the parent transcript durably commits the tool result.

### `delegate_send`

`delegate_send` accepts `to: <delegate_id>`, a message, and `max_wait_ms`.

If the delegate is running, Serf delivers steering into the active activation and returns:

```json
{
  "delegate_id": "dlg_...",
  "action": "steered",
  "status": "running",
  "transcript_ref": "local:...",
  "wait_ignored_reason": "live_steer_returns_on_delivery"
}
```

A live steer does not wait for a reply and does not create another activation.

If the delegate is idle and resumable, Serf restores it and starts one activation. A positive `max_wait_ms` applies only to the activation started by that call and may return the same canonical inline terminal result as `delegate`.

If `resumable: false`, the call returns `target_not_resumable` and the durable reason. Transient restore failure returns a retryable restore error without changing resumability.

`delegate_send` never accepts or returns a job ID. Applicable model, sandbox, worktree, observer, validation, and warning metadata remains preserved.

### `job_status`

Input:

```json
{"target": "job_... or dlg_..."}
```

Both target types return `id`, `type`, and `status`. Shell status otherwise retains current fields.

Delegate status is metadata-only:

```json
{
  "id": "dlg_...",
  "type": "delegate",
  "status": "idle",
  "resumable": false,
  "not_resumable_reason": "isolation_disposed",
  "transcript_ref": "local:...",
  "last_outcome": {
    "status": "failed",
    "reason": "...",
    "ended_at": "..."
  }
}
```

A running delegate may report phase and timing metadata. Status does not return full result prose or structured data and does not consume or acknowledge a pending terminal delivery.

### `job_stop`

Input:

```json
{
  "target": "job_... or dlg_...",
  "include_children": false,
  "max_wait_ms": 0
}
```

Both target types preserve the existing expressive stop result:

- `id`, `type`, and current `status`;
- `previous_status`;
- `outcome`: `cancelled_by_request`, `already_terminal`, `completed_during_stop`, or `stop_requested`;
- reason and bounded-wait metadata.

`stopped` may remain only as a derived convenience boolean; callers use `outcome` to distinguish races.

Shell semantics remain unchanged. `include_children` is a shell opt-in.

Stopping a delegate is conversation/subtree control and always:

- requests cancellation of its active activation, if any;
- recursively requests cancellation of active descendant shell and delegate work;
- discards queued, undelivered steering from before the stop;
- durably closes the pre-stop attention wake gate so queued descendant attention cannot resurrect the coordinator.

This cascade occurs even if the target delegate is already idle. A later explicit `delegate_send` to a resumable delegate reopens the wake gate for the new activation. For delegate targets, `include_children: false` does not disable the mandatory cascade; the result states that delegate cascade semantics applied.

A positive `max_wait_ms` performs one bounded wait. If settlement remains pending, the terminal delivery intent stays armed. Stopping does not delete transcript, history, worktrees, branches, or files.

### `job_watch`

`job_watch(operation="create")` uses `target`, not `source`:

```json
{
  "operation": "create",
  "target": "self | parent | job_... | dlg_..."
}
```

`list`, `inspect`, and `clear` remain supported. Every watch projection consistently uses `target`.

Target behavior:

- `self`: public session-event predicates;
- granted `parent`: public parent-session-event predicates;
- `job_...`: shell output, progress, or event predicates;
- `dlg_...`: conversation-lifetime public session-event predicates.

Delegate-target watches accept only `events`, `event_filter`, and `every`. They reject raw output and progress predicates. They remain active while the delegate is idle and across runtime unload/restore and process restart.

All continuation state required by the predicate, including the matching-occurrence counter for `every`, is durable under the watch generation. Match-state advancement and delivery intent creation are atomic. A delivery is settled only by the existing receiver-commit acknowledgement path; restart neither resets the count nor duplicates a settled frame.

### `job_list`

`job_list` returns one newest-activity-first `items` array while retaining the current top-level orientation and supervision inventory:

```json
{
  "items": [],
  "count": 0,
  "total": 0,
  "offset": 0,
  "delegation_allowance": 2,
  "turn_slots": {},
  "watches": [],
  "recent_watches": []
}
```

Fields are present where applicable under the existing contract; the identity work does not remove watch discovery, delegation allowance, turn-slot orientation, or occupancy evidence.

Every item contains `id`, `type`, `status`, typed `parent` when applicable, visibility/depth metadata, and timestamps. Shell items retain existing execution metadata and use `id: job_...` without a duplicate `job_id`.

Delegate items use `id: dlg_...` and contain lifecycle, resumability, transcript ref, current phase/timings, latest outcome, original task/description, typed parent, and applicable operational metadata.

A delegate appears exactly once. Resume updates and reorders that item. Type and status filters operate on projected lifecycle; `status: ["failed"]` matches failed shell jobs, not idle delegates whose latest outcome failed. No `last_outcome` filter or pagination snapshot is added.

`include_nested`, `include_descendants`, `limit`, and `offset` retain their existing purpose. Ordering is descending latest activity, then ascending public ID.

## Delegate Lifecycle

### Creation

Before exposing success, creation:

1. mints `delegate_id`;
2. creates the child session and transcript;
3. persists descriptor, packet bound, ownership, authorization, and typed public lineage;
4. reserves active state and private activation metadata;
5. starts the activation;
6. returns the stable delegate projection.

Partial failure removes partial runtime state and exposes no private key.

### Steering and idle resume

A running delegate has one steering queue and one activation. An idle resume obtains the generation lock, checks resumability, restores from transcript and descriptor, and starts one activation. Concurrent resumes serialize: one starts; later calls steer or return a typed delivery error. Parallel activations are impossible.

### Terminal transition

Finalization follows this order:

1. determine whether the activation has an accepted communicate or requires a synthetic terminal error;
2. commit the exact raw communicate tool result or canonical terminal-error entry to the child transcript;
3. persist the exact transcript locator, validation metadata, activation outcome, and delivery intent under the private key;
4. project `last_outcome`, lifecycle `idle`, and resumability;
5. durably request runtime unload;
6. if the descendant subtree is quiescent, unload now; otherwise retain only the existing coordinator runtime required for descendant routing and mark unload deferred;
7. queue inline, callback, or background delivery independently.

Serf must not publish a terminal result or release runtime state before the transcript locator and delivery intent are durable.

### Descendant-aware runtime unload

This design does not detach descendants into a new supervisor. If active descendants exist, the ancestor's model turn is over and no new activation runs, but the current child session/job-manager shell remains reachable for:

- descendant listing and control routing;
- callback and notification drive-down;
- watch grants and event forwarding;
- worktree occupancy and safety scans.

The deferred ancestor may continue to block occupancy only to the extent the existing subtree requires it. When the last active descendant settles and queued descendant attention is durably routed, Serf automatically completes the pending unload. No caller flag or close tool is required.

A quiescent unload removes the child from live execution and occupancy scans while preserving transcript, identity, descriptor, policy, watches, outcome, delivery state, and filesystem contents.

### Restart

On process restart:

- a private activation recorded as running without a live runtime receives a canonical `terminal_error` with outcome `stopped` and reason `runtime_lost`;
- the exact synthetic-entry locator, outcome, and pending delivery are persisted;
- the delegate becomes idle;
- resumability is recomputed only from durable monotonic facts;
- descendant-aware unload rules are re-applied;
- pending deliveries and durable watch counters remain intact;
- later `delegate_send` restores only if resumable.

No public recovery flow uses a private activation key.

## Results, Delivery, and Notifications

### Durable delivery intent and acknowledgement

Every notification-armed activation has one durable delivery intent keyed privately and pointing to the exact terminal transcript entry. Delivery is at-least-once until receiver commit and exactly-once after commit through durable deduplication.

- Background notification settles only after the parent transcript durably contains the injected notification.
- Inline completion settles only after the parent transcript durably contains the initiating tool result.
- Observer callback settles only after the parent transcript durably contains the callback/steering result.
- Queue persistence failure is delivery failure, not success.
- On restart, any unsettled intent is replayed; the receiver checks its committed delivery ID before appending.

This avoids losing a result in the crash window between child finalization and parent transcript persistence.

### Background terminal notification

A real communicate result injects one packet:

```text
<job-notification target="dlg_..." job_type="delegate"
  event="completed" status="idle" outcome="completed"
  transcript_ref="local:...">
{"kind":"communicate","communicate":{"end_turn":true,"message":"...","output":{"message":"...","data":{},"artifacts":[]}},"structured_result_validation":{"valid":true}}
</job-notification>
```

The communicate arguments are complete and byte-for-byte equivalent after canonical JSON serialization. Serf performs no delegate-specific excerpting, truncation, or summarization. The notification wrapper also preserves applicable parent-generated operational metadata that the child cannot authoritatively report, including final isolated-worktree state and disposal hint, model/sandbox echoes, observer readiness (`watching`/`watches`), and structured-result validation. None of those fields may carry an activation ID.

An abnormal result injects the same lifecycle and operational wrapper with the canonical `kind: "terminal_error"` envelope instead of claiming a child call existed.

Shell notifications retain their bounded output-excerpt behavior.

Watch-origin observer callbacks retain their special presentation path, but use the same durable acknowledgement rule. They do not also emit a duplicate owner notification.

### Public event identity

Public shell lifecycle events identify `target: job_...`; delegate events identify `target: dlg_...`. Events contain typed public parent relations and preserve applicable outcome, reason, transcript, timing, model, sandbox, worktree, observation, and validation metadata. They never contain a private activation key.

Internal envelope generation may order and deduplicate events but is not serialized as a public control handle.

## Persistence Model

The durable delegate projection owns:

- delegate ID, child session ID, and transcript ref;
- owner, visible owner, and typed parent links;
- original task and profile configuration;
- restore descriptor, packet bound, sandbox, isolation, and worktree metadata;
- lifecycle, resumability, and permanent reason;
- private generation/current-activation state;
- latest activity and latest outcome;
- exact terminal transcript locator and validation metadata per private activation;
- durable delivery intent/acknowledgement state;
- durable watch relationships and predicate counters;
- pending descendant-aware unload state.

Private activation records may remain in the existing job store. Public folds project them into their owning delegate and never list them as jobs.

Full result content has one authoritative durable copy in the child transcript. The delivery intent stores a locator, not a second full copy. Bounded display metadata is allowed.

Shell records retain their behavior but adopt public `id` and typed parent projections.

## Authorization and Tree Visibility

Knowledge of an ID does not grant control. This work adds no new grant type.

| Operation | Shell target | Delegate target |
|---|---|---|
| list/status | Direct owner, plus visible descendants only through existing `include_nested`/`include_descendants` scope | Direct owner, plus visible descendants only through existing `include_nested`/`include_descendants` scope |
| transcript read | Existing transcript resolution policy | Existing visible child-session transcript policy; visibility is not created by the ref |
| watch | Direct owner, or an ancestor forwarding to a visible concrete descendant through the existing forwarded-watch path; no sibling/unrelated access | Direct owner, or an ancestor forwarding to a visible concrete descendant through the existing forwarded-watch path; no sibling/unrelated access |
| send | Not applicable | Direct owner only |
| stop | Direct shell control; recursive only when requested | Direct owner control; an ancestor reaches descendants only through mandatory cascade from a delegate it directly controls |
| `parent` watch target | Not applicable | Only a delegate created with `watch_parent: true` |

Visibility is not send authority. No “explicit tree grant” is invented. Nested routing uses existing ownership paths and typed public lineage. Unauthorized and hidden targets use the existing non-disclosing error policy.

## Worktree and Sandbox Semantics

Runtime unload releases occupancy; it does not perform filesystem cleanup.

- A running delegate blocks removal.
- An idle delegate with active descendants may retain the minimum coordinator blocker until subtree quiescence.
- A quiescent idle delegate does not block removal.
- Active descendant work always blocks unsafe removal.
- An isolated lane remains until authorized disposal.
- Dirty/unmerged safety gates remain unchanged.
- Disposal makes the delegate permanently non-resumable; unload never implies disposal.
- Restore re-applies persisted sandbox policy. Transient setup failure leaves the delegate resumable; missing/disposed required state does not.

## Error Contract

Tools reject targets synchronously and without hidden-resource disclosure:

- malformed prefix or wrong target kind: `invalid_request`;
- unresolved or hidden target: `target_not_found`;
- disclosed but unauthorized target: `not_controllable`;
- permanently unusable delegate: `target_not_resumable` with durable reason;
- transient runtime setup failure: retryable restore error;
- delegate watch with output/progress predicate: `invalid_request`;
- oversized terminal communicate: `terminal_packet_too_large`, activation remains running;
- concurrent restore loss: typed delivery error with no second activation.

Old fields fail schema validation. `job_status` and `job_stop` require `target`; `job_watch` create requires `target`; delegate results contain no activation job-ID fields.

## Public Surfaces

The identity, typed lineage, lifecycle, resumability, operational metadata, and terminal-result union apply consistently to:

- tool definitions and renderers;
- prompts and agent instructions;
- job-control, scenario, architecture, and worktree docs;
- terminal notifications;
- TUI and web projections;
- AppWire/RPC payloads;
- doctor reports;
- transcript renderings;
- events and audit output;
- tests, fuzz fixtures, and live scenarios.

UI rows key delegates by delegate ID. Activation history appears through the child transcript, not duplicate activity rows.

## Clean Cutover

The new contract uses a new durable-state epoch. Startup must refuse to load a nonempty incompatible job/delegate store. Deployment must stop the old process, archive or remove the old state root, and start the new runtime with fresh state.

This is intentional YAGNI: Serf will not add legacy folds, control aliases, read aliases, parent-link translation, transcript rewriting, dual writes, or migration branches.

Old state and transcripts may remain operator-managed archival bytes, but the new runtime does not load or promise API access to them. Tests must prove both fresh-epoch startup and explicit refusal of mixed or old durable state. Historical transcript text is never re-rendered as live new-epoch control data.

## Verification

Before completion, the implementation must pass normal deterministic gates and prove:

### Identity, schemas, and lineage

1. Delegate tools never emit or accept an activation job ID.
2. Status/stop/watch use `target`; old argument names fail validation.
3. Shell resources alone expose `job_...`; delegates expose `dlg_...`.
4. Every public nested relation uses typed `parent`, never `parent_job_id`.
5. Private activation keys do not escape any public surface.
6. Existing non-identity model, sandbox, worktree, observer, warning, and validation metadata remains present where applicable.

### Terminal results and delivery

7. Default-envelope and custom-schema communicate calls persist and deliver exact raw `output:any` values with separate validation metadata.
8. Failure, exhaustion, cancellation, panic recovery, and runtime loss append canonical terminal-error entries.
9. Each activation persists an exact terminal turn/entry and tool-call locator; replay never selects a later activation's result.
10. Accepted communicate packets are complete and never excerpted.
11. Oversized packets are rejected before terminal acceptance and can be retried.
12. Parent continuation construction reserves packet capacity and compacts before injection.
13. Inline, callback, and background delivery intents survive crashes until the parent transcript commit acknowledges them.
14. Receiver deduplication prevents duplicate committed delivery.
15. Metadata-only status does not consume pending delivery.
16. Observer callback delivery produces no duplicate owner notification.

### Lifecycle, stop, and resume

17. Create, steering, resume, completion, abnormal outcomes, and restart preserve one delegate ID.
18. Concurrent resume attempts create one activation.
19. Delegate stop always cascades through active descendants, including when the delegate is idle.
20. Stop durably gates pre-stop attention; explicit later send reopens the gate.
21. Stop results distinguish cancellation, prior completion, completion race, and pending stop.
22. Permanent disposal/pruning/exhaustion produces `resumable: false`; transient restore failure does not.
23. Send to a non-resumable delegate returns the durable typed error.

### Listing, visibility, and watches

24. A delegate appears once after multiple activations and is reordered on activity.
25. List responses retain count/total/offset plus watch, delegation, and occupancy orientation.
26. Nested/descendant views preserve typed lineage, depth, and per-operation authorization.
27. Visibility never grants delegate send authority.
28. `job_watch list`, inspect, and clear discover and manage unloaded delegate watches.
29. Delegate watches survive two unload/restore cycles and process restart.
30. An `every` threshold crossed across activation/restart fires exactly once.
31. Delegate targets reject output/progress predicates; shell predicates retain behavior.

### Runtime and worktrees

32. A quiescent terminal delegate unloads and releases occupancy automatically.
33. An ancestor with active descendants defers unload while preserving descendant routing, callback drive-down, listing, stop, watches, and blockers.
34. Deferred unload completes automatically after the subtree and queued attention become quiescent.
35. Runtime unload never deletes lanes/files or bypasses dirty/unmerged gates.
36. Disposal is monotonic and makes the delegate non-resumable.

### Cutover and robustness

37. New runtime starts with fresh-epoch state.
38. Startup refuses mixed or old nonempty durable state instead of translating it.
39. Race tests cover send/stop/finalize, delivery/parent commit, descendant settlement/unload, watch counter/delivery, and concurrent resume.
40. Fuzz/program tests cover malformed targets, dispatch, old-field rejection, typed lineage, terminal unions, packet bounds, and projection invariants.

## Acceptance Criteria

The design is complete when:

- shell executions are the only public `job_...` resources;
- delegates expose one `dlg_...` identity plus transcript and operational metadata;
- all lifecycle tools use neutral targets;
- public nested lineage is typed and contains no activation key;
- `job_list` shows one item per delegate without losing supervision inventory;
- every activation has an exact real communicate or canonical terminal-error result;
- accepted delegate communicate notifications are complete and receiver-safe;
- delivery survives crashes until durable parent acknowledgement;
- delegate stop safely controls its subtree;
- quiescent terminal delegates unload automatically, while active descendants defer unload without being orphaned;
- lifetime delegate watches retain exact predicate state across unload/restart;
- permanent non-resumability is explicit;
- the new runtime refuses incompatible state rather than carrying compatibility code;
- deterministic unit, integration, race, fuzz-seed, frontend, and scenario gates pass.
