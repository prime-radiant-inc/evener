# Parent-Watch Sidecar Simplification Design

Date: 2026-06-21
Status: proposed
Supersedes where conflicting: `docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md`, `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`
Builds on: `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md`

Prerequisite: this design assumes the agreed `communicate(end_turn:boolean)`
contract. If an implementation branch still exposes `await_reply`, replace it
before implementing the sidecar changes so observers have one result/narration
surface.

## Problem

The current observer-sidecar contract asks the parent to compose three concepts:

1. create a delegate,
2. create a `job_watch` on the parent session,
3. set `send.to` so the parent-owned watch delivers frames to the delegate.

That makes the parent say "watch session X and deliver to session Z" even though
the sidecar use case is always "the watcher receives the frames it asked for."
The public shape exposes routing machinery instead of the domain model.

The second half of the flow is also backwards. A normal delegate reports to its
caller with `communicate`, but observer sidecars were taught to report with
`delegate_send(to="caller")`. That creates a special upward callback path that is
harder for models to choose, easy to confuse with child-directed follow-up, and
inconsistent with the `communicate(end_turn)` turn contract.

The practical symptoms from fluency testing match the shape problem:

- models confuse `target` with the watched session and `send` with a result path;
- models omit or misuse `send.to` when they really want the current observer to
  receive frames;
- models use `delegate_send(to="caller")` for reports because the docs teach it,
  even though `communicate` is the normal reporting tool;
- the parent is tempted to poll after setting up a watch because the callback
  path is not visibly the observer's own final result.

The root cause is not a missing prompt prohibition. The API shape advertises too
many degrees of freedom for the common sidecar contract.

## Thesis

Make watch delivery watcher-owned:

- `delegate(watch_parent:true)` grants a child permission to observe its
  immediate parent.
- The child calls `job_watch(source:"parent")` when it wants frames from that
  parent.
- `job_watch` delivers matching frames to the session that created the watch.
- The observer reports findings and finals with `communicate`, using
  `end_turn:true` when the callback/result is complete.

This removes the model-facing "watch X and send to Z" composition. The runtime
can keep internal routing machinery, but the model sees a simple ownership rule:
watch a source, receive your own frames, communicate your result.

## Goals

- Make sidecar setup one explicit grant on `delegate`: `watch_parent:true`.
- Make `job_watch` describe the observed source, not the delivery target.
- Remove model-facing `send` from the sidecar path.
- Use `communicate` as the only observer-to-parent result path.
- Keep optional event filters for precision, but make a missing filter mean
  "deliver the watchable public events for this source."
- Preserve causal provenance and same-watch loop suppression.
- Keep `delegate_send` focused on parent-to-child delegate follow-up.
- Keep the system small enough to implement by reshaping existing watch delivery,
  not by adding a new observer runtime.

## Non-Goals

- No arbitrary cross-session watch graph.
- No parent-defined trigger DSL on `delegate`.
- No transitive "watch parent" grants.
- No unbounded transcript access.
- No compatibility period for old model-facing `job_watch.target`,
  `job_watch.send`, or observer `delegate_send(to="caller")` guidance.
- No new narration or no-op tool.

## Public Contract

### `delegate.watch_parent`

`delegate` gains one optional boolean parameter:

```json
{
  "task": "Watch my work. When you see the next successful read_file call for watch-trigger.txt, report WATCH_OBSERVED with communicate(end_turn:true).",
  "watch_parent": true
}
```

`watch_parent:true` grants only the spawned child permission to observe the
immediate parent session. It does not grant the child permission to observe the
parent's parent, siblings, unrelated sessions, or future descendants of the
parent. It does not grant `delegate`.

A child with this grant gets `job_watch` in its callable tool set even when it is
a leaf delegate. It still does not get `delegate` unless
`delegation_allowance > 0` and the role permits delegation.

### `job_watch.source`

`job_watch` replaces model-facing `target` with `source`.

For `operation:"create"`, `source` names what the watcher wants to observe:

| Source | Meaning | Authorization |
| --- | --- | --- |
| `self` | This session's public events | Always allowed |
| `parent` | Immediate parent session's public events | Allowed only with `delegate.watch_parent:true` |
| `job_...` | A concrete job's output/progress/events | Allowed when the job is owned by this session or a descendant session |

The runtime may internally keep a target field while the code is being reshaped,
but the model-facing schema, parser, errors, tool output, docs, and prompts use
`source`.

### Delivery

Delivery is implicit: matching frames are delivered to the watcher that created
the watch.

There is no model-facing `send` object for this path. The watcher does not name
another session as a delivery destination. A parent that wants a sidecar to watch
the parent grants `watch_parent:true` and lets the sidecar create its own watch.

The watch key becomes watcher-owned. Conceptually it is:

```text
(watcher_session_id, source_identity, condition_hash)
```

The implementation can keep an internal generation/send key as long as public
list/inspect/create results describe `source`, not `send.to`.

### Default Trigger

For cross-session session sources, omitting trigger fields means all bounded
public watch frames for the source:

```json
{
  "operation": "create",
  "source": "parent"
}
```

The frame stream is still bounded and event-shaped. It does not expose raw
private transcript state. Raw output matching remains explicit via
`output_match` on concrete job sources.

`source:"self"` keeps the structural loop guard for self-generated event kinds.
A self watch that would deliver `assistant.tool`, `communicate`, or wildcard
events back into the same session is rejected unless the implementation provides
a proven non-echo delivery path. This preserves the existing loop invariant
while keeping the sidecar path simple: `source:"parent"` is cross-session
delivery, so the watched session and watcher session differ.

Optional filters remain available:

```json
{
  "operation": "create",
  "source": "parent",
  "events": ["assistant.tool"],
  "event_filter": {
    "tool_name": "read_file",
    "status": "ok"
  }
}
```

`events`, `event_filter`, `every`, `output_match`, and
`progress_interval_ms` keep their existing semantics after being interpreted
against `source`. `event_filter` is precision, not the mechanism that makes a
watch valid.

### Observer Results

Observers report upward with `communicate`.

```json
{
  "message": "WATCH_OBSERVED read_file succeeded for docs/watch-trigger.txt.",
  "end_turn": true,
  "output": {
    "message": "WATCH_OBSERVED",
    "data": {
      "path": "docs/watch-trigger.txt",
      "tool": "read_file"
    },
    "artifacts": []
  }
}
```

For a watch-originated observer turn:

- `communicate(end_turn:false)` is visible narration/status and the observer can
  continue working;
- `communicate(end_turn:true)` is the observer result for that activation;
- when the observer turn was caused by a parent-source watch frame, terminal
  `communicate(end_turn:true)` resumes the parent as an `Observer callback`
  block containing the observer message and canonical output envelope;
- the later ordinary delegate terminal notification is suppressed when it would
  duplicate the delivered callback.

`delegate_send` remains for sending a message from a parent to one of its
delegates. It is not the observer-to-parent result path.

## Authorization Model

The default ancestry rule is directional:

- A session may observe itself.
- A session may observe its children and descendants when it can resolve the
  relevant child session or concrete descendant job.
- A child may observe its immediate parent only when spawned with
  `watch_parent:true`.
- That parent-watch grant is non-transitive.

Concrete job reads granted by a watch remain least-privilege. A sidecar that
receives a frame for a concrete job may read that watched job when the watch
grants the read, but does not gain general read access to the parent job store.

## Runtime Flow

### Parent-source sidecar

1. Parent calls `delegate(watch_parent:true, task=...)`.
2. Runtime spawns a child with:
   - `job_watch` exposed,
   - `delegate` still withheld unless independently allowed,
   - a non-transitive parent-watch install seam.
3. Child calls `job_watch(operation:"create", source:"parent", ...)`.
4. Runtime installs the watch against the parent event stream with the child as
   the watcher/receiver.
5. A matching parent event builds a bounded watch frame with watch provenance.
6. Runtime starts or steers the child with the frame.
7. Child evaluates the frame.
8. Child calls `communicate(end_turn:true, ...)` when it has a result.
9. Runtime resumes the parent with an `Observer callback` block carrying that
   result and marks the callback delivered for duplicate-notification
   suppression.

### Parent watching a descendant job

1. Parent calls `job_watch(operation:"create", source:"job_...")`.
2. Runtime resolves the job owner.
3. If the owner is the caller or a descendant, runtime installs the watch at the
   owner and records the caller as watcher/receiver.
4. Matching frames are delivered back to the caller through the existing durable
   watch mailbox/provenance path.
5. If the job belongs to a parent, sibling, or unrelated session, creation
   fails with `target_not_watchable`/`source_not_watchable` style guidance.

The implementation should prefer `source_not_watchable` in new errors. Existing
internal sentinel names may be renamed when the code is touched.

## Tool Prompting

The positive model-facing guidance should be:

- To create a sidecar observer, call `delegate(..., watch_parent:true)`.
- In the sidecar, call `job_watch(source:"parent")`.
- Narrow with `events` or `event_filter` only when useful.
- Use `communicate(end_turn:false)` for status while working.
- Use `communicate(end_turn:true)` for the observer result.
- Use `delegate_send(to=<delegate_id>)` only to send a message to your own child
  delegate.

Avoid "do not poll" or "do not use X" phrasing as the primary guidance. The
contract should make the successful path obvious.

## Compatibility

There is no compatibility mode for the old model-facing sidecar shape.

The implementation removes or rejects:

- `job_watch.target` in public tool arguments,
- `job_watch.send` in public tool arguments,
- `delegate_send(to:"caller")` in public tool descriptions and observer docs.

Runtime internals may temporarily reuse old watch-send storage or provenance
types, but that is not part of the public contract and must not leak into tool
schemas, prompts, or docs.

## Validation

Unit coverage must prove:

- `delegate` advertises and parses `watch_parent`.
- `watch_parent` leaf delegates receive `job_watch` but not `delegate`.
- `job_watch` advertises `source` and omits `target` and `send`.
- `job_watch` rejects legacy `target` and `send` arguments.
- `source:"parent"` fails without a parent-watch grant.
- `source:"parent"` succeeds with a parent-watch grant and delivers a frame to
  the child that created the watch.
- A watch-origin observer's `communicate(end_turn:true)` resumes the parent as
  the callback.
- The same activation does not also inject a duplicate terminal notification.
- Parent watches can resolve and install against descendant concrete jobs.
- An observer has no transitive permission to watch grandparent/sibling sessions.

Scenario coverage must prove model fluency:

- GPT and Kimi can create a parent-watch sidecar from the positive contract.
- The sidecar uses `job_watch(source:"parent")`.
- The sidecar reports with `communicate`, not `delegate_send(to:"caller")`.
- The parent resumes from the callback without polling.
- A broad default parent watch can be created without custom filters.
- A filtered watch can target successful `read_file` events without manual frame
  parsing in the parent.
