# Delegate Identity Simplification Design

Date: 2026-08-09
Status: Draft for written review
Scope: Delegate identity, job-control tools, notifications, runtime release, and projections

## Summary

Serf will expose one public identity per kind of work:

- `job_...` identifies one shell execution.
- `dlg_...` identifies one durable delegate conversation.

Delegate activations will no longer have public job IDs. Serf may retain private activation keys inside the supervisor, but no tool, result, notification, event, transcript rendering, UI payload, error, or prompt may expose or accept them.

The existing `job_` tools will control both kinds of work through neutral targets. `job_status`, `job_stop`, and `job_watch` will accept shell job IDs or delegate IDs where their operation supports that target. `job_list` will return one unified item array. A delegate will appear once, regardless of how many times it has run.

When a delegate activation becomes terminal, Serf will persist its result, make the delegate idle, and unload its runtime automatically. Idle delegates will not retain working-directory or worktree occupancy. `delegate_send` will restore an idle delegate from its transcript and durable descriptor when new work arrives.

This is a clean contract cutover. The implementation will provide no aliases, dual schemas, legacy delegate-job lookups, or migration behavior.

## Problem

The current public API assigns two handles to a delegate:

- a stable `delegate_id` for the conversation;
- a fresh `job_id` for each activation.

A delegate permits only one active activation. The per-activation job ID therefore provides no concurrency distinction for public control. It forces callers to remember which operations use `delegate_id` and which use the latest `job_id`, creates multiple list rows for one delegate, complicates watch and notification identity, and leaves completed delegate sessions resident in their working directories.

The activation ID remains useful inside the supervisor for event folding, cancellation races, notification deduplication, and restart reconciliation. Those are implementation concerns. They do not justify a second public handle.

## Goals

1. Give each delegate one stable public identity.
2. Reuse the existing job-control tools instead of adding delegate-specific lifecycle tools.
3. Show each delegate once in `job_list` and user interfaces.
4. Preserve shell execution semantics and `job_...` identities while adopting the neutral `target` and `id` field names.
5. Keep delegate activations serial: at most one may run per delegate.
6. Preserve full delegate reports and structured results in the child transcript.
7. Deliver the complete terminal `communicate` call to the parent when a background delegate finishes.
8. Unload terminal delegate runtimes automatically so idle delegates do not block worktree removal.
9. Keep conversation-lifetime delegate watches active across unload and restore.
10. Retain private activation supervision without creating a public contract for its key.

## Non-goals

- Do not add `delegate_status`, `delegate_stop`, `delegate_watch`, or `delegate_close`.
- Do not expose activation numbers, run IDs, generations, or aliases for private activation keys.
- Do not preserve compatibility with public delegate job IDs or old tool argument names.
- Do not delete transcripts when runtimes unload.
- Do not delete worktrees, branches, or dirty files as part of runtime release.
- Do not create a raw cross-activation output stream for delegates.
- Do not move full delegate results into `job_status`.
- Do not rewrite the supervisor merely to remove its private activation key.

## Identity Model

### Shell jobs

A shell job is one execution. Its public identity is `job_...`. Its lifecycle is terminal: once completed, failed, cancelled, or stopped, it never runs again.

### Delegates

A delegate is one durable child conversation. Its public identity is `dlg_...`. It alternates between two lifecycle states:

- `running`: one activation is processing;
- `idle`: no activation is processing.

The most recent activation has a separate outcome:

- `completed`;
- `failed`;
- `exhausted`;
- `cancelled`;
- `stopped`.

Lifecycle and outcome must remain separate. A delegate whose last activation failed is still idle and may receive another message.

### Private activation keys

The supervisor may mint a private key each time an idle delegate starts processing. It may use that key to:

- associate cancellation with the active runtime;
- fold start and terminal events;
- order and deduplicate terminal notifications;
- reconcile a lost runtime after restart;
- capture one activation's terminal result exactly once;
- prevent concurrent resume races.

Private activation keys have no stable format or public lifetime. Serf must strip them from every public boundary. Tests must fail if a delegate activation key appears in model-facing JSON, rendered tool output, notifications, public events, UI data, doctor reports, or prompts.

### Transcript identity

The child session's `transcript_ref` is the archival handle for the delegate conversation. The transcript contains every activation's messages, tool calls, terminal `communicate` call, and structured output envelope.

The transcript, not a per-activation output log, is the authoritative full-result source.

`read_transcript` accepts that session ref through its existing session-read path. The `job:<job_id>` form names shell output only. It does not accept `job:<delegate_id>`, a bare delegate ID, or a private activation key.

## Public Tool Contract

### `delegate`

`delegate` starts a new durable delegate conversation and its first private activation.

Inputs remain unchanged:

- `task`;
- `agent_type`;
- `model`;
- `reasoning_effort`;
- `max_wait_ms`;
- `delegation_allowance`;
- `watch_parent`;
- `isolation`;
- `sandbox`;
- `sandbox_net`;
- `result_schema`.

The result contains:

```json
{
  "delegate_id": "dlg_...",
  "type": "delegate",
  "status": "running",
  "running_in_background": true,
  "timed_out": false,
  "transcript_ref": "local:..."
}
```

If a positive `max_wait_ms` observes terminal completion, the result instead reports `status: "idle"` and includes the complete observed result:

```json
{
  "delegate_id": "dlg_...",
  "type": "delegate",
  "status": "idle",
  "running_in_background": false,
  "timed_out": false,
  "last_outcome": {
    "status": "completed",
    "ended_at": "..."
  },
  "transcript_ref": "local:...",
  "output": "...",
  "structured_result": {},
  "structured_result_valid": true
}
```

Fields that do not apply may be omitted. The result never contains `job_id`, `started_job_id`, `current_job_id`, `latest_job_id`, or `resumed_from_job_id`.

An activation that remains running when the tool returns is notification-armed. An activation whose terminal result is returned inline does not later emit a duplicate terminal notification.

### `delegate_send`

`delegate_send` accepts `to: <delegate_id>` and a message.

If the delegate is running, Serf steers the message into the active activation:

```json
{
  "delegate_id": "dlg_...",
  "action": "steered",
  "status": "running",
  "transcript_ref": "local:..."
}
```

If the delegate is idle, Serf restores its runtime and starts another private activation:

```json
{
  "delegate_id": "dlg_...",
  "action": "started",
  "status": "running",
  "transcript_ref": "local:..."
}
```

A positive `max_wait_ms` applies only to an activation started by that call. If it observes terminal completion, the result becomes idle and includes the same inline result fields as `delegate`.

A live steer returns when delivery succeeds. It does not wait for a later reply and does not create another activation.

`delegate_send` never accepts or returns a job ID.

### `job_status`

The input is:

```json
{"target": "job_... or dlg_..."}
```

Serf dispatches by prefix.

Both target types return the common identity fields `id`, `type`, and `status`. A shell result uses `id: "job_..."` and otherwise retains its current lifecycle, phase, timing, exit, output-size, and transcript fields.

For a delegate, the result is metadata-only:

```json
{
  "id": "dlg_...",
  "type": "delegate",
  "status": "idle",
  "transcript_ref": "local:...",
  "last_outcome": {
    "status": "failed",
    "reason": "...",
    "ended_at": "..."
  }
}
```

A running delegate may also report `phase`, `running_for_ms`, and `quiet_for_ms`. `job_status` does not return full result prose or structured result data. Callers read the child transcript for durable evidence.

Completion remains notification-driven. Callers must not poll `job_status`.

### `job_stop`

The input is:

```json
{
  "target": "job_... or dlg_...",
  "include_children": false,
  "max_wait_ms": 0
}
```

Both target types return `id`, `type`, `status`, `stopped`, and any applicable reason. A shell result uses `id: "job_..."`; its cancellation and bounded-wait semantics remain unchanged.

For a delegate, `job_stop` atomically targets its sole active activation:

- running: request cancellation;
- idle: return a successful no-op with `status: "idle"` and `stopped: false`;
- `include_children: true`: also stop active descendant work;
- `max_wait_ms > 0`: wait only up to the supplied bound for terminal settlement.

Once the delegate becomes terminal, normal finalization unloads its runtime. `job_stop` needs no release flag.

Stopping does not delete the transcript, delegate record, result history, worktree, branch, or files.

### `job_watch`

`job_watch(operation="create")` uses `target`, not `source`:

```json
{
  "operation": "create",
  "target": "self | parent | job_... | dlg_..."
}
```

`inspect` and `clear` continue to use `watch_id`.

Target behavior:

- `self`: public session-event predicates;
- granted `parent`: public parent-session-event predicates;
- `job_...`: shell `output_match` or `progress_interval_ms` predicates;
- `dlg_...`: conversation-lifetime public session-event predicates.

A delegate-target watch supports only:

- `events`;
- `event_filter`;
- `every`.

It rejects `output_match` and `progress_interval_ms`. Serf will not invent a delegate raw-output stream across activations.

A delegate-target watch remains active while the delegate is idle, survives runtime unload and process restart, and resumes observation when the delegate runs again. It ends only when cleared, exhausted by its own policy, or made invalid by owner/session teardown.

Watch delivery remains implicit to the session that created the watch.

### `job_list`

`job_list` returns one unified, newest-activity-first `items` array. Every item contains:

- `id`;
- `type` (`shell` or `delegate`);
- `status`;
- owner/visibility fields required by the caller's tree view;
- timestamps needed for ordering and orientation.

Shell items retain existing shell execution metadata and use `id: job_...`; they do not also expose a `job_id` field.

Delegate items use `id: dlg_...` and contain:

- `status: running|idle`;
- `transcript_ref`;
- `phase`, `running_for_ms`, and `quiet_for_ms` when running;
- `last_outcome` when an activation has ended;
- parent delegate/session identity when visible;
- the original task or concise description.

A delegate appears exactly once. Resuming it updates that item and moves it according to latest activity; it never appends a second public row.

`type` and `status` filters operate on projected items. The status enum is the union of shell execution statuses and delegate lifecycle statuses. Filtering `status: ["failed"]` matches failed shell jobs, not idle delegates whose `last_outcome` failed. The contract adds no `last_outcome` filter.

`include_nested`, `include_descendants`, `limit`, and `offset` retain their existing purpose. Ordering is descending by latest activity time, then ascending by public `id` for deterministic ties. As with the current live inventory, concurrent activity may move rows between paginated calls; no snapshot token is added.

## Delegate Lifecycle

### Creation

Creation performs these durable steps before exposing success:

1. mint `delegate_id`;
2. create the child session and transcript;
3. persist the delegate descriptor and ownership links;
4. reserve the delegate's active state;
5. start the private activation;
6. return the stable delegate projection.

A failure before the activation starts removes partial runtime state. Durable failure records must not expose a private activation key.

### Steering

Only one activation may exist. A message arriving while the delegate is running enters that activation's steering queue. It cannot start another activation.

### Idle resume

A message arriving while the delegate is idle obtains the delegate's generation lock, restores the child runtime from the transcript and descriptor, and starts one activation. Concurrent resume attempts serialize: one starts the activation, and later attempts steer it or fail with a typed delivery error. They never create parallel runs.

### Terminal transition

Finalization follows this order:

1. capture the activation's terminal `communicate` call exactly once;
2. commit that call and its structured output envelope to the child transcript;
3. persist activation outcome and terminal notification intent under the private key;
4. project `last_outcome` and set the delegate lifecycle to `idle`;
5. detach the execution environment and release cwd/worktree occupancy;
6. deliver or queue the parent notification independently.

Serf must not release the runtime before transcript and terminal intent are durable. It must not retain working-directory occupancy merely to keep an idle session warm.

### Runtime unload

Runtime unload removes the idle child from live execution and worktree-occupancy scans. It preserves:

- child transcript;
- delegate identity and ownership;
- restore descriptor and policy inputs;
- delegate watches;
- terminal outcome and notification state;
- worktree and filesystem contents.

Running descendants retain their own runtime identity and continue to block unsafe worktree removal. An idle ancestor does not add another blocker.

Runtime restoration failure is a retryable invocation error. It does not delete the transcript or create a permanent close state. Generic transcript-based session resume remains independent of the delegate runtime handle.

### Restart

On process restart:

- a private activation recorded as running without a live runtime settles as `stopped` with reason `runtime_lost`;
- its delegate becomes idle and releases occupancy;
- pending notifications remain pending and deduplicated;
- delegate-target watches remain durable;
- later `delegate_send` restores from the transcript and descriptor.

No public recovery flow requires a private activation key.

## Results and Notifications

### Authoritative result

The child's terminal `communicate` call is the authoritative delegate result. It includes:

```json
{
  "end_turn": true,
  "message": "...",
  "output": {
    "message": "...",
    "data": {},
    "artifacts": []
  }
}
```

When `result_schema` is present, Serf also persists and reports validation metadata according to the existing structured-result contract.

### Inline result

If `delegate` or `delegate_send` observes terminal completion inside `max_wait_ms`, the tool result includes the full report and structured-result fields. It does not also arm a terminal notification.

### Background terminal notification

If an activation remains running when its initiating tool returns, terminal completion injects one `<job-notification>` block:

```text
<job-notification target="dlg_..." job_type="delegate"
  event="completed" status="idle" outcome="completed"
  transcript_ref="local:...">
{"communicate":{"end_turn":true,"message":"...","output":{"message":"...","data":{},"artifacts":[]}}}
</job-notification>
```

The payload contains the complete terminal `communicate` call. Serf applies no delegate-notification-specific truncation, summarization, or excerpting. The parent must not need a second read to receive the delegate's report.

The lifecycle attributes surround the full packet and include `delegate_id` through `target`, outcome, and transcript ref. They expose no activation key.

Shell notifications use `target="job_..."` and retain their bounded output-excerpt behavior.

Private activation generation provides exact ordering and duplicate suppression. It is not serialized into the notification. If several delegate notifications queue at one parent boundary, Serf preserves their durable order and emits each full packet once.

Watch-origin observer callbacks retain their special delivery path: the observer's terminal `communicate` packet is the callback, and Serf does not add a duplicate owner notification for the same activation.

### Public event identity

Public shell lifecycle events identify `target: "job_..."`. Public delegate lifecycle and session events identify `target: "dlg_..."`. Delegate events may include lifecycle status, outcome, reason, transcript ref, and public provenance, but never a private activation key.

Event consumers deduplicate and order delegate events from durable envelope metadata that the runtime does not expose as a control handle. A model-facing watch frame therefore needs only its `watch_id`, delegate target, event kind, and bounded public event payload.

## Persistence Model

The durable delegate projection owns:

- `delegate_id`;
- child session ID and `transcript_ref`;
- owner, visible owner, and parent delegate links;
- original task and agent/profile configuration;
- restore descriptor, sandbox inputs, and isolation policy;
- lifecycle status (`running|idle`);
- private generation/current-activation state;
- latest activity time;
- `last_outcome`;
- terminal notification state;
- durable watch relationships.

The implementation may continue storing private activation records in the existing job store. Those records are supervisor internals. Public folds and APIs must project them into the owning delegate record and must never list them as jobs.

Delegate result prose and structured communicate data live in the child transcript. The delegate projection may store bounded display metadata but must not create a second authoritative full-result copy.

Shell job records remain unchanged.

## Authorization and Tree Visibility

Knowledge of a `job_` or `dlg_` string does not grant control.

- Shell control follows existing job ownership and visibility rules.
- Delegate status, stop, send, and watch operations require direct ownership or an existing explicit tree grant.
- A parent may list visible descendant delegates when `include_descendants` is enabled.
- Nested delegates appear as stable delegate items, not as activation rows.
- `parent` remains available as a watch target only to delegates created with `watch_parent=true`.

Errors must not reveal hidden target existence across ownership boundaries.

## Worktree and Sandbox Semantics

Runtime unload releases occupancy; it does not perform filesystem cleanup.

For a shared worktree:

- a running delegate blocks removal;
- an idle delegate does not;
- running descendant work still blocks removal;
- terminal finalization removes the idle delegate's live environment from occupancy scans.

For an isolated delegate lane:

- terminal finalization releases runtime occupancy;
- the lane remains until `manage_worktree dispose` or another authorized cleanup path removes it;
- dirty and unmerged safety gates remain unchanged;
- runtime unload never implies lane deletion.

Restore re-applies the delegate's persisted sandbox policy inputs. A restore error leaves the transcript intact and returns a typed error to the invocation.

## Error Contract

Tools reject targets synchronously and specifically:

- malformed prefix: `invalid_request`;
- well-formed target that does not resolve within the caller's visible scope: `target_not_found`;
- known but unauthorized target when policy permits disclosure: `not_controllable`; otherwise use the same non-disclosing `target_not_found` response;
- transcript or watch ref passed as lifecycle target: `invalid_request`;
- `self` or `parent` passed to status/stop: `invalid_request`;
- delegate watch with output/progress predicate: `invalid_request`;
- stop of idle delegate: successful no-op, not an error;
- concurrent restore loss: typed delivery/restore error with no second activation;
- missing runtime environment: retryable restore failure; transcript remains available.

Tool schemas remove:

- `job_id` from `job_status` and `job_stop`, replaced by `target`;
- `source` from `job_watch`, replaced by `target`;
- every delegate job-ID result field.

The cutover provides no aliases. Old field names fail schema validation.

## Public Surfaces

The following surfaces must adopt the same identity model:

- tool definitions and result renderers;
- system prompts and bundled agent prompts;
- `docs/job-control.md` and scenario documentation;
- terminal notification rendering;
- TUI and web activity/session-tree projections;
- appwire/RPC job-list and status payloads;
- doctor job, watch, and tree reports;
- transcript markdown/outline rendering;
- event payloads and audit output;
- tests, fuzz fixtures, and live scenarios.

UI rows key delegates by `delegate_id`. Activation history appears through the child transcript, not duplicate activity rows.

## Clean Cutover

This design requires no backward compatibility.

The implementation must not:

- accept old delegate job IDs;
- translate old IDs into delegate IDs;
- accept both `job_id` and `target` fields;
- accept both `source` and `target` watch fields;
- dual-write old and new public payloads;
- retain legacy delegate activation rows in new `job_list` output;
- add migration-only branches to model-facing tools.

Old transcripts and raw state files remain archival bytes, but the new runtime does not promise that their delegate job handles remain controllable or readable through job tools. Deployment must stop old active delegates before cutover. Tests use fresh state for the new contract.

## Verification

Before completion, the implementation must pass the repository's normal deterministic gates and prove the following behavior.

### Tool schemas and results

1. `delegate` and `delegate_send` never emit any delegate job-ID field.
2. `job_status` and `job_stop` require `target` and reject `job_id`.
3. `job_watch` create requires `target` and rejects `source`.
4. `job_status`, `job_stop`, and `job_list` return the common public identity field `id`; shell items do not duplicate it as `job_id`.
5. Shell targets retain their existing status, stop, output, and progress behavior.
6. No tool description or bundled prompt tells the model to retain a delegate job ID.

### Delegate lifecycle

7. Create, inline completion, background completion, live steering, idle resume, failure, exhaustion, cancellation, and restart preserve one stable `delegate_id`.
8. Concurrent resume attempts produce one activation.
9. Stopping an idle delegate is an explicit successful no-op.
10. Terminal finalization commits the full communicate packet before runtime unload.
11. A stopped or completed delegate can be restored from its durable transcript and descriptor.

### Listing and status

12. A delegate appears once in `job_list` after multiple activations.
13. Resume updates and reorders the stable delegate item without appending another item.
14. Delegate `status` reports running or idle; `last_outcome` reports the previous activation independently.
15. Status and type filtering obey the unified projection rules.
16. Nested and descendant views contain stable delegate items with correct ownership and depth.

### Notifications and results

17. A background delegate terminal notification names only `dlg_...` and carries no activation key.
18. The notification contains the complete final `communicate` call, including full output data and artifacts.
19. Inline completion does not produce a duplicate later notification.
20. Notification ordering and exactly-once delivery survive restart.
21. Observer callback delivery does not produce a duplicate owner notification.
22. The full prose and structured result remain readable from the child transcript after unload and restart.

### Watches

23. A delegate-target watch survives at least two idle unload/restore cycles and observes public events from both activations.
24. Delegate targets reject output and progress predicates.
25. Shell targets retain output/progress watches.
26. Clearing a delegate watch while its runtime is unloaded prevents future delivery.

### Runtime release and worktrees

27. A completed, failed, exhausted, cancelled, or stopped delegate releases shared-worktree occupancy.
28. A running delegate still blocks worktree removal.
29. Running descendant work still blocks removal after its idle ancestor unloads.
30. Runtime unload never removes an isolated lane, discards dirty files, or bypasses merge gates.
31. Restore reapplies sandbox policy and reports failures without deleting transcript history.

### Concurrency and durability

32. Race tests cover send versus stop, send versus finalization, unload versus watch delivery, notification versus restart, and concurrent resume.
33. Private activation keys never escape public JSON, text renderers, notifications, events, doctor reports, UI payloads, or prompts.
34. Fuzz and program tests cover malformed target prefixes, target-type dispatch, old-field rejection, and projection invariants.

## Acceptance Criteria

The design is complete when:

- shell executions are the only public `job_...` resources;
- delegates expose only `dlg_...` and `transcript_ref`;
- all lifecycle tools use neutral `target` where applicable;
- `job_list` shows one item per delegate;
- terminal delegate notifications carry the complete final `communicate` call;
- terminal delegates unload automatically and stop blocking worktree removal;
- lifetime delegate watches survive unload and resume;
- no backward-compatibility path remains;
- deterministic unit, integration, race, fuzz-seed, frontend, and scenario gates pass.
