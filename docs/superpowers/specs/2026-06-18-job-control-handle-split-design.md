# Job-Control Handle Split Design

Date: 2026-06-18
Status: draft for Jesse review
Builds on: `docs/job-control.md`, `docs/superpowers/specs/2026-06-08-job-control-design.md`, `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md`

## Problem

Observer sidecars are currently composed from `delegate` + `job_watch` +
`job_send_message`. That composition works mechanically, but the model-facing
API makes one handle do two different jobs:

- a delegate `job_id` names one concrete invocation/output record;
- the same `job_id` is also accepted as the follow-up target for the durable
  delegate conversation behind that invocation.

That ambiguity caused session `01KVC8AY93KVKJ8MA134A8AHJN` to go off the rails:
the caller treated an observer delegate job as a resident actor, chased resumed
job ids, installed duplicate watches, tried polling, and used an agent type that
did not have the tools required for observation.

The root issue is not that delegates complete. Serf is intentionally turn-based:
delegates complete one job and later resume in the same delegate conversation.
The API should make that lifecycle visible and make the wrong target type
structurally invalid.

## Goals

- Keep the primitive count low.
- Preserve the turn-based runtime and drive-down delivery model.
- Make observer sidecars remain ordinary composition, not a special runtime
  class.
- Make durable delegate conversation handles distinct from concrete job handles.
- Keep watch lifecycle inspectable and clearable by explicit handle.
- Make invalid handle use fail synchronously with actionable guidance.

## Non-goals

- Do not add `observe`, `observer_report`, observer-comment, or sidecar-specific
  tools.
- Do not add launch-time watch sugar to `delegate`.
- Do not keep backward-compatible message targeting by delegate `job_id`.
- Do not make `transcript_ref` a job-control target.
- Do not make `job_id`, `delegate_id`, or `watch_id` bearer capabilities.
- Do not add a bare wildcard watch target in v1.

## Model-Facing Handles

Serf exposes three control handles with distinct prefixes and meanings:

| Handle | Prefix | Meaning | Accepted by |
| --- | --- | --- | --- |
| `delegate_id` | `dlg_` | Durable delegate conversation. This is the thing to message. | `delegate_send.to`, `job_watch.send.to` |
| `job_id` | `job_` | One concrete job/turn/output record. This is the thing to read, stop, or watch. | `job_read_output`, `job_stop`, `job_watch.target` |
| `watch_id` | `watch_` | One owner-scoped watch registration. This is the thing to clear or inspect. | `job_watch` management |

`delivery_id` names one fired watch delivery and is used for dedupe/debugging.
It is globally unique, or the pair `(watch_id, delivery_id)` is treated as the
dedupe key if implementations choose per-watch delivery ids.

`transcript_ref` remains an archival read handle only. It is never accepted as a
control target by job tools.

None of these handles is a bearer secret. Authority is checked from the caller's
runtime identity, owner/delegate-tree relationship, and explicit grants. A
session that merely learns a handle string does not gain control authority.

### Durable Delegate Identity

`delegate_id` is minted when `delegate` creates a durable delegate
conversation. It is persisted independently from any concrete job record, and
every delegate job turn links back to that `delegate_id`.

The persisted delegate row records:

- `delegate_id`;
- child session id and `transcript_ref`;
- owner session id, visible session id, and parent delegate id if nested;
- agent type and resumability status;
- `current_job_id`, nullable when no concrete job is active;
- `latest_job_id`, nullable only before the first job record is durable;
- a monotonic delegate generation used for stop-gating pending deliveries.

On restart, running concrete jobs reconcile to `stopped/runtime_lost`.
Delegate rows remain as recovery handles. They are resumable only if the
delegate transcript/session restore preflight succeeds; otherwise `job_list`
reports them as `not_resumable` with `current_job_id = null` and the last known
`latest_job_id`.

If a delegate conversation is being driven in-memory before a concrete job
record exists, `job_list` reports `status = "driving"`,
`current_job_id = null`, and the previous `latest_job_id`. Messaging still
steers into that single in-flight drive; it must not start a concurrent job.

### Durable Watch Identity

`watch_id` is minted from a durable watch registry, not from a process-local
counter. The persisted watch row records:

- `watch_id` and generation;
- owner session id and visible session id;
- normalized target;
- normalized delivery route;
- config hash;
- delivery counters and recent delivery ids;
- pending delivery ids linked to the watch.

`watch_id` inspect/clear requires the owner identity and current generation.
Stale ids fail synchronously. Clearing a watch marks that generation inactive
and drops unsent pending deliveries for that watch.

## Tool Shape

### `delegate`

`delegate` creates a new durable delegate conversation and starts its first
concrete job.

```json
{
  "task": "Observe this work and comment only when useful.",
  "agent_type": "default",
  "max_wait_ms": 0
}
```

Response:

```json
{
  "delegate_id": "dlg_...",
  "started_job_id": "job_...",
  "current_job_id": "job_...",
  "latest_job_id": "job_...",
  "type": "delegate",
  "status": "running",
  "running_in_background": true,
  "timed_out": false,
  "transcript_ref": "local:..."
}
```

`delegate` only creates. It does not accept `delegate_id`, `job_id`,
`transcript_ref`, or `target` as inputs.

`started_job_id` is the concrete job created by this call. It remains present
even if `max_wait_ms` lets the job finish before the tool returns.
`current_job_id` means the active job if one is running. When the delegate is
idle, `current_job_id` is null in list/status views. `latest_job_id` is the most
recent concrete job for the delegate, whether active or complete.

### `delegate_send`

`delegate_send` replaces model-facing `job_send_message`. The old name is
misleading because messages go to durable delegates or reserved runtime routes,
not to jobs.

```json
{
  "to": "dlg_...",
  "message": "Continue, but keep the API smaller.",
  "on_idle": "start",
  "max_wait_ms": 120000
}
```

Allowed targets:

- `dlg_...`: a durable delegate handle visible and controllable by the caller.
- `caller`: a reserved runtime route available from delegate/watch-delivered
  contexts. It resolves to the owner conversation that launched the delegate or
  installed the watch.

Rejected targets:

- `job_...`: `invalid_request: job_id is a job/turn handle; send messages to delegate_id`.
- `transcript_ref`: archival read handles are not control targets.
- `main`: not a v1 alias.
- `watched`: not a direct messaging target.

Actions:

- `steered`: message entered the delegate's currently running job; no new job
  was created.
- `started`: message started the next job in the same durable delegate
  conversation. The response includes `started_job_id`, `current_job_id`, and
  `latest_job_id`.
- `delivered`: message delivered to a runtime route such as `caller`; no
  delegate job was created.

`on_idle` values:

- `fail` (default): if the delegate is idle/resumable, fail synchronously
  instead of starting a new job.
- `start`: if the delegate is idle/resumable, start the next job.

`delegate_send` must be atomic against delegate state. A result may say
`started` only if a new job was actually created. If the delegate is busy and a
bounded wait expires, return a typed timeout/failure rather than pretending a
reply was observed.

If a delegate conversation is running or being driven, `delegate_send` steers
into that single in-flight turn. It may return `started` only when no run/drive
is in flight and a new concrete job was durably created.

`max_wait_ms` applies only when `delegate_send` starts a new job. If the target
is running and the message is steered, or if the target is `caller`, the tool
returns after delivery and includes `wait_ignored_reason` when a positive wait
was supplied.

`delegate_send(to="caller")` is a contextual capability, not a public alias.
Top-level/root use is rejected. A delegate can send only to its immediate
caller route; it cannot select arbitrary ancestors or siblings. Watch delivery
to `caller` uses the watch owner's notification rail, not delegate steering.

### `job_watch`

`job_watch` remains the standing-trigger tool. It creates, lists, inspects, and
clears watches; it does not create delegates. It uses a discriminated schema:

- `operation = "create"` requires `target`, event condition, and delivery
  route.
- `operation = "list"` takes optional filters and does not take `target`.
- `operation = "inspect"` requires `watch_id`.
- `operation = "clear"` requires `watch_id`.

Watch targets:

- `job_...`: one concrete job.
- `caller`: this owner conversation's own session events.

`job_watch.target` does not accept `delegate_id`. To watch a delegate's current
work, use its `current_job_id`; future delegate jobs require a new watch. A
bare wildcard target is not part of v1.

Watch delivery targets:

- `send.to = "dlg_..."`: deliver frames to a durable delegate conversation.
- `send.to = "caller"`: deliver frames to the watch owner.

`send.to` does not accept `job_id`. The `watched` alias is removed for v1; watch
frames carry explicit `target_type`, `target_id`, trigger metadata, and bounded
excerpts. They do not carry `transcript_ref` unless the recipient already has
transcript-read authority by some non-watch mechanism.

Every active watch returns a `watch_id`. Clearing uses only that handle:

```json
{
  "operation": "clear",
  "watch_id": "watch_..."
}
```

No clear-by-target or tuple reconstruction is model-facing. `watch_id` is
owner-scoped and generation-bearing; only the watch owner can inspect or clear
it.

`job_watch(operation="create")` behaves as an upsert for the same owner and the
same normalized configuration. The equality key is target, event set, `every`,
`output_match`, progress interval, delivery route, delivery message, excerpt
options, and any future filters. Duplicate setup returns the existing stable
`watch_id`; any materially different payload/configuration creates a distinct
watch unless the caller clears the old one first.

### Existing Job Tools

`job_read_output(job_id)` and `job_stop(job_id)` continue to operate on concrete
jobs, not durable delegates. If passed a `delegate_id`, they fail with guidance
to use `current_job_id` or `latest_job_id` from `job_list`.

`job_id` is not a bearer secret. Reads and stops are authorized by caller
identity, ownership, delegate-tree control, and explicit watch-granted read
permissions. A session that merely learns a `job_id` does not gain read or stop
authority.

`delegate_send(to="dlg_...")` is authorized by caller identity,
owner/delegate-tree control, and explicit runtime route grants. `job_watch`
with `send.to = "dlg_..."` requires the watch owner to have send authority to
that delegate.

`job_list` exposes enough state for recovery:

```json
{
  "delegates": [
    {
      "delegate_id": "dlg_...",
      "status": "running",
      "current_job_id": "job_...",
      "latest_job_id": "job_...",
      "transcript_ref": "local:...",
      "resumable": true,
      "parent_delegate_id": null
    }
  ],
  "jobs": [
    {
      "job_id": "job_...",
      "type": "delegate",
      "delegate_id": "dlg_...",
      "status": "running"
    }
  ],
  "watches": [
    {
      "watch_id": "watch_...",
      "target": "job_...",
      "send_to": "dlg_...",
      "deliveries": 2
    }
  ],
  "recent_watches": []
}
```

`job_list` is the recovery surface for all job-control handles. It reports
delegates visible to the caller as `running`, `driving`, `idle`, `stopped`, or
`not_resumable`. `current_job_id` is non-null only for a concrete active job.
`latest_job_id` is ordered by durable jobstore event sequence and remains
available after completion or runtime-lost reconciliation. Nested delegates are
shown when the caller owns or controls the relevant delegate tree.

## Observer Sidecar Recipe

Observer sidecars remain ordinary composition:

1. Start the observer:

   ```text
   delegate(task="Watch this work and report to caller only when useful.")
   -> delegate_id=dlg_obs, current_job_id=job_obs
   ```

2. Attach a watch:

   ```text
   job_watch(operation="create", target=job_target, events=[...], send.to=dlg_obs)
   -> watch_id=watch_obs
   ```

3. The observer comments back:

   ```text
   delegate_send(to="caller", message="...")
   ```

The model never needs to update a watch to the observer's resumed job id because
the watch delivery target is the observer's durable `delegate_id`; the watch
target remains the observed concrete `job_id`.

## Delivery And Runtime Invariants

Watch sends to delegates append/coalesce durable pending delivery addressed to
the target delegate conversation. The target renders that frame in its own turn,
driven by normal owner/parent loop boundaries. Watch delivery must not mutate a
foreign session directly from the observation path and must not introduce a flat
scheduler.

If the target delegate is already running or being driven, watch delivery steers
into that single in-flight turn. If the target delegate is idle and resumable,
watch delivery may start the next job unless the delegate stop gate is closed.

Pending watch sends carry trigger generation/time. A pending send created before
a deliberate stop must not resurrect a stopped delegate; only explicit new model
work can clear that stop gate.

Each delegate has a durable stop generation. Every pending watch send records
the delegate generation observed at creation. `job_stop(current_job_id)` closes
the stop gate when that job is the delegate's current job. Drains must drop or
permanently suppress pending sends created at or before the closed generation,
and watch deliveries created while the gate is closed must not start a new
delegate job. `delegate_send(to="dlg_...", on_idle="start")` may explicitly
start new work and open a later generation, but it must not re-enable stale
pre-stop pending sends. To stop an idle delegate conversation in v1, clear its
watches; `job_stop` remains a concrete-job tool.

Observer read grants are keyed to the observer's durable delegate/session
identity and the concrete watched `job_id`. They grant snapshot
`job_read_output` only. They do not grant list, stop, message, subtree, future
run, or transcript authority.

## Error Guidance

The contract relies on sharp, actionable errors:

- `delegate_send(to="job_...")`:
  `invalid_request: job_id is a job/turn handle; send messages to delegate_id dlg_...`
  when Serf can name the associated delegate.
- `job_read_output("dlg_...")`:
  `invalid_request: delegate_id is a conversation handle; read output from job_id`
- `job_stop("dlg_...")`:
  `invalid_request: delegate_id is a conversation handle; stop a concrete job_id`
- `job_watch(target="dlg_...")`:
  `invalid_request: delegate_id is not watchable; watch current_job_id`
- Any use of `main`, `watched` outside historical/internal contexts, or
  `transcript_ref` as a control target fails synchronously.

## Migration

This is a contract cutover, not a compatibility layer. Model-facing
`job_send_message` is replaced by `delegate_send`; delegate `job_id` is no
longer accepted as a follow-up target.

Historical transcript rendering may still display old `job_send_message` calls
as archival data, but no model-facing tool or control path accepts the old
contract.

Docs, prompts, tool descriptions, and examples must be updated to the blunt
rule:

> Message delegates. Read, stop, and watch jobs. Clear watches by watch id.

## Why This Prevents The Incident

The failed observer run treated an ephemeral job handle as the address of a
resident observer. This design makes that mistake invalid:

- watches deliver to `delegate_id`, not to the observer's current `job_id`;
- resumed observer turns create new `job_id`s without changing the watch
  delivery target;
- duplicate watches are visible and clearable by `watch_id`;
- `job_id` cannot be used for messaging, so the model cannot chase resumed job
  ids as if they were actors;
- the observer reports through the same delegate messaging route, without a new
  observer-only primitive.

The primitive set stays essentially the same. The API becomes safer by cutting
ambiguous aliases and splitting handles by lifecycle role.
