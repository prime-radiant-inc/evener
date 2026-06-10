# Job Control Open-Decision Fixes

Status: ready for implementation.

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
   `structured_result_valid:false` plus a machine-readable error/reason.
4. **Sidecar marker/capacity:** v1 has no public/private sidecar parameter and
   no separate sidecar capacity class. Observer sidecars use existing
   delegate/job capacity.
5. **Runtime-lost delegate resume:** `stopped/runtime_lost` delegate jobs must
   be resumable after process restart when retained transcript/session state is
   sufficient. The current limitation that requires live child-session runtime
   bookkeeping is not acceptable.

## Non-Goals

- Do not add compatibility for `main`.
- Do not add a public `sidecar`, `private`, or observer-specific delegate
  parameter.
- Do not introduce polling loops as a substitute for durable watch-send
  coalescing.
- Do not make job tools accept `transcript_ref`; `job_id` remains the job-control
  handle.
- Do not make shell jobs messageable while implementing delegate restore.

## Contract Updates

Update `docs/job-control.md` and the cleanup plan so the resolved items are no
longer marked open:

- A1/B2: document watch-send coalescing, latest-frame-wins behavior, delivery
  retry on idle/resumable target, and diagnostic-on-hard-failure behavior.
- B1: remove `main` from v1 alias examples, target shapes, model-facing
  guidance, and observer examples. State that unknown aliases fail
  synchronously with `target_not_found` or `target_not_messageable` according
  to the existing target-resolution error model.
- B3: state that resumed delegate turns inherit the original `result_schema`;
  document the guaranteed invalid-result shape.
- B4: state that v1 sidecars are ordinary delegate jobs created/composed by
  `delegate` + `job_watch`; no public marker and no separate capacity class.
- B7c: state that `stopped/runtime_lost` delegate jobs are expected to be
  resumable when retained transcript/session state is present, and describe the
  synchronous error when required retained state is missing or pruned.

The cleanup doc should retain provenance, but the checkboxes for A1, B1, B2,
B3, B4, and B7c should be checked only after implementation and tests land.

## Implementation Plan

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
- `watched` remains valid only where watch context can resolve a concrete
  watched target/session. For wildcard watches, `watched` must resolve to the
  concrete event/job/session that triggered the frame when that is unambiguous;
  otherwise delivery fails with a visible diagnostic rather than guessing.
- `main` is not accepted by `job_send_message`, `job_watch.target`, or
  `job_watch.send.to`.
- No docs, prompts, or tool descriptions should suggest `main`.

Acceptance tests:

- `job_send_message(target:"main")` fails synchronously.
- `job_watch(target:"main", ...)` and `job_watch(send.to:"main", ...)` fail
  synchronously.
- Existing `caller` and `watched` paths still pass.
- A grep for model-facing `main` alias examples is clean outside historical
  docs/specs:

```bash
rg -n 'caller\\|main\\|watched|target=\"main\"|\"main\"' docs agent test tools cmd \
  | rg -v 'docs/superpowers/(specs|plans)/|CHANGELOG|/design/2026-|main session|package main|func main|GitBranch|src/main.go'
```

The grep is a review aid, not the only proof; do not delete unrelated ordinary
English/code uses of "main".

### 2. Persist and inherit delegate result schema

Current structured-result fields are persisted on job records/events, but the
original schema is not clearly part of resumed turn state.

Required behavior:

- The original `delegate.result_schema` is persisted with the delegate job or
  child-session metadata in a durable location available after restore.
- `job_send_message` to a delegate job uses the original schema for the resumed
  turn.
- The foreground/background return shape and `job_read_output` expose:
  - `structured_result` only when validation/capture produced a valid value.
  - `structured_result_valid:true` when the result is present and valid.
  - `structured_result_valid:false` when schema validation/capture failed,
    the result exceeded persistence bounds, or the model failed to provide a
    valid structured result.
  - `structured_result_error` or `structured_result_reason` with a stable
    machine-readable reason. Prefer a single string reason unless the codebase
    already has a structured error convention.

Recommended reason values:

- `schema_validation_failed`
- `schema_result_missing`
- `schema_result_too_large`
- `schema_capture_failed`

Acceptance tests:

- Delegate created with `result_schema`, then resumed with `job_send_message`,
  produces a valid structured result that is persisted and readable through
  `job_read_output`.
- A resumed turn with invalid/missing structured output returns no
  `structured_result`, sets `structured_result_valid:false`, and includes the
  reason field.
- The inherited schema still applies after session restore where delegate
  resume is supported by Task 4 below.
- Existing oversized structured-result bounds still drop the value and mark it
  invalid without corrupting the job record.

### 3. Implement watch-send coalescing

Current `job_watch.send` attempts immediate background delivery and emits a
diagnostic on failure. That is insufficient for the busy-sidecar steady state.

Required behavior:

- A watch send has a stable coalescing key. Use the existing watch identity:
  `(visible_session_id, target, send.to)` plus the triggering job/session identity
  if needed to avoid merging unrelated wildcard frames.
- At most one pending frame exists per key. New frames replace the pending frame
  for that key; they do not append unboundedly.
- The pending frame records enough metadata to build the final sent message:
  configured message, bounded frame/excerpt, triggering job/session, trigger
  reason, and a coalesced/replaced count if useful for diagnostics.
- If delivery fails because the target is busy/running and cannot accept another
  turn, keep the pending frame and retry when the target becomes idle/resumable.
- If delivery fails because the target is unknown, pruned, non-messageable, or
  not resumable, drop the pending frame and enqueue a caller-visible watch
  diagnostic notification.
- Concrete-job terminal ordering remains: flush queued watch notification/send
  before the terminal notification when a concrete watched job reaches terminal
  state. If the sidecar is busy at that moment, the coalesced frame must remain
  pending and the terminal notification must still be delivered; the pending
  frame is delivered later or diagnosed on hard failure.
- Watch-originated sends must retain the existing feedback-loop suppression
  (`FromWatch`) so sidecar lifecycle events do not retrigger their own watch.

Implementation notes:

- `agent/job_watch.go` is the likely home for pending send state and delivery
  attempts.
- Delivery retry should be event-driven where possible: when a delegate target
  completes a turn, transitions from running to terminal/resumable, or when
  job manager/session notification processing runs. Avoid timers unless there
  is already a retry mechanism suitable for this.
- Keep frame size bounds unchanged.

Acceptance tests:

- Busy sidecar: two matching frames arrive while the sidecar is mid-turn; only
  the latest coalesced frame is delivered when the sidecar is idle/resumable.
- Hard failure: pending frame to a non-resumable/pruned target is dropped and
  produces one caller-visible diagnostic.
- Terminal ordering: terminal notification for the watched job is not lost even
  when a pending sidecar frame remains queued.
- Feedback-loop suppression: a watch-originated sidecar completion does not
  retrigger the same watch.
- No unbounded queue growth: repeated frames for one key keep one pending entry.

### 4. Resume runtime-lost delegates from retained state

This is the largest fix. Current `job_send_message` requires live child-session
bookkeeping in `s.subagents.get(childID)`, so after restore it can return
`target_not_resumable` even when durable records and transcript state exist.

Required behavior:

- After `RestoreSessionFromMetaWithConfig`, a delegate job that was reconciled
  as `stopped/runtime_lost` and has retained child transcript/session metadata
  can be resumed with `job_send_message(target:<job_id>, message:...)`.
- Resume reconstructs the minimum child-session runtime state needed to launch
  the next delegate turn:
  - child/session id and transcript ref;
  - original delegate agent type/profile/model/reasoning settings;
  - working directory/environment snapshot required for the child session;
  - inherited `result_schema`;
  - parent/job linkage so new run records and nested events remain visible to
    the parent.
- The resumed turn creates a new durable delegate run/job record or updates the
  prior delegate job according to the existing job_send_message lifecycle
  contract. Choose the behavior already used for terminal-but-live delegate
  resume and make restore-based resume match it.
- If retained child metadata or transcript files are missing/pruned/corrupt,
  fail synchronously with `target_not_resumable` and a precise reason; do not
  create a partial job.
- Resuming from retained state must not re-run old child turns or replay old
  tool calls. It should append a new turn to the retained child conversation.
- No auto-resume: restart reconciliation must not resume delegates by itself.
  Resume occurs only through `job_send_message`.

Likely touchpoints:

- `agent/job_delegate.go`
- `agent/subagents.go`
- `agent/session_init.go` restore path
- `agent/schema` session metadata if additional persisted fields are needed
- child transcript/session persistence helpers
- jobstore record/event fields if the original delegate parameters are not
  already durable enough to reconstruct a child runtime

Acceptance tests:

- Create a delegate job, ensure its child transcript/session metadata is
  retained, simulate process restart via `RestoreSessionFromMetaWithConfig`,
  reconcile the job as `stopped/runtime_lost`, then call
  `job_send_message(target:<job_id>)`. The child receives the new message and
  produces a new result.
- The restored resume preserves original agent type/model/reasoning/profile
  settings and working directory.
- The restored resume inherits `result_schema` and applies the validation
  contract from Task 2.
- Missing child transcript/session metadata returns `target_not_resumable`
  without creating a job/run.
- Restart restore itself does not spend tokens or launch the delegate before
  `job_send_message` is called.

### 5. Remove sidecar open-decision caveats

After implementing Tasks 1-4, update the contract and cleanup doc:

- B4 is resolved as YAGNI: ordinary delegate/job capacity only, no public marker.
- Remove "private-sidecar marker remains open" language.
- Keep any implementation notes about watch-origin bookkeeping as internal
  behavior, not model-facing API.

Acceptance checks:

- `docs/job-control.md` no longer says alias/schema/sidecar/runtime-lost resume
  behavior is open.
- The cleanup doc marks A1, B1, B2, B3, B4, and B7c checked with a short
  disposition and commit reference.
- A fresh adversarial read finds no contradiction between the contract and
  shipped behavior.

## Validation

Run, at minimum:

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go test ./agent -run 'Test.*Watch|Test.*Delegate|Test.*JobSend|Test.*Structured|Test.*Restore' -count=1
go test ./agent ./agent/internal/jobstore ./appwire ./internal/appprojector ./cmd/serf-hub ./cmd/serf-tui/internal/msgrender ./cmd/serf-tui/internal/toolsummary -count=1
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

## Review Checklist

- No backward-compatible `main` alias remains.
- Watch-send coalescing is bounded and latest-frame-wins.
- Watch-send hard failures are caller-visible diagnostics.
- Resumed delegates inherit the original `result_schema`.
- Invalid structured results have a stable machine-readable failure reason.
- Runtime-lost delegate resume works after restore when retained state exists.
- Missing retained state fails synchronously and cleanly.
- No public sidecar/private marker was added.
- Contract, tool descriptions, prompts, and tests describe the same v1 API.
