# Delegate messaging: one channel up, one channel down

Status: Implemented. Approved by Jesse 2026-10-10 (approach A of three considered; the alternatives are recorded at the end). Merged PRs:

- #4244 journal records updates
- #4246 frames name their kind
- #4248 a delegate doesn't wake again for attention it saw (#3725)
- #4274 and #4284 attention stop/clear follow-ups
- #4252 delegate guidance
- #4268 `communicate(end_turn=false)` delivers updates (closes #4172)
- #4294 drain recovery
- #4295 compaction keeps pending attention turns
- #4299 `delegate_send` reaches only your own delegates
- #4305 steering provenance removed
- #4307 parent steers labelled from the parent

The live micro-test PR (PR 8) was skipped.

## Why

Sessions in a delegate tree talk to each other through overlapping, partly broken channels:

- A delegate has two unrelated ways to reach its parent. `communicate(end_turn=true)` is a durable, labeled report that wakes the parent. `delegate_send(to="caller")` is raw steering text with no sender, which fails with `target_busy` when the parent is an idle delegate. The second was removed in June (`48bc17be8c`) and restored in August (`78342bbd30`); the system prompt never mentions it, and `explorer` delegates don't have the tool at all.
- `communicate(end_turn=false)` from a delegate reaches only the delegate's own transcript, yet returns `accepted:true` and is described as for messages the caller should see (#4172).
- A caller message reaches the root as an unlabeled user-role steering turn, and a parent's message to a child carries `SteeringSource "user"`. Neither side can tell who spoke.
- A caller message queued when the root calls `communicate(end_turn=true)` is drained into the result's `inbox` and nothing acts on it. A caller message in a saved queue does not wake the root at attach.

The full map of mechanisms, history, overlaps and gaps was gathered on 2026-10-10 against `origin/main` `bf0941d4fd`.

## Decisions

1. `communicate` is the only way to reach your parent. `end_turn=false` is an update the parent receives; `end_turn=true` is the final report. (Jesse)
2. `delegate_send` addresses only your own delegates. `to="caller"` is removed. (Jesse)
3. Delegates send an update only when the parent needs to know something or act, never as narration. (Jesse)
4. An update wakes an idle parent. A parent waiting on its delegates is idle (the root rests with `end_reason: waiting_on_work`), so queue-only delivery would hide mid-work messages until the delegate finished. (Jesse approved the design carrying this.)
5. No backward-compatibility code. (Jesse)

## Tool contracts

### `communicate(message, end_turn, end_reason?)`

- **Delegate, `end_turn=false`:** an update to the parent. It is recorded in the delegate's transcript, as today, and delivered to the parent. The result reports `delivered` (handed to the parent's attention) or `queued` (the parent is between turns and will receive it on its next turn). It does not fail because the parent is idle.
- **Delegate, `end_turn=true`:** the final report. Unchanged.
- **Root:** unchanged. `end_turn=false` is a checkpoint to the human; `end_turn=true` with `end_reason` ends the turn.
- **Description:** states both delegate cases plainly and, for delegates, the update rule (decision 3).

### `delegate_send(to, message, max_wait_ms?)`

- `to` accepts only a `dlg_…` id the caller owns. `to="caller"` returns an error whose one sentence points at `communicate(end_turn=false)`.
- The description says it is for your own delegates.
- Behavior toward children is unchanged: steer a running child, start an idle one, optionally wait on that start.

### `delegate(...)`

Unchanged, including `watch_parent` observers. Observer guidance (tool descriptions, role text) says: send updates with `communicate(end_turn=false)`, finish with `communicate(end_turn=true)`.

### Prompts

- The system prompt's Reporting section gains a short delegate line: an update reaches your parent and may wake it, so send one only when the parent needs to know something or act.
- `internal/bundled/agents/subagent.md` drops its caller-route text.
- Tool descriptions and prompt text are pinned only by whole-output goldens.

## Delivery

### Updates ride the report's attention path

A delegate's terminal report already travels as typed attention: a durable record in the receiver's attention store, a labeled `<delegate-notification>` frame, wake rules, consumed markers and replay after restart (`docs/job-control.md` attention and restart sections). An update becomes a second kind of attention on that same path. `SteerCaller` (`agent/delegate_tree_steer.go`) and the `to="caller"` branch of the stable delegate send tool are deleted; nothing else may carry upward delegate messages.

This one change fixes the unlabeled message, the `target_busy` refusal (attention queues for an idle parent delegate), the restart wake (attention replays), and the swallowed inbox (attention is not drained into a finished turn's `inbox`).

### Frame

The parent sees the existing frame with a kind:

```
<delegate-notification delegate_id="dlg_…" name="…" kind="update">…message…</delegate-notification>
<delegate-notification delegate_id="dlg_…" name="…" kind="report">{packet JSON}</delegate-notification>
```

The report frame keeps its current body. The update body is the message text. Quiet-watchdog notices keep their current form.

### When it arrives

| Parent state | Behavior |
|---|---|
| Idle | Woken. The notification turn takes everything pending at once, so several updates become one turn. |
| Running | Injected at the next safe model boundary. |
| Awaiting its human's `ask_user` reply | Held until the reply, as reports are today. |

### Order

One delegate's updates reach the parent in the order sent, and always before that delegate's report from the same generation.

### Parent to child

A message `delegate_send` delivers is labeled as from the parent: its steering source is `parent`, not `user`. The child's model sees a short attribution line before the text; clients label it "From parent" (web and phone use the shared steering-label table in `appwire-client/typescript`).

### Activity and clients

The delegate's summary in Activity (`reportPreview`) still comes only from the terminal report (`docs/product/session-activity.md`). An update appears in the parent's transcript as its own row. Clients must render a `kind="update"` notification legibly; where today's renderer would show it as a report, it gets the smallest change that labels it an update. Any wire-shape change carries a `cmd/evener-tui` test case and goes through the recorded producers in `agent/testdata/subagentwire`.

## Errors

- An update that cannot be delivered (the parent is gone, the delegate was stopped, the parent route is closed) still succeeds as a `communicate` call: the update is already in the delegate's transcript. The result says it was not delivered and why, so the delegate is never blocked on it.
- `delegate_send(to="caller")` is rejected with the pointer described above.

## Migration

None. Caller messages already sitting in a steering queue drain as they do today, because the steering queue itself stays. Old transcripts render as written. Removing `SteerCaller` deletes code; nothing is translated.

## Testing

Real sessions below the model boundary, with a scripted provider:

- An update reaches an idle root and wakes it; reaches a running root at the next boundary; queues for an idle parent delegate and arrives on its next turn; is held while the root awaits `ask_user`.
- Updates arrive in order and before the report; several pending updates become one notification turn.
- A pending update survives a restart and wakes its parent.
- The frame carries `delegate_id`, `name` and `kind`.
- A `delegate_send` message arrives at the child labeled as from the parent.
- `delegate_send(to="caller")` errors with the pointer.
- An undeliverable update returns a not-delivered result and does not fail the call.
- Tool descriptions and prompt sections are pinned by goldens; wire changes by the recorded subagent producers and a TUI case.

An optional live micro-test (opt-in, outside default tests): a delegate on a cheap model that needs an answer from its parent mid-work gets one.

## Docs

Updated in the same change: `docs/job-control.md` (`delegate_send`, the caller route, observers, attention), `docs/product/session-activity.md` (updates visible in the transcript, the report stays the summary), `docs/product/subsystems.md` rows S11 and S12, and `docs/subagent-management/11-delegate-resource-model.md` (the caller route).

## Closes

#4172. PR #4197 is closed in favor of this design. Also fixes, from the 2026-10-10 map: the idle-parent-delegate refusal, the missing upward channel for roles without `delegate_send`, the restart wake for caller messages, unlabeled caller and parent messages, and caller messages swallowed into a finished turn's `inbox`.

## Out of scope

- #3725 (completed delegates reactivating on attention their generation already showed the model). It needs its own ruling against the 2026-08-29 design.
- Replacing `delegate_send` with a `to` parameter on `communicate`. Jesse asked for that analysis after this lands.
- Sibling-to-sibling messages, human-to-delegate messages, delegate-to-human messages. None exist today.

## Alternatives considered

- **B, minimal:** as A, but keep `delegate_send(to="caller")` as an alias for `communicate(end_turn=false)`. Rejected: two names for one thing.
- **C, one `message` tool** (`to: parent | dlg_… | human`) replacing `communicate` and `delegate_send`. Rejected for now: it rewrites the result tool every session depends on and reopens `end_turn` and `end_reason`. Its narrower form is the follow-up analysis above.
