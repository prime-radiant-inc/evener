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

Each terminal record carries its private activation tag. Before an accepted communicate returns, Serf atomically persists `terminal_prepared` under that activation with a private delivery ID and tagged locator:

- accepted communicate: transcript/session ID, stable entry sequence, and tool-call ID;
- synthetic terminal error: transcript/session ID, stable entry sequence, and private synthetic-entry ID.

Notification replay resolves the result from this locator. It must never search for “the last communicate.” The locator is private metadata and is not a control handle.

`terminal_prepared` is the durable bridge between communicate acceptance and finalization. Restart folds a prepared real terminal before considering `runtime_lost`; it synthesizes `runtime_lost` only when the running activation has no prepared record.

`read_transcript` accepts the session ref through its session-read path. `job:<job_id>` names shell output only. It does not accept a delegate ID or private activation key.

## Terminal Result Model

Every activation settles durably exactly once. Ordinary task activations have one canonical outward terminal result after finalization. A model-bearing attention drive that succeeds without `communicate` uses the private no-action disposition defined below and creates no outward result.

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

Serf does not normalize custom output into the default envelope. `communicate` has one dedicated dispatch seam: it receives the original argument bytes and tool-call ID, enforces the fixed outer fields and 16 KiB bound, and preserves `output` as `json.RawMessage` or decodes with `UseNumber`. The generic registry does not enforce the custom `result_schema` before this handler. The handler validates custom output separately to produce metadata.

A syntactically accepted, bounded terminal call therefore ends the activation even when its custom output fails validation. Serf preserves the exact raw call and reports `structured_result_validation: {"valid": false, "reason": "..."}`; it does not drop the invalid value. Raw arguments preserve JSON field presence and numeric spelling, so explicit `null` remains distinct from an omitted required `output`, which fails fixed outer validation and does not end the activation. This is a communicate-only seam, not a second general tool-dispatch API.

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

### Quiet attention-drive disposition

A model-bearing notification/watch drive may successfully decide that no action is required and end without `communicate`. Serf then persists an activation-tagged private `completed_no_action` disposition. It records completion and releases the drive/unload barrier, but creates no terminal result, parent delivery intent, notification, or callback. If that activation does call terminal `communicate`, the ordinary canonical result and delivery protocol applies instead.

### Full-packet safety bound

A real terminal communicate notification is complete: no delegate-specific excerpting, truncation, or summarization is allowed. To make that implementable:

1. The maximum serialized raw `communicate` arguments are 16 KiB. The complete canonical lifecycle/operational wrapper is at most 8 KiB.
2. Delegate creation measures the parent's fixed system/tool content, conservative provider tokenization, output allowance, wrapper overhead, and minimum supported context window. It fails synchronously if one full packet cannot be reserved after compaction.
3. A parent-scoped packet-admission lock and generation cover every mutation of noncompactable request overhead: delegate creation and settlement, model/fallback/profile replacement, runtime tool registration/removal, and system-prompt, plugin, MCP, or tool-set changes. External resolution may occur outside the lock, but each operation remeasures and commits under the same generation. A mutation is rejected before commit if any running/resumable delegate or unsettled delivery requirement would no longer fit. Delegate creation persists the admitted bound and parent capacity requirement with the restore descriptor.
4. `communicate(end_turn=true)` serializes and validates the whole raw call before acceptance. An oversized call returns a typed `terminal_packet_too_large` tool error and does not end the activation; the child must retry with a smaller call or store large evidence as artifacts and reference it from the bounded call.
5. The parent continuation builder reserves the required capacity and compacts parent history before injection. It injects at most one full delegate terminal packet per continuation; additional intents remain armed for later turns. It never drains or concatenates more packets than the measured request can fit.

The same 16 KiB bound applies to default and custom-schema output. Every variable wrapper field has a canonical byte bound. Watch metadata uses a deterministic prefix whose canonical JSON is at most 2 KiB, followed by `watch_count` and `watches_truncated`; watch creation itself remains unlimited by this design. Worktree, diagnostic, model, sandbox, validation, and observer fields fit the remainder of the 8 KiB wrapper allowance. Serf-generated terminal errors use bounded sanitized diagnostics. This preserves the full-call guarantee without allowing a valid but permanently undeliverable packet. Exact larger evidence remains available through transcript or artifact references chosen by the child; Serf does not silently substitute those references.

### Inline result admission

A parent tool-call batch has one inline delegate-packet token. Before dispatch, at most one `delegate` or idle-starting `delegate_send` call may use positive `max_wait_ms` to return a full terminal result. Additional positive-wait calls still start their work but do not wait; they return a bounded result with `inline_result_deferred: true` and `defer_reason: "inline_packet_slot_unavailable"`.

If a deferred activation has already settled by result construction, the tool returns metadata-only `status: "idle"`, `last_outcome`, transcript ref, and `result_delivery: "notification_pending"`; it omits `terminal_result`. Its durable delivery intent remains armed and later emits the full packet through ordinary notification delivery. Provider protocols therefore receive one bounded result for every tool call while at most one tool result contains a full delegate packet.

After all sibling tool calls finish and before the parent tool-result turn is committed, Serf measures the complete provider-required batch: fixed prompt/tool content, output allowance, the selected delegate result, and every sibling result after each tool's existing output-limit policy. If the request still cannot fit after permitted compaction, Serf replaces the selected full delegate result with the same metadata-only deferral result and leaves its delivery intent unacknowledged and armed. It may reduce excerptable sibling results only within their existing contracts. The final persisted batch must be fit-tested as a whole.

The generic 20,000-character job-tool limiter must never truncate a full admitted `delegate` or `delegate_send` terminal result. Those two tools either bypass generic truncation after bounded admission or use a non-truncating limit at least as large as the maximum canonical inline serialization. Unrelated job tools retain their existing limits.

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

Observer readiness fields such as `watching` and the bounded watch projection, isolated-worktree path/branch/HEAD/ahead/dirty/disposal metadata, model/sandbox echoes, and existing warning fields remain present when applicable within the 8 KiB wrapper allowance. The identity cutover removes activation-ID fields only.

The result never contains `job_id`, `started_job_id`, `current_job_id`, `latest_job_id`, or `resumed_from_job_id`.

An activation that remains running when the tool returns is notification-armed. Inline observation does not settle that delivery intent until the parent transcript durably commits the tool result.

Inline waiting is also subject to the parent tool-batch's single packet token. A call denied that token follows the bounded deferral result defined above; it never embeds a second full terminal packet in the same tool-result turn.

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

That positive wait requires the same single inline-packet token. Without the token, `delegate_send` starts normally, returns the bounded deferral result, and leaves full-result delivery armed.

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

Shell targets preserve the existing expressive stop result:

- `id`, `type`, and current `status`;
- `previous_status`;
- `outcome`: `cancelled_by_request`, `already_terminal`, `completed_during_stop`, or `stop_requested`;
- reason and bounded-wait metadata.

`stopped` may remain only as a derived convenience boolean; callers use `outcome` to distinguish races.

Shell semantics remain unchanged. `include_children` is a shell opt-in.

Stopping a delegate is conversation/subtree control and always:

- durably closes a new subtree stop epoch before traversing descendants;
- requests cancellation of its active activation, if any;
- recursively requests cancellation of active descendant shell and delegate work;
- discards queued, undelivered steering from before the stop;
- durably closes the pre-stop attention wake gate so queued descendant attention cannot resurrect the coordinator.

Every descendant shell start, delegate create, idle resume, and steering delivery that could start work checks the inherited stop epoch before admission. Stop cancels and drains earlier-epoch work to a fixed point, so a concurrent spawn cannot escape after its branch was visited. This cascade occurs even if the target delegate is already idle. A later explicit `delegate_send` to a resumable delegate opens a new epoch and wake gate for that new activation; it does not revive discarded old-epoch attention. For delegate targets, `include_children: false` does not disable the mandatory cascade.

Delegate stop outcomes describe the whole subtree: `already_terminal` means the delegate was idle and no active descendant or queued earlier-epoch work required action; `stop_requested` means any affected work remains pending. Once all affected work settles, `cancelled_by_request` wins if any work settled because of the request; `completed_during_stop` applies only when none did. The bounded `cascade` summary reports the detailed mixture through requested, cancelled, independently completed, pending, and failed counts. Failure to persist any stop request returns a partial-failure error with that summary and leaves the closed epoch in force.

A positive `max_wait_ms` performs one bounded wait over subtree settlement. If settlement remains pending, terminal delivery intents stay armed. Stopping does not delete transcript, history, worktrees, branches, or files.

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

Every watchable public source event receives a private monotonically increasing source sequence and is appended durably before publication. Each owner-store watch record persists the last processed source cursor. For one occurrence, advancing the cursor, updating the `every` counter, and creating any delivery intent happen in one owner-store batch. The source sequence is private event identity, not a delegate activation ID or public control handle.

Watch create, replacement, runtime restore, live reattachment, and steady-state delivery use one strict per-source sequencer. Live publication only records the durable event and wakes that sequencer; it never mutates a watch predicate directly. The sequencer processes durable events contiguously (`cursor+1`, then `cursor+2`, and so on), waiting at gaps even if a later event publishes first. It atomically advances each watch's cursor/counter/delivery state before taking the next sequence.

Attachment samples and persists the source cursor/generation, attaches the wake route, and drives the same sequencer until it catches the live frontier. Events appended between sampling and attachment are processed in order; events concurrently observed live merely wake the sequencer and are deduplicated by sequence. The operation does not report `watching: true` until catch-up reaches the frontier. Process restart uses this same path.

The directly controlling owner's durable store is the inventory and routing authority for receiver-visible delegate watches. It stores the watch ID, delegate target, child descriptor/session ref, predicate state, and delivery state. `job_list` and `job_watch` list/inspect/clear use this index while the child is unloaded; they do not restore the child model runtime merely to manage a watch.

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

1. use the activation's durably prepared real terminal record; otherwise persist `completed_no_action` for a successful quiet attention drive, or append a canonical synthetic terminal-error record for an abnormal end;
2. persist the tagged locator and validation metadata for an outward result, or the private no-action disposition; create a delivery intent only for an outward result;
3. project `last_outcome`, lifecycle `idle`, and resumability;
4. durably request runtime unload;
5. if the descendant subtree is quiescent, unload now; otherwise retain only the existing coordinator runtime required for descendant routing and mark unload deferred;
6. queue inline, callback, or background delivery independently.

The dedicated raw-argument `communicate` handler is the linearization point for real results: it validates and bounds the original bytes, appends a dedicated activation-tagged terminal record with canonical arguments, validation, tool-call ID, and stable entry sequence through the transcript's durable/fsync path, and commits `terminal_prepared(locator, delivery_id)` before returning accepted or triggering callback state. Those writes are one durable acceptance transaction or an ordered idempotent protocol whose recovery cannot expose the transcript record without reconstructing `terminal_prepared`. Finalization only folds the prepared record. Serf must not publish a terminal result or release runtime state before the locator and delivery intent are durable.

### Descendant-aware runtime unload

This design does not detach descendants into a new supervisor. If active descendants exist, the ancestor's terminal model turn is over, but the current child session/job-manager shell remains reachable for:

- descendant listing and control routing;
- callback and notification drive-down;
- watch grants and event forwarding;
- worktree occupancy and safety scans.

Pure durable routing, indexing, and acknowledgement do not open an activation. Any descendant notification or callback that requires model processing atomically opens a normal serial private activation, changes lifecycle to `running`, acquires the ordinary drive slot, and holds the unload barrier. That activation may steer, call tools, or communicate only under normal activation supervision and finalizes through the same terminal protocol. Pending attention after restart may restore the model runtime only through this activation path; recordless model-bearing drive turns are forbidden.

The deferred ancestor may continue to block occupancy only to the extent the existing subtree or model-bearing drive activation requires it. When the last active descendant and any drive activation settle and queued attention is durably routed or acknowledged, Serf automatically completes the pending unload. No caller flag or close tool is required.

A quiescent unload uses a dedicated `unloadDelegateRuntime` transition, not `Session.Close`. It durably flushes and closes transcript/store/provider/runtime handles, releases environment locks and occupancy, and detaches the child from the live parent manager. It deliberately skips recursive child cancellation, isolation-lane disposal, `SessionEnd` filesystem cleanup, and deletion of descriptors, watches, outcomes, or delivery state. A normal root/session close retains its existing stronger teardown semantics.

### Restart

On root process restart, before lazy model restoration, Serf runs one durable post-order reconciliation pass over delegate descriptors and child session IDs. It opens child stores and transcripts without constructing model runtimes, first folds every activation-tagged `terminal_prepared` or `completed_no_action` record, then settles each remaining private activation recorded as running without a live runtime, folds descendant outcomes upward, and reapplies deferred-unload state. Specifically:

- a running activation with `terminal_prepared` completes from that exact real terminal record;
- a quiet drive with durable `completed_no_action` settles without delivery;
- only a running activation without `terminal_prepared` receives a canonical `terminal_error` with outcome `stopped` and reason `runtime_lost`;
- the exact synthetic-entry locator, outcome, and pending delivery are persisted;
- the delegate becomes idle;
- resumability is recomputed only from durable monotonic facts;
- descendant-aware unload rules are re-applied;
- pending deliveries and durable watch counters remain intact;
- later `delegate_send` restores only if resumable.

This one-shot reconciliation prevents a crashed grandchild from remaining permanently `running` merely because its parent runtime is unloaded. It is not a detached live supervisor. No public recovery flow uses a private activation key.

## Results, Delivery, and Notifications

### Durable delivery intent and acknowledgement

Every notification-armed activation has one durable delivery intent with a private `delivery_id` and exact terminal locator. Every receiver transcript entry created for an inline result, callback, or background notification stores that hidden ID as entry metadata; it is never rendered as a public handle. Delivery is at-least-once until receiver commit and exactly-once after commit through durable deduplication.

- Background notification settles only after `AppendDurable` commits the parent notification entry.
- Inline completion settles only after `AppendDurable` commits the parent initiating-tool result with the hidden delivery ID.
- Observer callback settles only after `AppendDurable` commits the parent callback entry with the hidden delivery ID.
- Steering-queue or daemon-queue persistence is not acknowledgement.
- On restart, any unsettled intent is replayed; the receiver indexes committed hidden delivery IDs before appending. Rendered target text is never used for deduplication.

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

The communicate arguments are complete and byte-for-byte equivalent after canonical JSON serialization. Serf performs no delegate-specific excerpting, truncation, or summarization. The byte-bounded notification wrapper also preserves applicable parent-generated operational metadata that the child cannot authoritatively report, including final isolated-worktree state and disposal hint, model/sandbox echoes, bounded observer readiness/watch projection, and structured-result validation. None of those fields may carry an activation ID.

An abnormal result injects the same lifecycle and operational wrapper with the canonical `kind: "terminal_error"` envelope instead of claiming a child call existed.

Shell notifications retain their bounded output-excerpt behavior.

Watch-origin observer callbacks may retain distinct outer `Observer callback` presentation markup, but the payload inside that markup is the same complete canonical terminal union and byte-bounded operational wrapper as every other delegate terminal delivery. The legacy formatter that accepts only normalized `message`/`output` prose is removed. Scalar, array, explicit-null, and schema-invalid custom output remains exact. Observer callbacks use the same durable acknowledgement rule and do not also emit a duplicate owner notification.

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
- durable `terminal_prepared` locator/delivery state;
- private `completed_no_action` dispositions for quiet drives;
- durable delivery intent/acknowledgement state;
- durable watch relationships, source-event cursors, and predicate counters;
- parent packet-admission requirement and generation;
- pending descendant-aware unload state.

Private activation records may remain in the existing job store. Public folds project them into their owning delegate and never list them as jobs.

The directly controlling owner store also indexes unloaded child delegate summaries, receiver-visible watches, typed lineage, and routing descriptors needed by list/status/watch/stop without a live child model runtime. It atomically batches watch cursor/counter/delivery transitions. Child stores remain canonical for child transcript, activation data, and durable source-event sequence; the owner index contains only bounded projection and routing metadata.

Full result content has one authoritative durable copy in the child transcript. The delivery intent stores a locator, not a second full copy. Bounded display metadata is allowed.

Shell records retain their behavior but adopt public `id` and typed parent projections.

## Authorization and Tree Visibility

Knowledge of an ID does not grant control. This work adds no new grant type.

| Operation | Shell target | Delegate target |
|---|---|---|
| list | Direct owner; visible descendants only when the list request enables existing `include_nested`/`include_descendants` scope | Direct owner; visible descendants only when the list request enables existing `include_nested`/`include_descendants` scope |
| status | Direct owner, or an ancestor resolving a known visible concrete descendant through the existing forwarding policy; no list flag is required | Direct owner, or an ancestor resolving a known visible concrete descendant through the existing forwarding policy; no list flag is required |
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

Public projections follow this matrix:

| Surface | Identity and lineage | Result content |
|---|---|---|
| `delegate` / `delegate_send` inline result | `delegate_id`, typed `parent` where applicable | Full canonical terminal union when observed inline, plus operational metadata |
| terminal notification / observer callback | `target: dlg_...`, typed `parent` in metadata | Full canonical terminal union, plus operational metadata |
| child transcript read | session `transcript_ref` | Authoritative full terminal records and activation history |
| `job_list` / `job_status` | neutral `id`, `type`, typed `parent` | Lifecycle, outcome, resumability, timings, validation summary, and bounded operational metadata only |
| AppWire / TUI / web / doctor | neutral `id`, `type`, typed `parent` | The same metadata-only stable delegate projection; no delegate `Turns` activation rows and no child session ID as a second public identity |
| public lifecycle events / audit | neutral `target`, `type`, typed `parent` | Lifecycle, outcome, reason, validation summary, and bounded operational metadata only |

Only inline delegate results, terminal delivery, and transcript reads carry the full terminal-result union. Metadata surfaces may carry its private-locator presence/status but never the private locator value or a second full-result copy. AppWire removes delegate activation rows and bumps its protocol version; older clients fail the existing handshake instead of decoding the breaking schema.

The identity, typed lineage, lifecycle, resumability, and operational metadata rules apply consistently to:

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

The new contract uses one versioned marker at the state-root/project boundary. Before any session create/restore/discovery, transcript read, mutation-store access, job-store access, hub retained-state read, or doctor read, Serf acquires a root-wide lock and validates the marker. It atomically creates the new marker only when the permitted root contains no durable Serf artifacts. An absent or different marker with any transcript, metadata, mutation, job, delegate, or session artifact is incompatible and rejected before read or mutation, including transcript-only roots and mixed per-session roots.

Deployment must stop the old process, archive or remove the old state root, and start the new runtime with fresh state. AppWire increments `ProtocolVersion`; old client/server pairs fail initialization through the existing mismatch contract.

This is intentional YAGNI: Serf will not add legacy folds, control aliases, read aliases, parent-link translation, transcript rewriting, dual writes, or migration branches.

Old state and transcripts may remain operator-managed archival bytes, but the new runtime does not load or promise API access to them. Tests must prove fresh creation plus refusal of unmarked, wrong-epoch, transcript-only, metadata-only, empty-log-with-other-artifacts, mixed-session, hub fallback, doctor, and old-AppWire-client cases. Historical transcript text is never re-rendered as live new-epoch control data.

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
8. The dedicated communicate dispatch seam preserves original bytes, tool-call ID, scalar/array/object/null output, and exact numbers; custom schema validation produces metadata instead of pre-handler rejection.
9. Failure, exhaustion, cancellation, panic recovery, and runtime loss append canonical terminal-error entries.
10. Real results persist entry/tool-call locators and synthetic results persist entry/synthetic-entry locators; replay never selects a later activation's result.
11. The communicate handler durably commits the activation-tagged canonical terminal record and `terminal_prepared(locator, delivery_id)` before returning accepted.
12. Accepted communicate packets are complete and never excerpted; admitted inline delegate results bypass generic truncation or use a non-truncating bound large enough for the canonical maximum.
13. Oversized packets are rejected before terminal acceptance and can be retried.
14. Every mutation of noncompactable request overhead—including delegate lifecycle, model/profile, runtime tools, system prompt, plugin, MCP, and tool-set changes—remeasures and commits packet capacity under one parent generation lock.
15. Concurrent model/tool/prompt mutation and delegate creation cannot commit against a stale packet-capacity generation.
16. Each continuation injects at most one full packet and leaves remaining intents armed.
17. A multi-tool batch returns at most one inline full delegate result; final construction fit-tests the complete sibling result batch and falls back to bounded metadata-only deferral while leaving delivery armed when necessary.
18. Inline, callback, and background delivery intents survive crashes until `AppendDurable` commits a parent entry carrying the hidden delivery ID.
19. Hidden-ID receiver deduplication prevents duplicate committed delivery and does not inspect rendered target text.
20. Metadata-only status does not consume pending delivery.
21. Observer callbacks preserve the full canonical raw union for scalar, array, null, and schema-invalid output and produce no duplicate owner notification.
22. Every terminal wrapper fits the 8 KiB allowance; its deterministic watch prefix fits 2 KiB and reports total/truncation without limiting watch creation.

### Lifecycle, stop, and resume

23. Create, steering, resume, completion, abnormal outcomes, and restart preserve one delegate ID.
24. Concurrent resume attempts create one activation.
25. Delegate stop closes a durable subtree epoch before traversal and always cascades through active descendants, including when the delegate is idle.
26. Spawn, shell start, resume, and delivery races cannot admit old-epoch work after stop; explicit later send opens a new epoch.
27. Stop results classify whole-subtree cancellation/races with deterministic mixed-result precedence and report requested, cancelled, independently completed, pending, and failed cascade counts.
28. Permanent disposal/pruning/exhaustion produces `resumable: false`; transient restore failure does not.
29. Send to a non-resumable delegate returns the durable typed error.

### Listing, visibility, and watches

30. A delegate appears once after multiple activations and is reordered on activity.
31. List responses retain count/total/offset plus watch, delegation, and occupancy orientation.
32. Nested/descendant views preserve typed lineage, depth, and per-operation authorization; status of a visible concrete descendant does not depend on list-only flags.
33. Visibility never grants delegate send authority.
34. The owner-store index lets `job_watch` list, inspect, and clear manage unloaded delegate watches without model restoration.
35. Delegate watches survive two unload/restore cycles and process restart.
36. Watchable source events are durable before publication and carry a private stable sequence.
37. Create, replace, restore, reattach, steady-state, and restart all use one per-source sequencer that processes durable events strictly in contiguous sequence order; out-of-order live publication cannot skip or repeat an `every` occurrence.
38. Delegate targets reject output/progress predicates; shell predicates retain behavior.
39. AppWire, events, doctor, TUI, and web use metadata-only stable delegate projections without activation rows or child-session identity aliases.

### Runtime and worktrees

40. A quiescent terminal delegate unloads and releases occupancy automatically through the dedicated non-disposing unload path, never `Session.Close`.
41. An ancestor with active descendants defers unload while preserving descendant routing, callback drive-down, listing, stop, watches, and blockers.
42. Pure routing remains activation-free; every model-bearing notification/callback drive opens a normal activation, and a quiet successful drive persists `completed_no_action` with no outward delivery.
43. Deferred unload completes automatically after descendants, drive activations, and queued attention become quiescent.
44. Root restart folds prepared real terminals and quiet-drive dispositions before synthesizing runtime loss, and settles lost grandchildren without constructing model runtimes.
45. Runtime unload never deletes lanes/files, runs close-time filesystem cleanup, or bypasses dirty/unmerged gates.
46. Disposal is monotonic and makes the delegate non-resumable.

### Cutover and robustness

47. The root-wide locked epoch marker is validated before every state entry point and created only for a truly fresh permitted root.
48. Runtime, hub, doctor, and transcript paths refuse unmarked, wrong-epoch, transcript-only, metadata-only, and mixed roots instead of translating them.
49. AppWire protocol mismatch rejects old clients and servers.
50. Race tests cover raw communicate dispatch/preparation, receiver commit/replay, model/tool/prompt mutation versus packet admission, whole-batch inline fallback, spawn/stop mixed outcomes, quiet model drive/unload, and out-of-order `N+1`/`N` watch publication during create/replace/reattach, steady state, and restart.
51. Fuzz/program tests cover malformed targets, dispatch, old-field rejection, typed lineage, terminal unions, packet/wrapper bounds, epoch admission, and projection invariants.

## Acceptance Criteria

The design is complete when:

- shell executions are the only public `job_...` resources;
- delegates expose one `dlg_...` identity plus transcript and operational metadata;
- all lifecycle tools use neutral targets;
- public nested lineage is typed and contains no activation key;
- `job_list` shows one item per delegate without losing supervision inventory;
- every outward-result activation has an exact real communicate or canonical terminal error, while a quiet attention drive uses private `completed_no_action`;
- accepted delegate communicate notifications are complete and receiver-safe;
- delivery survives crashes until durable parent acknowledgement;
- delegate stop safely controls its subtree;
- quiescent terminal delegates unload automatically, while active descendants defer unload without being orphaned;
- lifetime delegate watches retain exact predicate state across unload/restart;
- permanent non-resumability is explicit;
- the new runtime refuses incompatible state rather than carrying compatibility code;
- deterministic unit, integration, race, fuzz-seed, frontend, and scenario gates pass.
