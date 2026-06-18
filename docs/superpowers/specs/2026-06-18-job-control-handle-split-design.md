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
- Do not make `job_id` a bearer capability for read or stop authority.

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
  "current_job_id": "job_...",
  "type": "delegate",
  "status": "running",
  "running_in_background": true,
  "timed_out": false,
  "transcript_ref": "local:..."
}
```

`delegate` only creates. It does not accept `delegate_id`, `job_id`,
`transcript_ref`, or `target` as inputs.

`current_job_id` means the active job if one is running. When the delegate is
idle, `current_job_id` is null in list/status views. If callers need the most
recent completed job, `job_list` may expose `latest_job_id`; that is separate
from `current_job_id`.

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
  conversation. The response includes a new `current_job_id`.
- `delivered`: message delivered to a runtime route such as `caller`; no
  delegate job was created.

`on_idle` values:

- `start` (default): if the delegate is idle/resumable, start the next job.
- `fail`: if the delegate is idle/resumable, fail synchronously instead of
  starting a new job.

`delegate_send` must be atomic against delegate state. A result may say
`started` only if a new job was actually created. If the delegate is busy and a
bounded wait expires, return a typed timeout/failure rather than pretending a
reply was observed.

`max_wait_ms` applies only when `delegate_send` starts a new job. If the target
is running and the message is steered, or if the target is `caller`, the tool
returns after delivery and includes `wait_ignored_reason` when a positive wait
was supplied.

### `job_watch`

`job_watch` remains the standing-trigger tool. It creates, lists, and clears
watches; it does not create delegates.

Watch targets:

- `job_...`: one concrete job.
- `caller`: this owner conversation's own session events.
- `*`: owner-visible jobs/events only. It is never global.

`job_watch.target` does not accept `delegate_id`. To watch a delegate's current
work, use its `current_job_id`; future jobs require a new watch or an explicitly
owner-scoped wildcard/filter design later.

Watch delivery targets:

- `send.to = "dlg_..."`: deliver frames to a durable delegate conversation.
- `send.to = "caller"`: deliver frames to the watch owner.

`send.to` does not accept `job_id`. The `watched` alias is removed for v1; watch
frames carry explicit `target_type`, `target_id`, trigger metadata, and any
bounded excerpt/transcript reference needed by the observer.

Every active watch returns a `watch_id`. Clearing uses only that handle:

```json
{
  "watch_id": "watch_...",
  "clear": true
}
```

No clear-by-target or tuple reconstruction is model-facing. `watch_id` is
owner-scoped and generation-bearing; only the watch owner can inspect or clear
it.

`job_watch` should behave as an upsert for the same owner, target, condition,
and delivery target: duplicate setup returns the existing stable `watch_id`
instead of creating another live watch. A materially different configuration
creates a distinct watch unless the caller explicitly clears the old one first.

### Existing Job Tools

`job_read_output(job_id)` and `job_stop(job_id)` continue to operate on concrete
jobs, not durable delegates. If passed a `delegate_id`, they fail with guidance
to use `current_job_id` or `latest_job_id` from `job_list`.

`job_id` is not a bearer secret. Reads and stops are authorized by caller
identity, ownership, delegate-tree control, and explicit watch-granted read
permissions. A session that merely learns a `job_id` does not gain read or stop
authority.

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
      "resumable": true
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

## Observer Sidecar Recipe

Observer sidecars remain ordinary composition:

1. Start the observer:

   ```text
   delegate(task="Watch this work and report to caller only when useful.")
   -> delegate_id=dlg_obs, current_job_id=job_obs
   ```

2. Attach a watch:

   ```text
   job_watch(target=job_target, events=[...], send.to=dlg_obs)
   -> watch_id=watch_obs
   ```

3. The observer comments back:

   ```text
   delegate_send(to="caller", message="...")
   ```

The model never needs to update a watch to the observer's resumed job id because
the watch targets the observer's durable `delegate_id`.

## Delivery And Runtime Invariants

Watch sends to delegates append/coalesce durable pending delivery addressed to
the target delegate conversation. The target renders that frame in its own turn,
driven by normal owner/parent loop boundaries. Watch delivery must not mutate a
foreign session directly from the observation path and must not introduce a flat
scheduler.

Pending watch sends carry trigger generation/time. A pending send created before
a deliberate stop must not resurrect a stopped delegate; only explicit new model
work can clear that stop gate.

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
  `invalid_request: delegate_id is not watchable; watch current_job_id or an owner-scoped wildcard`
- Any use of `main`, `watched` outside historical/internal contexts, or
  `transcript_ref` as a control target fails synchronously.

## Migration

This is a contract cutover, not a compatibility layer. Model-facing
`job_send_message` is replaced by `delegate_send`; delegate `job_id` is no
longer accepted as a follow-up target.

Docs, prompts, tool descriptions, and examples must be updated to the blunt
rule:

> Message delegates. Read, stop, and watch jobs. Clear watches by watch id.

## Why This Prevents The Incident

The failed observer run treated an ephemeral job handle as the address of a
resident observer. This design makes that mistake invalid:

- watches deliver to `delegate_id`, not to the observer's current `job_id`;
- resumed observer turns create new `job_id`s without changing the watch target;
- duplicate watches are visible and clearable by `watch_id`;
- `job_id` cannot be used for messaging, so the model cannot chase resumed job
  ids as if they were actors;
- the observer reports through the same delegate messaging route, without a new
  observer-only primitive.

The primitive set stays essentially the same. The API becomes safer by cutting
ambiguous aliases and splitting handles by lifecycle role.
