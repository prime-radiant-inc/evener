# Job Control Open-Decision Fixes

Status: ready for implementation after adversarial review.

This spec resolves the product decisions left open by
`docs/superpowers/plans/2026-06-09-job-control-contract-cleanup.md` after the
Phase 6 cutover. It is intentionally narrow: implement the decisions below,
update the evergreen contract, and keep existing job-control behavior intact
except where the old implementation conflicts with these decisions.

## Decisions

1. **Watch-send busy delivery:** when a `job_watch` send fires but the target
   sidecar cannot accept delivery immediately, Serf coalesces frames per watch
   key and delivers the latest frame when the target is next idle/resumable.
   A frame is dropped only on hard/non-resumable failure, and that drop must
   produce a caller-visible diagnostic notification.
2. **Alias vocabulary:** v1 exposes only `caller` and `watched` as runtime
   aliases. `main` is not a v1 alias. Do not keep `main` as a synonym or
   backward-compatible alias.
3. **Structured result schema inheritance:** resumed delegate turns inherit the
   delegate session's original `result_schema`. If a resumed turn cannot
   produce a valid structured result, omit `structured_result` and emit
   `structured_result_valid:false` plus `structured_result_reason`.
4. **Sidecar marker/capacity:** v1 has no model-facing or semantic
   sidecar/private class and no separate sidecar capacity class. Observer
   sidecars use existing delegate/job capacity. Internal `FromWatch` bookkeeping
   may exist only for feedback-loop suppression; it must not affect capacity,
   notification visibility, listing, retention, or resumability.
5. **Runtime-lost delegate resume:** `stopped/runtime_lost` delegate jobs must
   be resumable after process restart when retained transcript/session state is
   sufficient. The current limitation that requires live child-session runtime
   bookkeeping is not acceptable.

## Non-Goals

- Do not add compatibility for `main`.
- Do not add a public `sidecar`, `private`, or observer-specific delegate
  parameter.
- Do not introduce polling loops as a substitute for bounded watch-send
  coalescing.
- Do not make job tools accept `transcript_ref`; `job_id` remains the job-control
  handle.
- Do not make shell jobs messageable while implementing delegate restore.
- Do not persist or reuse `SessionConfig.spawn` directly. Restore data must use a
  dedicated durable descriptor.
- Do not persist `job_watch` registrations as standing watches across restart in
  this cleanup. Once a watch has already fired, however, any busy pending
  watch-send frame is durable until delivered or diagnosed.

## Contract Updates

Update `docs/job-control.md` and the cleanup plan so the resolved items are no
longer marked open:

- A1/B2: document durable watch-send coalescing, latest-frame-wins behavior,
  retry on idle/resumable target, and diagnostic-on-hard-failure behavior.
- B1: remove `main` from v1 alias examples, target shapes, model-facing
  guidance, and observer examples. `main` is an unknown target and fails
  synchronously with `target_not_found` in `job_send_message`,
  `job_watch.target`, and `job_watch.send.to`. Do not preserve it through a
  fallback alias path.
- B3: state that resumed delegate turns inherit the original `result_schema`;
  document the guaranteed invalid-result shape with
  `structured_result_reason`.
- B4: state that v1 sidecars are ordinary delegate jobs created/composed by
  `delegate` + `job_watch`; no model-facing or semantic sidecar/private class,
  no public marker, and no separate capacity class.
- B7c: state that `stopped/runtime_lost` delegate jobs are expected to be
  resumable when retained transcript/session state is present, and describe the
  synchronous error when required retained state is missing or pruned.

The cleanup doc should retain provenance, but the checkboxes for A1, B1, B2,
B3, B4, and B7c should be checked only after implementation and tests land.

## Implementation Requirements

### 1. Remove `main` from alias resolution

Current touchpoints include:

- `agent/job_delegate.go` alias resolution for `job_send_message`
- `agent/job_watch.go` watch target/send target validation
- `docs/job-control.md`
- tool descriptions in `agent/internal/tool/definitions.go`
- prompt text and examples under `agent/prompts/` and bundled agent docs
- tests in `agent/job_watch*_test.go`, `agent/job_delegate_test.go`, and
  profile/tool-definition tests

Required behavior:

- `caller` remains the runtime-originating session alias.
- `watched` is valid only when Serf has an internal watch context for the
  current delivery. Outside that context it fails synchronously.
- `main` is not accepted by `job_send_message`, `job_watch.target`, or
  `job_watch.send.to`.
- No docs, prompts, or tool descriptions should suggest `main`.

Alias resolution table:

| Tool field | `caller` | `watched` | `main` |
| --- | --- | --- | --- |
| `job_send_message.target` from normal model/tool input | Send to the runtime-originating caller. In a root session this is the current session; in a delegate this is its parent/caller. | Fail synchronously with `target_not_found` because there is no watch context. | Fail synchronously with `target_not_found`; no compatibility synonym. |
| `job_send_message.target` from watch-originated sidecar context | Same as above. | Send to the concrete watched session/job carried by the watch context. If the context has no unique concrete watched target, fail with `target_not_found`. | Fail synchronously with `target_not_found`; no compatibility synonym. |
| `job_watch.target` from normal model/tool input | Watch the runtime-originating caller/current session. | Fail synchronously with `target_not_found`; `watched` cannot define a watch without already being inside one. | Fail synchronously with `target_not_found`; no compatibility synonym. |
| `job_watch.send.to` | Send the watch frame to the runtime-originating caller/current session. | Send the watch frame to the concrete watched session/job that triggered this watch. For wildcard watches, the watch frame carries the concrete trigger identity. If that identity is missing or ambiguous, emit one visible diagnostic and do not guess. | Fail synchronously at watch registration with `target_not_found`; no compatibility synonym. |

The internal watch context is not a public API field. It carries at least
`caller_session_id`, the concrete watched job/session identity, the triggering
event identity, and whether the target came from a wildcard watch. It exists
only to resolve `watched` and to suppress observer feedback loops.

Acceptance tests:

- `job_send_message(target:"main")` fails synchronously with
  `target_not_found`; it does not queue steering, call a parent steer callback,
  create a job, or create a run.
- `job_watch(target:"main", ...)` and `job_watch(send.to:"main", ...)` fail
  synchronously with `target_not_found`, register no watch, and leave no pending
  watch send.
- Direct `job_send_message(target:"watched")` and `job_watch(target:"watched")`
  outside watch context fail synchronously.
- `job_watch(send.to:"watched")` delivers to the concrete watched target for a
  concrete watch and for an unambiguous wildcard-triggered frame.
- Existing `caller` paths still pass.
- A grep for model-facing `main` alias examples is clean outside historical
  docs/specs:

```bash
rg -n 'caller|main|watched|target="main"|"main"' docs agent test tools cmd \
  | rg -v 'docs/superpowers/(specs|plans)/|CHANGELOG|/design/2026-|main session|package main|func main|GitBranch|src/main.go'
```

The grep is a review aid, not the only proof; do not delete unrelated ordinary
English/code uses of "main".

### 2. Persist and inherit delegate result schema

Current structured-result fields are persisted on job records/events, but the
original schema is not clearly part of resumed turn state.

Required behavior:

- The original `delegate.result_schema` is persisted in one canonical durable
  place: the delegate job's durable restore descriptor, written before launch
  and folded into the job record. Do not split schema authority between jobstore
  and child metadata. This task introduces the descriptor if it does not exist
  yet, with `result_schema` and enough identity fields to fold/reopen it; Task 4
  extends the same descriptor with the remaining restore metadata.
- `job_send_message` to a delegate job uses the original schema for the resumed
  turn.
- The foreground/background return shape and `job_read_output` expose:
  - `structured_result` only when validation/capture produced a valid value.
  - `structured_result_valid:true` when the result is present and valid.
  - `structured_result_valid:false` when schema validation/capture failed,
    the result exceeded persistence bounds, or the model failed to provide a
    valid structured result.
  - `structured_result_reason` with a stable machine-readable reason whenever
    `structured_result_valid:false` is emitted.

Durable shape:

- Add `result_schema` to the delegate restore descriptor persisted with the
  delegate's `job_started` durable state.
- Add `structured_result_reason` to `jobstore.Event`, `jobstore.JobRecord`,
  `delegateResult`, `sendMessageResult`, `job_read_output`, `delegate`, and
  `job_send_message` result projections.
- `structured_result_reason` is durable on terminal job events and folded job
  records. It must survive store reopen and process restore anywhere
  `structured_result_valid:false` is surfaced.
- Do not add `structured_result_error`; `structured_result_reason` is the single
  model-facing invalid-result reason field.
- `structured_result_reason` is omitted when `structured_result_valid` is true
  or unset.
- If no schema was requested and no structured result was captured, omit
  `structured_result`, `structured_result_valid`, and
  `structured_result_reason`. Prose-only delegates are not invalid.
- If a schema was requested and the child turn completes without captured
  structured output, persist `structured_result_valid:false` and
  `structured_result_reason:"schema_result_missing"`.
- Durable persistence failure and bounded tool-result projection are separate:
  durable records use `schema_result_too_large` only when the structured result
  exceeds `maxPersistedStructuredResultJSONBytes` after JSON marshaling, and
  `schema_capture_failed` when capture/marshal fails. If `delegate`,
  `job_send_message`, or `job_read_output` must omit an already-persisted valid
  structured result only to fit that tool response's max-chars bound, the
  durable record remains valid and the bounded projection emits
  `structured_result_valid:false` with
  `structured_result_reason:"projection_too_large"`.

Reason values:

- `schema_validation_failed`
- `schema_result_missing`
- `schema_result_too_large`
- `schema_capture_failed`
- `projection_too_large`

Acceptance tests:

- Delegate created with `result_schema`, then resumed with `job_send_message`,
  produces a valid structured result that is persisted and readable through
  `job_read_output`.
- A resumed turn with invalid/missing structured output returns no
  `structured_result`, sets `structured_result_valid:false`, and includes
  `structured_result_reason`.
- The inherited schema still applies after session restore where delegate
  resume is supported by Task 4 below.
- Existing oversized structured-result bounds still drop the value and mark it
  invalid with `structured_result_reason:"schema_result_too_large"` without
  corrupting the job record.
- A valid persisted structured result that is omitted only because a bounded
  tool response is too small emits `projection_too_large` in that response
  without changing the durable job record.
- Delegates without `result_schema` and without captured structured output omit
  all structured-result validity/reason fields.
- `agent/internal/jobstore` round-trip/fold tests prove `result_schema` and
  `structured_result_reason` survive reopen.

### 3. Implement watch-send coalescing

Current `job_watch.send` attempts immediate background delivery and emits a
diagnostic on failure. That is insufficient for the busy-sidecar steady state.

Required behavior:

- A watch send has a stable durable coalescing key:
  `(visible_session_id, watch_target, resolved_watched_identity,
  resolved_send_to, watch_generation)`.
  - For concrete job watches, `resolved_watched_identity` is that concrete job id.
  - For `*` and session-level event watches, `resolved_watched_identity` is the
    concrete event/job/session identity that caused the frame; it is mandatory,
    not optional.
  - `resolved_send_to` is the concrete job/session target after applying
    `caller`/`watched` alias rules.
  - Replacing or clearing a watch increments/removes the watch generation and
    deletes stale pending frames for the old generation.
- At most one pending frame exists per key. New frames replace the pending frame
  for that key; they do not append unboundedly.
- The pending frame records enough metadata to build the final sent message:
  configured message, bounded frame/excerpt, triggering job/session, trigger
  reason, watch context for alias resolution, and a coalesced/replaced count.
- Pending watch-send frames are durable. If the parent process restarts while a
  frame is pending, restore rehydrates it and later delivers it or emits exactly
  one hard-failure diagnostic.
- Pending frames are bounded both per key and per active watch generation. The
  default per-watch-generation cap is 32 pending keys. When a new distinct key
  would exceed the cap, Serf evicts the oldest pending key for that watch
  generation and emits one caller-visible diagnostic naming the evicted trigger.
- Busy classification is explicit: a watch-originated send (`FromWatch`) to a
  running delegate sidecar is treated as busy and must be queued/coalesced
  instead of steering the running child. Non-watch model calls to a running
  delegate keep the existing steering behavior.
- If delivery fails because the target is busy/running under that classifier,
  keep the pending frame and retry when the target becomes idle/resumable.
- If delivery fails because the target is unknown, pruned, non-messageable, or
  not resumable, drop the pending frame and enqueue a caller-visible watch
  diagnostic notification.
- Concrete-job terminal ordering remains: persist/enqueue/attempt final watch
  notification/send before arming the terminal notification when a concrete
  watched job reaches terminal state. If the sidecar is busy at that moment, the
  coalesced frame must remain pending and the terminal notification must still be
  delivered; actual sidecar commentary may arrive after the terminal
  notification.
- Watch-originated sends must retain the existing feedback-loop suppression
  (`FromWatch`) so sidecar lifecycle events do not retrigger their own watch.
- Pending coalesced frames are durable after a watch fires. Standing watch
  registrations are not restored as active watches in v1.
- Clean up pending frames on successful delivery, hard diagnostic, watch clear,
  watch replacement, concrete watch expiry, watched-target prune, and
  job-manager/session close.

Implementation notes:

- `agent/job_watch.go` is the likely home for pending send state and delivery
  attempts.
- Delivery retry must be event-driven and expose a deterministic retry hook.
  Invoke it when a delegate target completes a turn or otherwise transitions
  from running to terminal/resumable. The retry path must run before and
  independently of event-trigger filtering, so `FromWatch` loop suppression
  cannot block it. Tests call this hook directly; do not rely on sleeps/timers.
- Keep frame size bounds unchanged.
- Do not classify retryable vs hard failure by matching incidental error text.
  Use a typed/internal delivery classification such as `delivered`, `busy`, or
  `hard_failure`.
- Never call `jm.send`, enqueue notifications, append jobstore events, read job
  output, or enter child-session/subagent methods while holding `jm.mu` or a
  pending-frame mutex. Select/mutate pending state under lock, copy delivery or
  diagnostic work into a local slice, release locks, then perform delivery.

Acceptance tests:

- Busy sidecar unit path: fake `job_send_message` returns a named busy
  classification (`delegate_session_busy` or the codebase's equivalent internal
  busy value); no diagnostic is emitted while the frame is pending. Calling the
  deterministic retry hook after the fake target becomes idle delivers only the
  latest coalesced frame.
- Busy sidecar integration path: a real watch-originated send to a running
  delegate sidecar is not steered immediately; two frames arrive while the
  delegate is mid-turn, and only the latest frame is delivered when the delegate
  becomes idle/resumable.
- Wildcard isolation: frames from two different wildcard-triggered watched jobs
  use different pending keys and do not overwrite each other.
- Hard failure: pending frame to a non-resumable/pruned target is dropped and
  produces one caller-visible diagnostic.
- Terminal ordering: immediate deliverable final watch send happens before the
  terminal notification; if the sidecar is busy, the terminal notification is not
  lost and the pending sidecar frame remains queued.
- Feedback-loop suppression: a watch-originated sidecar completion does not
  retrigger the same watch.
- No unbounded queue growth: repeated frames for one key keep one pending entry.
- Overall pending cap: more than 32 distinct wildcard-triggered pending keys for
  one watch generation evicts oldest keys with one diagnostic per evicted key.
- Reconfigure/clear cleanup: replacing or clearing a watch removes pending frames
  for the old watch generation.
- Restore durability: a busy pending frame survives restore and later delivers,
  or emits exactly one hard-failure diagnostic if the target becomes
  non-resumable.

### 4. Extend delegate restore descriptors

This is the foundation for runtime-lost delegate resume. Current
`job_send_message` requires live child-session bookkeeping in
`s.subagents.get(childID)`, so after restore it can return
`target_not_resumable` even when durable records and transcript state exist.

Required behavior:

- Before launching a delegate, Serf writes a durable delegate restore descriptor
  into the job's `job_started` state. Task 2 may already have introduced this
  descriptor for `result_schema`; this task completes the descriptor for
  restore-based resume.
- The versioned descriptor contains:
  - child session id and transcript ref;
  - parent session id, parent job id, owner/visible session ids, and origin
    turn/tool-call ids needed for forwarded nested job visibility;
  - original task;
  - requested `agent_type`, requested model override, resolved profile id,
    resolved model, reasoning effort, agent name, and role prompt source needed
    to rebuild the child session consistently;
  - local execution working directory and local env policy for
    `execenv.LocalExecutionEnvironment`; v1 does not snapshot arbitrary process
    environment variables;
  - inherited `result_schema`;
  - subagent tool policy/grants if the original delegate launch used explicit
    grants.
- The descriptor is folded into `jobstore.JobRecord` and survives store reopen.
- Do not read from this descriptor yet except in tests; this task only makes the
  required state durable.

Acceptance tests:

- Delegate launch appends a descriptor before or with durable job start, and a
  reopened jobstore exposes it on the delegate job record.
- Descriptor fields match the original launch settings, working directory, env
  policy, result schema, parent/job linkage, and child transcript ref.
- Missing optional launch settings are represented as empty/omitted values, not
  defaulted to misleading non-empty values.

### 5. Add strict child restore preflight

Required behavior:

- Given a stopped/runtime_lost delegate job, strict preflight validates the
  descriptor and retained child state before any new job event is appended.
- Child meta file must exist and parse.
- Child transcript file must exist, its header/session id must match the
  descriptor, and it must be readable enough to build resume history.
- Descriptor parent/session/job linkage must match the parent job record being
  resumed.
- The resolved profile/model must be available through the normal profile
  resolver.
- Non-tail transcript corruption is a hard `target_not_resumable` failure.
  Existing trailing-partial-line recovery may still apply if the transcript
  reader already supports it.
- Any failed preflight returns a synchronous error string with the exact shape
  `target_not_resumable:<not_resumable_reason>` before appending a new job
  event. The prefix is the stable tool error class; the suffix is one of the
  required reason values below. Do not return prose-only variants for these
  cases.
- `job_list` projects restored resumability: `resumable:true` for
  restore-resumable `stopped/runtime_lost` delegate jobs and `resumable:false`
  with `not_resumable_reason` when retained state is missing, corrupt, pruned, or
  lacks the delegate restore descriptor.

Required `not_resumable_reason` values:

- `missing_child_session_meta`
- `corrupt_child_session_meta`
- `missing_child_transcript`
- `corrupt_child_transcript`
- `transcript_session_mismatch`
- `missing_delegate_resume_metadata`
- `pruned_child_session_state`
- `child_session_busy`
- `parent_linkage_unavailable`
- `profile_unavailable`

Acceptance tests:

- Missing child meta returns
  `target_not_resumable:missing_child_session_meta` and creates no job/run.
- Missing child transcript returns
  `target_not_resumable:missing_child_transcript` and creates no job/run.
- Corrupt child transcript returns
  `target_not_resumable:corrupt_child_transcript` and creates no job/run.
- Bad descriptor linkage returns
  `target_not_resumable:parent_linkage_unavailable` and creates no job/run.
- Unavailable profile/model returns `target_not_resumable:profile_unavailable`
  and creates no job/run.
- Missing delegate restore descriptor returns `target_not_resumable` with
  `missing_delegate_resume_metadata` and creates no job/run.
- Before/after jobstore counts prove failed preflight paths create no partial
  job/run.
- `job_list` reports `resumable:true` for retained-state delegates and
  `resumable:false` plus `not_resumable_reason` for missing/corrupt/pruned
  retained state.

### 6. Reconstruct idle child runtime after restore

Required behavior:

- Restore-based resume reconstructs the minimum child-session runtime state from
  the descriptor and retained child session metadata. It must not rely on
  `SessionConfig.spawn` having been persisted.
- Reconstruction is lazy. `RestoreSessionFromMetaWithConfig` and job
  reconciliation must not launch delegates, spend tokens, or enqueue a child
  turn by themselves.
- The reconstructed child session has retained transcript history, original
  parent/caller linkage, profile/model/reasoning settings, working directory,
  local env policy, and inherited result schema.
- The reconstructed child is tracked as an idle retained delegate session so the
  normal resume path can launch the next turn.

Acceptance tests:

- Restart restore itself does not call the model adapter and does not create a
  new delegate job.
- Reconstructed runtime has the expected profile id, model, reasoning effort,
  working directory, local env policy, parent session id, parent job id, and
  result schema.
- No old delegate task is submitted as a new turn during reconstruction.

### 7. Resume runtime-lost delegates from retained state

Required behavior:

- After `RestoreSessionFromMetaWithConfig`, a delegate job that was reconciled
  as `stopped/runtime_lost` and passed strict preflight can be resumed with
  `job_send_message(target:<job_id>, message:...)`.
- The resumed turn creates a new durable delegate job record with a new `job_id`,
  the same `transcript_ref`, and `resumed_from_job_id` in the
  `job_send_message` result. The old job remains `stopped/runtime_lost`.
  `job_read_output` on the old id continues to read the old output; the new id
  reads the resumed turn output.
- Resuming from retained state must not re-run old child turns or replay old
  tool calls. It appends a new turn to the retained child conversation.
- No auto-resume: restart reconciliation must not resume delegates by itself.
  Resume occurs only through `job_send_message`.

Acceptance tests:

- Create a delegate job, ensure its child transcript/session metadata is
  retained, simulate process restart via `RestoreSessionFromMetaWithConfig`,
  reconcile the job as `stopped/runtime_lost`, then call
  `job_send_message(target:<job_id>)`. The child receives the new message and
  produces a new result.
- The restored `Session` is fresh from disk only: before `job_send_message`, the
  test asserts the restored subagent manager has no retained child runtime entry.
- The response includes a new `job_id`, `action:"resumed"`, and
  `resumed_from_job_id` equal to the old runtime-lost job.
- The restored resume preserves original agent type, requested/resolved model,
  reasoning effort, profile id, working directory, and local env policy.
- The restored resume inherits `result_schema` and applies the validation
  contract from Task 2.
- The first model request after restore is caused by `job_send_message`, includes
  retained transcript history, uses the new message as the fresh input, and does
  not submit the original delegate task as a new turn.

### 8. Remove sidecar open-decision caveats

After implementing Tasks 1-7, update the contract and cleanup doc:

- B4 is resolved as YAGNI: ordinary delegate/job capacity only, no
  model-facing or semantic sidecar/private class, no public marker.
- Remove prior deferred-language about a private-sidecar marker.
- Keep any implementation notes about watch-origin bookkeeping as internal
  behavior, not model-facing API.

Acceptance checks:

- `docs/job-control.md` no longer says alias/schema/sidecar/runtime-lost resume
  behavior is open.
- The cleanup doc marks A1, B1, B2, B3, B4, and B7c checked with a short
  disposition and commit reference.
- A fresh adversarial read finds no contradiction between the contract and
  shipped behavior.
- A grep/check rejects public `sidecar`, `private`, or observer-capacity
  parameters in tool definitions, prompts, and `docs/job-control.md` outside
  historical specs/plans and internal implementation notes.

## Validation

Run, at minimum:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go test ./agent/internal/jobstore -run 'Test.*Delegate|Test.*Structured|Test.*Restore' -count=1
go test ./agent -run 'Test.*Watch|Test.*Delegate|Test.*JobSend|Test.*Structured|Test.*Restore' -count=1
go test ./agent ./agent/internal/jobstore ./agent/internal/tool ./appwire ./internal/appprojector ./cmd/serf-hub ./cmd/serf-tui/internal/msgrender ./cmd/serf-tui/internal/toolsummary -count=1
make build
make test
make lint
```

Also run the Phase 6 cutover token gate to ensure no removed public subagent
surface leaked back in:

```bash
rg -n 'spawn_agent|resume_agent|close_agent|cancel_agent|list_agents|subagent_output|subagent-notification|DefSpawnAgent|DefSendInput|DefWait|DefCloseAgent|DefCancelAgent|DefListAgents|DefSubagentOutput|rootOnlyAgentManagementTools|SUBAGENT_START|SUBAGENT_END|EventSubagentStart|EventSubagentEnd|SubagentStartData|SubagentEndData|NotifySerfSubagent|SerfSubagentInfo|SubagentStatusInfo' \
  | rg -v 'docs/superpowers/(specs|plans)/|docs/job-control\.md|CHANGELOG|/original-attractor-specs/|/design/2026-'
```

Also run a targeted alias-surface gate over model-facing docs, prompts, and tool
definitions. This gate must find no active v1 `main` alias examples outside
historical specs/plans and ordinary code words:

```bash
rg -n 'caller[[:space:]]*\|[[:space:]]*main[[:space:]]*\|[[:space:]]*watched|caller`, `main`, `watched|"?(target|to)"?[[:space:]]*[:=][[:space:]]*"main"|"main".*alias|alias.*"main"' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/|main session|package main|func main'
```

Also run sidecar/open-decision model-facing gates. These must find no active
public marker/capacity escape hatches and no stale "open decision" language:

```bash
rg -n 'private-sidecar|private sidecar|sidecar.*capacity|capacity.*sidecar|implementation-specific policy.*sidecar|sidecar.*implementation-specific policy' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/'

rg -n 'Open decision|open decision|deferred/open decision|remains open|not yet normative' \
  docs/job-control.md agent/internal/tool agent/prompts docs/tools \
  | rg -v 'docs/superpowers/(specs|plans)/'
```

## Review Checklist

- No backward-compatible `main` alias remains.
- Alias resolution follows the table above.
- Watch-send coalescing is bounded and latest-frame-wins.
- Watch-send pending frames are durable across restore.
- Watch-send hard failures are caller-visible diagnostics.
- Watch-send busy classification is typed/internal, not error-string matching.
- Resumed delegates inherit the original `result_schema`.
- Invalid structured results have `structured_result_reason`.
- Runtime-lost delegate resume creates a new job with `resumed_from_job_id`.
- Runtime-lost delegate resume works after restore when retained state exists.
- Missing retained state fails synchronously and cleanly before creating a job.
- Restore/reconstruction does not spend tokens or replay old turns.
- No model-facing or semantic sidecar/private marker/class was added.
- Contract, tool descriptions, prompts, and tests describe the same v1 API.
